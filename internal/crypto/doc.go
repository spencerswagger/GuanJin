// Package crypto 是「国密封装：SM2/SM3/SM4 + 主密钥派生」的唯一边界。
//
// 口径（decisions.md / 基线）：
//   - 索引 §2；D4-B / D14② / ADR-005（HKDF-SM3 派生、8 个用途域）
//   - **主密钥只做派生输入、不进任何业务算法**：`MasterKey` 没有任何导出方法交出原文，
//     算法入口只接受派生得到的 `SM4Key` / `HMACKey`（字段不导出、无导出构造函数），
//     因此"把未派生的输入塞进算法"在**编译期**就写不出来；
//   - 用途域（**8 个，成对的加密/完整性相邻**）：
//     `guanjin/sm4/field` ↔ `hmac/field`、`sm4/keywrap` ↔ `hmac/keywrap`、
//     `hmac/audit`、`hmac/identifier`、`hmac/agent`、
//     `hmac/register`（D14② 注册窗口令牌，与盲索引分开：一密钥一用途）；
//     未知用途一律报错（禁自造 domain separator）；
//   - **SM4 密文一律带完整性（ADR-005）**：`SecretEnc = 0x02 | iv(16) | SM4-CBC(PKCS7) | HMAC-SM3(32)`，
//     MAC 覆盖 `版本 || iv || ct`，**先验 MAC 再解密**。没有 MAC 的 v1 密文
//     （`iv || 密文`）**一律拒绝**，不提供降级读取分支——否则"删掉 MAC 段"就成了
//     关闭完整性校验的开关。代价是旧密文不可读：项目未上线（P1），本地/预发库重灌即可；
//     将来轮换走一次性重加密任务，不引入 v1 读路径。
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
//   - **SM4-CBC 自身无认证**：只有 PKCS#7 校验时，错钥解密有约 1/256 的概率"padding 恰好合法"，
//     于是返回乱码明文、下游把"密钥不可用"误判成"凭据不对"（该缺陷曾让 CI 随机刷红两次）。
//     这就是 ADR-005 的由来——别再写一条"只加密不认证"的路径。
//
// 本包不含：JWKS 发布（S6）、令牌签发与 claim 校验（S6）、审计链语义（S10）。
package crypto
