// Package store 是「数据访问：GORM 模型 + 仓储 + 事务 + 错误映射」的包边界占位。
//
// 口径（decisions.md / 基线）：

//   - 技术栈 §5 五条硬约束；D4-A（禁 DB 默认值）、D4-D（23505 按约束名映射）
//   - **禁 AutoMigrate**（schema 唯一权威 = migrations/）；**禁 GORM 软删除**（无 DeletedAt）
//   - 复杂查询走 Raw()；高频表批量写；pgxerr 按 SQLSTATE + 约束名**双键**映射
//
// 本包在 S1（骨架）只声明边界与依赖方向，**不含实现**；
// 实现按 m1-slices.md 的切片顺序由对应负责人在后续 wave 落地。
package store
