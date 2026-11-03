// Package admin 是「管理面：三员各自的接口 + 鉴权矩阵」的包边界占位。
//
// 口径（decisions.md / 基线）：

//   - 接口清单 §3 / 页面规格 A1–A12；索引 §4.2-17
//   - 三员：系统管理员 / 安全管理员 / 审计员；越权 → THREE_POWER_VIOLATION（D3-E）
//   - clients.redirect_uris 去重校验在此层（D4-D：基线那条 CHECK 含子查询，PG 不支持）
//
// 本包在 S1（骨架）只声明边界与依赖方向，**不含实现**；
// 实现按 m1-slices.md 的切片顺序由对应负责人在后续 wave 落地。
package admin
