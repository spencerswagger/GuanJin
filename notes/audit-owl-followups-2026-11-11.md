# #88 事后审（Owl）三条跟进（2026-11-11）

结论先行：**代码不 revert**（新口径实测比旧口径更严），本次只补"证明增益"的守线 + 把段首取前驱的
SQL 从"单列"收成"行值"，并给外层查询补上分区裁剪谓词。三条都不放宽任何判据。

---

## P1-1 补一条"区分新旧"的守线（`TestAnchorHeadDetectsSealedTailDeletion`）

**旧用例的毛病**：`TestAnchorHeadUsesRealPredecessor` 的删除动作发生在"封锚之后又追加了一条"的形态上，
旧口径在那一刻也会红（追加使得锚点≠真实前驱），所以它只证明了"不是恒绿"，**证不到新口径的增益**。

**新守线的形态**（就是 Owl A/B 的那一种，旧口径的真空档）：

1. 上月写 2 条 → 复核通过 → 封锚 ⇒ **`anchor[M-1]` 恰好等于上月最后一条的 tag**；
2. 封锚之后**不再追加**上月记录；
3. 次月写 1 条（`prev_tag` 指向上月最后一条）；
4. **删掉上月最后一条**。

| 口径 | 段首期望值 | 结果 |
|---|---|---|
| 旧（取 `anchor[M-1]`） | 仍是被删那条的 tag，与次月首条 `prev_tag` **相等** | `broken=nil` ⇒ **漏检** |
| 新（取链上真实前驱） | 退化成被删记录的前一条 | 报 `prev_tag_mismatch` |

**A/B 实测**（把段首逻辑临时改回旧口径，其余不动）：本用例 **红**
（`封锚尾巴被删后次月复核仍报通过 —— 段首判据没走真实前驱（旧口径的漏检形态）`）；
恢复新口径后 **绿**。⇒ 这条守线**只在旧口径下红**，可以当"增益"的证据复用。

## P1-2 `TailTagBefore` 排序口径从单列改行值（+ 回归用例 `TestAnchorHeadHandlesCreatedAtTie`）

链序是 `(created_at, id)`，而 `created_at` **没有唯一约束**：`Append` 自己会抬 `created_at`
（链尾 +1µs）所以不会并列，但**非 `Append` 写入**（回灌 / 归档恢复 / 批量导入）会。
一旦上月最后一条与次月首条 `created_at` 相同，旧 SQL 的 `created_at < ?` 会把前者排除，
期望值落到更早一条 → 又冒出 `prev_tag_mismatch` **假告警**。

```sql
-- 旧：单列比较（并列时漏掉"同 created_at、id 更小"的那条）
WHERE created_at < COALESCE((SELECT min(created_at) ...), 'infinity')
-- 新：行值比较，链序是什么就按什么取；CTE 取本段首条，段内无记录 → 零行 → found=false
WITH head AS (SELECT created_at, id FROM audit_logs
               WHERE occurred_at >= ? AND occurred_at < ?
               ORDER BY created_at ASC, id ASC LIMIT 1)
SELECT a.integrity_tag FROM audit_logs a, head
 WHERE a.occurred_at < ?                                  -- P2-1 分区裁剪
   AND (a.created_at, a.id) < (head.created_at, head.id)
 ORDER BY a.created_at DESC, a.id DESC LIMIT 1
```

顺带把 `min()`+`COALESCE('infinity')` 那套 NULL 兜底去掉（CTE 空 → 连接零行，语义更直白）。

**A/B 实测**：改回单列口径 → `TestAnchorHeadHandlesCreatedAtTie` **红**（报 `prev_tag_mismatch`）；
行值口径 → **绿**，且断言 `AnchorUsed` 恰为"并列里前一条"的 tag。

## P2-1 外层补 `occurred_at < to`（分区裁剪）

EXPLAIN 实测（真 PG17）：

| 谓词 | Append 里出现的分区 |
|---|---|
| 无 `occurred_at` 谓词 | 全部（本机 3 个分区全扫） |
| `a.occurred_at < to` | 只到 `to` 为止的分区；把 `to` 收到 `2026-11-01` 时**只剩 `p202610`** |

正确性：写入有下界守卫，链序对 `occurred_at` 单调不减 ⇒ 前驱的 `occurred_at` 必然 `< to`。
（本机仍见 `Seq Scan` 是因为 `(created_at, id)` 索引还没落地——Ark 的 D20 / 迁移 `000021` 派给我了，
索引一上就会变成 `Index Scan (Backward)`；本次不越界改迁移。）

## P2-2 "单独 DROP 上月分区仍误报" —— **记 D，不顺手修**

结论：**记 D 记录**，理由两条：
1. 代码里已有兜底：`TailTagBefore` 取不到前驱时会回落到 `anchor[M-1]`（**断点锚**）。所以
   "删分区但**写过锚**"的形态不会误报（D4-C(2) 的规则本来就是"只允许删已归档且已写锚的分区"）；
   残留误报只发生在"删了分区却没写锚"——那是**流程违规**，不是代码缺口。
2. 真正的正解是 D4-C(2) 的 `audit.chain.segment.<YYYYMM>`（断点锚：把被删段的**首尾 tag**另存），
   而 M1 不做归档删除（R9 推后 365 天）。现在写这套等于给一条不存在的数据路径写投机代码。

建议 D 记录写明：**"归档删分区必须先写段锚（首+尾），否则次月段首会误报 `prev_tag_mismatch`"**，
并挂到 R9（归档）的落地清单里。

## 验证

- 新增两条用例在旧口径下**红**、新口径下**绿**（见上两处 A/B）。
- `internal/audit` 串行套件（`-tags 'integration audit_serial' -p 1`）**三连绿**。
- 相关包 `-p 8` **三连绿**（`internal/audit` / `internal/store` / `cmd/guanjind`）。
- 全量集成一次：**751 PASS / 0 SKIP / 0 FAIL**。
- 单测全绿、`make gates` 绿、`gofmt` 干净；覆盖率闸原值不动：**audit 91%（floor 90）/ 总 81.2%（floor 60）**。
