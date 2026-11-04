// Package model 是 GORM 模型层（只做读写映射，不承载业务规则）。
//
// 口径（技术栈 §5；decisions.md D4）：
//   - **禁 AutoMigrate**：schema 唯一权威是 migrations/；
//   - **禁软删除**：模型里不得出现 GORM 的软删除字段（CI 有门禁扫描）；
//   - 主键 UUID v7 由应用层 BeforeCreate 生成，**不依赖 DB 默认值**（D4-A）；
//   - 表名/列名照抄数据模型规格，不改名（F0 不可动名单）；
//   - `created_at` / `updated_at` 归**数据库**（DEFAULT now() + `set_updated_at()` 触发器），
//     模型侧声明为只读（`->`）：应用不再自己写时间戳，避免"应用时钟"与"库时钟"两套事实。
//
// 15 张表：users / clients / identifiers / authenticators / sessions / challenges /
// role_bindings / policy_snapshots / user_visited_clients / auth_events / audit_logs /
// login_policies / signing_keys / runtime_config + flows（D5 第 15 张）。
// 本切片（S3）先落的是 flows（D5）与 runtime_config（D5 的 flow.ttl_seconds 落点），
// 其余表随各自切片（S4~S8）落地。
package model
