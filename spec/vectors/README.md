# `spec/vectors/` —— 三语言一致性向量（契约附录 C）

来源：`docs/sdk-contract.md` 附录 C（§2.4 与附录 C 把本目录定为**唯一**向量位置）。
四份文件都是**数据**；每份都有真实的消费者测试（跑真实现，不是回读期望值）。
生成器在 `scripts/vecgen/`（`go run ./scripts/vecgen`）。三份**生成**的 JSON 不要手改；`authz-decisions.json` 是**手写**的（jcasbin 策略与判定没有生成器）。

| 文件 | 条目 | 覆盖 | 消费者（真实现） |
|---|:--:|---|---|
| `agent-hmac.json` | 15 | 空 query、单 query、多 query 乱序、重复键（不同值/同值）、`?` 后为空、空 body、UTF-8 body、带 context-path 的 PATH、`/a` vs `/a/`、字节序大小写、**RFC3986 编码**（`AH-12` 空格/`+`/`/`/中文；`AH-15` 先 decode 再编码，`%2Bb`→`a+b`→`a%2Bb`） | `internal/agent/spec_vectors_test.go`（canonical 走 `internal/agent.Canonical/SortedQuery`，签名 HMAC-SM3 现算） |
| `assertion-verify.json` | 14 | 正常、签名错、`exp`/`iat` 边界两侧（±skew 与 +1）、`iss`/`aud` 错、未知 `kid`、JWKS 不可达无缓存（503 `KEY_UNAVAILABLE`）、`jti` 重放、**`alg` 非 SM2 → 拒**（AV-12 oidc 轨键 / AV-13 HS256 / **AV-14 网关键 + `SM3withSM2` + 网关私钥真签**） | Go `sdk/go/guanjin/spec_vectors_test.go`（`Verifier`+`ValidateClaims`+`MemoryReplayGuard`）；Java `SpecVectorsConformanceTest`（`Sm2AssertionVerifier`+`ClaimValidator`+`ReplayGuard`） |
| `authz-decisions.json` | 14（+3 `scope`、+1 完整性 定性） | RBAC allow/deny、`defaultRole` 兜底与最小权限、`auditor` 行级豁免、密级 `>`/`=`/`<`/公开 0 级、资源组、未认证（401） | Java `SpecVectorsConformanceTest`（**真 jcasbin + 真 `classpath:/guanjin/model.conf`**） |
| `config-mapping.json` | 5 组 × 29 键 + **7 条时长负例** | 默认值、全显式、duration 形态、派生值（#12 jwks-uri / #9 aud）、list 与 role-prefix、REMOVED 键出现即失败、**§3.2.1 时长非法形态必须启动失败** | Go `LoadFromMap` + Java `GuanjinConfigLoader.fromMap`，两侧都断言 **expect 键集 = 29 项** |

## 判据口径（写死，防"看着像验过了"）

- **机器判据只有三个**：`expect.decision` / `expect.status` / `expect.code`；D29 相关另加
  `expect.reason_contains`（必须出现在拒绝理由里，否则说明走的是别的失败路径）。
  `expect.stage` 是**人读的定位提示，不参与断言**——别把它当证据。
- `authz-decisions.json` 的 `operation.kind`：`enforce` 返回布尔（只能判 allow/deny）；
  `require` 未命中**抛** 403 `INSUFFICIENT_ROLE`（未认证抛 401 `TOKEN_INVALID`）。要钉住错误码就必须走
  `require`——allow 侧也留了一条 `require`（AZ-01）证明放行不抛。
- `agent-hmac.json` 的 `query_raw` 只记录来源形态；断言只用 `query_pairs`。
- `config-mapping.json` 的 `reject_cases`：每条带 `current.{go,java}` = **该语言今日实跑行为**，消费者按它钉死
  ——某语言收紧后断言立刻红并提示把该格改成 `"reject"`（钉桩不会腐化成假绿）。`current` 出现 `"reject"` 时
  还要求**错误信息含键名**（§3.2.1 的 fail-fast 口径）。

## 怎么跑

```bash
go test -count=1 -run TestSpecVectors ./internal/agent/ ./sdk/go/guanjin/
(cd sdk/java && mvn -pl guanjin-core -Dtest=SpecVectorsConformanceTest test)
```

## 已定性的缺口（不记 PASS）

1. **`scope()` 无实现**：Java core 与 Go SDK 都没有（`grep -rn scope sdk/java/guanjin-core/src/main` = 0 命中；Go `capabilities.go` 里 GJ-CAP-12~16 `Implemented:false`）→ `authz-decisions.json` 的 `scope_cases` 定性 UNIMPLEMENTED。
2. **模型/策略完整性校验无实现**：Java `Authz.init` 无 digest 校验、Go `internal/authz` 只有 `doc.go` → `integrity_cases` 定性 UNIMPLEMENTED（附录 C 把这条与 `scope()` 分列，故单列一个桶）。
3. **Agent HMAC 无生产实现**：全仓 `hmac.New` 非测试调用 = 0，`internal/agent` 只拼 canonical；`internal/crypto.HMACSM3WithUserSecret` 明确**禁止**用于 Agent HMAC；Java `PullRequestSigner` 只有接口 → 签名由 gmsm 独立实现算出，待 Edge 落地后接同一向量。
5. **Go SDK 默认值缺陷（已修，2026-11-13）**：`guanjin.authz.model-conf` 契约 #17 = `classpath:/guanjin/model.conf`，Go 曾给 `model.conf`。现已对齐，`known_divergences.defects` 清空（机制保留：再出现同类漂移按「期望按契约 + 把实现现值钉死」登记，Go 侧漂移即红）。生成器也升级为**生成期硬校验**（实现漂移则生成向量时报错，不再静默覆盖期望）。
5. **duration 严格解析待实施（契约 §3.2.1 / `AC-SDK-11`）**：契约 §3.2.1（v13）已裁决「合法 = `trim` 后 `^[0-9]+(ms|s|m|h)$`」，**Java 与 Go 都还没按它收紧**（今日实测：Go 拒 `3000`/`1d`/`PT60S` 但接受 `1h30m`/`1.5s`/`+60s`；Java 相反——接受 `3000`/`1d`/`+60s`/`1S`，拒 `1h30m`/`1.5s`/`PT60S`）。`reject_cases` 的 `current` 记的正是这份现状，实施（Nova）落地后须把对应格改成 `"reject"`。
