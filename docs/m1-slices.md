# 关津 · M1 垂直切片拆解（m1-slices）

| 项 | 内容 |
|---|---|
| 主笔 / 归属 | 关津 架构 · Ark（TWKJSV） |
| 输入 | `/docs/baseline/` 12 份 + 索引；`/docs/decisions.md`（D1–**D12**、**P1–P8**、**F0 不可动名单**、R2–R5/R8/R9；R1 已转入 D5、R6 已转入 **P5**、R7 已转入 P4、新增 **P6**（分支纪律）/ **P7**（docs 必须入库），D5/P4/P5/P6/P7/P8 均经 hugo 追认 2026-11-02）；`/docs/acceptance-criteria.md`（**v11，90 条 AC，10 组**：REG 10 / LOGIN 8 / FLOW 13 / SESS 5 / DP 13 / SDK 10 / FC 8 / AUD 12 / ENG 8 / AGENT 3；**83 PASS-able / 0 `PENDING·CH-n` / 0 `BLOCKED` / 6 NOT-APPLICABLE / 0 NOT-IMPLEMENTED / 1「后置（DEFERRED）：本期不做判定」(`AC-AUD-10`)**；v11 = **P8 把 CH-1~CH-5 全部转「已追认」→ 13 条 `PENDING·CH-n` 一次转 `PASS-able`，断言一字未改、只改状态列**；`AC-SDK-03`/`AC-SDK-02` 已按 D6(3)/(4) 降为 `PASS-able`；**`AC-AUD-10` 的呈现以 `decisions.md` D8-B【正式裁定】为唯一口径 = 「后置（DEFERRED）：本期不做判定」、`BLOCKED` 归零**）；`/docs/sdk-contract.md`（**唯一 owner = Nova，落 PR #1**；本文**只引用不复制**，版本/行数/md5 以该 PR 为准——PR #2 曾带过一份 v4，已按 P6「一人一文件」摘除，避免与 v7 互不为祖先而静默回退）；本文只依赖其**数量口径**（22 能力 / 29 配置）与 D6/D10/D11 点名的条目名 |
| 写者纪律（**强制**） | **`/docs/m1-slices.md` 与 `/docs/decisions.md` 仅 Ark 可写**。其他成员（含 Gate / Nova / Spec / Edge / Core / Vue）**只回报改动请求**，不得直接修改这两份文件；发现数字或口径不一致，报给 Ark 一次改齐（防并行改写导致计数抖动——2026-11-02 已发生两起）。主持人的派工权不变，但落笔人只有一个。 |
| 权威顺序 | 索引 §2 → 端点/数据契约 → `decisions.md` → 本文件（切片口径） |
| 状态 | 方案稿。**本轮不建骨架、不入库、不写代码**；今晚按本文顺序施工 |
| 铁律 | 严格**串行**推进（同一时刻最多 2 人；本文不写并行幻想）；每步退出即「可运行、可演示」；不交半成品模块；**旧仓 SpIAM 只读——不改 / 不 push / 不开分支，代码只进 `GuanJin`（P5）** |

---

## 1. M1 边界（一句话：一条最小登录链路，端到端跑通，而不是按层横向铺开）

**M1 含**
1. **注册面**：`/auth/register/start|complete`（邮箱自注册、注册即登录）——见 §2 裁定 ①。
2. **密码因子 + 登录主链路**：`/authorize` → `/authorize/flow` → `/auth/challenge/password` → `/auth/verify/password` → `/authorize/submit` → `/oauth/token`，签发 `access_token` + `id_token`（`acr=AAL1`、`amr=["pwd"]`）。
3. **阶梯补步契约面**：`ACR_INSUFFICIENT` + `details.flow`、`done` 不重输、禁用因子不进 `current_acr`、因子全禁用的**不可达终态**（真实两级补步属 M2）。
4. **会话与实时撤销**：`sessions` 生命周期、登出/管理面撤销、多副本一致性（不靠缓存窗口）。
5. **数据面 7 件事**：验签、会话校验与实时撤销、身份注入 `X-GuanJin-Assertion`（SM2、逐请求、`exp=60s`）、按 client 档位判 ACR、认证风控（仅失败计数与锁定）、认证审计事件、单点登出通知。**TLS（含国密 TLS/TLCP）/WAF/LB/灰度/熔断一律不属本平台范围**——由部署方前置 nginx/Kong 承担，平台只见明文，不写代码、不设配置项、不进验收判据（P2②，依 spencer 口径 2026-11-02）。
6. **最小 SDK 接入**：`guanjin-core`（零 Spring）+ `guanjin-spring-boot2`（javax）：验签、剥离、`jti` 重放、JWKS 缓存、fail-fast。
7. **工程门禁与验收映射**：`migrations` 全量 up/down、禁 `AutoMigrate`/软删除脚本门禁、覆盖率下限、`AC-ID → PASS/FAIL/PENDING/NOT-IMPLEMENTED/NOT-APPLICABLE/后置（DEFERRED）` 映射表（`PENDING` 与「后置」各自**单列**，二者都不计入通过；映射表的「后置」栏表头写全名 **「后置（DEFERRED）：本期不做判定」**）。

**M1 不含**（与 Gate §13 一致）：TOTP/短信/邮箱因子与两级链路（M2/M4）｜找回/换绑/统一恢复（M3）｜注销/数据导出（M5）｜管理面 35 条全量 CRUD 与三员全矩阵（M1 只测 `sessions` 撤销 + 越权负向 `AC-AUD-07`）｜密钥轮换状态机（M1 只测静态双密钥发布）｜**Agent 面 `report`/`policies`**（被 D1 已收口但不在 M1 链路，排 M2 前置）｜**TLS / 国密 TLS（TLCP）/ 证书校验**（不属本平台，部署方前置，本平台不做判定）｜**密评判定与出证**（整体后置、本期不判定也不送检；`AC-AUD-10` 记「后置（DEFERRED）：本期不做判定」、`BLOCKED` 归零，但 §11.1 的 7 项输入仍须在 M2 之前备齐 = **后置 ≠ 免验**；等保相关的**代码要求**照基线实现，见下）。

---

## 2. 回答 Gate 的两个二选一（裁定，写入 M1 口径）

**① 注册面（`AC-REG-*`）留在 M1 —— 留，且 7 条全留（不只 `AC-REG-03`）。**
理由：注册是「登录能发生」的唯一合法入口；砍掉后 e2e 只能用管理面建号，而管理面建号**绕过准入规则**（`clients` 档位/准入策略），e2e 会测出一条生产不存在的路径。另外 `AC-REG-01/03` 是 `decisions.md` **D2（purpose 六值词表）** 的唯一天然证明点，也是 23514 风险的早暴露点。代价：M1 多 2 个端点 + 1 组判据，可接受。

**② 两级阶梯完整链路（`AC-FLOW-08` 起）推到 M2；M1 只测补步**契约面**（`AC-FLOW-01`~`07`、`09`）。**
理由：M1 只有密码因子，AAL2 物理不可达（移交 §八-5），强行测两级要么造假因子、要么塞 TOTP 进 M1，都会污染切片。M1 必须证明的是**契约面**：不足 → 403 `ACR_INSUFFICIENT` + `details.flow`（`AC-FLOW-02`）、`done` 不重输（`AC-FLOW-03`）、禁用因子不计入（`AC-FLOW-04`）、**不可达终态**（`AC-FLOW-07`，M1 可用「client 禁用 password 且目标 AAL2」构造）。`AC-AUD-09`（等保三级双因素）在 M1 记 `NOT-APPLICABLE`，M2 必须转 `PASS`。

---

## 3. 步骤总览（S0–S11，串行；顺序即执行顺序）

| 序 | 步骤 | 负责人 | 前置 | 主判据（PASS 条件见 §4） |
|---|---|---|---|---|
| S0 | 仓库与文档骨架（不入代码）**+ 开 PR** | **Ark** | 无（**今晚 22:05 开工**） | S0 DoD（§4-S0，含「PR 已开」） |
| S1 | 工程骨架 + 目录树 + CI 门禁 | **Ark** | S0（module path 见 P4，已定稿） | AC-ENG-03 / 07 / 05 / **08**（脚本就位 + **四注入**变异必红：`SERVICE_DEGRADED` / `STEP_ALREADY_SATISFIED` 越界 / `dataLabel(` / 裸 `clearance(`） |
| S2 | migrations：14 张表 + `flows`（D5 规格） | **Ark** | S1、P1、**D5 已追认**、D4、D2 | AC-ENG-02 / 04、AC-AUD-02 / 06、AC-REG-03 |
| S3 | crypto/store 基座（派生键·UUIDv7·错误映射） | **Edge** | S2 | AC-ENG-04、AC-FC-03、**AC-AUD-01**（trace 基础设施） |
| S4 | 标识符 + 凭据 + 密码因子 + 注册面 | **Core** | S3 | AC-REG-01~07、AC-LOGIN-02 / 03、AC-AUD-01 |
| S5 | flow 编排 + Descriptor + 挑战面 | **Core** | S4、**D5（已追认）**、D7、**CH-1 已追认（P8）** | AC-FLOW-01~07 / 09、**AC-FLOW-10 / 11**、AC-FC-06、AC-REG-03 |
| S6 | 协议面令牌签发（authorize/token/revoke/jwks/userinfo） | **Core** | S5、D3-E（已收口，见 §6） | AC-LOGIN-01 / 04 / 05 / 06 / 07 / 08 |
| S7 | 会话管理 + 实时撤销 + 管理面 sessions | **Core** | S6 | AC-SESS-01~03 / 05、AC-AUD-07（部分） |
| S8 | 数据面 `cmd/guanjingw` 7 件事 | **Edge** | S7 | **AC-DP-01~13**、AC-SESS-01 / 04 |
| S9 | 最小 SDK 接入（Java） | **Nova** | S8、`sdk-contract.md`（**Nova 维护、PR #1**）、D6、CH-5（仅「29 项交付集 + 删 `guanjin.mode`」） | **AC-SDK-01~10**、AC-DP-03 / 04~08、AC-FC-04 / 05 |
| S10 | fail-closed 故障注入 + 审计链校验 Job | **Edge**（+Gate 驱动） | S9、D4、D8 | AC-FC-01~08、AC-AUD-03 / 04 / 05、**AC-AUD-11** |
| S11 | M1 收口（e2e 演示 + AC 全表 + 交接） | **Gate**（Spec 出验收） | S10 | **AC-AUD-12**、AC-ENG-01 / 05 / 06 / **08（复核）** + 全表无 `NOT-IMPLEMENTED` |

> 串行说明：S9 的 SDK 单测可在 S8 完成实体后由 Nova 独立编写，但**合并顺序仍在 S8 之后**；S10 的审计链并发用例需 Edge 与 Gate 同场，不提前开跑。任何一步不得跳序，不得"先铺完所有包的空壳再补逻辑"。

