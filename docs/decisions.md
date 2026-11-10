# 关津 · 文档矛盾裁定台账（decisions.md）

| 项 | 内容 |
|---|---|
| 主笔 | 关津 架构 · Ark（TWKJSV） |
| 输入 | 6 份成员基线理解笔记（**未入库**，原始记录在各自工作区；不在本仓 `notes/` 下，故不作文件引用）+ Spec 收口《接手理解摘要》第 6 段 9 类冲突 + 移交 Prompt §七 + **Spec《口径锚结论》回读（2026-11-02）** |
| 裁定授权 | spencer 2026-11-02：授权团队按「索引 §2 优先 + 端点契约优先」自行收口，只报结果 |
| 裁定权威顺序 | ① 《设计基线索引》§2「已生效的设计决定」→ ② 端点契约（接口清单）/ 数据契约（数据模型 DDL）→ ③ 工程约定（技术栈与工程约定）→ ④ 三者皆无时按 **D9** 处理 |
| 效力 | 本文是**实现口径**，用于 M1 起编码与验收；`/docs/baseline/` 一个字不改，基线原文与本文冲突处以本文为准并在此登记 |

---

## 0. 已生效前提（spencer 本轮拍板，不再列为待裁）

- **P1 迁移执行方式已闭合**：项目未上线，**不做自动迁移机制**；`migrations/` 由 `golang-migrate` 手动执行（本地 `migrate up`，发布走一次性 Job/init container），应用启动**任何情况**不得自动迁移（技术栈 §6.1）。移交 Prompt §七-1 由此关闭。
- **P2 密码产品与传输层边界已定（拆分为两件独立的事）**：①**密码产品路径 = 全链路纯软国密**（SM2/SM3/SM4 纯软件实现，`tjfoc/gmsm` / BouncyCastle），**不接密码机/密码卡**——这是已生效前提，代码按此实现。②**TLS 完全不属本项目范围**：关津**只见明文**，**不做 TLS 终结、不做国密 TLS/TLCP、不校验证书**；TLS / WAF / 负载均衡 / 灰度 / 熔断一律由**前置 nginx / Kong 等部署方组件**承担，属**部署方前置项**——**不写进代码、不进验收判据、不设配置项、不做任何预置动作**（原「国密 TLS v1 只预留不启用」的表述作废，替换为「不属于本平台范围」）。标注：TLS 边界依 spencer 口径（2026-11-02）。
- **P3 仓库与基线入库已定**：仓库用现成空仓 `spencerswagger/GuanJin`；12 份基线文档今晚随骨架一起入库。
- **P4 Go module path 已定（原 R7）**：**module path = `github.com/spencerswagger/guanjin`**（与新仓一致），**内部包前缀同此**（`github.com/spencerswagger/guanjin/internal/<pkg>`），`cmd/guanjind`、`cmd/guanjingw` 同前缀。**经 hugo 追认，2026-11-02**。R7 由此关闭（已从「残余待裁」移除）；今晚 S0/S1 的 `go.mod` 与全仓 import 前缀按此定稿，不再讨论大小写或子目录变体。
- **P5 旧仓 SpIAM 一律冻结归档、只读参考（全员红线，非仅技术裁定）**：旧仓 **SpIAM 冻结归档，只作设计参考**——**不许改、不许 `push`、不许在其内开分支**；关津的**所有代码只进 `GuanJin` 仓**（`spencerswagger/GuanJin`），一切提交、PR、CI 只发生在 `GuanJin`。**经 hugo 追认，2026-11-02**。R6 由此关闭（已从「残余待裁」移除）。**违反后果**：等于在别人的只读参考仓里写入，破坏旧仓可追溯性，直接打回。豁免条件：**无**（若确需从旧仓取实现细节，只能在 `GuanJin` 里重写，不得拷回旧仓）。
- **P6 分支纪律（经 hugo 追认，2026-11-02）：一律从 `origin/main` 开短特性分支，不开长寿命分支。** 任何工作（含文档、含别人的模块）**只允许** `git checkout -b <feat|fix>/<slug> origin/main` 这种**短命特性分支**，做完即提 PR、合入即删；**禁止**再开 `feat/phase-N-*` 这类**长寿命/按阶段命名**的分支（现存 8 个 `feat/phase-0..8-*` 即历史遗留，不得作为新开分支的范式）。理由：长寿命分支会分叉出"互不为祖先"的两条主干（PR #1 与 PR #2 都新增 `docs/sdk-contract.md`、一为 v4 一为 v7，合并必冲突且会**静默回退新版本**），分支寿命越长、冲突与回退面越大；短分支把"冲突"限制在一次 PR 内。**配套**：同一文件只允许一个 owner 分支落地（`sdk-contract.md` → Nova；`acceptance-criteria.md` → Gate；`decisions.md` / `m1-slices.md` → Ark），别人的文件**只引用、不复制**。**豁免条件：无。** 现存 8 个 phase 分支的处置（删除 / 归档）**不在本轮**，等 hugo 或 spencer 再点，团队不得自行处理。
- **P7 冻结文档纳入版本控制（经 hugo 追认，2026-11-02）：`.gitignore` 不得排除 `docs/`。** `docs/`（基线 14 个文件 + 本项目文档 + `docs/adr/`）**必须全部入库并跟踪**，否则"基线零回写"这条红线**无法自动校验**（没有版本记录就 `git diff` 不出改动）。**禁止**用 `git add -f` 单文件强塞来绕过忽略规则——那只掩盖问题、不解决问题；正确做法是改 `.gitignore`。Owl 认可这条正好补上「回写无法自动校验」的洞。
- **P8 契约变更清单已追认（经 hugo 追认，2026-11-02）：CH-1~CH-5 全部生效，后续变更走同一记录法。**
- **Q10 已裁定（**经 hugo 裁定，2026-11-04**）：Go SDK 使用独立 module** —— `sdk/go/go.mod`，module path **`github.com/spencerswagger/guanjin/sdk/go`**。理由：业务方 `import` SDK 时不该被拖进 `gin` / `gorm` / `pgx` 这类服务端依赖；主模块用 `replace` 指向本地做互操作测试（`spec/vectors` 在 SDK module 内同步一份，由 CI 校验与主模块一致）。影响面：`m1-slices.md` **S9**（Go 接入按独立 module 落地）；本条**替代**此前「Q10 仍待裁 / 未决前不新增 `sdk/go/go.mod`」的表述，旧表述已删，不留两套口径。
  ①**状态**：`CH-1`~`CH-5` 由「待追认」转为**「已追认」**，即日起按清单实现（OpenAPI / SDK 三语言 / Vue / 判据同步）；`CH-6` 是内部计数口径、无需追认。
  ②**记录法（后续所有契约变更一律照此，不得另起格式）**：在「契约变更清单」表**追加一行** `CH-<下一个可用号>`，字段固定为 **变更点 / 依据（D 条号）/ 影响面（模块 + 判据）/ 状态**；**编号只增不重排、不回收**（Gate 与 Nova 都在引编号，重排会让历史判据指错），作废的条目把状态改「已撤回」并**保留原行**。
  ③**判据侧影响（由 Gate 定账，本文不改其文件）**：追认后原先 **13 条** `PENDING·CH-n`（**CH-2** → `AC-FLOW-01`/`AC-FLOW-07`；**CH-4** → `AC-LOGIN-05`/`AC-FLOW-03`/`AC-SESS-01`/`AC-AUD-07`/`AC-ENG-08`/`AC-REG-08`/`AC-REG-09`/`AC-REG-10`；**CH-5** → `AC-SDK-01`/`AC-SDK-07`/`AC-FC-05`）**不再受"待追认"阻塞**，全部转 `PASS-able`；**Gate 已在 v11 落账：90 = 83 `PASS-able` / 0 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置（DEFERRED）：本期不做判定」**（= v10 原 70 + 追认解除的 13 条；**断言一字未改、只改状态列**；**账目数源永远是 `acceptance-criteria.md`，本文只给算式**）。**本文只登记口径与算账方式，不改 `acceptance-criteria.md` 一个字**；实际改由 Gate 在其文件按此式落地，改完两边数字必须一致。
  ④**不加动作**：追认只解除"能不能做"的问题，**不新增任何实现范围**；M1 切片（S0–S11）与判据条数一条不变。

---

## F0 不可动名单（冻结，全员引用）

> 本节只做**索引**，不改任何基线内容；条目全是已冻结事实，引用时**不得改名、不得重数、不得造别名**。与本文其他条冲突时以对应 D 条为准，但"名字/条数"以本节为准。

1. **表**：14 张基线表 + 追认新增 **`flows`**（第 15 张，见 D5）——**表名不得改名**；`migrations/` 文件名、GORM `TableName()`、判据里的表名一律照此。
2. **端点**：**具名端点 65 条 + 1 条数据面通配转发边界**；**「66 具名」这一措辞不得再引用**（见 D3-C）；Agent 面 **2 条**（`report`/`policies`），接口 §2.10 标题「3 条」是笔误。
3. **wire 名字**：四个内部签名头（`X-GuanJin-Client/Timestamp/Nonce/Signature`，D1）、内部令牌头 **`X-GuanJin-Assertion`**（D10）、内部令牌 claim 集（`iss`/`sub`/`aud`/`cid`/`uname`/`roles[]`/`acr`/`amr[]`/`iat`/`exp`/`jti`）、**错误响应四字段**（`code`/`message`/`trace_id`/可选 `details`）、幂等头 **`Idempotency-Key`**。
4. **概念名**：档位 **L1–L3**、保证级别 **AAL1/AAL2**（`AAL3` 为预留、V1 不得产出，见 D12）、**生效配置**、**薄数据面**、**三员**（系统管理员 / 安全管理员 / 审计员）。
5. **SDK 配置**：**29 键**、前缀 `guanjin.`、全小写连字符命名；清单以 `SDK契约` §3.1 实有 29 行为准，编号空洞 #7/#16/#17 **不补**（D6(1)）；环境变量名按 D6(6) 机械转换。
6. **Agent 面凭证**：`client_id` + HMAC secret 的取值格式与物理落点按 **D1 / D4-D**（`agent_hmac_secret` 是**概念名**，物理列 `secret_hash` / `secret_enc` / `prev_secret_hash`），本节不另立新名。

---

## D1 签名契约（Agent 面 HMAC canonical 串）

- **冲突**：索引 §4.2-26 `method + "\n" + path + "\n" + timestamp + "\n" + nonce + "\n" + sha256(body)` vs 接口清单 §5.3.1 `METHOD "\n" PATH "\n" sorted_query "\n" SHA256(body) "\n" timestamp "\n" nonce`（段序不同、多 `sorted_query`、用 SHA256 与「全链路国密、禁 SHA 系混用」冲突）。
- **裁定**：段序与段集合采用**接口 §5.3.1**（端点契约优先），哈希算法按**索引 §2** 把 `SHA256` 换成 **SM3**。冻结为：
  `canonical = METHOD "\n" PATH "\n" SORTED_QUERY "\n" SM3HEX(body) "\n" TIMESTAMP "\n" NONCE`，HMAC 用 **HMAC-SM3**，签名头 `X-GuanJin-Signature = Base64(HMAC-SM3(K, canonical))`。
- **补齐未定义项（Gate/Nova 回读后定稿，2026-11-02）**：`PATH` 见下 A1；`SORTED_QUERY` 见下 A2；`SM3HEX` = body 的 SM3 摘要**小写 hex 64 字符**，GET/HEAD/DELETE 等空体取 `SM3("")` 的固定值而非省略该段；时间戳窗口 **±300s**（索引 §4.2-26），重复 → `AGENT_TIMESTAMP_SKEW`；`NONCE` = **16~64 字节**的 `[A-Za-z0-9_-]`（base64url 去填充；按**字节**计长，Nova 订正），Redis 去重键 `agent:nonce:{client_id}:{nonce}`，TTL **600s**，重复 → `AGENT_NONCE_REPLAY`；secret = 交付给业务方的 base64 文本**解码后的 32 字节原始密钥**作 HMAC key（不做二次编码），平台侧取 `clients.secret_enc` 的 SM4 明文。
- **A1 `PATH` 的唯一取法（宿主有 context-path 时）**：取**客户端在请求行里实际发出的 path**（origin-form），**含全部路径前缀（含 context-path）、不含 scheme/host/port、不含 query/fragment、不做 URL 解码、不去尾斜杠、空 path 按 `/` 处理**（RFC 7230：空 path 等价 `/`）；`/a/` 与 `/a` 是**不同**的 `PATH`。实现取值：Go 用 `r.URL.EscapedPath()`（**不要**用 `r.RequestURI` 直接切、也不要 `Path` 解码后的值）；Java 用 `request.getRequestURI()`（**含** contextPath，**不得**用 `getServletPath()`——它已剥掉 context-path）；Node 用 `req.originalUrl.split('?')[0]`。**部署红线**：Agent 面链路（业务系统 → 平台）**禁止在中间件做 path 重写/前缀剥离**（nginx `rewrite`/`strip-prefix`、K8s ingress 的 `rewrite-target`）；若确有统一前缀，必须让两边看到同一段 path（要么平台侧挂在该前缀下、要么 SDK 的 `base-url` 就带上它）。理由：canonical 要求双方**逐字节**一致，而"框架 path / 容器 path / 重写后 path"三者并不总相同，只有"请求行原样"是客户端与服务器都能取到的同一事实（接口 §5.3.1 + 索引 §2 服务间认证）。
- **A2 `SORTED_QUERY` 的构造算法（含重复参数名）**：①取请求行 `?` 之后的原始串；无 `?` 或其后为空 → **空串**；②按 `&` 切分（空段丢弃），每段按**第一个** `=` 切成 name/value（无 `=` → value 为空串）；③**保留重复键**：不去重、不合并、不累加，每一对独立成项；④对 name 与 value **各自**做 RFC3986 percent-encoding（保留 `A-Za-z0-9-._~`，其余按 UTF-8 取字节后**大写**十六进制），**编码在比较之前**（先编码、再排序、再拼接，避免"解码后比较"造成的大小写/保留字符歧义）；⑤排序键 = **编码后 name 的字节序升序，name 相同再按编码后 value 的字节序升序**（两级比较，保证同名参数有确定顺序）；⑥拼接 `name=value` 以 `&` 连接；⑦**完全相同的重复对**（同名同值）因两级键相等，顺序对最终串无影响，无需额外规则。理由：`SORTED_QUERY` 的目的只是"让双方得到同一串"，同名参数若无确定顺序，两个实现会各自拼出一个合法但不同的串（接口 §5.3.1 只给了 `sorted_query` 这个名字、未定义排序，此条为补齐）。
- **依据**：索引 §2「服务间认证」+ §4.2-26（窗口/nonce 权威）；接口清单 §5.3.1（段集合，属端点契约）；索引 §2「令牌签名算法」禁 SHA 系列（替换 SHA256 的依据）。另：`logic` 层面的 `X-GuanJin-Client/Timestamp/Nonce/Signature` 四头名以接口 §5.3.1 为准。
- **受影响 / 落地**：Nova（SDK HMAC 客户端，能力 #20/#21 由此可用）与 Edge（`internal/agent/hmac.go`）**共用本文向量**；Gate 落 2 条黄金向量（空 query / 多 query 排序 / 空 body）作互操作验收；本口径写入 `internal/agent` 包注释并进 ADR-001。

## D2 `challenges.purpose` 词表

- **冲突**：数据模型 §3.4 DDL CHECK `login|bind|recover|stepup` vs 接口 §3.2.1 `login|step_up|enroll|recover|verify_identifier` vs 接口 §3.3 又用 `purpose=register`（自身三轨）vs 页面规格 `register/enroll`。
- **裁定**：冻结**唯一词表（6 值）**：`login | register | step_up | enroll | recover | verify_identifier`。作废 DDL 的 `bind`（→ `enroll`）与 `stepup`（→ `step_up`）；`register` 为独立值（注册即登录，语义不与 `login` 合并）。
- **依据**：②端点契约优先 —— 接口 §3.2.1 + §3.3 是两个**端点**的契约，覆盖数据模型 DDL 的注释性约束；`bind/recover` 复用同一链路属于编排内部实现，不影响 wire 词表。
- **补口 A′（Spec 回读 2026-11-02；只补依据强度与两条业务口径，词表不动）**：①**依据强度升级为「功能缺陷」**——若照 DDL 的 4 值实现，`POST /auth/register/start` 通过校验后要「下发**注册挑战**（`purpose=register`）落 `challenges`」（接口 §3.3.1），**第一次写就撞 CHECK**，注册链路根本跑不通；故 DDL 4 值不是"另一种风格"，是**不可实现**。②**`register` 必带 `client_id`、`user_id` 可空**：接口 §3.3.1 鉴权行「请求体**必带** `client_id`」，而此刻用户尚未创建 → `challenges.user_id` 必须允许 NULL（与 D5 的 `flows.user_id` 同语义：匿名流 NULL、完成时回填）。③**`login` 与 `step_up` 的唯一分界 = 是否已有会话**：`login` 只用于**建立**会话的授权流（匿名、凭 `flow_id`），`step_up` 只用于**已登录会话**的升级/绑定/重置（接口 §3.2.1 鉴权行「用户令牌」）；实现侧**不得**用"是否需要口令""是否已到 AAL2"之类推断二者（推断即隐性逻辑）。
- **受影响 / 落地**：本轮**不改基线 DDL**；今晚 M1 骨架的 `000006_create_challenges` 里 CHECK 按 6 值写并**显式命名** `challenges_purpose_valid`（不依赖 PG 自动命名，原因见 D4）；Core 编排层、Vue（注册/绑定页）、Gate 用例统一引用本文词表；新增词必须走 ADR。

## D3 三处口径双轨（分页 / 错误码 / 端点总数 / 失败计数）

- **冲突 A 分页**：索引 §4.2-28 定 `limit`+`cursor` vs 接口 §5.5 用 `page`/`page_size`（默认 50、上限 200，仅导出/Agent 用游标）。**裁定**：全站统一 **`limit`+`cursor`**（游标不透明、响应 `{items, next_cursor}`），`page`/`page_size` 与 `total` **作废**；`PAGE_SIZE_EXCEEDED` 保留，语义改为「`limit` 超上限 200」。依据：索引 §4.2-28 属「有默认值，按默认走」且与索引 §2 的「无状态/大表深分页」一致。
  - **A′ 分页补口（Spec 回读 2026-11-02；外部记作 `D3-A′`，Spec 锚点用此名）**：①**为何这里不按"端点契约优先"**——接口 §5.5 文末自认「⚠️ 基线未规定分页形态，**以上为本文提案**（见 §6-⑦）」，**提案不具备契约优先力**；索引 §4.2-28 是唯一显式默认值（§4.2 开头「有默认值，实现时按默认走」）→ 本条按 ①索引走。**注意与 D3-B/D3-C 结论不同但理由一致**：它们的依据是**真·端点契约**（§5.4 冻结枚举、逐面表），§5.5 只是提案，性质不同、不构成"同类不同标"。②**不返回 `total`**：响应固定 `{items, next_cursor}`，「还有更多」= **`next_cursor != null`**；**禁止为凑 `total` 做全表 `count(*)`**（大表深分页下 count 是独立全扫，与索引 §2 的取向相悖）。③**两个游标、语义不同、不得互喂**：列表游标（管理面 `limit`+`cursor`）与**导出 / Agent 增量**游标（接口 §5.5 的 `?cursor=`、Agent 的 `since_version`）是**不同的游标**，均不透明、**不得把一处的游标值投喂给另一处**；OpenAPI 里分别声明（导出与 Agent 面各自独立 schema），实现侧**不得复用同一个游标编解码器**（复用就会出现"能解但语义错"的静默错误）。
- **冲突 B 错误码**：数据模型 §4.1 的 `IAM_IDENT_LAST_*` 自称「待定稿」 vs 接口 §5.4 已冻结 `LAST_IDENTIFIER_CANNOT_UNBIND` / `LAST_AUTHENTICATOR_CANNOT_REMOVE` 等。**裁定**：**一律用接口 §5.4**，`IAM_*` 全部作废；I6（最后一个主标识符不可降级）**不设对外错误码**——接口清单 §3.4 与页面规格均无「设主标识符」端点/V1 不暴露该操作，故 I6 只在服务层内部校验；将来若暴露，按 §5.4 风格追加 E4 码并走 ADR。依据：②端点契约优先 + 事实核对（接口 §3.4 只有 4 条标识符端点，无 primary 操作）。
  - 逐条映射（Spec 复核）：**I1/I2 → `LAST_IDENTIFIER_CANNOT_UNBIND`**（E4/409）、**I3/I4 → `LAST_AUTHENTICATOR_CANNOT_REMOVE`**（E4/409）、**I8 → `TIER_DIRECTION_VIOLATION`**（E7/422）、**I6 无对外码**（不暴露该操作，仅服务层校验）。
  - **B′ 错误码补口（Spec 逐行扫出、我复核属实，2026-11-02；外部记作 `D3-B′`）**：①**枚举漏登记 3 码**——`REGISTRATION_NOT_ALLOWED`、`INVITE_CODE_INVALID`、`WEAK_PASSWORD` 在**端点行里在用**（接口 §3.3.1 / §3.3.2 / §3.5.1），但 §5.4「完整枚举」里没有（E1、E4 组均缺）。按**端点行为准**补登记：`INVITE_CODE_INVALID` → **E1 / HTTP 400**（请求参数不成立）；`WEAK_PASSWORD` → **E4 / HTTP 422**（口令不满足强度，属资源不变式）；`REGISTRATION_NOT_ALLOWED` → **E4 / HTTP 403**——**说明**：E4 前缀默认区间是 404/409/422，本条按 §5.4 首句「HTTP 状态由分级前缀决定，**接口可覆盖**」显式覆盖为 403，语义是"该 client 的注册准入未开放"；**不得**改挂 E3（档位/角色不足）或 E7（管理面三员/策略），那两个前缀管的是别的事。三码随 **CH-4**（**P8 已追认，2026-11-02**）；判据归 Gate 定账，我不改其文件。②**口令三码互斥 + 顺序固定**：同一次口令校验**最多返回一个 `code`**，判定顺序 **`WEAK_PASSWORD`（强度 / 弱口令黑名单）→ `PASSWORD_REUSED`（历史复用）→ `PASSWORD_POLICY_VIOLATION`（该 client 档位策略）**，命中即停；**全量命中**放 `details.violations[]`（元素 `{code, hint?}`，顺序同上），供页面就地高亮"未满足哪几条"（页面规格 C2/C5 要求指明未满足条件）。**禁止**同一次错误返回多个平级 `code`——那正是本节要消灭的"同故障双码"。
  - **B″ 错误码补登记表（§5.4 之外补入的全部码，一处集中；下版 OpenAPI/契约直接照此表落盘；外部记作 `D3-B″`）**：

| 码 | E 组 | HTTP | 出现端点 | 触发条件 | 判据（Gate 定账） | 依据 |
|---|:--:|:--:|---|---|---|---|
| `INVITE_CODE_INVALID` | E1 | 400 | `POST /auth/register/start` | `invite_code` 缺失 / 无效 / 已耗尽（准入=邀请制时） | `AC-REG-08`（`PENDING·CH-4`） | D3-B′① |
| `REGISTRATION_NOT_ALLOWED` | E4 | **403**（按 §5.4「接口可覆盖」） | `POST /auth/register/start`、`POST /auth/register/complete` | 目标 client 准入 = 邀请制 / 实名前置 / 关闭自注册 | `AC-REG-09`（`PENDING·CH-4`） | D3-B′① |
| `WEAK_PASSWORD` | E4 | 422 | `POST /auth/register/complete`、`POST /api/v1/me/password` | 强度 / 弱口令黑名单不通过（**三码中第 1 顺位**） | `AC-REG-10`（`PENDING·CH-4`） | D3-B′①② |
| `PASSWORD_REUSED` | E4 | 422 | 同上 | 与历史口令重复（**第 2 顺位**，仅当 `WEAK_PASSWORD` 未命中） | `AC-REG-10` | D3-B′② |
| `PASSWORD_POLICY_VIOLATION` | E4 | 422 | 同上 | 该 client 档位策略不满足（**第 3 顺位**） | `AC-REG-10` | D3-B′② |
| `STEP_ALREADY_SATISFIED` | E4 | 409 | `POST /auth/challenge/{factor}` | 该 factor 已在 `flows.completed_steps` 且 `status=done`（**只许 `internal/flow` 返回**） | `AC-FLOW-03`（`PENDING·CH-4`） | D3-F |

  > 前 3 行是 **§5.4 完整枚举里没有、但端点行在用**的码（Spec 逐行扫出，我复核属实）；后 3 行是 §5.4 **已存在**、本条只补**互斥与顺序**。全部随 **CH-4（现状"四处"，Gate 侧记为 CH-4④⑤）**——**P8 已追认（2026-11-02）**，按端点行实现、**不得**另造别名码。
