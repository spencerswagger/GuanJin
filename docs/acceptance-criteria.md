# 关津 · M1 验收判据（acceptance-criteria）

| 项目 | 内容 |
|---|---|
| 文档类型 | 验收判据（**项目自定口径**，非基线原文；口径权威见 D8-A） |
| 版本 | **v14**（2026-11-04，**只更新证据与下限、账目 90 条不变**：①`AC-ENG-01` 补 `flowstore` 85% 下限（D8 第 191 行）并注明**只在 `COVERAGE_TAGS=integration` 的 profile 里可判**；②`AC-ENG-02` 具名索引 **4 → 5**（新增 `flows_expired_cleanup_idx`，第二段清理专用）并换 run `41808510071`；③据实升级 `AC-AUD-02`（触发器官有，`23001`）、`AC-FLOW-10`④（`EXPLAIN` 命中两段索引）、`AC-FLOW-11`④ I-F8（`NOWAIT` 探针 + 正控）；④`AC-ENG-04` 用例数 24→34。v13 见变更记录） |
| 主笔 / 归属 | Gate（测试 · 合规）；本文件是 Ark《M1 垂直切片拆解》（`/docs/m1-slices.md` S0–S11）的**判据层** |
| 上位依据 | `/docs/baseline/` 12 份 + 索引；**实现口径以 `/docs/decisions.md` 为准**（本文只引用、不裁定） |

> **口径声明（必读）**：基线**没有**验收/测试文档——索引 §4.3-2 把「测试策略与验收清单」列为**建议级·可后补**，基线唯一可判定口径是《技术栈与工程约定》§9.3 覆盖率表。**因此本文全部判据属项目自定口径**，不是基线原文，不得反向当基线引用。**D8-A 已裁定**：验收权威 = 移交 Prompt §十一《验收（V1 完成定义）》三条线（业务/合规/安全）+ 技术栈 §9.3 覆盖率门禁；本文是该口径的**展开与可执行化**，不另立第二套口径。与基线原文冲突处一律以 `decisions.md` 为准。
> **契约边界（v3 新增；v11 更新）**：`decisions.md`「契约变更清单」6 条中 **CH-1~CH-5 已全部「已追认」**（**P8**，经 hugo 追认 2026-11-02；CH-6 为内部口径、无需追认）。**v11 起这 5 条不再按待定计**：原 13 条 `PENDING · CH-n` 已全部转 `PASS-able`，**断言内容不用改**；**后续任何契约变更**按 P8 记录法追加 `CH-<下一个可用号>`（**编号只增不重排、作废标「已撤回」保留原行**），新增项**仍按 `PENDING · CH-n` 记账**直到追认。
> **v4 同步说明**：`D1` 的 `PATH`(A1)/`SORTED_QUERY`(A2)、`D6(5)` 两处默认值（`authz.replay-store=redis`、`agent.pull.enabled=false`）、`D3-D` 键名、`D4-C(4)` 的 `trace_id` 取证口径，均为**团队收口结论（非待追认）**，已直接写成 PASS-able 期望；`PENDING·CH-n` **一律不计入通过**。

---

## 0. 使用约定

1. **判据 ID 即测试名**：实现侧测试名必须含该 ID（Go `TestAC_DP_04_x` / Java `AC_DP_04_x` / Vue `ac-dp-04.spec.ts` / 脚本 `ci/ac/AC-DP-04.sh`）。**v2→v3 未重排、未改名任何既有 ID**；新增只追加在组末：v2 的 `AC-FLOW-10/11`、`AC-AUD-11/12`、`AC-AGENT-01~03`，v3 的 `AC-DP-13`、`AC-ENG-08`，v4 的 `AC-SDK-10`。
2. **通过的定义**：给定输入 → 可观测结果与「断言」逐字一致；**失败路径必须被真实触发过**（不靠 mock 假定）。依赖不可用类判据一律真实依赖（testcontainers `postgres:16` + `redis:7`，或 `docker compose stop`）。**依赖类（DB）判据的权威环境 = CI `integration` job**（`postgres:16` service + `pg_isready`）——**本机无 docker 不算证据**；**SKIP = 未验证 = 红**（判据见 `docs/qa/gate-criteria.md` §9.1）。**四道门禁** = `go` / `race` / `web` / `integration`；`integration` 自 `f6aa05e` 起另加两道 guard（无 `//go:build integration` 文件=红、`-list` 差集为 0=红），并已真跑 **34 条** integration-only 用例（run `41808510071`，`tests_run=208 skip=0`）。
3. **`状态`列取值（六种，其中 `NOT-IMPLEMENTED` 是施工期运行时态）**：
   - `PASS-able · <依据>` = 期望值已唯一，可直接写成断言并进 CI 门禁；
   - `PENDING · CH-n` = 期望值依赖**尚待追认**的对外契约变更（**CH-1~CH-5 已于 2026-11-02 全部追认，见 `decisions.md` P8；故本版 `PENDING` = 0**，保留该状态定义供后续新增 CH 使用），**按待定计**，追认前不得写成已生效期望；**CI 映射表必须单列 `PENDING` 列**（与 `BLOCKED`/`NOT-IMPLEMENTED`/「后置」并列输出），**不得并入 `PASS`**、也不得省略该列；
   - **「后置（DEFERRED）：本期不做判定」** = **本期明确不判定**（依 spencer 口径 2026-11-02「密评整体后置」+ `decisions.md` **D8-B【正式裁定】**）——**不进 M1 验收、不出证/送检/预检结论**，但**不等于免验**：判据与输入清单保留（`AC-AUD-10`，见 §11.1），实现侧要求一条不放松。**计入「后置」栏**（不计入 `PASS-able`、也不算失败）；**不得写成通过、不得写「预检通过」、不得从账目里摘掉**（仍占 1 条、映射表单列「后置」栏）；恢复判定由 spencer 重开。**本文件只保留这一种写法，不再出现第二套标签**；
   - `BLOCKED · R<n>` = 期望值依赖**残余待裁**项，只能记 `BLOCKED`，不得自行选值宣称通过。**v7 起为 0 条，v8 起固定为 0**（原唯一 `AC-AUD-10` 已按 D8-B【正式裁定】转为「后置」，摘除依据见 §11.1）；保留该状态定义以备将来出现新的待裁项；
   - `NOT-APPLICABLE · M<n>` = 不在本切片（M1 记 `NOT-APPLICABLE`，归属已登记）；
   - 施工期运行时状态为五种：`PASS` / `FAIL` / `NOT-IMPLEMENTED` / `PENDING`（待 CH 追认，**本版 = 0**）/「后置（DEFERRED）：本期不做判定」，`BLOCKED` 本版为 **0**；**CI 映射表必须把这五种并列输出、并把 `BLOCKED` 与「后置」各单列一栏**（不得合并）；`NOT-IMPLEMENTED` 视为门禁失败（S11 DoD），`PENDING` **不计入通过**，「后置」**不计入通过也不算失败**、但必须在表里显式出现（不得隐藏、不得写成 `PASS`）。
4. **不引用缺章**：D9 已定兜底顺序（①同文档端点/数据契约 → ②索引 §2 → ③`decisions.md` 已裁条目 → ④皆无则不实现并登记 ADR），本文所有出处按此解析。

---

## 1. M1 范围

按 `m1-slices.md` §2 的两处裁决：**① 注册面（`AC-REG-01~10`）10 条全留 M1**（v10 追加 `08` 邀请码 / `09` 注册准入拒绝 / `10` 口令三码顺序）；**② 两级阶梯完整链路推 M2**，M1 只测补步**契约面**（`AC-FLOW-01~07`、`09`，外加 v2 起的 `AC-FLOW-10/11`、v10 的 `AC-FLOW-12/13`）。M1 门禁只跑 `PASS-able` 条目（**本版 `PENDING` = 0，13 条已随 CH 追认转正**）；`PENDING` **单列记账、不折进 `BLOCKED`**（本版 `BLOCKED` 恒为 0），且**不阻断 S1–S10 的编码**（它们全是判据层期望值，不是实现前置）。

---

## 2. 统计与覆盖

| 状态 | 条数 | 明细 |
|---|---:|---|
| `PASS-able` | **83** | 期望值唯一，可直接落地为断言/门禁。v10 +2（`AC-FLOW-12/13`，`acr` 取值域 `D12`）；**v11 +13**（原 `PENDING · CH-n` 13 条随 **P8 追认**全部转正，断言不变） |
| `PENDING · CH-n` | **0** | **v11 起为 0**：原 13 条已随 **P8「CH-1~CH-5 全部追认」**转 `PASS-able`（CH-2 → `AC-FLOW-01/07`；CH-4①~⑤ → `AC-FLOW-03`、`AC-ENG-08`、`AC-LOGIN-05`、`AC-SESS-01`、`AC-AUD-07`、`AC-REG-08/09/10`；CH-5 → `AC-SDK-01/07`、`AC-FC-05`）。**保留该栏目的理由**：`AC-ENG-05` 的 CI 映射表必须继续并列输出 `PENDING` 列（本版计数 0），后续新增 CH 仍按此记账。**CH-1 不单独挂判据**（`flow_id` 必填部分按 m1 §5「可选 + 内部兜底」占位测，`AC-FLOW-10` 的断言不依赖必填语义）；**CH-3 不挂 M1 判据**（只影响 M5 的 `AC-AUD-08`） |
| `BLOCKED` | **0** | v7 起归零、**v8 起固定为 0**：原唯一 `AC-AUD-10` 已按 `decisions.md` **D8-B【正式裁定】** 转为「后置（DEFERRED）：本期不做判定」（定义前提「期望值依赖残余待裁项」已消失：R2/R4 后置挂起、R3 关闭），摘除依据见 §11.1 |
| **「后置（DEFERRED）：本期不做判定」** | **1** | `AC-AUD-10`（密评预检**结论**）——**不进 M1 验收、不出证/送检/预检结论**；输入清单 §11.1 保留作重启密评时用；R2/R3/R4 一并挂起。**不得因后置把它从账目摘掉或写成 `PASS`**、**不得写「预检通过」**；**等保相关代码判据（`AC-AUD-02/03/04/07/09`）一条不动**；`I-1~I-7` 仍须在 M2 之前备齐；恢复判定由 spencer 重开 |
| `NOT-APPLICABLE · M1` | **6** | `AC-FLOW-08`(M2)、`AC-AUD-09`(M2)、`AC-AUD-08`(M5，CH-3 **已追认**：M5 断言按 `limit`+`cursor`、不返 `total`)、`AC-AGENT-01~03`(M2 前置) |
| `NOT-IMPLEMENTED` | **0** | 本文件层面无；实现侧在 S11 前全表为 `NOT-IMPLEMENTED`（施工状态） |
| **合计** | **90** | 条数 = 85（v2 82 + v3 新增 2 + v4 新增 1）+ **v10 新增 5**（`AC-REG-08/09/10`、`AC-FLOW-12/13`）；v5–v9 只改状态。**现行账目（唯一口径，v11）**：**90 = 83 `PASS-able` / 0 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置（DEFERRED）：本期不做判定」** |

**任务书点名的必测条款 → 判据**

| 必测条款 | 判据 |
|---|---|
| 验签失败 → 401 | `AC-DP-04` |
| 令牌缺失 → 401 | `AC-DP-05` |
| `exp` / `iat` 超时钟偏移 → 401 | `AC-DP-06` |
| `iss` / `aud` 不符 → 401 | `AC-DP-07` |
| `jti` 重放 → 401 | `AC-DP-08` |
| `X-GuanJin-Assertion` 剥离（下游 + 日志都取不到） | `AC-DP-02`、`AC-DP-03`、`AC-DP-10`、`AC-SDK-08` |
| Redis 不可用 → `REDIS_UNAVAILABLE` 整体拒服不降级 | `AC-FC-01`、`AC-FC-07` |
| DB / 主密钥不可用 → 拒服 | `AC-FC-02`、`AC-FC-03`、`AC-FC-07` |
| JWKS 无缓存 → 503 | `AC-FC-04`、`AC-SDK-05` |
| `ACR_INSUFFICIENT` 是补步而非失败 | `AC-FLOW-02`、`AC-DP-09` |
| `done` 步骤不可重输 | `AC-FLOW-03`（409 `STEP_ALREADY_SATISFIED`，D3-F） |
| 禁用因子不显示、不计入 `current_acr` | `AC-FLOW-04` |

---

## 3. 与 `m1-slices.md` 的步骤映射（S0–S11 ↔ 判据）

| 步 | 负责人 | 判据 |
|---|---|---|
| **S0**（S0.1–S0.7：骨架 → 15 个基线文件 → 四份项目文档 → ADR-000 → README 五条声明 → Makefile 四目标 → 分支+push+**开 PR**） | Ark | 无 AC（形态类 DoD 6 条在 m1 S0，逐条可查；其中「四份文档入库」使本文件成为可引用源） |
| S1 | Ark | `AC-ENG-03`、`AC-ENG-07`、`AC-ENG-05`、**`AC-ENG-08`（归位 S1：建 `check-forbidden-error-codes.sh` + 「注入 `SERVICE_DEGRADED` 必红」变异验证；S11 只复核）**（`AC-DP-11` 只交付脚本本体） |
| S2 | Ark | `AC-ENG-02`（**15 张表 / `flows` 13 列 / 4 具名索引**）、`AC-ENG-04`、`AC-AUD-02`、`AC-AUD-06`、`AC-REG-03` |
| S3 | Edge | `AC-ENG-04`（SQLSTATE+约束名双键）、`AC-FC-03`、`AC-AUD-01`（基础设施半条） |
| S4 | Core | `AC-REG-01~10`、`AC-LOGIN-02/03`、`AC-AUD-01` |
| S5 | Core | `AC-FLOW-01~07/09`、**`AC-FLOW-10/11`**、**`AC-FLOW-12/13`（v10，`D12`）**、`AC-FC-06`、`AC-REG-03` |
| S6 | Core | `AC-LOGIN-01/04/05/06/07/08` |
| S7 | Core | `AC-SESS-01~03/05`、`AC-AUD-07`（部分） |
| S8 | Edge | `AC-DP-01~13`、`AC-SESS-01/04` |
| S9 | Nova | `AC-SDK-01~10`（含 v4 新增 `AC-SDK-10` 两处默认值）、`AC-DP-03/04~08`、`AC-FC-04/05` |
| S10 | Edge（Gate 驱动） | `AC-FC-01~08`、`AC-AUD-03/04/05/06`、**`AC-AUD-11`** |
| S11 | Gate | `AC-ENG-01/05/06` + **`AC-AUD-12`** + **`AC-ENG-08` 复核**（确认脚本仍在门禁、未被人为跳过）+ 全表无 `NOT-IMPLEMENTED`、`PENDING` 单列不计入通过（**本版 `PENDING` = 0，该列仍须显式输出**）；`AC-AUD-09` 记 M2、`AC-AUD-10` 记「后置（DEFERRED）：本期不做判定」（不进 M1 验收，依 D8-B【正式裁定】） |