### 3.1 切片 → 端点 → 包 → 表 → 验收点（一表看全，端点名一律用冻结名）

> 用途：施工时"从端点倒推该动哪些包、写哪张表、由哪条判据收口"。**端点名、包名、表名一律照抄冻结名**，不得改名/造名；本表与 §4 逐步拆解互为索引（§4 给动作与断言细节，本表给落点）。

| 切片 | 入口端点（冻结名） | 涉及模块 / 包 | 数据落点（表） | 验收点（AC-ID，正文见 `acceptance-criteria.md`） |
|---|---|---|---|---|
| **S0** | 无（纯文档入库） | `docs/baseline/`、`docs/adr/`、`README.md`、`Makefile` | **无**（不建表、不写代码） | S0 DoD（§4-S0 六条）+ PR 已开 |
| **S1** | 无业务端点（仅 `GET /` 存活探针占位） | `cmd/guanjind`、`cmd/guanjingw`、`internal/*` 空骨架、`scripts/`、`.golangci.yml` | **无** | `AC-ENG-03`、`AC-ENG-07`、`AC-ENG-08`（四注入变异必红）、`AC-DP-11`（脚本本体） |
| **S2** | 无 HTTP 端点（`golang-migrate up/down`） | `migrations/`（`000000_common_functions` + `000001`~`000015`） | **全部 15 张**：`users` `clients` `identifiers` `authenticators` `sessions` `challenges` `auth_events` `audit_logs`(分区) `login_policies` `signing_keys` `runtime_config` `role_bindings` `policy_snapshots` `user_visited_clients` + **`flows`**(D5) | `AC-ENG-02`（15 张）、`AC-ENG-04`（23505↔码表）、`AC-AUD-02`（禁改）、`AC-AUD-06`（分区）、`AC-REG-03`（purpose 六值） |
| **S3** | 无 HTTP 端点（启动期 fail-fast + 基座单测） | `internal/crypto`（`masterkey.go` 只收派生键）、`internal/store`（`db.go`/`model/base.go`/`pgxerr.go`/`repo_flows.go` 含 `LockByID`）、`internal/config` | `runtime_config`（seed `flow.ttl_seconds`） | `AC-FC-03`（密钥缺失即启动失败）、`AC-AUD-01`（`trace_id` 基础设施） |
| **S4** | `POST /auth/register/start`、`POST /auth/register/complete` | `internal/identifier`、`internal/credential`（`password.go`）、`internal/factor/holding`、`internal/flow/register.go`、`internal/risk`、`internal/store` | `users`、`identifiers`、`authenticators`、`sessions`、`challenges`、`auth_events`（读 `clients`/`login_policies`） | `AC-REG-01~07`、`AC-LOGIN-02`、`AC-LOGIN-03`、`AC-AUD-01` |
| **S5** | `GET /authorize/flow`、`POST /auth/register/start`（`purpose=register` 复用）、`POST /auth/challenge/{factor}`、`POST /auth/verify/{factor}` | `internal/flow`（`orchestrator.go`/`descriptor.go`）、`internal/factor`（能力断言）、`internal/store/repo_flows.go`、`internal/httpx` | `flows`、`challenges`（读 `login_policies`/`clients`/`user_visited_clients`） | `AC-FLOW-01~07`、`AC-FLOW-09`、`AC-FLOW-10`、`AC-FLOW-11`、`AC-FC-06` |
| **S6** | `GET /authorize`、`POST /authorize/submit`、`POST /oauth/token`、`POST /oauth/revoke`、`POST /oauth/introspect`、`POST /userinfo`、`GET /.well-known/openid-configuration`、`GET /.well-known/jwks.json` | `internal/protocol`（`authorize.go`/`token.go`/`userinfo.go`/`discovery.go`）、`internal/crypto`（`sm2.go`/`jwks.go`）、`internal/session` | `sessions`、`signing_keys`、`auth_events`（读 `flows`） | `AC-LOGIN-01`、`AC-LOGIN-04`、`AC-LOGIN-05`、`AC-LOGIN-06`、`AC-LOGIN-07`、`AC-LOGIN-08` |
| **S7** | `GET /api/v1/sessions`、`DELETE /api/v1/sessions`、`DELETE /api/v1/sessions/{id}`、`GET /api/v1/me/sessions`、`DELETE /api/v1/me/sessions`、`DELETE /api/v1/me/sessions/{id}`、`POST /logout` | `internal/session/manager.go`、`internal/admin/sessions.go`、`internal/admin/rbac.go`（三员最小骨架）、`internal/audit/event.go` | `sessions`、`auth_events`、`flows`（活跃流置 `abandoned`）、`role_bindings`（三员） | `AC-SESS-01`、`AC-SESS-02`、`AC-SESS-03`、`AC-SESS-05`、`AC-AUD-07`（单副本先行） |
| **S8** | `ANY /{route}/`（数据面通配转发）+ `POST /api/v1/agent/report`（M1 只落验签件，判据归 M2） | `internal/gateway`（`proxy.go`/`admit.go`/`inject.go`/`strip.go`/`revoke.go`/`slo.go`/`route.go`）、`cmd/guanjingw` | `sessions`、`user_visited_clients`（写）、`clients`（档位读）、`auth_events` | `AC-DP-01~13`、`AC-SESS-01`、`AC-SESS-04` |
| **S9** | 平台侧无新增端点；SDK 消费 `GET /.well-known/jwks.json`（`GET /api/v1/agent/policies` 默认不调，`AC-SDK-10`） | `sdk/guanjin-core`（零 Spring：验签/claim/`jti`/JWKS 缓存/fail-fast）、`sdk/guanjin-spring-boot2`（`javax.servlet` Filter + `GuanjinProperties`） | **无**（SDK 侧本地 `jti` 落 Redis，不碰平台库） | `AC-SDK-01~10`、`AC-FC-04`、`AC-FC-05`、`AC-DP-03`、`AC-DP-04~08` |
| **S10** | 无新增业务端点（`/readyz` 探针 + `cmd/guanjind` 内定时任务） | `internal/audit`（`chain.go`）、`internal/httpx/error.go`（D8-C/C′ 映射）、`internal/config`、`cmd/guanjind` 定时器 | `audit_logs`（分区 + `prev_tag`）、`runtime_config`（`audit.chain.anchor.<YYYYMM>` / `segment.<YYYYMM>`） | `AC-FC-01`、`AC-FC-02`、`AC-FC-07`、`AC-FC-08`、`AC-AUD-03`、`AC-AUD-04`、`AC-AUD-05`、`AC-AUD-11` |
| **S11** | 全端点回放（一条 e2e：注册→登录→补步→转发→撤销→403） | 全仓（Gate 驱动，Spec 出验收） | 全表（只读取证 + 映射表） | `AC-AUD-12`、`AC-ENG-01`、`AC-ENG-05`、`AC-ENG-06`、`AC-ENG-08`（复核）+ 全表无 `NOT-IMPLEMENTED` |

### 3.2 开工顺序与依赖链（谁卡谁，一眼判定）

```text
S0 文档入库+PR
 └─S1 骨架/门禁
    └─S2 migrations(15 表)
       └─S3 crypto/store 基座
          └─S4 标识符+凭据+注册面
             └─S5 flow+Descriptor+挑战面        ← CH-1 已追认（P8）：flow_id 按必填实现
                └─S6 协议面令牌签发
                   └─S7 会话+实时撤销
                      └─S8 数据面 guanjingw
                         └─S9 SDK 最小接入
                            └─S10 fail-closed+审计链 Job
                               └─S11 收口(e2e+映射表)
```

- **硬依赖（不可并行、不可跳）**：`S2→S3`（无表则仓储无落点）、`S3→S4`（无派生键/错误映射则不能写库）、`S4→S5`（无账号与密码因子则流无对象）、`S5→S6`（无 `flows`/`challenges` 则签不出带 `acr/amr` 的令牌）、`S6→S7`（无令牌则撤销无对象）、`S7→S8`（撤销不实时，数据面判 ACR 无依据）、`S8→S9`（无断言头则 SDK 无内容可验）。
- **可让路（单步内部并行，但合并顺序不变）**：S9 的 SDK 单测可在 S8 完成实体后由 Nova 先写；S10 的审计链并发用例需 Edge+Gate 同场；S5 的 `internal/flow` 单测可在 S4 收尾时先起（`flows` 表已在 S2 就位）。
- **卡点已解除**：`CH-1`~`CH-5` 全部追认（P8，经 hugo 追认 2026-11-02）——`flow_id` 按必填实现、Descriptor 新字段与错误码变更可直接落、SDK 按 29 项交付。**不再有「等追认」的停工点**；判据侧 Gate 已在 **v11** 把 13 条 `PENDING·CH-n` 一次转 `PASS-able`（**90 = 83 / 0 / 0 / 6 / 0 + 1 后置**），本文数字与之一致。
- **禁止**：在 `flows` 缺位时开工 S5；在 S8 实体未完成时合并 S9；把 S10 的故障注入改成 mock（必须真实停容器）。

---

## 4. 逐步拆解（目的 / 动作要点 / 验收判据与 PASS 条件 / 退出条件）

> **状态口径（统一，避免与 Gate 判据抖动）**：本节各步写的 `PASS = ...` 都是**「期望行为」**，状态以 `/docs/acceptance-criteria.md`（**v11**）与本文件 §6 契约变更行为准，账目 = **90 = 83 `PASS-able` / 0 `PENDING·CH-n` / 0 `BLOCKED` / 6 N/A / 0 `NOT-IMPLEMENTED` / 1「后置（DEFERRED）：本期不做判定」(`AC-AUD-10`)**；**`PENDING·CH-n` 已归零**——原 13 条（CH-2 = `AC-FLOW-01`/`AC-FLOW-07`；CH-4 = `AC-LOGIN-05`/`AC-FLOW-03`/`AC-SESS-01`/`AC-AUD-07`/`AC-ENG-08`/`AC-REG-08`/`AC-REG-09`/`AC-REG-10`；CH-5 = `AC-SDK-01`/`AC-SDK-07`/`AC-FC-05`）已随 **P8** 追认全部转 `PASS-able`；`AC-FLOW-12/13`（D12）自始 `PASS-able`。

