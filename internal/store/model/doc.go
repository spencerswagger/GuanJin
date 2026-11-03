// Package model 是 GORM 模型层（只做读写映射，不承载业务规则）。
//
// 口径（技术栈 §5；decisions.md D4）：
//   - **禁 AutoMigrate**：schema 唯一权威是 migrations/；
//   - **禁软删除**：模型里不得出现 gorm.DeletedAt（CI 有门禁扫描）；
//   - 主键 UUID v7 由应用层 BeforeCreate 生成，**不依赖 DB 默认值**（D4-A）；
//   - 表名/列名照抄数据模型规格，不改名（F0 不可动名单）。
//
// 15 张表：users / clients / identifiers / authenticators / sessions / challenges /
// role_bindings / policy_snapshots / user_visited_clients / auth_events / audit_logs /
// login_policies / signing_keys / runtime_config + flows（D5 第 15 张）。
package model
