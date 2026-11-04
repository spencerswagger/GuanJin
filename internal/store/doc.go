// Package store 是「数据访问：GORM 模型 + 仓储 + 事务 + 错误映射」。
//
// 口径（decisions.md / 基线）：
//   - 技术栈 §5 五条硬约束；D4-A（禁 DB 默认值）、D4-D（23505 按约束名映射）；
//   - **禁 AutoMigrate**（schema 唯一权威 = migrations/）；**禁软删除**字段；
//   - 复杂查询走 Raw()；pgxerr 按 **SQLSTATE + 约束名双键**映射（AC-ENG-04）；
//   - `flows` 的锁读只经 `FlowRepo.LockByID(tx, id)`（内部 FOR UPDATE，D5 I-F5/I-F8）；
//   - `runtime_config` 的启动期键（`flow.ttl_seconds` / `challenge.ttl_seconds` /
//     `challenge.max_attempts`）**缺键即 fail-fast**，代码不留 600 / 300 / 5 常量；
//     秒数走 `RequiredIntSeconds`、次数走 `RequiredPositiveInt`（D14①：5 不能被读成 5ns）。
package store