- **冲突 C 端点总数**：索引 §1.3 写「66 个具名端点」/ 接口 §2 首尾写 66 vs 接口文末统计逐面相加 = **65**；且 §2.10 标题「3 条」而表内只有 2 行。**裁定**：**具名端点 = 65**（10+2+2+4+3+2+3+35+2+2），"66" 是「65 具名 + 1 条数据面通配转发边界」被重复计入具名；**Agent 面 = 2 条**（`report`/`policies`），§2.10 标题「3 条」为笔误。依据：②端点契约（逐面表）优先，且文末统计自证 65；索引 §2 未规定端点总数。
- **冲突 D 失败计数权威源**：索引 §4.2-2（Redis 实时权威 + `authenticators.fail_count/locked_until` 为镜像）vs 接口 §3.2.2（「权威状态落 `challenges` 表，Redis 仅限流计数」）。**裁定**：**账号级失败计数/锁定 = Redis 权威**（原子 INCR + fail-closed，按 `user_id` 全局共享，`authenticators` 仅持久化镜像）；`challenges.attempts` 是**单次挑战的尝试次数**（另一个对象，非账号级计数），二者不冲突。**键名固定（Gate 回读 2026-11-02 补）**：账号级失败计数键 = **`login:fail:{user_id}`**（`user_id` 用 uuid 小写带连字符形式）、锁定标记键 = **`login:lock:{user_id}`**；两者 TTL 分别取 `login_policies.lock_duration_min` / `fail_threshold` 窗口，**键名不得由实现自行改写**（`AC-LOGIN-03` 的多副本用例直接按此键断言；改键须同步改判据与本文）。依据：①索引 §4.2-2 为已生效默认 + 索引 §2「挑战权威落库」指的是挑战本身。
- **裁定 E（收口 Gate §15-G1 之外的「同场景双码」，Gate 记作 D3-E）**：接口 §5.4 里同场景并列两码的，**按语义分工、不二选一**：①PKCE —— 缺/畸形 `code_verifier` → `INVALID_CODE_VERIFIER`；格式合法但与 `code_challenge` 不匹配 → `PKCE_VERIFICATION_FAILED`（均 E2/400~401）。②撤销 —— 令牌被 `POST /oauth/revoke` 显式撤销后再次使用 → **401 `TOKEN_REVOKED`**；会话被撤销（登出/管理面踢下线/refresh 复用）后访问 → **403 `SESSION_REVOKED`**（前者是凭证状态、后者是准入状态，HTTP 码不同）。③三员 —— 调用超出本员职责域的写操作 → `THREE_POWER_VIOLATION`（E7/403）；单纯没有该角色 → `INSUFFICIENT_ROLE`（E3/403）。验收侧对应 `AC-LOGIN-05`、`AC-SESS-01`、`AC-AUD-07`、`AC-FLOW-03` 已按此回填。
- **裁定 F（收口 Gate §15-G2：`done` 步骤被重输的拒绝码）**：**分配新码 `STEP_ALREADY_SATISFIED`**（E4 资源与不变式 / HTTP **409**），仅由 `internal/flow` 在「该 factor 在 `flows.completed_steps` 中已 `done` 时再次 `POST /auth/challenge/{factor}`」返回。**不借用** `CHALLENGE_CONSUMED`（语义是「某个 `challenge_id` 已消费」，会让客户端去换 `challenge_id`，而实际该步骤无需再挑战）与 `ACR_INSUFFICIENT`（语义是「档位不足需补步」，用在"无需补步"时会诱发补步死循环）。登记方式：接口 §5.4 是冻结表且基线只读，本码作为**E4 的追加项**记在此处，下一版契约/OpenAPI 落盘时并入；CI 增一条禁码扫描（`STEP_ALREADY_SATISFIED` 只允许出现在 `internal/flow`），与 `check-no-automigrate.sh` 同批落地。
- **跨模块改动边界（S4a 引入，2026-11-04，团队收口）**：①**`crypto.HMACSM3WithUserSecret` 只许用于 TOTP 的用户侧密钥**（per-user secret，来自 `authenticators.credential_data`）；**平台侧密钥一律只能走 `crypto.HMACKey`**（主密钥 HKDF 派生，D4-B），**禁止**用它做审计链、盲索引、网关断言或任何平台级 HMAC——调用点限 `internal/credential`，其它包 import 即评审打回。②**`httpx` 新增 4 码属 `D3-B′`/D3 表既有口径，已核对一致**：`INVALID_CREDENTIAL`（E2/401）、`TEMPORAL_CODE_REUSED`（E2/401）、`WEAK_PASSWORD`（E4/422）、`PASSWORD_REUSED`（E4/422）；口令三码互斥与固定顺序（`WEAK_PASSWORD` → `PASSWORD_REUSED` → `PASSWORD_POLICY_VIOLATION`）+ `details.violations[]` 与 D3-B′② 一致。③**前向约束**：`REGISTRATION_NOT_ALLOWED` 落码时必须显式映射 **403**（E4 按「接口可覆盖」，**不得**落到 default 500），`INVITE_CODE_INVALID` → 400，待 S4b 落地时核对。
- **受影响 / 落地**：接口清单 §5.5/§5.7 的示例响应由 Spec 在下版口径文档对齐（不改基线则写入 OpenAPI/契约文件）；Core（分页/风控计数/`STEP_ALREADY_SATISFIED`）、Edge（限流键）、Vue（列表组件改用 cursor）、Gate（用例预期）统一按本条；Core 需在 `challenges` 与 `risk` 之间显式区分两套计数（`attempts` vs `fail_count`）。

## D4 存储 / 密钥 / 审计链（4 处 DDL 级冲突）

- **冲突 A UUID 默认值**：数据模型 §1.1「全库统一：主键默认值写作 `uuidv7()`」vs 技术栈 §5.0/§6.2「migration 不写 `DEFAULT uuidv7()`」。**裁定**：**一律不写 DB 默认值**，UUID v7 由应用层 `uuid.NewV7()` 生成（GORM `BeforeCreate`），migration 主键只写 `uuid PRIMARY KEY`。依据：①索引 §4.2-13 明确「应用层生成 UUID v7」+ 本机 PG17 亦无 `uuidv7()`（PG18 才有）。
- **冲突 B 主密钥用法**：数据模型 §3.7 `HMAC-SM3(主密钥, …)` 与技术栈 §5.6 示例代码 `crypto.AuditTag(r.masterKey, …)` **直接使用主密钥** vs 技术栈 §7.3 强制 **HKDF-SM3 派生子密钥**（"不要把主密钥直接用在不同算法上"）。**裁定**：**统一走 HKDF-SM3 派生**，主密钥只做派生输入、不进任何业务算法。冻结用途隔离（`info = salt` domain separator）：`guanjin/sm4/field`→16B（标识符/私钥落库）、`guanjin/sm4/keywrap`→16B、`guanjin/hmac/audit`→32B（审计链）、`guanjin/hmac/identifier`→32B（盲索引）、`guanjin/hmac/agent`→32B（如平台侧需验 HMAC 干线）。依据：③技术栈 §7.3 为专章且与「一密钥一用途」一致；纯软路径（P2）下密钥全在进程内存，用途隔离是唯一可做的止血。**（本条是当时 5 个用途域的原始冻结口径；后经 D14② +1、ADR-005 +2，当前累计 5 → 8，全量清单与生效范围以 D15③ 为准。）**
- **冲突 C 审计表分区与链**：索引 §4.2-12「按月分区 + `DROP PARTITION` 整段清理（365 天）」vs 数据模型 §3.7 DDL **无分区**、`PRIMARY KEY (id)`、`prev_tag` 首条为 NULL 且**无锚点、无整链校验 Job**，但页面 A11 要求展示「完整性校验状态」。**裁定**：(1) `audit_logs` 按 `occurred_at` **RANGE 月分区**，主键改 `(id, occurred_at)`（分区表主键必须含分区键）；`forbid_mutation` 触发器挂父表（PG≥11 自动下发各分区）。(2) 链**不因 DROP 而悬空**：只允许删除「已归档且已写锚」的最老分区；分区切换与每日定时任务把该段末条 `integrity_tag` 写入既有 `runtime_config`（键 `audit.chain.anchor.<YYYYMM>`，base64），删除分区时把被删段首尾 tag 另存为断点锚（键 `audit.chain.segment.<YYYYMM>`）；跨分区首条的 `prev_tag` 取上一分区末 tag（或断点锚），链保持连续。(3) 「完整性校验状态」= 分区内逐条复算 + 与 `runtime_config` 锚比对，枚举 `verified | broken | unverified(锚缺失)`；校验 Job 落在 `cmd/guanjind` 内定时任务（不新增二进制），Prometheus 暴露 `audit_chain_broken_total`、`audit_anchor_age_seconds`。**(4) `trace_id` 取证口径（Gate 回读 2026-11-02 补）**：`flows.trace_id` **只记创建该流那一次请求的 `trace_id`，补步时不覆盖**；因此同一次登录里 `flows.trace_id` 与后续 `auth_events.trace_id` **本来就会不同**——取证时按"`flows.trace_id` 定位这次登录的入口请求、按各 `auth_events.trace_id` 定位各步动作"，**两者不一致不得判为缺陷**（否则会把正常的补步链路误报成 trace 断链）。若需"一次登录一条链"，用 `flows.id`（`flow_id`）作为关联键，而不是要求 `trace_id` 全链相同。依据：①索引 §4.2-12 为已生效默认；③技术栈 §5.6 单链 + 咨询锁保留；D5 的 `trace_id` 列定义（只写创建那条）。**(5) 链序口径（Spec 裁定 2026-11-10 补，依 (4) 的写法登记，不是判据变更）**：**链序 = `created_at`（入链序，平台生成）；`occurred_at` = 调用方事件时间，原样保留并参与 `integrity_tag`；二者不同不算缺陷。** 事实依据：`migrations/000011` 里 `occurred_at` 是**分区键**且与 `id` 组成主键 `(id, occurred_at)`（`:26`/`:29`），四个取证索引全按 `occurred_at DESC` 排序（`:32~:35`），且 `audit_logs_append_only` 触发器（`:38~:40`）令写入后**永不可改**——所以「为单调而抬 `occurred_at`」= **永久改错取证时间且无法补记原值**；可安全抬升的是平台自己的 `created_at`（同表已有，零 schema 变更）。另加**下界守卫**：`occurred_at` 早于链尾所在分区的月初 → **拒绝并告警**（否则今天的写入会插进上月分区，破掉 (2) 的跨分区 `prev_tag` 链接 / `AC-AUD-06`）。用例要求：同一条事件改 `occurred_at` 两值 → `integrity_tag` **必须不同**（证明它真进了哈希）。完整裁定见 `notes/audit-chain-order-2026-11-10.md`。
- **冲突 D `clients` 缺列 + 一条不可执行 CHECK**：接口/页面引用的 `agent_hmac_secret`、`is_confidential` 在 `clients` 无同名列（实有 `secret_hash` / `secret_enc` / `prev_secret_hash`）。**裁定**：`agent_hmac_secret` 是**概念名**，物理落点即 `secret_hash`（HMAC-SM3 盲索引，验签用）+ `secret_enc`（SM4，还原/重发用）+ 轮换期 `prev_secret_hash`，**不新增列**；`is_confidential` **删除引用**——它与索引 §2「平台不做密级」冲突，受信 introspect 改为「`clients.status='active'` + `runtime_config` 键 `introspect.trusted_clients` 白名单」。附带：数据模型 §3.8 的 `CONSTRAINT clients_redirect_unique CHECK (redirect_uris = (SELECT array_agg(DISTINCT x) FROM unnest(...)))` **PG 禁止 CHECK 内子查询**，照抄会让 `000002` 直接失败 → 迁移里**不写**该 CHECK，改由 `internal/admin` 去重校验 + 单测覆盖（实现细节，不需 spencer 裁决）。
- **补口 D4-D₂（Edge 复核 2026-11-02 提出、Ark 裁定）：23505/23514 → 对外错误码的完整映射表（8 处）。**
  背景：D5 只说 `flows_one_active_per_user_client_uniq` 撞 23505 → **409**，没给**码名**；`clients_pkey` / `signing_keys_kid_uniq` / `login_policies_*_uidx` 连 HTTP 码都没定。Edge 的做法是**只用接口 §5.4 已有码、不自造**（技术栈里出现的 `KEY_KID_EXISTS` / `REFERENCE_NOT_FOUND` **不在 §5.4**，不得使用）——本条**认这个原则**，并补两处他漏的（`role_bindings_uniq`、`policy_snapshots_uniq` 在 §5.4 里**本来就有**专用码，不该退化成 `INVALID_PARAMETER`）：

  | # | 约束（显式名） | SQLSTATE | 码 | 组 / HTTP | 客户端该怎么反应 |
  |---|---|---|---|---|---|
  | 1 | `flows_one_active_per_user_client_uniq` | 23505 | **`COOLDOWN_ACTIVE`** | E4 / **409** | **用同一条 `flow_id` 继续**，不要再开新流（见下「前置条件」） |
  | 2 | `clients_pkey` | 23505 | **`INVALID_CLIENT`** | E1 / **400** | 换一个 `client_id`（message 必须写明「已存在」） |
  | 3 | `signing_keys_kid_uniq` | 23505 | **`INVALID_PARAMETER`** | E1 / **400** | 换 `kid` |
  | 4 | `login_policies_tier_default_uidx` / `login_policies_client_override_uidx` | 23505 | **`INVALID_PARAMETER`** | E1 / **400** | 该档位 / 该 client 的策略已存在，改走编辑 |
  | 5 | `identifiers_one_primary_per_user` | 23505 | **不映射** | — | D3-B：I6 **无对外码**（V1 不暴露「设主标识符」）；服务层必须先清旧 primary，撞索引即缺陷 |
  | 6 | `user_visited_clients_uniq` | 23505 | **不映射** | — | 数据面按 upsert 写（`ON CONFLICT DO UPDATE`），撞索引即缺陷 |
  | 7 | `role_bindings_uniq` | 23505 | **`BINDING_ALREADY_EXISTS`** | E7 / **409** | 该用户已有此角色（§5.4 E7 已有专用码，**不得**退化成 `INVALID_PARAMETER`） |
  | 8 | `policy_snapshots_uniq` | 23505 | **`POLICY_SNAPSHOT_CONFLICT`** | E4 / **409** | `(client_id, version)` 已存在（§5.4 已有专用码） |

  **第 1 条的前置条件（否则会把客户端带错方向）**：正常路径**必须先查后建** —— 存在该 `(user_id, client_id)` 的 `active` 流时**直接返回那条流（200 + 同一个 `flow_id`）**，不是报错；23505 只是**并发竞态的最后防线**，此时返回 `COOLDOWN_ACTIVE`(409) 且 `message` 必须写「已有进行中的流程，请用同一 `flow_id` 继续」，**不得**只写 cooldown 让人以为要等。
  **依据**：②端点契约优先（只用 §5.4 既有码，禁自造）+ ①索引 §2「不许有隐性逻辑」（「撞了索引怎么办」必须逐条写死，不能留实现去猜）+ D3-B（I6 无对外码）。
  **受影响 / 落地**：**Edge** 在 `internal/store/pgxerr.go` 按本表**八行逐条**落（`SQLSTATE × 约束名` 双键，禁止「23505 一律 409 / 23514 一律 400」）；**Core** 的 flow 创建按第 1 条「先查后建」；**Gate** 的 `AC-ENG-04` 逐行举证（本表即判据期望值）；新增/变更对外码一律走 **P8②** 记录法。

- **受影响 / 落地**：Ark 的 14 张 migrations 全部按本条落地（含 `(id, occurred_at)` 主键与分区）；Edge 的 `crypto/masterkey.go` 与 `audit/chain.go` 改为**只接受派生键**（不接受 `masterKey` 入参，编译期挡住回退）；Core 的盲索引/字段加密同样只取派生键；`runtime_config` 新增的锚点键由管理面只读展示。

## D5 阶梯中间态（flow）的权威落点 —— 新增第 15 张表 `flows`

- **冲突**：14 张表**无 `flows` 表**，而基线要求多副本无状态 + Redis 只承载限流计数与热缓存，但 Flow Descriptor 需要持久承载 `flow_id` / 已达成 `amr` / `target_acr` / `expires_at` / 已消费步骤。
- **裁定**：**新增第 15 张表 `flows`**（`migrations/000015_create_flows`），作为阶梯认证中间态的唯一权威落点。**经 hugo 追认，2026-11-02**。R1 由此关闭（已从「残余待裁」移除）；本轮只定**表结构规格**，`.sql` 今晚建骨架时随 P1–P3 一并落盘，**不进 `migrations/`、不写代码**。
- **批准理由（原文照录）**：多副本无状态 + Redis 只许承载限流，中间态无处存，硬扛会逼出隐性回退。
- **表结构规格（13 列；字段 / 类型 / 可空 / 默认值口径）**：

| 列 | 类型 | 可空 | 默认值口径 | 说明 |
|---|:--|:--:|:--|:--|
| `id` | `uuid` | NOT NULL | **无 DB 默认值**（应用层 UUID v7，见 D4-A） | 即对外 `flow_id`，不透明凭证，只在 TLS 内传递 |
| `user_id` | `uuid` | NULL | — | 注册/登录前匿名流为 NULL，完成时回填；FK `users(id) ON DELETE SET NULL`（流是编排临态，不得因删用户而阻塞） |
| `session_id` | `uuid` | NULL | — | **归属会话**：登录成功后回填；阶梯升级流必有。FK `sessions(id) ON DELETE SET NULL` |
| `client_id` | `text` | NOT NULL | — | 必带业务系统上下文（索引 §2「注册必带业务系统上下文」）；FK `clients(client_id) ON DELETE RESTRICT` |
| `target_acr` | `text` | NOT NULL | `'AAL1'` | `CHECK (target_acr IN ('AAL1','AAL2','AAL3'))`，与 `sessions.acr` 同词表；`AAL0` 仅前端展示「未认证」，**不落库** |
| `achieved_amr` | `text[]` | NOT NULL | `'{}'` | 已达成因子集合，元素取 D2/附录 B 的 `amr`；**单调不减**（I-F2） |
| `completed_steps` | `jsonb` | NOT NULL | `'[]'::jsonb` | `[{factor, challenge_id, at}]`，**只追加不改写**；`challenge_id` 为逻辑引用（见下） |
| `status` | `text` | NOT NULL | `'active'` | `CHECK (status IN ('active','completed','expired','abandoned'))` |
| `expires_at` | `timestamptz` | NOT NULL | 由 `runtime_config` 键 `flow.ttl_seconds` 提供，**V1 初值 600s**（显式登记，禁隐性） | 流级 TTL；判定以 **DB 时间**为准 |
| `consumed_at` | `timestamptz` | NULL | — | 一次性消费标记；与 `status='completed'` 一致（I-F4） |
| `trace_id` | `text` | NULL | — | **第 13 列（Edge/Core 对齐新增）**：只写入**创建该流的那一次请求**的 `trace_id`，后续补步**不覆盖**；**不参与任何查询谓词与唯一约束**，仅用于取证串联（与 `auth_events.trace_id` 对查） |
| `created_at` | `timestamptz` | NOT NULL | `now()` | — |
| `updated_at` | `timestamptz` | NOT NULL | `now()` | 由 `set_updated_at()` 触发器维护 |

- **索引（名字全部显式，不依赖 PG 自动命名，理由见 D4）**：主键 `flows_pkey (id)`；`flows_one_active_per_user_client_uniq (user_id, client_id) WHERE status='active' AND user_id IS NOT NULL`（同一用户对同一业务系统至多一条活跃流，匿名流不受限）；`flows_session_idx (session_id) WHERE session_id IS NOT NULL`（会话撤销时批量作废）；`flows_expires_idx (expires_at) WHERE status='active'`（TTL 清理器只扫活跃段）；`flows_client_created_idx (client_id, created_at DESC)`（按业务系统排查/取证）；**`flows_expired_cleanup_idx (updated_at) WHERE status = 'expired'`**（第二阶段物理删除 `status='expired' AND updated_at < cutoff`，由 **`000016`** 引入，见下条「第二段清理索引裁定」）。**注意：带 `WHERE` 的部分唯一索引只能用 `CREATE UNIQUE INDEX`，不得写成 `CONSTRAINT … UNIQUE`**（PG 不支持）——这是 Edge 的 DDL 复核项之一。
- **不变式**：**I-F1 归属**——`status='completed'` 必须 `user_id NOT NULL`（DB CHECK 可表达）；`status='active'` 必须 `client_id NOT NULL`。**I-F2 amr 单调不可回退**——`achieved_amr` 只增不减（服务层事务内校验 + 二期可选 DB 触发器比对 `OLD ⊆ NEW`），任何"补步后 amr 变短"的写入直接拒绝。**I-F3 过期语义**——`expires_at <= now()` 即过期，惰性置 `status='expired'`，过期流**不得**再签发任何令牌（返回 `FLOW_EXPIRED`）。**I-F4 一次性**——`completed` 后 `consumed_at NOT NULL`（可加单行 `CHECK ((status='completed') = (consumed_at IS NOT NULL))`）；已完成流二次提交 → `FLOW_NOT_FOUND`，已过期 → `FLOW_EXPIRED`。**I-F5 并发不丢 amr**——并发补步只允许串行推进（`SELECT … FOR UPDATE` 锁该 flow 行），多副本下不得出现两条 flow 各自累加 amr。**I-F6 清理（改为两段式，Core 对齐结论）**——流不软删（无 `deleted_at`）、不入审计哈希链，但**"过期"不等于"删除"**：①第一段只置 `status='expired'`（行保留，`FLOW_EXPIRED` 才可能稳定返回）；②**保留 7 天**后第二段才 `DELETE`；只删 `status='expired' AND updated_at < now()-7d` 的行——**两段各走各的索引**：第一段（置 expired）走 `flows_expires_idx (expires_at) WHERE status='active'`，第二段（物理删）走 `flows_expired_cleanup_idx (updated_at) WHERE status='expired'`（`000016` 引入）。**这条改变是为了消除 `AC-FLOW-10` 的 flaky**：若"过期即 DELETE"，行消失后只能返回 `FLOW_NOT_FOUND`，`FLOW_EXPIRED` 会时隐时现。**I-F7 已完成后的一次性回填例外**——`status='completed'` 的行**允许一次** `session_id: NULL → 非 NULL` 的回填（S5 编排完成、S6 才建会话的交接期必须允许），回填后**不得再改**；除此以外 `flows` 的 `session_id` 只允许"NULL→值"一次，`user_id` 同理（服务层事务内校验，不加 DB 触发器以免正常交接被拦）。**I-F8 锁序与锁内禁令**——全站加锁顺序统一 **`sessions → flows`**（禁止反向）；**`sessions` 行一律不做 `FOR UPDATE`**（会话状态用条件更新/版本列保证，避免长事务占住热表）；**`flows` 行锁内禁做任何重活**——PBKDF2-SM3（≥10 万迭代、百毫秒级）、短信/邮件投递、外部 HTTP、审计导出等**必须在锁外完成**，锁内只允许：读该行、校验 `status`/`expires_at`/`achieved_amr`、`challenges` 的一次性消费更新（同一事务）、写回 `achieved_amr`/`completed_steps`/`status`。**例外条件：无**——若某步确需长耗时计算，必须先算后锁（"预计算 → 短事务"），这是硬约束不是建议。
- **与 `challenges` 的职责边界与引用关系**：`challenges` = **单步挑战**（发给谁、应答摘要、`attempts`、一次性消费、TTL）；`flows` = **流级进度**（目标档位、已达成 amr、已完成步骤序列），**不存任何应答材料**。引用方向**只有一条**：`flows.completed_steps[].challenge_id → challenges.id`，且为**逻辑引用、不加 FK**（挑战会被 TTL 清理与分区归档，加 FK 会锁死清理路径；完整性由服务层校验）；`challenges` **不反向加 `flow_id` 列、也不做反查**（Core 对齐结论）——阶梯内的流归属由**请求显式携带的 `flow_id`** 表达，见下文「契约变更清单」CH-1。理由：匿名流的 `user_id`/`session_id` **双 NULL**，无法用「session + factor」反查定位；请求显式带 `flow_id` 是唯一可举证的解。
- **第二段清理索引裁定（Edge 证据挖掘，2026-11-04，团队收口）**：Edge 用 `EXPLAIN` 证到第一段走 `flows_expires_idx`，但**第二段的谓词 `status='expired' AND updated_at < cutoff` 现有 4 条具名索引都不覆盖** → 分区表上会退化成全表扫。**裁定**：①**新增部分索引** `flows_expired_cleanup_idx ON flows (updated_at) WHERE status = 'expired'`；**不改造既有 4 条**——`flows_one_active_per_user_client_uniq`（唯一活跃流）/`flows_session_idx`（会话撤销批量作废）/`flows_expires_idx`（第一段扫描，列是 `expires_at` 且行集 `status='active'`，与第二段**谓词与列都不同**）/`flows_client_created_idx`（client+created 排查）各自服务一个查询，合并只会让两个谓词都变差。**索引清单最终为 5 条**（上一条已给全：4 条具名 + 本条的 `flows_expired_cleanup_idx`）。②**迁移编号 = 新增 `000016_create_flows_expired_cleanup_idx`（up/down 成对，down 里 `DROP INDEX`），不改 `000015`**——理由：`golang-migrate` 只记版本号、**不校验 checksum**，改已被 `up` 过的迁移会让**已迁移的库静默缺索引**（本地三库与 CI 缓存库都会中招）；技术栈 §6.2 亦定「序号全局单调递增、不复用」。③**实现归属 = Edge**：出 `000016` + 把 `integration` 里的 **`flows_named_indexes` 断言由 4 改 5**（`.github/workflows/ci.yml` 的 psql 断言与 Go 侧集成断言都要改，否则 CI 会红）+ 补一条「第二段 `DELETE … EXPLAIN` 命中 `flows_expired_cleanup_idx`」的用例。**Ark 只改台账，不代写迁移、不改脚本。**
- **`flow.ttl_seconds` 的落地口径（Edge/Core 对齐结论）**：`000015` 迁移内 **seed 一行** `runtime_config('flow.ttl_seconds','600')`，必须带 **`ON CONFLICT DO NOTHING`**（不得覆盖运维已改过的值）；**运行时键缺失 = 启动 fail-fast**（`config/runtime.go` 启动校验，与 P1 的 fail-fast 风格一致）；**代码里不得出现 600 常量**（禁隐性默认值），测试用 `runtime_config` 显式注入。
- **依据**：索引 §2「多副本 + 无状态化；挑战落库、Redis 只承载限流计数与热缓存」；④「不许有隐性逻辑」——复用 `challenges` 冒充 flow 会引入无表可查的隐式语义，比新增一张表更危险；`AAL0` 不落库的依据是索引 §4.2-15（档位 → ACR 只有 AAL1/AAL2）。
- **补口 D5-₂（Edge 提问、Ark 裁定 2026-11-02）：`runtime_config` 启动校验的归属与失败语义。**
  **归属：实现挂 S10**（`/readyz` 与 fail-closed 故障注入都在 S10）；**S3 只交付函数 + 单测、不塞进 `cmd/guanjind`** —— Edge 的判断正确，本条追认。
  但**口径现在定死，不留到实现时再猜**（三种情况必须分开，否则「DB 抖动」会变成「全线重启风暴」）：

  | 情况 | 处置 | 理由 |
  |---|---|---|
  | **A. 环境变量缺失 / 格式错 / 主密钥长度不足** | **启动 fail-fast，非 0 退出** | 进程内可判定，且错了也起不来（已由 `internal/config` 实现） |
  | **B. DB 连不上（网络 / 认证 / 超时）** | **不退出**：`/livez` 200、`/readyz` **503 `DB_UNAVAILABLE`**、业务端点一律拒（fail-closed） | DB 是外部依赖，不可达 ≠ 配置缺失；启动即退出会把 DB 抖动放大成集群重启风暴，还绕过编排器重试 |
  | **C. DB 可达但 `runtime_config` 缺键**（如 `flow.ttl_seconds`） | **启动 fail-fast，非 0 退出**（D5 原文），日志必须打印**缺失键名** | 这是**真配置缺失**（seed 漏了），与 B 是两件事；放行会逼实现用隐性默认值 |

  **依据**：D5「`flow.ttl_seconds` 键缺失 = 启动 fail-fast」+ 项目硬约束 3（fail-closed）+ 索引 §2「不许有隐性逻辑」（把 B/C 混为一谈就是隐性逻辑）。
  **受影响 / 落地**：**S10** 落 `/readyz` 与三类分支 + 判据（`AC-FC-01/02`）；**Edge** 把 `runtime_config` 校验函数做成**可区分返回**（A/B/C 三态，不要只回一个 `error`），便于 S10 接线；**Gate** 按 A/B/C 各出用例（B 态尤其要验「进程活着但业务全拒」）。