> 交叉核对（2026-11-02，v6.1）：m1 §7 已把 **7 条「M1 外判据」**登记完（6 条 N/A + 1 条后置：`AC-FLOW-08`、`AC-AUD-09`（M2）、`AC-AUD-08`（M5）、`AC-AGENT-01~03`（M2 前置）、`AC-AUD-10`（「后置（DEFERRED）：本期不做判定」）——原「8 条」把 `AC-AGENT-03` 重复计，已订正），**本文件 85 条全部有归属**；m1 引用的全部 `AC-*` 在本文件均存在、**无失效引用**，反向亦无未引用条目（`AC-SDK-10` 已由 m1 S9 收录）。
> **v10 交叉核对变更**：本文 **85 → 90 条**，新增 5 条（`AC-REG-08/09/10` 归 **S4**、`AC-FLOW-12/13` 归 **S5**）。**m1 §7/§8 尚未收录这 5 条**，需由 **Ark** 一次改齐（见 §15-**G15**）；在此之前「本文件全部有归属」这句只在 85 条范围内成立，**新增 5 条的 m1 收录状态 = 待同步**（不得据此说 m1 与本文已一致）。

---

## 4. 判据 · 注册面（AC-REG）

> 出处：接口清单 §2.3 / §3.3；档位清单 §三 D；索引 §2；**D2**（`purpose` 六值 + 约束显式命名）。

| ID | 断言 | 证据形式 | 出处 | 状态 |
|---|---|---|---|---|
| AC-REG-01 | `POST /auth/register/start` 带合法 `client_id` + 未占用 email → 200 返回 `challenge_id`、`purpose='register'`、`expires_at`；DB `challenges` 有行，且 `attempts_left`/TTL **等于 `runtime_config` 登记值**（改一次键再跑，值必须跟着变 → 证明非硬编码） | 集成 | 接口 §3.3 + D2/D5 | PASS-able · D2 |
| AC-REG-02 | 不带 `client_id` → 400 `MISSING_PARAMETER`（**不得**按平台默认 L2 静默放行） | e2e | 索引 §2 | PASS-able |
| AC-REG-03 | `challenges.purpose` 的 CHECK 恰为 6 值 `login/register/step_up/enroll/recover/verify_identifier`、约束名显式 `challenges_purpose_valid`；`purpose='register'` 能落库；写入作废值 `bind`/`stepup` → `23514` | 集成 | 接口 §3.3 + **D2** | PASS-able · D2 |
| AC-REG-04 | 已存在 email 再注册 → 403/409 `IDENTIFIER_ALREADY_BOUND`，耗时与不存在账号差异 ≤ 阈值（阈值取自 `runtime_config`） | e2e | 接口 §3.3/§3.7.1 + D3-B | PASS-able · D3-B |
| AC-REG-05 | 准入规则为"仅用户名+密码"→ `register/start` 可跳过挑战，`register/complete` 按 `client_id` 校验准入并建号 | e2e | 接口 §3.3 | PASS-able |
| AC-REG-06 | `register/complete` 成功后：建 `users`、标识符 `verified=true`、**注册即登录**（三件令牌）、写 `auth_events`，且一条 `flows` 行 `status='completed'` 且 `consumed_at` 非空（I-F4） | e2e + 集成 | 接口 §2.3 + **D5 I-F4** | PASS-able · D5 |
| AC-REG-07 | 口令不合规 → `PASSWORD_POLICY_VIOLATION`，事务回滚后 `users`/`identifiers`/`flows` 三表**零残留行** | 集成 | 接口 §3.3 + 档位清单 §三 A | PASS-able |
| AC-REG-08 | **邀请码路径（v10 补登，§5.4 枚举漏登的在用码）**：`POST /auth/register/start` 带**无效 / 已使用 / 过期**邀请码 → **400 `INVITE_CODE_INVALID`**（E1/400）——该码在接口 §3.3 端点表**在用**、但 §5.4「完整枚举」E1 行**漏登记**，按 D9 兜底①（同文档端点契约优先于枚举表）取期望；**不得**回 `MISSING_PARAMETER` / `INVALID_PARAMETER` / `REGISTRATION_NOT_ALLOWED`；且 `users`/`identifiers` **零新增行**（不得先建号后校验） | e2e + 集成 | 接口 §3.3 主要错误码 + §5.4（E1） | **PASS-able · CH-4④ 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |
| AC-REG-09 | **注册准入拒绝（v10 补登）**：目标 client 未开放自助注册 → `register/start` 与 `register/complete` **两条路径各触发一次**，均 **403 `REGISTRATION_NOT_ALLOWED`**（E4 分组**HTTP 由本条覆盖**为 403——§5.4 分组表 E4 默认 404/409/422，本条按「接口可覆盖」取 403）；**不得**回 400/409/500、不得建号（`users`/`identifiers` 零新增）；前端文案按页面规格「该业务系统当前不开放自助注册」 | e2e | 接口 §3.3 主要错误码 + §5.4 分级表（接口可覆盖）+ 页面规格 §错误码表 | **PASS-able · CH-4④ 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |
| AC-REG-10 | **口令三码固定顺序（v10 新增，CH-4 追认项）**：`WEAK_PASSWORD`（E4/422）→ `PASSWORD_REUSED` → `PASSWORD_POLICY_VIOLATION`。①**三码同时命中**时 `code` **只回首个**（恰为 `WEAK_PASSWORD`）；②**全量**命中项放 `details.violations[]`（数组，每项可定位到命中规则；断言长度 = 命中条数、顺序与上表一致）；③**顺序不得调换**——构造「弱口令 + 命中历史 + 不符策略」的三命中用例，`code` 若回 `PASSWORD_POLICY_VIOLATION` 即 FAIL；④注册（§3.3）与改密（§3.6）**两条链路各跑一次** | 集成 | 接口 §3.3/§3.6 主要错误码 + **CH-4⑤（v10 新增项）** | **PASS-able · CH-4⑤ 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |

---

## 5. 判据 · 登录与令牌（AC-LOGIN）

> 出处：索引 §2；接口清单 §3.1、§5.8；数据模型 §4.1 I7；**D3-B/D3-D**；D9。

| ID | 断言 | 证据形式 | 出处 | 状态 |
|---|---|---|---|---|
| AC-LOGIN-01 | 正确口令链路（challenge → verify → submit → token）→ 200：`access_token` 为 JWT、`id_token` 头 `alg=SM3withSM2` 含 `kid`、`acr=AAL1`、`amr=["pwd"]`；`/userinfo` 返回 `sub` 且**该请求 SQL 计数 = 0** | e2e | 索引 §2 + 接口 §5.8 + D9 | PASS-able · D9 |
| AC-LOGIN-02 | 错误口令 → 401 `INVALID_CREDENTIAL`，响应含可复制 `trace_id`，`message` 无区分性信息 | e2e | 接口 §5.4 | PASS-able |
| AC-LOGIN-03 | 连续失败达阈值 → `ACCOUNT_LOCKED`；**权威计数在 Redis**：计数键固定 **`login:fail:{user_id}`**、锁定标记键固定 **`login:lock:{user_id}`**（`user_id` 为小写带连字符 uuid；**键名不得由实现自行改写**，改键须同步改本条与 D3-D），原子 INCR + fail-closed、按 `user_id` 全局共享，TTL 取 `login_policies` 的 `fail_threshold` 窗口 / `lock_duration_min`；2 副本各失败 n-1/第 n 次 → 锁定（直接按上述两键断言）；`authenticators.fail_count/locked_until` 仅镜像、不得作判定依据；`challenges.attempts` 与账号级计数是两个对象 | 集成（2 副本） | **D3-D** + m1 S4 | PASS-able · D3-D |
| AC-LOGIN-04 | 授权码二次换令牌 → `INVALID_GRANT`；refresh 二次 → `REFRESH_TOKEN_REUSED` 且该会话被撤销 | e2e | 接口 §5.8/§5.6 | PASS-able |
| AC-LOGIN-05 | **PKCE 双码按语义分工**：`code_verifier` **缺失/畸形** → `INVALID_CODE_VERIFIER`；**格式合法但与 `code_challenge` 不匹配** → `PKCE_VERIFICATION_FAILED`；两用例各断言精确码 | e2e | 接口 §5.4 E2 + **D3-E** | **PASS-able · CH-4③ 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |
| AC-LOGIN-06 | discovery 的 `id_token_signing_alg_values_supported=["SM3withSM2"]`、`code_challenge_methods_supported=["S256"]`、`acr_values_supported=["AAL1","AAL2"]`，并与实际签发**交叉校验**一致；`S256` 按 m1 §5「R5 不卡 M1」，且已在 `AC-AUD-12` 列为唯一 SHA 白名单 | e2e + 配置检查 | 接口 §3.1.1 + R5 | PASS-able · R5 不影响 M1 |
| AC-LOGIN-07 | `/.well-known/jwks.json` 同时发布 user_token 与 gateway_assertion 两类密钥，`kid` 前缀可区分、`kty=EC`、`crv=SM2` | e2e | 索引 §4.2-23 + 接口 §3.1.2 | PASS-able |
| AC-LOGIN-08 | `/oauth/introspect` 不被每请求调用：1k 次受保护请求后 introspect 计数 = 0 | 集成 | 接口 §5.8 | PASS-able |

---

## 6. 判据 · 阶梯补步 / Descriptor / `flows`（AC-FLOW）

> 出处：索引 §2「档位」；接口清单 §1.1 原则 3、§5.4；**D5**（第 15 张表：**13 列**含 `trace_id`、**5 个具名索引**（第 5 条 `flows_expired_cleanup_idx` 由 `000016` 追加）、不变式 **I-F1~F8**、单向逻辑引用不加 FK、`flow.ttl_seconds` seed 600 + 键缺失 fail-fast）、**D7**、**D3-F**。

| ID | 断言 | 证据形式 | 出处 | 状态 |
|---|---|---|---|---|
| AC-FLOW-01 | Descriptor 按 **D7/CH-2** 齐备：`steps[i]` 含 `factor`、`done`、`challenge_id?`、`purpose`（D2 词表，不含 `register`）、`contributes_acr`；顶层含 `target_acr`、`current_acr`、`terminal_state`；并用 **`flows` 行举证**：同 `flow_id` 的 `achieved_amr`/`completed_steps` 与 Descriptor 逐项一致（`completed_steps[].challenge_id` 能在 `challenges` 找到） | e2e + 集成 | **D7/CH-2 + D5** | **PASS-able · CH-2 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |
| AC-FLOW-02 | 目标 AAL2、已完成 AAL1 → **403 `ACR_INSUFFICIENT`** + `details.flow`（当前档位 + 待补步骤）；非 500/401/空白页、非 302 死循环 | e2e | 接口 §5.4 + §1.1 原则 3 | PASS-able |
| AC-FLOW-03 | `done=true` 的 factor 再 `POST /auth/challenge/{factor}` → **409 `STEP_ALREADY_SATISFIED`**（E4，**D3-F 新增码**），且三条**语义**同时成立：①不产生新 `challenges` 行；②`flows.achieved_amr` 不重复元素（I-F2）；③`completed_steps` 不重复追加同一 factor。**不得**返回 `CHALLENGE_CONSUMED` / `ACR_INSUFFICIENT`（会诱发换 challenge_id 或补步死循环） | e2e + 集成 | **D3-F** + **D5 I-F2/I-F4** | **PASS-able · CH-4① 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |
| AC-FLOW-04 | client `disabled_factors=[X]` → ①`steps[]` 不含 X；②X 的已达成 amr **不计入** `current_acr`；③直接发起 X → `FACTOR_DISABLED` | e2e | 接口 §2.2 + 档位清单 §三 | PASS-able |
| AC-FLOW-05 | 平台级 `PATCH /api/v1/factors/{type} {enabled:false}` 与客户端级禁用效果一致（两级不互相覆盖），且写 `audit_logs` | 集成 | 接口 §3.8 | PASS-able |
| AC-FLOW-06 | 挑战权威在库：`redis-cli FLUSHALL` 后该挑战仍可被正确判定（成功或按 `attempts` 拒），不得"蒸发"成 `CHALLENGE_INVALID` | 集成（真实 Redis） | 索引 §2 + 接口 §5.9 | PASS-able |
| AC-FLOW-07 | 因子全禁用且目标 AAL2 → **HTTP 200** + `terminal_state='unreachable'` + `unreachable_reason.code='FACTORS_DISABLED'` + `missing_factors[]` 非空；不得只回 `satisfied:false` | e2e | **D7/CH-2** | **PASS-able · CH-2 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |
| AC-FLOW-08 | `M2`：pwd→totp 两级补步；第二次 Descriptor 不含已 `done` 的 password；最终 `acr=AAL2`、`amr=["pwd","totp"]`；`achieved_amr` 按 I-F2 单调增长 | e2e | 接口 §4.1 + D5 I-F2 | NOT-APPLICABLE · M2 |
| AC-FLOW-09 | 无 `client_id` 上下文的自助操作按"生效配置"：访问过 L3 后无上下文请求的 ACR 前置 = L3（`user_visited_clients` join `clients` 取 max） | 集成 | 索引 §2 + 数据模型 §3.9 | PASS-able |
| AC-FLOW-10 | **`flows` 生命周期按两段式（D5 I-F6，v3 定稿）**：①`expires_at <= now()` 后首次访问 → **必得 `FLOW_EXPIRED`**（第一段只置 `status='expired'`、**行仍在**，故不 flaky）；②`status='completed'` 后二次提交 → `FLOW_NOT_FOUND`（"流已不在"语义）；③`expired` 未满 7 天的行**仍存在**（`SELECT count(*) = 1` 举证），满 7 天后清理器才 `DELETE`，删除条件严格为 `status='expired' AND updated_at < now()-7d`；④清理器 `EXPLAIN` **命中 `flows_expires_idx`** 且只扫 `expired` 段；⑤临态无 `deleted_at`、不进审计哈希链；⑥会话撤销 → 该会话 `active` 流置 `abandoned`（经 `AbandonBySession` 窄接口） | 集成（DB 行为）：**唯一权威环境 = CI `integration` job**。**已真库证明**（run `41808510071`）：I-F3（`TestDBFlowstoreAdvanceExpiredKeepsRow`/`TestDBFlowstoreConsumeExpiredFlow`）、I-F4（`TestDBFlowstoreConsumeOnce`/`ConsumeRequiresUser`/`ConsumeSessionBackfillOnce`）、I-F6 两段式（`TestDBTwoPhaseCleanup`）、会话撤销置 `abandoned`（`TestDBAbandonBySession`）、`ttl_seed=600`（`TestDBRuntimeConfigFlowTTL`）；**④`EXPLAIN` 已证**：`TestDBFlowCleanupStage1UsesExpiresIndex`（第一段走 `flows_expires_idx`、无 `Seq Scan on flows`，3000 行种子）、`TestDBFlowCleanupStage2UsesExpiredCleanupIdx`（第二段走 `flows_expired_cleanup_idx`）、`TestDBFlowCleanupStage2KeepsRowsWithinRetention`（保留期内的行不被删） | **D5 I-F3/I-F4/I-F6** | PASS-able · D5（②③需 obs "flow 行存在"，与 CH-1 是否必填 `flow_id` 无关） |
| AC-FLOW-11 | **`flows` 不变式与并发（I-F1/I-F5/I-F7/I-F8）**：①`status='completed'` 且 `user_id IS NULL` → DB CHECK 拒（`23514` 映射 **400**）；②同 `(user_id, client_id)` 第二条 `active` 流 → `flows_one_active_per_user_client_uniq` 冲突（`23505` 映射 **409**，匿名流不受限，且映射按 **SQLSTATE + 约束名双键**，禁止"23505 一律 409 / 23514 一律 400"）；③N 线程同 `flow_id` 并发补步，经 `LockByID(tx,id)`（内部 `FOR UPDATE`）串行后 `achieved_amr` = 各步并集、不丢不双写；④**锁内禁重活（I-F8）**：全站锁序 `sessions → flows`、`sessions` 行不做 `FOR UPDATE`、`flows` 行锁内**不得**出现 PBKDF2-SM3/投递/外部 HTTP/导出（用一次 10 万迭代口令校验的并发用例举证不雪崩）；⑤**I-F7 一次性回填**：`completed` 行允许**一次** `session_id: NULL→非 NULL`，之后再改 → 拒绝；`user_id` 同理 | 集成（并发 / 唯一性，DB 侧）：**唯一权威环境 = CI `integration` job**。**已真库证明**（run `41808510071`）：I-F1（`TestDBFlowCompletedRequiresUserMapsTo400`）、I-F5（`TestDBLockByIDSerializesWriters`、`TestDBFlowstoreConcurrentAdvanceKeepsAllAMR`）、I-F7（`TestDBFlowstoreConsumeSessionBackfillOnce`）、唯一索引 23505→409（`TestDBFlowActiveUniqueMapsTo409`）；**④ I-F8「锁内禁重活」已证**：`TestDBFlowstoreHeavyWorkRunsOutsideFlowLock` —— PBKDF2-SM3（**`MinPBKDF2Iterations` ≥10 万，不是测试调小的数**）进行中用另一事务 `SELECT … FOR UPDATE NOWAIT` 探针必须**立刻拿到锁**（否则 55P03），外加**正控**：外部持锁时 `Advance` 必须阻塞到超时（否则「锁没生效」）。两条互补，缺一条都能假绿 | **D5 I-F1/I-F5/I-F7/I-F8** + D5 落地方案 | PASS-able · D5 |

