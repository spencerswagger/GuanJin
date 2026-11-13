# spec/vectors 四份向量落盘（2026-11-13）

**任务**：契约附录 C 列的 `spec/vectors/{agent-hmac,assertion-verify,authz-decisions,config-mapping}.json` 在 main 上一个都不存在（Edge 落盘核查发现，证据链缺口）。本单落盘四份文件，并把它们接进**真实现**的消费者测试（不是摆设）。

## 落盘物

| 文件 | 条目 | 消费者测试 |
|---|:--:|---|
| `spec/vectors/agent-hmac.json` | 13 | `internal/agent/spec_vectors_test.go`（canonical 走真 `Canonical/SortedQuery`，HMAC-SM3 用 gmsm 现算） |
| `spec/vectors/assertion-verify.json` | 13 | `sdk/go/guanjin/spec_vectors_test.go`（`Verifier`+`ValidateClaims`+`MemoryReplayGuard`）；Java `SpecVectorsConformanceTest`（`Sm2AssertionVerifier`+`ClaimValidator`+`ReplayGuard`） |
| `spec/vectors/authz-decisions.json` | 14（+3 `scope` 待实现） | Java `SpecVectorsConformanceTest`（真 jcasbin + 真 `classpath:/guanjin/model.conf`） |
| `spec/vectors/config-mapping.json` | 5 组 × 29 键 | Go `LoadFromMap` + Java `GuanjinConfigLoader.fromMap` 双侧 |
| `spec/vectors/README.md` | — | 口径、跑法、缺口 |

生成方式（不进仓库）：`generated/vecgen`（Go）——canonical/SM3 走 `internal/agent`/`internal/crypto`，SM2 签名走 `internal/crypto.SignSM2`（与 `signing.SignJWT` 同形态：`r||s` 64 字节 + base64url）。

## 实跑证据（本机）

```
go test -run TestSpecVectors ./internal/agent/ ./sdk/go/guanjin/     → 3 个 test 全 PASS（agent-hmac 13 条 / assertion 13 条 / config 5 组）
mvn -pl guanjin-core -Dtest=SpecVectorsConformanceTest test          → Tests run: 4, Failures: 0
mvn -pl guanjin-core test                                            → 77 tests, 0 failures（含新增 4 条）
./scripts/check-sdk-java8.sh                                         → 三条 OK（依赖树/14 条 JDK9+ 黑名单/class major 52）
```

**有牙性 A/B（各注入一次，均已还原，`git diff` 空）**：

1. `internal/agent/canonical.go` 去掉 `SORTED_QUERY` 的 value 二级排序 → `AH-04` 立红：
   `got="tag=b&tag=a" want="tag=a&tag=b"`（证明向量真在验 D1-A2，不是回读期望值）。
2. 把 Go 的 `DefaultModelConf` 改成契约值 → `config-mapping` 立红并点名
   「Go 实现已偏离被钉死的缺陷值 `model.conf`……若缺陷已修请同步 known_divergences」（证明缺陷钉桩不会腐化）。

## 顺带查出的缺口（已写进 README，不记 PASS）

1. **Go SDK 默认值缺陷**：契约 #17 默认 `classpath:/guanjin/model.conf`，Go 给 `model.conf`（Java 与契约一致）。→ 归 **Nova**；向量按契约写期望、Go 现值作钉桩。
2. **duration 解析口径差异**：纯数字（`3000`）与 `d`（`1d`）Java 接受、Go 拒绝。→ 须 **Spec/Ark** 收口；向量不采用这些形态。
3. **`scope()` 无实现**（Java/Go 都是）：`authz-decisions.json` 的 `scope_cases` 定性为 UNIMPLEMENTED。
4. **Agent HMAC 无生产实现**：全仓 `hmac.New` 非测试 = 0；`internal/crypto.HMACSM3WithUserSecret` 明确禁止用于 Agent HMAC；Java `PullRequestSigner` 只有接口。→ 归 **Edge**（实现落地后接同一向量）。

## D29 核心证据（assertion-verify.json 的 AV-12）

`alg=SM3withSM2`（**对外令牌轨**的 alg）真签的断言必须被网关断言验签器拒绝；JWKS 里同时放了 `kid=oidc-2026-11 / alg=SM3withSM2` 的公钥，Go 测试另做前提断言（`JWKS 缺 "alg":"SM3withSM2" / oidc-2026-11 → Fatal`），证明拒的是 **alg**，不是「找不到键」。oidc 轨自身令牌仍由平台按 `SM3withSM2` 签发/校验，**不在本向量面内**（文件里 `oidc_track_note` 写明）。