- **落地方案**：**Ark** 出 `migrations/000015_create_flows.up/.down.sql`（无 `DEFAULT uuidv7()`、`expires_at` **禁 DB 默认值**、约束/索引显式命名、部分唯一索引走 `CREATE UNIQUE INDEX`、`down` 可回滚）+ seed `flow.ttl_seconds`。**Edge** 在 `internal/store/model/flows.go`（GORM 映射 13 列 + `TableName()`，无 `DeletedAt`）、`internal/store/repo_flows.go`（读写、惰性过期、两段式清理器、**必须暴露"带 tx 的锁读"接口**：`LockByID(tx, id) (*Flow, error)` 内部 `FOR UPDATE`，供 Core 在同一事务内"锁 → 校验 → 消费 challenge → 累加 amr → 写回"）与 `internal/store/pgxerr.go` 映射表扩展（按 **SQLSTATE + 约束名**双键：`flows_one_active_per_user_client_uniq` 的 23505 → 业务码 **409**，I-F1 的 23514 → **400**，不得让 23505 一律误报 409 / 23514 一律误报 400）落地；DDL 由 Edge 复核（复核清单 6 条，已知 3 条：部分唯一索引不得写成 CONSTRAINT、`expires_at` 禁 DB 默认、I-F4 可加单行 CHECK；其余 3 条按 Edge 笔记逐条签署，本文不代述以免失真）。**Core** 在 `internal/flow/`（`state.go` 推进/消费、`descriptor.go` 档位映射、amr 单调校验、`completed_steps` 追加）与 `internal/session/manager.go`（登录成功**一次性回填** `session_id`，见 I-F7；`Revoke` → 通过**窄接口 `AbandonBySession`** 把该会话 `active` 流置 `abandoned`，避免 `session ↔ flow` 包循环）落地。**Gate** 用 flows 行举证 `AC-FLOW-01/03/07/10/11`（M2 的 `AC-FLOW-08`）。**Vue** 用 `flow_id` 续流，不感知本表。

## D6 SDK 配置项（29 vs 32 / mode / jwks-uri / 暗开关）

- **冲突**：契约 §3.1 编号缺 #7/#16/#17、实有 **29** 行 vs 全项目口径「**32** 项」；`guanjin.mode: gateway|direct` 存在于 §4.1 示例且不在清单内，与索引 §2「只保留网关形态」冲突；`jwks-uri` 默认 `${base-url}/api/v1/jwks` 指向不存在的端点（真值见索引 §4.2-23 `/.well-known/jwks.json`）；`guanjin.enabled` 默认 `true` 且「生产置 false 应被视为误配并告警」，实为可跳过验签的暗开关。
- **裁定**：(1) **以 §3.1 实有 29 项为交付集**，"32 项" 作废；缺号 #7/#16/#17 视为**编号空洞（文档笔误）**，**不得新增 key 补齐编号**（禁止编造契约），Nova 侧连续编号 1..29 并在 README 标注此差异（ADR-003）。(2) **删除 `guanjin.mode`**，SDK 无模式开关，只有一个形态（流量必过数据面）。(3) `jwks-uri` 默认改为 `${guanjin.platform.base-url}/.well-known/jwks.json`（base-url 不带尾斜杠），与 `/.well-known/openid-configuration` 同基址。(4) `guanjin.enabled` 与 `guanjin.authz.enabled` **受 profile 门控**：仅 `spring.profiles.active` 含 `local|test` 时可置 `false`，其它 profile 置 `false` → **启动直接失败（fail-fast，不是告警）**；生产不存在跳过验签/跳过判定的开关。
- **裁定 (5) 三处默认值 = 纠错性默认值（团队收口 + Spec 复校 2026-11-02；**同类同标，不单列追认**）**：①`guanjin.authz.replay-store` 默认 = **`redis`**（原契约 `memory`）—— 默认 `memory` 在多副本下静默降级为单机内存，`jti` 防重放形同失效，正是索引 §2 禁止的降级放行，且须与 `AC-SESS-05` 一致；`memory` 只允许 `local|test` 显式选。②`guanjin.agent.pull.enabled` 默认 = **`false`**（原契约 `true`）—— 索引 §4.2-24 已定「角色放进内部令牌（方案 A，零回源）」，默认 `true` 等于默认回源，且 100 系统 × 30s 是无谓负载；需要兼容的接入方显式置 `true`。③`guanjin.gateway.jwks-uri` 默认 = **`${base-url}/.well-known/jwks.json`**（见 (3)）—— 旧默认指向**不存在的端点**（首启 404），属错标。
- **为什么这三条不算「对外契约变更」、不进 CH-5（唯一口径）**：三条都是把默认值**改回与已生效决定一致**（索引 §4.2-23 JWKS 路径 / 索引 §2 多副本无状态 / 索引 §4.2-24 零回源），性质是**纠错**而非新增契约面——key 名、数量（29 项）、类型、必填性一处未动。把纠错也挂「待追认」会造成**同类不同标**（先前 `replay-store`/`agent.pull` 已按纠错处理、`jwks-uri` 却留在 CH-5）且让 `PENDING` 账目虚高。**因此 `jwks-uri` 默认值从 CH-5 移出，归入本条**；CH-5 只保留真正改变契约面的两项（29 项交付集、删 `guanjin.mode`）。**副作用（由 Gate 定账，我不改其文件；Gate 回读 2026-11-02 已据此更新）**：`AC-SDK-03` 由 `PENDING·CH-5` 降为 `PASS-able·D6(3)/(5)`（**12 → 11**），`AC-SDK-02` 再因 (4) 属纯加固裁定降为 `PASS-able`（**11 → 10**）——**故现行 `PENDING` = 10、`PASS-able` = 68**，与 `acceptance-criteria.md` 一致。
- **裁定 (5) 与既有条目的关系**：两项均**不与 (1)~(4) 冲突**，也不改 29 项的数量与 key 名（只改"默认值"列）→ `AC-SDK-07` 的逐项对齐仍按 29 项执行；但 Nova 的 `sdk-contract.md` 需同步这两个默认值（其 §3.1 表格 + §8 修订点），**未同步前以本条为准**。
- **【待澄清登记】设计提及 32 项、配置表实测 29 项（缺 3 个，已定位；编号不重排）** —— 本条只**登记缺口**，不改变已定口径（交付集 = 29 项），也不解除 CH-5。
  - **① 依据（配置表实测）**：`/docs/baseline/GuanJin-SDK契约.md` §3.1「可配置项」表**实测 29 行**，原始编号排到 **32**（第 7、16、17 号缺行）。逐键如下（**保留基线原编号，不重排**）：
    `1` `guanjin.enabled`｜`2` `guanjin.platform.base-url`｜`3` `guanjin.platform.connect-timeout`｜`4` `guanjin.platform.read-timeout`｜`5` `guanjin.client.id`｜`6` `guanjin.client.hmac-secret`｜**`7` 缺**｜`8` `guanjin.gateway.assertion-header`｜`9` `guanjin.gateway.issuer`｜`10` `guanjin.gateway.aud`｜`11` `guanjin.gateway.clock-skew`｜`12` `guanjin.gateway.replay-window`｜`13` `guanjin.gateway.jwks-uri`｜`14` `guanjin.gateway.jwks-cache-ttl`｜`15` `guanjin.gateway.jwks-unknown-kid-force-refresh`｜**`16` 缺**｜**`17` 缺**｜`18` `guanjin.authz.enabled`｜`19` `guanjin.authz.default-role`｜`20` `guanjin.authz.model-conf`｜`21` `guanjin.authz.replay-store`｜`22` `guanjin.agent.report.enabled`｜`23` `guanjin.agent.report.on-startup`｜`24` `guanjin.agent.report.on-change`｜`25` `guanjin.agent.pull.enabled`｜`26` `guanjin.agent.pull.interval`｜`27` `guanjin.public-paths`｜`28` `guanjin.public-paths-include-builtin`｜`29` `guanjin.security.inject-security-context`｜`30` `guanjin.security.authorities-resolver`｜`31` `guanjin.security.role-prefix`｜`32` `guanjin.startup.print-public-paths`。**合计 29 条键 / 32 个编号位。**
  - **② 影响面**：**SDK 三语言契约一律以实测 29 项为准**（Nova 已交付的 `docs/sdk-contract.md` §3.2 即 29 项口径，**按其对齐、不让 Nova 返工**；其编号为连续 1..29，与基线「1..32 含 3 空洞」的映射关系 = **去空洞后的先后顺序**，两套编号指向同一 29 个 key）。"32 项"仅作为**历史表述**出现在设计文与 CH-5 描述里，**不得据此新增配置键、不得据此判定 SDK 不完整**。Gate 侧期望值仍是 `AC-SDK-07` 的 **29 项**、`AC-FC-05` 的逐项对齐。
  - **③ 缺的 3 个键名（已定位，非"待定位"）**：由 `GuanJin-SDK契约.md` §4.1 的 `GuanjinProperties` 字段声明顺序（`enabled` → `platform` → `client` → **`mode`** → `gateway` → **`direct`** → `authz` → …）反推，编号正是按字段声明顺序排的，故三个空洞分别是：
    | 缺号 | 键名（推定，来自设计文 `GuanjinProperties` 的 `Mode`/`Direct` 内部类） | 状态 |
    |---|---|---|
    | `7` | `guanjin.mode`（`gateway \| direct`，枚举 `Mode{GATEWAY,DIRECT}`） | **已废止**（D6(2)：只保留网关形态，无模式开关） |
    | `16` | `guanjin.direct.user-token-jwks-uri`（`Direct.userTokenJwksUri`） | **已废止**（随 `direct` 形态作废） |
    | `17` | `guanjin.direct.user-token-issuer`（`Direct.userTokenIssuer`，设计默认 `guanjin`） | **已废止**（同上） |
    即：3 个缺口**不是漏写、也不是待补的键**，而是「direct 形态的 3 个键被删除后编号未重排」留下的空洞。**结论：不补号、不重排、不实现**（补号 = 编造契约；重排 = 让基线文档与 SDK 契约的编号彻底对不上）。
  - **与既有条目的关系**：本条**不新增裁定**，只是把 D6(1) 的"29 项"与"32 项"差异**显式登记为待澄清缺口**并给出可核验证据（键名逐一列出 + 缺号来源）。**CH-5 已随 P8 追认（2026-11-02）**（29 项交付集 + 删 `guanjin.mode` 两项），本条不改变其内容、只登记证据。
- **裁定 (6) 配置键的命名与注入口径（Spec 回读 2026-11-02；不改 29 项、不改 key 名）**：①**无别名键、无旧键兼容**——29 项之外**不得**新增"同义 key"；旧默认值指向错误端点的写法（如 `.../api/v1/jwks`）**也不保留兼容读取**（旧值只作为"错标"被纠正，不双读）。②**环境变量 = key 的机械转换**：全大写、`.`→`_`、`-`→`_`，**逐段映射、不省略任何一段**（`guanjin.platform.base-url` → **`GUANJIN_PLATFORM_BASE_URL`**）；**禁止缩写/合段**（`GUANJIN_BASE_URL` 这类**非法**）。理由：三语言 SDK（Java 8 / Node / Go）必须对同一份 key 表做**同一套**环境变量绑定，任何"顺手取个短名"都会让三语言行为不等价；且平台自身启动配置也用 `GUANJIN_*`（技术栈 §7.2），缩写会与之混淆。③**Java 侧 Spring 宽松绑定的变体（如 `GUANJIN_PLATFORM_BASEURL`）不作为契约**：文档与示例只写规范名，测试不依赖变体（否则 Node/Go 无法等价）。
- **依据**：①②③ —— ③技术栈 §1.1（SDK 只出 2.x、能力不加不减）；①索引 §2「只保留网关形态」「令牌必须验签（预设档位清单）」；①索引 §4.2-23（JWKS 路径与 kid 约定）。
- **受影响 / 落地**：Nova（配置类、Properties、示例 yml、README、`guanjin-core` 的 fail-fast 校验）统一按本条；示例 yml 删掉 `mode:` 行与 `jwks-uri` 旧默认；Gate 增加「非 local profile + enabled=false → 启动失败」用例；契约文档的"32 项"文案由 Spec 在下版口径对齐。

## D7 Flow Descriptor 契约字段

- **冲突**：`FlowStep` 只有 `{factor, role, status, challenge_hint}`，缺 `challenge_id` / `purpose` / 该步可贡献的 `acr`；且所需因子全被禁用时只有 `satisfied:false`、无不可达终态 → 前端只能重渲染死循环。
- **裁定**：Descriptor 契约**补充**（`GET /authorize/flow` 与 `/authorize/submit` 的 `details.flow` 共用）：`steps[i]` 增 `challenge_id?: string|null`（该步已预签挑战时非空，前端据此直接提交而不必先发起挑战）、`purpose: 'login'|'step_up'|'enroll'|'recover'|'verify_identifier'`（取 D2 词表，`register` 不进 Descriptor）、`contributes_acr: 'AAL1'|'AAL2'|null`（该步完成后预计达到的级别）；顶层增 `terminal_state: 'satisfied'|'need_more'|'unreachable'` 与 `unreachable_reason?: {code:'FACTORS_DISABLED'|'POLICY_UNSATISFIABLE', missing_factors: FactorType[]}`。
- **依据**：②端点契约优先（`/authorize/flow` 是契约端点，**加字段不破坏既有键**）；**Descriptor 的字段权威有明确出处**——索引 §4.2-14「Flow Descriptor 字段：以 `页面规格` §4.1 的结构为契约冻结」+ 页面规格 §4.1 标题「对齐 `接口清单` **§3.1.4**」（该节**确实存在**，见 D9 补口②）；故 **字段权威 = 页面规格 §4.1 + 本条扩展**，**端点行为与错误码权威 = 接口 §3.1.4/§3.1.5**，两源分工、不互相推翻；索引 §2「禁止隐性逻辑」——不可达必须是**显式枚举**而非由前端推断。
- **受影响 / 落地**：Edge/Core 计算并下发新字段；Vue 用 `terminal_state` 终止渲染（`unreachable` 渲染终态页并给出可操作建议：联系管理员/换业务系统），**不得**用 `disabled_factors` 自行推断；Gate 加「全部因子禁用 → `unreachable` + HTTP 200（不是错误页）」用例；页面规格 §4.1 的 TS 类型由 Vue 侧同步更新（不回写基线）。

## D8 验收 / 合规口径

- **冲突 A 验收**：基线无验收/测试文档，索引 §4.3-2 把「测试策略与验收清单」降为**建议级·可后补**，唯一可判定的口径只剩技术栈 §9.3 覆盖率表。**裁定**：验收权威 = **移交 Prompt §十一《验收（V1 完成定义）》的三条线（业务/合规/安全）+ 技术栈 §9.3 覆盖率门禁**，二者组合为唯一口径；"建议级"只表示「基线未给本文档」，不表示「可不验收」。Gate 据此出《验收清单》（条款→断言 ID），不另立第二套口径。
- **覆盖率模块下限补登：`internal/flowstore` = 85%（2026-11-04，团队收口）**：它是 **S3 阶梯中间态落库基座**（D5 的 I-F2/I-F5/I-F6/I-F8 唯一落点：锁、校验、amr 累加、两段式清理），**错一处则上层全返工**，故与 `internal/flow` 同级取 **85%**（高于 `internal/store` 的 75%）。；**`internal/identity` = 85%（2026-11-04，团队收口）**：它是"注册即登录"与 I1/I2/I3/I4/I6 不变式的**编排收口**（一次事务内 users→identifiers→authenticators→completed+consumed 的 `flows`→`auth_events`，失败零残留），错一处即**用户被锁门外或产生孤儿账号**，故与 `internal/factor`/`internal/credential`/`internal/identifier` 同级取 85%；**执行侧同样归脚本 owner**（`module_floor()` 需把 `internal/identity` 并入 85 那一条 case）——本台账只定口径，**不改脚本**（改脚本等于改门禁语义，由 Gate 域落地并同步本行）。
- **【正式裁定 D8-B】(2026-11-02，收 Gate §15-G12；本节即外部引用的「D8-B」)**：`AC-AUD-10` 的呈现唯一口径 —— 状态名 = **「后置（DEFERRED）：本期不做判定」**，**计入「后置」栏，`BLOCKED` 栏计数 = 0**；**现行账目（Gate v11，2026-11-02）= 90 = 83 `PASS-able` / 0 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置（DEFERRED）：本期不做判定」**（v10 起 85 → 90，+5 条全由本条之后新增的两个口径带出：**D12** 的 `AC-FLOW-12/13`（`PASS-able`）、**CH-4④⑤** 的 `AC-REG-08/09/10`（原 `PENDING·CH-4`，P8 追认后随 13 条一并转 `PASS-able`，即 v11 的 70 → 83）；分组随之变为 REG 10 / LOGIN 8 / FLOW 13 / SESS 5 / DP 13 / SDK 10 / FC 8 / AUD 12 / ENG 8 / AGENT 3；**账目与分组以 Gate 的 `acceptance-criteria.md` 为唯一数源，本文只同步引用、不自行计数**）。**正式摘除 `BLOCKED` 的依据**（满足 Gate「摘 `BLOCKED` 需 decisions 正式裁定」的要求）：Gate 状态表里 `BLOCKED` 的**定义前提是「期望值依赖残余待裁项」**，而 `AC-AUD-10` 依赖的三项已不在「待裁」状态——**R2/R4 已按 spencer 口径后置（挂起）、R3 已关闭（TLS 出本平台边界）**，前提消失则标签失效；继续挂 `BLOCKED` 会让账目永远有一个永不收敛的红项，掩盖真实进度。**同时保留 Gate 的关切为硬约束（后置 ≠ 免验）**：`acceptance-criteria.md` §11.1 的 **7 项输入（I-1~I-7）仍须在 M2 之前备齐**，`AC-AUD-12` 继续覆盖应用侧可判定部分；「后置」项**不得写成通过、不得写"预检通过"、不得从账目里摘掉**（仍占 1 条、仍出现在映射表的「后置」栏）。
- **依据**：①索引 §2「密评」条 + spencer 口径（2026-11-02）「由 `BLOCKED` 改成「后置（DEFERRED）：本期不做判定」」；②Gate v6 的状态定义（`BLOCKED` = 依赖残余待裁）；③D8-A「不造假通过」——摘 `BLOCKED` 不等于记 PASS，而是换成更准确的状态名。
- **密评后置的三条内容（原「冲突 B」裁定，保留）**：①**密评整体后置**——**本期不做密评判定、不出证、不送检**（依 spencer 口径 2026-11-02），相关条目一律记「后置（DEFERRED）：本期不做判定」，**R2/R4 挂起、R3 关闭（TLS 已出本平台边界，不是"挂起"）**。②**等保相关的代码要求照基线实现、不受后置影响**：双因素/多因素组合鉴别、审计只增不可篡改（哈希链）、三员分权、留痕与脱敏等**实现要求保留**，只是**本期不做出证与送检判定**（"实现要求保留、判定与出证后置"）。③**传输层与关津无关**：TLS 终结/国密 TLS(TLCP)/证书校验**不属本平台范围**，由部署方前置（见 P2②），平台只见明文，因此「传输国密」不构成本平台的实现项，也不进验收判据。
- **【已作废 · 2026-11-02】裁定 E（密评等级口径，外部记作 `D8-E`【旧版】，收口第 10 项）：等级只引用索引 §2「上限二级」，数据模型的「密评三级」不适用。**
  > **⚠️ 本条结论已作废**：其「上限二级」的核心结论**被下方 `D8-E`（第二版）取代**（密评等级不得低于备案等级，见第二版①）。原文保留仅供可追溯，**任何引用一律以第二版为准**；旧版中**仍然有效**的三条（策略数据完整性按索引 §4.2-27 实现、`L2` 档位默认值 ≠ 平台定级、本期仍不做密评判定）已并入第二版⑥。
  - **冲突**（四处互相矛盾）：①索引 §2「**密评上限为二级**（因不引入硬件密码模块）」；②数据模型 **§3.13 `policy_snapshots`** 的设计说明「**密评三级** 8.4 g)/8.3 c) 要求访问控制信息与策略数据在存储中完整性受保护」；③数据模型 **§3.8 `clients`** 的 `tier` 设计说明「默认 L2（新建业务系统不特别声明即满足**等保二级 + 密评二级**）」；④移交 Prompt §一「等保三级 + **密评**」（等级写「见 §七 待确认」，§七-3 明说要定二级还是三级）。
  - **裁定**：①**本项目对外只声明「等保三级」**；**密评等级一律引用索引 §2「上限二级」**（纯软实现、不接密码机 → 对应 GB/T 37092 二级口径），**不声明、不追三级**。②数据模型里「密评三级 8.4 g)/8.3 c)」属**引错级别**——该条文对应密码应用三级要求，本项目不适用；**仅保留其实现要求中与等级无关的部分**（策略数据完整性用 `payload_hash` + `integrity_tag`(HMAC-SM3) 保护，这条本来就由索引 §4.2-27 要求），**不得据此新增任何"三级专属"实现项或判据**。③「等保二级」那句**不与「等保三级」冲突**：`L2` 是**客户端档位默认值**（默认 L2、承诺 AAL1），「等保三级」是**平台整体定级**，二者是不同维度，**不得互相推导**（不得因"默认 L2"就说平台只满足等保二级，也不得因"等保三级"就把默认档位抬到 L3）。④**TLS 不参与等级判定**（P2②：不属本平台范围）。⑤**本期仍不做密评判定**（D8-B），本条只收口"写哪个等级"，不解除后置。
  - **依据**：①索引 §2 优先于正文引用（`D1` 同源规则）；②P2①（纯软国密、不接密码机）+ P2②（TLS 出边界）共同决定"上限二级"是自洽的那个数；③`D8-B②`（等保相关**代码要求**照基线实现、只是不出证）。
  - **受影响 / 落地**：**Gate** 不新增三级专属判据（`AC-AUD-02/03/04/07/09/12` 的等保代码要求一条不动）；**Spec** 对外统一一句「等保三级；密评本期不判定，~~引用上限二级~~」**（该句作废，见第二版①②）**；**Ark** 不因"密评三级"扩大实现范围（不加三级专属密码模块要求）；**Nova/Vue/Core/Edge** 无动作（本条不改任何表 / 端点 / 字段）。**不改的冻结名**：`等保三级`、`密评`、档位 `L1/L2/L3`、`acr`(`AAL1/AAL2/AAL3`) 全部照抄。
