// Package gateway 是「数据面 7 件事（验签 / 会话 / 注入 / 档位 / 风控 / 审计 / SLO）」的包边界占位。
//
// 口径（decisions.md / 基线）：

//   - 数据面设计 §1.1；D10 六步剥离时序；P2② TLS 不属本平台
//   - 反代走标准库 httputil.ReverseProxy（不套 Gin 热路径）
//   - 身份注入：逐请求签 X-GuanJin-Assertion（SM2、exp=60s、kid）
//
// 本包在 S1（骨架）只声明边界与依赖方向，**不含实现**；
// 实现按 m1-slices.md 的切片顺序由对应负责人在后续 wave 落地。
package gateway