> **I-F1~F8 覆盖对照**（供 Ark/Edge 逐项核对，避免"只写在 D5 没人验"）：I-F1→`AC-FLOW-11①`；I-F2→`AC-FLOW-03②`+`AC-FLOW-08`；I-F3→`AC-FLOW-10①`；I-F4→`AC-REG-06`+`AC-FLOW-10②`；I-F5→`AC-FLOW-11③`；I-F6→`AC-FLOW-10③④⑤`；I-F7→`AC-FLOW-11⑤`；I-F8→`AC-FLOW-11④`。

**v10 追加两条（`acr` 取值域上限，`D12`）**

| ID | 断言 | 证据形式 | 出处 | 状态 |
|---|---|---|---|---|
| AC-FLOW-12 | **`acr` 取值域上限（`D12`）**：对外只 `AAL1`/`AAL2`。①造 `clients.tier='L3'` → Descriptor `target_acr` **必须是 `AAL2`**（**`L3` 档位 → `AAL2`**；V1 不存在能拉出 `AAL3` 的档位）；②**任何路径不得产出 `AAL3`**：跑完 M1 全链路后 `flows.target_acr` / `sessions.acr` 中 `='AAL3'` 的行数 = **0**（DB 计数举证）；③`AAL0` **仅前端展示「未认证」、任何表零落库**（`CHECK` 词表不含 `AAL0`）；④discovery `acr_values_supported` 仍为 `["AAL1","AAL2"]`（与 `AC-LOGIN-06` 交叉，本行不重复其内容） | e2e + 集成（SQL 计数） | 索引 §2/§4.2-15 + **`D12`**（`decisions.md` D12 §209–214，**已复核在位**） | PASS-able · D12 |
| AC-FLOW-13 | **`acr='AAL3'` 注入必须被拒（`D12`）**：①用测试私钥**合法签发**一枚 `acr=AAL3` 的令牌（非伪造签名，验签会过）→ 数据面 **401 `TOKEN_INVALID`**，**不得 200 放行、不得静默降级成 `AAL2` 转发**；②请求体/查询参数里带 `acr=AAL3` → **400 `INVALID_PARAMETER`**（不得静默忽略后按 `AAL1` 放行）；③两次尝试后 `sessions.acr`、`flows.target_acr` 均**无 `AAL3` 新增行**。**语义**：`AAL3` 是预留值（`CHECK` 里保留），V1 任何入口都不得把它变成"已达成" | e2e + 集成 | **`D12`** + 接口 §1.1 原则 3（档位即准入） | PASS-able · D12 |

---

## 7. 判据 · 会话与实时撤销（AC-SESS）

> 出处：索引 §2「撤销」；接口清单 §2.6/§3.6；技术栈 §8.3；**D3-E**。

| ID | 断言 | 证据形式 | 出处 | 状态 |
|---|---|---|---|---|
| AC-SESS-01 | **撤销双码按语义分工**：①会话被撤销（登出 / 管理面踢下线 / refresh 复用）后，同一 access_token 经数据面 → **403 `SESSION_REVOKED`**（E3）；②令牌被 `POST /oauth/revoke` 显式撤销后再用 → **401 `TOKEN_REVOKED`**（E2）。两用例各断言精确码；均**立即生效**（`revoked_at` → 首个拒绝 < 1 个请求往返），无缓存窗口 | e2e | 索引 §2 + **D3-E** | **PASS-able · CH-4③ 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |
| AC-SESS-02 | 管理面两种撤销（单条 / 按 `user_id`）同样实时生效，且写 `auth_events` | e2e | 接口 §3.6 | PASS-able |
| AC-SESS-03 | 撤销后新登录产生新 `session_id`，旧 `session_id` 的令牌**永久**失效 | 集成 | 接口 §3.6 | PASS-able |
| AC-SESS-04 | 多副本一致性：2×`guanjingw` + 2×`guanjind`，A 登录 / B 撤销 / A 立即失效；不依赖会话粘滞 | 集成（4 进程） | 技术栈 §8.3 | PASS-able |
| AC-SESS-05 | **`jti` 防重放默认即 Redis（D6(5) 团队收口）**：`guanjin.authz.replay-store` **默认值 = `redis`**；**「多副本」的判定口径 = `spring.profiles.active` 不含 `local\|test`**（**不新增任何配置键**）——该口径下：①不显式配置时默认即 `redis`（不得回落 `memory`）；②显式置 `memory` → **启动失败（fail-fast）**；③`local\|test` 下显式 `memory` 允许但启动日志必须告警。默认 `memory` = 多副本下静默降级放行（违反索引 §2），故为硬门禁 | 配置检查 + 单测 | **D6(4)+D6(5)** + 技术栈 §8.3 | PASS-able · D6(5) |

---

## 8. 判据 · 数据面 7 件事与断言（AC-DP）

> 出处：数据面设计 §1.1/§4.6；接口清单 §5.2/§5.4；技术栈 §10.1；**D8-B**（传输国密归前置网关）、**D8-C′**（E6 七码归位）。

| ID | 断言 | 证据形式 | 出处 | 状态 |
|---|---|---|---|---|
| AC-DP-01 | 合法令牌 + 达标 ACR → 转发并注入 `X-GuanJin-Assertion`：`iss=guanjin-gateway`、`sub`、`aud`=目标 client、`cid`、`acr`、`amr[]`、`iat`、`exp=iat+60`、`jti` 齐全，上游能用 JWKS 验签通过 | e2e | 接口 §5.2 | PASS-able |
| AC-DP-02 | **网关侧「先弃后签」**（与 D10 的 SDK 侧剥离是两件事）：客户端自带伪造 `X-GuanJin-Assertion` → 上游收到该 header **只有 1 个**且是网关现签的；伪造值零透传，且不影响 ACR/身份判定 | e2e | 数据面 §4.6 + **D10** | PASS-able |
| AC-DP-03 | 下游再下一跳收不到该 header：SDK 按 **D10 六步**「验签 → claim 校验 → 注入上下文 → **替换请求视图**」后，业务转发与日志同样取不到（与 `AC-SDK-08` 成对；网关侧剥离见 `AC-DP-02`） | e2e | 数据面 §4.6 + **D10** | PASS-able · D10 |
| AC-DP-04 | 篡改 payload / 错误 `kid` / 未知公钥签名 → 401 且不注入身份 | 单测 + 集成 | SDK §7 + I7 | PASS-able |
| AC-DP-05 | 令牌缺失 → 401 deny-by-default | e2e | SDK §7 | PASS-able |
| AC-DP-06 | `exp` 过期 → 401；`iat` 超前 > `clock-skew` → 401；边界按 `skew±1s` 打点（`skew-1s` 内 200、`skew+1s` 外 401） | 单测（注入时钟）+ e2e | 接口 §5.2 | PASS-able |
| AC-DP-07 | `iss` 非 `guanjin-gateway` / `aud` 不符或缺失 → 401 | 单测 | SDK §7 | PASS-able |
| AC-DP-08 | 同 `jti` 窗口内第二次 → 401；窗口过期后可再用 | 集成（真实 Redis） | SDK §7 + 数据面 §4.7 | PASS-able |
| AC-DP-09 | 经数据面访问且档位不足 → 403 `ACR_INSUFFICIENT` + `details.flow`（非 302 死循环、非 500） | e2e | 接口 §3.9 | PASS-able |
| AC-DP-10 | 日志禁令牌：全链路跑完后全量日志 grep `access_token`/`refresh_token`/`X-GuanJin-Assertion` 原文/口令/标识符明文 → **命中 0** | 配置检查 + e2e | 技术栈 §10.1 | PASS-able |
| AC-DP-11 | 7 件事不越界：`cmd/guanjingw` 无 `tls.Listen`、无 WAF/灰度/熔断库、无业务路由发现。**TLS 完全不属本项目范围**（依 spencer 口径 2026-11-02）：「TLS / WAF / LB / 灰度 / 熔断由前置 nginx / Kong 承担，**不属本平台验收面**，本平台不做任何 TLS / TLCP / 证书相关判定」——本条只验「网关不自己实现这些能力」，**不设任何 TLS 相关的跳过开关**；判据本身（验签/注入/剥离/明文转发）不因 TLS 出范围而减弱 | 配置检查 | 数据面 §1.1–1.3 + **spencer 口径（2026-11-02）** | PASS-able |
| AC-DP-12 | 单点登出通知走 SM2 签名发往已访问业务系统；通知失败**不阻塞**本端登出（本地会话必须已撤销） | 集成 | 数据面 §1.1 ⑦ | PASS-able |
| AC-DP-13 | **上游故障用专职码（v3 新增，D8-C′）**：上游拨号失败/超时 → **503 `UPSTREAM_UNAVAILABLE`**（唯一判定点在 `cmd/guanjingw`）；**不得**用 `DEPENDENCY_UNAVAILABLE`（只兜未分类第三方）或 `SERVICE_DEGRADED`（已废弃，见 `AC-ENG-08`）；同时断言"上游不可达时不注入断言、不回落直连" | 集成 | **D8-C′(④)** | PASS-able · D8-C′ |

---

## 9. 判据 · 最小 SDK 接入（AC-SDK）

> 出处：SDK契约 §3.2/§6.1/§7；**D6**；**CH-5 已追认（P8，2026-11-02）**。

| ID | 断言 | 证据形式 | 出处 | 状态 |
|---|---|---|---|---|
| AC-SDK-01 | 无模式开关：`GuanjinProperties` 中**不存在 `mode` 键**（反射扫描断言、示例 yml 无 `mode:` 行）；出现 `direct` 语义 → 启动失败 | 配置检查 + 单测 | **D6(2)/CH-5** | **PASS-able · CH-5 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |
| AC-SDK-02 | `guanjin.enabled`/`authz.enabled` **受 profile 门控**：仅 `spring.profiles.active` 含 `local\|test` 可置 false；其它 profile 置 false → **启动直接失败（fail-fast，不是告警）**；任何 profile 下篡改令牌都必须 401。**性质已定（v6，原 G11）**：D6(4) 属**纯加固裁定、不进 CH 清单**——非 `local\|test` 禁止置 `false` 本就是基线禁止项（注释性引用：**SDK 契约 §3.2「验签不可关 / fail-closed 不可关」+ 移交 Prompt §九「不得为了让测试通过而关闭 fail-closed 或跳过验签」**），D6(4) 只是把「告警」升级为「启动失败」让禁令**可执行**，不新增契约面；判据取 Ark 的三分判据：「是否让业务方能用基线原本不允许的方式配置 SDK？」→ 不能，则不进 CH | 单测 | **D6(4)**（纯加固裁定；SDK §3.2 / 移交 §九为注释性引用） | PASS-able · D6(4)（v6 由 CH-5 待定项降级，G11 已闭合） |
| AC-SDK-03 | `jwks-uri` 默认 = `${platform.base-url}/.well-known/jwks.json`（base-url 不带尾斜杠）；默认配置首启即拉取成功（404/500 即失败）。**性质：纠错性默认值（D6(3)）——已从 CH-5 移出、不需追认** | 配置检查 + e2e | **D6(3)**（纠错性默认值） | PASS-able · D6(3)（v5 由 CH-5 待定项降级） |
| AC-SDK-04 | 平台地址未配置/非法 → 启动失败（快速失败），不得静默放行或退化为匿名 | 单测 | SDK §7 末行 | PASS-able |
| AC-SDK-05 | JWKS 拉取失败 + 有缓存 → 沿用缓存**仍真实验签**（篡改令牌 401）；**无缓存 → 503 `KEY_UNAVAILABLE`**（点名该码：无法取得验签公钥属密钥不可用；**不得**用 `DEPENDENCY_UNAVAILABLE`——它只兜未分类第三方依赖，D8-C′③；也不得用已废弃的 `SERVICE_DEGRADED`），且不得降级放行 | 单测（MockWebServer） | SDK §7 + **D8-C′** | PASS-able · D8-C′ |
| AC-SDK-06 | `jti` 存储不可用 → 拒绝请求，不得放行 | 单测 | SDK §7 | PASS-able |
| AC-SDK-07 | **交付配置集 = 29 项**（D6：以契约 §3.1 实有 29 行为准；缺号 #7/#16/#17 为编号空洞，**不得新增 key 补齐**）：反射扫描 `GuanjinProperties` ↔ 契约表逐项一致（key 名 + 类型 + 默认值），Nova 侧连续编号 1..29 + README 标注差异（ADR-003）；数量 ≠ 29 或存在补号 key → 失败 | 配置检查 | **D6(1)/CH-5** | **PASS-able · CH-5 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |
| AC-SDK-08 | **剥离唯一时序（D10 六步，禁止换序）**：①读原始头取 `X-GuanJin-Assertion`（此时**不删**，它是唯一凭证来源）→ ②验签（SM2 + JWKS 缓存 + 未知 `kid` 强刷一次）→ ③校验 claim（`exp`/`iat` 偏移、`iss`、`aud`、`jti`）→ ④注入上下文（`SecurityContext`/`Principal`）→ ⑤**替换请求视图**（wrapper 覆写 `getHeader`/`getHeaders`/`getHeaderNames`，不是「删 header」）→ ⑥业务 handler 运行。四条断言：①`MockFilterChain`/handler 收到的 request **`getHeader` 为 null、`getHeaderNames` 不含该名**；②**下游业务转发**与**日志**两个消费者同样取不到（三处 consumer 全查）；③**反向断言：篡改令牌仍 401**（验签先于剥离，不得因剥离导致「无令牌可验」而放行或空验）；④Filter 顺序在 `UsernamePasswordAuthenticationFilter` 之前。**禁止**「验签前剥离」（D10 明确否决）与「只在下游转发前删一次」（框架/日志已缓存 header 名） | 单测 | **D10** + SDK §5.4 | PASS-able · D10 |
| AC-SDK-09 | `guanjin-core` 零 Spring/servlet 依赖：`dependency:tree` 中 core 无 `org.springframework.*`/`javax.servlet.*` | 配置检查 | 技术栈 §1.1 | PASS-able |
| AC-SDK-10 | **两处默认值（v4 新增；D6(5) 团队收口、非待追认）**：①`authz.replay-store` 默认 = **`redis`**（默认启动解析结果必须是 redis，不得 memory）；②`agent.pull.enabled` 默认 = **`false`**——默认启动**不向平台拉取绑定**（断言：启动后 30s+ 内零次 `/api/v1/agent/policies` 请求，对齐索引 §4.2-24「零回源」），接入方显式置 `true` 才拉。**只改默认值、不改 29 项数量**（本条不数项，与 `AC-SDK-07` 不冲突） | 单测 + 配置检查 | **D6(5)** + 索引 §4.2-24 | PASS-able · D6(5) |