### S0 · 仓库与文档骨架 + 开 PR（负责人 Ark，前置：无；开工时间 **今晚 22:05**）
- **目的**：把"唯一权威"落到版本管理里，之后一切引用可追溯；**S0 的产出必须是一个已开的 PR**（不是本地 diff）。用现成空仓 `spencerswagger/GuanJin`（P3），不新建仓库。
- **动作清单（S0.1→S0.7，内部子步骤，不新增强制动作、不改步骤编号）**：
  - **S0.1 建骨架**：`docs/baseline/` + `docs/adr/` + `README.md` + `Makefile` + `.gitignore`（此步只落**目录与文档**，不写任何 Go/JS/Java 源码；代码骨架属 S1）。
  - **S0.2 基线入库**：`docs/baseline/` 原样入库 **12 份基线文档 + 索引（共 14 个文件：12 个 `.md` 含索引与《项目移交Prompt》+ 2 个 `.html`）**，**内容零改动**、不重命名、不调整目录层级。源目录里另有 1 个 `pinyin-name-search-single.html`（「中文姓名拼音模糊搜索 · 实施方案」），**与关津设计基线无关，不入库**——这不是漏文件，是剔除无关产物；入库清单逐文件见 PR 描述。
  - **S0.3 项目文档入库（一人一文件，P6）**：本分支只落 **`docs/decisions.md`（Ark）+ `docs/acceptance-criteria.md`（Gate 原文、Ark 不写）+ `docs/m1-slices.md`（Ark）** 三份；**`docs/sdk-contract.md` 不入本分支**——它唯一 owner 是 **Nova / PR #1**，Ark 只引用不复制（Owl P1：两分支各带一份会冲突并回退新版本）。
  - **S0.4 ADR 规范 + 首批 ADR**：`docs/adr/ADR-000-记录规范.md`（只引用 `decisions.md` 的 D/P/CH 编号，不复制基线正文）+ 首批四篇 `ADR-001`（签名 canonical / `D1`）、`ADR-002`（`flows` 表 / `D5`）、`ADR-003`（SDK 29 项配置 / `D6`）、`ADR-004`（客体密级 + `level` 词表 / `D11`）——`decisions.md` 正文引用了这四个文件名，**缺文件即断链**。
  - **S0.5 README 声明**：写清「schema 唯一权威 = `migrations/`」「基线只读」「迁移不随应用启动（P1）」「旧仓 SpIAM 只读（P5）」「module path（P4）」。
  - **S0.6 Makefile**：只放 `migrate-up` / `migrate-down` / `test` / `lint` 目标（本地 `golang-migrate up`，**不写**自动迁移）。
  - **S0.7 分支 + PR**：分支 `guanjin/ark` → commit（写清"为什么"）→ **`git push origin guanjin/ark`** → **在 `GuanJin` 仓开 PR** → Owl 评审 → **由 spencer 点合入**（不自行合入、不直推 main）。
- **验收判据（DoD，逐条可查）**：① `docs/baseline/` 与源内容逐字节一致（`git diff` 为空，**未改一字**）；② **14 个文件**在库（12 `.md` + 2 `.html`，名单见 S0.2），且与源逐字节一致；`pinyin-name-search-single.html` **不在库**（无关产物）；③ `decisions.md` / `acceptance-criteria.md` / `m1-slices.md` **三份**在本分支入库（`sdk-contract.md` 由 **PR #1** 提供，本分支**不复制**），且可检索到 `D1–D12` / `P1–P8` / `F0` / `CH-1–CH-6` / `AC-*` / `S0–S11` 交叉引用无断链；④ `README` 含 **P1/P4/P5/P6/P7** 五条声明；⑤ **PR 已开**（PR 链接可用、标题与描述写明"仅文档入库、零代码"、CI 未跑代码）；⑥ 无任何 `.sql`、源码文件被提交。
- **红线（S0 期间硬约束）**：**旧仓 SpIAM 只读**——不得在其内提交、`push`、开分支；关津所有代码**只进 `GuanJin` 仓**（P5，经 hugo 追认，2026-11-02）。另：不执行 `git worktree add|remove|prune`；不碰生产/测试环境；基线一字不改；本轮（22:05 前）**不实际执行**任何一步。
- **退出条件**：PR 已开且评审通过前不合入；文档交叉引用（decisions↔AC↔slices↔sdk-contract）无断链。

### S1 · 工程骨架 + 目录树 + CI 门禁（负责人 Ark，前置：S0；module path 已定稿）
- **目的**：一次性把"目录/门禁"定死，避免后续每包各自为政。
- **动作要点**：按技术栈 §3.1 建 `cmd/guanjind` + `cmd/guanjingw` + `internal/` 10 包 + `migrations/` + `web/` + `sdk/` 骨架；`go.mod` 的 **module path = `github.com/spencerswagger/guanjin`**（P4，经 hugo 追认，2026-11-02），内部包前缀同此，全仓 import 一次写对、不再讨论大小写/子目录变体；`scripts/check-no-automigrate.sh`、`coverage-gate.sh`、`.golangci.yml`；CI 步骤按技术栈 §9.4 落成（此时只有 gofmt/lint/门禁三类可跑）。**`docs/` 目录约定（2026-11-02 定，Gate 问、Ark 裁）**：项目文档一律进 `docs/` 子目录——`docs/baseline/`（基线只读）、`docs/adr/`、**`docs/qa/`**（Gate 的 `gate-criteria.md` 等验收文档，**不放仓库根 `qa/`**）、`docs/ddl/`（DDL 导出的参考样本，**不放 `docs/baseline/`**）；`qa/` 若已落根目录，迁到 `docs/qa/` 即算达标，门禁脚本已对两处都豁免（`scripts/check-forbidden-error-codes.sh` 的 `qa/*` 与 `docs/qa/*`）。
- **验收判据**：`AC-ENG-03` PASS 条件 = 故意注入一行 `gorm.DeletedAt` 与一处 `AutoMigrate(` → CI 必须**失败**（变异测试，脚本存在不算通过）；**`AC-ENG-08` PASS（Gate v3 新增，归位此处：禁码扫描 + 变异测试）** = `scripts/check-forbidden-error-codes.sh` 在仓内存在，且**故意注入一处返回 `SERVICE_DEGRADED` 的代码 → CI 必须红**（脚本存在不算通过；理由：这是门禁脚本类，与 `AC-ENG-03` 同批落，不能等到 S10 才有扫描——S11 只做映射表复核）；**同一脚本按 D11 覆盖密级写法两个禁词**：`dataLabel(` 与**裸 `clearance(`** 均不得出现在 SDK 三语言仓（`GJ-CAP-13` 已冻结为 `withClearance(level)`；`clearance()` 同属作废、不留别名，Spec 补口 C1 / Gate v9）；扫描正则 **`(?<![A-Za-z0-9_])clearance\(`（大小写敏感，`withClearance(` 不命中）**，**范围限 SDK 仓源码/`examples/`/`README*`/仓内 `docs/`，不含 `/docs/` 与基线**（作废声明本身要能写出旧名）；**变异用例四个注入各验一次**（`SERVICE_DEGRADED`、`STEP_ALREADY_SATISFIED` 越界、`dataLabel(`、`.clearance(`）；`AC-ENG-07` PASS = `gofmt -l` 为空 + `govulncheck`/`trivy` 阻断生效（SDK/前端项此时记 `NOT-IMPLEMENTED`）；`AC-DP-11` 的**静态脚本**在 S8 生效，S1 只交付脚本本体。
- **退出条件**：`make lint` 在空骨架上全绿；门禁脚本在变异注入下确实红；`go list ./...` 全部落在 `github.com/spencerswagger/guanjin/...` 前缀下。
- **阻塞**：无（R7 已收口 → P4）。

### S2 · migrations：14 张表 + `flows`（负责人 Ark，前置：S1、P1、**D5 表结构规格（已追认）**、D4、D2）
- **目的**：schema 唯一权威一次落全，之后所有包只读这个口径。
- **动作要点**：`000000_common_functions`（`set_updated_at`/`forbid_mutation`）+ `000001`~`000014`，顺序 `users→clients→identifiers→authenticators→sessions→challenges→其余`；按 **D4** 落实：主键**不写** `DEFAULT uuidv7()`、`audit_logs` 按月分区且主键 `(id, occurred_at)`、不写 `clients_redirect_unique` 那条非法 CHECK；按 **D2** 把 `challenges.purpose` CHECK 写成 **6 值**并显式命名 `challenges_purpose_valid`；**唯一约束名全部显式**（`signing_keys.kid` 不得依赖 PG 自动名，否则 23505 映射会静默降级）。`000015_create_flows` 按 **D5** 13 列落地（含 `trace_id`；`expires_at` **禁 DB 默认**；`flows_one_active_per_user_client_uniq` 这条**部分唯一索引只能 `CREATE UNIQUE INDEX`**，不得写成 `CONSTRAINT`；I-F4 可加单行 CHECK），并在同一迁移内 **seed `runtime_config('flow.ttl_seconds','600')` + `ON CONFLICT DO NOTHING`**。 **后续追加（S3 证据阶段）：`000016_create_flows_expired_cleanup_idx`** —— 第二段清理的部分索引 `(updated_at) WHERE status='expired'`（口径见 `decisions.md` D5「第二段清理索引裁定」；编号只增不改 `000015`，实现归 Edge，须同步 `flows_named_indexes` 断言 4→5）。
- **验收判据**：`AC-ENG-02` PASS = `migrate up` 全绿 + `down` 可回滚 + 表名逐字一致，**期望值改为 15 张（14 + `flows`）**（见 §6 回填）；`AC-ENG-04` PASS = 23505 各类约束各触发一次并映射到接口 §5.4 的码（约束名 ↔ 错误码映射表齐全）；`AC-AUD-02` PASS = `UPDATE`/`DELETE` 被 `restrict_violation` 拒；`AC-AUD-06` PASS = 分区实际存在、删最老分区后检索不漏（锚点见 S10）；`AC-REG-03` PASS = `purpose='register'` 能落库（D2 生效后无 23514）。
- **退出条件**：`guanjin_test` 库上 up/down 往返一次无损；漂移测试（GORM 模型 ↔ 迁移列）骨架就位。
- **阻塞**：**无（D5 已追认 → 按表结构规格落 `000015`）**；`flows` 的 DDL 由 Ark 出、Edge 复核，Core 依 D5 的 **I-F1~F8**（含 I-F7 `completed` 一次性回填 `session_id`、I-F8 锁序与锁内禁重活）在 `internal/flow` 落地。若前期想抢时间，可先落 `000001~000014` 再补 `000015`，但**不得**在 `flows` 缺位时开工 S5。