- **冲突 C fail-closed 触发码四选一**：`DEPENDENCY_UNAVAILABLE` / `REDIS_UNAVAILABLE` / `DB_UNAVAILABLE` / `KEY_UNAVAILABLE` 未定。**裁定**：按**具体依赖发具体码**（`REDIS_UNAVAILABLE` / `DB_UNAVAILABLE` / `KEY_UNAVAILABLE`），`DEPENDENCY_UNAVAILABLE` 仅用于未分类的第三方依赖；统一 HTTP **503** + `Retry-After`，并同步反映到 `/readyz`（不就绪）。
- **裁定 C′（收口 Gate §15-G1：E6 全组归位）**：接口 §5.4 的 E6 七个码一次性收口，杜绝"同一故障两个码"的双轨——①`INTERNAL_ERROR`＝未捕获的内部错误（500，缺陷，非预期路径）；②`REDIS_/DB_/KEY_UNAVAILABLE`＝具体依赖不可用（503，D8-C 主口径）；③`DEPENDENCY_UNAVAILABLE`＝**未分类**的第三方依赖（503，兜底，不得用于上面三个已知依赖）；④`UPSTREAM_UNAVAILABLE`＝**数据面转发上游失败/超时**（`GUANJIN_GATEWAY_UPSTREAM_TIMEOUT`/拨号失败，唯一明确判定点在 `cmd/guanjingw`）；⑤**`SERVICE_DEGRADED` 明确废弃、永不返回**——它与 `DEPENDENCY_UNAVAILABLE` 语义重叠且无独立判定口径，保留它必然产出"同故障双码"，与本条要消灭的问题同类；CI 增 `scripts/check-forbidden-error-codes.sh` 禁止该常量出现在任何返回值中（与 `check-no-automigrate.sh` 同批落地）。据此 E6 已无悬空码。
- **裁定 D′（收口 Gate §15 的 `/userinfo` 争议，Gate 记作 D8-D）**：`/userinfo` 按接口 §5.8 是**本地解析、不查库不回源**，因此 **DB 不可用时它仍返回 200**（SQL 计数必须为 0）——这**不构成降级放行**：该端点没有失去任何防护（令牌已验签、`exp`/`aud` 已校验），fail-closed 针对的是"依赖不可用时仍放行本该被拦的流量"，而本地解析不依赖该依赖。**反向约束**：一旦实现需要查库（例如要向库取最新角色），该路径**必须**返回 503 `DB_UNAVAILABLE` 而不是降级用缓存——即"要么零查库，要么如实 503"，不存在第三种。
- **【正式裁定 D8-E（**第二版**，2026-11-02，**取代上方旧版「裁定 E」**；旧版"上限二级"结论作废）】密评等级与责任归属口径（收 Spec 口径锚；**只改承诺与表述、不改工程实现、不改任何判据状态**）**：
  - ①**索引 §2「密评上限为二级（因不引入硬件密码模块）」作废、不再引用，也不再对外使用**。依据：**密评等级随接入系统的等保定级走、不得低于备案等级**（GB/T 39786-2021 第 4 章；密评 FAQ 第三版 1.2/1.3「应确认密码应用等级等于或高于等级保护备案等级」——原文核对由 Spec 完成）。**代价说清**：「等保三级 + 密评二级」这一组合**测评机构不受理**，基线原句会把目标定低并诱导团队按二级裁剪能力。
  - ②**条款引用保持 8.x，禁止改指 7.x**：二级条款下 8.2 e)、8.3 远程管理通道、8.4 h) 属**「不作要求」**，且 8.x 的「应」在二级降为「宜/可」——把引用降到 7.x 等于把**已按三级实现**的能力（双因素 / 审计链 / 存储加密）**自降标准**。统一表述冻结为：**「实现按 GB/T 39786 第三级条款固定，二级场景为其子集；判定与出证后置」**（后半句即 D8-B 的"后置"，两句同时成立、不矛盾：**实现标准不降、判定不出证**）。
  - ③**TLS 责任归属必须写清，禁写"无影响"**：二级 7.2 c) / 7.4 c) 是**「宜」**（不阻塞，但**未声明不适用**时仍纳入测评、判部分符合而扣分）；三级 8.2 c) / 8.4 d) 是**「应」**（阻塞）。按 **P2②** TLS 已出本平台边界 → 正确表述是：**本平台不因 TLS 被判不符合（测评对象是本平台的密码应用；传输通道由前置承担），该风险转由部署方前置 TLS（国密传输）承担**。**不得**写成"TLS 与本项目无关 / 无影响"——那是把**责任转移**说成**风险消失**。
  - ④**密码产品不得承诺"密评通过"**：GB/T 37092 **一级**对**纯软可达**（一级不要求物理安全机制），但 **BouncyCastle / `tjfoc/gmsm` 是算法库、不是密码产品**，**自研软实现直接判不符合**。v1 的**合法定性只有「自评 + 遗留风险登记」**（或改用持证的软件密码模块）；若服务对象是**等保三级**系统，需 37092 **二级**（含防拆封装等物理安全机制）→ **纯软无解**。此结论与 **R2（后置）**一致：本期**不送检、不出证**，只如实登记，**不得**对外说"密评通过"或"预检通过"。
  - ⑤**落点分工**：本条是**口径/表述**裁定——不改实现、不改 `P2①`（纯软国密照旧）、不改判据（`AC-AUD-10` 仍为「后置（DEFERRED）：本期不做判定」，`BLOCKED` 仍为 0）；索引 §2 原句与档位清单 / 数据模型里「承诺密评二级」的同类表述，**由 Spec 在对外口径统一改写**（其 `frozen-names-and-gaps.md` 已登记，Gate 已同意同步条号侧的目标等级表述），本文只冻结**实现侧**措辞。
  - ⑥**从旧版承接、仍然有效的三条**（旧版作废不连带这三条）：(a) 数据模型 §3.13「密评三级 8.4 g)/8.3 c)」的**实现要求**照做（`payload_hash` + `integrity_tag`(HMAC-SM3)），依据是**索引 §4.2-27**、不是"三级专属"——**不得**因此新增任何"三级专属"实现项或判据；(b) `L2` 是**客户端档位默认值**（承诺 AAL1），与「平台定级」是**不同维度**，**不得互相推导**；(c) **本期仍不做密评判定**（D8-B），本条只收口"按哪级条款实现、责任怎么写"，**不解除后置**、不出证、不送检。
  - ⑦**呈现口径（hugo 裁决，2026-11-02）：等级只标「待澄清 / 待追认」，不写死；密评整体按「预检清单」落，不进契约强制项。**
    (a) **不写死**：对外任何文档、页面、SDK 说明**不得**出现「承诺密评二级 / 密评三级已通过」这类结论性表述；牵涉「目标等级」的地方一律写 **「待澄清 / 待追认」**（正文口径与 Spec 的对外改写同步）。
    (b) **只看实现、不作声明**：工程侧口径仍是 ②**按 GB/T 39786 第三级条款固定、二级为子集**（这是"按哪级条款实现"，不是"等级承诺"），条款引用**保持 8.x 不降 7.x**；**不得据此对外声明密评等级**（对外一律「待澄清 / 待追认」）。
    (c) **按预检清单落、不进契约强制项**：密评相关材料以**预检清单**形式交付与登记，**不写进 OpenAPI / SDK 契约 / 页面契约的强制项**，也不作为任何端点的前置条件。
    (d) **不阻塞任何一件**：`AC-AUD-10` 仍为「后置（DEFERRED）：本期不做判定」、`BLOCKED` 恒 0；`AC-AUD-12`（应用侧国密实证）照常独立判；M1 任何步骤、任何 PR 都**不因密评表述未定而卡住**。
  - ⑦**本次改判的影响面（必须同步，否则又是两套口径）**：旧 `D8-E` 曾被 `m1-slices.md`（第 10 项）与 Spec/Gate 引用 → **我已同步改 `m1-slices.md`**；**Spec 的对外口径**改由第二版措辞；**Gate 的判据侧**若有"上限二级"表述需同步（其文件不动，由 Gate 自己改）。**不改的冻结名**：`等保三级`、`密评`、档位 `L1/L2/L3`、`acr`（`AAL1/AAL2`，`AAL3` 预留见 D12）照抄。
- **受影响 / 落地**：Gate（验收清单与状态记账；**密评判定已后置 → `AC-AUD-10` 记「后置（DEFERRED）：本期不做判定」，不得写"预检通过"**）、Edge（就绪探针与 503 码 + `UPSTREAM_UNAVAILABLE` 判定点）、Core（异常映射表）、Ark（新增禁码扫描脚本）、Spec（对业务方口径）。**遗留**：R2/R4 后置（挂起）、R3 已出本平台边界（TLS 属部署方前置）——见残余待裁与 P2②。

## D9 引用缺章（数据模型 §6 / 页面规格 §7 / 接口清单 §6）

- **冲突**：三处被正文引用的章节在源文档中**不存在**（数据模型全文只有 §0–§5 + 附录 A/B；页面规格无 §7；接口清单 §5.5/§5.8 的 ⚠️ 正指向不存在的 §6），导致 C5/C10/A1 页面与分页/签名算法的「见 §x」全部断链。
- **裁定**：**引用缺章时的兜底顺序**（写进工程规范）：① 被引用文档内**同主题的端点契约/数据契约**（表格化、可执行的那部分）→ ② **索引 §2** → ③ **本台账已裁条目** → ④ 皆无则该行为**不实现**（禁推定默认），并登记 ADR 待补。逐条映射：数据模型 §6 → 以其 §4.1 不变式 + 索引 §4.2 各项默认值为准（数据模型正文各处的「见 §6」已知项均已落在 §4.2）；页面规格 §7「待补清单」→ 以页面规格 §2 页面清单总表 + 接口清单 §3 逐面详表为准，Vue 缺细项时**只用已冻结端点/字段**，不得自造；接口清单 §6 → 分页按 **D3-A**、幂等按接口 §5.6、签名算法按 **D1** 执行，不再等待 §6 补文。
- **补口（Spec 回读 2026-11-02）：缺章清单再补 1 处、另 1 处经复核否决、1 处改指向**：
  - ①**`SDK契约` §8 缺失（真悬空，必须补进清单）**：其 §2 能力 #21 注「权限模型设计 §5；**本文 §8**」，而该文全文只到 **§7「异常与降级」**（无 §8）。影响三件全无契约：**`GET /api/v1/agent/policies` 的 `since_version` 语义**（接口 §3.10.2 只写 `?since_version=&user_ids[]=`，无形态、无"何时递增"）、**策略缓存失效条件**（`jwks-cache-ttl` 有、策略缓存无）、**拉取失败的降级**（§7 表里"方案 A / 方案 B"两行）。→ 兜底按本节顺序取：`since_version` = **服务端单调递增的值、客户端原样回传、不得解释内部含义**；默认走**方案 A（角色放进令牌、零回源，索引 §4.2-24）**，`agent.pull.enabled=false` 时该能力**不实现**（禁推定默认）；细节待 ADR 补。**这条直接卡 Nova 三语言实现与 `AC-SDK-10`。**
  - ②**`页面规格` §4.1「对齐接口清单 §3.1.4」不是悬空（Spec 提出、经我复核【否决】）**：接口清单第 206 行确有 `#### 3.1.4 \`GET /authorize/flow\``，并给出 Descriptor 响应要点；且索引 §4.2-14 已冻结「Flow Descriptor 字段以 `页面规格` §4.1 的结构为契约」。故 **§3.1.4 不得被判为笔误、不得删除引用**（否则 Descriptor 的端点归属失去出处）。正确口径 = **两源分工**：**字段权威 = 页面规格 §4.1 + D7 扩展**；**端点行为 / 错误码权威 = 接口 §3.1.4 / §3.1.5**（与 D7 依据一致）。
  - ③**`数据模型` §4.1 指向的《错误码体系》确实不存在**（12 份基线无此文）→ 该表表头「建议错误码，待《错误码体系》定稿」的兜底改为 **接口 §5.4 + 本文 D3-B / D3-B′**：I1/I2 → `LAST_IDENTIFIER_CANNOT_UNBIND`、I3/I4 → `LAST_AUTHENTICATOR_CANNOT_REMOVE`、I8 → `TIER_DIRECTION_VIOLATION`、I6 **无对外码**。
- **附带**：索引 §4.1/§5 写「待裁决项：无」，与移交 Prompt §七 的 6 项并存。**裁定**：以**移交 Prompt §七**为最新状态（索引 §4/§5 是编制时点快照）；其中 §七-1 已由 **P1** 关闭、§七-2 由索引 §2 关闭（令牌算法固定 `SM3withSM2`，不保留 RS256 兼容路径）、§七-3/4/5/6 见残余待裁。
- **受影响 / 落地**：Spec（对外口径与文案引用不得指向缺章）、Vue（C5/C10/A1 只依据 §2 总表 + 接口 §3）、Gate（用例不引用缺章条款，改引本台账编号）、Ark（所有 ADR 引用正文编号而非缺章）。

## D10 内部令牌 `X-GuanJin-Assertion` 的剥离时序（唯一口径，Spec 复校 2026-11-02）

- **冲突**：三处互斥——`sdk-contract.md` M8 与 §5.3.1-2 写「**验签前**剥离」；`AC-SDK-08` 写「注入上下文**之后**、下游转发之前」；基线数据面设计 §4.6 的顺序是「…注入上下文 → 剥离」。按 M8 字面实现会「没令牌可验」（凭空 401）或直接挂掉 `AC-SDK-08`。
- **裁定（唯一时序，六步，禁止换序）**：①**读**原始请求头取 `X-GuanJin-Assertion`（此时不删，它是唯一凭证来源）→ ②**验签**（SM2，JWKS 缓存 + 未知 `kid` 强刷一次）→ ③**校验 claim**（`exp`/`iat` 时钟偏移、`iss`、`aud`、`jti` 重放）→ ④**注入上下文**（`SecurityContext` / `Principal`）→ ⑤**替换请求视图**：交给宿主的 request 中该头**已不存在**（wrapper 覆写 `getHeader`/`getHeaders`/`getHeaderNames`；Go 侧删除并重建 `r.Header`）→ ⑥**业务 handler 运行**。
- **剥离的对象与时点**：剥离的是**交给宿主的请求视图**，不是原始凭证——因此「验签前剥离」被明确否决（会无令牌可验）；「只在下游转发前删一次」也不够（框架/日志中间件可能已缓存 header 名），必须用**视图替换**保证 handler、业务转发、日志三类消费者**都取不到**。等价表述：**剥离晚于验签与 claim 校验、早于模型与业务代码**。
- **依据**：数据面设计 §4.6 第 5 步（验签后剥离）、接口清单 §5.2「验签后必须在下游转发前剥离该 header，任何日志不得记录完整令牌」；①②③④ 的顺序即接口 §5.2 的强制项（验签→`exp`→`jti`）。
- **受影响 / 落地**：**Nova** 改 `sdk-contract.md` 的 **M8 与 §5.3.1-2** 那句「验签前剥离」为「验签与 claim 校验通过后、注入上下文之后**替换请求视图**（handler/下游/日志均取不到）」；**Gate** 的 `AC-SDK-08` 断言按本条改为「`MockFilterChain` 收到的 request 无该 header **且** 验签发生在剥离之前（篡改令牌仍 401、不因剥离而空验）」；Edge 的网关侧「先弃客户端伪造同名头、后签」不变（那是**另一个**剥离，见 S8）。

## D11 SDK 能力命名冻结：`GJ-CAP-13` 的密级方法（Spec 复校 2026-11-02）

- **冲突**：`sdk-contract.md` 的 `GJ-CAP-13` 写 `dataLabel()`，而基线《索引》§4.2-25 冻结的是 `role(name).**withClearance(level)**` —— 同一能力两套写法。
- **裁定（含客体密级的唯一入口，0 页终校后补齐 2026-11-02）**：**统一为 `role(name).withClearance(level)`**，`dataLabel()` **作废**（不得作为别名保留，避免"两个方法一个语义"）。`GJ-CAP-13` 的完整签名以索引 §4.2-25 为准：`role(name).can(resource, ...actions)` / `role(name).withClearance(level)` / `resourceGroup(name).contains(resource)` / `defaultRole(name).can(...)` / `build()`。
- **客体密级（原来由 `dataLabel()` 声明的那一半）→ 选方案①：不做声明式打标**。客体等级由**业务在调用点**把值传给既有能力 `Authz.checkLabel(objLevel)`（`GJ-CAP-16` 语义不变），**不新增任何 builder 方法、不新增配置、不新增 SDK API**。理由：①索引 §4.2-25 冻结的五项里**没有客体声明入口**，`dataLabel()` 是 SDK 契约自造的第六项，作废后不应再自造替代名；②索引 §2「平台不做行级/密级判定」只约束平台侧，SDK 本地 `checkLabel` 判定本来就允许（权限模型 §3 三层之一）；③"声明式打标"要求平台/SDK 维护一份客体标签字典，那是**新增数据面**，成本远大于收益，且 M5 才用到。
- **它是否属新增契约面（按三分判据判定）**：**不属**——不改 key、不改能力数量（22 条不变）、不改既有方法签名，只是把"等级由调用点传入"显式写成唯一用法，**业务方不能用基线原本不允许的方式调用 SDK**（`checkLabel` 早已是允许的本地判定）。**因此不进 CH 清单**；但它是**设计选择而非纠错**，故按「团队收口 + 登记 **ADR-004**」处理（`docs/adr/ADR-004-客体密级由调用点传值.md`，随 S0 入库）。
- **依据**：①索引 §4.2-25（冻结的五项签名）+ 索引 §2（平台不做密级判定）；②权限模型 §3（SDK 三层判定：角色能力 / 密级 / 资源组）；索引 §2「不许有隐性逻辑」——把一个自造方法换成"调用点传值"必须显式写死，不能留给实现者猜。
- **补口 C1（Spec 复校 2026-11-02 提出、Gate v9 已落判据）：裸 `clearance()` 同属作废，不留第三套写法。** 基线 SDK 示例除 `dataLabel()` 外还用 **`.clearance(3)`**（无 `with` 前缀），上一版 D11 只作废 `dataLabel()`，照基线示例实现就会复活第三个名字。故冻结：**唯一写法 `role(name).withClearance(level)`**，`dataLabel()` 与**裸 `clearance()`** 一并作废、均不得作为别名保留；门禁侧（`AC-ENG-08`③，Gate v9）同步扩为**两个禁词**并明示口径：正则 **`(?<![A-Za-z0-9_])clearance\(`（大小写敏感）**——`withClearance(` 的 `C` 大写且前缀是 `with`，**不命中**；`.clearance(3)`、`clearance(` 均命中；**扫描范围只限 SDK 三语言仓**（源码 + `examples/` + `README*` + 仓内 `docs/`），**不含 `/docs/` 与 `/docs/baseline/`**——作废声明本身必须能写出旧名，否则登记处自己被门禁点红；变异测试由三个注入扩为**四个注入各自单独验一次**。
- **补口 B1（Spec 复校 2026-11-02 提出、Gate 同意）：`level` 取值词表冻结为 1/2/3，且「公开」不占级。** 基线示例自相矛盾（`.clearance(3) // 秘密` 与 `dataLabel("order_items", 2) // 秘密`），若不冻结，M5 的 `checkLabel`（主体 ≥ 客体）无法唯一判定。故冻结：**`1 = 内部`、`2 = 秘密`、`3 = 机密`**，数值越大越敏感；**「公开」= 资源不声明 label → 跳过密级判定，不存在 0 级**（依权限模型 §3.4「未声明即不判定」）——这样 M5 不会留第四个悬空值。基线 `.clearance(3) // 秘密` 的注释属**笔误**（3 应为机密），与 `dataLabel(...,2) // 秘密` 的矛盾由此消解；词表随 **ADR-004** 入库。**本条不影响 M1**（`GJ-CAP-13/16` 只在 M5 生效，22 条能力与 29 项配置不变）。
- **补口 C2（Spec 复校 2026-11-02 提出）：`checkLabel` 的形参名统一 `objLevel`** —— 主体密级叫 `level`（builder `withClearance(level)`），客体密级在调用点传入时叫 **`objLevel`**（`Authz.checkLabel(objLevel)`），避免同名不同义（`sdk-contract.md` §2.1 原写 `checkLabel(level)` 需同步改名，属命名统一、不改签名个数）。
- **依据**：①索引 §4.2-25 属「已生效的设计决定（有默认值/已冻结签名）」，优先级高于 SDK 契约的命名；索引 §2「平台不做密级判定」指的是**平台侧**，SDK builder 的密级**声明**能力不受影响（声明在 SDK 本地、判定在 SDK 本地）；②补口 C1/B1 的落点是「同一语义只许一个名字、一个取值域」——索引 §2「不许有隐性逻辑」与此一致（第三个名字、第四个悬空取值都会变成隐性契约）。
- **受影响 / 落地**：**Nova** 把 `sdk-contract.md` 的 `GJ-CAP-13` 行与相关示例里的 `dataLabel()`、**裸 `.clearance(n)`** 全部改为 `withClearance(level)`，`checkLabel(level)` 改名 `checkLabel(objLevel)`，并在其 §8 修订点登记；**基线示例 `/docs/baseline/GuanJin-SDK契约.md` 里那两行 `.clearance(3)` / `.dataLabel("orders", 1)` 已被本条取代、照抄进 SDK 即门禁红**（基线只读、不回写，靠 D11 + 门禁表达）。**Ark** 在 `scripts/check-forbidden-error-codes.sh`（S1）按上述正则与范围落两个禁词、四个注入的变异用例；**Gate 已在 `acceptance-criteria.md` v9 落判据**；**Ark** 把 level 词表与「公开不占级」写入 **ADR-004**。能力条数不变（仍 22 条），配置仍 29 项。

---

## D12 `acr` 取值域（基线四处四种写法）与「`AAL3` 不得产出」

