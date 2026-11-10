# 审计误报收口：段首判据改用「真实前驱」，锚点退回「封条 + 断点兜底」（2026-11-11）

## 1. 诊断结论（先说结果）

1. 派工里描述的**同月形态**（"封锚后同一自然月再复核必报"）在 main `3464010` **已经不存在**：
   由 PR #76（`e07f5ab` 锚点语义统一）+ PR #80（`3e5606b` 段尾改存在性判定、`f53d1df`
   canonical 读写同口径）修掉，回归用例 `TestAnchorContinuitySameMonth` 在 main HEAD 上**绿**。
2. 但**同一根因的跨月形态仍然活着**，且症状更重：`RunRange` 拿**上月锚点快照**当本段首条
   `prev_tag` 的期望值，而 `AnchorCurrentMonth` 是**每一拍**拍的快照。审计事实是持续写入的，
   于是「月末最后一拍封锚 → 当月又写一条 → 次月首条 `prev_tag` 指向那条」之后，
   **整个次月每一拍都报 `prev_tag_mismatch`**：`audit_chain_broken_total` 每天 +1、
   `audit_anchor_age_seconds` 永久停住 —— 校验 Job 沦为噪声源（真断链被淹没）。
3. 不是"重复校验被重复计数"，也不是分桶键/时区截断：`RunRange` **只读不写**，
   连跑 N 次结论一致（幂等）；`monthOf` 统一 `UTC()` 截月初；`anchor[M]` 的 key 是月份，
   与"事件唯一标识"无关。

## 2. 最小复现

```
# 修复前（main 3464010 的 verify.go）
go test -tags 'integration audit_serial' ./internal/audit \
  -run 'TestAnchorHeadUsesRealPredecessor' -v
--- FAIL: TestAnchorHeadUsesRealPredecessor (0.08s)
    anchor_head_serial_db_test.go:101: 封锚后正常追加被误报成断链（index=0 reason=prev_tag_mismatch）：段首期望值仍在用上月锚点快照
```

用例步骤（每一步都是真实业务形态）：当月写 2 条 → 复核通过 → 封锚 → 当月再写 1 条 →
次月写 1 条 → 次月复核。**期望：通过**；修复前**实际**：`prev_tag_mismatch`（index=0）。
修复后同一条用例 PASS，且反向守线（把上月最后一条真删掉）**必须**报 `prev_tag_mismatch`。

## 3. 方案（最小改动，不碰只增不改的约束）

| 判据 | 字段（落库级） | 为什么可靠 |
|---|---|---|
| 段内接续 | `audit_logs.prev_tag` vs **上一条记录的** `audit_logs.integrity_tag` | 同一张表内相邻两行的字段互证，写入时在同一事务里算好；删/改/重排都会破坏它 |
| 段首接续 | 本段首行 `prev_tag` vs **链上真实前驱**行的 `integrity_tag`（`Chain.TailTagBefore`） | "前驱"是按 `created_at` 定位的真实行，不是快照；上月尾部被删时前驱会变→必报 |
| 链序定位 | `audit_logs.created_at` | `Append` 在同一把 advisory 锁内把它抬到 `max(now, 链尾+1µs)`，**严格单调**，可唯一定位"本段首条/真实前驱" |
| 段尾封条 | `runtime_config.value`（key `audit.chain.anchor.<YYYYMM>`） | **只做存在性判定**：封条那条记录在段内找不到 → `anchor_mismatch`（尾部截断/整月清空唯一线索） |
| 跨段兜底 | 同上锚点（"断点锚"） | **仅**在前驱所在分区已被 DROP（D4-C(2) 的 365 天归档，M1 未启用）时顶替段首期望值 |

**关键区分（真断链 vs 重复校验）**：`anchor.*` 是**快照**，落后于链尾是正常态，不许当接续判据；
接续只认同表内的 `prev_tag` / `integrity_tag`（真事件写入时生成、append-only 触发器保证不可改）。
`RunRange` 本身无写操作，重复执行不改任何状态。

## 4. 影响面

- **表**：无 schema 变更（`audit_logs` / `runtime_config` 列与索引都不动）。
- **端点**：无（校验 Job 在 `cmd/guanjind` 进程内，指标只有 `audit_chain_broken_total` /
  `audit_anchor_age_seconds` 两个名字不变）。
- **既有校验结果**：此前被误报为 `broken` 的月份会转为 `verified`（这正是修复目的）；
  真断链仍报（反向守线用例钉住）。`AC-AUD-03/05/11` 判据口径不变。
- **性能（新增一条只读查询）**：`TailTagBefore` 每天一次；EXPLAIN 实测它与**既有的**链尾读
  （`ORDER BY created_at DESC LIMIT 1`，每次 `Append` 都跑）一样是 **全分区 Seq Scan + Sort**——
  因为 `audit_logs` **没有 `created_at` 索引**。本次不擅自加索引（迁移归 Ark / `AC-ENG-02` 台账），
  已在汇报里提给 Ark：建议 `CREATE INDEX audit_logs_chain_order_idx ON audit_logs (created_at DESC, id DESC)`。

## 5. 证据（本地真 PG 17.11 + 真 Redis 7）

- `TestAnchorHeadUsesRealPredecessor`：修复前红（见 §2）、修复后绿；含反向守线（删上月最后一条 → 必报）。
- `TestSameMonthRepeatVerifyNoFalseAlarm`：封锚后追加+复核连跑 3 轮，`chain_broken_total=0`。
- 串行套件 `-tags 'integration audit_serial' -p 1 -count=3`：全绿。
- 全量集成 `-tags=integration -p 8 ./...`：**735 PASS / 0 SKIP / 0 FAIL**。
- 单测主闸 `go test ./...`：**587 PASS / 0 SKIP**；`-race` 全绿；`make gates` 绿。
- 覆盖率（并行 + 串行两套 profile 并集）：`internal/audit = 91%`（下限 90）、总 80.9%（下限 60）。

## 6. 一句话教训

判据要钉在**同表内可互证的字段**上，不能钉在"自己按时钟拍的快照"上——
快照永远会被正常业务甩在后面，那不是断链。