### S3 · crypto/store 基座（负责人 Edge，前置：S2）
- **目的**：把国密与数据访问的"不可散落"部分先钉死。
- **动作要点**：**只接受派生键**（D4-B：`Derive(masterKey, purpose, len)`，子密钥 `guanjin/sm4/field|sm4/keywrap|hmac/audit|hmac/identifier|hmac/agent`，编译期不允许把 `masterKey` 直传算法）；`gorm.Open`（禁 `AutoMigrate`）+ `BaseModel/AppendOnlyModel` + UUID v7 `BeforeCreate`；`mapPgError` 全量约束表 —— **按 SQLSTATE + 约束名双键**扩展（`flows_one_active_per_user_client_uniq` 的 23505 → 409、I-F1 的 23514 → 400，禁止"23505 一律 409 / 23514 一律 400"）；**`repo_flows` 必须提供带 tx 的锁读 `LockByID(tx, id)`（内部 `FOR UPDATE`）**，供 Core 在同一事务内完成"锁 → 校验 → 消费 challenge → 累加 amr → 写回"；**`pgxerr` 按 `decisions.md` D4-D₂ 的八行映射表逐条落**（不是"23505 一律 409"）；**`runtime_config` 校验函数要能区分 A/B/C 三态**（D5-₂：env 错 / DB 不可达 / 缺键），接线归 S10；testcontainers（PG16 + redis:7）基架；`runtime_config` 启动校验（**`flow.ttl_seconds` 键缺失即 fail-fast**，代码不留 600 常量）。
- **验收判据**：`AC-ENG-04` 的运行时部分（已在 S2 列）；`AC-FC-03` 的"启动期 fail-fast"半条 PASS = 主密钥缺失/非 base64/<32B → 启动失败且退出码非 0；`AC-AUD-01` 的 `trace_id` 贯穿由本步提供基础设施（PASS 在 S4 验）；**`AC-ENG-01` 增补：`internal/flowstore` 与 `internal/crypto`/`internal/store` 一并纳入逐模块覆盖率下限，其中 `flowstore` = **85%**（口径见 `decisions.md` D8 "覆盖率模块下限补登"行；执行侧由 `scripts/coverage-gate.sh` 的 owner 补一行，本文件只引用）**。
- **退出条件**：单测用标准向量（SM2 签验 / SM3 / SM4 / PBKDF2-SM3）全绿；派生键用途隔离有单测（同一用途两次派生结果一致、不同用途不同）。

### S4 · 标识符 + 凭据 + 密码因子 + 注册面（负责人 Core，前置：S3）
- **目的**：先把"账号能合法存在、能证明自己是自己"做通，注册即登录。
- **动作要点**：`identifiers` 三段式（盲索引 HMAC-SM3 + SM4 密文 + 脱敏）、规范化（email 小写 / phone E.164 / username 小写）、I1/I2/I6 服务层校验；`authenticators` 口令 PBKDF2-SM3（≥100000、盐 ≥16B）+ 历史 N 次不可复用；`/auth/register/start|complete`（必带 `client_id`、按目标 client 准入规则算门槛）；风险计数（**D3-D**：Redis 权威 + `authenticators.fail_count` 镜像）。
- **验收判据**：`AC-REG-01` PASS = 200 + `challenges` 行 + `purpose=register`；`AC-REG-02` PASS = 无 `client_id` → 400 `MISSING_PARAMETER`（**不得**静默按平台默认 L2 放行）；`AC-REG-03` PASS = 见 S2；`AC-REG-04` PASS = 重复 email → `IDENTIFIER_ALREADY_BOUND` 且耗时差异 ≤ 阈值；`AC-REG-05` PASS = 准入规则为"仅用户名+密码"时可跳过挑战；`AC-REG-06` PASS = 建 `users` + 标识符 `verified=true` + 直接返回三件令牌 + 写 `auth_events`；`AC-REG-07` PASS = 口令不合规 → `PASSWORD_POLICY_VIOLATION` 且事务回滚后无残留行；`AC-LOGIN-02` PASS = 错误口令 401 + 可复制 `trace_id` + 无区分性文案；`AC-LOGIN-03` PASS = 2 副本各失败 n-1/n 次后锁定（键 **`login:fail:{user_id}` / `login:lock:{user_id}`**，D3-D）；`AC-AUD-01` PASS = 成功与失败都写 `auth_events` 且含 `trace_id`；**Gate v4 反向断言**：`flows.trace_id` 与后续 `auth_events.trace_id` **不一致仍 PASS**，只有该覆盖没覆盖才 FAIL（D4-C(4)）。
- **退出条件**：注册 → 登录 → 拿到 JWT 的 e2e 在单副本可跑（数据面留待 S8）。

### S5 · flow 编排 + Descriptor + 挑战面（负责人 Core，前置：S4、**D5 flows 表结构规格（已追认）**、D7、**CH-1 已追认（P8）**）
- **目的**：把"按档位阶梯补步"做成状态机 + 唯一契约。
- **动作要点**：`flows` 表读写（D5：`achieved_amr`/`completed_steps`/`expires_at`/`trace_id`，**不用 Redis、不用 cookie**）；Descriptor 按 **D7** 下发 `steps[i].challenge_id/purpose/contributes_acr` + 顶层 `terminal_state`/`unreachable_reason`；`/auth/challenge/{factor}`、`/auth/verify/{factor}`（按能力断言选因子，**禁 `if type==`**）；挑战权威落 `challenges`（Redis 只放限流计数）。**并发与锁（I-F8）**：全站锁序 `sessions → flows`、`sessions` 禁 `FOR UPDATE`、**`flows` 行锁内只做"读该行/校验/消费 challenge/累加 amr/写回"，PBKDF2-SM3（≥10 万迭代）、短信邮件投递、外部 HTTP 一律在锁外**（禁例外，需长耗时计算时走"先算后锁"）。**流归属（CH-1，**已追认（P8）**；v6 起不单独挂判据）**：`challenges` 不加 `flow_id`、不反查，阶梯内由请求显式携带 `flow_id`（`purpose∈{login,step_up}` **必填**，缺省 → `MISSING_PARAMETER`；`AC-FLOW-10` 的断言不依赖必填语义，照旧举证）。**清理两段式（I-F6）**：第一段只置 `status='expired'`（行保留），**7 天后**第二段才 `DELETE`，只删 `status='expired' AND updated_at < now()-7d` 且命中 `flows_expires_idx`。**交接回填（I-F7）**：`completed` 行允许**一次** `session_id: NULL→非 NULL`（S5 完成 / S6 建会话），之后不得再改。
- **验收判据**：`AC-FLOW-01` PASS = 字段齐且与页面 §4.1 + D7 一致；`AC-FLOW-02` PASS = 目标 AAL2 已达成 AAL1 → **403 `ACR_INSUFFICIENT` + `details.flow`**（不是 500/401/空白）；`AC-FLOW-03` PASS = `done=true` 再发起 → **409 `STEP_ALREADY_SATISFIED`**（D3-F，本轮新增码）且三条语义成立（不产生新 challenge 行 / `amr` 不重复 / `completed_steps` 不重复追加）；`AC-FLOW-04` PASS = 禁用因子不出现、不计入 `current_acr`、直接调用 → `FACTOR_DISABLED`；`AC-FLOW-07` PASS = 因子全禁用 + 目标 AAL2 → `terminal_state='unreachable'` + `unreachable_reason.code='FACTORS_DISABLED'`；`AC-FLOW-09` PASS = 无上下文自助操作按 `user_visited_clients` join `clients` 取 max（访问过 L3 后门槛 = L3）；**`AC-FLOW-10` PASS（两段式定稿后可写稳定断言）**：①`expires_at` 到期后首次访问 → **必得 `FLOW_EXPIRED`**（因行仍在，不会时隐时现）；②`completed` 后二次提交 → `FLOW_NOT_FOUND`；③`status='expired'` 但未满 7 天的行**仍然存在**（`SELECT count(*)=1` 举证），满 7 天后清理器才删；④无 `deleted_at`、不进审计链；⑤会话撤销 → 该会话活跃流置 `abandoned`；⑥清理器 `EXPLAIN` 命中 `flows_expires_idx` 且**只扫** `status='expired'`；**`AC-FLOW-11` PASS = `flows` 并发与唯一性**（完成态 `user_id` 为 NULL 被 CHECK 拒 → 23514 映射 **400**；同 `(user_id, client_id)` 第二条活跃流撞 `flows_one_active_per_user_client_uniq` → 23505 映射 **409**，匿名流不受限；N 线程同 `flow_id` 并发补步经 `FOR UPDATE` 后 `achieved_amr` = 各步并集、不丢不双写，且**锁内无 PBKDF2**——用一次 10 万迭代口令校验的并发用例举证不雪崩）；`AC-FC-06` PASS = `redis-cli FLUSHALL` 后挑战仍可判定（不"蒸发"）。**状态标注（v6）**：本条里 `AC-FLOW-01`/`AC-FLOW-07` 记 **`PENDING·CH-2`**、`AC-FLOW-03` 记 **`PENDING·CH-4`**，追认前不计入通过（其余 `AC-FLOW-02/04/09/10/11`、`AC-FC-06` 均为 `PASS-able`）。
- **退出条件**：一条"目标 AAL2 的 L3 客户端 + 只有密码"的链路能稳定给出 `ACR_INSUFFICIENT`→补步→再判定的完整循环。

### S6 · 协议面令牌签发（负责人 Core，前置：S5；D3-E 双码分工已收口；**D13 `login_policies` 三档 seed 已落 `000017`，启动期 fail-fast 由 Core 补**）
- **目的**：把 OIDC 面做完，签出 `SM3withSM2` 令牌。
- **动作要点**：`/authorize`、`/authorize/submit`、`/oauth/token`（授权码一次性、refresh 轮换 + 复用即撤销会话）、`/oauth/revoke`、`/oauth/introspect`（低频）、`/userinfo`（**本地解析，零查库**）、`/.well-known/openid-configuration`、`/.well-known/jwks.json`（双密钥 `kid` 前缀区分，`kty=EC/crv=SM2`）。 **策略来源（D13）**：`/authorize`/`/authorize/flow` 读 `login_policies` 的 `tier_default`（L1/L2→AAL1、L3→AAL2，由 `000017` seed）；`cmd/guanjind` 装配期必须校验三档各恰一行，**缺失/多行 → 非 0 退出**（不得退化成 500）。
- **签名私钥的解封口径（D15④，`signing_keys.private_key_enc`）**：信封 v2 = `0x02 | iv | ct | HMAC-SM3`，**先验 MAC 再解密**；**启动期**打不开（`ErrIntegrityMismatch` / `ErrCiphertextVersion`）→ **非 0 退出并打印 `kid` 与原因**（`D5-₂` 三态：数据/配置坏了就起不来），**不得**带着坏密钥对外服务、也不得把 500/503 返回给客户端。运行期只有"密钥不在内存/未激活"才是 `503 KEY_UNAVAILABLE`；`kid` 未知属配置 → `INVALID_PARAMETER`/400（D3-B 既有码）。
- **验收判据**：`AC-LOGIN-01` PASS = JWT `alg=SM3withSM2` + `kid` + `acr=AAL1`/`amr=["pwd"]`，`/userinfo` 用「该请求 SQL 计数 = 0」证明本地解析；`AC-LOGIN-04` PASS = 授权码二次 → `INVALID_GRANT`，refresh 二次 → `REFRESH_TOKEN_REUSED` + 会话撤销；`AC-LOGIN-05` PASS = verdict 按 **D3-E** 分工（缺/畸形 verifier → `INVALID_CODE_VERIFIER`；不匹配 → `PKCE_VERIFICATION_FAILED`）；`AC-LOGIN-06` PASS = discovery 声明与实际签发**交叉校验**一致；`AC-LOGIN-07` PASS = 两类密钥同时发布且 `kid` 可区分；`AC-LOGIN-08` PASS = 1k 次受保护请求后 introspect 计数 = 0。
- **退出条件**：一条 curl 链路（authorize→token→userinfo）可演示；discovery 与行为一致。

