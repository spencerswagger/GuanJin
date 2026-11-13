# `spec/vectors/` —— 三语言一致性向量（契约附录 C）

来源：`docs/sdk-contract.md` 附录 C（§2.4 与附录 C 把本目录定为**唯一**向量位置）。
四份文件都是**数据**；每份都有真实的消费者测试（跑真实现，不是回读期望值）。
生成器在 `scripts/vecgen/`（`go run ./scripts/vecgen`）。三份**生成**的 JSON 不要手改；`authz-decisions.json` 是**手写**的（jcasbin 策略与判定没有生成器）。

| 文件 | 条目 | 覆盖 | 消费者（真实现） |
|---|:--:|---|---|
| `agent-hmac.json` | 15 | 空 query、单 query、多 query 乱序、重复键（不同值/同值）、`?` 后为空、空 body、UTF-8 body、带 context-path 的 PATH、`/a` vs `/a/`、字节序大小写、**RFC3986 编码**（`AH-12` 空格/`+`/`/`/中文；`AH-15` 先 decode 再编码，`%2Bb`→`a+b`→`a%2Bb`） | `internal/agent/spec_vectors_test.go`（canonical 走 `internal/agent.Canonical/SortedQuery`，签名 HMAC-SM3 现算） |
| `assertion-verify.json` | 14 | 正常、签名错、`exp`/`iat` 边界两侧（±skew 与 +1）、`iss`/`aud` 错、未知 `kid`、JWKS 不可达无缓存（503 `KEY_UNAVAILABLE`）、`jti` 重放、**`alg` 非 SM2 → 拒**（AV-12 oidc 轨键 / AV-13 HS256 / **AV-14 网关键 + `SM3withSM2` + 网关私钥真签**） | Go `sdk/go/guanjin/spec_vectors_test.go`（`Verifier`+`ValidateClaims`+`MemoryReplayGuard`）；Java `SpecVectorsConformanceTest`（`Sm2AssertionVerifier`+`ClaimValidator`+`ReplayGuard`） |
| `authz-decisions.json` | 14（+3 `scope`、+1 完整性 定性） | RBAC allow/deny、`defaultRole` 兜底与最小权限、`auditor` 行级豁免、密级 `>`/`=`/`<`/公开 0 级、资源组、未认证（401） | Java `SpecVectorsConformanceTest`（**真 jcasbin + 真 `classpath:/guanjin/model.conf`**） |
| `config-mapping.json` | 5 组 × 29 键 + **10 条负例** + **1 条 pending** + 2 条 trim 正例 | 默认值、全显式、duration 形态、派生值（#12 jwks-uri / #9 aud）、list 与 role-prefix、REMOVED 键出现即失败、**§3.2.1 时长非法形态必须启动失败**（含 v15 补口：`0s` 越权、`int64` 纳秒上界、NBSP 不 trim）、**4 字符 ASCII trim 集内合法**；`keys[].kind` 词表 = `bool\|string\|resource\|duration_ms\|list`（#17 `guanjin.authz.model-conf` = **`resource`**，契约 §3.2.2） | Go `LoadFromMap` + Java `GuanjinConfigLoader.fromMap`，两侧都断言 **expect 键集 = 29 项** |

## 生成器与门禁的边界（**裁量结论：不作为门禁**，2026-11-13）

`scripts/vecgen` 是**人跑的重生成工具**，不进 CI、不做「重生成后 `git diff` 为空」的断言。理由（成本/收益算过）：

1. **做不到**：生成器每次运行都**新生成一对 SM2 密钥**（`assertion-verify.json` 的 JWKS 与全部 token 随之变化），
   所以"重生成后 diff 为空"恒不成立；要让它成立就得把**固定私钥入库**——那是安全反模式，比这条门禁重要得多。
2. **不需要**：向量的**正确性**已被两侧消费者测试覆盖（`go test -run TestSpecVectors ./internal/agent/ ./sdk/go/guanjin/` +
   Java `SpecVectorsConformanceTest`）；生成器的**单方面漂移**由**生成期硬校验**拦（例如 #17 的默认值一漂就 panic）。
   门禁要防的是"文档与实现不符"，这件事已经由消费者测试直接判，不靠"文件有没有被重生成过"。
3. **保守的手工核对法**（怀疑向量被手改时用）：在临时副本里 `go run ./scripts/vecgen`，把两边的
   `token` / `signature` / `expected_signature` / `jwks` / `secret_b64` 等易变字段剥掉后 `diff`——剥壳后应逐字段相同
   （Owl #139 第二轮即用此法验过提交物确实是生成器产物）。

## 判据口径（写死，防"看着像验过了"）

- **机器判据只有三个**：`expect.decision` / `expect.status` / `expect.code`；D29 相关另加
  `expect.reason_contains`（必须出现在拒绝理由里，否则说明走的是别的失败路径）。
  `expect.stage` 是**人读的定位提示，不参与断言**——别把它当证据。