---

## 10. 判据 · fail-closed 故障注入（AC-FC）

> 出处：技术栈 §1-5/§8.3-4；接口清单 §5.7；SDK §7；**D8-C + D8-C′**（E6 七码归位、`SERVICE_DEGRADED` 废弃）、**D8-D′**（`/userinfo`）、**D4-B**（派生键）。

| ID | 断言 | 证据形式 | 出处 | 状态 |
|---|---|---|---|---|
| AC-FC-01 | 真实停 Redis → 认证端点一律 **503 + `REDIS_UNAVAILABLE` + `Retry-After`**；`/readyz` 非 200 且 `{checks:{redis:false}}`；期间**零**个"200 且带身份"响应 | 集成（真实停容器） | 接口 §5.7 + **D8-C** | PASS-able · D8-C |
| AC-FC-02 | 真实停 PostgreSQL → **503 + `DB_UNAVAILABLE` + `Retry-After`**；`/readyz` 非 200；不注入身份。**不得**用 `DEPENDENCY_UNAVAILABLE`（只兜未分类第三方），**不得**用 `SERVICE_DEGRADED`（已废弃，D8-C′⑤，由 `AC-ENG-08` 机器拦截） | 集成（真实停容器） | **D8-C / C′** | PASS-able · D8-C′ |
| AC-FC-03 | 主密钥缺失/非 base64/解码后 < 32B → **启动 fail-fast，退出码 ≠ 0**（D4-B 只接受派生键）；运行期派生键异常 → 503 `KEY_UNAVAILABLE`，**不产出任何可用令牌** | 单测（启动 fail-fast，**已实测**：`TestMasterKeyRejectsShortAndBadBase64`、`TestLoadFailsClosed/no_master_key`）+ 集成（运行期 503 `KEY_UNAVAILABLE`）：**权威环境 = CI `integration` job**，但当前 24 条真库用例**无运行期密钥异常用例** → 该半条**未验证** | **D4-B + D8-C** | PASS-able · D4-B |
| AC-FC-04 | SDK 侧 JWKS 无任何缓存（首启即不可达）→ **503 `KEY_UNAVAILABLE`**（与 `AC-SDK-05` 成对、同码）；启动自检须把「密钥不可用」与「平台不可达」区分开，后者才允许 `DEPENDENCY_UNAVAILABLE` | 单测 | SDK §7 + **D8-C′** | PASS-able · D8-C′ |
| AC-FC-05 | **无生产跳过开关**：全仓配置键与代码路径扫描，不存在可关闭「验签 / fail-closed / 断言剥离」的开关；白名单 = **D6 的 29 项（无 `mode`）+ `enabled/authz.enabled` 的 profile 门控**，多出一个键即失败。**三处纠错性默认值（`jwks-uri` / `replay-store=redis` / `agent.pull.enabled=false`）已定、不再是待追认变量**；本条曾唯一待追认的是 **「29 项交付集」本身**（CH-5 已于 v11 追认，断言转正、内容不变） | 配置检查（CI 门禁） | **D6/CH-5** + SDK §7 | **PASS-able · CH-5 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |
| AC-FC-06 | Redis 全挂期间不丢状态：已签发挑战在 Redis 恢复后仍按原 TTL/`attempts` 判定 | 集成 | 数据模型 §3 + 接口 §5.9 | PASS-able |
| AC-FC-07 | 降级放行负向扫描：FC-01/02/03 三种故障下对"匿名白名单外的全部端点 + 数据面通配转发"发探针，200 响应中**没有一个**携带身份（无 `sub`、无 `X-GuanJin-Assertion`）；且返回值只允许 `REDIS_/DB_/KEY_UNAVAILABLE` 或 `UPSTREAM_UNAVAILABLE`（转发场景），出现 `SERVICE_DEGRADED` 即 FAIL | 集成 | SDK §7 + **D8-C′** | PASS-able · D8-C′ |
| AC-FC-08 | **`/userinfo` 与 DB（D8-D′）**：零查库 → DB 停机时**允许 200 且该请求 SQL 计数 = 0**（不构成降级放行：该端点无依赖判定被跳过）；**一旦实现引入查库/回源 → 必须 503 `DB_UNAVAILABLE`**。断言：DB 停机 + 有效令牌请求 `/userinfo` → ①SQL 计数 = 0 且 200，或 ②503 `DB_UNAVAILABLE`；**"查库了却仍 200"即 FAIL**，不接受第三种 | 集成（真实停 DB + SQL 计数） | 接口 §5.8 + **D8-D′** | PASS-able · D8-D′ |

---

## 11. 判据 · 审计与合规证据（AC-AUD）

> 出处：索引 §2/§4.2-19；数据模型 §3.7/§4.1 I5；**D4-C**（分区/锚/校验 Job）、**D8-A/B**。

| ID | 断言 | 证据形式 | 出处 | 状态 |
|---|---|---|---|---|
| AC-AUD-01 | 登录成功与失败都写 `auth_events` 且含 `trace_id`（可与响应体互查）；`flows.trace_id` = **创建该流那一次请求**的 `trace_id`，补步**不覆盖**、不参与查询谓词与唯一约束。**明确不算缺陷（D4-C(4)）**：同一次登录里 `flows.trace_id` 与后续补步的 `auth_events.trace_id` **本来就会不同**，**不得**因二者不等判 FAIL；要「一次登录一条链」只能用 `flows.id`（`flow_id`）作关联键。**反向断言**：补步后二者不等 → 本条仍 `PASS`；若实现把 `flows.trace_id` 覆盖成补步的 trace → **FAIL**（违反 D5 列定义） | 集成 | 技术栈 §10.2 + **D5 + D4-C(4)** | PASS-able · D5/D4-C |
| AC-AUD-02 | `auth_events`/`audit_logs` 的 `UPDATE`/`DELETE` 被 `forbid_mutation` 拒绝（`restrict_violation`）；无 `updated_at`；触发器挂父表且在分区上生效（PG≥11 自动下发） | 集成（触发器官有）：**唯一权威环境 = CI `integration` job**。**已真库证明**（run `41808510071`，`tests_run=208 skip=0`）：`TestDBAuditLogsAppendOnlyTriggerExists`（父表 `audit_logs_append_only` 恰 1 个 + **当月分区上也复制到 1 个**，直击 D4-C 的锚/链前提）、`TestDBAuditLogsRejectsUpdateAndDelete`（`UPDATE`/`DELETE` 均被拒且 SQLSTATE = **`23001` `restrict_violation`**，拒绝后**行仍在**，未用应用层拦截替代） | 数据模型 I5 + D4-C | PASS-able · D4-C |
| AC-AUD-03 | 哈希链：删中间一条 → 后续校验可检出断裂；重排/覆盖同理 | 集成 | 数据模型 §3.7 | PASS-able |
| AC-AUD-04 | 并发 100 写不断链（`pg_advisory_xact_lock` 串行化链尾），并发后逐条复核全链通过 | 集成 | 索引 §4.2-19 | PASS-able |
| AC-AUD-05 | 「完整性校验状态」由真实校验得出：分区内逐条复算 + 与 `runtime_config` 锚（`audit.chain.anchor.<YYYYMM>`）比对；枚举恰为 `verified\|broken\|unverified`（**锚缺失 = `unverified`，不得记 broken**）；构造被篡改行 → `broken`；构造缺锚分区 → `unverified` | 集成 | **D4-C(3)** | PASS-able · D4-C |
| AC-AUD-06 | `audit_logs` 按 `occurred_at` **RANGE 月分区**、主键 **`(id, occurred_at)`**；只删「已归档且已写锚」的最老分区，删除时把首尾 tag 存为断点锚（`audit.chain.segment.<YYYYMM>`），跨分区首条 `prev_tag` 取上一分区末 tag 或断点锚 → 链保持连续；删最老分区后检索不漏、全链复核仍 `verified`。⚠️ 归档介质与归档密钥保管不在本条（**R9**，365 天内不触发） | 集成 | **D4-C(1)(2)** + 索引 §4.2-12 | PASS-able · D4-C |
| AC-AUD-07 | 三员越权**双码按语义分工**：①调用超出本员职责域的写操作 → **403 `THREE_POWER_VIOLATION`**（E7）；②单纯缺该角色 → **403 `INSUFFICIENT_ROLE`**（E3）。遍历 35 条管理端点 × 3 角色矩阵，断言**审计员写操作零成功**，两类码各自可复现 | 集成（矩阵遍历） | 接口 §3.8 + **D3-E** | **PASS-able · CH-4③ 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |
| AC-AUD-08 | 审计导出按 Client 切分、标识符脱敏（导出件 grep 原始手机号/邮箱 → 命中 0）；分页按 `limit`+`cursor` | e2e | 接口 §3.8 + D3-A | NOT-APPLICABLE · M5（CH-3 **已追认**：M5 断言按 `limit`+`cursor`、响应 `{items, next_cursor}`） |
| AC-AUD-09 | 等保三级 身份鉴别 d) 双因素 → **M2 判据**：M1 只证明 AAL1 可达，M1 记 `NOT-APPLICABLE`，M2 必须转 `PASS` | e2e（M2） | 等保三级 + 索引 §2 | NOT-APPLICABLE · M2 |
| AC-AUD-10 | 密评预检**结论**（「密评是否通过」，**不再写成「二级预检是否通过」**——等级口径按 **`D8-E` 第二版**：**实现按 GB/T 39786 第三级条款固定、二级场景为其子集；判定与出证后置**；条款引用**保持 8.x、不降 7.x**；旧版「密评上限二级」**作废**）：**后置（DEFERRED）：本期不做判定**（依 spencer 口径 2026-11-02「纯软实现，暂不管密评」+ `decisions.md` **D8-B【正式裁定】**）——**本期不出证、不送检、不写预检结论、不进 M1 验收**。输入清单见 **§11.1**（保留作重启密评时用；`I-1~I-7` 仍须在 M2 之前备齐）。**禁止**写「预检通过」、**禁止**自行选等级、**禁止**自选算法路径；**不得从账目摘掉**（仍占 1 条、映射表单列「后置」栏）。**注意边界**：后置的只是「出证/送检/预检结论」这一层，**等保代码要求照旧**（`AC-AUD-02/03/04/07/09`、`AC-AUD-12` 应用侧国密实证本期可独立判） | 文档评审（不充当证据） | **spencer 口径（2026-11-02）** + **D8-B【正式裁定】**（R2/R4 后置、R3 关闭） | **后置（DEFERRED）：本期不做判定** |
| AC-AUD-11 | 链锚与校验 Job（D4-C 落地）：每日定时整链校验落在 `cmd/guanjind` 内（**不新增二进制**）；暴露 `audit_chain_broken_total`、`audit_anchor_age_seconds`；人为篡改一条 → 下一次任务使 `audit_chain_broken_total` 递增且状态置 `broken`；锚超期 → `audit_anchor_age_seconds` 超阈值告警 | 集成 | **D4-C(3)** | PASS-able · D4-C |
| AC-AUD-12 | **应用侧全链路国密实证**：①令牌 `SM3withSM2`；②审计完整性 `HMAC-SM3`；③敏感字段 `SM4`；④口令 `PBKDF2-SM3`（迭代 ≥100000、盐 ≥16B）；⑤全仓算法扫描**不含 SHA-1/256/512 与 RSA** 用于签名/摘要/口令（**唯一白名单：PKCE `S256`**，见 R5）；⑥主密钥只做 HKDF-SM3 派生、不进业务算法（D4-B）。任一项出现 SHA 系/RSA 即 FAIL | 配置检查（算法扫描）+ 集成 | 索引 §2 + **D4-B/D8-B** | PASS-able · D8-B |

### 11.1 `AC-AUD-10` 的判定输入清单（**后置（DEFERRED）：本期不做判定**）

> **状态：`AC-AUD-10` = 「后置（DEFERRED）：本期不做判定」**（依 spencer 口径 2026-11-02：「纯软实现，**暂不管密评**」；密评整体后置，R2/R3/R4 一并挂起）。**不进 M1 验收、不出证、不送检、不写预检结论。**
> **`D8-E` 已改第二版（2026-11-02 spencer；旧版「密评上限二级」作废，v10 起本条为新口径）**：①**统一表述** = 「**实现按 GB/T 39786 第三级条款固定，二级场景为其子集；判定与出证后置**」——条款引用**保持 8.x、不降 7.x**（8.x 的「应」在二级降为「宜/可」，**降引用 = 自降标准**）；依据 GB/T 39786-2021 第 4 章 + 密评 FAQ 三版 1.2/1.3：**密评等级随接入系统等保定级走、不得低于备案等级**，故「等保三级 + 密评二级」测评机构**不受理**。②**TLS 写责任转移、禁写「无影响」**：二级 7.2 c)/7.4 c) 是「**宜**」、三级 8.2 c)/8.4 d) 是「**应**」；按 P2② TLS 出本平台边界 → **本平台不因 TLS 被判不符合**（测评对象是本平台的密码应用、传输通道由前置承担），风险由**部署方前置 TLS** 承担（`AC-DP-11` 判据不变）。**关键限定（`decisions.md` D8-E ③ 原文）**：「宜」**不阻塞，但未声明不适用时仍纳入测评、判「部分符合」而扣分** → 故 **I-2 的书面归属不是可选项**；且**不得**写成「TLS 与本项目无关 / 无影响」——那是把**责任转移**说成**风险消失**。③**密码产品不承诺「密评通过」**：算法库 ≠ 密码产品，**GB/T 37092 一级纯软可达、二级（含防拆封装等物理安全机制）纯软无解**；V1 只做「**自评 + 遗留风险登记**」，不得对外说「密评通过 / 预检通过」。**本版只改「等级怎么写」，不解除后置。**
> **正式摘除 `BLOCKED` 的依据（可直接引，`decisions.md` D8-B【正式裁定】，2026-11-02，收本文 §15-G12）**：本文件 `BLOCKED` 的**定义前提是「期望值依赖残余待裁项」**；而 `AC-AUD-10` 依赖的三项**已不在待裁状态**——**R2/R4 已按 spencer 口径后置（挂起）、R3 已关闭（TLS 出本平台边界）**，定义前提消失则标签失效。**继续挂 `BLOCKED` 会留下一个永不收敛的红项、掩盖真实进度**，故状态名改为「后置（DEFERRED）：本期不做判定」、`BLOCKED` 栏计数 = **0**。**这不是记通过**（D8-A「不造假通过」），只是换成更准确的状态名。
> **后置 ≠ 免验（硬约束，D8-B 裁定原文保留）**：①**7 项输入 `I-1~I-7` 仍须在 M2 之前备齐**；②「后置」项**不得写成通过、不得写「预检通过」、不得从账目里摘掉**（仍占 1 条、映射表单列「后置」栏）；③**恢复判定由 spencer 重开**（重开前不得自行启用下表判定）。
> **下表保留**，作为**将来重启密评时的输入清单**（不是本期待办、不催办）。
> **边界（与后置严格区分）**：**等保相关的代码要求照基线保留判据**——双因素（`AC-AUD-09`，M2）、审计只增不改 + 哈希链（`AC-AUD-02/03/04`）、三员分权（`AC-AUD-07`）、审计证据可导出（`AC-AUD-08`，M5）**全部不动**；后置的只是「**出证 / 送检 / 预检结论**」这一层判定，**不是**放松实现要求。
> 判定状态机（**仅由 spencer 重开密评后启用**）：「后置」→（输入未齐）`BLOCKED` → `PASS` / `FAIL`；**输入到位但结论为「不通过」时也必须如实记 `FAIL`**，不得含糊写「待定即通过」。