### S7 · 会话管理 + 实时撤销 + 管理面 sessions（负责人 Core，前置：S6）
- **目的**：撤销必须**实时**，这是"M1 含数据面"的原因。
- **动作要点**：`sessions` 生命周期（idle/absolute 双超时按 `login_policies`）、撤销与级联（refresh/access 同步失效）、并发上限与溢出动作；管理面 `GET/DELETE /api/v1/sessions*`；三员鉴权矩阵的最小骨架（审计员不得写入）。
- **验收判据**：`AC-SESS-01` PASS = 撤销后**立即**用同一 access_token 经数据面 → 403 `SESSION_REVOKED`（E3；被 `revoke` 端点撤销的令牌走 `TOKEN_REVOKED`，D3-E），无任何缓存窗口；`AC-SESS-02` PASS = 管理面撤销同样实时 + 写 `auth_events`；`AC-SESS-03` PASS = 撤销后新登录新 `session_id`，旧永久失效；`AC-SESS-05` PASS = `replay-store=memory` 在多副本配置下启动失败/告警（不静默降级）；`AC-AUD-07` 的"审计员零写入"单副本先行。
- **退出条件**：撤销延迟可测量（`revoked_at` → 首个 403 的时间 < 1 个请求往返）。

### S8 · 数据面 `cmd/guanjingw` 7 件事（负责人 Edge，前置：S7）
- **目的**：把"流量必过数据面"这条唯一形态跑起来。
- **动作要点**：`httputil.ReverseProxy`（连接池/超时/背压，不套 Gin 热路径）；验签 + 会话校验 + 按 client 档位判 ACR；逐请求签 `X-GuanJin-Assertion`（SM2、`exp=iat+60`、`kid`）；**剥离客户端自带同名头（先弃后签）**——这是**网关侧**的剥离，与 SDK 侧"验签后剥离网关现签的头"是两件事（后者见 **D10**，不在本步）；实时撤销判定；单点登出签名通知（失败不阻塞本端）；`route → client` 静态映射（域名/路径前缀）。
- **验收判据**：`AC-DP-01` PASS = 上游能用 JWKS 验签通过且 claim 齐全；`AC-DP-02/03` PASS = 伪造头零透传、下游下一跳收不到；`AC-DP-04~08` PASS = 篡改/未知 kid/缺令牌/`exp`·`iat` 越界/`jti` 重放 **全部 401 且不注入身份**（边界按 `skew±1s` 打点；其中 **`jti` 二次的 401 由验签方产生，S8 只举证不产生**——Spec 裁定 2026-11-10，见 `decisions.md` D16①）；`AC-DP-09` PASS = 档位不足 → 403 `ACR_INSUFFICIENT` + `details.flow`（非 302 死循环）；`AC-DP-10` PASS = 全量日志 grep 令牌/断言/口令/标识符明文 **命中 0**；`AC-DP-11` PASS = `guanjingw` **不自己实现 TLS/WAF/LB/灰度/熔断**（无 `tls.Listen`、无熔断库、无业务路由发现；静态脚本 + 依赖清单举证）——这些由前置 nginx/Kong 承担、**不属本平台验收面**，本平台**不做任何 TLS/TLCP/证书判定**，且**不设"跳过 TLS"的暗门**；非 TLS 判据（`AC-DP-01~10`、`AC-DP-13`）**一条不减**；`AC-DP-12` PASS = 判据全句以 `acceptance-criteria.md:188` 为准（**走 SM2 签名发往已访问业务系统 + 通知失败不阻塞本地登出**）；SLO 通知的 wire 形态（路径/报文/事件名）**待 `R12` 裁定**，裁定前不得自造；**`AC-DP-13` PASS = 上游失败语义（Gate v3 新增，归位此处）**：上游超时/拨号失败 → **503 `UPSTREAM_UNAVAILABLE`**（不是 500、不是 `DEPENDENCY_UNAVAILABLE`、不是 `SERVICE_DEGRADED`），且**不注入** `X-GuanJin-Assertion`；`AC-SESS-04` PASS = 2×`guanjind` + 2×`guanjingw`，A 登录 / B 撤销 / A 立即失效，**不依赖粘滞**。
- **退出条件**：一条真实转发链路可演示（含伪造头攻击用例）。

### S9 · 最小 SDK 接入（负责人 Nova，前置：S8、`/docs/sdk-contract.md`（**Nova 唯一 owner，落 PR #1；本文件不复制其内容、不写死版本号**）、D6、**CH-5 已追认（P8，仅「29 项交付集 + 删 `guanjin.mode`」部分；`jwks-uri`/`replay-store`/`agent.pull.enabled` 三处默认值按 D6(3)/(5)，`AC-SDK-02` 仍是 `PASS-able · D6(4)`）**）
- **目的**：在业务系统侧证明"验签 + 剥离 + 判定"闭环。
- **动作要点**：`guanjin-core`（零 Spring/servlet：SM2 验签、claim 校验、`jti` 重放、JWKS 拉取缓存、fail-fast）+ `guanjin-spring-boot2`（`javax.servlet` Filter，位置在 `UsernamePasswordAuthenticationFilter` 之前）；**剥离按 D10 的六步时序（唯一口径）**：读原始头 → 验签 → 校验 `exp/iat/iss/aud/jti` → 注入上下文 → **替换请求视图**（handler/下游/日志都取不到）→ handler，**禁止"验签前剥离"**；配置按 **D6**：29 项交付集、无 `mode`、`jwks-uri` 默认 `/.well-known/jwks.json`、`enabled/authz.enabled=false` 仅 `local|test` 否则启动失败、`replay-store` 默认 `redis`、`agent.pull.enabled` 默认 `false`。
- **验收判据**：`AC-SDK-01` PASS = `direct` 不可配置（设为 direct → 启动失败）；`AC-SDK-02`（**`PASS-able · D6(4)`**，v6 已降级、**不进 CH**）PASS = `enabled=false` / `authz.enabled=false` 在**非 `local|test`** profile 下**启动失败**、在 `local|test` 下仍完整验签（篡改令牌必须 401）——**理由写死**：非 `local|test` 置 `false` 本就是基线禁止项（注释性引用 SDK §3.2「验签不可关」+ 移交 §九「不存在生产可跳过验签的开关」），D6(4) 只是把「告警」升级为「启动失败」使禁令**可执行**，属**加固**而非新增契约面；判据取三分判据「**是否让业务方能用基线原本不允许的方式配置 SDK？**」→ **不能**；`AC-SDK-03` PASS = 默认配置首启即命中真实 JWKS（404 即失败；`jwks-uri` 默认值属 **D6(3)/(5) 纠错性**，已为 `PASS-able`）；`AC-SDK-04` PASS = 平台地址缺失/非法 → 启动失败；`AC-SDK-05` PASS = 有缓存时沿用缓存**仍真实验签**（篡改令牌 401）、无缓存 → **503 `KEY_UNAVAILABLE`**（**不得**用 `DEPENDENCY_UNAVAILABLE`、不得用已废弃的 `SERVICE_DEGRADED`）；`AC-SDK-06` PASS = `jti` 存储不可用 → 拒请求；`AC-SDK-07` PASS = 反射扫描 `GuanjinProperties` ↔ 契约表**逐项一致**，**数量期望 29 项**、三处默认值按 **D6(3)/(5)**（`jwks-uri` 路径、`replay-store=redis`、`agent.pull.enabled=false`）；`AC-SDK-08` PASS = **按 D10 六步**（读原始头→验签→claim→注入→**替换请求视图**→handler）四条断言同时成立：①handler 侧 `getHeader` 为 `null` 且 `getHeaderNames` 不含该名；②下游业务转发与日志两个消费者同样取不到（共三处）；③**反向断言**：篡改令牌仍 401（验签在剥离之前，不得"没令牌可验"）；④Filter 顺序在 `UsernamePasswordAuthenticationFilter` 之前；且禁止"验签前剥离"与"只在下游转发前删一次"；`AC-SDK-09` PASS = `dependency:tree` 中 core 无 `org.springframework.*`/`javax.servlet.*`；**`AC-SDK-10`（Gate v4 新增，归位此处）** PASS = `agent.pull.enabled` 默认 `false`，启动后 **30s+ 内 `/api/v1/agent/policies` 零次调用**（默认不回源，依据 D6(5) + 索引 §4.2-24）；`AC-FC-04` PASS = 首启不可达 → **503 `KEY_UNAVAILABLE`**；`AC-FC-05` PASS = 配置键扫描与 D6 交付集（29 项）逐项对齐，多一个键即失败（三处默认值已按 D6(5) 定稿，**不再是变量**）。
- **退出条件**：一个最小 Spring Boot 2.7 demo 服务跑通「SDK 验签 → 取到身份 → 下游无断言头」。
- **阻塞**：**无**——`sdk-contract.md` 由 Nova 维护（PR #1，当前 v7，含 N1–N8 命名原则、能力→端点表、29 项安全约束列）；**本文不复述其内容**（避免两处口径打架、也避免版本漂移）。本切片只依赖三件事：能力数 **22**、配置数 **29**、D10 的六步剥离时序与 D11 的 `withClearance(level)`/`objLevel`。契约字段名/配置 key **一律以该文件为准**，不得自造。