- **冲突（同一取值域四种写法）**：①数据模型 DDL `sessions.acr` 与 `login_policies.min_acr` 写 `CHECK (… IN ('AAL1','AAL2','AAL3'))`；②索引 §4.2-15 只映射到 `L1/L2 → AAL1、L3 → AAL2`；③discovery 只发布 `acr_values_supported=["AAL1","AAL2"]`（接口 §3.1.1），内部令牌 claim 也写 `acr（AAL1|AAL2）`（接口 §5.2）；④页面规格 §4.1 的 `Acr` 类型含 **`'AAL0'`**。另：`系统架构说明` §2 词表与第八章把保证级别写成「**三级 = 强方式**」——本项目**没有 AAL3**（档位清单明示 AAL3 需硬件认证器 + 抗钓鱼，本期不做）。
- **裁定**：(1) **对外可见取值只有 `AAL1 | AAL2`**——discovery、`id_token` 的 `acr` claim、`/auth/verify` 响应、Descriptor 的 `target_acr`/`current_acr` 一律照此。(2) **`AAL0` 只是前端展示态**（"未认证"），**不落库、不进任何 wire 字段**；页面规格的 `Acr` 类型保留它仅用于渲染，**不得出现在 API 响应里**（D5 已同此口径）。(3) **`AAL3` = 预留值**：DDL 的 `CHECK` **保留**取值空间（不改基线、避免将来做迁移），但 **V1 任何代码路径不得产出 `AAL3`**；实现侧**必须断言**——档位映射器（`internal/flow/descriptor.go`）与令牌签发处对 `acr` 做**白名单校验**（只允许 `AAL1|AAL2`），越界按**内部缺陷**处理（500/503，`INTERNAL_ERROR`），**绝不静默截断成 AAL2**（静默即隐性逻辑）。(4) **必须有用例**：①「`L3` 档位 → `AAL2`，绝无 `AAL3`」；②变异注入构造 `acr='AAL3'` 的会话/令牌 → 断言被拒。**理由**：discovery 已对外声明只支持 AAL1/AAL2，若某条路径真能产出 AAL3，**对外契约不实**（客户端按 `acr_values_supported` 做能力协商会拿到未声明值）。
- **依据**：索引 §4.2-15（档位→ACR 只有 AAL1/AAL2，属「有默认值，按默认走」）+ 接口 §3.1.1 discovery 的对外声明 + 档位清单「AAL3 本期不做」；`AAL0` 不落库见 D5。
- **受影响 / 落地**：**Core**（映射器 + 签发处白名单断言 + 两条用例）；**Edge**（`sessions.acr` 读写不放大取值域；`pgxerr` 对 `sessions_acr_check` 的 23514 映射为 400 而非 500）；**Vue**（`Acr` 类型只用于展示，API 层类型不得含 `AAL0`）；**Gate**（已落 `AC-FLOW-12`（`L3`→`AAL2`、`AAL3`/`AAL0` 零落库）与 `AC-FLOW-13`（合法签发 `acr=AAL3` → 401、入参 → 400，禁静默降级），均 `PASS-able·D12`）；`系统架构说明` 第八章「三级 = 强方式」判为**笔误**，**对外接入文档不得照抄**（由 Spec 统一改）。

---

## D13 `login_policies` 的三档 `tier_default` 必须有 seed（S6 e2e 唯一前置）

- **冲突/缺口**：`login_policies` 只有表、没有 `tier_default` 行 → 综合环境里注册/登录取不到策略，只能返回 **500 配置错误**；Core 在 S4b 实测撞到，且这是 **S6 e2e 的唯一前置**。
- **裁定（选 ①：迁移里 seed 三行）**：新增 **`migrations/000017_seed_login_policies_tier_default.{up,down}.sql`**，插入 **L1/L2/L3 三个 `tier_default`**，`min_acr` 按索引 §4.2-15 **L1/L2 → `AAL1`、L3 → `AAL2`**；`id` 用**固定 UUID 字面量**（`id` 无 DB 默认值，D4-A），仅满足主键、**不承载业务含义**；幂等用 `ON CONFLICT (tier) WHERE scope = 'tier_default' DO NOTHING`（对准部分唯一索引 `login_policies_tier_default_uidx`，运维改过的值**不被覆盖**）；`down` 按那三个固定 UUID 精确删，不动 `client_override` 行。
  - **为什么选 ① 而不是"初始化流程"**：三档与它们的 `min_acr` 是**基线冻结的结构性默认**（索引 §2「档位只有一层」、§4.2-15/16），不含环境/租户信息 → 属**参考数据**，与 `000015` 的 `flow.ttl_seconds` seed 同类；而初始化流程会引入"首个管理员还不存在就要配策略"的先有鸡先有蛋问题。
  - **为什么新增 `000017` 而不是改 `000012`**：`golang-migrate` 只记版本号、**不校验 checksum**，改已 `up` 过的迁移会让**已迁移库静默缺 seed**；技术栈 §6.2 要求序号单调递增。
- **必须配 fail-fast（否则缺策略仍可能 500）**：`cmd/guanjind` 装配期**校验三档 `tier_default` 各恰一行**（`L1/L2/L3`，`scope='tier_default'` 且 `tier` 非空），**缺失或多行 → 非 0 退出**（与 `flow.ttl_seconds` 键缺失即启动失败的风格一致）。**归属：Core**（S6 前置在 `internal/identity`/`protocol` 的策略读取路径上，且它先撞到该缺口）。
- **归属与复核**：迁移 **Ark 出**（本 PR 已落 `000017`）、**Edge 复核**（与 D5 的 DDL 复核同批）；启动期校验 **Core 落**；`m1-slices.md` **S6 前置点**已补引本条。

---

## D14 注册面三处补口（挑战 TTL/次数来源、`register_token` 形态、邀请码）

> 触发：M1 补课刀（S4 注册面 HTTP）。三处都是**契约没写死**、不允许实现自己发明的地方。

- **① 挑战 TTL 与失败次数：单一来源 = `runtime_config`（迁移 `000018` 已落）**
  - 落地记录：`000018` 由 **#32** 落进 main（`8cd751a`），`git ls-tree origin/main migrations/` 里**恰好两份**（`.up`/`.down`）；本 PR 只留裁定文本，**不再带同名迁移**（避免两份编号）。
  - 新增两个键（`migrations/000018_seed_challenge_config.{up,down}.sql`，`ON CONFLICT (key) DO NOTHING`）：**`challenge.ttl_seconds` = 300**（`int`）、**`challenge.max_attempts` = 5**（`int`）。键名沿用 `flow.ttl_seconds` 的 `.` 分段 + 下划线风格。
  - **同迁移内 `ALTER TABLE challenges ALTER COLUMN max_attempts DROP DEFAULT`**：留 `DEFAULT 5` 等于给"忘记传值"留一条静默成功路径（键改成 3、代码漏传 → 库里还是 5），正是 `AC-REG-01` 要消灭的"第二套来源"；移除后漏传立即 **23502**（GORM 省略零值 → NOT NULL）或 **23514**（`max_attempts > 0`）。
  - **读取口径**：`challenge.ttl_seconds` 走既有 `store.RequiredIntSeconds` 并**加入 `StartupKeys()`**（缺键 = 启动 fail-fast，与 `flow.ttl_seconds` 同款）；`challenge.max_attempts` **不得**借 `RequiredIntSeconds`（它返回 `time.Duration`，5 会被读成 5ns，语义错），需另一个 `RequiredPositiveInt`（`value_type` 必须 `int`、值 > 0）并同样纳入启动期校验（一次把所有缺失键报全）。**代码里不得出现 300 / 5 常量**（含测试数据以外的一切路径）。
  - **`register_token` 在 skip 分支的有效期同样取 `challenge.ttl_seconds`**（注册窗口与挑战同 TTL，**不新增第三个键**）。
  - 归属：迁移 **Ark**（已由 #32 落 main）；`internal/store/runtimeconfig.go` 的三个读取点 + `crypto` 见②**归 Edge**；`cmd/guanjind` 装配期调用 + `internal/identity`（注册服务）消费**归 Core**。

- **② `register_token` = 自包含的 HMAC-SM3 签名串（**不是** `challenge_id`，**不得**为空）**
  - **为什么不能用 `challenge_id`**：§3.3.2 的请求体只有 `{register_token, code?, password}`——**没有 `client_id`/`type`/`value`**，而建号必须知道"给哪个 client 注册哪个标识符"。`challenges` 行也**没有任何列**能承载"尚未存在的标识符值"（`identifier_id` 指向还没建的行、`target_masked` 是脱敏值、`response_hash` 是摘要）。
  - **为什么不能为空**：§3.3.2 鉴权行原文是「匿名 + `register_token`」；§3.3.1 允许缺的是 **`challenge_id`**，不是 `register_token`。skip 分支（`AC-REG-05`：仅用户名+密码免验证）照样签发令牌，只是其中 `chid` 为空。
  - **形态**：`v1.<payload_b64url>.<mac_b64url>`；`payload` = 紧凑 JSON `{"v":1,"cid":"<client_id>","typ":"email|phone|username","val":"<归一化标识符>","chid":"<challenge_id 或空>","exp":<unix 秒>}`；`mac` = `HMAC-SM3(K_register, "guanjin/register-token/v1\n" + payload_b64url)`，校验**常量时间**比较。`val` 必须走**与 `identifiers` 落库同一套归一化**（单一来源，不许另写一个）。
  - **密钥**：新增用途域 **`guanjin/hmac/register`（32B）**——D4-B 冻结集合的**本步 5 → 6**；**当前累计 5 → 8**（ADR-005 再追加 `hmac/field` / `hmac/keywrap` 两个完整性域，全量清单与生效范围见 **D15③**）。`Purpose` 仍是封闭集合，新增只由本台账决定，调用方**不得**自造 domain separator。**不得复用 `hmac/identifier`**——"一密钥一用途"是密评口径，复用即自废。
  - **绑定（安全要点，必测）**：令牌必须同时绑定 `client_id` + `type` + `value` +（有挑战时）`challenge_id`。缺任何一项都能构造"用自己手机的验证码把别人的号码标成 `verified=true`"（账号接管级缺陷）。
  - **`complete` 校验顺序**：①解析/验 MAC 失败 → `INVALID_PARAMETER`（E1/400）；②`exp` 已过（**以 DB 时间判定**，与 `challenges`/`flows` 同一个钟）→ `CHALLENGE_EXPIRED`（E2/401）；③有 `chid` → 该挑战必须存在、`purpose='register'`、`client_id` 与令牌一致、未消费（已消费 → `CHALLENGE_CONSUMED`）、未过期；④无 `chid` → **按令牌里的 `client_id` 重新校验准入与标识符唯一性**（TOCTOU 必须复查，不能只信 `start` 那次的结论）。
  - **一次性**：有 `chid` 的走 `challenges.consumed_at` 行级原子消费（与建号同一事务）；skip 分支没有挑战行，可重复提交，但由 `identifiers` 唯一性兜住（第二次必然 `IDENTIFIER_ALREADY_BOUND`），**不为此新增 Redis 键**（§5.9：Redis 不作挑战状态权威源）。
  - **对外契约面**：`register_token` 对客户端是**不透明串**（原样回传），形态属内部实现 → **不登记 CH**，将来换随机句柄客户端无感。**但必须脱敏**：与密码/验证码同级，**不得进访问日志**（令牌内含标识符明文）。

- **③ 邀请码：本刀"显式拒绝"，不静默忽略**
  - 现状：全库**没有**邀请机制（无 invite 表，`clients`/`login_policies` 无字段），因此**不存在合法邀请码来源**。本刀实现：`invite_code` **缺省或空串** = 未使用；**任何非空值 → 400 `INVITE_CODE_INVALID`**（E1；已在 D3-B″ 补登记），且判定在**建任何行之前**（`users`/`identifiers` 零新增，满足 `AC-REG-08`）。
  - **判据账目（必须说实话）**：`AC-REG-08` 的"无效"分支本刀可举证；"已使用 / 过期"两分支**当前不可达**（无机制 → 无"已使用/过期"的码）。Gate 在账目里标注"分支不可达（机制未落地）"，**不得**标 PASS 后又说三条分支都验过。邀请机制（表/字段/准入规则）**推 M2+**，届时本裁定作废、重新出条。
  - **错误顺序（禁隐性）**：`MISSING_PARAMETER`（缺 `client_id`/`type`/`value`）→ `INVALID_CLIENT` → `REGISTRATION_NOT_ALLOWED`（准入）→ `INVITE_CODE_INVALID` → `IDENTIFIER_INVALID_FORMAT` → `IDENTIFIER_ALREADY_BOUND` → 限流类（`RATE_LIMITED`/`BUDGET_EXCEEDED`/`CAPTCHA_REQUIRED`）。**准入在邀请码之前**：准入是 client 自身属性（不泄露用户信息），先判它不会掩盖 `INVITE_CODE_INVALID`（`AC-REG-08` 用例用开放注册的 client；关闭注册的 client 走 `AC-REG-09`）。
  - **归属**：Core（`internal/register`）；Gate 按上条改账目口径。

- **④ `attempts_left` 的口径（两处端点、两个计数，不许混用）**
  - `POST /auth/challenge/{factor}`（§3.2.1）：`attempts_left` = **单挑战**剩余次数（`challenges.max_attempts - attempts`），**不得**用账号级 `login:fail:{user_id}` 的剩余次数顶替（D3-D 明写两个对象）。**无状态挑战**（口令/TOTP 不落挑战行）回 **0 = "该维度不适用"**，不是"没机会"。
  - `POST /auth/register/start`（§3.3.1）：`attempts_left` 是**新增字段**（§3.3.1 原响应要点为 `{register_token, challenge_id?, next_action, target_masked?, expires_at}`，无此键），值 = 该注册挑战的 `max_attempts`（来自 `runtime_config.challenge.max_attempts`）。登记为 **CH-8（纯加字段，待追认）**；**追认前不得作为判据依据**——`AC-REG-01` 仍按 DB `challenges` 行断言。
  - **账号级剩余次数（`fail_threshold - count`）一律不对外暴露**；要暴露必须新增字段并另立 CH，**不得复用同名键**。

- **⑤ 落地登记（跨模块代落，只登记、不新开 D 条）**

  | 模块（归属） | 新增 / 改动 | 为什么必须在这里 | 复核 |
  |---|---|---|---|
  | `internal/store/runtimeconfig.go`（Edge） | `KeyChallengeTTLSeconds` / `KeyChallengeMaxAttempts`、`RequiredPositiveInt`、`LoadChallengeTTL` / `LoadChallengeMaxAttempts`、`StartupKeys()` 含 3 键、`ValidateStartup` → `StartupConfig{FlowTTL,ChallengeTTL,ChallengeMaxAttempts}` | ①的运行期参数单一读取口径；`max_attempts` 是次数，**不能**借 `RequiredIntSeconds`（会变 5ns） | Edge（main `abde6e0`） |
  | `internal/crypto/purpose.go`（Edge） | `PurposeHMACRegister` = `guanjin/hmac/register`（32B），用途域本步 5 → 6（**累计 5 → 8**，见 D15③） | ②的独立用途域（一密钥一用途） | Edge（main `abde6e0`） |
  | `internal/store/repo_client_policy.go`（Edge） | `LoadLockoutPolicy(ctx, clientID)`：`client_override` 优先 → 同档 `tier_default` 回落 → 两侧都没有 = `ErrPolicyNotConfigured` | 失败阈值/锁定时长只能来自 `login_policies`，代码里不留 5 / 15 | Edge（Core 在 #32 代落，main `8cd751a`；未反对即默认通过） |
  | `internal/store/db.go`（Edge） | `DBNow(ctx, db)`（`SELECT now()`，返回 UTC） | 权威时钟：挑战过期、注册窗口与 `flows` 用同一个钟（多副本时钟漂移会让同一瞬间得出不同结论） | 同上 |

  > 后续同类"为别的模块代落一个窄读取"一律**追加到本表**（格式不变），不新开 D 条、不改 `docs/baseline/`。

- **报备（不需裁定，随本条记录）**：`ACCOUNT_LOCKED` 在接口 §5.4 **E2 组**（→ HTTP **401**）有登记，`internal/httpx` 码表漏收 → Core 补登记并按 **E2/401** 映射；**不得**改挂 E5（那是 `ACCOUNT_TEMP_BLOCKED` 的 429 风控语义）。

---

## D15 SM4 信封 v2（enc-then-MAC）与"完整性失败"的对外码（批 ADR-005；用途域累计 5 → 8）

> 触发：`crypto.EncryptSM4` 原格式 `iv(16) || SM4-CBC(PKCS7)` **只有加密、没有完整性**，`DecryptSM4` 唯一校验是 PKCS#7 填充。后果不是"少一层防护"，而是**故障被误分类**：错钥/篡改约 **0.36%**（本机 18/5000）padding 恰好合法 → 返回乱码明文 → 下游把"密钥不可用"报成"凭据不对"，并让 CI **随机刷红**（同一 commit 重跑即绿，两次命中不同包的不同用例）。

- **① 格式 v2（唯一受支持）**：`0x02 | iv(16) | SM4-CBC(PKCS7(明文)) | HMAC-SM3(32)`，`mac = HMAC-SM3(K_mac, 0x02 || iv || ct)`——**版本号也进 MAC**。校验顺序固定：**先验 MAC（常量时间）再解密**，不返回半截明文。
- **② v1 一律拒绝**（`ErrCiphertextVersion`）：**不设"没有 MAC 就按 CBC 解"的兼容读路径**——留兼容等于把完整性做成可选项（删掉 MAC 段就降级）。
- **③ 用途域累计 5 → 8（唯一口径，以代码为准）**——追补 D4-B 的冻结集合。
  - **事实来源**：`internal/crypto/purpose.go` 的 `Purposes()`（main `7ea144c` 核对，2026-11-04）；`internal/crypto/doc.go` 同步写「8 个用途域」。**本条是本文档对用途域条数的唯一口径**；文中出现的「5 → 6」「6 → 8」只作分段历史，**当前值一律 = 8**。
  - **沿革**：D4-B 冻结 **5 个**（`sm4/field`、`sm4/keywrap`、`hmac/audit`、`hmac/identifier`、`hmac/agent`）→ **D14②** 追加 `hmac/register`（**5 → 6**，注册窗口令牌 `register_token`）→ **ADR-005** 为 SM4 信封配套追加 `hmac/field`、`hmac/keywrap`（**6 → 8**，enc-then-MAC 的 MAC 键）。两段增量合计 **5 → 8**。
  - **冻结清单（按代码 `Purposes()` 顺序，成对的加密/完整性相邻）**：

    | # | 用途域 | 长度 | 用途 | 引入 |
    |---|---|---|---|---|
    | 1 | `guanjin/sm4/field` | 16B | 敏感字段加密（`identifiers.value_enc` 等） | D4-B |
    | 2 | `guanjin/hmac/field` | 32B | 上条信封的完整性（MAC 覆盖 `版本‖iv‖ct`） | **ADR-005（新增）** |
    | 3 | `guanjin/sm4/keywrap` | 16B | 密钥封装（`signing_keys.private_key_enc`） | D4-B |
    | 4 | `guanjin/hmac/keywrap` | 32B | 上条信封的完整性 | **ADR-005（新增）** |
    | 5 | `guanjin/hmac/audit` | 32B | 审计链 `audit_logs.integrity_tag` | D4-B |
    | 6 | `guanjin/hmac/identifier` | 32B | 标识符盲索引（`identifiers.value`） | D4-B |
    | 7 | `guanjin/hmac/agent` | 32B | Agent 面 HMAC（`clients.secret_hash`） | D4-B |
    | 8 | `guanjin/hmac/register` | 32B | `register_token` 签名（`cid+typ+val+chid+exp`） | D14② |

  - **为什么两把 MAC 键不合并**：两把 MAC 键**各自直接由主密钥按本用途域派生**（HKDF-SM3，`salt = info = purpose`），**不是**从 SM4 键再派生；两个 MAC 域也不合并——"被包裹的私钥密文挪进 `value_enc` 必须验不过"，靠的正是两把 MAC 键互不通用。用途表另有编译期一致性自检（`validatePurposeSpecs`）：每个 SM4 用途**必须**有配套 32B HMAC 用途，否则接线时就会报错（不允许出现"某条加密路径忘了带完整性"）。
  - **与 ADR-005 的关系**：这两个域是 ADR-005「先验 MAC 再解密」的组成部分，**不是**独立扩展；ADR-005 状态已由 `Proposed` 改 **`Accepted`**（2026-11-04，接受依据 = Owl 追认 #33 无阻断 + 代码现状已按该 ADR 落地），接受不改其已定内容。
  - **生效范围（冻结 ≠ 已接线）**：8 个域全部是封闭集合成员，未知 `Purpose` 一律报错；**装配期当前实际派生 4 个**——`cmd/guanjind/wiring.go` 三处调用：`DeriveHMAC(hmac/identifier)`、`DeriveSM4(sm4/field)`（该调用同时派生配套的 `hmac/field`）、`DeriveHMAC(hmac/register)`。其余 **4 个已冻结、尚无调用点**，按各切片接入：`sm4/keywrap`+`hmac/keywrap` → S6（签名私钥解封）、`hmac/audit` → S10（审计链）、`hmac/agent` → Agent 面（`secret_hash`）。**"没调用点"不等于"可用别的域替代"**。
  - **变更成本**：8 个域同由一把主密钥派生，主密钥不变则派生值不变；**轮换任一域**（尤其 `hmac/field`/`hmac/keywrap`）会使对应历史密文不可读，须走 ⑤ 的一次性重加密口径。历史的 v1 密文（`iv‖ct`）不属本清单管辖，一律按 ② 拒绝。
- **④ 完整性失败的对外码（Edge 问的那条，定死）**：

  | 情况 | 内部哨兵 | 对外 | 为什么 |
  |---|---|---|---|
  | 该用途域的键**压根没装配/为空** | `ErrKeyMissing` | **503 `KEY_UNAVAILABLE`** | 这是"依赖/配置暂不可用"，修好键即可恢复 → 503 的"稍后重试"语义成立 |
  | 键可用但 **MAC 不通过** | `ErrIntegrityMismatch` | **500 `INTERNAL_ERROR`** | 密文来自我们自己的库：不是客户端参数错（**不 400/422**），重试永远不会成功（**不 503**，否则造成重试风暴并掩盖数据损坏）；必须打日志 + 计数指标（`crypto_integrity_failure_total{scope}`）并告警 |
  | 版本字节非 `0x02`（含 v1 存量） | `ErrCiphertextVersion` | **500 `INTERNAL_ERROR`**，日志点名"存量密文未重加密" | 同上；日志必须指向运维动作（重灌 / 一次性重加密任务），否则只能看到"5xx 变多了" |
  | **启动期**打不开（如 `signing_keys.private_key_enc`） | 上述任一 | **非 0 退出**（不是 HTTP 码） | D5-₂ 三态口径：数据/配置坏了就起不来，不带着坏密钥对外服务 |

  **禁止**把 MAC 不过报成 400/422（前端会去"改参数"）或 503（伪装成可重试）。
- **⑤ 存量口径**：项目未上线（P1）→ **不做双读、不做自动重加密**，本地与预发库重灌；上线后轮换走一次性重加密任务，**不引入 v1 读路径**（任何"灰度期同时接受 v1/v2"须另出 ADR）。
- **落地**：**Edge**（实现 + 变异用例，main `18ed08e`）；**Core**（仅注释同步：`internal/identifier/blindindex.go`、`internal/credential/totp.go` 里"含 IV 前缀"的表述改为"含版本/IV/MAC"）；**Gate**（`AC-AUD-12` 补**反向用例**"篡改任一字节必拒"，当前只断言算法名；再加一条"MAC 失败 = 500 而非 503"）；**Ark**（**已完成 2026-11-04**：`docs/adr/ADR-005-SM4信封加密后MAC.md` 状态改 **Accepted**（依据 = Owl 追认 #33 无阻断 + 代码现状）+ 本条 + 本文档用途域口径统一为 **8**，见 ③）。
- **依据**：`数据模型 §3.3/§3.7`、D4-B（一密钥一用途）、`AC-AUD-12`（应用侧国密实证）、ADR-005。

---

## D16 数据面（S8）三处口径收口：断言 claim 来源 / 准入判定基准 / 路由规则冻结（2026-11-10，团队收口）

> **修订记录（2026-11-10，三次改口，取代关系写死）**：
> ① **`jti` 二次判定**：初版写「`AC-DP-08` 是 SDK 侧用例」（暗示整条移出 S8）→ **作废**；二版按「双列 S8/S9 + 平台不落库」重写 → 仍偏"整条归 SDK"；**现行表述见 ①「`jti` 二次判定的落点」= Spec 裁定「各判一半」**（S8 只证每请求现签新 `jti`、被拒零注入；同 `jti` 二次的 401 由验签方产生；平台零 Redis、零 `jti` 存储）。
> ② **未匹配路由码**：初版「503 `UPSTREAM_UNAVAILABLE`」→ **作废**（与 **D8-C′④** / `AC-DP-13` / `sdk-contract` C-9 冲突：该码只给"已开始转发但上游失败/超时"）；二版「404 `INVALID_REQUEST`」→ **作废**（**更正事实**：§5.4 分级表的 **E1 区间本就是 400/404**，选它**不需要**任何「接口可覆盖」的例外——上一版把这个事实写反了）；**现行表述见 ③ = 404 `CLIENT_NOT_FOUND`**（沿用既有码、扩充使用语境，与 Edge 已落实现一致，少改一处）。
> ③ **`AC-DP-12` 记账**：初版拆出 `PENDING·R12` 但未同步判据 → **作废**（替判据改账目，越权）；**现行表述见 ④**：判据文件一字不改（`PASS-able`），只有 SLO 通知的 wire 形态「待 R12 裁定」，候选 A 下 `acceptance-criteria.md:188` 亦不改。
>
> **触发**：S8 基座已落 main（`fea9d1e`，#50：转发 + 静态路由 + 先弃同名头 + 上游失败语义）。余下四件事——验签+会话校验、按档位判准入、逐请求签 `X-GuanJin-Assertion`、实时撤销（S8·2）与单点登出通知（S8·3）——在基线里只写了「要有什么」，没写「从哪取、和谁比、冲突怎么办」；**R11① 亦登记为"只登记、本轮不实现"**。不写死就是让三个人各猜一套：Edge 照猜实现、Nova 照猜验签、Gate 照猜断言，最后 `AC-DP-01/09` 三方口径必打架。本条把**能从基线唯一推出的部分**冻结，把**推不出来的部分**如实登记为待裁（§④），不自造 wire 名。

