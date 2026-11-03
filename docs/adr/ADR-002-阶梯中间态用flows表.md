# ADR-002 · 阶梯认证中间态新增第 15 张表 `flows`

| 项 | 内容 |
|---|---|
| 状态 | Accepted（经 hugo 追认，2026-11-02） |
| 日期 | 2026-11-02 |
| 主笔 | 关津 架构 · Ark（TWKJSV）；表结构 6 处并入经 Edge（H8CSJ5）/ Core（Y05H0D）复核 |
| 上位依据 | `decisions.md` **D5**（13 列 + I-F1~I-F8）与 **D4-A**（主键不写 DB 默认值）；索引 §2「多副本 + 无状态化；挑战落库、Redis 只承载限流计数与热缓存」 |

## 1. 背景

基线是 **14 张表**，没有给「阶梯认证进行到哪一步」留落点。但基线同时要求：

1. 多副本 + 无状态化（不允许进程内/粘滞会话）；
2. Redis **只**承载限流计数与热缓存（不许拿 Redis 存业务状态）；
3. Descriptor 需要持久承载 `flow_id`、已达成 `amr`、`target_acr`、`expires_at`、已消费步骤。

三者叠加后，"中间态"无处可存。若硬扛成「用 `challenges` 冒充 flow」或「塞进 Redis / cookie」，会产出**无表可查的隐式语义**——正是"不许有隐性逻辑"这条铁律禁止的形态。

## 2. 决定

**新增第 15 张表 `flows`**（`migrations/000015_create_flows`），作为阶梯认证中间态的唯一权威落点，13 列：

| 列 | 类型 | 可空 | 说明 |
|---|---|:--:|---|
| `id` | `uuid` | NOT NULL | 即对外 `flow_id`；**无 DB 默认值**（应用层 UUID v7，见 `D4-A`） |
| `user_id` | `uuid` | NULL | 匿名流为 NULL，完成时回填；FK `ON DELETE SET NULL` |
| `session_id` | `uuid` | NULL | 登录成功后**一次性**回填（I-F7）；FK `ON DELETE SET NULL` |
| `client_id` | `text` | NOT NULL | 业务系统上下文（FK `ON DELETE RESTRICT`） |
| `target_acr` | `text` | NOT NULL | `CHECK IN ('AAL1','AAL2','AAL3')`；`AAL0` 仅前端展示、不落库 |
| `achieved_amr` | `text[]` | NOT NULL | 已达成因子集合；**单调不减**（I-F2） |
| `completed_steps` | `jsonb` | NOT NULL | `[{factor, challenge_id, at}]`，**只追加**；`challenge_id` 为**逻辑引用、不加 FK** |
| `status` | `text` | NOT NULL | `active` / `completed` / `expired` / `abandoned` |
| `expires_at` | `timestamptz` | NOT NULL | TTL 取 `runtime_config.flow.ttl_seconds`（V1 初值 600s）；**禁 DB 默认值**；以 DB 时间判定 |
| `consumed_at` | `timestamptz` | NULL | 一次性消费标记（I-F4） |
| `trace_id` | `text` | NULL | **只写创建该流那次请求**，补步不覆盖；不参与查询与唯一约束（`D4-C(4)`） |
| `created_at` / `updated_at` | `timestamptz` | NOT NULL | `updated_at` 由 `set_updated_at()` 触发器维护 |

索引（**名字全部显式**，不依赖 PG 自动命名）：`flows_pkey (id)`；`flows_one_active_per_user_client_uniq (user_id, client_id) WHERE status='active' AND user_id IS NOT NULL`（**部分唯一索引只能 `CREATE UNIQUE INDEX`**，不得写成 `CONSTRAINT`）；`flows_session_idx`、`flows_expires_idx`、`flows_client_created_idx`。

不变式 I-F1~I-F8 见 `D5`，其中三条最容易被实现踩坏：**I-F6 清理两段式**（先置 `expired` 保留、7 天后才 `DELETE`，否则 `FLOW_EXPIRED` 会时隐时现）、**I-F7 一次性回填 `session_id`**、**I-F8 锁序 `sessions → flows` 且 `flows` 行锁内禁做 PBKDF2/投递/外部 HTTP**。

## 3. 备选与不选的理由

- **备选 A（复用 `challenges` 加 `flow_id` 反查）**：否。流级进度与单步挑战是不同对象；且匿名流 `user_id`/`session_id` **双 NULL**，无法用「session + factor」反查定位，会引入双向耦合。改由请求显式携带 `flow_id`（登记为对外契约变更 **CH-1，待追认**）。
- **备选 B（Redis 存中间态）**：否。直接违反索引 §2「Redis 只承载限流计数与热缓存」；且 Redis 丢数据即使用户登录流程断在半路，无审计可查。
- **备选 C（客户端 cookie / JWT 携带进度）**：否。客户端可篡改 `achieved_amr`，等于把「已达成几个因子」交给攻击者声明；且无法做服务端过期与撤销。
- **备选 D（进程内内存）**：否。多副本下补步会随机落到另一副本 → 必然失败。

## 4. 后果

- **正面**：中间态可查、可审计、可并发安全推进；补步语义（不足 → 补 → 再判）可被断言，`AC-FLOW-10/11` 可稳定复现。
- **负面**：多一张表 + 一个清理器 + 一个 TTL 配置键（`flow.ttl_seconds`，键缺失即启动 fail-fast，代码不留 600 常量）。
- **需要谁做什么**：**Ark** 出 `000015`（含 seed `flow.ttl_seconds` + `ON CONFLICT DO NOTHING`）；**Edge** 出 `store/model/flows.go` + `repo_flows.go`（含带 tx 的锁读 `LockByID`）+ `pgxerr` 的 `SQLSTATE × 约束名` 双键映射；**Core** 在 `internal/flow` 落地 `I-F1~F8`，`internal/session` 通过窄接口 `AbandonBySession` 作废活跃流（避免包循环）；**Gate** 用行级举证 `AC-FLOW-01/03/07/10/11`。

## 5. 验证方式

- `AC-ENG-02`（表数 = **15**、`up`/`down` 往返无损）、`AC-ENG-04`（23505/23514 → 码表）、`AC-FLOW-10`（过期→`FLOW_EXPIRED`、`completed` 二次提交→`FLOW_NOT_FOUND`、7 天内行仍在）、`AC-FLOW-11`（并发补步 `achieved_amr` = 并集、锁内无 PBKDF2）。

## 6. 变更历史

- 2026-11-02 初版（`D5`，经 hugo 追认）。后续若并入到基线下一版数据模型，本 ADR 状态改 `Superseded`，口径以新版为准。