### S10 · fail-closed 故障注入 + 审计链校验 Job（负责人 Edge，Gate 驱动，前置：S9、D4、D8）
- **目的**：证明"依赖挂了就拒服"，并把审计链做成可校验的。
- **动作要点**：`/readyz` 反映 DB/Redis/密钥；**并接手 `runtime_config` 三类失败的接线**（D5-₂：A env 错 → 启动退出；B DB 不可达 → 进程活着但 `/readyz` 503 `DB_UNAVAILABLE`、业务全拒；C DB 可达但缺键 → 启动退出并打印缺失键名）；异常映射按 **D8-C / C′**（`REDIS_UNAVAILABLE`/`DB_UNAVAILABLE`/`KEY_UNAVAILABLE`，`DEPENDENCY_UNAVAILABLE` 仅兜未分类、`UPSTREAM_UNAVAILABLE` 只给数据面转发失败、**`SERVICE_DEGRADED` 已废弃——实现里不许出现**，统一 503 + `Retry-After`）；审计链：单全局链 + `pg_advisory_xact_lock`、分区切换与每日任务写锚 `runtime_config`（`audit.chain.anchor.<YYYYMM>` / `segment.<YYYYMM>`）、`cmd/guanjind` 内定时整链校验（**不新增二进制**）+ 两个指标 `audit_chain_broken_total` / `audit_anchor_age_seconds`。
- **验收判据**：`AC-FC-01` PASS = 真停 Redis → 认证端点一律 503 `REDIS_UNAVAILABLE` + `/readyz` 非 200 + 期间**零**带身份 200；`AC-FC-02` PASS = 停 PG → 503 `DB_UNAVAILABLE`（D8-C）且不注入身份；`AC-FC-03` PASS = 主密钥缺失 → 启动 fail-fast；运行期加密/签发失败 → `KEY_UNAVAILABLE`，**不产出任何可用令牌**；`AC-FC-07` PASS = 三种故障下探针扫全部非白名单端点 + 数据面通配，**没有一个**带 `sub`/断言；`AC-FC-08` PASS = `/userinfo` 在 DB 挂时仍 200 且 SQL 计数 = 0（**D8-D′**：零查库才允许 200，一旦查库必须 503）；`AC-AUD-03` PASS = 删中间一条 → 后续校验断链可检出；`AC-AUD-04` PASS = 并发 100 写不断链且全链复核通过；`AC-AUD-05` PASS = 构造被篡改行 → 完整性状态变"校验失败"（**时机/范围/是否阻断导出**按 D4-C：分区内逐条 + 锚比对，`verified|broken|unverified`）；**`AC-AUD-11` PASS = 链锚与校验 Job**（每日定时整链校验落在 `cmd/guanjind` 内、无新二进制；人为篡改一条后下一次任务使 `audit_chain_broken_total` 递增且状态置 `broken`；锚超期使 `audit_anchor_age_seconds` 超阈值告警）。
- **退出条件**：三种故障都能被**真实容器停掉**复现（禁 mock），且有证据留档 `artifacts/<commit>/<AC-ID>/`。

### S11 · M1 收口（负责人 Gate，Spec 出验收，前置：S10）
- **目的**：把 M1 变成"可演示 + 可验收"的整体，而不是一堆绿测试。
- **动作要点**：跑通单条 e2e（注册 → 登录 → 补步判定 → 访问业务接口 → 撤销 → 403）；输出 `AC-ID → PASS/FAIL/PENDING/NOT-IMPLEMENTED/NOT-APPLICABLE/后置（DEFERRED）` 映射表（「后置」栏表头写全名 **「后置（DEFERRED）：本期不做判定」**）；覆盖率门禁按技术栈 §9.3；M2 项显式标 `NOT-APPLICABLE` 并注明归属；**密评整体后置** → `AC-AUD-10` 记 **「后置（DEFERRED）：本期不做判定」**（`BLOCKED` 归零，依据 D8-B【正式裁定】）：**不得写成通过、不得写"预检通过"、不得从账目里摘掉**（仍占 1 条、单列「后置」栏；§11.1 的 7 项输入仍须在 M2 之前备齐——后置 ≠ 免验）；等保相关的**代码要求照基线实现**（双因素/审计不可篡改/三员分权/脱敏留痕），后置只是"不出证、不送检"。
- **验收判据**：`AC-ENG-01` PASS = 各模块覆盖率不低于下限；`AC-ENG-05` PASS = 映射表存在且**无 `NOT-IMPLEMENTED`**，`PENDING·CH-n` 与「后置」各自单列、一律**不写成 PASS**（**账目 = 83 `PASS-able` / 0 `PENDING` / 0 `BLOCKED` / 6 N/A / 0 `NOT-IMPLEMENTED` / 1「后置（DEFERRED）：本期不做判定」= 90**；`PENDING` 栏仍必须**单列输出**，本版为 0 条）；`AC-ENG-06` PASS = 证据留档 + 失败项带复现命令与 `trace_id`；**`AC-ENG-08` 复核**（S1 已建脚本并变异验证，S11 只需确认它仍在门禁中、未被跳过）——**四注入三选一（v9 起四个）都必须红**：①`SERVICE_DEGRADED` 永不出现在平台侧码表/返回（D8-C′⑤）；②`STEP_ALREADY_SATISFIED` 只许由 `internal/flow` 返回、出现在包外即红（D3-F）；③`dataLabel(` 与**裸 `clearance(`** 永不出现在 SDK 源码/示例/文档，唯一写法 `role(name).withClearance(level)`（D11；正则 `(?<![A-Za-z0-9_])clearance\(`、范围限 SDK 三语言仓、四个注入各自单验）；**`AC-AUD-12` PASS = 应用侧全链路国密实证**（令牌 `SM3withSM2` / 审计 `HMAC-SM3` / 敏感字段 `SM4` / 口令 `PBKDF2-SM3` 迭代≥100000·盐≥16B / 全仓算法扫描**不含 SHA 系与 RSA**——**唯一白名单：PKCE `S256`（R5）** / 主密钥只做 HKDF-SM3 派生不进业务算法，任一项出现即 FAIL）；`AC-AUD-09` 按 M2 记 `NOT-APPLICABLE`、`AC-AUD-10` 按「**后置（DEFERRED）：本期不做判定**」记账（依 D8-B【正式裁定】+ spencer 口径 2026-11-02，**`BLOCKED` 归零**；R2/R4 挂起、R3 已出本平台边界；7 项输入仍须备齐）。
- **退出条件**：一份可交给 spencer 的 M1 演示脚本 + 一张验收表；M2 切片输入清单就绪。

---

## 5. 阻塞矩阵（R1–R9 → 卡哪步；今晚能动手 vs 必须等拍板）

> **已收口（经 hugo 追认，2026-11-02）**：**R1** → `decisions.md` **D5**（新增第 15 张表 `flows`，本轮只定表结构规格，`.sql` 随骨架落盘）；**R7** → `decisions.md` **P4**（Go module path = `github.com/spencerswagger/guanjin`）。二者不再阻塞 S1/S2/S5。

| 阻塞 | 卡住的步骤 | 影响与规避 |
|---|---|---|
| ~~R7~~ **已收口** | 原卡 S1 → 现按 P4 定稿 | module path = `github.com/spencerswagger/guanjin`（内部包前缀同此），S0/S1 直接写死 |
| ~~R1~~ **已收口** | 原卡 S2/S5 → 现按 D5 | 第 15 张表 `flows` 已追认；S2 出 `000015`、S5 依 **I-F1~F8** 落地，不再有"无中间态落点"的退化方案 |
| R2 纯软国密是否被认可 | `AC-AUD-10`（**「后置（DEFERRED）：本期不做判定」**，`BLOCKED` 归零） | **后置（DEFERRED）：本期不做判定、不出证、不送检**（spencer 口径 2026-11-02 + `decisions.md` D8-B【正式裁定】）；**后置 ≠ 免验**——§11.1 的 7 项输入仍须在 M2 之前备齐。不影响 M1 编码：代码仍按 P2① 纯软国密实现 |
| R3 传输段（TLS/国密 TLS/TLCP/证书）归属 | 无 M1 判据（`AC-DP-11` 只判「不自己实现」，不判 TLS 本身） | **已出本平台边界、关闭**：TLS/国密 TLS/TLCP/证书校验均由部署方前置承担，平台只见明文——**不写代码、不设配置项、不做任何 TLS/TLCP/证书判定、不进验收判据**（P2②），且**不设"跳过 TLS"的暗门**；`AC-DP-11` 的"不自己实现"仍要测 |
| R4 密评送检 | `AC-AUD-10` | **「后置（DEFERRED）：本期不做判定」**：本期不送检、不判定（spencer 口径 2026-11-02；D8-B【正式裁定】已给出摘除 `BLOCKED` 的依据）；等保代码要求照基线实现；7 项输入仍须备齐 |
| R5 PKCE S256 与国密口径 | 不卡 M1（按接口 §3.1.3 用 S256） | 若后续要求改 S256→国密变体，属 OIDC 兼容性变更，需 ADR |
| ~~R6~~ **已收口（经 hugo 追认 2026-11-02）** | 原「旧仓定位」→ 现按 **P5** | 旧仓 SpIAM **冻结归档、只读参考**：不改 / 不 push / 不开分支；代码只进 `GuanJin`。与 M1 无技术依赖，但是全员红线 |
| R8 数据面 QPS 目标 | S8 压测（不卡功能） | M1 只做功能 e2e；压测基线待 R8 |
| R9 归档密钥保管 | S10（锚点已实现，归档动作推后） | 365 天内不触发删分区，M1 只需"分区 + 锚 + 校验 Job" |

**今晚就能动代码**：**22:05 开工，按本文从 S0 走**（S0 = 文档骨架 + **14 个基线文件** + 四份项目文档 + 5 篇 ADR 入库 + **开 PR**）→ S1（module path 已定稿）→ S2 含 `000015_create_flows`（按 D5 规格 + seed `flow.ttl_seconds`）→ S3 → S4 → S5（含 `AC-FLOW-10/11`）。
**仍需等外部结论的**：**本期无**——密评整体后置，`AC-AUD-10` 按 **「后置（DEFERRED）：本期不做判定」** 记账（`BLOCKED` 归零，依据 D8-B【正式裁定】：Gate 的 `BLOCKED` 定义前提是"依赖残余待裁"，而 R2/R4 已挂起、R3 已关闭）。**但后置 ≠ 免验**：`acceptance-criteria.md` §11.1 的 7 项输入（I-1~I-7）**仍须在 M2 之前备齐**，否则 M5 的等保密评证据链闭不上；「后置」项不得写成通过、不得写"预检通过"、不得从账目里摘掉。`AC-AUD-12`（应用侧国密实证）仍可独立判定。**等保相关的代码要求不受后置影响**：双因素、审计不可篡改、三员分权、脱敏留痕照基线实现，只是本期不出证/不送检。R5/R8/R9 不卡 M1 编码（状态保留在 `decisions.md`，R5 的 S256 已在 `AC-AUD-12` 里列为唯一白名单）。
**契约变更：CH-1~CH-5 已全部追认（经 hugo 追认，2026-11-02，见 `decisions.md` P8）**，S5/S6/S9 与全站分页按清单实现即可；CH-6 为内部口径。**判据侧**：**Gate 已在 v11 把 13 条 `PENDING·CH-n` 一次转 `PASS-able`**（**90 = 83 / 0 / 0 / 6 / 0 + 1 后置**，断言一字未改、只改状态列），本文数字与 `acceptance-criteria.md` v11 一致、**不再有「待 Gate 更新」的悬空引用**。**CH-1（`/auth/challenge` 新增必填 `flow_id`）直接决定 S5 的端点签名**——追认后按**必填**实现（缺省 → `MISSING_PARAMETER`），不再有"可选 + 兜底"占位。**挂判据的口径（原 v6 标注，追认后 PENDING 已解除）**：CH-2 → `AC-FLOW-01`/`AC-FLOW-07`；CH-4 → `AC-LOGIN-05`/`AC-FLOW-03`/`AC-SESS-01`/`AC-AUD-07`/`AC-ENG-08`；CH-5（仅「29 项 + 删 `mode`」）→ `AC-SDK-01`/`AC-SDK-07`/`AC-FC-05`；**CH-1 不单独挂判据**（`AC-FLOW-10` 的断言不依赖必填语义）、**CH-3 不挂 M1 判据**（只影响 M5 的 `AC-AUD-08`）。**追认前合计 13 条 `PENDING·CH-n`（Gate v10），已随 P8 在 v11 全部转 `PASS-able`（90 = 83/0/0/6/0+1 后置）**；逐条点名的仍是上面 10 条老判据，v10 新增的 `AC-REG-08/09/10`（CH-4④⑤）随同一批追认解除。

