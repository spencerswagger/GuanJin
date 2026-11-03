# 旧分支 `feat/phase-8-authz` 对照与取优补齐（对照表）

| 项 | 内容 |
|---|---|
| 对照对象 | `origin/feat/phase-8-authz`，tip `cb208d2`，**19 个提交**、作者 `GuanJin Dev`（spencer 本人 10 月实现），292 个文件 |
| 对照基准 | **`docs/decisions.md` 现行裁定**（D1–D11 / P1–P5 / CH 清单）+ `docs/m1-slices.md` 的 S0–S11 边界；**不是**"哪版更新更全" |
| 现行 main | `e02d70f`（PR #1–#4 已合，三闸绿） |
| 硬约束 | **不 merge 那 19 个提交**（会与新骨架纠缠）；只按需 cherry-pick 或重做；旧分支**只读**，不改写不强推 |
| 基线文档 | 旧分支 `design-baseline/` **13 份** ≠ 现 `docs/baseline/` 的 14 份 → **以现库为准**，旧版只作参考，**不回灌覆盖** |
| 方法 | 对每个模块用 `git show <tip>:<path>` 抽样比对关键实现点（词表、算法、锁、错误码、配置键），不搬代码 |

---

## 0. 一页结论

| 判档 | 数量 | 模块 |
|---|---:|---|
| **直接吸收** | **0** | ——（没有一块能原样落地：全部都绑在旧 14 表 / 旧 `crypto`、`store` 接口 / 旧 `purpose` 词表上） |
| **改写后吸收** | **7** | GORM/store 模型与错误映射、因子 SPI、TOTP、注册面、网关 E2E、Spring Boot Starter、权限判定（binding） |
| **重做** | **4** | 编排引擎（flow）、Go 验签、Agent HMAC、审计链 |
| **放弃** | **3** | 旧 14 表 schema、旧 `design-baseline/` 13 份、旧骨架（已被 PR#4 取代） |

**一句话**：旧分支的价值在**业务逻辑与测试用例**（因子 SPI、注册、网关 E2E、权限绑定、Java Starter），不在**数据面契约**——它整体停在"14 表 + SHA256 HMAC + `dataLabel()` + 无分区审计链"的旧世界，凡是触碰这些契约的文件都必须改写或重做。

> **本轮实际落地代码文件数 = 0**。原因见 §3：任何一块搬进来都会立刻破 D1/D2/D5/D11/D4-C 中的至少一条，三闸必红；正确做法是按 §3 的分期计划在 S2–S11 内逐个改写吸收（那属编码工作，不是合并工作）。

---

## 1. 逐模块对照（14 行）

