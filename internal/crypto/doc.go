// Package crypto 是「国密封装：SM2/SM3/SM4 + 主密钥派生」的唯一边界。
//
// 口径（decisions.md / 基线）：
//   - 索引 §2；D4-B / D14②（HKDF-SM3 派生、6 个用途域）
//   - **主密钥只做派生输入、不进任何业务算法**：`MasterKey` 没有任何导出方法交出原文，
//     算法入口只接受派生得到的 `SM4Key` / `HMACKey`（字段不导出、无导出构造函数），
//     因此"把未派生的输入塞进算法"在**编译期**就写不出来；
//   - 用途域：`guanjin/sm4/field` | `sm4/keywrap` | `hmac/audit` | `hmac/identifier` | `hmac/agent` |
//     `hmac/register`（D14② 注册窗口令牌，与盲索引分开：一密钥一用途）；
//     未知用途一律报错（禁自造 domain separator）；
//   - 纯软国密（P2①）：不接密码机/密码卡，底层用 `tjfoc/gmsm`；
//   - 算法固定最高级、与档位无关（技术栈 §2）：SM3 / HMAC-SM3 / SM4 / PBKDF2-SM3 /
//     SM3withSM2。**全包不出现 SHA 系与 RSA**（AC-AUD-12 的唯一白名单是 PKCE S256）。
//
// 实现要点（踩过的坑，别再踩）：
//   - `tjfoc/gmsm` 的 `SM3.Sum(b)` **不满足 `hash.Hash` 约定**（把 b 当数据再哈希、且用
//     `b[len(b):]` 而不是 append 语义）→ 不能与 `x/crypto/pbkdf2`/`hmac` 的非空 Sum 组合，
//     否则多块 PBKDF2 直接 panic。本包的 PBKDF2/HKDF 均只用 `Sum(nil)`，见 pwdcrypto.go；
//   - `gmsm` 的 `sm2.Compress` 前缀是 0/1（非 SEC1 的 0x02/0x03）→ 对外编码在本包修正，
//     见 sm2.go。
//
// 本包不含：JWKS 发布（S6）、令牌签发与 claim 校验（S6）、审计链语义（S10）。
package crypto