---

## 6. Gate 回读后的期望值对齐（已全部收口，无待补项）

Gate 于 2026-11-02 按 `decisions.md` 回填并迭代到 `acceptance-criteria.md` **v11**（`AC-AUD-10` 的呈现按 **D8-B【正式裁定】**统一为「后置（DEFERRED）：本期不做判定」、`BLOCKED` 固定归零；**v11 按 P8 把 CH-1~CH-5 追认落账 → 13 条 `PENDING·CH-n` 一次转 `PASS-able`，账目 `90 = 83 PASS-able / 0 PENDING / 0 BLOCKED / 6 NOT-APPLICABLE / 0 NOT-IMPLEMENTED / 1「后置」`**；ID 未重排、断言未改；v10 另按 **D12** 加 `AC-FLOW-12/13`、按 **CH-4④⑤** 加 `AC-REG-08/09/10`；`AC-SDK-03`/`AC-SDK-02` 已按 D6(3)/(4) 降为 `PASS-able`）。它提出 4 处需我确认的期望值，**结论已全部写入 `decisions.md`**，本节只留对照表备查：

| # | 事项 | 唯一结论（`decisions.md` 位置） | 影响 AC |
|---|---|---|---|
| D3-E | 同场景双码分工 | **按语义分工，不二选一**：PKCE 缺/畸形→`INVALID_CODE_VERIFIER`、不匹配→`PKCE_VERIFICATION_FAILED`；`/oauth/revoke` 撤销→401 `TOKEN_REVOKED`、会话撤销→403 `SESSION_REVOKED`；三员越权→`THREE_POWER_VIOLATION`(E7)、缺角色→`INSUFFICIENT_ROLE`(E3) —— 见 **D3 裁定 E** | AC-LOGIN-05、AC-SESS-01、AC-AUD-07 |
| D3-F | `done` 步骤重输的码（原 G2） | **新增 `STEP_ALREADY_SATISFIED`（E4/409）**，只由 `internal/flow` 返回；不借用 `CHALLENGE_CONSUMED`/`ACR_INSUFFICIENT` —— 见 **D3 裁定 F** | AC-FLOW-03 |
| D8-D′ | `/userinfo` 在 DB 挂时 | **零查库才允许 200**（SQL 计数=0，不算降级放行）；一旦查库必须 503 `DB_UNAVAILABLE` —— 见 **D8 裁定 D′** | AC-FC-08 |
| D8-C′ | `SERVICE_DEGRADED` 用途（原 G1） | **明确废弃、永不返回**（与 `DEPENDENCY_UNAVAILABLE` 重叠必造双轨）；`UPSTREAM_UNAVAILABLE` 归数据面转发失败；CI 增禁码扫描 —— 见 **D8 裁定 C′** | AC-FC-01/02/07 |
| 回填 | `AC-ENG-02` 期望表数 | **15 张**（14 + `flows`） | AC-ENG-02 |
| 回填 | `AC-SDK-07` 期望配置数 | **29 项**（D6 作废"32 项"） | AC-SDK-07 |
| Edge/Core 对齐 | `flows` 表的 6 处并入（第 13 列 `trace_id` / seed 600 三条件 / 锁序 `sessions→flows` + 锁内禁重活 / 清理两段式 / `completed` 一次性回填 `session_id` / `challenges` 不加 `flow_id`） | **全部采纳**，已写入 `decisions.md` D5 的 I-F6/I-F7/I-F8 与落地方案，并标「Edge/Core 对齐结论（2026-11-02）」（**非追认标注**） | AC-FLOW-10/11、AC-AUD-01 |
| 契约变更 | 对外可见变更 6 条（CH-1~CH-6） | **CH-1~CH-5 已全部追认（经 hugo 追认 2026-11-02，见 `decisions.md` P8）**；CH-1 的 `flow_id` 按**必填**实现（`AC-FLOW-10` 的断言仍不依赖必填语义）；CH-6 为内部口径。**原先挂 `PENDING·CH-n` 的 13 条已在 v11 转 `PASS-able`**（**90 = 83/0/0/6/0+1 后置**，只改状态列、断言未改）—— 见 `decisions.md`「契约变更清单」：**CH-2**（Descriptor 新字段）= `AC-FLOW-01`、`AC-FLOW-07`；**CH-4**（错误码三变）= `AC-LOGIN-05`、`AC-FLOW-03`、`AC-SESS-01`、`AC-AUD-07`、`AC-ENG-08`；**CH-5**（**仅**「29 项交付集 + 删 `guanjin.mode`」）= `AC-SDK-01`、`AC-SDK-07`、`AC-FC-05`。**CH-1 不单独挂判据**（S5 的 `flow_id` 必填由新契约测试覆盖）；**CH-3 不挂 M1 判据**（只影响 M5 的 `AC-AUD-08`） | `AC-FLOW-01`、`AC-FLOW-07`、`AC-LOGIN-05`、`AC-FLOW-03`、`AC-SESS-01`、`AC-AUD-07`、`AC-ENG-08`、`AC-SDK-01`、`AC-SDK-07`、`AC-FC-05`（共 10 条） |
| Gate v5/v6 回读（G9/G11） | ①**G9**：账目 66/12 → **67 PASS-able / 11 `PENDING`**（卷首、§6、§7、§8 DoD 全部改齐，卷首版本号 v4→**v5**）；②**G11**：`AC-SDK-02`（profile 门控）**定为 D6(4) 纯加固裁定、不进 CH 清单**→ 降为 `PASS-able`（账目 **68/10**，v6 已落地）；③`dataLabel(` 并入 `AC-ENG-08` 禁词扫描 | 3 处全部采纳（标「Gate 回读（2026-11-02）」）。**G11 选②**：`enabled/authz.enabled` 在非 `local\|test` 置 `false` 本来就是基线禁止项（SDK §3.2「验签不可关」+ 移交 §九「不存在生产可跳过验签的开关」），D6(4) 只是把"告警"升级为"启动失败"使其**可执行**，属**加固**而非新增契约面；判据取三分判据「是否让业务方能用基线原本不允许的方式配置 SDK？」→ 不能 | AC-SDK-02、AC-ENG-08 |
| Gate v6.1 回读（G12） | **同一事实两种标签**（`AC-AUD-10`） | **按 spencer 口径定为唯一一种呈现**：「后置（DEFERRED）：本期不做判定」，`BLOCKED` 归零——依据写成 `decisions.md` **D8-B【正式裁定】**（Gate 的 `BLOCKED` 定义前提是"依赖残余待裁"，而 R2/R4 已挂起、R3 已关闭，前提消失）；**后置 ≠ 免验**（§11.1 的 I-1~I-7 仍须 M2 之前备齐）、不得写"预检通过"、不得从账目摘掉。标「Gate 回读（2026-11-02）」 | `AC-AUD-10` |
| Nova 回读 | D1 补 **A1 `PATH` 取法**（含 context-path、禁 path 重写）与 **A2 `SORTED_QUERY` 算法**（重复键保留、编码先于排序、name→value 两级排序）；D6 补默认值（`replay-store=redis`、`agent.pull.enabled=false`） | **4 条全部采纳**（标注「Gate/Nova 回读（2026-11-02）」）；D1 两条是 SDK↔平台互验的唯一口径，D6 两条只改默认值列、不动 29 项数量 | `AC-AGENT-01~03`、`AC-SDK-07`、`AC-SESS-05`、`AC-FC-05` |
| Spec 复校 | ①断言头剥离时序三处互斥 → 新增 **D10**（唯一六步时序）；②CH-5 同类不同标 → **D6(5) 统一为「纠错性默认值」**、`jwks-uri` 移出 CH-5；③`GJ-CAP-13` 密级方法两套写法 → 新增 **D11** 冻结 `withClearance(level)` | 3 处全部收口（标「Spec 复校（2026-11-02）」）；S9 动作要点与 `AC-SDK-08` 已按 D10 改；S8 补注"网关侧先弃后签 ≠ SDK 侧剥离" | `AC-SDK-08`、`AC-SDK-03`、`AC-SDK-07` |
| Spec 终校（D-1/D-2） | ①**D-1**：`PENDING` 口径 `12→11→**10**`（`AC-SDK-03` 纠错降 1、`AC-SDK-02` 按 D6(4) 再降 1），CH-5 判据列删 `AC-SDK-02`；②**D-2**：**D11 补客体密级入口 = 方案①**（不做声明式打标，业务在调用点传 `Authz.checkLabel(objLevel)`），**不进 CH 清单、登记 ADR-004** | 两处已收口（标「Spec 终校（2026-11-02）」）；**只影响 M5**（`GJ-CAP-16` 语义不变、22 能力不变），不动 M1 任何步骤与编号 | （无 M1 判据）`GJ-CAP-16` 授权 API 归 M5 |
| **Spec 复校二轮（C1/B1/C2）** | ①**C1**：裸 `clearance()` 同属作废（唯一写法 `withClearance(level)`），门禁扩为两个禁词 + 正则 + 范围 + 四注入；②**B1**：`level` 词表冻结 **1=内部/2=秘密/3=机密**，「公开」= 不声明、无 0 级；③**C2**：`checkLabel(objLevel)` 形参名统一 | 三处已收口（标「Spec 复校二轮（2026-11-02）」）：`decisions.md` **D11** 补 C1/B1/C2；**Gate 已在 `acceptance-criteria.md` v9 落 `AC-ENG-08`③**；**S1/S11 的"三禁词"表述已同步改为四个注入**；B1 词表随 **ADR-004** 入库 | `AC-ENG-08`（v9）；B1/C2 只影响 M5（`GJ-CAP-13/16`），**不挂 M1 判据** |
| **密评等级口径（第 10 项，D8-E 第二版）** | ①**密评整体后置**：`AC-AUD-10` = 「后置（DEFERRED）：本期不做判定」、`BLOCKED` 恒 0（D8-B【正式裁定】）；②**「密评上限为二级」已作废**——密评等级**随接入系统备案等级对齐、不得低于备案等级**，故实现按 **GB/T 39786 第三级条款固定、二级为其子集**，条款引用**保持 8.x 不降 7.x**（降级等于把已按三级实现的能力自降标准）；③数据模型 §3.13 的「密评三级 8.4 g)/8.3 c)」**实现要求照做**，依据是索引 §4.2-27、**不是"三级专属"**，不得据此新增三级专属实现或判据；④`L2` 是**客户端档位默认值**（承诺 AAL1），「等保三级」是**平台定级**，两者不同维度、不得互相推导；⑤**TLS 责任转移**（部署方前置承担），**不写"无影响"**；⑥**密码产品不承诺"密评通过"**（算法库 ≠ 密码产品，纯软对 37092 二级无解） | 六处已统一为 **D8-E（第二版，2026-11-02）**；旧版「上限二级」结论作废并已在 `decisions.md` 标注；等保**代码**判据 `AC-AUD-02/03/04/07/09/12` 一条不动 | 无新增判据；`AC-AUD-10` 记「后置」、`AC-AUD-12` 本期仍独立判 |

