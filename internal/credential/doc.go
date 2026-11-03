// Package credential 是「凭据（口令派生 / TOTP / 恢复码）的存储与比对」的包边界占位。
//
// 口径（decisions.md / 基线）：

//   - 数据模型 §3.3；口令 PBKDF2-SM3 迭代≥100000、盐≥16B（索引 §2）
//   - 历史口令不可复用；禁用 SHA 系列直接哈希（项目硬约束 5）
//   - 密码学实现由 internal/crypto 提供，本包只做凭据语义
//
// 本包在 S1（骨架）只声明边界与依赖方向，**不含实现**；
// 实现按 m1-slices.md 的切片顺序由对应负责人在后续 wave 落地。
package credential