- `authz-decisions.json` 的 `operation.kind`：`enforce` 返回布尔（只能判 allow/deny）；
  `require` 未命中**抛** 403 `INSUFFICIENT_ROLE`（未认证抛 401 `TOKEN_INVALID`）。要钉住错误码就必须走
  `require`——allow 侧也留了一条 `require`（AZ-01）证明放行不抛。
- `agent-hmac.json` 的 `query_raw` 只记录来源形态；断言只用 `query_pairs`。
- `config-mapping.json` 的 `reject_cases` / `pending_cases`：每条带 `current.{go,java}` = **该语言今日实跑行为**，
  消费者按它钉死。词表五种（`scripts/vecgen/config.go` 与两侧消费者同口径）：
  `accept` / `reject@load`（装载期解析失败）/ `reject@validate`（装载成功、**校验期**失败，如 `replay-window=0s`
  撞安全约束）/ `reject@load:no-key` 与 `reject@validate:no-key`（失败但**错误信息不含键名** → §3.2.1 缺陷）。
  某语言行为一变即红并提示改成新行为；`pending_cases` 更进一步——须**升级成正例**（进 `cases` 按 `expect_after_fix`
  断言），不许只改 `current` 挂着。

## 怎么跑

```bash
go test -count=1 -run TestSpecVectors ./internal/agent/ ./sdk/go/guanjin/
(cd sdk/java && mvn -pl guanjin-core -Dtest=SpecVectorsConformanceTest test)
```

## 已定性的缺口（不记 PASS）

1. **`scope()` 无实现**：Java core 与 Go SDK 都没有（`grep -rn scope sdk/java/guanjin-core/src/main` = 0 命中；Go `capabilities.go` 里 GJ-CAP-12~16 `Implemented:false`）→ `authz-decisions.json` 的 `scope_cases` 定性 UNIMPLEMENTED。
2. **模型/策略完整性校验无实现**：Java `Authz.init` 无 digest 校验、Go `internal/authz` 只有 `doc.go` → `integrity_cases` 定性 UNIMPLEMENTED（附录 C 把这条与 `scope()` 分列，故单列一个桶）。
3. **Agent HMAC 无生产实现**：全仓 `hmac.New` 非测试调用 = 0，`internal/agent` 只拼 canonical；`internal/crypto.HMACSM3WithUserSecret` 明确**禁止**用于 Agent HMAC；Java `PullRequestSigner` 只有接口 → 签名由 gmsm 独立实现算出，待 Edge 落地后接同一向量。
5. **Go SDK 默认值缺陷（已修，2026-11-13）**：`guanjin.authz.model-conf` 契约 #17 = `classpath:/guanjin/model.conf`，Go 曾给 `model.conf`。现已对齐、`defects` **已删空**（口径：**已修就删表、未修才登记**；历史只在 git 里，不留"已修条目"）。首选拦法是**生成期硬校验**（实现漂移则 `go run ./scripts/vecgen` 直接 panic），`defects` 只留给"生成期判不死"的偏差。
5. **duration 严格解析（契约 §3.2.1 + §8.7/§8.8 E39–E42 / `AC-SDK-11`）：九类非法与补口①–③ 全部落地（2026-11-13）**
   - **已落地**：`3000`/`1d`/`1h30m`/`1.5s`/`PT60S`/`+60s`/`1S` 及空串/内部空白 —— 两侧全部 `reject@load`（`CM-06`~`CM-12`）。
   - **已落地（原 `CM-15`）**：`platform.connect-timeout=9999999999999h` —— 两侧 `reject@load`
     （#155 起 Java 补 `int64` 纳秒上界判定、Go 补 `ParseUint` 上界并让文案含键名）。
   - **已落地（原 `CM-16`）**：`gateway.clock-skew="\u00A02s"`（NBSP）—— 两侧 `reject@load`
     （#155 起两侧都只 trim 4 个 ASCII 空白，禁用语言默认 trim）。
   - **已落地（`CM-13`，2026-11-13 转正）**：`gateway.clock-skew=0s` —— 契约 **v16 §8.8 E42** 判「`0s` **合法**、
     语义 = 零容忍，**不得**以『必须为正』拒绝」（基线无下界；零容忍只会更严 = fail-closed）。Nova 放开零值（#163）后
     已从 `pending_cases` **移进 `cases` 正向**（`expect` 里 `gateway.clock-skew = 0`，且**≠ 默认 60000**）——
     `pending_cases` 桶因此清空，两侧消费者改为断言「**必须为空**」；生成期另有硬校验：`CM-13` 的期望若不是 0
     （或等于默认 60000）直接 panic，防止把"回落默认"记成契约期望。
   - `CM-14`（`replay-window=0s`）两侧都拒且属**安全约束**失败（不是解析失败），与补口① 一致。
   - `CM-17`（`"\t30s\r\n"` → 30000）/ `CM-17B`（`"\t45s\r\n"` → 45000）：trim 已落地。**CM-17 单独证明不了
     「没回落默认」**（30s 恰是 #23 的默认值），故 Gate 补了 `CM-17B`——这一条是加严，不是 Spec 清单要求。