| # | 需要的输入（重启密评时需要，本期不催办） | 由谁提供 | 拿到后 `AC-AUD-10` 怎么判 |
|---|---|---|---|
| **I-1** | **接入系统定级 / 备案等级的书面确认**（v10 改题：**不再问「是否确认二级」**——`D8-E` 第二版：密评等级**随接入系统等保定级走、不得低于备案等级**，GB/T 39786-2021 第 4 章 + 密评 FAQ 三版 1.2/1.3） | spencer（合规负责人）+ 测评机构 | ①拿到**备案等级**书面结论 → 按 `D8-E` 第二版写死「**实现按 GB/T 39786 第三级条款固定，二级场景为其子集；判定与出证后置**」，**条款引用保持 8.x、不降 7.x**；②数据模型引用的「三级 8.3 e)/8.4 g)」**不再按"引错级别 / 改标超范围"处理**（旧版做法作废）——**照引用、不降级**；③若备案等级超出纯软可测评能力 → `AC-AUD-10` **FAIL** 并触发密码产品路径重评（接密码机） |
| **I-2** | **传输段证据的归属确认**：TLS / TLCP 由**部署方前置组件**承担，**本平台不做判定**；仅需一份「部署方证书与 TLCP 配置由谁负责」的书面归属（**不构成本平台判据**；但 **`D8-E` ③ 定：它是必需项、不是可选项**——「宜」在**未书面声明不适用**时仍纳入测评并按「部分符合」扣分） | 运维 / 部署方 | 本平台侧**无代码动作**：`AC-DP-11` 已把「TLS/WAF/LB/灰度/熔断不属本平台验收面」写死，本条只作为重启密评时的**范围声明 + 责任承接证据**，不给本平台产出任何 pass/fail；**声明到位后才能写「本平台不因 TLS 被判不符合」，声明缺失时不得写「无影响」** |
| **I-3** | **纯软国密的认可形式**：书面材料而非口头结论——三选一：①测评机构书面认可（函/意见书）；②明确「软件密码模块」可受理的**检测报告类型/名称**；③**自评材料**（GB/T 37092 段落对照 + 纯软实现说明 + **遗留风险登记表**） | spencer + 测评机构 | 按 **`D8-E` 第二版③**：算法库 ≠ 密码产品、纯软对 GB/T 37092 二级**无解**，V1 **不承诺「密评通过」**——材料只用于「**自评 + 遗留风险登记**」，`AC-AUD-10` **仍记「后置」，不得因自评材料齐备而记 `PASS`**；若测评机构书面明确**不受理纯软** → `AC-AUD-10` 判 **FAIL** 并触发密码产品路径重评（接密码机 → 部署形态与成本变更，需复评 `AC-DP-11`、`AC-FC-03`） |
| **I-4** | **密评送检范围**：送检系统边界（仅关津？含前置网关？含业务系统？）、被测组件清单 | spencer + 测评机构 | 边界明确后才能定 `AC-AUD-10` 的判定对象；**传输段一律标注「不在本组件范围」并附边界文件**（TLS 已出范围，见 I-2） |
| **I-5** | **送检时点与预检执行方**：是否送检、何时送、谁做预检、结论何时出具 | spencer | 时点确定 → 判定**绑定「预检报告出具」那一刻**，只认报告原文、不认团队自评；预检方确定 → 证据归档路径固定 `artifacts/compliance/` |
| **I-6** | **证据形式与签章要求**：报告编号、签章页、日期、是否需机构盖章 | spencer + 测评机构 | 明确后证据链闭合（报告编号 + 签章页 + 日期归档）；在此之前即使「口头通过」也不得记 `PASS` |
| **I-7** | **结论为「不通过」时的处置授权**：是否接受改道接密码机、延期上线、或调整合规目标 | spencer | 有授权 → 可据实以 `FAIL` 结案并进入整改；无授权 → 维持「后置（DEFERRED）：本期不做判定」并在验收表注明「待处置决策」，**不得**含糊写「待定即通过」 |


## 12. 判据 · 工程门禁（AC-ENG）

> 出处：技术栈 §9.3/§9.4/§5/§6；**D4-A/D/D5**（表结构与约束写法）、**P1**（不自动迁移）、**D8-C′/D3-F**（禁码扫描）。

| ID | 断言 | 证据形式 | 出处 | 状态 |
|---|---|---|---|---|
| AC-ENG-01 | 覆盖率下限：`crypto` ≥90%、`audit` ≥90%、`flow` ≥85%、**`flowstore` ≥85%（D8 补登，I-F2/F5/F6/F8 唯一落点）**、`credential` ≥85%、`identifier` ≥85%、`factor` ≥85%、`gateway` ≥80%、`risk` ≥80%、`store` ≥75%、`protocol`/`admin`/`agent` ≥70%；低于下限即 CI 失败。**`flowstore` 的下限只在 `COVERAGE_TAGS=integration` 的 profile 里可判**（其行为全在 `//go:build integration` 用例内；unit profile 会打 `[INFO]` 显式说明、不静默跳过） | 配置检查（`scripts/coverage-gate.sh`；`flowstore` 需 integration profile，见 `docs/qa/gate-criteria.md` §9.5⑤） | **技术栈 §9.3 + D8（第 191 行补登）** | PASS-able · D8-A |
| AC-ENG-02 | **表结构门禁**：`migrate up` 全绿、`down` 可回滚；**表数 = 15**（14 + `flows`）、表名逐字一致；`flows` **13 列**（含 `trace_id`，`id` 无 `DEFAULT uuidv7()`、`expires_at` **禁 DB 默认**）；**5 个具名索引**（`flows_one_active_per_user_client_uniq` / `flows_session_idx` / `flows_expires_idx` / `flows_client_created_idx` + `flows_expired_cleanup_idx (updated_at) WHERE status='expired'`，后者由 `000016` 追加、专供第二段清理）；I-F1 的 CHECK 存在；`flows_one_active_per_user_client_uniq` 必须写成 **`CREATE UNIQUE INDEX … WHERE`**（不得写 `CONSTRAINT … UNIQUE`，PG 不支持部分唯一约束）；`000015` 内 **seed `runtime_config('flow.ttl_seconds','600')` + `ON CONFLICT DO NOTHING`**；不写 `clients_redirect_unique` 那条含子查询的非法 CHECK（D4-D） | 集成：**CI `integration` job 的 schema 断言（唯一权威环境，`postgres:16` service）**——迁移逐条 `0/u`…`16/u`（**17 条**，`16/u create_flows_expired_cleanup_idx` 已实跑）后逐项**真断言**（形如 `[ "$x" = "y" ]`，不等即打印 `[FAIL]` 并 `exit 1`，不是打印后不看）：`logical_tables=15`、`audit_logs_partitions=3`、`flows_columns=13`、**`flows_named_indexes=5`**、`flows_if1_check=1`、`ttl_seed=600`；实测锚点 run `41808510071`（`41803170713` 为 4 索引阶段、已过期）。另有 Go 侧真库用例把「索引清单/形状」与台账对齐：`TestDBFlowsNamedIndexesMatchLedger`、`TestDBFlowsExpiredCleanupIdxShape`；**变异验证**（`DROP INDEX` → `Seq Scan` → 用例报错）见 `docs/qa/gate-criteria.md` §9.2。**本机无 docker 不构成证据** | **D5 + D4-A/D + P1** | PASS-able · D5 |
| AC-ENG-03 | 禁 `AutoMigrate`/禁 `gorm.DeletedAt` 脚本门禁在 CI 拦截（**变异注入**：插一行 `DeletedAt` 与一处 `AutoMigrate(` → CI 必须失败）；应用启动**任何情况**不自动迁移（P1） | 配置检查（变异） | 技术栈 §5 + **P1** | PASS-able · P1 |
| AC-ENG-04 | 唯一约束冲突 → 映射到**接口 §5.4 + D3-F 追加项**的业务码；映射按 **SQLSTATE + 约束名双键**（禁止"23505 一律 409 / 23514 一律 400"）；23505 各类各触发一次；`mapPgError` 覆盖 I1–I9 且 **I6 无对外错误码** | 集成（DB 行为）：**唯一权威环境 = CI `integration` job**。**已真库证明**（run `41808510071`，`integration_only_cases=34`、`tests_run=208 tests_pass=208 tests_skip=0 cached=no`）：约束冲突→业务码的双键映射——`TestDBSessionsAcrCheckMapsTo400`（23514→400）、`TestDBFlowActiveUniqueMapsTo409`（23505→409）、`TestDBFlowCompletedRequiresUserMapsTo400`（I-F1）、`TestDBCH7UniqueRowsMapTo409`、`TestDBCH7FlowUniqueIsCooldown`。**仍缺**：标识符类 I1/I2/I3/I4/I6 的 DB 用例、「23505 各类各触发一次」的全量枚举 → 该部分**保持未验证** | **D3-B + D5 落地方案** | PASS-able · D3-B/D5 |
| AC-ENG-05 | 判据↔测试映射：CI 产出 `AC-ID → PASS / FAIL / NOT-IMPLEMENTED / PENDING / BLOCKED / 后置` **六栏并列**的映射表（`AC-AUD-10` 落在**列名「后置」**那栏）——**不得合并、不得省略任何一栏**：`PENDING` **不计入通过**（不得写成 `PASS`，也不得折叠进 `BLOCKED`）；出现 `NOT-IMPLEMENTED` 即门禁失败；`BLOCKED` 行必须带 R 编号、`PENDING` 行必须带 CH 编号；**「后置」栏必须显式列出 `AC-AUD-10`、状态值写全名「后置（DEFERRED）：本期不做判定」**（不计入通过、也不算失败、不得隐藏、不得写成 `PASS`）；本版 **`BLOCKED` 栏计数 = 0**、**「后置」栏计数 = 1** | 配置检查（CI 映射表 + **四道门禁 `go`/`race`/`web`/`integration` 全绿**，run `41803170713`）。⚠️ **R10 唯一未闭合**：branch protection 仍**无 `required_status_checks`**（`gh api …/branches/main/protection`）→ 门禁「CI 会红」但「红着仍可合」，待 admin(spencer) 开启；见 `docs/qa/gate-criteria.md` §9.5③ | 本文 §0-3 + m1 S11 + **D8-B【正式裁定】** | PASS-able |
| AC-ENG-06 | 证据留档：`artifacts/<commit>/<AC-ID>/{result.json,run.log}`，失败项附可复制复现命令 + `trace_id` | 配置检查 | 本文 §0-2 | PASS-able |
| AC-ENG-07 | `govulncheck` + `trivy` HIGH/CRITICAL 阻断；`gofmt -l` 为空；`vue-tsc --noEmit` 通过 | 配置检查 | 技术栈 §9.4 | PASS-able |
| AC-ENG-08 | **禁码 / 禁词扫描（v3 新增；v6 扩到三禁；v9 密级禁词扩为两个）**：`scripts/check-forbidden-error-codes.sh` 在 CI 阻断三类——①`SERVICE_DEGRADED` **永不出现在任何返回值/码表**（D8-C′⑤ 已废弃）；②`STEP_ALREADY_SATISFIED` **只允许出现在 `internal/flow`**（D3-F 限定返回点）；③**密级写法只许一种**：`dataLabel(` 与**裸 `clearance(`** 永不出现在 SDK 源码 / 示例 / 文档（D11：`dataLabel()` 作废，**`clearance()` 同属作废、不留别名**，唯一写法 `role(name).withClearance(level)`）。**扫描口径（v9 明示，避免假阳/假阴）**：正则 `(?<![A-Za-z0-9_])clearance\(`（**大小写敏感**）——`withClearance(` 的 `C` 大写且前缀为 `with`，**不命中**；`.clearance(3)`、`clearance(`、文档里 `` `clearance(` `` 均命中。**扫描范围 = SDK 三语言仓（`sdk-java`/`sdk-node`/`sdk-go`）的源码 + `examples/` + `README*` + 仓内 `docs/`**；**不含 `/docs/`（契约登记处）与基线文档**——作废声明本身必须能写出旧名，否则无法登记（这条只约束 SDK 仓，不约束 `/docs/baseline` 与 `decisions.md`）。**基线示例已被 D11 取代**：`/docs/baseline/` SDK 契约示例仍写 `.clearance(3)` / `.dataLabel("orders", 1)`，**照抄进 SDK 即红**，Nova 需按 D11 改写。**变异测试**：注入一个 `SERVICE_DEGRADED` 常量、或在 `internal/admin` 返回 `STEP_ALREADY_SATISFIED`、或新增一处 `dataLabel(`、或新增一处 `.clearance(` → CI 都必须失败（**四个注入各自单独验一次**；脚本存在不算通过；S11 只复核脚本仍在门禁且未被人为跳过） | 配置检查（变异） | **D8-C′⑤ + D3-F + D11** | **PASS-able · CH-4② 已追认（P8，经 hugo 追认 2026-11-02）**（断言内容一字未改） |

---

## 13. 判据 · Agent 面签名黄金向量（AC-AGENT，M2 前置）

> 出处：**D1**（`canonical = METHOD "\n" PATH "\n" SORTED_QUERY "\n" SM3HEX(body) "\n" TIMESTAMP "\n" NONCE`，HMAC-SM3）。D1 明文把「Gate 落黄金向量作互操作验收」派给本文；**不在 M1 门禁内**。m1 §7 已登记归属，并允许 S9 顺手落 `testdata/agent-hmac/*.json`（不计门禁）。

