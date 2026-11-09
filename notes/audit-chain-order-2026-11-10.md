# 审计链序口径：`created_at` 入链、`occurred_at` 原样（2026-11-10）

> **性质**：基线**释义 + 执行形态**记录，不是契约变更、不是判据变更。裁定人 = **Spec**（基线释义负责人）；入库转录 = Ark（2026-11-10）。落点 `docs/decisions.md` **D4-C(5)**，本文件是其完整版；两处口径一致，本文件不再另起说法。
>
> **一句话结论**：**链序 = `created_at`**（入链序，平台生成）；**`occurred_at` = 调用方事件时间，原样保留并参与 `integrity_tag`**；二者不同**不算缺陷**。

## 1. **基线对"链序用哪个时间"无规定（必须保留的诚实标注）**

> `AC-AUD-03/04/05` 与 `D4-C(1)(2)(3)` 都只规定"按月分区 + 链不悬空 + 校验状态"，**没有规定链序取哪个时间列**。本条是**实现方形态**需要被裁的原因，不是"补一条既有规定"，也不是新增判据。

## 2. 为什么不能靠抬 `occurred_at` 保单调（事实，可复核）

| 事实 | 出处 |
|---|---|
| `occurred_at` 是**分区键**，且与 `id` 组成主键 `(id, occurred_at)` | `migrations/000011_create_audit_logs.up.sql:26`、`:29`（`PARTITION BY RANGE (occurred_at)`） |
| 四个取证索引全按 `occurred_at DESC` 排序 | 同文件 `:32`~`:35` |
| 写入后**永不可改**：`audit_logs_append_only` 触发器 → `forbid_mutation()` | 同文件 `:38`~`:40`（`AC-AUD-02` 已在真库实证） |
| `created_at` 同表已有、平台生成 | 同文件 `created_at timestamptz NOT NULL DEFAULT now()` |

所以"为单调而把 `occurred_at` 抬 1µs" = **永久改错取证时间，且事后连原值都补不回来**——审计链唯一不能碰的正是这一列。可安全抬升的是**平台自己**的 `created_at`（零 schema 变更）。

## 3. 正确做法（唯一形态）

1. **链序取 `created_at`**：链尾推进、`prev_tag` 串联、锚点水位都按 `created_at`；同一事务内 `created_at` 冲突时**只抬 `created_at`**。
2. **`occurred_at` 原样入库**，并**参与 `integrity_tag` 计算**（哈希覆盖原值才是"证据原样"）。
3. **下界守卫**：`occurred_at` **早于链尾所在分区的月初** → **拒绝并告警**。否则今天的写入会插进上月分区，破掉 `D4-C(2)` 的跨分区 `prev_tag` 链接（`AC-AUD-06`）。
4. **用例要求**：同一条事件用两个不同的 `occurred_at` → `integrity_tag` **必须不同**（证明它真的进了哈希，而不是"只在结构里存在"）。

## 4. 与判据的关系

- **不动判据**：`acceptance-criteria.md` 一字不改，`90 = 83/0/0/6/0/1` 不变；不进 CH（对外不可见、无 wire 变化）。
- **不动 `migrations/`**：本条不需要任何 schema 变更——`created_at` 与 `occurred_at` 两列本就在表里。
- `AC-AUD-03/04/05` 的通过条件不变；本条只回答"链序用哪个时间"，属取证口径登记（写法与 `D4-C(4)` 的 `trace_id` 口径同型）。

## 5. 遗留

- 已实现侧另有一处待补注释（裸 `syscall.ETIMEDOUT` 的边界说明）走 comment-only PR，与本条无关。
- `occurred_at` 的**上界**（未来时间）未在本次裁定；若后续要裁，同样只登记 `D4-C`，不改判据。
