# `spec/vectors/` —— 三语言一致性向量（契约附录 C）

来源：`docs/sdk-contract.md` 附录 C（本目录被 §2.4 与附录 C 定为**唯一**向量位置）。
四份文件都是**数据**，不含代码；每份都有真实的消费者测试，不是摆设。

| 文件 | 条目 | 覆盖 | 消费者（真实现） |
|---|:--:|---|---|
| `agent-hmac.json` | 13 | 空 query、单 query、多 query 乱序、重复键（不同值/同值）、`?` 后为空、空 body、UTF-8 body、带 context-path 的 PATH、`/a` vs `/a/`、RFC3986 编码、字节序大小写 | `internal/agent/spec_vectors_test.go`（canonical 串走 `internal/agent.Canonical/SortedQuery`，签名用 HMAC-SM3 现算比对） |
| `assertion-verify.json` | 13 | 正常、签名错、`exp`/`iat` 边界两侧（±skew 与 +1）、`iss`/`aud` 错、未知 `kid`、JWKS 不可达无缓存（503 `KEY_UNAVAILABLE`）、`jti` 重放、**`alg` 非 SM2 → 拒**（`SM3withSM2` = 对外令牌轨的 alg、`HS256` 降级） | Go `sdk/go/guanjin/spec_vectors_test.go`（`Verifier`+`ValidateClaims`+`MemoryReplayGuard`）；Java `SpecVectorsConformanceTest`（`Sm2AssertionVerifier`+`ClaimValidator`+`ReplayGuard`） |
| `authz-decisions.json` | 14（+3 条 `scope` 待实现） | RBAC allow/deny、`defaultRole` 兜底与最小权限、`auditor` 行级豁免、密级 `>`/`=`/`<`/公开 0 级、资源组、未认证 | Java `SpecVectorsConformanceTest`（**真 jcasbin + 真 `classpath:/guanjin/model.conf`**） |
| `config-mapping.json` | 5 组 × 29 键 | 默认值、全显式、duration 形态、派生值（#12 jwks-uri / #9 aud）、list 与 role-prefix、REMOVED 键出现即失败 | Go `LoadFromMap` + Java `GuanjinConfigLoader.fromMap` 双侧 |

## 怎么跑

```bash
# 向量 + 消费者（都在既有测试里，CI 无需额外接线）
go test -count=1 -run TestSpecVectors ./internal/agent/ ./sdk/go/guanjin/
(cd sdk/java && mvn -pl guanjin-core -Dtest=SpecVectorsConformanceTest test)
```

## 已定性的缺口（不记 PASS）

1. **`scope()` 无实现**：Java core 与 Go SDK 都没有 `scope()`（`grep -rn scope sdk/java/guanjin-core/src/main` = 0 命中；Go `capabilities.go` 里 GJ-CAP-12~16 `Implemented:false`）。`authz-decisions.json` 的 `scope_cases` 先落盘定性，实现落地后由同一 runner 接。
2. **Agent HMAC 无生产实现**：全仓 `hmac.New` 非测试调用 = 0，`internal/agent` 只提供 canonical 拼接；`internal/crypto` 的 `HMACSM3WithUserSecret` 明确**禁止**用于 Agent HMAC。故 `agent-hmac.json` 的签名由 gmsm 独立实现算出，待 Edge 按 D1 落实现后接同一向量（Java 侧 `PullRequestSigner` 目前只有接口）。
3. **Go SDK 默认值缺陷**：`guanjin.authz.model-conf` 契约 #17 = `classpath:/guanjin/model.conf`，Go 给 `model.conf`。`config-mapping.json` 的 `known_divergences.defects` 把它钉死（契约值作期望、Go 现值作钉桩），修好后两侧测试会同时提示删表。
4. **duration 解析口径差异**：纯数字（`3000`）与单位 `d`（`1d`）Java 接受、Go 拒绝。向量不使用这些形态，已记 `known_divergences.parse_forms`，须 Spec/Ark 收口。