| ID | 断言 | 证据形式 | 出处 | 状态 |
|---|---|---|---|---|
| AC-AGENT-01 | **向量 A（空 query + 空 body）+ `PATH` 唯一取法（D1-A1 定稿）**：①`GET /api/v1/agent/policies` 无 query、空体 → `SORTED_QUERY=""`、`SM3HEX=SM3("")` 的 64 位小写 hex（**不得省略该段**）；②`PATH` = **客户端请求行里实际发出的 path**，含全部路径前缀（**含 context-path**）、不含 scheme/host/port、不含 query/fragment、**不 URL 解码、不去尾斜杠**（`/a/` 与 `/a` 是不同 PATH）、**空 path 按 `/`**；③三语言取值必须一致：Go `r.URL.EscapedPath()`、Java `request.getRequestURI()`（**含 contextPath；禁 `getServletPath()`**）、Node `req.originalUrl.split('?')[0]`；④**部署红线（检出即 FAIL）**：Agent 链路（业务系统→平台）配置里**不得出现 path 重写/前缀剥离**——扫描 nginx `rewrite` / `strip-prefix` / K8s ingress `rewrite-target`，命中即 FAIL（canonical 要求逐字节一致，重写必致互验失败）；⑤平台与 SDK 对同一向量签名**逐字节相同** | 单测（双端共享向量文件）+ 配置检查（部署红线扫描） | **D1-A1** | NOT-APPLICABLE · M2 前置（静态扫描可提前落，不计门禁） |
| AC-AGENT-02 | **向量 B（`SORTED_QUERY` 算法，D1-A2 定稿）**：①无 `?` 或其后为空 → 空串；②按 `&` 切分（空段丢弃），每段按**第一个** `=` 切 name/value（无 `=` → value 空串）；③**保留重复键**：`?a=2&a=1` → 两项都在、不合并不累加，输出 `a=1&a=2`（同名再按编码后 value 字节序）；④name 与 value **各自**先做 RFC3986 编码（保留 `A-Za-z0-9-._~`，其余 UTF-8 字节**大写**十六进制），**编码先于排序**；⑤排序 = 编码后 name 字节序升序、同名再按编码后 value 字节序升序（**两级键**）；⑥同名同值的重复项顺序无关、对结果串无影响；⑦示例 `?b=2&a=1&c=%E4%B8%AD` → `a=1&b=2&c=%E4%B8%AD`；⑧双端（Nova SDK ↔ Edge `internal/agent/hmac.go`）对同一向量结果**逐字节相同** | 单测（双端共享向量文件） | **D1-A2** | NOT-APPLICABLE · M2 前置 |
| AC-AGENT-03 | 拒绝路径：时间戳偏差 > ±300s → `AGENT_TIMESTAMP_SKEW`；同 `(client_id, nonce)` 第二次 → `AGENT_NONCE_REPLAY`（键 `agent:nonce:{client_id}:{nonce}`、TTL 600s）；`nonce` 非 **16~64 字节**的 `[A-Za-z0-9_-]`（base64url 去填充，**按字节计长**）→ 拒绝；secret 取 base64 **解码后 32 字节原始密钥**作 HMAC key | 集成 | **D1** + 接口 §5.4 E8 | NOT-APPLICABLE · M2 前置 |

---

## 14. `decisions.md` 回填台账

### 14.1 裁定 → 判据（v2 已回填，v3 增补）

| 裁定 | 一句话结论 | 落到的判据 | 当前状态 |
|---|---|---|---|
| **P1** | 不做自动迁移；`migrations/` 手动 / 一次性 Job | `AC-ENG-03` | PASS-able |
| **P2** | 先全链路**纯软国密**；**TLS 不属本项目范围**（v7 依 spencer 口径：TLS/TLCP/证书由部署方前置承担、本平台不判定，取代原「国密 TLS v1 只预留」表述） | `AC-DP-11`、`AC-AUD-12` | PASS-able |
| **P4** | module path = `github.com/spencerswagger/guanjin` | `AC-SDK-09` 扫描范围 | PASS-able |
| **P5** | 旧仓 SpIAM 冻结归档、只读参考（不改/不推/不开分支，代码只进 `GuanJin`） | 无 AC（全员红线，非判据；与 M1 无技术依赖） | — |
| **D1** | canonical 段序取接口 §5.3.1、`SHA256`→**SM3**；**A1 `PATH`**（含 context-path、不去尾斜杠、不解码、空 path→`/`、三语言取值 API、**禁中间件 path 重写**）；**A2 `SORTED_QUERY`**（保留重复键、编码先于排序、name→value 两级排序）；`nonce` 16~64 **字节** | `AC-AGENT-01~03` | N/A · M2 前置（v4 已按 A1/A2 定稿） |
| **D2** | `purpose` 冻结 6 值、CHECK 显式命名 | `AC-REG-01`、`AC-REG-03` | PASS-able |
| **D3-A** | 分页统一 `limit`+`cursor`（CH-3 **已追认**） | `AC-AUD-08` | N/A · M5（CH-3 已追认，断言可写死） |
| **D3-B** | 错误码一律 §5.4、`IAM_*` 作废、I6 无对外码 | `AC-REG-04`、`AC-ENG-04` | PASS-able |
| **D3-C** | 具名端点 65、Agent 面 2 条 | `AC-AGENT` 组范围 | PASS-able |
| **D3-D** | 账号级失败计数 Redis 权威、`attempts` 另算；**键名固定 `login:fail:{user_id}` / `login:lock:{user_id}`**（不得实现自改） | `AC-LOGIN-03` | PASS-able（v4 已对齐键名） |
| **D3-E** | 同场景双码**按语义分工**（CH-4③ **已追认**） | `AC-LOGIN-05`、`AC-SESS-01`、`AC-AUD-07` | **PASS-able · CH-4③ 已追认（P8，经 hugo 追认 2026-11-02）** |
| **D3-F** | **新增 `STEP_ALREADY_SATISFIED`（E4/409）**，只由 `internal/flow` 返回（CH-4① **已追认**） | `AC-FLOW-03`、`AC-ENG-08` | **PASS-able · CH-4① 已追认（P8，经 hugo 追认 2026-11-02）** |
| **D4-A** | 不写 DB 默认值，UUID v7 应用层生成 | `AC-ENG-02` | PASS-able |
| **D4-B** | 统一 HKDF-SM3 派生（5 用途域），编译期拒直传 | `AC-FC-03`、`AC-AUD-12` | PASS-able |
| **D4-C** | 月分区 + 主键 `(id, occurred_at)` + 锚 `anchor/segment` + `verified|broken|unverified` + Job 在 `guanjind` + 2 指标；**(4) `trace_id` 取证口径**：`flows.trace_id` 只记创建那次，**与后续 `auth_events.trace_id` 不一致不算缺陷** | `AC-AUD-02/05/06/11`、`AC-AUD-01` | PASS-able（v4 已补 (4) 口径） |
| **D4-D** | `agent_hmac_secret` 走既有三列；`is_confidential` 删除引用；非法 CHECK 不写 | `AC-ENG-02`、`AC-SDK-07` key 集合 | PASS-able |
| **D5** | 第 15 张表 `flows`：**13 列**（含 `trace_id`）、4 具名索引、**I-F1~F8**、两段式清理、`flow.ttl_seconds` seed 600 + 缺键 fail-fast、单向逻辑引用不加 FK | `AC-REG-06`、`AC-FLOW-01/03/10/11`、`AC-ENG-02`、`AC-AUD-01` | PASS-able（`AC-FLOW-01/03` 另受 CH-2/CH-4 约束） |
| **D6** | SDK 交付集 29 项（32 作废、缺号不补）、删 `mode`（**这两项即 CH-5，已追认（P8）**）；**(3)(5) 三处「纠错性默认值」与 (4) profile 门控 = 纯加固/纠错、不进 CH**：`jwks-uri`→`/.well-known/jwks.json`、`authz.replay-store=redis`、`agent.pull.enabled=false`、非 `local|test` 置 `false` → 启动失败。边界（Ark 三分判据）：「是否让业务方能用基线原本不允许的方式配置 SDK？」→ 不能，不进 CH；新增 key / 改 key 名 / 改数量仍需进 CH | `AC-SDK-01/07`（CH-5）、**`AC-SDK-02`（v6 PASS-able）**、`AC-SDK-03`、`AC-SESS-05`、`AC-SDK-10`、`AC-FC-05` | `AC-SDK-02/03/SESS-05/SDK-10` **PASS-able**；`AC-SDK-01/07`、`AC-FC-05` **v11 起亦 PASS-able（CH-5 已追认（P8，经 hugo 追认 2026-11-02））** |
| **D7** | Descriptor 补 4 字段 + `terminal_state`/`unreachable_reason`；不可达时 HTTP 200（CH-2 **已追认**） | `AC-FLOW-01/07` | **PASS-able · CH-2 已追认（P8，经 hugo 追认 2026-11-02）** |
| **D8-A** | 验收权威 = 移交 §十一 三线 + 技术栈 §9.3 | `AC-ENG-01`、文首声明 | PASS-able |
| **D8-B** | **密评整体后置**（v7 起；**等级口径不写在本条**，一律引 `D8-E` 第二版）；**传输段不在本项目范围**（TLS 由部署方前置承担、本平台不判定）；应用侧固定最高级国密（本期仍可独立判，见 `AC-AUD-12`）；**`AC-AUD-10` 呈现唯一口径（【正式裁定】）** = 「后置（DEFERRED）：本期不做判定」、计入「后置」栏、`BLOCKED` 栏 = 0，摘除依据 = 定义前提消失（R2/R4 后置、R3 关闭） | `AC-DP-11`、`AC-AUD-12`；`AC-AUD-10` → **「后置（DEFERRED）：本期不做判定」** | PASS-able；`AC-AUD-10` 记「后置」、`BLOCKED` = 0（R2/R3/R4 挂起） |
| **D8-E 第二版**（v10 替换旧版） | **密评等级口径**：①统一表述 = 「**实现按 GB/T 39786 第三级条款固定，二级场景为其子集；判定与出证后置**」，条款引用**保持 8.x、不降 7.x**；②**TLS 写责任转移、禁写「无影响」**（二级 7.2 c)/7.4 c) 为「宜」、三级 8.2 c)/8.4 d) 为「应」→ 本平台不因 TLS 被判不符合，风险由部署方前置 TLS 承担）；③**密码产品不承诺「密评通过」**（算法库 ≠ 密码产品，纯软对 GB/T 37092 二级无解，V1 只「自评 + 遗留风险登记」）。**旧版「密评上限二级」作废**；依据 GB/T 39786-2021 第 4 章 + 密评 FAQ 三版 1.2/1.3（等级随接入系统等保定级、不得低于备案等级） | `AC-AUD-10`（§11.1 整节 + I-1/I-3）、`AC-DP-11`（**判据不变**） | PASS-able（口径）；`AC-AUD-10` 仍记「后置」 |
| **D12**（新） | **`acr` 取值域**：对外只 `AAL1`/`AAL2`；`AAL0` **仅展示不落库**；`AAL3` **预留但 V1 任何路径不得产出**（`L3` 档位 → `AAL2`；注入 `acr='AAL3'` 必须被拒） | `AC-FLOW-12`、`AC-FLOW-13`（另与 `AC-LOGIN-06` 交叉） | PASS-able · D12（`decisions.md` D12 已落文并复核，见 §15-G13 闭环） |
| **D8-C** | 具体依赖发具体码、统一 503 + `Retry-After` | `AC-FC-01/02/03`、`AC-FC-08` | PASS-able |
| **D8-C′** | E6 七码归位：`INTERNAL_ERROR`(500) / 三依赖码(503) / `DEPENDENCY_UNAVAILABLE` 只兜未分类 / `UPSTREAM_UNAVAILABLE` 归数据面转发 / **`SERVICE_DEGRADED` 废弃** + 禁码扫描 | `AC-FC-02`、`AC-FC-07`、`AC-DP-13`、`AC-ENG-08` | `AC-DP-13`/`AC-FC-07` PASS-able；`AC-ENG-08` **PASS-able · CH-4② 已追认（P8，经 hugo 追认 2026-11-02）** |
| **D8-D′** | `/userinfo` 零查库才允许 200（SQL 计数=0），查库即 503 `DB_UNAVAILABLE` | `AC-FC-08` | PASS-able |
| **D9** | 引用缺章兜底顺序 | `AC-LOGIN-01`、`AC-FLOW-01`、`AC-AUD-05` | PASS-able |
| **D10**（新） | `X-GuanJin-Assertion` 剥离的**唯一六步时序**：读→验签→claim→注入→**替换请求视图**→handler；剥离对象是**交给宿主的请求视图**而非原始凭证；否决「验签前剥离」与「只在下游转发前删一次」；网关侧「先弃后签」是另一个剥离 | `AC-SDK-08`（重写）、`AC-DP-02`、`AC-DP-03` | PASS-able · D10 |
| **D11**（新） | SDK 能力命名冻结：`GJ-CAP-13` = `role(name).**withClearance(level)**`；`dataLabel()` **作废且不留别名**；**（v9 补口 C1）`clearance()` 同属作废**——基线 SDK 示例的 `.clearance(3)` 属旧写法，唯一写法只有 `withClearance(level)`；完整签名按索引 §4.2-25 五项，能力条数仍 22 | `AC-ENG-08`③（v9 起：禁词 = `dataLabel(` + 裸 `clearance(`，排除 `withClearance(`，四个注入各验一次）；SDK 授权 API 的其余判据归 M5，届时以 D11 为准 | PASS-able · D11（`AC-ENG-08` 的 CH-4② 已于 v11 追认、整条转 `PASS-able`；③项本来自 v9 起先行生效） |

### 14.2 契约变更清单（CH）对本文的影响