### ① 断言 claim 的取值来源（逐项，唯一口径）

| claim | 取值来源 | 硬约束 |
|---|---|---|
| `iss` | 常量 **`guanjin-gateway`** | 接口 §5.2 冻结；SDK 侧按此值校验（不一致 → 401） |
| `sub` | 访问令牌的 `sub`（= `users.id`） | **不重新解析标识符、不换成用户名**（用户名可改，`sub` 不可变） |
| `aud` | **路由表命中的 `client_id`**（目标业务系统） | 转发前必须先断言 `token.aud == route.client_id`，**不等即 401 `TOKEN_INVALID`**（AC-DP-07）；**禁止**"用 token 的 `aud` 覆盖路由命中结果"（那等于让令牌决定去哪） |
| `cid` | 访问令牌的 `cid`（发起 client） | 会话按 client 建立（`sessions.client_id`），正常路径下 `cid == aud`；**不等同 401**，不做"跨系统代持" |
| `uname` | `users.username`（规范化小写存储值） | **不取 `display_name`**：断言是身份凭据，不是展示面 |
| `roles` | `role_bindings` 中 `(user_id, client_id = aud)` 且 `status='active'` 的 `role` 集合 | 空集 → **`[]`（键必须存在）**，不得省略、不得填 `null`；平台只搬"用户→角色"绑定，**角色→权限定义与判定在业务方**（索引 §2） |
| `acr` | `sessions.acr`（白名单 `AAL1|AAL2`，D12） | **不从令牌取**：撤销/降档以库为准；越界值按内部缺陷 500（D12(3)），不静默截断 |
| `amr` | `sessions.amr` | 同上；空数组照发（`[]`，不省略） |
| `iat` / `exp` | `iat = now`，**`exp = iat + 60`** | 接口 §5.2 / 索引 §4.2-23；**60s 是防重放窗口，不是会话生命周期**，不得"续期"或"沿用上一次" |
| `jti` | 每请求新建 **UUIDv7**（与应用主键同一生成器） | 见下条「`jti` 二次判定的落点」——**平台侧不落库不比对、判定在验签方**（Spec 裁定 2026-11-10） |

- **`jti` 二次判定的落点（`AC-DP-08`，Spec 以基线释义负责人身份裁定 2026-11-10：「各判一半」）**：
  - **S8（平台侧）只证「每请求现签新 `jti`、被拒零注入」，不产生「同 `jti` 二次 → 401」**：`jti` 每请求新建、`exp=iat+60`、重试转发复用同一个 `jti`；平台侧**零 Redis、零 `jti` 存储、零比对**。
  - **「同 `jti` 二次 → 401」由验签方（SDK / S9）产生**：`GJ-CAP-07` 用 `replay-store=redis`（D6(5)①）在窗口内判重。证据链：`数据面设计:225-229`「逐请求签发 / 60s 只作防重放窗口」+ `:233-237`「**SDK 侧要做的事（5 步）**」第 3 步「校验 `exp`，并用 `jti` 做重放检测」+ `SDK契约:66` 第 7 项「重放检测（`jti`）·必须」+ `接口清单:681`（内部令牌使用约束）+ `m1-slices.md:73`（S9 介质列「无（SDK 侧本地 `jti` 落 Redis，**不碰平台库**）」）+ `数据模型规格` 全文 `jti` 零命中（平台无落库位置）。
  - **为什么平台侧判重在语义上不成立**：平台是签发方，每条断言的 `jti` 都是它刚生成的新 UUID —— 判重判的是自己刚写出的值，正常流量永不命中，只有构造用例能"证"出第二次 401；且按 D10 六步，断言的「读」发生在验签方（平台永不接收断言）。
  - **判据文件不改**：`acceptance-criteria.md` 的状态列仍记 `PASS-able`（**第 184 行**，`AC-DP-08` 那一行；第 179 行是 `AC-DP-01`）、归属仍双列 S8/S9（第 80–81 行）；**账目按 Gate 的拆分口径记**：S8 半绿（平台侧举证通过）/ 整条待 S9。完整依据（逐条行号）见裁定稿 **`notes/ac-dp-08-归属裁定-2026-11-10.md`**（代码注释引用的就是这条路径）。
- **同一请求只签一次**：`iat/exp/jti` 一次生成、整条链路复用（重试转发不得重签：重签会换 `jti`，等于把防重放窗口清零）。
- **`kid` 必须发**：取自 `signing_keys` 中 `purpose='gateway'` 的活动行（`signing.PurposeGateway`），与用户令牌的 `oidc` 密钥**分用途**（D4-B）；未知 `kid` 只可能来自密钥轮换，SDK 侧按 `GJ-CAP-03` 强刷一次 JWKS。
- **断言绝不进日志**（`AC-DP-10`）：访问日志只允许 `trace_id` / `method` / `path` / `status` / `duration_ms` / `client_id`。

### ② 准入判定基准（`AC-DP-09` 的"档位不足"到底跟谁比）

- **比较对象**：`sessions.acr`（当前会话已达成） vs **目标业务系统的门槛**。
- **门槛的取法（唯一）**：`login_policies` 里 `scope='client_override' AND client_id=<路由命中>` 优先 → 回落 `scope='tier_default' AND tier=<clients.tier>` → **两侧都没有 = `ErrPolicyNotConfigured`**，按**配置缺陷**返回 **500 `INTERNAL_ERROR`**（客户端没传错东西、重试不会成功；同 D15④ 的判据），**不得在代码里垫一套默认值**（那是第二套口径）。
- **为什么不拿 `flow.TargetACRForTier(tier)` 再算一遍**：`min_acr` 是**存储态**、运维可改（D13 的幂等口径就是"不覆盖运维改过的值"），而 `TargetACRForTier` 是**档位→级别的映射函数**（L1|L2→`AAL1`、L3→`AAL2`，`000017` 的 seed 与之逐档一致）。数据面**只认 `min_acr` 一个真值**；两者打架时是配置漂移，不是"取小的那个"。
   > **留给 Core 的一条**（不在 S8 内）：`flow.Begin` 目前用 `TargetACRForTier(clients.tier)` 作目标级别，而数据面用 `min_acr`。运维改过 `client_override.min_acr` 时两者会分叉；M1 不改（seed 一致、无运维改值），**M2 前由 Core 统一到 `min_acr`**——已登记，别在 S8 里顺手改 Core 的代码。
- **不足时的响应**：**403 `ACR_INSUFFICIENT`**（E3）+ `details.flow`（Descriptor），**不是 302、不是 500、不是 200**（`AC-DP-09`）。
- **数据面对 `flows` 的权限 = 只读**：S8 的落点表只有 `sessions` / `user_visited_clients`(写) / `clients`(读) / `auth_events`，**没有 `flows` 写**。故 `details.flow` 只能**读现成活跃流并渲染**（复用 `internal/flow` 的 `DescriptorOfFlow` / `BuildDescriptor`），**不得**在数据面新建流；没有活跃流时给出 `terminal_state='need_more'` 的 steps，客户端按 CH-1 先 `GET /authorize/flow` 取 `flow_id` 再 `POST /auth/challenge/{factor}`（`step_up` 必带 `flow_id`）。
- **`user_visited_clients` 的写时机**：请求**通过准入之后**才 upsert（`tier_at_visit` 记目标 client 的 `tier`、`visit_count += 1`、`last_seen_at = now`）；被 401/403 拒的请求**不写**（否则被拒的探测会污染"已访问"与生效配置）。

### ③ 路由规则冻结（R11① 的规则部分，与 `fea9d1e` 的实现一致；容量部分仍待 spencer）

| 维度 | 冻结口径 | 理由 / 判据 |
|---|---|---|
| 匹配依据 | **路径前缀**（`route → client` 静态映射，索引 §4.2-18）；域名匹配**不属本平台**（前置 nginx/Kong 承担，P2②） | 数据面只做认证语义，域名分流是前置组件的"粗路由"（数据面 §1.2） |
| 前缀形态 | 必须以 `/` 开头、**不以 `/` 结尾**（根路径 `/` 例外）；上游只接受 `http`/`https` 绝对 URL | 装配期校验，非法即**起不来**（fail-closed），不"跳过坏的那条继续跑" |
| 匹配粒度 | **按路径段**：`/api/bill` 命中 `/api/bill` 与 `/api/bill/x`，**不命中** `/api/billing` | 字符串前缀会把两个业务系统混成一个 = 拿 A 的流量用 B 的身份转发，属安全事故 |
| 冲突判定 | **重复前缀 = 装配期报错**（不"后者覆盖前者"、不按顺序取第一个） | 静默覆盖会让某个业务系统在运行期忽然 503，日志里看不出是配置写错 |
| 优先级 | **长前缀优先**（`/api/billing` 先于 `/api`） | 与"粗路由在前置"一致；短前缀是兜底不是例外 |
| 未匹配 | **404 `CLIENT_NOT_FOUND`**（E4，`httpx.HTTPStatus` 默认即 404），**不裸转发、不回落默认上游**；同时打 WARN（含 `trace_id` + `path`）并计数，让路由表漂移可见 | ①**不再用 `UPSTREAM_UNAVAILABLE`**：按 **D8-C′④**（`decisions.md:203`）与 `AC-DP-13`（`acceptance-criteria.md:189`），该码只给「**已开始转发**但上游失败/超时」；未匹配时一次转发都没发生，套用它是把「配置缺路由」错报成「上游挂了」（也违 `sdk-contract` C-9「`UPSTREAM_UNAVAILABLE` 是数据面转发失败专用」）。②**码名取 `CLIENT_NOT_FOUND`**：§5.4 既有码里没有「路由缺失」专用码，**F0 禁自造码名**，只能在既有码里选。③**`CLIENT_NOT_FOUND` 的语义边界（对既有码的「使用语境扩充」，不是新增码名）**：基线里该码只在**管理面按 `client_id` 定位**时使用（`接口清单` 的管理面语义）；数据面复用它属**扩充使用语境**，码名与分级（E4/404）**一个字没动**。分工写死：`CLIENT_NOT_FOUND`（E4/404）**仅**用于「`route → client` 静态映射缺失」（打 WARN + 计数），**不得**当通用 `NOT_FOUND` 使用；其余「查不到」一律仍用各自专职码——`USER_NOT_FOUND` / `KEY_NOT_FOUND` / `SESSION_NOT_FOUND` / `FLOW_NOT_FOUND`（及 `SESSION_NOT_OWNED` / `BINDING_NOT_FOUND` 各自的既有语境）。`INVALID_REQUEST`（E1/400）**留作请求本身不合法**（畸形体 / 非法参数 / 协议层不合法），**不用于路由缺失**，也不得用它替代上列任何一个「*_NOT_FOUND」。令牌/会话类失败仍走 `TOKEN_INVALID` / `TOKEN_EXPIRED` / `SESSION_REVOKED` 等 E2/E3 码，一律不落到 404。④**为什么不是 `INVALID_REQUEST`**：结论保留（**既有码零自造**）、但理由换成——**与已落实现一致、少改一处**（`#54` 的 `proxy.go` 已按 E4/404 落 `CLIENT_NOT_FOUND`），且 `INVALID_REQUEST` 语义是「请求本身不合法」，用它会把「路由表少一条」这种**部署漂移**记成调用方参数错、诱导调用方改写重试。（注：§5.4 的 E1 区间本就含 404，**不存在**「必须靠接口覆盖把 400 改 404」的问题——上一版这句话是错的，作废。） |
| 路径改写 | **不改写**：上游看到的路径 = 客户端发出来的路径（前缀只用于匹配，不剥） | D1-A1（`PATH` 两端逐字节一致）的同源纪律；剥前缀会让上游的相对链接与签名口径同时错 |
| 容量目标 | **R11② 仍待 spencer**（数据面全量转发 QPS / 连接数基线未定，与 R8 合并出结论） | 本表只冻规则，不冻容量；不得用压测缺位当"已定"引用 |

### ④ 推不出来的两处：**不自造，只登记**

**状态（2026-11-10 更新）**：R12 **已裁并落地**（候选 A，见 CH-9 待追认）；R13 **已裁**（判据不涉及该键，实现沿用 `omitempty`）。以下是两处**当时**的契约空洞记录，保留原文只作沿革。原始状态：`PENDING`（待裁，不由架构定型）。S8 还剩两处基线没给、也不能由团队自行收口的契约空洞——**SLO 通知的 wire 契约（R12）** 与 **`details.flow` 在「无活跃流」时的字段序列化形态（R13，CH-2 未规定）**。自造端点名 / 新 claim / 新字段都属新增契约面（F0 禁自造 wire 名），故本条**不裁定**，权威落点见文末「残余待裁」表的 **R12 / R13** 两行（唯一出处，此处不复写，避免两套说法）。

- **`AC-DP-12` 三方口径（`decisions.md` / `acceptance-criteria.md:188` / D16）统一为一条**：**判据文件一字不改**，状态仍 `PASS-able`；本文件**不再出现 `PENDING·R12` 的记账写法**（原先那条拆分作废——它等于替判据改账目，越权）。
- **唯一待裁的是 wire 形态**：SLO 通知的**路径 / 报文 / 事件名**一律「待 R12 裁定」，本条与 `m1-slices.md` 都不得预设。若 R12 取**候选 A**（零新表零新列、复用既有 SM2 签名机制与既有 claim 集），则 `acceptance-criteria.md:188` 亦**一字不改**，平台按裁定实现即可；取 B 才需要动 `clients` 加列 + 新表（撞 D5 的 15 表冻结）并走迁移。
- **裁定前的实现边界**：平台侧只允许「复用既有 SM2 签名机制 + 既有 claim 集」，**不得自造**路径 / 报文 / 事件名；举证范围按判据原文（「通知失败不阻塞本端登出、本地会话必须已撤销」这一半在任何方案下都可证），送达/验签那半边等 R12。

### ⑤ 落地

- **Edge**（S8·2/S8·3）：按 ①②③ 实现验签+会话+准入、断言签发、实时撤销、SLO 通知；`store` 侧新增一条**窄读取**（按 client 读 `min_acr`，与 `LoadRegistrationPolicy` 同构、同错误语义）属**你自己的包**，无需登记代落。**一处跟随改动**：未匹配路由按 ③ 改回 **404 `INVALID_REQUEST`**（`fea9d1e` 的 `internal/gateway/proxy.go` 现为 503 `UPSTREAM_UNAVAILABLE`，一行 + `proxy_test.go` 的 no-route 用例同步）。
- **Gate**：`AC-DP-01` 的「claim 齐全」按 ① 逐 claim 断言（含 `roles=[]` 不得省略、`exp-iat==60`）；`AC-DP-09` 的门槛取法按 ②；`AC-DP-08` 按 ① 「`jti` 二次判定的落点」验收（S8 只举证：每请求现签新 `jti`、被拒零注入；同 `jti` 二次的 401 由 S9 产生），账目按拆分口径记 S8 半绿 / 整条待 S9，**判据文件不改**；`AC-DP-12` 按 ④：判据不改，R12 未裁前**不接受**自造 wire 形态。
- **Nova**：SDK 侧验签按 ①（`iss` 常量、`aud` 必须等于本系统 `client_id`、`roles` 空数组合法）；`AC-DP-08` 的 `jti` 窗口 = `exp`。
- **Ark**（本步）：本条 + `AC-DP-11` 的可执行举证 `scripts/check-dataplane-boundaries.sh`（含 `--self-test` 的注入必红），已接进 `make gates` 与 CI 的 `go` job。

---

## D17 能力矩阵与 AC 账目的计数单位（22 能力 vs 90 判据，不得换算）—— 2026-11-10，Spec 核准表述

> **触发**：一份 SDK 裁定稿曾把 `GJ-CAP-17`/`20/21` 写成"已计入 `acceptance-criteria.md:45` 的 6 条 `NOT-APPLICABLE · M1`"。该说法经 **产品 / 测试 / 实现三方独立核对** 为错，并已差点被抄进 PR 正文与仓库文档。根因是**两套计数单位被混用**：`GJ-CAP-xx` 是**能力编号**（22 条），`AC-*` 是**判据条目**（90 条）；两者不是同一单位。本条是**唯一的记法出处**（其他文档只允许指过来，不得另写一份）。

- **事实**：`GJ-CAP-17`/`GJ-CAP-20/21` **无任何 AC 条目**。`acceptance-criteria.md:45` 的 6 条 `NOT-APPLICABLE · M1` 是 `AC-FLOW-08`(M2)、`AC-AUD-09`(M2)、`AC-AUD-08`(M5)、`AC-AGENT-01~03`(M2 前置)——**全是 AC 条目、无能力编号**；`GJ-CAP` 在 `acceptance-criteria.md` 全文只见于 **`:332`**（D11 的 `GJ-CAP-13`）与 **`:379`**（「22 条能力签名…不验本地授权判定模型 → M5」）。
- **记法（能力矩阵，22 条）**：`GJ-CAP-17` = **`DEFERRED（M5）`**；`GJ-CAP-20/21` = **`N/A / DEFERRED（M2 前置）`**。依据 `acceptance-criteria.md:379`/`:380`；「Agent 面 `report`/`policies` 判据归 M2」在 `m1-slices.md:72`。
- **不得双计**：`GJ-CAP-20/21` 对应的 AC 就是 `AC-AGENT-01~03`，这 3 条**已在 6 条 `N/A` 内**，AC 账目已计过一次，**能力矩阵不得再计**。
- **不得相加、不得互相换算**：22 条能力 ≠ 90 条判据。`AC-ENG-05` 的账目 **`90 = 83 PASS-able / 0 PENDING / 0 BLOCKED / 6 NOT-APPLICABLE / 0 NOT-IMPLEMENTED / 1 后置（DEFERRED）`** 与 `GJ-CAP-17/20/21` **无关、不受影响**。
- **三套计数不得互换**：22 条能力（`sdk-contract` / `GJ-CAP-*`）、90 条判据（`acceptance-criteria.md` / `AC-ENG-05` 账目）、65 个具名端点（+1 数据面通配，`D3-C`）分别计数、不得相加或互相换算。
- **落地**：**Gate**（S11 收口材料按本条记：90 行逐条给结论与证据指针；6 条 `N/A` 必须带"为什么 N/A + 依据行号"；`AC-DP-08` 两半合并记一次、不拆行凑数）；**Nova/Edge**（写 PR 与文档时不得把能力编号说成 AC 条目）；**Spec**（后续裁定稿引用本条，不另起表述）。
- **本条不改判据文件**：`acceptance-criteria.md` 一字未动（`90 = 83/0/0/6/0/1` 原样），也不改任何能力定义。

---

## D19 概念名 vs 物理列：`disabled_factors` 的权威落点 = `login_policies`（2026-11-11，实现与判据不变，只登记措辞）

> **触发**：Spec 实读基线时发现**既有文本冲突**——`接口清单:211`（`GET /authorize/flow` 响应要点）写「`disabled_factors` 来自 `clients` 的"禁用因子"」，而数据模型的 DDL 把该列放在 `login_policies`（`数据模型:770`）。**本条只登记权威判定与影响面：不改基线正文、不改代码、不动判据、不动账目。**（编号说明：**D18** 由 draft PR #72「无 client 上下文时 `ACR_INSUFFICIENT` 不带 `details.flow`」占用、待 spencer 拍板，本条顺延为 **D19**，不重排、不占 D18。）

- **一句话结论：不是设计变更，是接口文档的措辞滞后（笔误）；物理落点 = `login_policies.disabled_factors`，实现与判据均以它为准，不改判。** 依据全在基线内，且直指这一条：
  1. **索引 §2-16（`设计基线索引:130`，本文裁定权威顺序的第 ① 层）**：「门槛项的存储位置 = 门槛项落 `login_policies`；`clients` 只存 `tier`」。
  2. **数据模型 §3.9（`数据模型:734`/`:739`）**：`login_policies` 的职责原文是「把预设档位清单第三节 A~E 的门槛项落成数据」；`disabled_factors` 就在该表（`:770`），语义注（`:805`）同节。**`clients` DDL（`:684`）没有任何 `disabled_factors` 列** → 接口那句在数据契约里无落点。
  3. **数据模型 §4 的 I9（`:1042`）**：「门槛一律查 `login_policies`」——同一条纪律的另一种写法。
  - **实现已一致**：`migrations/000012_create_login_policies.up.sql:36`；读取 `internal/store/repo_admission.go`（`client_override` 优先 → 同档 `tier_default` 回落 → 两侧都无即配置缺陷）；Descriptor 字段注释 `internal/flow/descriptor.go:129`、`internal/flow/orchestrator.go:147`。
  - **接口那句的正确读法**：`clients` 是**概念归属**（"按业务系统配置"），`login_policies` 是**物理落点**（`scope='client_override'` 行；缺行回落同档 `tier_default`）。管理面 `GET/POST /api/v1/clients` 的 `disabled_factors` 字段外形（`接口清单:449`/`:455`）**不算冲突**：那是 client 资源的投影，后端由 `login_policies` 供数，对外形状零变化。

- **影响面（三处，结论都是"不变"）**
  1. **前端**：Flow Descriptor 的 `disabled_factors[]` 由平台计算下发，`页面规格 §4.1` 的 TS 类型与 `web/src/types/flow.ts:47` **一个字不改**；管理后台"客户端页"（A5）编辑的 `disabled_factors` 落 `login_policies.client_override`，界面与接口字段均无变化。**`页面规格:533` 的注释（"来自 `clients` 的禁用因子"）与 `接口清单:211` 是同一处笔误**，同批对齐。
  2. **测试断言**：`AC-FLOW-04`（`acceptance-criteria.md:135`：不进 `steps[]`、不计入 `current_acr`、直发 → `FACTOR_DISABLED`）、`AC-FLOW-07`（`:138`：全禁 + 目标 AAL2 → 200 + `terminal_state='unreachable'`）以及 Spec 已按本条完成的 `AC-FLOW-05`/`AC-FLOW-09` 判据，全部建立在 `login_policies` 上；夹具要直改库必须写 `login_policies`（`scope='client_override'`），写 `clients` 无列可写。**若改判到 `clients`**：需新迁移加列 + 改 store/flow 两处读取 + 上述断言整套失效 → **不采用**。
  3. **接口文档**：只有 `接口清单:211`（+ `页面规格:533` 同一处注释）的措辞需更正；`数据模型` 正文不动。

- **平台级 ≠ 本条（顺手纠正一处转述）**：`AC-FLOW-05` 里的「平台级开关」**不在** `login_policies`——基线把它落在 **`runtime_config`**（`数据模型:1071`：`factors.<type>.enabled=true`），与 client 级禁用**互不覆盖、效果取并集**；`login_policies` 只承载 **client 级**（含 `tier_default` 回落）。此前群聊里「平台级开关 = `login_policies`」的转述应更正为「**平台级 = `runtime_config`，client 级 = `login_policies`**」。附事实：`/api/v1/factors/{type}` 端点当前 main **未实现**（`cmd/guanjind/main.go` 未挂 `/api/v1/factors`），`AC-FLOW-05` 的实现侧仍依赖该端点与 `factors.<type>.enabled` 键；**本条不改变它的账目状态**。
- **有效禁用集 = 两个物理来源的并集（后来者只需记这一句）**：`effective_disabled = 平台级(runtime_config) ∪ client/tier 级(login_policies.disabled_factors)`；**两侧互不覆盖、互不改写**，任一侧新增禁用只会让有效集变大（方向恒为"只能增加禁用"，预设档位 §三 E 组）。`AC-FLOW-05` 的「两级不互相覆盖」正是这个并集语义——**它与本条争论的物理落点无关**（落点只决定 client 级那一半存在哪张表）。举例：client 级禁 `otp` + 平台级停 `sms` ⇒ 有效集 `{otp, sms}`，两者都不进 `available_factors`、都不计入 `current_acr`。
- **平台级那一半的键形态（诚实标注，别自造）**：基线只给了**示例**——`factors.<type>.enabled=true`（`数据模型:1071`，逐因子一键、写在 `runtime_config`），**没有**冻结"禁用集数组"式的键名（如 `factors.disabled: [...]` 不存在于任何基线文本）。因此：实现平台级启停时按"**逐因子一个 `enabled` 键**"落地，键名形如 `factors.<type>.enabled`；若将来要改成集合键，属新增键形态 → 走本文裁定 + `runtime_config` 迁移，**不得在代码里先造一个再用**。