| # | 模块 | 旧分支现状（`cb208d2`） | 与现行裁定的差 | 判档 | 理由（条号） |
|---|---|---|---|---|---|
| 1 | **骨架** | `0595950` phase0：cmd 两个 + internal 包 + web `.gitkeep` | 现 main 的骨架由 PR#4 落地，含 `scripts/` 四把门禁与 `.github/workflows/ci.yml`（三闸） | **放弃** | 现骨架已是 D8/AC-ENG 的落地版，旧骨架更薄且无三闸接线（P1 / AC-ENG-03/07） |
| 2 | **schema（migrations）** | `d723ceb`：000000–000014 = **14 张表**，`audit_logs` **无分区、无锚、无咨询锁** | 现库 `000000–000015` = **15 张表**（含 `flows` 13 列 / 4 具名索引 / I-F1~F8） | **放弃** | **D5**（flows 第 15 张表）、**D4-C**（月分区 + 锚 + 状态枚举）、D2（`purpose` 6 值）。旧 DDL 拿来即违三条 |
| 3 | **GORM 模型 / store** | `d723ceb` + 后续：36 个文件，14 个 `TableName()`，无 `DeletedAt`、无 `AutoMigrate` ✅ | 缺 `flows` 模型；`mapPgError` 未按 "SQLSTATE + 约束名" 双键（flows 的两个约束会误报） | **改写后吸收** | GORM 五约束已满足（技术栈 §5）；补 **D5** 的 13 列模型与 **D5 落地段**的双键映射即可复用其余仓储代码 |
| 4 | **因子 SPI** | `2fd4295`/`764ab1f`：21 文件，`Descriptor()` 12 处、`Capabilities` 8 处，**无 `if type=="sms"` 分支** ✅ | 依赖旧 `credential`/`store` 接口签名 | **改写后吸收** | **索引 §2 因子** + 因子模型 §7 守护规则（能力断言）已满足；改写点＝接口对接，不涉语义 |
| 5 | **编排引擎（flow）** | `2fd4295`：`descriptor.go`/`orchestrator.go`/`register.go`/`state.go`，**无 `flows` 表、无 `FOR UPDATE`**，amr 仅内存累加 | 缺 I-F2（amr 单调）、I-F5/I-F8（行锁 + 锁内禁重活）、两段式过期清理；`purpose` 字面量混用（`login/bind/stepup/recover` + `step_up/enroll/verify_identifier/register` 并存） | **重做** | **D5（I-F1~F8）+ D2（6 值词表）+ D10（剥离时序在 SDK 侧，编排不得自造）**；无锁版多副本必然丢 amr，不是补丁能修的 |
| 6 | **TOTP** | `764ab1f`：TOTP 持有型因子，验证 SPI 可扩展性 | 需接现 `credential` 布局与 SM3 时间窗参数 | **改写后吸收** | 索引 §2 因子四类（TOTP 属 M2）；算法口径（SM3）与现 D4-B 派生键一致 |
| 7 | **注册面** | `3c84599`：`register/start|complete` + 标识符自助 | 缺 "必带 `client_id` 上下文"、`purpose=register` 落库（旧词表无该值） | **改写后吸收** | **索引 §2「注册必带业务系统上下文」+ D2**；逻辑骨架可留，校验与词表必须换 |
| 8 | **网关 E2E** | `ce03dd3`：数据面 `/gw/{client_id}` 路由 + E2E 用例 | 需核对：只做 7 件事、客户端自带头先弃后签、断言 `exp=60s`、`UPSTREAM_UNAVAILABLE` | **改写后吸收** | **数据面 §1.1–§1.3 + D8-C′**；E2E 用例价值最高（可转成 `AC-DP-*` 的资产） |
| 9 | **Spring Boot Starter** | `b598bd4`：`sdk/guanjin-core`（34 个 java）+ `guanjin-spring-boot2` + 示例 | **`dataLabel(` 6 处、裸 `.clearance(` 1 处**（D11 作废名）；配置需核 29 项与 `jwks-uri` 默认路径 | **改写后吸收** | **D11**（唯一写法 `withClearance(level)`、`dataLabel()`/裸 `clearance(` 零残留）+ **D6**（29 项 / 无 `mode` / jwks 默认）；owner = Nova |
| 10 | **Go 验签（SDK 侧互操作）** | `b598bd4`：Go 侧验签示例/工具 | 现 main `internal/agent/canonical.go` **已按 D1 实现**（`sorted_query` + `SM3HEX(body)` + `PATH` 规则） | **重做（已完成）** | **D1**：旧分支同期 `hmac.go` 用 **SHA256**，与 D1 直接冲突；现实现为权威，旧文件作参考 |
| 11 | **权限判定（binding）** | `cb208d2`：`authz/binding.go` + 绑定 E2E | 需确认"平台只存绑定、判定在 SDK 本地"；错误码双码分工（`THREE_POWER_VIOLATION` vs `INSUFFICIENT_ROLE`） | **改写后吸收** | **索引 §2「平台不做授权」+ D3-E**；绑定/撤销联动（`AbandonBySession`）与 D5 一致 |
| 12 | **Agent HMAC** | `cb208d2`：`agent/hmac.go`/`report.go`/`policies.go`/`service.go` + E2E | **`SHA256` 命中**（`hmac.go` 3 处）＝违 D1；nonce 长度按字符非字节 | **重做** | **D1（A1/A2 口径）**：canonical 段序含 `sorted_query`、`SM3HEX(body)`；现 main 已有 D1 版 canonical，HMAC 侧按同一向量重写 |
| 13 | **审计链** | `cb208d2`：`audit/chain.go`，`integrity_tag` + `prev_tag` 有，**无 advisory lock / 无锚 / 无分区** | 缺 D4-C 的月分区、锚 `runtime_config`、两段式 + 整链校验 Job、派生键 | **重做** | **D4-B（HKDF 派生、主密钥不进业务算法）+ D4-C（分区/锚/状态枚举）**；旧链多副本必断链，且 `prev_tag` 直用主密钥 |
| 14 | **旧 `design-baseline/` 13 份** | `4c6f922`：13 份基线（与现库版本不同） | 现 `docs/baseline/` 为 14 份且是引用来源 | **放弃** | **P3 + 索引 §2**：现库为准，旧版只作参考，绝不回灌 |

---

## 2. 硬冲突清单（旧分支若原样使用会立刻违的条）