| CH | 变更点 | 受影响的判据 | 处理 |
|---|---|---|---|
| **CH-1** | `/auth/challenge/{factor}` 在 `purpose∈{login,step_up}` 时 **`flow_id` 必填** | `AC-FLOW-03`、`AC-FLOW-10` | **已追认（P8，经 hugo 追认 2026-11-02）**。`AC-FLOW-10` 的断言只依赖"该流存在"、本就不依赖必填语义；`AC-FLOW-03` 的 `flow_id` 部分现按已追认口径写（`purpose∈{login,step_up}` 必填），断言内容不变 |
| **CH-2** | Descriptor 新增 `challenge_id`/`purpose`/`contributes_acr` + `terminal_state`/`unreachable_reason` | `AC-FLOW-01`、`AC-FLOW-07` | **已追认（P8）**：新字段（`challenge_id`/`purpose`/`contributes_acr`/`terminal_state`/`unreachable_reason`）断言**自 v11 起生效**，两条判据转 `PASS-able`；断言内容不变 |
| **CH-3** | 分页 `limit`+`cursor`，`page/page_size/total` 作废 | `AC-AUD-08`（M5） | **已追认（P8）**：M5 断言按 `limit`+`cursor`、响应 `{items, next_cursor}` 写，**不得**再按基线 `page/page_size`/`total` 写；`AC-AUD-08` 仍记 N/A·M5（与 CH 无关） |
| **CH-4** | ①新增 `STEP_ALREADY_SATISFIED`；②废弃 `SERVICE_DEGRADED`；③双码语义分工；**④（v10 补登）§5.4 完整枚举漏登记 3 个在用错误码**：`INVITE_CODE_INVALID`（E1/400）、`WEAK_PASSWORD`（E4/422）、`REGISTRATION_NOT_ALLOWED`（E4/**403**，按 §5.4「接口可覆盖」）；**⑤（v10 新增）口令三码固定顺序** `WEAK_PASSWORD → PASSWORD_REUSED → PASSWORD_POLICY_VIOLATION`，`code` 只回首个、**全量放 `details.violations[]`** | `AC-FLOW-03`、`AC-ENG-08`、`AC-LOGIN-05`、`AC-SESS-01`、`AC-AUD-07`、**`AC-REG-08`（④）、`AC-REG-09`（④）、`AC-REG-10`（⑤）** | **已追认（P8）①~⑤**：`AC-FLOW-03`（①）、`AC-ENG-08`（②）、`AC-LOGIN-05`/`AC-SESS-01`/`AC-AUD-07`（③）、`AC-REG-08/09`（④）、`AC-REG-10`（⑤）**8 条全部转 `PASS-able`**，**码值与 `details.violations[]` 结构自 v11 起可写成既定事实**（`AC-REG-10` 的固定顺序与全量 `violations[]` 直接断言）；断言内容不变 |
| **CH-5** | **已收窄**：只剩「29 项交付集 + 删 `guanjin.mode`」——`jwks-uri` 默认值已移出（与 `replay-store`/`agent.pull.enabled` 同属纠错性默认值，按 D6(3)/(5) 处理、不追认）；`AC-SDK-02` 的 profile 门控按 D6(4) **纯加固**排除（不进 CH） | `AC-SDK-01`、`AC-SDK-07`、`AC-FC-05`（`AC-SDK-03` 与 `AC-SDK-02` **均已降为 PASS-able**） | **已追认（P8）**：29 项交付集 + 删 `guanjin.mode` 自此为**已生效对外契约**，三条判据转 `PASS-able`；断言内容不变 |
| **CH-6** | 端点计数 65 / Agent 面 2 条 | 无 | 内部口径，**无需追认**，不阻塞任何判据 |

---

## 15. 残余缺口与已收口项（v11 更新）

| # | 缺口 | 现状 | 归属 |
|---|---|---|---|
| ~~G1~~ | `SERVICE_DEGRADED` 用途 | **已闭合**：D8-C′⑤ 废弃、永不返回；`AC-ENG-08` 机器拦截（变异验证）+ `AC-FC-07` 负向断言 | 闭环 |
| ~~G2~~ | `done` 重输的码 | **已闭合**：D3-F 分配 `STEP_ALREADY_SATISFIED`（E4/409）；`AC-FLOW-03` 已改为可断言期望 | 闭环 |
| ~~G3~~ | m1 未引用本文件判据 | **已闭合（正文已对齐 v8）**：m1 §7 已把 **7 条 M1 外判据**登记完（**6 条 N/A**：`AC-FLOW-08`、`AC-AUD-09`（M2）、`AC-AUD-08`（M5）、`AC-AGENT-01~03`（M2 前置）+ **1 条后置**：`AC-AUD-10`）；原「8 条」把 `AC-AGENT-03` 重复计，已订正；`AC-FLOW-10/11`、`AC-AUD-11/12` 已并入 S5/S10/S11，全表 85 条无漏引 | 闭环 |
| ~~G12~~ | `AC-AUD-10` 两种标签（BLOCKED vs 后置） | **已闭合（v7 定为后置；v8 依 D8-B【正式裁定】收口为唯一一种呈现）**：状态名 = **「后置（DEFERRED）：本期不做判定」**、计入「后置」栏、`BLOCKED` = **0**；摘除依据 = `BLOCKED` 定义前提「期望值依赖残余待裁项」已消失（R2/R4 后置、R3 关闭）；后置 ≠ 免验（`I-1~I-7` 仍须 M2 前备齐、不得写通过、不得写「预检通过」、不得从账目摘掉、恢复由 spencer 重开） | 闭环（正式裁定） |
| ~~G4~~ | 密评「预检通过」证据形式未定义 | **已转后置**：密评整体后置（spencer 2026-11-02），`AC-AUD-10` 记「后置（DEFERRED）：本期不做判定」；I-6 保留在 §11.1 作为重启密评时的输入要求，**本期不得出现「预检通过」字样** | 闭环（后置） |
| ~~G5~~ | m1 的「I-F1~F6」旧口径 | **已闭合**：m1 S2 阻塞说明与 §5 阻塞矩阵均已改为 **I-F1~F8**（含 I-F7 回填、I-F8 锁序/锁内禁重活），`AC-FLOW-11` 逐项覆盖 | 闭环 |
| ~~G6~~ | m1 §8 DoD 计数 | **已闭合（正文已对齐 v8）**：m1 §8 已按现行 **85 条 = 68 `PASS-able` / 10 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置」** 记账，并写明 `PENDING` 追认前不计入通过、「后置」不得写成通过；原「84 条 / 66/12」已随 G9 一并更新 | 闭环 |
| ~~G7~~ | 失败计数键名 | **已闭合**：`login:fail:{user_id}` 与 `login:lock:{user_id}` 已写进 **D3-D**，`AC-LOGIN-03` 已对齐（小写带连字符 uuid、TTL 取 `login_policies`） | 闭环 |
| ~~G8~~ | `flows.trace_id` 与 `auth_events.trace_id` 不一致是否缺陷 | **已闭合**：**D4-C(4)** 已定"不一致不算缺陷、用 `flow_id` 关联"；`AC-AUD-01` 已加**反向断言**（两者不等仍 PASS；`flows.trace_id` 被补步覆盖才 FAIL） | 闭环 |
| ~~G9~~ | m1 曾 3 处写「v2，82 条」，后又滞后为 v5 的 67/11 | **已闭合（正文已对齐 v8，2026-11-02）**：m1 卷首「输入」、§4 状态口径、§6 开头与契约变更行、§7 开头、§8 DoD **共 6 处**已统一为 **85 条 = 68 `PASS-able` / 10 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置（DEFERRED）：本期不做判定」**（`AC-AUD-10` 计入「后置」栏），并注明「条数同 v4、只改状态」（`AC-SDK-03`、`AC-SDK-02` 两次降级）。Ark 已完成改齐，本文件 v8 与 m1 同数、Spec 终次抽查判「一致」 | 闭环 |
| ~~G11~~ | `AC-SDK-02`（profile 门控 fail-fast）该不该进 CH | **已闭合（Ark 选方案②）**：D6(4) 属**纯加固裁定、不进 CH**——非 `local|test` 置 `false` 本就是基线禁止项（SDK §3.2 + 移交 §九），D6(4) 只把「告警」升级为「启动失败」让禁令可执行，不新增契约面；Ark 已在 `decisions.md` 契约变更清单末尾落**三分边界 + 判据**（「是否让业务方能用基线原本不允许的方式配置 SDK？」→ 不能，不进 CH）。`AC-SDK-02` 已降 `PASS-able`，账目 11→**10** | 闭环 |
| ~~G16~~（v11 新） | 契约变更清单的「追认」没落到判据侧（13 条一直挂 `PENDING`） | **已闭合（v11）**：`decisions.md` **P8**（经 hugo 追认 2026-11-02）把 CH-1~CH-5 全部转「已追认」并写死记录法；本文 13 条 `PENDING` 一次转 `PASS-able`，账目 **90 = 83/0/0/6/0/1**，**断言一字未改**。**待 Ark 同步**：`m1-slices.md` 若仍记 `70/13` 需按本版改齐（我不动他文件） | 闭环（v11）；m1 同步待 Ark |
| ~~G10~~ | m1 卷首「输入」行曾写 `decisions.md`「P1–**P4**」，漏 **P5**（旧仓 SpIAM 冻结归档、只读红线） | **已闭合（正文已对齐 v8）**：m1 卷首现为「**D1–D11、P1–P5**、R2–R5/R8/R9」，`P5` 已可见；红线另在 m1 铁律行与 S0 红线同步 | 闭环 |
| ~~G13~~（v10 新，**v10.1 已闭合**） | `decisions.md` 落文缺口：`D8-E` 第二版 + 新 `D12` 曾查不到 | **已闭合（Gate 逐条复核通过，2026-11-02 15:0x）**：①`D8-E` **第二版 §①–⑦ 全部在位**（第 165–172 行）：①等级随备案等级、旧版「上限二级」作废并标 **【已作废 · 2026-11-02】** 原文留档（第 156–157 行）；②条款保持 8.x 禁降 7.x，二级不作要求项点名 8.2 e)/8.3/8.4 h)；③**TLS 条款号与本文件一致**（二级 7.2 c)/7.4 c)「宜」、三级 8.2 c)/8.4 d)「应」，且写明「宜」未声明不适用仍判部分符合扣分、**禁写"无影响"**）；④密码产品只「自评 + 遗留风险登记」（含 **37092 一级纯软可达 / 二级无解**）；⑤不改判据；⑥承接旧版仍有效三条；⑦点名影响面。②`D12` 在第 209–214 行，含 `acr` 取值域 / `AAL0` 不落库 / `AAL3` 不得产出（白名单断言 + 两条用例），**并在「受影响/落地」直接引用了 `AC-FLOW-12/13`（`PASS-able·D12`）**。③**B″ 补登记表 6 行在位**（第 68–73 行）：`INVITE_CODE_INVALID` E1/400、`REGISTRATION_NOT_ALLOWED` E4/403、`WEAK_PASSWORD` E4/422、`PASSWORD_REUSED`、`PASSWORD_POLICY_VIOLATION`（顺序与互斥）、`STEP_ALREADY_SATISFIED` E4/409——**判据 ID 与本文一一对应、无错号**。④账目 **90 = 70/13/0/6/0/1** 在 `decisions.md` D8-B（第 153 行）与 `m1-slices.md` **6 处**（卷首、§4 状态口径、§6、§8 DoD ×2、§7 归属段）均为同数，**分组 REG10/LOGIN8/FLOW13/SESS5/DP13/SDK10/FC8/AUD12/ENG8/AGENT3 与本文实测逐项相符**（Gate 用脚本核过 90 行、无重号）。**残留（P2、不影响判据）**：旧版「裁定 E」第 161 行仍留一句「Spec 对外统一一句…引用上限二级」，处在作废块内但**未加行内作废标记**，grep `上限二级` 会命中 → 建议 Ark 就地补「（作废）」二字，避免被当现行口径引用 | 闭环（Ark 落文 + Gate 复核通过） |
| ~~G14~~（v10 新，**v10.1 已闭合**） | 接口 §5.4「完整枚举」漏登记 3 个在用错误码 | **已闭合（v10.1）**：`decisions.md` **D3-B′ / B″ 补登记表**（第 63–75 行）已把这 3 码集中登记（`INVITE_CODE_INVALID` E1/400、`WEAK_PASSWORD` E4/422、`REGISTRATION_NOT_ALLOWED` E4/403 覆盖），并写明「前 3 行是 §5.4 枚举里没有、端点行在用的码；下版 OpenAPI 直接照此表落盘」——**不改基线 §5.4（基线只读），以补登记表替代**。**判据侧不变**：`AC-REG-08/09` 仍记 `PENDING · CH-4④`（**`CH-4` 整体在 `decisions.md` 仍标「待追认」**，第 227 行），**不因登记表落地就自行解封**；spencer 追认 CH-4 后转 `PASS-able`，断言内容不变 | 闭环（待 CH-4 追认后改状态） |
| **G15**（v10 新） | **条数口径变更未同步 m1** | **已闭合（v10.1，Gate 实测）**：`m1-slices.md` **6 处**（卷首「输入」、§4 状态口径、§6、§8 DoD 两处、§7 归属段）已全部为 **90 = 70 `PASS-able` / 13 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置」**，CH-4 影响面已含 `AC-REG-08/09/10`、分组 **REG10/LOGIN8/FLOW13/SESS5/DP13/SDK10/FC8/AUD12/ENG8/AGENT3** 与本文脚本实测逐项相符；`m1` §7 仍记「7 条 M1 外判据（6 N/A + 1 后置）」、§8 `NOT-IMPLEMENTED` = 0 | 闭环 |

---

## 16. 明确不纳入 M1

| 项 | 为什么不进 M1 | 归属 |
|---|---|---|
| TOTP / 短信 / 邮箱因子与两级阶梯完整链路 | M1 只有密码因子，AAL2 不可达 | M2/M4（`AC-FLOW-08`、`AC-AUD-09`） |
| 找回 / 换绑 / 统一恢复模型 | 依赖标识符自助面与通知 | M3 |
| 注销 / 数据导出（PIPL） | 依赖留存与导出脱敏口径（+CH-3） | M5（`AC-AUD-08`） |
| 管理面 35 条全量 CRUD 与三员全矩阵 | M1 只测 `sessions` 撤销 + 越权负向 | 逐片推进（`AC-AUD-07`） |
| 密钥轮换状态机 / JWKS 轮换窗口 | M1 只测静态双密钥发布 | M5（`AC-LOGIN-07`） |
| SDK 授权 API / 22 条能力签名（含 `GJ-CAP-13` 的 `withClearance(level)`，D11） | M1 只验「接入 + 验签 + 剥离」，不验本地授权判定模型 | M5（届时按 **D11** 与索引 §4.2-25 五项签名写判据） |
| Agent 面 `report`/`policies` 与 HMAC 互操作 | D1 已冻结但不在 M1 链路 | M2 前置（`AC-AGENT-01~03`） |
| **TLS / TLCP / 证书校验 / 国密 TLS** | **完全不属本项目范围**（spencer 口径 2026-11-02）：「TLS / WAF / LB / 灰度 / 熔断由前置 nginx / Kong 承担，**不属本平台验收面**」，本平台**不做任何 TLS 相关判定**，也不设「跳过 TLS」的暗门 | 部署方前置（非本平台） |
| 密评实证 / 送检 / 预检结论 | **整体后置**（spencer 口径）：本期不出证、不送检、不写结论；判据与输入清单保留在 §11.1，账目记 1 条「后置（DEFERRED）：本期不做判定」 | 后置（重启密评时启用） |
| 数据面 QPS 压测基线 | 索引 §4.3-1 未估算 | 待 R8 |
| 等保三级年度渗透测试、集中管控实证 | 建设方与测评机构动作 | 验收阶段外部证据 |

---

## 17. 维护

- **权威序**：索引 §2 → 端点/数据契约 → `decisions.md` → 本文件；不一致时以前者为准并回改本文件。
- **ID 纪律**：既有 ID 不重排、不改名；新增只在组末追加，并同步 §2 统计与 §3 映射（m1 由 Ark 更新）。
- **`PENDING` 的解封条件**：`decisions.md` 契约变更清单里对应 CH 被 spencer 追认 → 把该行状态改为 `PASS-able · CH-n 已追认`，断言内容不变；若被否 → 按基线原文重写该行，并在此登记"被否 + 回改"。
- 变更记录：v1（75 条，23 条带阻塞标记）→ v2（82 条：75/1/6/0，回填 D1–D9 + P1–P4）→ **v3（84 条：65/12/1/6/0）**：①`AC-AUD-10` 新增 §11.1 判定输入清单（7 项，仍 `BLOCKED`）；②`AC-FLOW-10` 按两段式清理定稿为稳定断言；③`AC-FLOW-03` 补 `409 STEP_ALREADY_SATISFIED`；④`SERVICE_DEGRADED` 全量废弃（新增 `AC-DP-13` 上游码、`AC-ENG-08` 禁码扫描）；⑤`AC-ENG-02` 跟到 `flows` 13 列 + 4 索引 + I-F1~F8（I-F7/I-F8 落在 `AC-FLOW-11`）；⑥引入 `PENDING · CH-n` 状态，12 条依赖待追认契约的判据按待定计→ **v4（85 条：66/12/1/6/0）**：①`D1-A1`（`PATH` 含 context-path、不去尾斜杠、不解码、空 path→`/`、三语言取值、**禁中间件 path 重写且检出即 FAIL**）与 `D1-A2`（保留重复键、编码先于排序、name→value 两级排序、`nonce` 按**字节**计长）写入 `AC-AGENT-01/02/03`；②`D6(5)` 两处默认值写入 `AC-SESS-05`（多副本口径 = 非 `local|test`，不新增配置键）与新增 `AC-SDK-10`；③`D3-D` 键名对齐 `AC-LOGIN-03`；④`D4-C(4)` 写入 `AC-AUD-01` 并加**反向断言**（trace 不一致不算缺陷）；⑤`PENDING` 单列进 `AC-ENG-05` 映射表（不计入通过）；⑥`AC-DP-13` 归 S8、`AC-ENG-08` 归 S1（S11 只复核）已在 §3 标注。 → **v5（85 条：67/11/1/6/0，条数不变、只改状态）**：①按 **D10** 重写 `AC-SDK-08`（六步时序 + 替换请求视图 + 三处消费者 + **反向断言：篡改令牌仍 401**），并在 `AC-DP-02/03` 加「网关侧先弃后签 ≠ SDK 侧剥离」区分；②`AC-SDK-03` 由 `PENDING·CH-5` **降为 `PASS-able`**（`jwks-uri` 属 D6(3) 纠错性默认值、已移出 CH-5），PENDING 12→11；③`AC-SDK-05`/`AC-FC-04` **点名 503 `KEY_UNAVAILABLE`** 并禁 `DEPENDENCY_UNAVAILABLE`/`SERVICE_DEGRADED`；④`AC-FC-05` 收敛为「仅 29 项交付集待追认」；⑤登记 **D11**（本文无判据涉及 `withClearance`/`dataLabel`，`dataLabel(` 建议并入禁词扫描）；⑥新增 §15-**G11**（`AC-SDK-02` 的 profile 门控未进任何 CH，暂保守记 PENDING）与 G9 更新（m1 需按 67/11 改计数）。 → **v6（85 条：68/10/1/6/0，条数不变、只改状态）**：①**G11 闭合**——`AC-SDK-02` 按 D6(4)「纯加固裁定、不进 CH」降为 `PASS-able`（注释性引用 SDK §3.2 / 移交 §九；判据取 Ark 三分判据），PENDING 11→**10**；②`dataLabel(` 已并入 `AC-ENG-08` 禁词扫描（与 `SERVICE_DEGRADED` 同脚本，S1 建、S11 复核）；③账目与仍 PENDING 的 10 条已在 §2 点明，并已同步 Ark 以便 m1 一次改齐 68/10。 → **v6.1（同日，同步 Ark 后）**：`AC-AUD-10` 增「后置」标注（密评整体后置，经 Ark 转述；**`BLOCKED` 不摘除**），新增 §15-**G12** 统一「BLOCKED vs 后置」两种切法（总数同为 85），并在 §11.1 写明「后置 ≠ 免验、7 项输入仍须备齐、摘除 BLOCKED 需 `decisions.md` 正式裁定」。 → **v7（85 条：68/10/0/6/1，条数不变）**：①**密评整体后置**（spencer 2026-11-02「纯软实现，暂不管密评」）——`AC-AUD-10` 由 `BLOCKED` 改为「后置（DEFERRED）：本期不做判定」、`BLOCKED` 归零、R2/R3/R4 挂起，新增第五态并写进状态表与 CI 映射表要求；②§11.1 整表保留为**重启密评时的输入清单**，删去「对口径中」类措辞，I-2 改为「传输段归属（本平台不判定）」；③**TLS 完全不属本项目范围**——`AC-DP-11`、§16、`P2`/`D8-B` 台账统一改为「由前置 nginx/Kong 承担、不属本平台验收面、本平台不做 TLS 判定」，保留「不设跳过暗门」一句，非 TLS 判据（验签/注入/剥离/明文转发）**一条不减**；④边界：**等保相关代码判据（`AC-AUD-02/03/04/07/09` + `AC-AUD-12` 应用侧国密）一条不动**，后置只涉「出证/送检/预检结论」；⑤G4 转后置闭环、G12 闭合。 → **v8（85 条：68/10/0/6/0/1，条数不变、只改呈现）**：依 `decisions.md` **D8-B【正式裁定】（2026-11-02，收本文 G12）**，`AC-AUD-10` **全文件统一为唯一一种写法**「**后置（DEFERRED）：本期不做判定**」——①状态定义（§0-3）、账目行（§2，新增「后置」栏）、判据行（§11）、§11.1 标题与状态行、S11 归属行（§3）、§14.1 `D8-B` 台账、§15 `G12`/`G4`、§16 全部改为该写法，**全文件只有这一种状态名、不再保留任何旧标签**；②§11.1 增**正式摘除 `BLOCKED` 的依据**（`BLOCKED` 定义前提「期望值依赖残余待裁项」已消失：R2/R4 后置挂起、R3 关闭）与**硬约束**（后置 ≠ 免验：`I-1~I-7` 仍须 M2 前备齐、不得写成通过 / 不得写「预检通过」/ 不得从账目摘掉、恢复判定由 spencer 重开）；③`AC-ENG-05` 映射表由五态改为**六栏并列**（`PASS`/`FAIL`/`NOT-IMPLEMENTED`/`PENDING`/`BLOCKED`/「后置（DEFERRED）」），`BLOCKED` 栏 = 0、「后置」栏 = 1 必须显式输出；④修掉 §1「`PENDING` 以 `BLOCKED` 记账」的旧矛盾表述（`PENDING` 单列、`BLOCKED` 恒 0）；⑤账目唯一式：**85 = 68 `PASS-able` / 10 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置」**；⑥`decisions.md` 与 `m1-slices.md` 按卷首写者纪律**只由 Ark 写**，本文不改那两份，差异按本版回报。 → **v9（85 条：68/10/0/6/0/1，条数不变、只扩禁词）**：应 Ark 请求（`decisions.md` D11 补口 **C1**）——①`AC-ENG-08`③ 由单一 `dataLabel(` 扩为**两个禁词** `dataLabel(` + **裸 `clearance(`**（`clearance()` 同属作废、不留别名，唯一写法 `withClearance(level)`），并写明扫描正则 `(?<![A-Za-z0-9_])clearance\(`（大小写敏感，`withClearance(` 不命中）、扫描范围（SDK 三语言仓源码/示例/README/仓内 docs；**不含** `/docs/` 与基线），以及"基线示例 `.clearance(3)` / `.dataLabel("orders", 1)` 照抄即红"；②变异测试由三个注入扩为**四个注入各自单独验一次**（新增"注入一处 `.clearance(` → 必须红"）；③§14.1 `D11` 行同步补 `clearance()` 作废与 `AC-ENG-08`③ 落点；④**账目、状态分布、等保代码判据（`AC-AUD-02/03/04/07/09/12`）一律未动**（B1 的 level 词表属 M5/ADR-004，本文无判据依赖）。 → **v10（90 条：70 `PASS-able` / 13 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置」，条数 +5）**：①**`D8-E` 改第二版**（spencer 2026-11-02，旧版「密评上限二级」**作废**）——`AC-AUD-10`、§11.1 状态行 + **I-1 改题**（不再问"是否确认二级"，改问**备案等级**；数据模型引「三级 8.3 e)/8.4 g)」**不再按"引错级别"处理、照引用不降级**）+ **I-3 改判**（纯软对 GB/T 37092 二级**无解**，材料只用于「自评 + 遗留风险登记」，**不得因自评齐备记 `PASS`**）、§14.1 台账新增 `D8-E 第二版` 行；统一表述 = 「**实现按 GB/T 39786 第三级条款固定、二级为其子集；判定与出证后置**」，**条款引用保持 8.x、不降 7.x**，**TLS 写责任转移、禁写「无影响」**；**`AC-DP-11` 判据不变**；②**新增 5 条判据**：`AC-REG-08`（`INVITE_CODE_INVALID` E1/400）、`AC-REG-09`（`REGISTRATION_NOT_ALLOWED` E4/**403**，按 §5.4「接口可覆盖」）、`AC-REG-10`（口令三码**固定顺序** `WEAK_PASSWORD → PASSWORD_REUSED → PASSWORD_POLICY_VIOLATION`，`code` 只回首个、全量进 `details.violations[]`）——三条均 `PENDING · CH-4④/⑤`；`AC-FLOW-12`（`L3` 档位 → `AAL2`；任何表零 `AAL3`/零 `AAL0`）、`AC-FLOW-13`（合法签发 `acr=AAL3` 令牌 → 401、入参 `acr=AAL3` → 400，**不得静默降级**）——两条 `PASS-able · D12`；③§14.2 **CH-4 扩为①~⑤**（+补登 3 码 + 口令顺序），影响面 5 条 → **8 条**；④新增 §15-**G13**（`decisions.md` 落文缺口：**文中仍是旧版 `D8-E`、无 `D12`**，Gate 已逐行实证）、**G14**（§5.4 枚举漏登 3 码，按 D9 兜底①定账）、**G15**（条数 85→90 待 Ark 同步 m1）；⑤**等保代码判据（`AC-AUD-02/03/04/07/09/12`）一条未动**，密评**仍后置**、`BLOCKED` 仍 **0**。 → **v10.1（90 条：70/13/0/6/0/1，条数不变、只复核）**：Ark 三条落文后 Gate 逐条复核——①`D8-E` 第二版 §①–⑦ 与本文口径**逐句对得上**（含 TLS 条款号 7.2 c)/7.4 c) 宜、8.2 c)/8.4 d) 应），据其原文**补两处限定**：**「宜」未书面声明不适用时仍纳入测评判「部分符合」扣分 → I-2 归属声明非可选**、**37092 一级纯软可达 / 二级无解**；②`D12` 在位、**直接引用** `AC-FLOW-12/13`，判据 ID 无误；③`D3-B′/B″` 补登记表 6 行与本文 `AC-REG-08/09/10`、`AC-FLOW-03` **一一对应、无错号**；④`m1-slices.md` 6 处同数、10 组分组与脚本实测相符；⑤`G13`/`G14`/`G15` **全部闭环**（残留 P2 一项：旧版裁定 E 第 161 行仍留「引用上限二级」一句在作废块内、未加行内作废标记，建议 Ark 补「（作废）」）；⑥**决定：维持 90**，`AC-REG-08/09` 不单独解封（`CH-4` 整体仍待追认），待追认后一次转 8 条 → **90 = 78 / 5 / 0 / 6 / 0 / 1**。 → **v11（90 条：83 `PASS-able` / 0 `PENDING` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置」，条数不变、只改状态）**：①`decisions.md` **P8**（经 hugo 追认 2026-11-02）把 **CH-1~CH-5 全部转「已追认」**，本文 **13 条 `PENDING · CH-n` 一次全部转 `PASS-able`**（CH-2 两条、CH-4①~⑤ 八条、CH-5 三条），**断言内容一字未改、未新增/重排任何 AC ID**；②账目 `70/13` → **`83/0`**，`BLOCKED` 仍 0、「后置」仍 1（`AC-AUD-10` 不动，密评口径以 `qa/compliance-precheck.md` 为准）；③§0-3 的 `PENDING` 定义改为「本版 = 0、保留定义供后续新增 CH」，`AC-ENG-05` 的映射表**仍须显式输出 `PENDING` 列**（计数 0）；④§14.2 CH 表全部改「已追认（P8）」并写明断言自 v11 起生效；⑤新增 §15-**G16** 闭合本条；⑥**等保代码判据（`AC-AUD-02/03/04/07/09/12`）与 `AC-DP-11` 一条未动**；⑦**与 Ark 算式核对一致**（`90 = 83/0/0/6/0/1`）。
- **v14（2026-11-04，只改证据/下限，账目仍 90 = 83/0/0/6/0 + 1 后置）**：按 main `5828f10` / run `41808510071` 实测——`16/u create_flows_expired_cleanup_idx` 已跑、`flows_named_indexes=5`、`integration_only_cases=34`、`tests_run=208 pass=208 skip=0 cached=no`；升级 `AC-AUD-02`（`TestDBAuditLogsAppendOnlyTriggerExists` 父表+分区各 1、`TestDBAuditLogsRejectsUpdateAndDelete` 断言 `23001` 且行保留）、`AC-FLOW-10`④（`Stage1UsesExpiresIndex`/`Stage2UsesExpiredCleanupIdx`/`Stage2KeepsRowsWithinRetention`）、`AC-FLOW-11`④ I-F8（`TestDBFlowstoreHeavyWorkRunsOutsideFlowLock`：10 万迭代期间 `FOR UPDATE NOWAIT` 必须拿到锁 + 外部持锁时 `Advance` 必须阻塞）；`AC-ENG-02` 索引 4→5；`AC-ENG-01` 补 `flowstore` 85%。**仍不升级**：`AC-FC-03` 运行期 503（无用例）、`AC-ENG-04` 的 I1/I2/I3/I4/I6 枚举与「23505 全量」
- **v13（2026-11-04，只改证据，账目仍 90 = 83/0/0/6/0 + 1 后置）**：①按 main `f6aa05e` / run `41803170713` 实测——`integration_tagged_files=4`、`integration_only_cases=24 (all=144 plain=120)`、`tests_run=144 tests_pass=144 tests_skip=0 tests_fail=0 cached=no`、四 job 全 success；②**升级**：`AC-ENG-04`（双键映射 4+1 条映射用例）、`AC-FLOW-10`（I-F3/I-F4/I-F6/abandoned/ttl）、`AC-FLOW-11`（I-F1/I-F5/I-F7 + 唯一索引 23505→409）→「**已真库证明**」；③**仍不升级（不夸大）**：`AC-AUD-02` 触发器官有、`AC-FC-03` 运行期 503、`AC-ENG-04` 的 I1/I2/I3/I4/I6 枚举、`AC-FLOW-10` 的 `EXPLAIN` 命中索引、`AC-FLOW-11` 的 I-F8 行为断言；④`AC-ENG-05` 记 **R10**（`required_status_checks` 待 admin）为**唯一未闭合**项。
- **v12（2026-11-04，只改证据/出处 + §0-2 一句，账目仍 90 = 83/0/0/6/0 + 1 后置）**：①`decisions.md` 已加 CI `integration` job（PR#10 / main `2ef3bd7`），据此把「依赖类判据的权威环境」写死为 **CI `integration` job**、本机无 docker 不算证据、**SKIP = 未验证 = 红**；②`AC-ENG-02` 证据改为该 job 的 **schema 断言（六项真断言，形如 `[ x = y ] || exit 1`）**并记实测锚点 run `41800113745`；`AC-ENG-04`/`AC-FLOW-11`/`AC-AUD-02` 明确「**迁移与 schema 已证、DB 行为用例 0 条 → S3 前未验证**」；`AC-FC-03` 拆为「单测已实测 + 运行期 503 待集成」；`AC-ENG-05` 记「四道门禁 + branch protection 未要求 status checks 的待补」；③**未新增判据、未改任何状态码**。

---

*Gate 产出（M1 验收判据层）。基线未改一字；`decisions.md` / `m1-slices.md` / `sdk-contract.md` 未改一字；本文件为项目自定口径，见文首声明。*
