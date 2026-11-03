// Package protocol 是「协议面（OIDC/OAuth2 标准端点）」的包边界占位。
//
// 口径（decisions.md / 基线）：

//   - 接口清单 §3.1；D3-E 双码分工；D8-D′ `/userinfo` 零查库
//   - 端点：/authorize、/authorize/flow、/authorize/submit、/oauth/token、/oauth/revoke、
//     /oauth/introspect、/userinfo、/.well-known/openid-configuration、/.well-known/jwks.json
//   - 令牌签发 SM3withSM2 + kid（索引 §2）；授权码一次性、refresh 轮换（AC-LOGIN-04）
//   - 本包禁止直接读 Redis/DB：依赖通过构造注入，便于 `/userinfo` 保持零查库（D8-D′）
//
// 本包在 S1（骨架）只声明边界与依赖方向，**不含实现**；
// 实现按 m1-slices.md 的切片顺序由对应负责人在后续 wave 落地。
package protocol
