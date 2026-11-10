# D20 落地：`audit_logs` 链序索引 `(created_at, id)`（迁移 000021，2026-11-11）

## 迁移编号：**000021**（不是 D20 正文里的 000019）

D20 正文最初写 `000019`，随后按 Ark 自己那条「取 `ls migrations/` 最大号 + 1，不预写死编号」的规则
修订为 **`000021`**——当前已落地到 `000020`（`000019` = D21 阈值 seed、`000020` = D22 平台级
禁用集 seed）。预写死编号会撞号或留编号空洞（库先到 21，后补的 20 永远不会被 `migrate up` 应用）。

- `migrations/000021_audit_logs_created_id_idx.up.sql` = `CREATE INDEX audit_logs_created_id_idx ON audit_logs (created_at, id);`
- `migrations/000021_audit_logs_created_id_idx.down.sql` = `DROP INDEX IF EXISTS audit_logs_created_id_idx;`

定义**照抄 D20，不增不减**：无 `DESC`（btree 可反向扫）、无 `occurred_at`/月份前缀
（`occurred_at` 是分区键，前导列服务不了"无谓词"的链尾读）、无 partial、非唯一、
**无 `CONCURRENTLY`**（不能进事务块，golang-migrate 默认包事务会当场失败；将来大表上线走运维脚本）。
在**父表**上建 ⇒ PG 给每个现有分区自动建子索引，并把它当模板套给新分区 ⇒ `Append` /
`TailTagBefore` **零代码改动**（链序口径不变，仍按 `(created_at, id)`）。

## 证据一：父子索引 + 新分区继承（可执行，`TestDBAuditCreatedIDIndexOnParentAndEveryPartition`）

放 `internal/store`（索引是**迁移产物**；`internal/audit` 的用例默认跑在手工建的隔离 schema `audit_test`
里，那里不含迁移索引——放错包会假红）。断言四件事：

1. 父索引存在且 `relkind='I'`（分区父索引）；
2. **每个现有分区**都挂着它的子索引（按 `pg_inherits` 父子关系判，**不按名字**：
   PG 给分区的子索引命名是 `audit_logs_pYYYYMM_created_at_id_idx`，按名字数会假红）；
3. 形状：`btree (created_at, id)`、无 `DESC`、无 `WHERE`、非 `UNIQUE`；
4. **新分区自动继承**：`audit.EnsurePartitions` 建一个"两年后"的分区 → 立刻检查它也有该子索引。

## 证据二：迁移往返（`scripts/verify-migrate-roundtrip.sh`，CI 同款）

```
expected_version=21
roundtrip_up1_version=21
roundtrip_down1_version=nil      # down 干净消失（残留 0）
roundtrip_down1_again_version=nil
roundtrip_up2_version=21         # 再 up 幂等，且结构快照与第一次逐字节相同
roundtrip_down2_version=nil
[OK] AC-ENG-02 down 半边：up(21) → down(nil, 残留 0) → up(21, 快照一致) → down(nil, 残留 0)
```

## 证据三：计划变化（`EXPLAIN (ANALYZE, BUFFERS)`，本地真 PG 17.11，423 行）

| 查询 | 前（无索引） | 后（有索引） |
|---|---|---|
| ①`Append` 的链尾读（`ORDER BY created_at DESC, id DESC LIMIT 1`） | `Sort`（top-N heapsort）+ 3×`Seq Scan`，**1.142 ms** | `Index Scan Backward using audit_logs_p2026xx_created_at_id_idx`，**0.120 ms**，零显式排序 |
| ②`TailTagBefore` 的段首前驱 | 外层 3×`Seq Scan`（`Sort` 参与） | 外层 `Index Scan Backward`（分区裁剪只碰 ≤`to` 的分区） |

索引一到位，两处"按链序定位一条"都不再是全分区 Seq Scan + Sort；`Append` 的那次读还在
advisory 锁内，缩短它等于缩短锁持有时间。完整前后对照 plan 由 Gate 另跑，这里只摘要点。

## 顺带修掉的两处测试卫生问题（否则三连跑不稳）

1. **并发用例改用"探针因子类型"**（`s11-probe-factor`）：`factors.disabled` 是**全局状态**，
   `-p 8` 下别的包正在算 ACR——把 `password` 停掉会让 `TestE2E_AC_FLOW_02_10` 报
   `current_acr 应为 AAL1，实际 ""`。探针类型只进那个字符串数组，不影响任何真实因子。
   （实测：改前 `-p 8` 三连里有一轮红，改后 3/3 绿。）
2. **还原时不再 `DELETE` 那一行**：D22（迁移 `000020`）要求它**存在**且是 `jsonb []`。
   原先"快照到不存在就删掉"会把 seed 行删掉，让后续用例又回到"行不存在"的竞态前提
   ——这也是别的包偶发红的一个来源。现在缺失就补成 `'[]'::jsonb`。
   （同类问题在 `cmd/guanjind` 的 `newACFLOW05Env` 里还在：它的 cleanup 仍会 `DELETE`，
   建议 Core 一并改成"补 `[]` 而不是删"。）

## 验证

- 新增 `TestDBAuditCreatedIDIndexOnParentAndEveryPartition`：PASS（父/各分区/新分区/形状四项）。
- 迁移往返脚本：`up(21) → down(nil, 残留 0) → up(21, 快照一致) → down(nil, 残留 0)`。
- 相关包（store / audit / guanjind）`-p 8` **三连绿**；全量集成 **752 PASS / 0 SKIP / 0 FAIL**。
- 单测全绿、`make gates` 绿、`-race` 绿、`gofmt` 干净。
- 覆盖率闸原值未动：`internal/audit` 91%（floor 90）、`internal/store` 79%（floor 75）、总 81.2%（floor 60）。