| # | 冲突点 | 旧分支表现 | 现行裁定 | 影响 |
|---|---|---|---|---|
| C1 | 传输/签名摘要算法 | `internal/agent/hmac.go` 用 `SHA256` | **D1**：`SM3HEX(body)`，全链路国密 | Agent 面互验必失败 |
| C2 | 密级方法名 | SDK 内 `dataLabel(` 6 处、裸 `.clearance(` 1 处 | **D11**：唯一 `withClearance(level)`，两个旧名作废 | SDK 三语言判定不一致 |
| C3 | 错误码 | `SERVICE_DEGRADED` 仍出现 | **D8-C′⑤**：永不返回 | 同故障双码 |
| C4 | 表数与中间态 | 14 表，无 `flows` | **D5**：15 表 + I-F1~F8 | 阶梯 amr 无处落 |
| C5 | `purpose` 词表 | `stepup`/`bind` 与 `step_up`/`enroll`/`verify_identifier`/`register` 混用 | **D2**：6 值唯一词表 | 写入触发 CHECK 失败或语义分叉 |
| C6 | 审计链 | 无分区 / 无锚 / 无 advisory lock | **D4-C** | 多副本断链、无法展示校验状态 |
| C7 | 并发安全 | flow 包**无 `FOR UPDATE`** | **D5 I-F5/I-F8**（锁序 `sessions→flows`、锁内禁重活） | 200 QPS 下补步丢 amr |
| C8 | 配置面 | 环境变量层 20 键；SDK 配置未按 29 项收口 | **D6**（29 项、无 `mode`、`jwks-uri` 默认、两处默认值） | `AC-SDK-07` 反射对齐失败 |

---

## 3. 分期吸收计划（挂到 `m1-slices.md` 的步骤，不新增步骤编号）

| 期 | 步骤 | 吸收内容（文件 → 来源提交） | 改写点 |
|---|---|---|---|
| P1 | **S2/S3** | `internal/store/model/*`（14 模型）→ `d723ceb`；`internal/store/repo_*.go`、`pgxerr.go` → `cb208d2` | 补 `flows` 13 列模型 + 4 具名索引；`mapPgError` 加 SQLSTATE+约束名双键（`flows_one_active_per_user_client_uniq`→409、I-F1→400）；**不得**带入 `migrations/migrations.go` 的 AutoMigrate 路径 |
| P2 | **S4/S5** | `internal/factor/**`（21）→ `2fd4295`/`764ab1f`；`internal/flow/{descriptor,state,register}.go` → `2fd4295`/`3c84599` | 接入现 `crypto`/`store` 接口；编排改读写 `flows`（`LockByID` + 事务内消费 challenge）；`purpose` 换 6 值；两段式过期清理；amr 单调校验 |
| P3 | **S8** | `internal/gateway/**`（9）+ `gateway` E2E → `ce03dd3` | 只保留 7 件事；客户端自带头"先弃后签"；`UPSTREAM_UNAVAILABLE`；断言 `exp=60s` |
| P4 | **S9（Nova）** | `sdk/guanjin-core/**`（34 java）、`sdk/guanjin-spring-boot2/**`（21）、`sdk/examples`、`sdk/testdata` → `b598bd4` | `dataLabel(`/裸 `.clearance(` **全删**改 `withClearance`；配置 29 项 + `jwks-uri` 默认；剥离按 **D10 六步**；`enabled` profile 门控 |
| P5 | **S10** | `internal/audit/chain.go` 仅作**对照**（不搬） | 重做：月分区 + 主键 `(id, occurred_at)` + advisory lock + 锚 + 两段式 + 校验 Job（D4-C） |
| P6 | **M2 前置** | `internal/agent/{hmac,report,policies,service}.go` + `agent_e2e_test.go` → `cb208d2` | 算法换 HMAC-**SM3**；nonce 按**字节**；沿用现 `internal/agent/canonical.go` 的 D1 实现 |

---

## 4. 明确不做的事

1. **不 merge** `feat/phase-8-authz` 的 19 个提交，也不 rebase 它（只 `git show` 读文件）。
2. **不把旧 `design-baseline/` 13 份**写回 `docs/baseline/`（现库为准）。
3. **不搬**旧 `migrations/`（14 表）与 `migrations/migrations.go`（应用内迁移路径，违 P1）。
4. **不搬** `internal/notify`、`internal/observability` 的旧结构（前者不在 10 包边界内、后者与现 `observability` 规划重复）。

---

*本文只登记对照结论与借用计划，不含代码；旧分支内容一字未改。*