- **已知行为（只登记，不改）**：现实现是**整行胜出**——`client_override` 存在即整份取它的 `disabled_factors`，**不与 `tier_default` 合并**。当前 `000017` 三档 seed 都是 `{}`，故今日无差异；一旦某档 `tier_default` 设了非空禁用集，`client_override` 会遮蔽它。要不要改成并集属**行为变更**，另开裁定（不在本条）。

- **基线处理：不改正文，也不进 CH**。按 **P8③** 的判据（"是否让业务方能用基线原本不允许的方式配置？"），本条对外可见面——Descriptor 字段名、管理 API 字段外形、错误码——**一个字不变**，属**措辞纠错**，不占 CH 编号；`/docs/baseline/` 一个字不改（本文效力条款），更正随**下版契约文档一次性落盘**（与 CH-1~CH-9 同批，时机 = M1 判据回填后）。

- **归属**：Spec（判据沿用本条、不另立表述）；Vue（不改数据源与字段）；Gate（断言/夹具只写 `login_policies`，不写 `clients`）；Core/Edge（`LoadAdmissionPolicy` 路径与注释已一致，**无需改代码**）。

---

## D20 `audit_logs` 补一条链序索引：**加**，现在加（`(created_at, id)`，迁移 `000020`，零代码改动）

> **触发**：Edge 修「跨月锚点被当接续判据」的误报（PR #88 / main `6ffa21f`）时发现——`audit_logs` **没有任何 `created_at` 索引**，而 `Append` 的链尾读（`internal/audit/repo.go` 的 `tailSQL`：`ORDER BY created_at DESC, id DESC LIMIT 1 FOR UPDATE`）与新增的 `TailTagBefore`（`repo.go:224`）都按 `created_at` 排序。他按约束没擅自加迁移，要裁定。**本条只给决议与索引定义，迁移由 Edge 落；不在本 PR 写 SQL 文件、不改代码。**

- **结论：加，本切片就加。** 索引定义（唯一一条）：
  ```sql
  CREATE INDEX audit_logs_created_id_idx ON audit_logs (created_at, id);
  ```
  迁移 **`000020_audit_logs_created_id_idx.{up,down}.sql`**（**编号修订 2026-11-11**：D21 的阈值 seed 占 `000019`，本条顺延 `000020`，避免出现编号空洞——库若先到 20，后补的 19 永远不会被 `migrate up` 应用；`000016` 是"索引单开一条"的先例）：`up` = 上面那句，`down` = `DROP INDEX IF EXISTS audit_logs_created_id_idx;`。**在父表上建**，PG 会自动在每个现有分区上建同名子索引，并作为**分区索引模板**自动套用到 `EnsurePartitions` 将来新建的分区 → **`EnsurePartitions` 不用改、`Append`/`TailTagBefore` 一行代码都不用改**（两句 SQL 已经按 `(created_at DESC, id DESC)` / `(created_at DESC, id DESC)` 写好了，规划器自己会改走索引）。

- **理由落在「锁内路径的写放大与尾延迟」，不是泛泛性能**：`Append` 的链尾读在 **`pg_advisory_xact_lock(chainLockKey)` 之内**——它是**全局串行点**，同一时刻只有一个审计写者，锁持有时长 = 全局审计写吞吐的倒数。当前这条读没有任何可用索引 → 规划器只能 `Seq Scan`（无 `occurred_at` 谓词 ⇒ **不剪枝，扫全部存活分区**）+ `Sort`（top-N 也必须吃完全部输入行）+ `Limit 1`，于是**每写一条审计，都要把留存期内（365 天）的全部审计行读一遍，且全程持锁**。代价随留存天数**线性**增长，不是常数：今天表小看不出，M1 之后每个月都比上个月慢一档，而这是"事后无法补"的写法——索引能补，但补的时机越晚、锁窗口越长。加上索引后该读变成**每分区一次 btree 反向下降 + `LIMIT 1`**（分区数由留存决定，12–13 个封顶），锁内只留 O(log n)。附带：`FOR UPDATE` 的语义不变（仍是"先 advisory、后行锁"，只锁命中那一行），**断电顺序/链序口径一个字未动**。

- **四件形态确认（逐条给结论）**
  1. **不要 `DESC`**：`(created_at DESC, id DESC)` 与 `(created_at, id)` 对 `ORDER BY created_at DESC, id DESC` 等价（btree 可反向扫），但后者还能顺带服务升序扫描；写库里少一处无意义差异。
  2. **不要 `occurred_at` / `month` / `partition` 前缀**：`occurred_at` 是**分区键、不是月列**，"分区前缀"在分区表上不成立（每个分区自带索引）；而一旦把 `occurred_at` 放前导列，**恰恰服务不了锁内那条读**——它没有 `occurred_at` 谓词。`Load` / `TailTagBefore` 的 `occurred_at` 区间靠**分区剪枝**（落到 1 个分区）已经足够，它们跑在写锁外、由定时任务驱动，不构成风险；若将来 `EXPLAIN` 证明校验侧排序成为问题，**另开一条**（不塞进本条）。
  3. **不要 `CONCURRENTLY`**：①`CREATE INDEX CONCURRENTLY` **不能在事务块内执行**（PG 直接报 `cannot run inside a transaction block`），而 `golang-migrate` 的 pgx 驱动**默认把每个版本包在事务里** → 写进迁移会当场失败；②未上线、表小，普通 `CREATE INDEX` 的 `SHARE` 锁窗口以毫秒计。若**上线后**要加同类索引，走 CONCURRENTLY 的运维脚本（不入 `migrations/`），并按技术栈 §6.2 登记为流程差异。
  4. **不要 partial / 不要第二条**：链尾读对全行集无谓词（partial 会漏链尾，直接断链级错误）；一条索引已覆盖锁内路径，第二条只增加每写一次的索引维护成本（右端顺序插入代价小但非零）。

- **否决的替代方案（两次被提出的两个）**：①**链尾 tag 缓存 / 落 `runtime_config`**——把"链尾"变成**第二份真源**：事务回滚、进程在"写锚点后、插入前"崩、或别名写入路径漏更新，都会让缓存与真实链尾分叉，而**分叉的表现是"链看着正常、事后整链复核才发现断"**（这类错误没有即时信号）→ 用安全换性能，不采纳；且它救不了 `TailTagBefore` / `Load` 的排序路径。②**只在内存/进程内缓存**——多副本各有一份，advisory 锁下的"链尾"必须是**库里的**那一行，否则分叉。**若不加，先劣化的路径**（写进本条备查）：先是**审计写 p99**（全局串行点），随后是 `audit_anchor_age_seconds`（锚点任务与写入抢同一把锁排队）。

- **账目/契约面：零改动**。纯性能索引，不改表/列/约束/错误码/端点 → **不进 CH**（按 P8③ 判据：不让业务方多出任何一种配置方式）；`acceptance-criteria.md` 一字未动，`AC-AUD-*` 的判据口径不变。**举证归 Gate**：Edge 落 `000020` 后，用 `EXPLAIN (ANALYZE, BUFFERS)` **前后对照**贴 plan，断言「锁内链尾读 = Index Scan（Backward）/ Merge Append，不再是 Seq Scan + Sort」；`FOR UPDATE` 仍在、并发分叉反证（`disableChainLock`/`disableRowLock` 两个注入点）仍应红。

- **归属与顺序**：**Edge** 落 `000020`（up/down 各一份，注释写清"服务哪条 SQL、为什么单开一条、为什么不用 CONCURRENTLY"）；**Gate** 出 plan 前后对照；**Core 不参与**（`Append` 零改动）。本条与 D19 同批回传、**分属两条 DEC**。

---

## D21 `AC-REG-04` 的「耗时差异阈值」= `register.timing.max_diff_ms`（判据参数，非运行时参数；迁移 `000019`）

> **触发**：Core 在 #86 里如实标注——`AC-REG-04` 判据（`acceptance-criteria.md:99`）要求「耗时与不存在账号差异 ≤ 阈值（**阈值取自 `runtime_config`**）」，但**库里没有这个键**，于是这条 AC 只能记「码与状态已证 / 时序不可判」。基线只有定性要求、**无数字**：`技术栈:1556`「注册的『账号是否存在』绝不通过 message/状态码/**耗时**泄露」、`页面规格:232`/`:250`「响应耗时保持一致」。数字只能由本条裁定，**不得由实现或测试自造魔法数**。

- **① 键名与语义（冻结）**
  - 键：**`register.timing.max_diff_ms`**，`value_type = 'int'`，默认 **`50`**（毫秒）。
  - 测**哪两段**：**同一端点、同一请求体形状、只换 `value`** 的两次 `POST /auth/register/start`——
    **A = 标识符已存在**（返回 `IDENTIFIER_ALREADY_BOUND`）、**B = 标识符不存在**（走正常 `need_challenge`/skip 分支）。
    **不做跨端点比较**（`register/complete` 的 TOCTOU 复查另有口令派生，量级不同，**不混进这条**）。
  - **口径是谁减谁**：两条路径各自耗时取中位数，判据 = **`|median(A) − median(B)| ≤ 阈值`**（**绝对值，不设方向**——快了慢了都算泄露）。
  - **时钟源 = 应用/测试进程的单调钟**（`time.Since`），**不用 DB `now()`**：DB 钟只用于跨节点/跨行的判定（`flows`/`challenges` 过期），此处测的是"同一个 handler 的处理时长"，走 DB 会把**网络往返 + 两钟偏差**当信号。判据用 e2e 侧（回环、黑盒）的 elapsed；服务端若打点只作**诊断**，不对外暴露字段。

- **② 允许值 = 50 ms，依据与不确定性（如实说）**
  - 依据只有一条硬约束 + 一条量级判断：**硬约束**＝必须远大于回环 e2e 的抖动（毫秒级，取 9 次中位数后更稳），否则判据自己 flaky；**量级判断**＝必须小于"结构性差异"的量级——口令派生 `PBKDF2 ≥ 100000` 次（`internal/crypto/pwdcrypto.go:19` 的 `MinPBKDF2Iterations`）在数十毫秒级，拒绝路径若**跳过派生**或**多打一次外部调用**（发码），差值会明显超过 50 ms。
  - **诚实标注（必须留）**：这是**经验值，不是观测分布**——沙盒/本地都没有可用 PG（本地无集群），拿不到真跑直方图。样本口径 = CI `integration` job 里 Gate 那条 e2e 的 3 次 `-count=3` 真跑；若实测抖动 > 20 ms，**改键即可**（判据本身要求"改键值要跟着变"），不改代码。
  - **反作弊**：**禁止在实现里 `sleep` 凑时长**（那等于用可用性换"判据绿"）；若某条路径天然差 > 阈值，正确解法是把外部调用（发码）移出同步路径，**不是放宽键**。

- **③ 超阈值 = fail（红），不是 warn**：它是判据的一部分（判据原文就是"≤ 阈值"），且是安全属性（侧信道）——`warn` 等于允许静默回归。**防 flake 口径（判据用法，归 Gate）**：每条路径 **9 次、丢前 2 次预热、取中位数**；失败信息必须打印**两组原始样本**（便于一眼看出是抖动还是结构性差异）；该用例只在 `integration` job 跑（**不进 `-race`**，race 会放大抖动）。

- **④ 落地（谁改哪里）**
  - **迁移 = 本条（Ark）**：`migrations/000019_seed_register_timing_config.{up,down}.sql` —— `up` 插一行 `ON CONFLICT (key) DO NOTHING`，`down` 按 key 精确删；**不改 000014/000018**（技术栈 §6.2 编号只增不改）。
  - **服务代码：零改动（比"改一行读取处"更省）**。该键是**判据参数**——由 **e2e 测试直接查库**读取（`SELECT value FROM runtime_config WHERE key='register.timing.max_diff_ms'`，`value_type` 必须是 `int`、值 > 0，否则用例直接红），**服务不读它、也不得据此改变任何对外行为**；因此**不进 `StartupKeys()`**（不给生产加一条与安全无关的启动硬依赖）：缺键 = 判据不可执行（红），不是启动失败。
  - **判据侧（Gate）**：`AC-REG-04` 由「码与状态已证 / 时序不可判」升为**可判**——前半句已由 #86 的 `TestE2E_AC_REG_04_DuplicateEmailIs409AlreadyBound` 证；后半句新写一条计时用例（按 ③ 的 9 次中位数口径）；`acceptance-criteria.md` **一字不改**（判据文本本来就要求"取自 `runtime_config`"，本条只是把缺失的键补上）。
  - **CI**：`integration` job 的 schema 断言补一项 `register_timing_key=50`（与 `challenge.*` 两键同款真断言，不等即 `[FAIL]` + `exit 1`），防"迁移在但 seed 丢"。

- **⑤ 边界（不做的事）**：本条不定义"账号存在性"的**其它**防枚举面（`recover/*` 的响应结构与耗时属基线原文，另论）；不给 `/auth/register/*` 新增任何对外字段（`attempts_left` 是 CH-8，另一条）；**不进 CH**（对外契约面零变化，纯补一个内部判据参数）。

---

## 契约变更清单（对外可见）—— 状态：**CH-1~CH-5 已追认（经 hugo 追认，2026-11-02）；CH-6 为内部口径；CH-7 已裁定；CH-8 待追认（2026-11-04）；CH-9 待追认（2026-11-10）**

> 本表只收**对外可见**的变更（影响业务系统、SDK、前端或对外错误码的）。基线 `/docs/baseline/` 一个字不改，这些变更以本表为准并在**下版契约文档**（OpenAPI + `sdk-contract.md` + 页面规格口径）一次性落盘；落盘时机 = 全部 M1 判据回填后、M1 编码前。**本表状态：CH-1~CH-5 = 已追认（经 hugo 追认，2026-11-02，记录法见 P8）；CH-6 = 内部口径、无需追认；CH-7 = 已裁定；CH-8 = 待追认（2026-11-04）；CH-9 = 待追认（2026-11-10，SLO 通知）。** 后续新增变更一律按 **P8②** 追加行（编号只增不重排）；作废条目改「已撤回」并保留原行。