---

## 7. 后续切片归属（本次未纳入 M1 的判据，一律有主，不丢）

Gate 判据（**90 条**）里，**7 条**（**6 条 `NOT-APPLICABLE` + 1 条「后置」`AC-AUD-10`**）不在 M1 链路；对应账目 **70 / 13 / 0 / 6 / 0 + 1 后置 = 90**（当初写「8 条」是把 `AC-AGENT-03` 的两次出现重复计了，Gate 已同版改齐）。按「保持 S0–S11 串行与编号不变」的要求，**不塞进 M1 步骤**，在此登记归属与解锁条件；每片开工时由 Spec 把它并入该片的切片文档：

| 判据 | 归属 | 为什么不在 M1 | 解锁 / 前置 |
|---|---|---|---|
| **AC-AUD-08**（审计导出按 Client 切分 + 标识符脱敏 + `limit`/`cursor` 分页） | **M5**（注销 · 数据导出 · 留存） | 导出依赖留存策略与归档口径（R9），M1 只验"分区 + 锚 + 校验 Job"，不碰导出 | D3-A（已收口）+ R9 归档介质；接口 §3.8 `audit-logs` 导出端点 |
| **AC-AGENT-01 / 02 / 03**（Agent 面 HMAC 三条：黄金向量 A / B / 拒绝路径） | **M2 前置**（Agent 面接入）——**确认不挂 M1 任何步骤** | Agent 面 `report`/`policies` 不在 M1 登录链路；但 D1 已冻结 canonical，**向量文件可在 S9 顺手落盘**（不占 M1 门禁、不改 S9 编号） | D1 **A1/A2 已补**（`PATH` 含 context-path、不去尾斜杠、不解码、空 path→`/`；`SORTED_QUERY` 重复键保留 + 编码先于排序 + name→value 两级字节序；`nonce` 按**字节**计长）+ **部署红线**（链路出现 `rewrite`/`strip-prefix`/`rewrite-target` 即 FAIL）；Nova `guanjin-core` HMAC 客户端。判据正文由 Gate v4 持有 |
| **AC-AGENT-03**（拒绝路径：±300s / nonce 重放 / 长度字符集 / secret 32B 解码） | **M2 前置** | 同上；需要 Redis nonce 去重键，M1 的 Redis 只服务限流与 `jti` | D1 + `agent:nonce:{client_id}:{nonce}` TTL 600s |
| **AC-AUD-09**（等保三级双因素 身份鉴别 d)） | **M2** | M1 只有密码因子，AAL2 物理不可达 | M2 TOTP 上线后转 `PASS`，M1 记 `NOT-APPLICABLE` |
| **AC-FLOW-08**（pwd→totp 两级补步完整链路） | **M2** | 同 §2 裁定 ② | M2 TOTP 上线；`flows.achieved_amr` 累加逻辑在 M1 已具备（`AC-FLOW-11`） |
| **AC-AUD-10**（密评预检结论） | **「后置（DEFERRED）：本期不做判定」**（`BLOCKED` 归零） | **密评整体后置**（spencer 口径 2026-11-02 + `decisions.md` D8-B【正式裁定】：Gate 的 `BLOCKED` 前提"依赖残余待裁"已因 R2/R4 挂起、R3 关闭而消失）；等保相关的**代码要求照基线实现**；**后置 ≠ 免验** | **§11.1 的 7 项输入（I-1~I-7）仍须在 M2 之前备齐**（后置不排期，但证据链不能缺）；应用侧可判定部分由 `AC-AUD-12` 覆盖；要恢复判定由 spencer 重开 |
| （附）**R6 旧仓定位** | 无判据 | 不影响任何切片；**已收口 → P5**（旧仓只读，代码只进 `GuanJin`） | — |

> 说明：M2 的三条 Agent 判据与 M1 的 SDK 步骤（S9）**共用 D1 的同一份向量文件**，因此 S9 若有余量可先把 `testdata/agent-hmac/*.json` 落盘——但**不计入 M1 门禁**，也不改变 S9 的编号与顺序。

**v4 修订/新增判据的归属登记（Gate 回读 2026-11-02；只登记 ID，判据正文由 Gate 持有）**

| 判据 | 挂在哪 | 状态 |
|---|---|---|
| `AC-AGENT-01 / 02 / 03` | **M2 前置**（不在 M1 任何步骤，见上表首段） | v4 已加严（`PATH` 含 context-path / 不去尾斜杠 / 不解码 / 空 path→`/`；`SORTED_QUERY` 两级字节序 + 编码先于排序；`nonce` 按字节；部署红线扫描），D1 的 A1/A2 已同步 |
| **`AC-SDK-10`（v4 新增）** | **S9**（SDK 最小接入） | 已登记进 S9 验收判据：`agent.pull.enabled` 默认 `false`、启动 30s+ 零次 `/agent/policies` |
| `AC-SESS-05` | **S7**（会话与实时撤销） | 已挂，口径按 D6(5)：默认 `redis`；`memory` 只在 `local\|test`（多副本 = 非 `local\|test`） |
| `AC-ENG-05` | **S11**（收口映射表） | 已挂，口径补「映射表须**单列** `PENDING`、追认前不计入通过」 |
| `AC-AUD-01` | **S4**（标识符/凭据/注册面） | 已挂，口径补 Gate v4 的**反向断言**：`flows.trace_id` 与后续 `auth_events.trace_id` 不一致仍 PASS，只有该覆盖未覆盖才 FAIL（依据 D4-C(4)） |

### 7.1 技术债登记（v1 有意的欠账，逐条可追）

| 编号 | 欠账 | 事实与影响（只记事实，不作评价） | 何时偿还 |
|---|---|---|---|
| **TD-1** | **授权码 / refresh token 无持久化落点**（15 张表里没有 `auth_codes` / `refresh_tokens`） | v1 两样都存 **Redis**：`auth:code:{code}` / `auth:rt:{token}`，`GETDEL` 原子消费；消费、轮换、撤销各记一条 audit 事件。Redis **不是持久化存储**——进程/实例故障或 Redis 重启会丢失**未消费**的授权码与 refresh token，影响面 = **用户需重新发起授权**（可用性损失）；**不损安全**（不会出现"已消费的码复活"或"撤销失效"，撤销记录与判定不因此回退）。15 张表已冻结，**加表属基线变更**，故 v1 不加表。 | **M2** 落表（`auth_codes` / `refresh_tokens`），届时按**基线变更流程**走（`/docs/baseline/` 另起一版，不在本仓回写）。 |
| **TD-2** | **`TOKEN_REVOKED`（令牌级撤销）在 v1 没有触发路径** | v1 的 `/oauth/revoke` **只撤刷新令牌**（删 Redis 键 `auth:rt:{token}` + 一条 `revoke` 审计）；访问令牌是**无状态 JWT**，`/userinfo`、`/introspect` 均**本地验签、零查库**，仓里**没有令牌黑名单**。因此 `decisions.md` **D3-E** 的 `TOKEN_REVOKED`(401) 在 v1 **无触发路径**——契约与实现的落差。访问令牌的**唯一失效路径**是会话撤销（撤销后复查 → **403 `SESSION_REVOKED`**）；即 v1 只能「连同会话一起撤」，不能单独吊销一张访问令牌；令牌自然过期不受影响。 | **M2**。补它需三件**同时**成立：① 访问令牌带 `jti`（当前 claims 无）；② `/oauth/revoke` 把 `jti` 写 **Redis 黑名单**（**键名需随之裁定，v1 未定**）；③ 解析侧**每请求查一次 Redis**。③ 与「`/userinfo`、`/introspect` 零查库」的判据以及 **S8 数据面热路径**正面冲突，故 v1 不做；M2 与黑名单键名一并裁定。 |
| **TD-3** | **`httputil.ReverseProxy.Director` 已被 Go 1.26 官方废弃（安全理由）** | `internal/gateway/proxy.go`（`proxyFor`，`Director: func(req *http.Request)`）用 `Director` 只改目标地址、**不做 path 重写**（D1-A1：PATH 两端逐字节一致）。Go 1.26 起 `Director` 标废弃、推荐迁 `Rewrite`（就位修改语义）。现状 CI 不红、行为不变，但这是**只对"本地最低工具链"可见的废弃信号**——若本地停在旧版本，它会随"CI 全绿"无声留存。 | **随 S8 数据面后续迭代迁 `Rewrite`**（Go 1.27 前）；归属 **Edge**（文件 owner），本条只登记不派工（Owl 评审 PR #133 时提出，2026-11-13）。 |

---

## 8. M1 完成定义（DoD，一页）

1. **可演示**：一条 e2e 脚本从注册跑到撤销，全程真实容器（PG16 + Redis7），无 mock 伪装故障。
2. **可验收**：`AC-ID → 状态` 映射表齐全，`NOT-IMPLEMENTED` = **0**；账目 **90 = 83 `PASS-able` / 0 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置（DEFERRED）：本期不做判定」(`AC-AUD-10`)**——与 `acceptance-criteria.md` **v11** 数字同源、状态名以 `decisions.md` D8-B【正式裁定】为准（P8 追认后 13 条 `PENDING` 已全转 `PASS-able`）。其中 **`PENDING` 栏仍须单列输出**（本版 0 条，`AC-ENG-05` 要求不得省略该列）；**「后置」项单列、不得写成通过、不得写"预检通过"、不得从账目里摘掉**（后置 ≠ 免验：§11.1 的 7 项输入仍须 M2 之前备齐，`AC-AUD-12` 覆盖应用侧可判定部分）；§7 已把 **7 条**（6 `NOT-APPLICABLE` + 1「后置」）M1 外判据登记到 M2/M5/验收阶段。
3. **可交接**：`migrations` up/down 无损；门禁在变异注入下确实红；ADR 引用 `decisions.md` D 编号而非基线缺章。
4. **不越界**：`guanjingw` 只有 7 件事；无 `direct` 形态；无生产跳过验签/跳过 fail-closed 的开关；基线零改动。

*本文是切片方案，不是基线；与基线冲突处按 `decisions.md`。本轮未建骨架、未入库、未写代码。*
