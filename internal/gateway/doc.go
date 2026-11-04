// Package gateway 是**薄数据面**（`cmd/guanjingw`）的转发内核：只做 S8 的 7 件事。
//
// 口径（decisions.md / m1-slices.md S8 / 数据面设计 §4）：
//
//  1. `httputil.ReverseProxy` 转发（连接池 / 超时 / 背压；不套 Gin 热路径、不做业务路由发现）；
//  2. 验签 + 会话校验 + 按 client 档位判 ACR（复用 S6 的 `httpx.CallerResolver`）；
//  3. 逐请求签 `X-GuanJin-Assertion`（SM2、`exp = iat + 60`、带 `kid`）；
//  4. **先弃后签**：客户端自带的同名头一律先删，再由网关签自己的（AC-DP-02/03）；
//  5. 实时撤销判定（每次请求查会话状态，**不缓存**，AC-SESS-01/04）；
//  6. 单点登出签名通知（失败不阻塞本端撤销，AC-DP-12）；
//  7. `route → client` **静态**映射（路径前缀；索引 §4.2-18）。
//
// **明确不做**（AC-DP-11，属部署方前置 nginx/Kong）：TLS 终结、国密 TLS/TLCP、证书校验、
// WAF、负载均衡、灰度、熔断。本包不 import `crypto/tls`、不引熔断库、不设"跳过校验"的暗门。
//
// 本 PR（S8·1）落的是**基座**：7 里的 ①④⑦ 与上游失败语义（AC-DP-13）；
// ②③⑤ 在 S8·2（认证/撤销/断言）、⑥ 在 S8·3（SLO 通知），接口已按最终形态留好，
// 不给"先跑起来再改签名"留余地。
package gateway
