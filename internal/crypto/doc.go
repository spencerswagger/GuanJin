// Package crypto 是「国密封装：SM2/SM3/SM4 + 主密钥派生 + JWKS」的包边界占位。
//
// 口径（decisions.md / 基线）：

//   - 索引 §2；D4-B（HKDF-SM3 派生，5 个用途域）
//   - 主密钥只做派生输入，**不进任何业务算法**；签名/摘要走独立 SPI 可替换（索引 §4.2-20）
//   - 用途域：guanjin/sm4/field | sm4/keywrap | hmac/audit | hmac/identifier | hmac/agent
//   - 纯软国密（P2①）：不接密码机/密码卡；实现库由 Edge 在 S3 引入（tjfoc/gmsm）
//
// 本包在 S1（骨架）只声明边界与依赖方向，**不含实现**；
// 实现按 m1-slices.md 的切片顺序由对应负责人在后续 wave 落地。
package crypto
