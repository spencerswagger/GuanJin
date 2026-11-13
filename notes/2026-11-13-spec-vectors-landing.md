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

## D29 核心证据（第一版写错，见下方修正；头版声称 AV-12 自证「拒的是 alg」——**作废**）

`alg=SM3withSM2`（**对外令牌轨**的 alg）真签的断言必须被网关断言验签器拒绝；JWKS 里同时放了 `kid=oidc-2026-11 / alg=SM3withSM2` 的公钥，Go 测试另做前提断言（`JWKS 缺 "alg":"SM3withSM2" / oidc-2026-11 → Fatal`），证明拒的是 **alg**，不是「找不到键」。oidc 轨自身令牌仍由平台按 `SM3withSM2` 签发/校验，**不在本向量面内**（文件里 `oidc_track_note` 写明）。

**修正（Owl 第一轮 P1-1 实测）**：上面这句在头版是错的——两侧 `ParseJWKS` 都只保留 `alg=SM2` 的键，oidc kid 的 401 只可能是"kid 找不到"；把 alg 闸整段删掉原测试仍全绿。判别力改由 **AV-14**（`kid=gw-2026-11` + 头 `alg=SM3withSM2` + **网关私钥真签**）提供，并用 `reason_contains: "alg"` 断理由。Owl 进一步把 AV-12/13 移出只留 AV-14 摘闸：Go `decision=allow status=0`、Java `expected: <reject> but was: <allow>` —— 那条签名真有效，唯一拦住它的是 alg 闸。

## Owl 评审（第一轮）必改 3 项与修法（head 待更新）

1. **P1-1「AV-12 证明不了拒的是 alg」——成立**：两侧 `ParseJWKS` 都只留 `alg=SM2` 键，所以 oidc kid 的 401 只能是"kid 找不到"；把 `assertion.go` / `Sm2AssertionVerifier.java` 的 alg 闸整段删掉，原测试仍全绿（我复现了）。
   修法：新增 **AV-14**（`kid=gw-2026-11` + 头 `alg=SM3withSM2` + **网关私钥真签**——SM3withSM2 与 SM2 是同一段字节的签名，摘掉 alg 闸就会被接受），并给 AV-12/13/14 加机器判据 `expect.reason_contains: "alg"`。**A/B 复验：摘 alg 闸 → Go 红在 AV-12（`拒绝理由不含 "alg" ... 未找到可用验签公钥`）、Java 红在 AV-12（`kid=oidc-2026-11 强刷后仍不存在`）**；还原后全绿。
2. **P1-2「29 键只验清单不验覆盖」——成立**：两侧消费者加「`expect` 键集 == `keys[]`」断言（Go 与 Java）。实测删掉某键的 expect → 现在两侧都红。
3. **P1-3「AH-12 拿向量改了冻结的 D1-A2」——成立**：契约 A2 字面 raw→再编码（`%2B`→`%252B`）与我取的"先解码再编码"对含 escape 的 query 结果不同。修法：`AH-12` 标 `pending_spec`，消费者**显式跳过并断言 pending 集合恰为 `["AH-12"]`**（不是静默 skip）；`query_pairs_note` 不再声称"已报 Spec 确认"；README 记明待 Spec 在 A2 补一句。另加 `ParseRawQuery` 缺失的事实说明（解码那步没有实现可测）。

P2 顺带：`stage` 降级为人读提示（判据 = decision/status/code/reason_contains）；`query_raw` 声明为形态记录；`known_divergences.defects` 加「键必须出现在某个 case 的 expect 里」防静默失效；AZ-01 改走 `require`（allow 侧也钉住不抛）；AZ-S3 移出 `scope_cases` 单列 `integrity_cases`；duration 口径钉方向（**Java 宽松才是偏差**）；生成器入库到 `scripts/vecgen/`（`go run ./scripts/vecgen` 重生成，SM2 键每次随机 → 旧 token 失效属预期）；去掉 `assertNull(null)` 空断言。

## Owl 第二轮（必改 0，可合）+ 我采纳的一条建议

- **AH-12 不该整条 pending**：消费者只吃 `query_pairs`、不读 `query_raw`，raw/解码之争对 AH-12 的断言毫无影响；整条 skip 反而丢掉 D1-A2 第④⑤步的唯一覆盖。**已改**：AH-12 恢复断言，另起 **AH-15**（`q=a%2Bb`，两种口径结果不同）pending 专记争议，pending 集合断言改为 `["AH-15"]`。AV-14 挪到 AV-13 之后顺号。
- **覆盖率**：Owl 报"`scripts/vecgen` 225 语句 0% → 总闸 67.32→65.39"。复核：正常流程**不受影响**——`go test ./...` 对没有测试文件的包不产块（实测 profile 里 `scripts/` 0 行）；只有单独 `go test ./scripts/vecgen -coverprofile` 才会出现 89 块 / 154 语句 0%（对照：混入后 66.9%，剔除后 68.4%，与干净 unit profile 一致）。仍给 `scripts/coverage-gate.sh` 加了 §0.5「剔除含 `/scripts/` 的块」当护栏（正常流程是空操作，剔除量会打印），并写清只认 `/scripts/`、藏不起 internal/sdk/cmd。
- 另修：README「不要手改 JSON」排除手写的 `authz-decisions.json`；本节顶部 D29 段落加作废标注。