| # | 变更点 | 依据（本文条） | 影响面 | 状态 |
|---|---|---|---|---|
| **CH-1** | `POST /auth/challenge/{factor}` **新增必填参数 `flow_id`**：当 `purpose ∈ {login, step_up}` 时**必填**（阶梯内的流必须显式指明），缺省 → `MISSING_PARAMETER`；其余 purpose（`recover`/`enroll`/`verify_identifier`）可空。理由：匿名流 `user_id`/`session_id` 双 NULL，无法用「session + factor」反查定位，显式带 `flow_id` 是唯一可举证解（Core 对齐结论；`challenges` 表不加 `flow_id` 列） | D5「与 `challenges` 的职责边界」 | OpenAPI / SDK 三语言（Java·Node·Go）/ Vue 登录页补步 / 判据 `AC-FLOW-01/03/10` | **已追认**（2026-11-02） |
| **CH-2** | Flow Descriptor **新增字段**：`steps[i].challenge_id`、`steps[i].purpose`、`steps[i].contributes_acr`，顶层 `terminal_state`（`satisfied/need_more/unreachable`）与 `unreachable_reason`。纯加字段、不删既有键，向后兼容 | D7 | SDK（Descriptor 解析）/ Vue（渲染终止条件）/ 判据 `AC-FLOW-01/07` | **已追认**（2026-11-02） |
| **CH-3** | 分页统一 **`limit`+`cursor`**，`page`/`page_size`/`total` 作废；`PAGE_SIZE_EXCEEDED` 语义改为「`limit` > 200」 | D3-A | 管理面全部列表 / 前端列表组件 / SDK 分页/ 判据 `AC-AUD-08`（M5） | **已追认**（2026-11-02） |
| **CH-4** | 错误码**四处**变更：①**新增** `STEP_ALREADY_SATISFIED`（E4/409）；②**废弃** `SERVICE_DEGRADED`（永不返回）；③同场景双码**按语义分工**（PKCE 两码 / `TOKEN_REVOKED` vs `SESSION_REVOKED` / `THREE_POWER_VIOLATION` vs `INSUFFICIENT_ROLE`）；④**枚举补登记 3 码** `INVITE_CODE_INVALID`（E1/400）、`WEAK_PASSWORD`（E4/422）、`REGISTRATION_NOT_ALLOWED`（E4/**403**，按 §5.4「接口可覆盖」）+ 口令三码**固定顺序 + `details.violations[]` 带全量** | D3-E、D3-F、D3-B′、D8-C′ | 错误码表 / SDK 异常映射 / 前端文案表 / 判据 **`AC-LOGIN-05`、`AC-FLOW-03`、`AC-SESS-01`、`AC-AUD-07`、`AC-ENG-08`（共 5 条，Spec 抽查订正：原漏前两条；新增 3 码与口令 violations 的判据缺口由 Gate 定账）** | **已追认**（2026-11-02） |
| **CH-5** | SDK 契约：配置以 **29 项**为交付集（"32 项"作废、缺号 #7/#16/#17 不补 key）、**删除 `guanjin.mode`**（无 direct 形态）。**注：`jwks-uri` 的默认值已移出本条**——它与 `replay-store`/`agent.pull.enabled` 同属**纠错性默认值**、按 **D6(5)** 处理，不单列追认 | D6 (1)(2)(5) | SDK 三语言配置类与示例 / `sdk-contract.md` / 判据 **`AC-SDK-01`、`AC-SDK-07`、`AC-FC-05`**（`AC-SDK-02` 已按 D6(4) 纯加固裁定移出 CH-5） | **已追认**（2026-11-02） |
| **CH-6** | 端点计数口径：**具名端点 65**（"66" 是 65 + 1 条数据面通配边界）、Agent 面 2 条 | D3-C | 无外部影响（内部计数与文档口径） | 内部口径，**无需追认** |
| **CH-7** | **约束冲突 → 对外错误码映射表（8 处）**：`flows_one_active_per_user_client_uniq`→`COOLDOWN_ACTIVE`(E4/409)、`clients_pkey`→`INVALID_CLIENT`(E1/400)、`signing_keys_kid_uniq` 与两条 `login_policies_*_uidx`→`INVALID_PARAMETER`(E1/400)、`role_bindings_uniq`→`BINDING_ALREADY_EXISTS`(E7/409)、`policy_snapshots_uniq`→`POLICY_SNAPSHOT_CONFLICT`(E4/409)、`identifiers_one_primary_per_user` 与 `user_visited_clients_uniq` **不映射**。**只用 §5.4 既有码、不新增码名**；新增的是「哪个约束对应哪个码」这一层口径 | D4-D₂、D3-B、D5 | `internal/store/pgxerr.go`（八行逐条）/ Core 的 flow 先查后建 / 判据 `AC-ENG-04`、`AC-FLOW-11`、`AC-REG-03` | **已裁定（2026-11-02，随契约文档落盘）** |
| **CH-8** | `POST /auth/register/start` **响应新增 `attempts_left`**：§3.3.1 原响应要点是 `{register_token, challenge_id?, next_action, target_masked?, expires_at}`，**无此键**。值为该注册挑战的 `max_attempts`（来自 `runtime_config.challenge.max_attempts`，300s / 5 由 `000018` 落库）。**纯加字段、不删既有键、向后兼容**，老客户端忽略即可 | D14①④ | Vue 注册页（展示"还能试几次"）/ SDK 三语言注册响应模型 / 判据 `AC-REG-01`（**可选用**该字段，断言仍以 DB `challenges` 行为准） | **待追认（2026-11-04，Ark 提）**：追认前**不得作为判据依据**，也不得据此把 `AC-REG-01` 记 PASS |
| **CH-9** | **单点登出（SLO）通知 = 对外可见的新 wire**（R12 取候选 A 后的登记）：平台在登出/撤销时向该用户**已访问的业务系统**发 **SM2 签名通知**，业务方需落一个**接收端点**并验签。**路径 / 报文 / 事件名 / ack 语义一律待 R12 落盘**——本条**只登记"这是一条对外契约"**，不预设任何字段、不新增端点名 | R12（候选 A）、D16④ | 业务系统接入方（接收端点 + 验签）/ `sdk-contract.md`（若 SDK 提供接收侧能力）/ 判据 `AC-DP-12`（**状态与账目不动**，见 D16④） | **待追认（2026-11-10，团队取 A）· 默认口径，spencer 可一句话改**；**R12 已按候选 A 落地并已验**（`AC-DP-12` 送达半边），本条仍只登记绝不预设字段：追认前**不得**据此新增端点/字段/事件名，判据与 90 项账目不变 |

**合计 9 条：CH-1~CH-5 已全部追认（经 hugo 追认，2026-11-02；记录法与判据侧影响见 P8），CH-6 为内部口径、**CH-7** 为约束→码映射口径（2026-11-02 裁定）、**CH-8 待追认（2026-11-04，`§3.3.1` 响应新增 `attempts_left`）、CH-9 待追认（2026-11-10，SLO 通知属对外新 wire，R12 取 A 的登记，不定型字段）。** 追认前的假设性回退方案（**现已不适用，仅留档**）：CH-1 若被否，回退方案是「`challenges` 加 `flow_id` 列 + 反查」（需 ADR，且会引入双向耦合，D5 已论证不推荐）；CH-2/CH-4/CH-5 若被否，按基线原文实现并同步回改本文与 Gate 判据（不留在两套口径）。**纠错/加固性边界的完整口径（Gate v5 回读 2026-11-02 补，解决 G11）**：①「把错标默认值改回与已生效决定一致」= **纠错**，走 D6(3)/(5)，不进本清单（`jwks-uri` 路径、`replay-store=redis`、`agent.pull.enabled=false`）；②「把基线**已经禁止**的取值在非 `local|test` 下变成启动失败」= **加固**，走 D6(4)，同样不进本清单（`guanjin.enabled` / `guanjin.authz.enabled` 的 profile 门控即属此类——SDK 契约 §3.2「验签不可关」+ 移交 §九「不存在生产可跳过验签的开关」本已禁止，D6(4) 只是让禁令**可执行**，没有新增契约面，故 **`AC-SDK-02` 不进 CH、应为 `PASS-able`**）；③**只有**「新增 key / 改 key 名 / 改数量 / 改必填性 / 改变某 key 在 `local|test` 之外的可选值域（即新增一种合法取值）」才必须进本清单待追认。判据：**这条改动是否让业务方能用基线原本不允许的方式配置 SDK？** 能 → 进 CH；不能（只是让基线已禁止的用法启动失败）→ 加固，不进。

---

## 旧分支借用登记（`origin/feat/phase-8-authz`，2026-11-02）

> 背景：该分支为 spencer 10 月实现，19 个提交（`4c6f922`…`cb208d2`，tip `cb208d2`）、292 文件。**判断标准 = 哪版更贴本文现行裁定**，不是"哪版更新更全"。**不 merge 那 19 个提交**（会与新骨架纠缠），只按需 cherry-pick 或重做。完整对照见 `docs/legacy-branch-adoption.md`（14 模块四档判档）。

**本轮实际落地代码文件数 = 0。** 原因：旧分支每一块都绑在"14 表 / 旧 `crypto`·`store` 接口 / 旧 `purpose` 词表 / SHA256 HMAC / `dataLabel()` / 无分区审计链"上，原样搬入会同时破 D1/D2/D5/D11/D4-C，三闸必红。故**本轮只登记借用清单与改写点**，代码在 S2–S11 内逐块改写吸收。

| 拟借用文件（glob） | 来源提交 | 将落地步骤 | 必须改写点（依本文条号） |
|---|---|---|---|
| `internal/store/model/*.go`（14 个模型）<br>`internal/store/repo_*.go`、`internal/store/pgxerr.go` | `d723ceb`<br>`cb208d2` | S2 / S3 | ①补 `flows` **13 列**模型与 4 具名索引（**D5**）；②`mapPgError` 改 **SQLSTATE + 约束名**双键（`flows_one_active_per_user_client_uniq`→409、I-F1→400，**D5 落地段**）；③**禁止**带入 `migrations/migrations.go` 的"应用内迁移"路径（**P1**） |
| `internal/factor/**`（21 文件）<br>`internal/factor/totp*.go` | `2fd4295`<br>`764ab1f` | S4 / M2 | 接口改接现 `crypto`/`store`；保持**能力断言**（禁 `if type==`，索引 §2 因子 + 因子模型 §7）；TOTP 时间窗走 SM3 |
| `internal/flow/{descriptor,state,register}.go` | `2fd4295` / `3c84599` | S5 | ①改读写 **`flows`**，用 `repo_flows.LockByID`（`FOR UPDATE`）做 amr 累加（**D5 I-F5/I-F8**）；②`purpose` 换 **6 值**词表（**D2**）；③补**两段式过期清理**（**D5 I-F6**）；④amr **单调不减**校验（**I-F2**）；⑤注册必带 `client_id`（索引 §2） |
| `internal/gateway/**`（9 文件）+ `gateway` E2E 用例 | `ce03dd3` | S8 | 只保留 **7 件事**（数据面 §1.1）；客户端自带头**先弃后签**；上游失败 → `UPSTREAM_UNAVAILABLE`（**D8-C′④**）；断言 `exp=60s`、`kid` 区分（索引 §4.2-23） |
| `sdk/guanjin-core/**`（34 java）、`sdk/guanjin-spring-boot2/**`（21）、`sdk/examples`、`sdk/testdata` | `b598bd4` | S9（owner = Nova） | ①`dataLabel(` / 裸 `.clearance(` **全删**，唯一 `role(name).withClearance(level)`（**D11**，旧分支有 6+1 处）；②配置 **29 项**、无 `mode`、`jwks-uri` 默认 `/.well-known/jwks.json`、`replay-store=redis`、`pull.enabled=false`（**D6**）；③剥离按 **D10 六步**（替换请求视图，非"验签前剥离"）；④`enabled`/`authz.enabled` 非 `local|test` **启动失败**（**D6(4)**，加固非 CH） |
| `internal/authz/binding.go`、`internal/authz/binding_e2e_test.go` | `cb208d2` | S7 / M5 | 平台只存"用户→角色"绑定并审计、判定在 SDK 本地（索引 §2）；双码分工 `THREE_POWER_VIOLATION`(E7/403) vs `INSUFFICIENT_ROLE`(E3/403)（**D3-E**）；撤销联动走 `AbandonBySession`（**D5**） |
| `internal/agent/{hmac,report,policies,service}.go`、`agent_e2e_test.go` | `cb208d2` | M2 前置 | **重做为 HMAC-SM3**：旧文件用 `SHA256`（违 **D1**）；沿用现 `internal/agent/canonical.go` 的 D1 实现（`sorted_query` + `SM3HEX(body)` + `PATH` 含 context-path、不解码、不去尾斜杠）；`nonce` 按**字节**计长（**D1 A1/A2**） |
| `internal/audit/chain.go` | `cb208d2` | S10 | **只作对照、不搬**：重做为 月分区 + 主键 `(id, occurred_at)` + `pg_advisory_xact_lock` + 锚（`runtime_config`）+ 两段式 + 整链校验 Job（**D4-B/D4-C**） |

**明确不借用（放弃）**：①旧 `migrations/` 的 **14 表** DDL 与 `migrations/migrations.go`（**D5** 已定 15 表、**P1** 禁应用内迁移）；②旧 `design-baseline/` 的 13 份（现 `docs/baseline/` 14 份为准，**P3**，绝不回灌）；③旧骨架（已被 PR#4 的骨架 + 三闸取代）；④`internal/notify`、旧 `internal/observability`（不在 10 包边界内 / 与现规划重复）。

**旧分支硬冲突清单（8 条，落地时必须逐条消）**：C1 `agent/hmac.go` 用 `SHA256`（违 D1）；C2 SDK `dataLabel(`×6、`.clearance(`×1（违 D11）；C3 `SERVICE_DEGRADED` 仍出现（违 D8-C′⑤）；C4 14 表无 `flows`（违 D5）；C5 `purpose` 词表 `stepup/bind` 与 `step_up/enroll/verify_identifier/register` 混用（违 D2）；C6 审计链无分区/无锚/无咨询锁（违 D4-C）；C7 flow 包无 `FOR UPDATE`（违 D5 I-F5/I-F8）；C8 SDK 配置未按 29 项收口（违 D6）。

---

## 残余待裁（spencer 本轮未覆盖，本节不作裁定）

| # | 事项 | 现状 / 卡点 | 需要谁 |
|---|---|---|---|
| R2 | **纯软件国密库是否被密码测评机构认可**（`tjfoc/gmsm`、BouncyCastle 达 GB/T 37092 二级口径） | **后置（DEFERRED）：本期不做判定**（挂起）——依 spencer 口径（2026-11-02）密评整体后置；代码仍按 P2① 纯软国密实现，本期不出证/不送检。**定性部分已由 D8-E ④ 明确**（算法库 ≠ 密码产品；纯软对 37092 二级**无解**），R2 现**只剩"送检/出证时点"一项** | —（本期不推进；如需恢复由 spencer 重开） |
| R3 | **前置网关国密 TLS（TLCP/SM2 证书）选型与责任方** | **不属本平台范围（部署方前置项），已关闭**——TLS 终结/TLCP/证书校验均由部署方承担，平台只见明文、不写代码、不进验收判据（见 P2②，依 spencer 口径 2026-11-02） | —（已出本平台边界） |
| R4 | **密评受理口径与送检时间** | **后置（DEFERRED）：本期不做判定**（挂起）——依 spencer 口径（2026-11-02）；等保相关**代码要求照基线实现**，只是本期不出证/不送检 | —（本期不推进；如需恢复由 spencer 重开） |
| R5 | **PKCE `code_challenge_method=S256` 与「全链路国密/禁 SHA 系」的兼容口径** | 接口清单 §3.1.3 要求 `S256`（OIDC 只定义 `plain`/`S256`），与索引 §2 的国密要求未对齐；M1 不阻塞（`AC-AUD-12` 已把 S256 列为唯一白名单） | spencer（可定为"传输层外不属签名，保留 S256"，需确认） |
| R8 | **数据面 QPS 目标**（索引 §4.3-1） | 峰值登录 200 QPS 已知，数据面全量转发 QPS 未估 → 压测基线与扩容比缺失 | Ark 提议 + spencer 确认 |
| R9 | **审计归档介质与归档密钥保管责任方** | 365 天留存与锚点机制已定（D4-C），归档到哪、谁持旧密钥副本（索引 §2 要求保留 365 天）未定 | spencer + 运维 |
| R10 | **`main` 分支保护缺 `required_status_checks`**（四道门禁目前是**自律门禁**：红着也能点 merge） | 需 **admin**（spencer）在 repo settings 把 `go` / `race` / `web` / `integration` 设为 required；团队**不自改**（无权限，也不擅自改仓库策略）——本条只作登记，不改任何文件 | spencer（admin） |
| R10 | **引导流程：首个管理员 + 首把签名密钥 + 首个业务系统接入**（索引 §4.3-3 建议级） | **只登记、本轮不实现**（来源：**Spec · 建议级 · 索引 §4.3-3**）。现状：`signing_keys` / `users.is_admin` / `clients` 三张表的结构已就绪，但"第一个管理员从哪来、首把 SM2 密钥怎么生成与激活、首个 client 怎么登记"没有流程定义；骨架阶段就会遇到（AC-ENG 系列要跑起来必须先有可登录账号与可签发密钥）。**登记形态**：本行 + M1 之后的首个迭代排期，**不写代码、不生成密钥、不建种子账号**（禁默认口令） | Ark 起草流程 → spencer 确认；实现归 S2 之后的运维/引导切片 |
| **R12** | **单点登出（SLO）通知的 wire 契约**：目标地址来源 / 报文形态 / 事件名 / ack 语义 | 基线与接口清单只写「发签名通知（同 SM2 方向）」（数据面 §1.1⑦、接口 §5.3.2），**无端点名、无报文定义**；自造端点或新 claim 属新增契约面（F0 禁自造 wire 名）→ 本轮**不裁定**。**已裁并落地**：取候选 A（零新表零新列、复用既有 SM2 签名机制与既有 claim 集），已实现并验过——`AC-DP-12` 送达半边已证（假上游收到**恰好 1 条**、JWKS 验签通过、十项 claim 齐、上游 500 时撤销仍 200）；对外契约登记见 **CH-9（待 spencer 追认）**。候选 B（`clients` 加列 + 多一张表，撞 D5 的 15 表冻结）**未采用**。`AC-DP-12` 本轮**按 `acceptance-criteria.md:188` 记 `PASS-able`、判据一字不改**；裁的只有 wire 形态（见 D16④） | **spencer + Spec**（定契约后按 CH 记录法落盘） |
| **R13** | `AC-DP-09` 的 `details.flow` **字段级最小集**（无活跃流时 `steps[i].challenge_id` 填 `null` 还是省略） | CH-2 冻结了 Descriptor 字段，但没规定"该字段无值"的序列化形态；影响 SDK 反序列化与 Gate 断言粒度。**已裁**：判据不涉及该键（断言只取存在性 + `current_acr`/`target_acr`/`terminal_state`），实现沿用 `steps[i].challenge_id` 的 `omitempty`（无值即省略，不填 `null`）；若日后 CH-2 落盘要改该形态，另走判据变更 | —（已裁，不需 spencer） |
| R11 | **数据面逐条转发规则与容量目标**（索引 §4.3-1 / §4.2-18） | **① 规则部分已冻结（2026-11-10，D16③）**：按路径段匹配、长前缀优先、重复前缀装配期报错、**未匹配 404 `CLIENT_NOT_FOUND`（`fea9d1e` 原为 503 `UPSTREAM_UNAVAILABLE`，按 D8-C′④ 以本条为准；Edge 已落 `CLIENT_NOT_FOUND`，两边一致）**、不改写路径、域名分流不属本平台——规则不再待裁。**② 容量部分仍待裁**（数据面全量转发的 QPS / 连接数基线未定；与 R8 合并出结论，不得当已定引用） | ① 已由 Ark 出草案并冻结（团队收口）；② **spencer**（压测基线 + 扩容比，与 R8 合并） |

*本文只登记裁定与口径，不改 `/docs/baseline/` 任何内容；今晚建骨架时与 12 份基线一并入库。ADR-001~004 由 Ark 在 `docs/adr/` 落地时引用本文 D 编号。*

**收口记录（2026-11-02，经 hugo 追认）**：R1 → 转入 **D5**（新增第 15 张表 `flows`，只定表结构规格）；R6 → 转入 **P5**（旧仓 SpIAM 冻结归档、只读参考：不改/不推/不开分支，代码只进 `GuanJin`）；R7 → 转入 **P4**（Go module path = `github.com/spencerswagger/guanjin`）。本节现存 **R2–R5、R8、R9**，编号保持不变（不重排，避免打乱 Gate 判据与 Nova 契约的引用）；其中 **R2/R4 已后置（本期不做判定、挂起）、R3 已关闭（TLS 不属本平台范围）**——依 spencer 口径 2026-11-02，团队不再裁定、也不会再等任何"对口径"；R5/R8/R9 在外部结论到位前其状态仍如实保留为「未定」，**不得当已决引用**。

**Gate v2 回读后的追加收口（2026-11-02）**：`D3-E`（同场景双码按语义分工）、`D3-F`（新增 `STEP_ALREADY_SATISFIED`，收 G2）、`D8-C′`（E6 七码归位，`SERVICE_DEGRADED` 废弃，收 G1）、`D8-D′`（`/userinfo` 零查库 200 / 查库即 503）已全部写入本文，**不再有待补项**；Gate 的 §15-G1/G2 与"§6 待补 4 处"由此闭合，`acceptance-criteria.md` 无需再向本文追问。

**Gate v3 / Nova 回读后的追加收口（2026-11-02，团队收口）**：①**D1 补 A1（`PATH` 唯一取法，含 context-path、禁中间件 path 重写）与 A2（`SORTED_QUERY` 算法：保留重复键、编码先于排序、name→value 两级排序）**——Nova 判定这两条直接决定 SDK 与平台能否互验；②**D6 补两处默认值**（`authz.replay-store=redis`、`agent.pull.enabled=false`），只改默认值不动 29 项；③**D3-D 补失败计数键名** `login:fail:{user_id}` / `login:lock:{user_id}`；④**D4-C 补 `trace_id` 取证口径**（`flows.trace_id` 只记创建那次，与后续 `auth_events.trace_id` 不一致**不算缺陷**）。以上均标「Gate/Nova 回读（2026-11-02）」，**不含任何追认标注**（追认标注仅 P4/P5 与 D5 的 R1）。

**Spec 复校后的收口（2026-11-02，团队收口）**：①**新增 D10**——`X-GuanJin-Assertion` 剥离的**唯一六步时序**（读→验签→claim→注入→替换视图→handler），明确否决「验签前剥离」；②**D6(5) 扩为三处默认值并统一为「纠错性默认值」口径**，`jwks-uri` 从 **CH-5 移出**（解决同类不同标），CH-5 只留 29 项与删 `mode`；③**新增 D11**——`GJ-CAP-13` 密级方法冻结为 `withClearance(level)`，`dataLabel()` 作废。以上标「Spec 复校（2026-11-02）」，**未动用追认标注**。

**密评后置 + TLS 出边界（2026-11-02，依 spencer 口径）**：①**密评整体后置**——本期**不做判定、不出证、不送检**；R2/R4 改为「后置（DEFERRED）：本期不做判定」，R3 关闭（**TLS 不属本平台范围**）。②**等保相关的代码要求照基线实现、不受影响**（双因素、审计不可篡改、三员分权、脱敏留痕），只把「出证 / 送检判定」后置。③**P2 拆分并重写**：纯软国密/不接密码机保留为已生效前提；TLS 部分改为「不属于本平台范围（部署方前置）」，**不再有"只预留"的表述**，且**不写进代码、不进验收判据、不设配置项**。本条**未动用**「经 hugo 追认」标注。

**Spec 终校后的两处补齐（2026-11-02，团队收口）**：①**D-1 数字自洽**——D6(5) 的 `PENDING` 由 `12 → 11` 更正为 `12 → 11 → **10**`（`AC-SDK-03` 因 `jwks-uri` 纠错降 1、`AC-SDK-02` 因 D6(4) 加固再降 1），CH-5 判据列删去 `AC-SDK-02`，与 Gate v6 的 `68 / 10` 一致；②**D-2 客体密级入口**——D11 补选**方案①（不做声明式打标、调用点传 `Authz.checkLabel(objLevel)`）**，判定为**不进 CH 清单**（不新增 API 面）、**登记 ADR-004**。以上标「Spec 终校（2026-11-02）」，未动用追认标注。

**Spec 复校二轮 → D11 三处补口（2026-11-02，团队收口，未动用追认标注）**：①**C1** 裸 `clearance()` 与 `dataLabel()` 一并作废（唯一写法 `withClearance(level)`），门禁口径 = 正则 `(?<![A-Za-z0-9_])clearance\(` + 范围限 SDK 三语言仓 + 四个注入各验一次，**Gate 已在其 `acceptance-criteria.md` v9 落判据**（`AC-ENG-08`③ 由单一 `dataLabel(` 扩为两个禁词；账目与等保代码判据未动）；②**B1** `level` 词表冻结 **1=内部 / 2=秘密 / 3=机密**，「公开」= 不声明、无 0 级，随 ADR-004 入库（仅影响 M5）；③**C2** `checkLabel(objLevel)` 形参名统一（主体 `level` / 客体 `objLevel`）。本段**未动用**「经 hugo 追认」标注，也不改任何账目数字。

**Edge/Core 对齐结论（2026-11-02，团队收口，非追认）**：`flows` 表按两人复核并入 6 处——①新增第 13 列 `trace_id`（Edge/Core 均赞成）；②`flow.ttl_seconds` seed 600 + `ON CONFLICT DO NOTHING` + 键缺失启动 fail-fast + 代码不留常量；③加锁顺序 `sessions→flows`，并追加「`sessions` 禁 `FOR UPDATE`、PBKDF2/投递等重活必须在锁外」（I-F8）；④**清理器改两段式**（置 `expired` 不删 → 7 天后 `DELETE`），消除 `AC-FLOW-10` flaky；⑤`completed` 行允许**一次性**回填 `session_id`（I-F7 例外条款）；⑥`challenges` 不加 `flow_id`、不反查，改请求显式携带 → 已登记为对外契约变更 **CH-1（P8 已追认，2026-11-02）**。另采纳 Edge/Core 的落点划分（Core：`internal/flow` + `internal/session` 窄接口 `AbandonBySession`；Edge：`store/model` + `repo_flows`（含带 tx 的 `LockByID`）+ `pgxerr` 双键映射）。**本条不含任何追认标注。**

**Spec《口径锚结论》回读收口（2026-11-02，团队收口，未动用追认标注）**：①**四条确认 + 补口落地**——D2 补 A′（依据升级为「功能缺陷」：DDL 4 值下 `register/start` 首次写 `challenges` 即撞 CHECK；`register` 必带 `client_id`、`user_id` 可空；`login`↔`step_up` 分界 =**是否已有会话**）、D3-A 补 A′（§5.5 自认提案→不具契约优先力；**不返 `total`**，用 `next_cursor`；列表游标与导出/Agent 游标**语义不同、不得互喂**）、D3-B 补 B′（**补登记 3 码** `INVITE_CODE_INVALID` E1/400、`WEAK_PASSWORD` E4/422、`REGISTRATION_NOT_ALLOWED` E4/403 覆盖；口令三码**固定顺序 + `details.violations[]` 全量**）、D6 补 (6)（无别名键 / 无旧键兼容 / 环境变量**机械逐段转换**，禁 `GUANJIN_BASE_URL` 这类缩写）。②**D8-E 改判为第二版（密评等级口径）——旧版"上限二级"结论作废**：索引 §2「密评上限为二级」**不再引用**，等级**随接入系统备案等级**对齐（「等保三级 + 密评二级」测评不受理）；条款引用**保持 8.x 不降 7.x**；统一表述「实现按三级条款固定、二级为其子集、判定与出证后置」；**TLS 责任转移写清、禁写"无影响"**；**密码产品不得承诺"密评通过"**（算法库 ≠ 密码产品；纯软对 37092 二级无解；v1 只做「自评 + 遗留风险登记」）；旧版仍有效的三条（策略完整性按索引 §4.2-27 / `L2` 档位 ≠ 平台定级 / 本期仍不判定）已并入第二版⑥；**`m1-slices.md` 第 10 项已同步改**，Gate 判据侧与 Spec 对外口径需各自对齐。③**D9 补口三处**：`SDK契约` §8 **确为缺章**（`since_version`/缓存失效/失败降级三件无契约，卡 Nova 与 `AC-SDK-10`）；`页面规格` §4.1 引「接口清单 §3.1.4」**经复核不悬空**（§3.1.4 存在，索引 §4.2-14 亦冻结字段权威）→ **否决该项**，改立「字段权威/端点权威」两源分工（并据此修正 D7 依据）；`数据模型` §4.1 的《错误码体系》**不存在** → 改指接口 §5.4 + D3-B/B′。④**新增 D12（`acr` 取值域）**：对外只 `AAL1|AAL2`、`AAL0` 仅展示不落库、**`AAL3` 预留但 V1 不得产出**（代码白名单断言 + 两条用例，防"对外契约不实"）；`系统架构说明` 第八章「三级=强方式」判笔误、对外文档不照抄。⑤**新增 F0 不可动名单**（14+`flows` 表 / 65 具名端点 + 1 通配 / wire 名字与四字段 / 概念名 / SDK 29 键 / Agent 凭证落点），供全员统一引用，**「66 具名」措辞作废**。⑥**CH-4 由"三处"扩为"四处"**（并入补登记 3 码与口令 `violations` 结构），当时状态为**待追认**、**后随 P8 追认（2026-11-02）**；**Gate 据此落 v10：账目 85 → 90 = 70 `PASS-able` / 13 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置（DEFERRED）：本期不做判定」**（+5 条 = `AC-FLOW-12/13`（D12，`PASS-able`）+ `AC-REG-08/09/10`（CH-4④⑤，`PENDING`）；`AC-AUD-02/03/04/07/09/12` 等保代码判据一条未动），本文 D8-B 与 `m1-slices.md` 已同步为该数字。

---

## 附 · 外部锚点索引（供全员按名查，避免翻平行文件）

> 用途：Spec / Gate / Nova / Core / Edge / Vue 引用口径时用**左列的锚点名** grep 右列的落点。**锚点名不得改名**（已在各自文件与群聊里用过）；若发现左右不一致，**以本文件的右侧落点为准**并回报 Ark 一次改齐。

| 锚点名（外部引用用） | 落点（本文件内） | 一句话内容 |
|---|---|---|
| `F0` | §**F0 不可动名单** | 表 15 张 / 具名端点 65 + 1 通配 / wire 名字 / 概念名 / SDK 29 键 / Agent 凭证落点，**六条** |
| `D2 补口 A′` | D2 的「补口 A′」 | 挑战用途 **6 值**（`register` 为独立值；`login` vs `step_up` 分界＝是否已有会话） |
| `D3-A′` | D3 的「A′ 分页补口」 | 分页 `limit`+`cursor`、**不返回 `total`**、两类游标不得互喂 |
| `D3-B` / `D3-B′` / `D3-B″` | D3 的裁定 B / B′ / B″ | 错误码一律接口 §5.4；补登记 3 码；口令三码**固定顺序** + `details.violations[]` |
| `D8-E 第二版` | D8 的【正式裁定 D8-E（第二版）】 | 实现按 GB/T 39786 三级条款固定、二级为子集、判定与出证后置；TLS 责任转移不写"无影响" |
| `D8-E⑦` | D8-E 的 ⑦ | 密评等级只标「待澄清/待追认」、按预检清单落、**不进契约强制项**、不阻塞任何一件 |
| `D9 补口 ①②③` | D9 的「补口」 | `SDK契约` §8 真悬空（`since_version`/缓存失效/降级）；`页面规格 §4.1` 误判已否决；《错误码体系》不存在 |
| `D12` | §**D12** | `acr` 取值域：对外只 `AAL1|AAL2`，**`AAL3` 预留、本期任何路径不得产出** |
| `D13` | §**D13** | `login_policies` 三档 `tier_default` 必须有 seed（`000017`）+ 启动期恰一行 fail-fast |
| `D15` | §**D15** | SM4 信封 v2（先验 MAC 再解密）+ 用途域 **5 → 8（现 8，唯一清单见 D15③）** + "完整性失败 → 500、键缺失 → 503" 的对外码 + ADR-005 已 `Accepted` |
| `D14①②③④⑤` | §**D14** | 挑战 TTL/次数只从 `runtime_config` 读（`000018`）、`register_token` = HMAC-SM3 签名串、邀请码非空一律 400、`attempts_left` 两处口径、跨模块代落登记表 |
| `D4-C(5)` | D4 裁定 C 的第 (5) 条 | 链序 = `created_at`；`occurred_at` 原样并**参与 `integrity_tag`**；二者不同不算缺陷；下界守卫防跨月破链（完整裁定 `notes/audit-chain-order-2026-11-10.md`） |
| `D17` | §**D17** | 能力矩阵与 AC 账目的计数单位：`GJ-CAP-17/20/21` **无 AC 条目**、能力矩阵记 `DEFERRED(M5)`/`N/A(M2)`、与 `AC-AGENT-01~03` **不得双计**、22 能力与 90 判据**不得换算**；`90 = 83/0/0/6/0/1` 不受影响 |
| `D19` | §**D19** | `disabled_factors` 的物理落点 = `login_policies`（client 级 `client_override`、回落 `tier_default`），`clients` 无此列；`接口清单:211`/`页面规格:533` 的"来自 clients"是笔误、只改措辞不进 CH；**有效禁用集 = 平台级 `runtime_config`（`factors.<type>.enabled`，逐因子一键）∪ client 级，互不覆盖** |
| `D21` | §**D21** | `AC-REG-04` 的耗时阈值 = `runtime_config.register.timing.max_diff_ms`（int，默认 **50**，迁移 `000019`）；口径 = `register/start` 两条路径耗时中位数之差的**绝对值**上界，时钟源 = 进程单调钟；**超阈值 = 红**；键是**判据参数、服务不读、不进 `StartupKeys()`**、零代码改动 |
| `D20` | §**D20** | `audit_logs` 加 `audit_logs_created_id_idx (created_at, id)`（迁移 **`000020`**，父表建、自动套用新分区、零代码改动）；不加 DESC/`occurred_at` 前缀/`CONCURRENTLY`/partial；否决"链尾 tag 落 `runtime_config`"缓存方案；不进 CH、判据不动 |
| `D16①②③④⑤` | §**D16** | 数据面 S8 收口：断言 claim 逐项来源（`aud`=路由命中 client、`roles`=活跃 `role_bindings`、`exp=iat+60`、`jti`=UUIDv7）、准入基准=`login_policies.min_acr`、路由规则冻结（R11①）、SLO 通知与 `details.flow` 字段集登记 R12/R13 |
| `CH-8` | 契约变更清单 | `§3.3.1` 响应新增 `attempts_left`（纯加字段，**待追认**；追认前不作判据依据） |
| `D6(1)(6)` + `F0-5` | D6 裁定 (1)/(6) + F0 第 5 条 | SDK 配置 **29 键**（缺号 #7/#16/#17 不补）+ 环境变量逐段机械转换 |
| `P6` / `P7` / `P8` | §0 已生效前提 | 分支必须短命 / `docs/` 必须入库 / 契约变更清单已追认 + 记录法 |
| `R10` / `R11` | 残余待裁表末尾 | 引导流程（首个管理员+首把密钥+首个 client）/ 数据面逐条转发规则与容量（**只登记不实现**） |
