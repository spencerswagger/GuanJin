# 关津 · 语言无关 SDK 契约（`sdk-contract.md`）

| 项 | 内容 |
|---|---|
| 文档类型 | 实现前契约（语言无关）——22 条能力 + **29 项**配置的等价定义与冻结命名 |
| 主笔 | 关津 SDK · Nova（HBZ8HT） |
| 校对 | Spec（W87AB0）：命名冻结、条数、与基线 / `decisions.md` 的一致性 |
| 编写日期 | 2026-11-02（**v8**：按 PR #1 Owl 评审一次性修完 2 个 P2 + 4 个 P3，见 §8.4 E26–E32；另按 Gate 把关所指的 1 处 P3 文字不一致修正 C-1/C-2 的「10 条」，见 §8.4 E34） |
| 上游权威 | ① `/docs/decisions.md`（**D1 / D6(3)(4)(5) / D8-C′ / D10 / D11 / P4** + 契约变更清单 CH-4/CH-5）→ ② 索引 §2 → ③ `GuanJin-SDK契约.md` 正文 → ④ 接口清单 §5.2/§5.3/§5.4 |
| 作用 | **三份实现（Java 8 / Node / Go）的唯一共同真值**：能力 ID、配置键、错误码、canonical 串、fail-closed 行为、一致性向量 |
| 约束 | 只写契约与命名，**不含任何源文件**；`/docs/baseline/` 一字未改；不含迁移相关能力（P1） |
| 状态 | **待 Spec 校对**；标 ❓ 需 Spec/spencer 确认，标 ⛔ 已按 `decisions.md` 收口，标 ⏳ 依赖 CH 追认 |

> **标注约定**：`FROZEN` 冻结不改名；`DERIVED` 由冻结项机械推导（§1.1）；`REMOVED` 基线存在过但已废止，**不得实现**；`❓` 本文推定待确认；`⛔` 与基线冲突已裁定；`⏳` 依赖 `decisions.md` 契约变更清单追认（未追认前按基线回退）。

---

## 0. 一页结论

1. **22 条能力一个不多一个不少**，ID 为 `GJ-CAP-01..22`；三语言对外命名按**两层规则**（§1.5）：概念层逐字符相同、API 层逐格冻结，语义与判据完全一致（§4.1）。
2. **配置交付集 = 29 项，连续编号 1..29**（`decisions.md` D6-1）。原「32 项」作废：缺号 #7/#16/#17 **不补 key**。一句话交代来历——基线 §3.1 的编号是按 `GuanJinProperties` 字段声明顺序排的，缺的三个号正好是 direct 形态的 3 个键（`mode` + `Direct.userTokenJwksUri` + `Direct.userTokenIssuer`），索引 §2 定「只保留网关形态」后这 3 项失去存在理由、被从表中删掉但编号未重排，于是「号到 32、行剩 29」。**作废依据 = D6-1（禁编造契约）**，故本文不新增 key 补齐编号。
3. **三语言只允许两处差异**：打包物与宿主接入点。验签算法、canonical 串、能力 ID、错误码、model.conf 三层、JWKS 缓存、fail-closed、配置键名 **必须逐字节一致**（§4）。Go 的 module path 已定稿为 **`github.com/spencerswagger/guanjin`**（P4）；「SDK 是否另起独立 module」留 **Q10 交 spencer**（§5.3.2 给建议案 A）。
4. **一致性由「黄金向量」而非口头承诺保证**：同一批 JSON 向量 + 同一 runner 契约，三语言各跑一遍、CI 比对（§2.4、附录 C）。
5. **Java 8 裁决的真实代价**：基线 §4.4/§5.4 示例用了 JDK 11+ API（`java.net.http.HttpClient`、`String.isBlank()`、`var`），**Java 8 下不可编译**——禁用清单见 §5.1.1。
6. **交付结构（本版补齐）**：§2.1 能力表（语义 + 三语言命名）＋ **§2.1.1 能力 → 冻结端点 → fail-closed → 依赖关系**＋ **§2.1.2 密级唯一写法与取值词表**；§3.2 配置表（编号/键/类型/默认/必填/三语言等价）＋ **「安全约束」列**；**§1.4 命名原则 + §1.5 命名分层与 R-1..R-5 形态规则**。合起来即「22 能力 + 29 配置」的完整等价定义，Java 8 / Node / Go 三份实现**照表抄**即可，不许自行推导。

---

## 1. 冻结总纲

### 1.1 命名规则（`DERIVED`，机械可转，禁止手工特例）

| 对象 | 规范形态 | Java 8 | Node | Go |
|---|---|---|---|---|
| 能力 ID | `GJ-CAP-NN`（2 位，01..22） | 同（`Capabilities.CAP_01`） | 同（`CAP_01`） | 同（`Capability01`） |
| 配置键 | 点分小写 kebab：`guanjin.<段>.<键>`，**连续编号 1..29** | 原样作 `@ConfigurationProperties` 键 | 去 `guanjin.` → **点分 camelCase**（`security.injectSecurityContext`），与 Java 逐字符相同 | 去 `guanjin.` → 结构体字段，**每段首字母大写、点号保留**（`Security.InjectSecurityContext`） |
| 环境变量 | 配置键去 `guanjin.`，`.`→`_`，`-`→`_`，全大写，加前缀 `GUANJIN_` | 同名 | 同名 | 同名 |
| **Go 缩写大小写（封闭白名单）** | 只允许这 5 个缩写全大写，其余单词**只有首字母大写**：`ID` / `URI` / `URL` / `TTL` / `HMAC`。故 `client.id`→`Client.ID`、`gateway.jwks-uri`→`Gateway.JwksURI`、`jwks-cache-ttl`→`JwksCacheTTL`；**`jwks` 一律写 `Jwks`（不是 `JWKS`）** | — | — | 由键名**机械推导**，不得手写特例 |
| 枚举值 | 小写 snake（`memory`/`redis`） | 枚举 + 小写序列化 | 字符串字面量联合 | 自定义类型 + `Validate()` |
| 错误码 | 接口清单 §5.4（含 D3-F 新增 `STEP_ALREADY_SATISFIED`、D8-C′ 废弃 `SERVICE_DEGRADED`），**SDK 只消费不新增** | 常量类 | 常量对象 | 常量 + `error` 实现 |
| 时长 | 字符串 `<n><unit>`（`60s`/`10m`/`300s`），**规范形态是字符串** | `java.time.Duration` | 毫秒 `number`（内部） | `time.Duration` |
| 列表 | JSON 数组 | `List<String>` | `string[]` | `[]string` |

> 时长为什么定成字符串：三语言原生类型不同，只有字符串是同一份配置在三语言都可读、也能进一致性向量的形态。

### 1.2 语言无关类型（跨语言传值的最小集合）

`Principal{ sub, uname, roles[], acr, amr[], cid, client_id, iss, aud, iat, exp, jti }`、`Decision{ allow, reason }`、`ScopeFilter{ sql_fragment, params }`、`Snapshot{ client_id, sdk_version, version, model_conf_digest, permissions[], roles[{role,permissions[]}], interface_map[{path,method,resource,action}], default_role{role,permissions[]}, label_defs[]? }`、`BindingSet{ version, bindings[{user_id, roles[]}], issued_at }`。**序列化一律 snake_case**。

### 1.3 SDK 明确不做（越界清单，三语言同样禁止）

不定义授权策略；不做跨系统统一角色；不做登录流程（**因此 CH-1 的 `flow_id` 参数与 SDK 无关**）；不提供 `RequireRole` 类能力；不签发令牌、不管会话与撤销；不登记回调地址；不做文件级访问控制；不硬编码平台地址；**不做数据库迁移**（P1）；不提供「业务系统直连」形态（索引 §2）。

### 1.4 命名原则（三语言共用同一套概念名，单列成节）

> 一句话：**概念名只有一个来源——基线冻结的名字；三语言只允许「同一概念名的语法外壳」不同，不允许换名。**

| # | 原则 | 具体规定 | 反例（一律不许） |
|:--:|---|---|---|
| N1 | **概念名照抄冻结名** | 表名（14 张）、端点名（65 个具名 + 1 条数据面通配转发）、概念名（`X-GuanJin-Assertion`、`role_bindings`、`policy_snapshots`、`user_visited_clients`、`acr`/`amr`、档位 L1/L2/L3、AAL1/AAL2）**逐字照抄**，SDK 文档与 API 注释里都用基线写法 | 把 `X-GuanJin-Assertion` 改叫 `Assertion`/`AssertToken`；把 `role_bindings` 写成 `roleBindings` 表 |
| N2 | **端点名不得改名/加别名** | 契约里出现的端点一律写全路径原文（`POST /api/v1/agent/report`、`GET /api/v1/agent/policies`、`GET /.well-known/jwks.json`）；大小写、复数、前缀 `/api/v1` 一字不改；不提「别名端点」「兼容端点」 | `/api/v1/agent/reportPolicy`、`/api/v1/sdk/report`、`/api/v1/jwks`（后者**全库不存在**，见 C-2） |
| N3 | **两层命名，各管一段** | **概念层**（跨语言**逐字符相同**）：能力 ID、配置键、环境变量、claim / 传输字段、错误码、端点路径、canonical 段名、枚举值。**API 层**（逐格冻结）：每条能力在三语言的主入口标识符，**以 §2.1 三列为唯一真值**；形态差异只许落在 **R-1..R-5** 五类（§1.5），**不许各队自行推导** | 概念层改名（`GJ_TOKEN_INVALID`、`guanjin.platform.baseUrl`）；API 层"按规则自己推"推出表里没有的名字 |
| N4 | **配置键固定 `guanjin.` + 小写 kebab** | 29 个键逐字照抄（§3.2）；环境变量按 §1.1 机械转换（`GUANJIN_` + 大写 + `_`）；**不提供别名键、不提供旧键兼容、不提供驼峰变体** | `guanjin.platform.baseUrl`、`guanjin.jwks`、`GUANJIN_BASE_URL` |
| N5 | **claim / 字段名 snake_case 照抄** | JWT claim（`sub`/`uname`/`roles`/`acr`/`amr`/`cid`/`iss`/`aud`/`iat`/`exp`/`jti`）与跨语言传值（`Principal`/`Decision`/`Snapshot`/`BindingSet`）序列化**一律 snake_case**（§1.2）；Go 结构体字段是 Go 语法外壳，**JSON tag 必须还是 snake_case** | `principal.userName`、Go `json:"userName"` |
| N6 | **错误码只消费不改写** | 三语言都直接返回接口清单 §5.4 的字面码（`TOKEN_INVALID`、`KEY_UNAVAILABLE`…），**不自造前缀、不做语言化改写**；`message` 可本地化，`code` 不可 | `GJ_TOKEN_INVALID`、`invalidToken`、`TOKEN_INVALID_CN` |
| N7 | **避免同义词扩散** | 同一动作只许一个词（`verify`/`pull`·`report`/`strip` 已冻结）；新增 API 必须先在本文登记再实现 | 同时存在 `pull`/`fetch`/`load` 三种拉取 |
| N8 | **改名流程** | 任何改名 = 改契约 → 走 ADR + Spec 校对 + `decisions.md` 记录；**不许在实现里先改、文档后追** | 「代码里已经叫 xxx 了，文档改一下」 |

### 1.5 命名分层与形态规则（R-1..R-5，机械可执行）

> 这一节回答 Owl 的追问：**哪些位置必须逐字符相同、哪些允许语言惯用差异、三队怎么保证"照表能得到唯一答案"。** 结论：**表即真值（逐格冻结）+ 差异只许来自 R-1..R-5 五类 + CI 反射比对**，三者缺一不可。

| 层 | 覆盖对象 | 要求 | 机械校验 |
|---|---|---|---|
| **概念层** | 能力 ID `GJ-CAP-01..22`、29 个配置键、环境变量、JWT claim、跨语言传输字段、错误码、端点路径、canonical 段名、枚举值（`memory`/`redis`）、表名（在文档与快照字段里出现时） | **逐字符相同**：大小写、连字符、下划线、前缀一律不许变 | 三仓 grep + 反射比对，逐字命中 |
| **API 层** | 每条能力的对外类型名 / 方法名 / 函数名（§2.1 的 Java / Node / Go 三列） | **以 §2.1 表格为唯一真值（逐格 `FROZEN`）**；差异必须能归到 R-1..R-5 之一 | Java 反射取方法名、Node 取导出符号、Go 取 `go doc` 符号，逐格比对 |

**形态规则（封闭枚举：新增第六类必须走 ADR，不得在实现里就地发明）**

| 规则 | 内容 | 适用范围 |
|---|---|---|
| **R-1 大小写与接收者** | Java / Node 方法名**小驼峰**；Go 导出方法**大驼峰** + 显式接收者 `(*T)`；同一格内**词序不得调整** | 全部 22 条 |
| **R-2 构造器（builder / factory）** | Java `<Type>.builder()`；Node `build<Type>()`（Node 无静态 builder 惯例）；Go `New<Type>Builder()`（Go 的 `New*` 构造惯例） | `GJ-CAP-13`（`AuthzModel` → `AuthzModel.builder()` / `buildAuthzModel()` / `authz.NewModelBuilder()`） |
| **R-3 Go 的 context 写入口** | 写入 `context.Context` 的能力，Go 一律 `With<Concept>(ctx, ...)`（Go 惯例） | `GJ-CAP-10`（`WithPrincipal`） |
| **R-4 Java 宿主接口限定词** | 能力落在 Spring Security 既有概念上时，允许带 `Granted` 限定词（Spring 的 `GrantedAuthority`）；**仅此一处**，且必须逐格写在表里 | `GJ-CAP-11`（`GrantedAuthoritiesResolver`） |
| **R-5 宿主适配入口** | 「把身份交给宿主框架」的入口，三语言用各宿主惯用名；**概念名仍是 `InjectHostContext`**，且该入口必须在代码里以常量或注释回指 `GJ-CAP-10`（可机械校验） | `GJ-CAP-10`（Java `toAuthentication` = 产出 Spring `Authentication`；Node `injectPrincipal`；Go `WithPrincipal`） |

**三处"看起来打架"的地方，差异类别与理由（一次定死，不再讨论）**

| 能力 | 语言无关名 | Java / Node / Go（表内原样） | 差异类别 | 为什么必须这样，而不是改成同一词根 |
|---|---|---|---|---|
| `GJ-CAP-10` | `InjectHostContext` | `toAuthentication` / `injectPrincipal` / `WithPrincipal` | R-5 + R-3 | Java 侧"注入"的产物就是 Spring 的 `Authentication`，业务方要在 `SecurityContext` 里看到它；Go 侧写 `context.Context` 的惯例是 `With*`。强行统一成 `injectHostContext` 会得到一个"名字像注入、返回却是 `Authentication`"的 API，反而更难用 |
| `GJ-CAP-11` | `AuthoritiesResolver` | `GrantedAuthoritiesResolver.resolve` / `authoritiesResolver` / `AuthoritiesResolver func(...)` | R-4 + R-1 | Java 侧是 Spring Security 的扩展点，`Granted` 是 Spring 概念前缀；Node/Go 没有这个前缀，去掉才对 |
| `GJ-CAP-13` | `AuthzModelBuilder` | `AuthzModel.builder()` / `buildAuthzModel()` / `authz.NewModelBuilder()` | R-2 + R-1 | Java 用静态 `builder()` 链、Node 无静态方法故用工厂函数、Go 用 `New*` 构造；三者都是"造一个 `AuthzModel`"，语义同一 |

> **裁定与理由（回应 Owl P2-2：改 N3 措辞，不改表）**：①**表里的 22 格是宿主 API 的真实形态**，硬改成同一词根会迫使其中两语言写出反惯用名的 API（例如 Java 叫 `injectHostContext` 却返回 `Authentication`、Go 叫 `injectPrincipal` 却写 context），可读性和可维护性都更差；②**表已逐格冻结**，对三队来说"照表抄"就是唯一答案，比"照规则推"更少歧义（规则会诱使实现者自己外推）；③**差异来源被封闭在 R-1..R-5**，新增第六类必须 ADR，因此不会退化成"各队各写各的"；④**配套 CI 反射比对**，把"照表"从口头约定变成可执行校验（不一致即 FAIL）。**代价**：R-2..R-5 这四条规则本身要维护——所以它们只允许覆盖上表这 3 条能力（CAP-10/11/13），别处出现同类差异即视为违规。



---

## 2. 能力契约（22 条）

### 2.1 能力总表（ID / 语义 / 必需性 / 三语言对外命名）

> 「必需」= 缺了破坏默认安全或接入契约，**不可关闭**。三语言命名同格列出，Spec 只需校对**命名**与**条数**。

| ID | 语言无关名 | 语义一句话 | 必需 | Java 8 | Node | Go |
|---|---|---|:--:|---|---|---|
| `GJ-CAP-01` | `VerifyAssertion` | 验 `X-GuanJin-Assertion` 的 SM2 签名（公钥来自 JWKS），**无跳过开关** | 必须 | `AssertionVerifier.verify(raw)` | `verifyAssertion(raw, ctx)` | `(*Verifier).Verify(ctx, raw)` |
| `GJ-CAP-02` | `SelectKeyByKid` | 按 JWT header 的 `kid` 选公钥，与用户令牌密钥分离 | 必须 | `JwksClient.publicKey(kid)` | `jwks.publicKey(kid)` | `(*Jwks).PublicKey(ctx, kid)` |
| `GJ-CAP-03` | `JwksCache` | 启动拉一次并缓存（TTL 见配置 #13），不逐请求回源 | 必须 | `JwksClient.refresh()` | `jwks.refresh()` | `(*Jwks).Refresh(ctx)` |
| `GJ-CAP-04` | `RefreshOnUnknownKid` | 遇未知 `kid` 强制刷新一次（应对密钥轮换） | 必须 | `JwksClient.publicKey` 内部 | 同 | 同 |
| `GJ-CAP-05` | `ValidateExpIat` | 校验 `exp`/`iat`，容忍时钟偏移（默认 ±60s，配置 #10） | 必须 | `AssertionVerifier.validateClaims(a, gw)` | `validateClaims(a, opts)` | `(*Verifier).ValidateClaims(a, cfg)` |
| `GJ-CAP-06` | `ValidateIssAud` | `iss=guanjin-gateway`；`aud`=本系统 `client.id` | 必须 | 同上 | 同上 | 同上 |
| `GJ-CAP-07` | `ReplayGuard` | `jti` 在窗口内唯一，重复即拒（`memory`/`redis`，配置 #18） | 必须 | `ReplayGuard.firstUse(jti, exp)` | `replayGuard.firstUse(jti, exp)` | `(*ReplayGuard).FirstUse(ctx, jti, exp)` |
| `GJ-CAP-08` | `StripAssertionHeader` | 转发下游前剥离该 header，同时丢弃客户端伪造的同名头 | 必须 | `HeaderStrippingRequestWrapper` | `stripHeader(req, name)` | `stripHeader(r, name)` |
| `GJ-CAP-09` | `ReadIdentity` | 解析 `sub/uname/roles[]/acr/amr[]/cid` 为 `Principal` | 必须 | `GuanjinPrincipal.from(a)` | `principalFrom(a)` | `PrincipalFrom(a)` |
| `GJ-CAP-10` | `InjectHostContext` | 注入宿主身份上下文（Java `SecurityContext`；Node `req`；Go `context.Context`） | 必须 | `GuanjinAuthenticationConverter.toAuthentication(p)` | `injectPrincipal(req, p)` | `WithPrincipal(ctx, p)` |
| `GJ-CAP-11` | `AuthoritiesResolver` | 扩展点：业务方把身份属性映射为业务权限 | 可选 | `GrantedAuthoritiesResolver.resolve(p)` | `authoritiesResolver(p)` | `AuthoritiesResolver func(Principal) []string` |
| `GJ-CAP-12` | `AuthzEngine` | 三层 `model.conf`（RBAC / 行级 ABAC / 密级 BLP）逐层生效 | 必须 | `Authz`（静态门面） | `Authz`（对象） | `(*Authz)` |
| `GJ-CAP-13` | `AuthzModelBuilder` | **完整签名按索引 §4.2-25（D11 冻结）**：`role(name).can(resource, ...actions)`、`role(name).withClearance(level)`、`resourceGroup(name).contains(resource)`、`defaultRole(name).can(...)`、`build()`；**`dataLabel()` 与裸 `clearance()` 同属作废**（D11 补口 C1，两者均不得作别名），密级取值与唯一写法见 **§2.1.2** | 必须 | `AuthzModel.builder()` | `buildAuthzModel()` | `authz.NewModelBuilder()` |
| `GJ-CAP-14` | `RequirePermission` | 一行保护接口；**不提供** `RequireRole` | 必须 | `@RequirePermission(resource, action)` | `requirePermission(res, act)` | `RequirePermission(res, act)` |
| `GJ-CAP-15` | `CheckRow` / `CheckOwner` | 行级 / 数据实例级判定，`auditor` 自动豁免 | 必须 | `Authz.checkRow()` / `Authz.checkOwner()` | `authz.checkRow()` / `authz.checkOwner()` | `(*Authz).CheckRow()` / `.CheckOwner()` |
| `GJ-CAP-16` | `CheckLabel` | 密级判定：主体密级 ≥ 客体密级。**形参名固定 `objLevel`**（D11 补口 C2：客体侧叫 `objLevel`，主体侧才叫 `level`） | 必须 | `Authz.checkLabel(objLevel)` | `authz.checkLabel(objLevel)` | `(*Authz).CheckLabel(objLevel)` |
| `GJ-CAP-17` | `Scope` | 行级条件下推为查询过滤，避免全量捞出再过滤 | 必须 | `Authz.scope(resource)` | `authz.scope(resource)` | `(*Authz).Scope(resource)` |
| `GJ-CAP-18` | `DefaultRole` | 未绑角色时的兜底角色，**权限最小**，不得「已登录即全部」 | 必须 | `AuthzModel.builder().defaultRole(n)` | `defaultRole: name` | `builder.DefaultRole(name)` |
| `GJ-CAP-19` | `PublicPathDeclare` | `@PublicApi` + `public-paths` 显式声明；deny-by-default | 必须 | `@PublicApi` + 配置 #24 | `publicPaths` + `isPublic(path)` | `PublicPaths` + `IsPublic(path)` |
| `GJ-CAP-20` | `PolicyReport` | 启动 + 变更时 `POST /api/v1/agent/report`（HMAC-SM3），业务方不写上报代码 | 必须 | `PolicyReporter.report(Snapshot)` | `reporter.report(snapshot)` | `(*Reporter).Report(ctx, snap)` |
| `GJ-CAP-21` | `PolicyPull` | 按需 `GET /api/v1/agent/policies`（HMAC-SM3）+ 缓存；索引 §4.2-24 方案 A 下仅作补漏 | 可选 | `PolicyPuller.pull(sinceVersion)` | `puller.pull(since)` | `(*Puller).Pull(ctx, since)` |
| `GJ-CAP-22` | `StartupSelfCheck` | 启动自检 + 打印全部放行路径 | 必须 | `GuanjinStartupChecker` | `startupSelfCheck(opts)` | `StartupSelfCheck(cfg)` |

**条数自检**：22 行，与 `GuanJin-SDK契约.md` §2 的 #1..#22 **同序**一一对应（附录 A），无重排/合并/拆分。**「必须」20 条、「可选」2 条**（仅 `GJ-CAP-11` `AuthoritiesResolver`、`GJ-CAP-21` `PolicyPull`；其余 20 条不可关闭）。

### 2.1.1 能力 → 冻结端点 → fail-closed → 与其它能力的关系

> 端点名一律用**冻结名**（接口清单 §1/§3.10/§5.1；N2）。**「无端点」= 纯本地能力**，不产生任何出站请求——22 条里只有 3 个**出站端点**（JWKS / report / policies，涉及 `GJ-CAP-03/04/20/21`），其余全部本地执行；另有 `GJ-CAP-07` 在 `replay-store=redis` 时连**宿主注入的**本地 Redis（不是平台端点）。这是「平台不在每个请求路径上」的落地证明。

| ID | 对应端点（冻结名） | fail-closed | 与其它能力的关系 |
|:--:|---|:--:|---|
| `GJ-CAP-01` | 无端点（消费数据面注入的 `X-GuanJin-Assertion`；数据面通配转发不是具名端点） | ✅ 验签失败即拒 | 入口能力；**依赖 02/03/04 取公钥**，产出交 05/06/07/09 |
| `GJ-CAP-02` | 无端点（读 `GET /.well-known/jwks.json` 的结果，仅经 03 拉取） | ✅ 无匹配 `kid` 即拒 | 03 的下游消费者；01 的前置 |
| `GJ-CAP-03` | **`GET /.well-known/jwks.json`** | **条件性**：有缓存 → 沿用（非降级）；无缓存 → ✅ 拒（503 `KEY_UNAVAILABLE`） | 启动期唯一拉取点；服务 02/04；受配置 #12/#13 约束 |
| `GJ-CAP-04` | **`GET /.well-known/jwks.json`**（未知 `kid` 触发的强制重拉） | 同 03（刷不到且无缓存 → ✅） | 03 的补充路径；受配置 #14 控制 |
| `GJ-CAP-05` | 无端点 | ✅ 越界即拒 | 01 之后、09 之前；用配置 #10 |
| `GJ-CAP-06` | 无端点 | ✅ 不符即拒 | 与 05 同批执行；`aud` 取配置 #9 |
| `GJ-CAP-07` | 无端点（`redis` 存储时只连宿主提供的 Redis，不调平台） | ✅ 存储不可用也拒 | 05/06 之后、09 之前；受配置 #15/#18 |
| `GJ-CAP-08` | 无端点（作用于交给宿主的请求视图） | ✅ 无法剥离则不放行 | 时序受 D10 六步约束（§4.5）；必须先于 10、晚于 01 |
| `GJ-CAP-09` | 无端点 | 否（输入已验签） | 01/05/06/07 全部通过后才执行；产出喂 10/12..18 |
| `GJ-CAP-10` | 无端点 | 否（纯注入） | 09 的下游；可选叠加 11 |
| `GJ-CAP-11` | 无端点 | ✅ 扩展点抛异常 → 请求失败（不静默降级） | 10 的可选增强；**可选**能力 |
| `GJ-CAP-12` | 无端点（`model.conf` 本地加载） | ✅ 模型加载/完整性校验失败 → 拒判定 | 13 的运行时；被 14..18 使用；受配置 #15/#17 |
| `GJ-CAP-13` | 无端点 | ✅ 声明非法 → 启动失败 | 12 的声明入口；18 的载体 |
| `GJ-CAP-14` | 无端点 | ✅ 无声明即拒（deny-by-default） | 12..18 的接口级门；与 19 互补（19 管放行，14 管保护） |
| `GJ-CAP-15` | 无端点 | ✅ 判定异常 → 拒 | 12 的行级分支；可与 16/17 叠加 |
| `GJ-CAP-16` | 无端点 | ✅ 主体密级 < 客体密级 → 拒 | 客体密级由业务在调用点传值（D11，不新增 API） |
| `GJ-CAP-17` | 无端点 | ✅ 无法计算过滤条件 → 拒（绝不返回全量） | 12 的列表分支；与 15 同源语义 |
| `GJ-CAP-18` | 无端点 | ✅ 兜底且权限最小 | 13 的声明项；无角色用户的后备 |
| `GJ-CAP-19` | 无端点 | ✅ 未声明即拒 | 与 14 构成「保护 + 放行」两侧；受配置 #24/#25 |
| `GJ-CAP-20` | **`POST /api/v1/agent/report`**（HMAC-SM3，§4.3） | 否（上报失败不阻塞业务） | 消费 13 的声明（`Snapshot`）；受配置 #6/#19/#20/#21 |
| `GJ-CAP-21` | **`GET /api/v1/agent/policies`**（HMAC-SM3） | 否（拉不到按 18 兜底） | 消费 20 的版本基线；**可选**能力；受配置 #22/#23 |
| `GJ-CAP-22` | 无端点 | ✅ 自检失败 → 启动失败 | 启动期最先执行；校验 02/03（JWKS 可达）与 #2/#5/#6 |

**端点汇总（SDK 只碰这 3 条 + 1 个入站 header，与「两个 Agent 面接口 + 一个 JWKS」一致）**：

| 端点（冻结名） | 方向 | 谁调用 | 鉴权 |
|---|---|---|---|
| `GET /.well-known/jwks.json` | SDK → 平台 | `GJ-CAP-03/04` | 匿名（公开公钥） |
| `POST /api/v1/agent/report` | SDK → 平台 | `GJ-CAP-20` | Agent 签名（HMAC-SM3） |
| `GET /api/v1/agent/policies` | SDK → 平台 | `GJ-CAP-21` | Agent 签名（HMAC-SM3） |
| `X-GuanJin-Assertion`（入站 header，非端点） | 数据面 → SDK | 数据面逐请求注入 | 平台 SM2 签名，SDK 验签 |

> **SDK 不得调用**：`/oauth/introspect`（硬规则「禁止每请求校验」，接口 §5.8）、`/userinfo`（SDK 拿不到用户令牌）、`/api/v1/*` 管理面。出现即越界（§1.3）。

### 2.1.2 密级的唯一写法与取值词表（D11 补口 C1/C2 + B1，转写）

> 为什么要单列：基线 SDK 示例里同一个"密级"出现过**三个名字**（`dataLabel()`、裸 `.clearance(n)`、`withClearance(level)`）与**一个自相矛盾的取值注释**（`.clearance(3) // 秘密` 而 `dataLabel(...,2) // 秘密`）。三语言若各挑一个写法，M5 的「主体 ≥ 客体」判定就无法唯一。故本节把写法与取值都**闭死**。

| 项 | 冻结内容 | 依据 |
|---|---|---|
| **主体密级（唯一写法）** | `role(name).withClearance(level)`（builder 链上，`level` 为整数） | 索引 §4.2-25 + D11 |
| **客体密级（唯一入口）** | `Authz.checkLabel(objLevel)` —— 业务在**调用点**把值传进来；**不声明式打标** | D11 方案① + 补口 C2 |
| **两个作废名** | `dataLabel(resource, level)`、**裸 `clearance(n)`** 一并作废，**均不得留别名、不得另造替代名**（`withClearance` 不是"别名"，它是唯一名） | D11 补口 C1 |
| **取值词表** | `1 = 内部`、`2 = 秘密`、`3 = 机密`（数值越大越敏感）；**「公开」不占级**：资源不声明 label → **跳过**密级判定，**不存在 0 级** | D11 补口 B1 + 权限模型 §3.4「未声明即不判定」 |
| **判定规则** | 主体密级 **≥** 客体密级放行；主体未声明密级视为最低（只能访问"公开"资源） | `GJ-CAP-16` |
| **基线示例怎么办** | 基线 §4.2 的 `.clearance(3) // 秘密` 注释属**笔误**（3 = 机密），与 `dataLabel("order_items", 2) // 秘密` 的矛盾由此消解；**基线只读、不回写**，靠本节 + 门禁表达 | D11 |
| **门禁口径（给三语言 CI）** | 禁词正则 **`(?<![A-Za-z0-9_])clearance\(`**（大小写敏感）→ `withClearance(` 不命中，`.clearance(3)`、`clearance(` 命中；`dataLabel` 同样禁。**扫描范围只限 SDK 三语言仓**（源码 + `examples/` + `README*` + 仓内 `docs/`），**不含 `/.octop`、`/docs/baseline/`**（作废声明本身必须能写出旧名）。**本文件例外**：`docs/sdk-contract.md` 是作废声明与词表登记处，扫描时**整体排除**——否则登记处被自己的门禁点红（与 D11「作废声明必须能写出旧名」同源） | D11 补口 C1 + Gate v9 `AC-ENG-08`③ |

**三语言落点（同义同形，只有语法外壳差异）**：

| 语言 | 主体密级 | 客体密级调用 | 禁出现 |
|---|---|---|---|
| Java 8 | `role(n).withClearance(level)` | `Authz.checkLabel(objLevel)` | `dataLabel(`、`clearance(`（不带 `with`） |
| Node | `role(n).withClearance(level)` | `authz.checkLabel(objLevel)` | 同上 |
| Go | `Role(n).WithClearance(level)` | `(*Authz).CheckLabel(objLevel)` | 同上 |

> 本条**不影响 M1**（`GJ-CAP-13/16` 只在 M5 生效）；**能力仍 22 条、配置仍 29 项**。
>
> **给门禁实现的提醒**：本节必然出现旧名（作废声明要写得出旧名），因此扫 SDK 仓时**整体排除本文件**，并同步在 `examples/` 与 `README` 里保持零旧名——否则"登记处免税、实现处零容忍"这条会反过来变成漏洞。

### 2.2 能力的三语言语义锚点（同构判据，Gate 可直接断言）

| 能力组 | 必须一致的判据 |
|---|---|
| `01..06` | 同一令牌 + 同一 JWKS → 三语言**同 allow/同 reject 原因**；`exp` 边界在 `exp+skew` 与 `exp+skew+1` 两侧各测 |
| `07` | 同一 `jti` 第二次必拒；`memory` 与 `redis` 窗口语义相同（键、TTL） |
| `08` | 下游 handler 与日志采集器**都取不到**该 header；客户端伪造的同名头被丢弃（不是覆盖） |
| `12..18` | 同一 `model.conf` + 同一策略 + 同一请求 → 三语言**同 allow/deny**；`scope()` 片段语义等价（不要求字面相同，§4.2） |
| `20..21` | 同一向量 → 三语言签名**逐字节相同**（§4.3） |
| `22` | 启动日志含完整放行清单；`public-paths` 为空时只含内置白名单 |

### 2.3 越界清单（误加即缺陷）

会话管理、令牌签发/刷新、撤销清单比对、回调地址自动登记、`user_visited_clients` 上报、`RequireRole`、数据库迁移、`direct` 形态、跳过验签开关、关闭 fail-closed 开关。

### 2.4 同构保证机制（黄金向量）

- 向量目录（仓库内，非代码）：`spec/vectors/agent-hmac.json`、`assertion-verify.json`、`authz-decisions.json`、`config-mapping.json`。
- 每语言同一 runner 约定：`<pkg> conformance --vectors <dir> --out <file>`，输出 `[{vector_id, expected, actual, pass}]`，**退出码 0 仅当全 pass**；CI 三语言各跑一次并比对 `vector_id` 集合相同。
- `agent-hmac.json` 必含 `decisions.md` D1 要求的三类：空 query、多 query 排序、空 body；三语言签名**逐字节相同**（含 Base64 形态）。

---

## 3. 配置契约（29 项，连续编号）

### 3.1 「32」的来历与作废依据（只交代一次）

| 段 | 键数 | 基线原编号 | 依据 |
|---|:--:|---|---|
| `guanjin.enabled` | 1 | 1 | §3.1 #1 |
| `platform.*` | 3 | 2–4 | §3.1 #2–#4 |
| `client.*` | 2 | 5–6 | §3.1 #5–#6 |
| **`guanjin.mode`** | **1** | **7** | §4.1 yml 里 `mode:` 紧跟 `client`、在 `gateway` 之前 |
| `gateway.*` | 8 | 8–15 | §3.1 #8–#15（连续） |
| **`direct.user-token-jwks-uri` / `direct.user-token-issuer`** | **2** | **16–17** | `GuanjinProperties.Direct` 恰有这 2 个字段，声明顺序在 `Gateway` 后、`Authz` 前 |
| `authz.*` | 4 | 18–21 | §3.1 #18–#21（连续） |
| `agent.*` | 5 | 22–26 | §3.1 #22–#26（连续） |
| `public-paths`×2 + `security.*`×3 + `startup.*` | 6 | 27–32 | §3.1 #27–#32（连续） |
| **合计** | **32** | | `1+3+2+1+8+2+4+5+6 = 32` ✔ |

**结论**：缺的 #7/#16/#17 **正好是 direct 形态的全部 3 个键**；索引 §2「只保留网关形态」后它们被删除但编号未重排 → 「号到 32、行剩 29」。**作废依据 D6-1**：以实有 29 项为交付集，「32 项」不再使用，**不新增 key 补齐编号**（禁编造契约）。3 项的处置见 §3.3。本条差异按 `decisions.md` **D6-1** 登记 **ADR-003**（「29 项 vs 32 项」编号空洞），仓库 README 同步标注，避免后来者把缺号当遗漏再补。

### 3.2 29 项冻结键（`FROZEN`，连续编号 1..29）

| # | 规范键 | 类型 | 默认 | 必填 | Java 8 | Node | Go | 安全约束（三语言同） |
|---|---|---|:--:|:--:|---|---|---|---|
| 1 | `guanjin.enabled` | bool | `true` ⛔C-1 | 否 | `enabled` | `enabled` | `Enabled` | **暗开关封口**：仅 `local\|test` 可 `false`，其它环境 `false` → **启动失败**；`false` = 不注册过滤器（等价未接入），**不是跳过验签** |
| 2 | `guanjin.platform.base-url` | string(url) | — | **是** | `platform.baseUrl` | `platform.baseUrl` | `Platform.BaseURL` | 必填、**无内置默认**（禁硬编码）；含 scheme+host、**不带尾斜杠**；值即信任源，改它=换平台 |
| 3 | `guanjin.platform.connect-timeout` | duration | `2s` | 否 | `platform.connectTimeout` | `platform.connectTimeout` | `Platform.ConnectTimeout` | 超时**即失败**，不得重试后放行；上限建议 ≤ 5s（否则启动自检被拖死） |
| 4 | `guanjin.platform.read-timeout` | duration | `3s` | 否 | `platform.readTimeout` | `platform.readTimeout` | `Platform.ReadTimeout` | 同上；`GJ-CAP-20` 退避重试的总时长以此为单次上界 |
| 5 | `guanjin.client.id` | string | — | **是** | `client.id` | `client.id` | `Client.ID` | 必填；对应 `clients` 表一行；**一系统一 id，不得多系统共用**；同时作 `aud` 期望值与 HMAC 标识 |
| 6 | `guanjin.client.hmac-secret` | string | — | **是** | `client.hmacSecret` | `client.hmacSecret` | `Client.HMACSecret` | **环境变量注入，禁入仓库/禁明文配置文件/禁日志**；每系统独立；轮换按 `POST /api/v1/clients/{id}/secret/rotate` 并行窗口 |
| 7 | `guanjin.gateway.assertion-header` | string | `X-GuanJin-Assertion` | 否 | `gateway.assertionHeader` | `gateway.assertionHeader` | `Gateway.AssertionHeader` | 改名**不改变剥离义务**；匹配大小写不敏感；不得配置成 `Authorization` 等宿主已用头 |
| 8 | `guanjin.gateway.issuer` | string | `guanjin-gateway` | 否 | `gateway.issuer` | `gateway.issuer` | `Gateway.Issuer` | 改值 = 放宽信任域（接受非本平台签发的断言）→ 需 ADR；默认不得动 |
| 9 | `guanjin.gateway.aud` | string | `${client.id}` | 否 | `gateway.aud` | `gateway.aud` | `Gateway.Aud` | 偏离 `client.id` = 接受发给别家的令牌（跨系统挪用）→ 必须显式评审 |
| 10 | `guanjin.gateway.clock-skew` | duration | `60s` | 否 | `gateway.clockSkew` | `gateway.clockSkew` | `Gateway.ClockSkew` | 只允许 `s` 级小值；`>300s` 视为风险配置（Gate 告警），它同时放大 `exp` 重放口子 |
| 11 | `guanjin.gateway.replay-window` | duration | `60s` | 否 | `gateway.replayWindow` | `gateway.replayWindow` | `Gateway.ReplayWindow` | **必须 ≥ 令牌 `exp`（60s）**；配置得比 `exp` 小会漏检重放 → 启动校验失败 |
| 12 | `guanjin.gateway.jwks-uri` | string(url) | `${platform.base-url}/.well-known/jwks.json` ✅C-2 | 否 | `gateway.jwksUri` | `gateway.jwksUri` | `Gateway.JwksURI` | **只允许平台 JWKS 基址口径**（C-2）；旧值 `/api/v1/jwks` **全库不存在**、首启 404 → 禁写；跨域/第三方地址 = 换信任源，需 ADR。**执行形态**：SDK 启动期校验 `gateway.jwks-uri` 与 `platform.base-url` **同源**（`scheme` + `host` + `port` 一致），不一致即**启动失败**（本条为执行上述已冻结约束，不新增键/默认值/必填性，不提供任何放行开关；裁定与完整执行形态见 `notes/sdk-jwks-uri-同源校验-2026-11-10.md`） |
| 13 | `guanjin.gateway.jwks-cache-ttl` | duration | `10m` | 否 | `gateway.jwksCacheTtl` | `gateway.jwksCacheTtl` | `Gateway.JwksCacheTTL` | 公钥公开，缓存**不构成放行降级**；TTL 只影响轮换生效时延 |
| 14 | `guanjin.gateway.jwks-unknown-kid-force-refresh` | bool | `true` | 否 | `gateway.jwksUnknownKidForceRefresh` | `gateway.jwksUnknownKidForceRefresh` | `Gateway.JwksUnknownKidForceRefresh` | 置 `false` **不降低安全性**，但密钥轮换后新 `kid` 全部 401（可用性风险）→ 生产保持 `true` |
| 15 | `guanjin.authz.enabled` | bool | `true` ⛔C-1 | 否 | `authz.enabled` | `authz.enabled` | `Authz.Enabled` | 同 #1 profile 门控；`false` = **不注册判定**（等价未接入），**绝不是"全部放行"**；非 `local\|test` 置 `false` → 启动失败。**执行形态**：`authz.enabled=true` 且容器内无 `AuthzModel` bean → **启动失败**（不静默降级为"不判定"）；报错须点名修法——声明 `AuthzModel` bean（基线 §4.2 ② 的接入步骤本就要求，见 `GuanJin-SDK契约.md:287-292`），或**仅** `local\|test` profile 置 `authz.enabled=false`；不提供任何放行开关；Go/Node 同形态（装配期失败）。裁定见 `notes/sdk-authz-enabled-默认-2026-11-10.md` |
| 16 | `guanjin.authz.default-role` | string | `user` | 否 | `authz.defaultRole` | `authz.defaultRole` | `Authz.DefaultRole` | **权限最小**，禁「已登录即全部」（等保 e）；名字必须与 `builder().defaultRole()` 一致，否则启动失败 |
| 17 | `guanjin.authz.model-conf` | resource | `classpath:/guanjin/model.conf` | 否 | `authz.modelConf` | `authz.modelConf`（文件路径） | `Authz.ModelConf`（文件路径） | 不建议修改（三层模型是合规基线）；外部文件须做**完整性校验**，失败 → 拒绝授权判定（§4.4） |
| 18 | `guanjin.authz.replay-store` | enum | `redis` ✅C-4 | 否 | `authz.replayStore` | `authz.replayStore` | `Authz.ReplayStore` | 默认 `redis`；`memory` 仅 `local\|test`（多副本用 `memory` = `jti` 防重放失效 = 降级放行）；Redis 参数**不由 SDK 配置**（§3.5） |
| 19 | `guanjin.agent.report.enabled` | bool | `true` | 否 | `agent.report.enabled` | `agent.report.enabled` | `Agent.Report.Enabled` | `false` 不影响判定，但平台侧策略快照失真（审计/回溯能力下降）→ 需报备，不是"性能优化项" |
| 20 | `guanjin.agent.report.on-startup` | bool | `true` | 否 | `agent.report.onStartup` | `agent.report.onStartup` | `Agent.Report.OnStartup` | 无安全影响；关掉会使快照与实际声明不一致 |
| 21 | `guanjin.agent.report.on-change` | bool | `true` | 否 | `agent.report.onChange` | `agent.report.onChange` | `Agent.Report.OnChange` | 同上；变更不上报会让平台看到过期权限目录 |
| 22 | `guanjin.agent.pull.enabled` | bool | `false` ✅C-5 | 否 | `agent.pull.enabled` | `agent.pull.enabled` | `Agent.Pull.Enabled` | 默认 `false`（方案 A 零回源）；置 `true` = **新增一个回源面**（受 HMAC 保护），需评估平台负载与可用性依赖 |
| 23 | `guanjin.agent.pull.interval` | duration | `30s` | 否 | `agent.pull.interval` | `agent.pull.interval` | `Agent.Pull.Interval` | 过密 = 100 系统轮询放大；过疏 = 绑定变更延迟；**撤销实时性不依赖它**（撤销在数据面） |
| 24 | `guanjin.public-paths` | list | `[]` | 否 | `publicPaths` | `publicPaths` | `PublicPaths` | **放行即声明**：禁把受保护业务接口写进白名单；启动打印供评审；空值 = 只有内置白名单（最保守） |
| 25 | `guanjin.public-paths-include-builtin` | bool | `true` | 否 | `publicPathsIncludeBuiltin` | `publicPathsIncludeBuiltin` | `PublicPathsIncludeBuiltin` | 关掉只是把 `/healthz` 等也纳入登录（可用性代价），**不降低安全性**；内置白名单本身不含业务接口 |
| 26 | `guanjin.security.inject-security-context` | bool | `true` | 否 | `security.injectSecurityContext` | `security.injectSecurityContext` | `Security.InjectSecurityContext` | 关掉只影响宿主取身份的方式（`GuanjinContextHolder`/`req.guanjin`/`context` 仍可用），**不跳过验签、不影响判定**。**执行形态**：`true`（默认）而类路径无 `spring-security-core` → **启动失败**（不静默 NOOP）；出路是**显式**置 `false`——该 `false` **不受 profile 门控、生产允许**（`AC-SDK-02` 的门控只管 #1 `guanjin.enabled` 与 #15 `authz.enabled`），此时 `GuanjinContextHolder` 等仍可取到身份、验签与判定不受影响；除此之外不提供任何开关。裁定见 `notes/sdk-inject-security-context-2026-11-10.md` |
| 27 | `guanjin.security.authorities-resolver` | string(bean/符号名) | — | 否 | `security.authoritiesResolver`（Bean 名） | `security.authoritiesResolver`（函数） | `Security.AuthoritiesResolver`（函数） | 只做「身份属性 → 业务权限」映射；**不得在此做放行决策、不得回源平台**；抛异常 → 请求失败（fail-closed） |
| 28 | `guanjin.security.role-prefix` | string | `ROLE_` | 否 | `security.rolePrefix` | `security.rolePrefix` | `Security.RolePrefix` | 只影响 Spring Security 的 `hasRole` 前缀；**不改变角色事实**，不得用它伪造角色 |
| 29 | `guanjin.startup.print-public-paths` | bool | `true` | 否 | `startup.printPublicPaths` | `startup.printPublicPaths` | `Startup.PrintPublicPaths` | 生产建议保持 `true`（放行清单可审计）；日志只打路径，**不得打完整令牌/secret** |


**四处与基线的偏离（全部 `✅ 已定`，依据 `decisions.md` D6；`jwks-uri`/`replay-store`/`pull.enabled` 三项属「纠错性默认值」，**不进 CH-5、不需追认**）**：
- **C-1（#1/#15）**：`enabled=false` / `authz.enabled=false` **仅 `local|test` profile 可用**，其它 profile 置 `false` → **启动即失败（fail-fast，不是告警）**（D6-4）。对外表述保持「验签不可跳过、无生产跳过开关」；语义是「不注册过滤器（等价未接入）」而非「跳过验签」。
- **C-2（#12）✅ 已定**：默认由 `${base-url}/api/v1/jwks` 改为 **`${platform.base-url}/.well-known/jwks.json`**（base-url 不带尾斜杠）。依据三重吻合：**D6-3** + 索引 §4.2-23（JWKS 路径约定）+ 接口清单 §3.1.2（该端点确实存在），而 `/api/v1/jwks` **全库不存在** → 原默认会让 `GJ-CAP-03` 首启即 404、SDK 起不来。此项**不再列为待确认**。
- **C-4（#18）✅ 已定**：默认 = **`redis`**（基线原为 `memory`）；`memory` 只允许在 `local|test` profile 下显式选（与 C-1 门控同源）。依据：索引 §4.2-9（多副本 `jti` 必须 Redis）+ **`decisions.md` D6 裁定 (5)①**（团队收口，2026-11-02；明文「未同步前以本条为准」）。理由：本项目是多副本 + 无状态，默认 `memory` 会在默认配置下**静默降级为单机内存**（`jti` 防重放形同失效）= 索引 §2 禁止的降级放行。
  **给 Gate 的判定口径（不新增配置键）**：「多副本」= **非 `local|test` 的任何环境**（部署形态不通过新键表达，沿用 C-1 的 profile 门控）；`local|test` 下允许 `memory`，其余环境出现 `memory` 即 `AC-SESS-05` FAIL（启动失败或阻断级告警，与 D6(5)① 的 `AC-SESS-05` 要求一致）。
- **C-5（#22）✅ 已定**：默认 = **`false`**（基线原为 `true`）。依据：索引 §4.2-24「角色放进令牌（方案 A，零回源）」+ **`decisions.md` D6 裁定 (5)②**（团队收口，2026-11-02）。理由：默认 `true` 等于默认回源，与已生效决定相反，且 100 个系统 × 30s 轮询是无谓负载；需要兼容能力的接入方显式置 `true`（对应判据 `AC-SDK-10`：默认 `false` + 启动 30s+ 零次 `/agent/policies`）。

> **给 Gate 的交接**：`AC-SDK-07` / `AC-FC-05` 的「契约表」「29 项白名单」在实现期**以本文 §3.2 为准**（键名 + 类型 + **默认值**）。默认值共 4 处与基线原表不同，且**全部已定**（无待追认项）：C-1 门控语义（#1/#15）、C-2 `jwks-uri`（#12）、C-4 `replay-store`（#18）、C-5 `pull.enabled`（#22）；后两项依据 `decisions.md` **D6(5)**，可写进期望值、**不再标 `PENDING`**。

### 3.3 3 项已废止（`REMOVED`，不得实现）

| 基线原编号 | 推定键名（基线未印出点分键，由字段名机械推导） | 原语义 | 废止依据 | 若保留会怎样 |
|---|---|---|---|---|
| 7 | `guanjin.mode`（`gateway`\|`direct`） | 选择网关 / 直连形态 | 索引 §2「只保留网关形态」+ D6-2 | 出现直连兜底 = 绕过数据面 → 撤销不再实时、重放窗口失效 |
| 16 | `guanjin.direct.user-token-jwks-uri` | 直连形态下验用户令牌的公钥地址 | 同上 + D6-2 | SDK 去验会话级令牌，与「内部令牌 60s 逐请求签发」模型冲突 |
| 17 | `guanjin.direct.user-token-issuer` | 直连形态下用户令牌 `iss` | 同上 | 同上 |

> 三项统一 `REMOVED`：实现里**不得出现同名键，也不得出现等价开关**（`mode`、`direct`、`userToken*`）。出现即 Gate 判 FAIL（`AC-SDK-01` 无模式开关 + `AC-FC-05` 键白名单）。

### 3.4 固定行为（**不提供开关**，三语言一致）

验签不可关；剥离 `X-GuanJin-Assertion` 不可关；fail-closed 不可关；默认角色存在且权限最小；回调地址不登记；平台地址无内置默认值；`enabled`/`authz.enabled` 的 profile 门控见 C-1。

### 3.5 「不得新增 key」带来的两个实现后果（显式定死，禁隐性逻辑）

| 场景 | 基线要求 | 无配置项可用的处理（❓待 Spec 确认） |
|---|---|---|
| 策略上报失败的本地持久化队列（§7） | 「本地持久化队列 + 退避重试」 | 改为**内存队列**：容量固定 16 条、退避 5s→20s→60s（最多 3 次）、进程退出即丢弃；重启后**全量重报**（快照是幂等 upsert）。不新增配置项、不落盘 |
| `jti` 重放的 Redis 连接参数 | 未规定 | **不引入连接配置**：复用宿主已注入的客户端（Java 从容器取 Bean；Node/Go 由调用方初始化时传入 `replayStore` 实现）。SDK 不自己读 Redis 地址 |

### 3.6 禁键执行机制（未知 / 未支持键必须**启动失败**，不能只告警）

> 原则（Spec R3）：**只有 §3.2 的 29 个键存在**，任何拼错、旧键（含 §3.3 的三个废止键）、臆造键都会静默失效 → 必须做成**可执行的失败**，且必须报出**键名**（不允许只说「配置错误」）。

| 语言 | 机制（三语言同语义：fail-fast + 报键名 + 列出可用键） |
|---|---|
| Java 8 | ① `@ConfigurationProperties(prefix = "guanjin", ignoreUnknownFields = false)` —— **Spring Boot 2.x 默认是 `true`，必须显式置 `false`**，否则未知键被静默吞掉；② `guanjin-core` 自带 `Config.validate()`（core 不许依赖 Spring），由 `guanjin-spring-boot2` 在 Bean 初始化阶段调用，覆盖「非 Spring 入口」与「profile 门控」（C-1）；③ 未知键 → `ConfigurationPropertiesBindException` / 自定 `GuanjinConfigException` → **启动失败**（进程非 0 退出） |
| Node | 构造 `createGuanjin(opts)` 时对 `opts` 做**深度遍历**并与 29 项白名单逐键比对：出现白名单外的键 → 立即 `throw`（`GuanjinConfigError`，附 `unknownKeys[]` + 可用键清单）。环境变量同理：**以 `GUANJIN_` 开头但映射不到 29 项** → 同样抛错（防 `GUANJIN_JAWKS_URI` 这类拼写错误静默失效） |
| Go | ① `Config.Validate()` 在 `guanjin.New()` 内调用，**先于**任何网络动作（JWKS 拉取）；② 若配置来自文件/JSON，用 `json.Decoder.DisallowUnknownFields()`；③ 环境变量走白名单（`GUANJIN_` 前缀但未知 → 返回 error）；④ 返回 error 时**不得**同时返回可用实例（防调用方忽略 err 继续跑） |

**口径（一）配置键只有一套白名单**：即 §3.2 的 **29 项**（「32 项」是编号空洞，已由 `decisions.md` D6-1 / ADR-003 作废，**不是缺项**、不得补齐）。

**口径（二）来源二、映射一**：来源有两个——选项对象/配置文件用规范键名 `guanjin.*`；环境变量按 §1.1 **机械映射**：去掉 `guanjin.` 前缀，`.`→`_`，`-`→`_`，全大写，再加 `GUANJIN_`。映射名**逐字等价，不得别名/缩写/合段**（`GUANJIN_BASE_URL`、`GUANJIN_PLATFORM_BASEURL` 这类都不是契约，见 `decisions.md` D6(6)）。

**口径（三）未知键 = 启动失败**：非 0 退出（或构造期抛错），错误信息**必须含键名**、可列出可用键，**无关闭开关**。三语言同语义（见上表）。

**口径（四）非配置命名空间（例外，逐条封闭枚举，禁止通配）**：`GUANJIN_` 前缀同时被宿主/平台自用变量占用——它们是平台的**启动配置**（基线《技术栈与工程约定》§7.2 冻结）、平台的**实现自用参数**、以及**构建/测试夹具**。这些变量 SDK **不读、不校验、不参与默认值**，因此不进入白名单判定；**以本附录清单为准，逐条枚举，不做前缀/后缀式例外**（开放式例外会与「29 项冻结 + 未知键必须失败」正面冲突）。清单外任何 `GUANJIN_*` 映射不到 29 项 → **照样启动失败并报键名**。**例外不得当配置通道用**：清单内变量不能被 SDK 读成配置项，新增 SDK 配置只能走 ADR 改 29 项。

**附录：非配置命名空间封闭清单（37 条，逐条；改这里必须同步三语言实现与 golden 用例）**

| # | 变量 | 组 | 归属/用途 |
|---|---|---|---|
| 1 | `GUANJIN_ENV` | A | 运行时环境标识（基线 §7.2；Go `EnvFromEnv()` 读，值域 local/test/其余=prod） |
| 2 | `GUANJIN_LOG_LEVEL` | A | 日志级别（基线 §7.2） |
| 3 | `GUANJIN_LOG_FORMAT` | A | 日志格式（基线 §7.2） |
| 4 | `GUANJIN_HTTP_ADDR` | A | 控制面监听地址（基线 §7.2；`internal/config`） |
| 5 | `GUANJIN_GATEWAY_ADDR` | A | 数据面监听地址（基线 §7.2） |
| 6 | `GUANJIN_PUBLIC_BASE_URL` | A | 对外基址（基线 §7.2，必填） |
| 7 | `GUANJIN_ISSUER` | A | OIDC `iss`（基线 §7.2；`internal/config`、`cmd/guanjind`） |
| 8 | `GUANJIN_DATABASE_URL` | A | 迁移用库地址（基线 §7.2） |
| 9 | `GUANJIN_DB_MAX_OPEN_CONNS` | A | 连接池上限（基线 §7.2） |
| 10 | `GUANJIN_DB_MAX_IDLE_CONNS` | A | 空闲连接（基线 §7.2） |
| 11 | `GUANJIN_DB_CONN_MAX_LIFETIME` | A | 连接生命周期（基线 §7.2） |
| 12 | `GUANJIN_REDIS_URL` | A | Redis 地址（基线 §7.2） |
| 13 | `GUANJIN_REDIS_TIMEOUT` | A | Redis 单命令超时（基线 §7.2） |
| 14 | `GUANJIN_MASTER_KEY` | A | 主密钥 base64（基线 §7.2，必填） |
| 15 | `GUANJIN_MASTER_KEY_FILE` | A | 主密钥文件（基线 §7.2/§7.3 部署示例） |
| 16 | `GUANJIN_BOOTSTRAP_ADMIN_USERNAME` | A | 引导管理员（基线 §7.2） |
| 17 | `GUANJIN_BOOTSTRAP_ADMIN_PASSWORD` | A | 引导口令（基线 §7.2） |
| 18 | `GUANJIN_GATEWAY_UPSTREAM_TIMEOUT` | A | 数据面转发超时（基线 §7.2；`cmd/guanjingw`、`internal/gateway`） |
| 19 | `GUANJIN_GATEWAY_DIAL_TIMEOUT` | A | 上游连接超时（基线 §7.2；`cmd/guanjingw`、`internal/gateway`） |
| 20 | `GUANJIN_METRICS_ADDR` | A | Prometheus 指标地址（基线 §7.2） |
| 21 | `GUANJIN_OTEL_ENDPOINT` | A | OTel 导出端点（基线 §7.2） |
| 22 | `GUANJIN_DB_DSN` | B | 平台库 DSN（`internal/config` 必填） |
| 23 | `GUANJIN_REDIS_ADDR` | B | 平台 Redis（`internal/config` 必填） |
| 24 | `GUANJIN_REDIS_PASSWORD` | B | 平台 Redis 口令（`internal/config`） |
| 25 | `GUANJIN_TRACE_HEADER` | B | trace 头名（`internal/config`） |
| 26 | `GUANJIN_GATEWAY_ROUTES` | B | 数据面路由表（`cmd/guanjingw`、`cmd/guanjind/slo_wiring.go`） |
| 27 | `GUANJIN_SLO_NOTIFY_PATH` | B | SLO 通知落点（`internal/gateway/slo_http.go`） |
| 28 | `GUANJIN_GW_CLOCK_SKEW` | B | 数据面时钟容忍偏移**现名**（PR #121 改名；`cmd/guanjingw`）。旧名见 29 |
| 29 | `GUANJIN_GATEWAY_CLOCK_SKEW` | B | 同上**旧名**（PR #121 之前的名字；滚动升级期旧配置可能仍在环境里，故一并保留） |
| 30 | `GUANJIN_DEV_DSN` | C | 本地开发库（`Makefile`、`internal/store`/`internal/flowstore` 测试） |
| 31 | `GUANJIN_STAGING_DSN` | C | 预发库（`Makefile` 提示与 DB/DEV DSN 一并导出） |
| 32 | `GUANJIN_TEST_DSN` | C* | CI/测试夹具：测试库（`.github/workflows/ci.yml`、`internal/*`） |
| 33 | `GUANJIN_TEST_REDIS_ADDR` | C* | CI/测试夹具：测试 Redis |
| 34 | `GUANJIN_TEST_REDIS_PASSWORD` | C* | CI/测试夹具：测试 Redis 口令 |
| 35 | `GUANJIN_TEST_DURATION` | C* | 测试夹具：时长参数（`cmd/guanjingw/main_test.go`） |
| 36 | `GUANJIN_ALLOW_TEST_CLEANUP` | C* | 测试护栏：允许清理共享库（`internal/testdb`） |
| 37 | `GUANJIN_AUDIT_NO_ISOLATION` | C* | 测试护栏：审计隔离旁路（`internal/audit`） |

> **分组口径**：A 21 条（基线 §7.2 冻结）+ B 8 条（平台实现自用，含时钟偏移的现名/旧名）+ C 8 条（构建·测试夹具）= **37 条**。
> 与 `decisions.md` **D25**（平台侧 27 名：A 的 15 条 §7.2 + B 的 12 条代码在读）的关系：D25 是**平台侧口径**、本附录是**SDK 侧完整清单**，两者是包含关系（本表 ⊇ D25），差异两处已登记——①D25 记的 `GUANJIN_GATEWAY_CLOCK_SKEW` 已被 PR #121 改名，本表两条都收；②`GUANJIN_MASTER_KEY_FILE`（§7.2/§7.3 部署示例）D25 未列，本表收（按"基线冻结即登记"）。

> `C*` = **过渡条目**：测试夹具正在改名 `GJTEST_*`（单独 PR，与 SDK 修复解耦）。改名落地后这 6 条从清单删除——`GJTEST_*` 不带 `GUANJIN_` 前缀，SDK 本来就看不见。

> 事故记录（S9 / PR #51）：CI 的 `integration` job 导出 `GUANJIN_TEST_DSN` / `GUANJIN_TEST_REDIS_ADDR`，Go 侧曾把整个 `os.Environ()` 里的 `GUANJIN_*` 都当配置键（只用手写 `HasSuffix("_DSN")` 等三条例外兜）→ SDK 装载失败 → 该 job 必红。修法即本附录：**逐条封闭枚举 + 校验语义不变**；同时关掉 `_DSN` 后缀兜底这个**隐性放行**（`GUANJIN_X_DSN` 这类拼错键改后必须失败）。

**发布说明（"重启即挂"类，需写进升级须知，不进 CH）**：升级到含本附录的版本后，若启动失败且错误信息形如 `未知键 [guanjin.<…>]（该键不属于 SDK 配置命名空间；…）`，说明部署里存在**清单外**的 `GUANJIN_*` 变量——要么是拼错/废止键（改掉），要么是平台自用变量但未登记（按流程补进附录，**不要**改成通配放行）。

**明确不做**：不提供 `strict-config=false` 之类的关闭开关（否则又成暗开关，与 C-1 同类）；不为未知键提供「预留 / 实验」通道——新增键只能走 ADR 改 29 项。上表的非配置命名空间**不是**这种通道：它们不进 `Config`、不可被读取为配置项、也不新增任何 `guanjin.*` 键。

**Gate 可断言**：塞一个 `guanjin.foo`（或 `GUANJIN_FOO`）→ **启动失败且错误信息含 `guanjin.foo`**；塞 `guanjin.mode: direct`（已废止键，§3.3）→ 同样启动失败（同时满足 `AC-SDK-01`）；同时导出 `GUANJIN_TEST_DSN` / `GUANJIN_TEST_REDIS_ADDR` + 合法配置 → **必须正常装载**（否则 integration job 假红）。

---

## 4. 三语言同构：MUST 一致 / MAY 差异

### 4.1 必须一致（8 类）

| # | 项目 | 判据 |
|---|---|---|
| M1 | **Agent 面 HMAC canonical 串** | §4.3 一字不差；同一向量三语言签名**逐字节相同** |
| M2 | **断言验签算法** | `SM3withSM2`（JWT 形态），`kid` 选键，`alg` 不做协商降级；失败一律拒 |
| M3 | **能力 ID** | `GJ-CAP-01..22` 与语义一一对应，不得增删改 |
| M4 | **错误码** | 只消费接口 §5.4（含 D3-F 新增 `STEP_ALREADY_SATISFIED` E4/409，仅平台 `internal/flow` 产生，**SDK 不得产生也不得吞**）；**`SERVICE_DEGRADED` 已废弃（D8-C′⑤），SDK 永不返回**；SDK 本地拒绝（缺 header / 验签失败 / 重放 / `iss`·`aud` 不符 / `exp`·`iat` 越界）统一映射 **401 `TOKEN_INVALID`**，不自造新码。**`TOKEN_EXPIRED` 属平台对用户令牌（`access_token`）的响应，SDK 永不返回**——界分见 §4.4 末表 |
| M5 | **model.conf 三层语义** | 同一份 `model.conf` + 同一策略 → 同 allow/deny；`auditor` 豁免、`defaultRole` 兜底、密级 `≥` 判定一致。**客体密级入口 `✅ 已定`（D11 补选方案①，标「Spec 终校（2026-11-02）」）**：**不做声明式打标**，客体密级由**业务在调用点**把值传给既有能力 `Authz.checkLabel(objLevel)`（`GJ-CAP-16` 语义不变）；**不新增 builder 方法、不新增配置、不新增 SDK API**；`dataLabel()` 作废（**裸 `clearance()` 同属作废**，见 §2.1.2）且**不留别名、不另造替代名**（索引 §4.2-25 冻结的五项里没有客体声明入口）。判定**不进 CH**（不改 key、能力仍 22、不改既有签名），作**设计选择**登记 **ADR-004**（见附录 A 注） |
| M6 | **JWKS 缓存与 fail-closed** | TTL、未知 `kid` 强刷一次、拉取失败沿用旧缓存、无缓存则拒（§4.4） |
| M7 | **配置键名** | 29 个规范键 + 环境变量映射，不得各语言改名或加别名 |
| M8 | **剥离时机与剥离对象** | 剥离**对象 = 交给宿主的请求视图**，不是"进入 SDK 就丢头"：按 **D10 唯一六步**（读 → 验签 → 校验 claim（含 `jti` 重放）→ 注入上下文 → **替换视图** → handler），**禁止换序**；替换后 handler、下游转发、日志三类消费者**都取不到**（详见 §4.5）。**"验签前剥离"已被 D10 明确否决** |

### 4.2 允许差异（仅打包物与接入点）

| 维度 | 允许差异 | 不允许差异 |
|---|---|---|
| 包/模块形态 | Maven 双模块 / npm 单包 / Go module | 能力集合、配置键、错误码 |
| 宿主接入 | Servlet Filter / `node:http` 中间件 / `net/http` 中间件 | 过滤器顺序语义（必须在宿主认证之前）、剥离时机 |
| 并发与缓存实现 | `ConcurrentHashMap` / `Map` / `sync.Map` | 缓存 TTL 与失效语义 |
| 策略引擎库 | jcasbin / node-casbin / casbin v2 | `model.conf` 文本与判定结果 |
| `scope()` 输出形态 | 对象 / `{sql, params}` / `ScopeFilter` | 过滤语义（受限角色 → 追加 `owner_id = :me`；`auditor`/全量角色不加过滤） |
| 日志与错误文案 | 各语言惯用 | 不得记录完整令牌；错误码不得变 |

### 4.3 Agent 面 canonical 串（`decisions.md` D1，逐字 `FROZEN`）

```
canonical = METHOD "\n" PATH "\n" SORTED_QUERY "\n" SM3HEX(body) "\n" TIMESTAMP "\n" NONCE
X-GuanJin-Signature = Base64( HMAC-SM3( K, canonical ) )
```

| 段 | 取值规则（三语言同） |
|---|---|
| `METHOD` | 大写 HTTP 方法 |
| `PATH`（D1-**A1**，已定稿） | **取客户端在请求行里实际发出的 path（origin-form）**：**含全部路径前缀（含 context-path）**、不含 scheme/host/port、不含 query/fragment、**不做 URL 解码**、**不去尾斜杠**、空 path 按 `/` 处理（RFC 7230）；`/a/` 与 `/a` 是**不同**的 `PATH`。取值实现：Java `request.getRequestURI()`（含 contextPath，**不得**用 `getServletPath()`——它已剥掉 context-path）；Go `r.URL.EscapedPath()`（**不得**用 `r.RequestURI` 直切、也不得用解码后的 `Path`）；Node `req.originalUrl.split('?')[0]`。**部署红线**：Agent 面链路**禁止中间件做 path 重写/前缀剥离**（nginx `rewrite`/strip-prefix、K8s ingress `rewrite-target`）；确有统一前缀时，必须让两边看到同一段 path（平台挂在该前缀下，或 SDK 的 `platform.base-url` 就带上它） |
| `SORTED_QUERY`（D1-**A2**，已定稿） | ①取请求行 `?` 之后的原始串，无 `?` 或其後为空 → **空串**；②按 `&` 切分（空段丢弃），每段按**第一个** `=` 切 name/value（无 `=` → value 为空串）；③**保留重复键**（不去重、不合并、不累加，每对独立成项）；④name 与 value **各自**做 RFC3986 percent-encoding（保留 `A-Za-z0-9-._~`，其余按 UTF-8 取字节后**大写**十六进制），且**先编码、再排序**；⑤排序键 = 编码后 **name 字节序升序**，name 相同再按编码后 **value 字节序升序**（两级，保证同名参数顺序确定）；⑥`name=value` 以 `&` 连接；⑦同名同值的完全重复对顺序无所谓，无需额外规则 |
| `SM3HEX` | body 的 **SM3** 摘要小写 hex 64 字符（原 `SHA256` 已按索引 §2「禁 SHA 系」替换）；空体取 `SM3("")` 固定值，**不省略该段** |
| `TIMESTAMP` | Unix 秒；窗口 **±300s**，超窗 → `AGENT_TIMESTAMP_SKEW` |
| `NONCE` | **base64url 无填充，16~64 字节**，字符集 `[A-Za-z0-9_-]`；去重键 `agent:nonce:{client_id}:{nonce}`，TTL **600s**，重复 → `AGENT_NONCE_REPLAY` |
| `K` | 交付 secret 的 base64 文本**解码后的 32 字节**原始密钥（不做二次编码） |
| 头名 | `X-GuanJin-Client` / `X-GuanJin-Timestamp` / `X-GuanJin-Nonce` / `X-GuanJin-Signature` |

> **D1 定稿状态（2026-11-02）**：`PATH`（A1）与 `SORTED_QUERY`（A2）两处**已由 `decisions.md` D1 补齐**，上表按定稿逐字转写，不再有推定成分。
> ⚠️ **对 v2 的更正**：v2 曾推定「`PATH` 取原始请求行、**不含** context-path」——**与 D1 定稿相反**（定稿要求**含** context-path，且禁中间件重写）。照 v2 实现会让带 context-path 的业务系统与平台算出不同串、签名全失败；本节已更正，`/a` 与 `/a/` 的差异也一并按定稿处理。

### 4.4 fail-closed 矩阵（三语言同一张表）

| 场景 | 行为 | HTTP / 码 | fail-closed |
|---|---|---|:--:|
| 验签失败 / 令牌缺失 / `exp`·`iat` 越界 / `iss`·`aud` 不符 / `jti` 重放 | 拒绝，清上下文，不注入身份 | 401 `TOKEN_INVALID` | ✅ |
| JWKS 拉取失败**但有缓存** | 沿用缓存继续验签（公钥公开，验签仍真实执行） | 正常 | 否（非降级） |
| JWKS 拉取失败**且无缓存** | 拒绝 | 503 **`KEY_UNAVAILABLE`**（D8-C′：`DEPENDENCY_UNAVAILABLE` 不得用于已知依赖；JWKS = 验签公钥不可得） | ✅ |
| `jti` 存储不可用（`replay-store=redis`） | 拒绝 | 503 `REDIS_UNAVAILABLE` | ✅ |
| 策略上报失败 | **不阻塞业务请求**，内存队列 + 退避（§3.5） | 业务请求正常 | 否 |
| 策略拉取失败（#22=true） | 判定不依赖平台（方案 A）→ 无影响；无缓存时该用户按 `defaultRole` | 正常 | 否 |
| 模型/策略完整性校验失败 | 拒绝授权判定，不加载可疑策略 | 403 `INSUFFICIENT_ROLE` | ✅ |
| `platform.base-url` 未配置 / 非法 | **启动失败**（fail-fast，非静默放行） | 进程退出非 0 | ✅ |

**永不发生**（三语言共同红线）：① 因验签失败或依赖不可用而**放行**；② 提供任何「跳过验签 / 关闭 fail-closed」的生产开关；③ 返回 `SERVICE_DEGRADED`（D8-C′ 已废弃）。

**`TOKEN_INVALID` vs `TOKEN_EXPIRED` 的界分（消除 §5.4 双码二义，回应 Owl P3-3）**：两者都在接口清单 §5.4 的 E2 分级里，但**归属不同主体、语义不同**，SDK 只产前者：

| 场景 | 谁返回 | 码 | 为什么 |
|---|---|---|---|
| SDK 校验**内部断言**（`X-GuanJin-Assertion`，60s 短令牌）失败——含签名错、缺 header、`iss`/`aud` 不符、`iat` 越界、`jti` 重放、以及 **`exp` 已过** | **SDK** | `401 TOKEN_INVALID` | 故意**不区分"过期"与"伪造"**：对外区分等于告诉攻击者"这张票只是旧了"，且内部断言只有 60s 生命、过期几乎总伴随重放或时钟异常，可诊断性由**日志**（含 `requestId`）承担，不由响应码承担 |
| 平台对**用户访问令牌**（`access_token`）判定过期——`/userinfo`、`/oauth/introspect` 等端点 | **平台** | `401 TOKEN_EXPIRED` | 用户令牌是会话级凭证、过期是**正常业务事件**（前端要引导刷新），必须可区分；SDK 既不签发也不验用户令牌（§1.3 越界清单），**因此 SDK 永不返回 `TOKEN_EXPIRED`** |

> 一句话记法：**`TOKEN_EXPIRED` 属于用户令牌（平台说），`TOKEN_INVALID` 属于内部断言（SDK 说）**；三语言实现不得把内部断言的 `exp` 越界映射成 `TOKEN_EXPIRED`，也不得在日志之外暴露具体失败原因。

---

### 4.5 请求处理时序（`decisions.md` **D10**，唯一六步，禁止换序）

> 依据：**D10**（Spec 复校 2026-11-02，团队收口）＋ 数据面设计 §4.6 第 5 步 ＋ 接口 §5.2。**「验签前剥离」被明确否决**（会无令牌可验、凭空 401）；「只在下游转发前删一次」也不够（框架/日志中间件可能已缓存 header 名）→ 必须**替换请求视图**。

| 步 | 动作 | 关键点（三语言同） |
|:--:|---|---|
| ① | **读**原始请求头 | 从**原始请求**取 `X-GuanJin-Assertion`（`guanjin.gateway.assertion-header`，配置 #7）。**此时不删**——它是唯一凭证来源。缺失 → 401 `TOKEN_INVALID` |
| ② | **验签** | `SM3withSM2`；公钥按 `kid` 取自 JWKS 缓存，未知 `kid` 强刷一次，无缓存则拒（`GJ-CAP-02/03/04`） |
| ③ | **校验 claim** | `exp`/`iat`（±`clock-skew`）、`iss`、`aud`、**`jti` 重放**（`GJ-CAP-05/06/07`） |
| ④ | **注入上下文** | `SecurityContext` / `Principal`（Java `GuanjinContextHolder`、Node `req.guanjin`、Go `context.Context`）（`GJ-CAP-09/10`） |
| ⑤ | **替换请求视图（= 剥离）** | 交给宿主的 request 中该头**已不存在**：Java `HttpServletRequestWrapper` 覆写 `getHeader`/`getHeaders`/`getHeaderNames`（大小写不敏感）；Node 显式 copy 后删除；Go 删除并**重建** `r.Header`。客户端伪造的同名头一并丢弃（不是覆盖） |
| ⑥ | **业务 handler 运行** | handler、业务转发、日志三类消费者**都取不到**该头 |

**等价表述（便于评审）**：**剥离晚于验签与 claim 校验、早于模型与业务代码**。
**两个常见错法（Gate 判 FAIL）**：① 在步 ① 就删头 → 无令牌可验；② 只在步 ⑤ 后删**下游请求**的头，而宿主 handler 仍能读到（`AC-SDK-08` 的 `MockFilterChain` 会抓到）。
**实施注意（不是第 7 步）**：`try/finally` 清理 ThreadLocal / `req` 属性 / 上下文；任何异常路径仍 fail-closed（§4.4）。
**与网关侧的区别**：Edge 在网关侧「先弃客户端伪造同名头、后签发」是**另一个**剥离，与 SDK 侧本条不冲突（见 `m1-slices.md` S8 注）。

---

## 5. 打包物与接入方式

### 5.1 Java 8

| 项 | 内容 |
|---|---|
| 坐标 | `com.guanjin:guanjin-core`（**零 Spring / 零 servlet**：验签、claim、jti、JWKS、HMAC 客户端、jcasbin 判定、`model.conf`）＋ `com.guanjin:guanjin-spring-boot2`（`javax` 适配：Filter、`@ConfigurationProperties`、`SecurityContext` 注入、自动配置） |
| 版本线 | `0.x`（M1 内部）；`maven.compiler.release=8`，禁止 11+ |
| 宿主 | Spring Boot **2.7.x**（`javax.*`）；自动配置走 `META-INF/spring.factories` 的 `EnableAutoConfiguration`（2.x 方式，非 3.x 的 `AutoConfiguration.imports`） |
| 接入 | 引依赖 → 配 `guanjin.platform.base-url` / `client.id` / `client.hmac-secret` → 声明 `AuthzModel` Bean → 可选 `@PublicApi`/`public-paths`。过滤器顺序 `HIGHEST_PRECEDENCE + 100`（在 Security 之前） |
| 依赖 | BouncyCastle `bcprov-jdk18on`（JDK 1.8+）；jcasbin；HTTP 客户端见 5.1.3 |

#### 5.1.1 JDK 8 编译门禁（由「Java 8」裁决直接产生，务必进 CI）

`maven.compiler.release=8` ＋ **真实 JDK 8 构建**是双门禁（见下方 C-1/C-4 两条补充）；API 黑名单**固化 14 条**（原 10 条 + Spec D-4 补 4 条）：

| # | 禁用 | 出现位置 | JDK | Java 8 替代 |
|:--:|---|---|:--:|---|
| 1 | `java.net.http.HttpClient` / `HttpRequest` / `HttpResponse` | 基线 §4.4 `AssertionVerifier.JwksClient` | 11 | `HttpURLConnection` 或 OkHttp（core 抽 `HttpFetcher` 接口） |
| 2 | `String.isBlank()` / `strip()` / `repeat()` | 基线 §4.4 过滤器 | 11 | `s == null \|\| s.trim().isEmpty()` / `trim()` / 循环或 `String.join` |
| 3 | `var`（局部类型推断） | 基线 §5.4 `FilterRegistrationBean` 示例 | 10 | 显式类型 |
| 4 | `List.of` / `Map.of` / `Set.of` / `Collectors.toUnmodifiable*` | 潜在写法 | 9–10 | `Arrays.asList` / `Collections.unmodifiable*` |
| 5 | `Files.readString` / `writeString` / `Path.of` | 潜在写法 | 11 | `Files.readAllBytes` + `Paths.get` |
| 6 | `record` / `sealed` / `switch` 表达式 / 文本块 / `instanceof` 模式匹配 | 潜在写法 | 14+ | 普通类 / if-else / 字符串拼接 |
| 7 | `Optional.isEmpty()` / `ifPresentOrElse()` | 潜在写法 | 11 / 9 | `!isPresent()` / `if (x.isPresent()) … else …` |
| 8 | `Stream.toList()` | 潜在写法 | 16 | `collect(Collectors.toList())` |
| 9 | `ProcessHandle` / `StackWalker` / `Module` API / `java.util.concurrent.Flow` | 潜在写法 | 9 | 不用（core 无此需求） |
| 10 | `CompletableFuture.orTimeout` / `completeOnTimeout` / `Thread.onSpinWait` | 潜在写法 | 9 | 自建 `ScheduledExecutorService` 超时 / `Thread.yield()` |
| 11 | `List.copyOf` / `Set.copyOf` / `Map.copyOf` | 潜在写法 | 10 | `Collections.unmodifiableList(new ArrayList<>(src))` |
| 12 | `InputStream.readAllBytes()` / `readNBytes()` / `transferTo()` | 潜在写法 | 9 | `ByteArrayOutputStream` + `read(buf)` 循环 |
| 13 | `Map.entry(k, v)` / `Map.ofEntries` | 潜在写法 | 9 | `AbstractMap.SimpleImmutableEntry` / 手工组装 |
| 14 | `HexFormat` | 潜在写法 | **17** | 自写 `bytesToHex`（小写、定长；与 `SM3HEX` 的 64 位小写 hex 一致） |

> 第 11–14 条（Spec D-4 补）同样由**双门禁**兜住：`--release 8` 会在编译期直接拒绝这些 API，真实 JDK 8 实编（+ class file version ≤ 52）在运行期再兜一次；这里补齐只是让 CI 静态扫描**不依赖编译器报错也能命中**。

**Spec 要求补的四条（v3 新增）**：

- **C-1 `--release 8` 与「用 JDK 8 实际构建」的关系**：`--release 8` 只保证**本模块源码**的 API 面与字节码目标（编译期挡住表内**第 1–14 条**里被 `javac` 识别的 API），**挡不住依赖 jar**，也会因「用 JDK 17 编译、目标 8」而给出虚假安全感。因此：**`--release 8` 是"不许用新 API"的门禁**，**「CI 用真实 JDK 8 跑 `mvn -q test`」是"确实跑得起来"的门禁**，两者都必须有，缺一不可。
- **C-2 黑名单固化位置**：上表 **14 条**（原 10 条 + Spec D-4 补的第 11–14 条，同批扫描）全部写入 `guanjin-core` 与 `guanjin-spring-boot2` 的 CI 静态扫描（正则/字节码扫描），命中即失败；不依赖 code review 记忆。
- **C-3 `bcprov-jdk18on` 命名澄清**：`jdk18on` = **JDK 1.8 or later**，**不是** JDK 18 —— 它正是 Java 8 可用的 BouncyCastle 线；不要因为名字误判为「JDK 18 专用」而换成已停更的 `bcprov-jdk15on`。同理 `bcprov-ext-jdk18on` 亦为 1.8+。
- **C-4 依赖必须过 JDK 8 实编门禁**：所有第三方 jar 的 **class file version ≤ 52（Java 8）**；用 `maven-enforcer-plugin` 的 `enforceBytecodeVersion`（或 `animal-sniffer-maven-plugin` 的 `java18`/`java8` signature）在 `verify` 阶段阻断，防止引入 class version 55+ 的传递依赖（否则运行期才炸 `UnsupportedClassVersionError`）。

> 这不是风格问题：基线 §4.4 的示例代码**在 Java 8 下无法编译**，照抄会直接卡住 S9。

#### 5.1.2 `guanjin-core` 零依赖边界（可断言）

`guanjin-core` 的依赖树**不得出现** `spring-*`、`jakarta.*`、`javax.servlet-api`；`guanjin-spring-boot2` 才允许。Gate 用 `mvn dependency:tree` 断言（`AC-SDK-09`）。

#### 5.1.3 HTTP 客户端（❓待确认）

`guanjin-core` 需 `HttpFetcher` 抽象 + 默认实现：默认 `HttpURLConnection`（零依赖），可选 OkHttp。理由：JDK 8 无 `java.net.http`，core 又不能依赖 Spring 的 `RestTemplate`。

### 5.2 Node

| 项 | 内容 |
|---|---|
| 包名 | `@guanjin/sdk`（单包，`exports` 同时给 ESM/CJS） |
| 版本 | **Node ≥ 18**（`engines` 声明） |
| 接入形态（**已选定**） | 以**原生 `node:http` 中间件为核心**：`createGuanjin(options)` → `(req, res, next) => void`，零框架依赖（对应 core 零 Spring）。Express 直接可用（同签名）；Fastify 给薄适配（`onRequest` hook），**不把框架依赖引入核心** |
| 身份落点 | `req.guanjin = principal` + `getPrincipal(req)` |
| 授权 | `buildAuthzModel(...)` + `requirePermission(res, act)`；`Authz` 是对象而非静态门面（Node 无注解） |
| 国密库 | ❓**待确认**：`sm-crypto`（SM2 签/验、SM3、SM4）或 `gm-crypt`。Node 生态国密库成熟度低于 BouncyCastle/`tjfoc/gmsm`，需并入 `decisions.md` **R2**（纯软库是否被测评机构认可）一并确认；三语言必须用同一组向量互验 |
| 配置 | 选项对象为主（camelCase 路径），`GUANJIN_*` 环境变量为辅；冲突时**选项对象优先**并告警一次 |
| 剥离 | 用**包装后的 `req` 视图**（显式 copy + 删除 header），保证下游与日志中间件都取不到 |

### 5.3 Go（module path 已定稿）

| 项 | 内容 |
|---|---|
| **module path（定稿）** | **`github.com/spencerswagger/guanjin`**（`decisions.md` **P4**，hugo/spencer 已批；R7 关闭）。内部包前缀同此：`internal/*` 只出现在该 module 内 |
| **SDK 的 module 归属** | ❓**待 spencer 定（Q10）**，两案见下，本文**不自行定死**；两案的**包名、API 名字、29 项配置键与能力 ID 完全相同**，只差 import 前缀与 tag 形态 |
| 包名 | `guanjin`（对外唯一包名；`authz` 为其子包，不暴露任何平台 internal） |
| 接入形态 | `net/http` 中间件，两个入口：`func (s *SDK) Middleware(next http.Handler) http.Handler`（包在 router 外层）与 `func (s *SDK) Wrap(h http.HandlerFunc) http.HandlerFunc`（单 handler 包装，用于只保护部分路由） |
| 身份落点 | `context.Context`：`WithPrincipal(ctx, p)` / `PrincipalFrom(ctx)`（`context.WithValue`，键类型不导出，避免碰撞） |
| 授权 | `authz.NewModelBuilder()` + `RequirePermission(a, res, act)(next)`；`(*Authz).Scope` 返回 `ScopeFilter{SQL, Args}`（供 `WHERE` 拼接） |
| 依赖 | `tjfoc/gmsm`（与平台 P2 同一库 → 天然同构）、`casbin/v2`；HTTP/JSON 全用标准库 `net/http` + `encoding/json`，零第三方传输栈 |
| 配置 | `Config` 结构体 + functional options（`guanjin.WithConfig(...)`、`guanjin.WithHTTPClient(...)`）；env 前缀 `GUANJIN_`；`Validate()` 做 fail-fast（C-1/C-2） |
| 发布 | 与 Java/Node 同步按 `0.x` 走；tag 形态取决于 Q10（见下案 A/B） |

#### 5.3.1 `net/http` 接入写法（结构化描述，无源码）

1. **构造**：`cfg := guanjin.Config{...}`（29 项，字段名见 §3.2 的 Go 列）→ `s, err := guanjin.New(cfg)`；`New` 内部完成 **启动自检**（`GJ-CAP-22`：校验必填项、拉 JWKS、打印放行清单），失败即返回 error，**调用方必须把它当启动失败处理，不得忽略**（C-1/C-2 的 fail-fast）。
2. **挂载（整站保护，推荐）**：`mux := http.NewServeMux()` → 注册业务路由 → `handler := s.Middleware(mux)` → `http.ListenAndServe(addr, handler)`。中间件**必须包在 router 外层**：否则未匹配路由（404 分支）与 router 自身日志会绕过**视图替换**（按 **D10 六步**：先读头验签，再替换 `r` 交给内层，handler 与日志均取不到该头；详见 §4.5）。
3. **挂载（部分保护）**：只对需要登录的路由用 `s.Wrap(h)`；未包装的路由等同公开，**必须在 `PublicPaths`（配置 #24）里显式登记**，否则启动自检报「未声明的公开路由」并拒绝启动（deny-by-default，`GJ-CAP-19`）。
4. **取身份**：handler 内 `p, ok := guanjin.PrincipalFrom(r.Context())`；`ok=false` 表示该路由未受保护（不注入 nil 身份，避免业务方误判）。
5. **保护接口**：`http.Handle("/orders", s.RequirePermission("orders", "read")(orderHandler))`，或配 `@RequirePermission` 的等价物 `guanjin.RequirePermission(a, "orders", "read")`；**不提供** `RequireRole`。
6. **列表过滤**：`f := authz.Scope("orders")` → 用 `f.SQL` + `f.Args` 拼 `WHERE`；**禁止**查出全量再逐条过滤（`GJ-CAP-17`）。
7. **关闭**：`s.Close()` 释放后台刷新协程与上报队列（`GJ-CAP-20` 的退避重试跑在独立 goroutine 内，`Close` 不得等待网络超时，上限 = 配置 #4 `read-timeout`）。
8. **反例（会被 Gate 判 FAIL）**：把中间件挂在 router 内部的某个子路由上还指望整站生效；用 `http.StripPrefix`/自写 wrapper 事后删 header 代替 `GJ-CAP-08`；在 handler 里调 `PrincipalFrom` 拿到 nil 就放行。

#### 5.3.2 两种 module 布局（❓交 spencer，本文不预设结论）

| | **案 A（推荐）：SDK 独立 module** | **案 B：同一 module 的子包** |
|---|---|---|
| import 前缀 | `github.com/spencerswagger/guanjin/sdk/go/guanjin`（`sdk/go/go.mod` 里 `module .../guanjin/sdk/go`） | `github.com/spencerswagger/guanjin/sdk/go/guanjin`（无独立 `go.mod`，由根 module 覆盖） |
| tag 形态 | `sdk/go/v0.1.0`（Go 多 module 子目录 tag 约定），与平台 `vX.Y.Z` 解耦 | 只能跟根 module 一起 `vX.Y.Z` |
| **依赖隔离** | **编译期硬隔离**：业务方 `go get` 只拉 SDK 依赖，拉不到平台的 `gorm`/`pgx`/`kong`/15 表模型 | 依赖靠约定；`go mod tidy` 会把根 module 的传递依赖算进业务方 |
| **越界防护** | **天然挡住**：不同 module 无法 import 平台 `internal/*`，`GJ-CAP` 的「不碰平台内部」由编译器保证 | 只能靠 code review / lint 挡（Go 允许同 module 内 import `internal`） |
| 契约共享（`spec/vectors/*.json`） | 向量需在 SDK module 内（`sdk/go/testdata/vectors/`）或用 `go:embed`；与根 `spec/` 保持单向同步（CI 校验一致） | 直接读根 `spec/`，零同步成本 |
| CI | 两处 `go build`/`go test`/`govulncheck`，需 `go.work` 或分别 `cd` | 一次跑完，最省事 |
| 发布摩擦 | 每次 SDK 变更要打独立 tag（业务方 `go get ...@v0.1.x`） | 与平台版本强绑定，平台发版即 SDK 发版 |
| 我的建议 | **选 A**：SDK 是给 100 个业务方 `go get` 的交付物，**module 边界是 Go 里最硬的边界**（比目录约定、lint 都可靠），且能顺手把「SDK 不得依赖平台内部实现」变成编译期事实；代价只是多一次 tag 与一份向量同步 | 仅当运维明确不想管双 module 时才取；此时必须补「SDK 依赖白名单」lint 门禁 |

> 该选择**不影响**本文其余任何条款：能力 ID、29 项配置键、canonical 串、错误码、fail-closed 全案通用。

### 5.4 三语言接入一页对照

| 关注点 | Java 8 | Node | Go |
|---|---|---|---|
| 初始化 | 自动配置（`spring.factories`）+ `@ConfigurationProperties` | `createGuanjin(opts)` | `guanjin.New(cfg, opts...)` |
| 挂载 | `FilterRegistrationBean`（`HIGHEST_PRECEDENCE+100`） | `app.use(gj)` / Fastify hook | `s.Middleware(mux)` |
| 取身份 | `SecurityContextHolder` / `GuanjinContextHolder` | `req.guanjin` | `PrincipalFrom(ctx)` |
| 保护接口 | `@RequirePermission` | `requirePermission()` | `RequirePermission()` |
| 公开接口 | `@PublicApi` + 配置 #24 | `publicPaths` | `PublicPaths` |
| 启动自检 | 自动打印放行清单 | 同 | 同 |

---

## 6. 基线冲突的语言无关口径（逐条转写）

| 编号 | 基线冲突 | 收口口径（三语言同） | 依据 |
|---|---|---|---|
| **C-1** | `enabled=false` 是「跳过验签」暗开关 | 语义改为「**不注册过滤器**（等价未接入）」+ 醒目告警；**非 `local\|test` 置 false → 启动 fail-fast**；`authz.enabled` 同规则 | D6-4 |
| **C-2** | `jwks-uri` 默认指向不存在端点 | 默认改 `${platform.base-url}/.well-known/jwks.json`（无尾斜杠） | D6-3 |
| **C-3** | `mode`/`direct` 与「只保留网关形态」冲突 | 3 个键 `REMOVED`（§3.3），不得出现同名键或等价开关 | 索引 §2 + D6-2 |
| **C-4** | `replay-store` 默认 `memory` 违反「多副本必须 Redis」 | 默认 = `redis`；`memory` 仅 `local\|test`；「多副本」= 非 `local\|test`，不新增配置键（**已定：D6(5)①**） | 索引 §4.2-9 + D6(5)① |
| **C-5** | `agent.pull.enabled` 默认 `true` 与「方案 A 零回源」冲突 | 默认 = `false`，需兼容者显式置 `true`（**已定：D6(5)②**） | 索引 §4.2-24 + D6(5)② |
| **C-6** | 29 行 vs 「32 项」 | `32 = 29 + 3`（§3.1 推导）；交付 29 项连续编号，不新增 key | D6-1 |
| **C-7** | 错误码双轨（`IAM_*` vs §5.4） | 只消费接口 §5.4；本地拒绝统一 `TOKEN_INVALID`(401) | D3-B |
| **C-8** | 分页双轨（`limit`+`cursor` vs `page`/`page_size`） | `GET /agent/policies` 用 `since_version`（基线原文），**不引入分页参数**；响应若超限由平台截断并回 `next_cursor`（❓需 Edge 确认） | D3-A + 接口 §3.10.2 |
| **C-9** | E6 七码双轨 / 同故障双码 | JWKS 无缓存 → `KEY_UNAVAILABLE`；`jti` 存储 → `REDIS_UNAVAILABLE`；**`SERVICE_DEGRADED` 永不返回**；`UPSTREAM_UNAVAILABLE` 是数据面专用，SDK 不产生 | D8-C′ |
| **C-10** | `STEP_ALREADY_SATISFIED`（新增码） | SDK 不产生、不吞、不映射；仅透传平台响应体里的 `code` | D3-F |

---

## 7. 待确认清单（给 Spec / spencer，按阻塞度）

| # | 事项 | 阻塞什么 | 需谁 |
|---|---|---|---|
| Q1 | ~~C-5（#22 `pull.enabled` 默认 `false`）~~ **已定**：`decisions.md` **D6(5)②** 明文冻结 `false` | — | **已关闭** |
| Q2 | ~~C-4（#18 `replay-store` 默认 `redis`）+「多副本 = 非 `local\|test`」~~ **已定**：`decisions.md` **D6(5)①** 明文冻结 `redis` | — | **已关闭** |
| Q3 | ~~D1 两处未定义~~ **已定稿**：`PATH` 取法（A1，含 context-path、禁中间件重写、空 path 按 `/`、`/a/`≠`/a`）与 `SORTED_QUERY` 算法（A2，保留重复键、先编码后排序、name→value 两级）已写入 `decisions.md` D1，本文 §4.3 已逐字对齐（并更正 v2 的错误推定） | **三语言签名互验** | **已关闭**（Ark 已落盘，本文已回读对齐） |
| Q4 | **Node 国密库选型** | Node 实现能否过密评 | 并入 R2，spencer |
| Q5 | ~~§5.3 Go SDK 子目录形态~~ **已定稿**：module path = `github.com/spencerswagger/guanjin`（P4，R7 关闭）→ 见 §5.3 / 改由 Q10 承接 module 归属 | import 前缀 | **已关闭** |
| Q6 | §3.5 上报队列改**内存**（不新增配置项） | `GJ-CAP-20` 的可靠性与测试口径 | Spec → spencer |
| Q7 | §6 C-8：`/agent/policies` 是否回 `next_cursor` | 大客户全量拉取上限 | Edge 确认 |
| Q8 | §5.1.3 core 的 HTTP 客户端默认实现（`HttpURLConnection` vs OkHttp） | core 依赖边界 | Nova 自定，Spec 知情 |
| Q9 | **CH-5 已收窄为「29 项交付集 + 删 `guanjin.mode`」**（D6(3)/(5) 已把三处默认值移出 CH-5，归为「纠错性默认值」，不进追认）；若 CH-5 被否 → 本文 C-3/C-6 回退基线，C-2/C-4/C-5 不受影响（它们是纠错） | 仅「29 项」与「删 `mode`」两处的效力 | spencer 追认（CH-5） |
| **Q10** | **Go SDK 是否作为可独立分发的 module 子路径**（案 A `sdk/go` 独立 module / 案 B 同 module 子包）。★**需 spencer 定，本文不自行定死**：建议**案 A**（`module github.com/spencerswagger/guanjin/sdk/go` + tag `sdk/go/v0.x`），理由＝业务方 `go get` 只拉 SDK 依赖，且不同 module 无法 import 平台 `internal/*`，「SDK 不碰平台内部」由编译器保证；代价＝多一次 tag、`spec/vectors` 需在 SDK module 内同步一份（CI 校验一致）。案 B 更省事但只能靠 lint 挡越界 | Go SDK 的 import 前缀与 tag 形态（**不影响**包名/API/29 项配置/能力 ID/签名口径） | **spencer** |

> 注：Q1/Q2/Q3/Q6 不影响今晚 S9（Java 8 交付），只影响 M2+ 与三语言互验；Q9 若未追认，本文 C-2/C-3/C-6 回退为基线原文并在 §6 标注。

### 7.1 交付节奏（与 `m1-slices.md` S9 对齐）

M1 的 S9 **只交付 Java 8 两份产物**；Node / Go 随 M2+ 落地。但**能力 ID 与 29 个配置键此刻即冻结**，后续两语言不得改名或先自造命名，S9 的反射扫描（`AC-SDK-07`）之后无需返工。

---

## 8. 对齐 `decisions.md` 的修订点（v1 → v2）

| # | 修订点 | 原（v1） | 现（v2，依据） |
|---|---|---|---|
| R1 | 配置交付集 | 「32 = 29 + 3 REMOVED」，表内保留 3 行占位、保留基线原编号 | **29 项、连续编号 1..29**；3 项移到 §3.3 单列，「32 来历」只在 §3.1 交代一次（**D6-1**） |
| R2 | `guanjin.mode` / `direct` | 标 `REMOVED` 但仍在配置表内 | 从 29 项表中**完全移除**，仅存于 §3.3 废止清单（**D6-2**） |
| R3 | `jwks-uri` 默认 | 已改 `/.well-known/jwks.json`（v1 已按 D6 推定） | **`✅ 已定`（D6(3)）**：属「纠错性默认值」，**已从 CH-5 移出、不需追认** |
| R4 | `enabled=false` | 「启动 fail-fast + 告警」表述较松 | 明确 **仅 `local\|test` 可用，其余环境启动即失败**；对外表述固定为「验签不可跳过、无生产跳过开关」（**D6-4**） |
| R5 | canonical 串 | 已按 D1（含 `sorted_query`、SM3 替 SHA256） | 段序/规则不变；`NONCE` 由「16~64 **字符**」订正为 **16~64 字节**（base64url 无填充）（**D1**） |
| R6 | JWKS 无缓存的错误码 | `DEPENDENCY_UNAVAILABLE`（原列 Q10 待确认） | 定为 **`KEY_UNAVAILABLE`**（D8-C′：`DEPENDENCY_UNAVAILABLE` 不得用于已知依赖），Q10 关闭（**D8-C′**） |
| R7 | 错误码消费规则 | 只写「统一 `TOKEN_INVALID`」 | 增补：`SERVICE_DEGRADED` **废弃永不返回**；`STEP_ALREADY_SATISFIED` 平台专用、SDK 不产生不吞；新增 C-9/C-10（**D8-C′、D3-F**） |
| R8 | Go module path | 「BLOCKED by R7」 | **已定稿**：`github.com/spencerswagger/guanjin`（**P4**，hugo/spencer 已批、R7 关闭）；§5.3 按此重写并补 `net/http` 接入 8 步写法 |
| R8b | Go SDK 的 module 归属 | §5.3 曾推定 `.../sdk/go/guanjin` 子包（半定死） | 改为**两案并列 + 我的建议（案 A 独立 module）**，结论留 **Q10 交 spencer**，本文不预设（追加任务要求） |
| R9 | 登录/补步相关 | — | 明确 **CH-1（`flow_id` 必填）与 SDK 无关**（SDK 不做登录流程），无需在 SDK 契约留口 |
| R10 | 待确认清单 | 10 项（含 Q10、Q5 的 module path、Q9 编号呈现） | 收敛为 10 项：旧 Q10（错误码）关闭并入 R6、Q5 关闭（改由 Q10 承接 module 归属）、编号呈现按 D6-1 定案 |

### 8.1 v2 → v3 修订点（Spec 校对勘误，2026-11-02）

| # | 勘误 | 原（v2，错/不完整） | 现（v3） |
|---|---|---|---|
| E1 | §2.1 必需/可选计数 | 「必须 18 / 可选 4」 | **「必须 20 / 可选 2」**（仅 `GJ-CAP-11`、`GJ-CAP-21` 可选）——与表内及基线一致 |
| E2 | #18 符号自相矛盾 | 默认值列 `⛔` 而说明写 `❓` | 统一为 **`⏳`（待追认，并入 CH-5）**，说明同步改口径 |
| E3 | `jwks-uri`（#12）定性 | `⛔` 与「待确认」混着写 | **✅ 已定**：D6-3 + 索引 §4.2-23 + 接口 §3.1.2 三重依据（`/api/v1/jwks` 全库不存在）；从待确认清单移除 |
| E4 | `replay-store`（#18） | 只写「❓待 Spec 确认」 | **`⏳` 待追认 + 补依据**（索引 §4.2-9 + D6 追加收口）；并补 Gate 可用口径「多副本 = 非 `local\|test`，不新增配置键」 |
| E5 | `agent.pull.enabled`（#22） | 「❓待 Spec 确认」 | **`⏳` 待 spencer 追认**（索引 §4.2-24 只写可选项；D6 追加收口已补默认值但非追认） |
| E6 | §4.3 `PATH` 取法 | **推定「不含 context-path」→ 与 D1 定稿相反** | 按 **D1-A1 定稿**：`PATH` **含全部前缀（含 context-path）**、不去尾斜杠、空 path 按 `/`、`/a/`≠`/a`、禁中间件 path 重写，并给三语言取值 API（`getRequestURI` / `EscapedPath` / `originalUrl`）。**照 v2 实现会全签名失败**，此条为本轮最严重更正 |
| E7 | §4.3 `SORTED_QUERY` | 只写「字节序升序」 | 按 **D1-A2 定稿**展开 7 步（保留重复键、先编码后排序、name→value 两级、`?` 后为空 → 空串） |
| E8 | Java 8 门禁 | 仅 6 行黑名单 + 「`--release 8` 是唯一门禁」 | 补 Spec 要求的 4 条：**C-1** `--release 8`（挡 API）与**真实 JDK 8 构建**（挡运行期）互补、缺一不可；**C-2** 黑名单**固化**并进 CI 静态扫描（v4 起 **14 条**，含 Spec D-4 补的 `List.copyOf`/`readAllBytes`/`Map.entry`/`HexFormat`）；**C-3** `bcprov-jdk18on` = **JDK 1.8+，不是 JDK 18**；**C-4** 依赖 class file version ≤ 52（`enforceBytecodeVersion`/`animal-sniffer` 阻断） |
| E9 | §7 Q3 | 「D1 两处未定义，本文推定」 | **已关闭**：Ark 已把 A1/A2 落入 D1，本文已回读逐字对齐 → 不再列为待确认 |
| E10 | 附录 C 向量 | 6 条，未含 context-path / 尾斜杠 | 扩到 **10 条**（含 context-path、`/a` vs `/a/`、同名同值、`?` 后为空） |

### 8.2 v3 → v4 修订点（Spec 复校结论「通过·有条件」，2026-11-02）

| # | 复校要求 | 原（v3） | 现（v4） |
|---|---|---|---|
| E11 | `replay-store` / `pull.enabled` 标注**反过来** | 两者标 `⏳` 待追认、写「并入 CH-5」、让 Gate 记 `PENDING·CH-5` | **`✅ 已定`**：依据 **`decisions.md` D6 裁定 (5)①/②**；Spec 三份互证结论＝**D6(5) 明文集结 `redis`/`false`，且 `jwks-uri` 一并归为「纠错性默认值」、三项均移出 CH-5、不需追认**。删去全部「并入 CH-5」「PENDING」表述 → Gate 的 `AC-SESS-05`/`AC-SDK-10` 可直接写期望值，`PENDING` 账目不会虚增；CH-5 收窄为「29 项 + 删 `mode`」 |
| E12 | **剥离时机改写**（唯一阻塞 S9 的硬冲突） | M8 写「验签前即从视图移除」→ 照字面实现会「无令牌可验」或挂 `AC-SDK-08` | M8 改为「剥离**对象** = 交给宿主的请求视图；**时序** = 读原始头 → 验签 → 校验 → 注入上下文 → **替换视图**」，并新增 **§4.5 请求处理时序**；与基线 §4.4「注入 → 剥离」及 `AC-SDK-08`「注入之后、下游转发之前」一致。条号已由 **D10** 补齐（见 E16），不再有「待对齐」 |
| E13 | 禁键执行机制（R3） | 只有「不得出现同名键/等价开关」的原则 | 新增 **§3.6**：Java `ignoreUnknownFields=false`（Spring Boot 2 默认 `true`，必须显式关）+ `core.Config.validate()`；Node 构造期深度白名单比对 + `GUANJIN_*` 白名单；Go `Validate()` + `DisallowUnknownFields` + 白名单；三者统一 **fail-fast + 报键名 + 无关闭开关**，并给 Gate 可断言用例（塞 `guanjin.foo` / `guanjin.mode` → 启动失败且报键名） |
| E14 | 附录 A 措辞 + 补 4 个 API（R4/R5） | 只有一句「同序一一对应」 | 附录 A 改为 **22 行映射表**（ID ↔ 基线 §2 # ↔ 基线要点 ↔ 对外 API），并新增 **A-1~A-4 四个 builder API**（`can(resource, ...actions)`、`withClearance(level)`、`resourceGroup().contains(...)`、`defaultRole().can(...)`，依据索引 §4.2-25 + **D11**）；明确它们属能力 #13/#18，**不新增能力** |
| E15 | 快照 | 477 行 / `d997b5ab` | 见文末新快照（Spec 按此复校） |
| E16 | 剥离时序改为**引用 D10 定稿** | §4.5 标「待 D-x 对齐」、写成我自己归并的 7 步 | Ark 已落 **D10「唯一六步时序」**（读→验签→claim（含 `jti`）→注入→**替换视图**→handler），并明文否决「验签前剥离」；§4.5 已按 D10 **逐字改为六步**（清理改为「实施注意」不作步骤），M8 直接引 D10，并补「与网关侧先弃后签是另一个剥离」的区分 |
| E17 | **`dataLabel()` 作废** | 附录 A 补遗 A-4 是 `dataLabel(resource, level)`，`GJ-CAP-13` 行也含它 | **D11** 冻结密级方法为 `role(name).withClearance(level)`、`dataLabel()` **作废且不得留别名**：§2.1 的 `GJ-CAP-13` 改为索引 §4.2-25 的完整五段签名，附录 A 补遗 A-4 换成 `defaultRole(name).can(resource, ...actions)` |
| E18 | 附录 A 自相矛盾（Spec D-3） | 只写「无重排/合并/拆分」，但 `#9`/`#10` 实际重切过边界 | 附录 A 加脚注：`#9`/`#10` 做过**一次边界重切**（`GJ-CAP-09` 只解析→`Principal`，`GJ-CAP-10` 负责注入宿主上下文；因 Java 的「请求上下文」与 `SecurityContext` 同义、Node/Go 无 `SecurityContext`），**22 条能力与 29 项配置一个没变** |
| E19 | Java 8 黑名单补 4 项（Spec D-4） | 黑名单 10 条 | 补 `List.copyOf`(10)、`InputStream.readAllBytes`(9)、`Map.entry`(9)、`HexFormat`(**17**) → **14 条**；并注明 4 项同样由「`--release 8` + 真实 JDK 8 实编 + class version ≤ 52」双门禁兜住，静态扫描只是不依赖编译器报错 |
| E20 | 客体密级入口（**已定 · D11 补选方案①**，对标「Spec 终校（2026-11-02）」） | E17 之后只删了 `dataLabel()`，未说客体密级怎么声明 | 附录 A 与 §4.1 M5 两处由「⚠️ 待 D11 补句」改为 **`✅ 已定`**：**不做声明式打标**，客体密级由**业务在调用点**传给既有 `Authz.checkLabel(objLevel)`（`GJ-CAP-16` 语义不变）；**不新增 builder 方法 / 配置 / SDK API**；`dataLabel()` 作废、**不留别名、不另造替代名**。按三分判据**不进 CH**，作**设计选择**登记 **ADR-004** |
| E21 | 本轮对齐（Spec 终校 D-2 → D11 方案①，回读收口） | 附录 A / §4.1 M5 仍写「⚠️ 待 D11 补句」「本文推定、未被 `decisions.md` 收录」 | 措辞按 `decisions.md` **D11** 原文（标「Spec 终校（2026-11-02）」）**逐条对齐**：①方案①、不做声明式打标；②入口 = 调用点传值给 `Authz.checkLabel(objLevel)`，`GJ-CAP-16` 语义不变；③不新增 builder / 配置 / SDK API，**不设 A-5**；④`dataLabel()` 作废且**不留别名、不另造替代名**；⑤**不进 CH**（三分判据）+ 登记 **ADR-004**。**能力仍 22 条、29 键不变；只影响 M5，M1 未动** |

### 8.3 v6 → v7 修订点（P0-1 交付格式补齐，2026-11-02）

> 起因：P0-1 要求「能力表每条含 对应端点 / 是否 fail-closed / 与其它能力的关系」「配置表每项含 安全约束」「命名原则单列一节」。v6 已有语义、三语言命名、fail-closed 场景矩阵（§4.4）与命名规则（§1.1），但**上述三项没有按条落表**——本版只补齐呈现，**不改任何已冻结的结论**。

| # | P0-1 要求 | 原（v6） | 现（v7） |
|---|---|---|---|
| E22 | 能力表补「对应端点（冻结名）/ fail-closed / 关系」 | §2.1 只有 ID / 语义 / 必需 / 三语言命名；端点只在 §6/§1 文字里零散提到；fail-closed 只有 §4.4 的**场景矩阵**（按故障场景，不按能力）；能力间先后关系只在 §4.5 时序里隐含 | 新增 **§2.1.1**：22 行 × 「对应端点（冻结名）/ fail-closed / 与其它能力的关系」，并加**端点汇总表**（3 个出站端点 + 1 个入站 header）与「SDK 不得调用的端点」红线。**只补齐，不改判据**：22 条能力、必需/可选计数、§4.4 场景表一字未动 |
| E23 | 配置表补「安全约束」 | 29 行只有 编号/键/类型/默认/必填/三语言列；安全约束散落在表下 C-1~C-5 的说明里 | §3.2 表新增**第 9 列「安全约束」**，逐项 29 行写死（必填性、secret 注入方式、`clock-skew` 上限、`replay-window ≥ exp`、`jwks-uri` 只许平台基址、`memory` 仅 `local\|test`、白名单禁写业务接口…）。**键名/编号/默认值/必填性一处未改** |
| E24 | 命名原则单列一节 | 只有 §1.1 的「命名规则（机械可转）」表，讲的是**转换规则**，没有讲「与冻结表名/端点名/概念名一致」的原则 | 新增 **§1.4 命名原则 N1–N8**：概念名唯一来源＝基线冻结名；端点名逐字照抄、不得加别名；能力名三语言同词；配置键固定 `guanjin.` + kebab；claim 与传值 snake_case；错误码只消费不改写；禁同义词扩散；改名必须走 ADR + Spec 校对 |
| E25 | 编号缺口 / jwks 路径 / `enabled` 暗开关（三个已知问题）复核 | v6 已收口（§3.1 复算 `32 = 29 + 3`、§3.2 #12 改 `.well-known/jwks.json`、C-1 定义 `enabled` profile 门控） | **原文保留、只做交叉引用校验**：三个问题的结论与 `decisions.md` **D6(1)(3)(4)(5)** 逐字一致，本轮未产生新裁定；#12 与「`/api/v1/jwks` 全库不存在」的取证仍成立（接口清单 §3.1.2 只有 `/.well-known/jwks.json`） |

### 8.4 v7 → v8 修订点（PR #1 Owl 评审：2 个 P2 + 4 个 P3，一次性改完；＋ Gate 把关的 1 处 P3 = E34）

> Owl 原话：0 阻断 / 0 个 P1，2 个 P2 + 4 个 P3。本轮**只改文档、不写代码**，29 项配置与 22 条能力**一条未增删**。
>
> **读法**：本节「原（v7）」列会**引用旧写法/旧键名**（如 `checkLabel(level)`、`security.injectContext`），那是变更记录、**不构成契约**；契约以正文 §1–§7 为准，扫描与评审一律以正文为准（与 §2.1.2「登记处可写旧名、实现处零容忍」同一条原则）。

| # | Owl 意见 | 原（v7） | 现（v8） |
|---|---|---|---|
| **E26（P2-1）** | 命名自相矛盾：能力表还有三处 `checkLabel(level)`，D11 补口 C2 已要求 `objLevel` | §2.1 `GJ-CAP-16` 三语言三格都写 `checkLabel(level)` | **全文统一 `objLevel`**：§2.1 三格改为 `checkLabel(objLevel)`，并在语义列写明「形参名固定 `objLevel`，主体侧才叫 `level`」（D11 补口 C2）。`grep -c 'checkLabel(level)' = 0`。M5/附录 A/§2.1.2 原本已是 `objLevel`，现四处一致 |
| **E27（P2-2）** | N3 写「词根不许变」，但 CAP-10 / 11 / 13 三格明显不同词根 → 原则与表打架 | N3 一句「词根不许变」，无例外定义，三队可各写各的 | **选「改 N3 措辞、不改表」**：N3 重写为**两层命名**（概念层逐字符相同 / API 层逐格冻结），并新增 **§1.5**：分层定义 + **R-1..R-5 五类封闭形态规则**（大小写与接收者 / 构造器 / Go 的 `With*` context 写入口 / Java 的 `Granted*` 宿主限定词 / 宿主适配入口）+ **三处冲突的差异类别与理由表** + **CI 反射比对**（Java 反射方法名、Node 导出符号、Go `go doc` 逐格比对，不一致即 FAIL）+ 裁定理由。差异只许来自这五类，**新增第六类必须 ADR** |
| **E28（P3-1）** | `#26` Node 侧字段名不机械 | Node `security.injectContext`（自定义简化）+「（Node 恒等价）」；Go `Security.InjectContext` | 改为**机械推导**：Node `security.injectSecurityContext`（点分 camelCase，与 Java 逐字符相同）、Go `Security.InjectSecurityContext`；并在 §1.1 增补 **「Go 缩写封闭白名单」**（`ID`/`URI`/`URL`/`TTL`/`HMAC` 全大写、`jwks`→`Jwks` 不写 `JWKS`），使 29 个 Go 字段名全部可由键名机械生成、无手写特例 |
| **E29（P3-2）** | D11 的「裸 `clearance()` 作废 + 密级 1/2/3 词表」没进契约 | 只写了 `dataLabel()` 作废，未提裸 `clearance()`，也没有取值词表 | 新增 **§2.1.2**：唯一写法 `role(n).withClearance(level)` / 客体入口 `checkLabel(objLevel)` / **两个作废名**（`dataLabel`、裸 `clearance`）/ **词表 `1=内部 2=秘密 3=机密`、公开不占级** / 判定 `主体 ≥ 客体` / 基线示例 `.clearance(3) // 秘密` 判为笔误 / **禁词正则与扫描范围**（`(?<![A-Za-z0-9_])clearance\(`，只扫 SDK 三语言仓，不含 `/docs/`）。§2.1 `GJ-CAP-13` 行同步标注 |
| **E30（P3-3）** | `exp` 越界返回 `TOKEN_INVALID` 与 §5.4 `TOKEN_EXPIRED` 并存，二义 | M4 只说本地拒绝统一 `TOKEN_INVALID`，未提 `TOKEN_EXPIRED` 归属 | §4.4 末尾加**界分表 + 一句话记法**：内部断言（60s）任何失败含 `exp` 越界 → **SDK** 返回 `TOKEN_INVALID`（故意不区分过期/伪造，可诊断性交给日志的 `requestId`）；`TOKEN_EXPIRED` 是**平台**对用户访问令牌（`access_token`）的响应，**SDK 永不返回**（SDK 不验用户令牌）。M4 同步加指向 |
| **E31（P3-4）** | `docs/` 被 gitignore 导致「回写即缺陷」无法自动校验 | 我 v7 用 `git add -f` 单文件绕过 | **只确认、不重复改**：Ark 已在 `feat/docs-decisions-m1` 里删掉 `.gitignore` 的 `docs/` 规则（实测该分支 `.gitignore` 已无 `docs/`），**`origin/main` 目前仍是旧规则**——等他 PR 合入即生效。我**不动 `.gitignore`**（避免与他撞车），并在 PR 描述里同步这条状态 |
| **E32** | 复核红线 | — | 29 项配置、22 条能力、冻结表名/端点名/概念名**一处未改**；`/docs/baseline/` 零写入；零代码；未执行 `git worktree`；未改 `.gitignore` |
| **E33** | 自查补充（E29 带出的连带问题） | 契约落入仓内 `docs/` 后，D11 的禁词扫描范围（含仓内 `docs/`）会把**作废声明本身**点红 | §2.1.2 与门禁口径行明确 **`docs/sdk-contract.md` 整体排除扫描**（登记处免税），同时要求 `examples/` 与 `README` 保持零旧名——否则"登记处免税"会变成实现侧的漏洞 |
| **E34**（「原」列口径 = **PR #1 合入版 v8**，非 v7） | Gate 把关（PR #1 READY 附的 1 处 P3 文字不一致） | §5.1.1 的 **C-2** 仍写「上表 **10 条**写入 CI 静态扫描」，但黑名单表已是 **14 条**（第 11–14 条为 Spec D-4 补），与 E8/E19 的「14 条」自相矛盾 | C-2 改为「上表 **14 条**（原 10 条 + 第 11–14 条，同批扫描）全部写入 CI 静态扫描」；同段 **C-1** 的「第 1–10 条」连带改为「第 1–14 条」（`--release 8` 对 11–14 同样在编译期拒绝，见第 450 行说明），两处口径与表体、E8/E19 全部对齐。**仅改文字，黑名单条目、29 项配置、22 条能力一律未动** |



---

## 9. 附录

### 附录 A：能力 ID ↔ 基线 §2 编号 ↔ 对外 API 入口

**对应关系**：`GJ-CAP-01..22` 与 `GuanJin-SDK契约.md` §2 的 #1..#22 **严格同序**——`GJ-CAP-NN` ⇔ 基线 §2 第 `NN` 行，**无重排、无合并、无拆分、无新增能力**；22 条能力的语义原文以基线为准，本文只做语言无关化与命名冻结。

| ID | 基线 §2 # | 基线原文要点（缩写） | 对外 API 入口（三语言同格见 §2.1） |
|---|:--:|---|---|
| `GJ-CAP-01` | 1 | 验签内部令牌（SM2），无跳过开关 | `AssertionVerifier.verify` / `verifyAssertion` / `(*Verifier).Verify` |
| `GJ-CAP-02` | 2 | 用 `kid` 选公钥 | `JwksClient.publicKey` / `jwks.publicKey` / `(*Jwks).PublicKey` |
| `GJ-CAP-03` | 3 | JWKS 缓存 | `JwksClient.refresh` / `jwks.refresh` / `(*Jwks).Refresh` |
| `GJ-CAP-04` | 4 | 未知 `kid` 强制刷新 | 同上（内部行为） |
| `GJ-CAP-05` | 5 | 校验 `exp`/`iat` + 时钟偏移 | `validateClaims` / `validateClaims` / `ValidateClaims` |
| `GJ-CAP-06` | 6 | 校验 `iss`/`aud` | 同上 |
| `GJ-CAP-07` | 7 | `jti` 重放检测 | `ReplayGuard.firstUse` / `replayGuard.firstUse` / `(*ReplayGuard).FirstUse` |
| `GJ-CAP-08` | 8 | 剥离 `X-GuanJin-Assertion` | 视图替换（§4.5 步 6） |
| `GJ-CAP-09` | 9 | 读身份注入请求上下文 | `GuanjinPrincipal.from` / `principalFrom` / `PrincipalFrom` |
| `GJ-CAP-10` | 10 | 注入 Spring Security `SecurityContext` | `toAuthentication` / `injectPrincipal` / `WithPrincipal` |
| `GJ-CAP-11` | 11 | `GrantedAuthoritiesResolver` 扩展点（**可选**） | `resolve` / `authoritiesResolver` / `AuthoritiesResolver` |
| `GJ-CAP-12` | 12 | 三层 `model.conf` 底座 | `Authz` / `Authz` / `(*Authz)` |
| `GJ-CAP-13` | 13 | `AuthzModel.builder()` 声明式模型 | `AuthzModel.builder()` / `buildAuthzModel()` / `authz.NewModelBuilder()` |
| `GJ-CAP-14` | 14 | `@RequirePermission`（**不提供** `RequireRole`） | `@RequirePermission` / `requirePermission` / `RequirePermission` |
| `GJ-CAP-15` | 15 | `checkRow` / `checkOwner` | 同名方法（三语言一致） |
| `GJ-CAP-16` | 16 | `checkLabel` | 同名方法 |
| `GJ-CAP-17` | 17 | `scope()` 列表过滤下推 | 同名方法 |
| `GJ-CAP-18` | 18 | 默认角色（权限最小） | `defaultRole` / `defaultRole` / `DefaultRole` |
| `GJ-CAP-19` | 19 | `@PublicApi` + `public-paths` | `@PublicApi` + 配置 #24 / `isPublic` / `IsPublic` |
| `GJ-CAP-20` | 20 | 策略上报 `POST /api/v1/agent/report` | `PolicyReporter.report` / `reporter.report` / `(*Reporter).Report` |
| `GJ-CAP-21` | 21 | 拉取绑定 `GET /api/v1/agent/policies`（**可选**） | `PolicyPuller.pull` / `puller.pull` / `(*Puller).Pull` |
| `GJ-CAP-22` | 22 | 启动自检 + 打印放行清单 | `GuanjinStartupChecker` / `startupSelfCheck` / `StartupSelfCheck` |

**附录 A 补遗：4 个 builder API（此前只在示例段隐含，现按索引 §4.2-25 正式列为对外 API）**

> 依据：**索引 §4.2-25 冻结的 `AuthzModel.builder()` 完整签名** + `decisions.md` **D11**（密级方法统一为 `withClearance(level)`）。这 4 个是**能力 #13/#18 的组成部分**，不是新能力（22 条不变）。

| # | API（语言无关签名） | 语义 | 三语言命名 |
|:--:|---|---|---|
| A-1 | `role(name).can(resource, ...actions)` | 角色 → 权限，**actions 为变参**（索引 §4.2-25 原文即 `...actions`）；同一 `resource` 多次 `can` 取**并集**，不覆盖 | Java `role(n).can(r, acts...)`；Node `role(n).can(r, ...acts)`；Go `Role(n).Can(r, acts...)` |
| A-2 | `role(name).withClearance(level)` | **主体密级**由角色推导（平台不维护「用户密级表」）；`level` 为整数。**D11 冻结此名** | Java `withClearance(int)`；Node `withClearance(n)`；Go `WithClearance(int)` |
| A-3 | `resourceGroup(name).contains(resource, ...)` | **资源分组降维**：一次授权覆盖组内多个 `resource`，避免逐表授权 | Java `resourceGroup(n).contains(rs...)`；Node `resourceGroup(n).contains(...rs)`；Go `ResourceGroup(n).Contains(rs...)` |
| A-4 | `defaultRole(name).can(resource, ...actions)` | **默认（兜底）角色的权限**；权限最小，不得「已登录即全部」（能力 `GJ-CAP-18`） | Java `defaultRole(n).can(r, acts...)`；Node `defaultRole(n).can(r, ...acts)`；Go `DefaultRole(n).Can(r, acts...)` |

> ⛔ **`dataLabel()` 已作废、不得实现**（`decisions.md` **D11**：与索引 §4.2-25 的 `withClearance(level)` 冲突，同一能力不许两套写法，也不许留别名）。
> ✅ **客体密级入口 `已定`（D11 补选方案①，标「Spec 终校（2026-11-02）」，已回读对齐）**：**不做声明式打标**。客体密级由**业务在调用点**把值传给既有能力 `Authz.checkLabel(objLevel)`（`GJ-CAP-16` 语义不变：主体密级 ≥ 客体密级）；**不新增任何 builder 方法、不新增配置、不新增 SDK API**——本表就停在 A-1~A-4，**不设 A-5**。`dataLabel()` 作废后**不得留别名、也不得另造替代名**（裸 `clearance()` 同属作废，写法与取值口径见 §2.1.2；依据：索引 §4.2-25 冻结的五项里没有客体声明入口，`dataLabel()` 是自造的第六项）。
> **是否进 CH：不进**。按三分判据——不改 key、不改能力数量（22 条不变）、不改既有方法签名，业务方不能用基线原本不允许的方式调用 SDK（`checkLabel` 本就是允许的本地判定）→ 故不列入契约变更清单；但它是**设计选择而非纠错**，因此走「团队收口 + **ADR-004**（`docs/adr/ADR-004-客体密级由调用点传值.md`，随 S0 入库）」，不是追认。

**校验方式**：22 行对读基线 §2 即可核「条数与顺序」；A-1~A-4 对读索引 §4.2-25 即可核「签名完整」。三者（能力表 §2.1 / 本附录 A / 基线 §2）**必须逐行一致**。

> **脚注（§2 的 `#9`/`#10` 曾做过一次边界重切，条数不变）**：语言无关化把基线这两条重新划了边界——基线 `#9` =「读身份、注入请求上下文（`sub`/`uname`/`roles[]`/`acr`/`amr[]`/`cid`）」、`#10` =「注入 Spring Security `SecurityContext`」；本文 `GJ-CAP-09` 只做**解析**（产出 `Principal`），`GJ-CAP-10` 负责**注入宿主上下文**（Java `SecurityContext`/`GuanjinContextHolder`、Node `req.guanjin`、Go `context.Context`）。原因：Java 的「请求上下文」与「SecurityContext」是同一件事的两种说法，而 Node/Go 根本没有 `SecurityContext`——若不合并，`#9` 与 `#10` 在三语言里会重叠成同一实现。**结论：22 条能力、29 项配置一个没变**，重切只是把 `#9` 里「注入」那半句移到 `#10`，语义无遗漏、无新增。

### 附录 B：配置键 ↔ 基线 §3.1（新旧编号对照）

| 本文（新） | 基线 §3.1（原） | 状态 |
|---|---|---|
| 1–6 | 1–6 | ACTIVE |
| 7–14 | 8–15 | ACTIVE（gateway 8 项） |
| — | 7 | **REMOVED**（`guanjin.mode`） |
| — | 16–17 | **REMOVED**（`direct.user-token-*`） |
| 15–29 | 18–32 | ACTIVE |
| **29 有效** | 32 号位 | 见 §3.1 复算 |

### 附录 C：一致性向量清单（Gate 写用例、三语言共用）

| 向量文件 | 最少条目 | 覆盖 |
|---|:--:|---|
| `spec/vectors/agent-hmac.json` | 10 | 空 query、单 query、多 query 乱序（验字节序）、**重复参数名（不同值 → 两级排序）**、**同名同值（顺序无关）**、`?` 后为空、空 body（GET）、含 UTF-8 body、**带 context-path 的 `PATH`**（如 `/order/api/v1/agent/report`）、**`/a` 与 `/a/` 视为不同串**；每条含 `expected_signature`（逐字节） |
| `spec/vectors/assertion-verify.json` | 10 | 正常、签名错、`exp` 边界 ±skew、`iat` 未来、`iss` 错、`aud` 错、未知 `kid`（强刷）、JWKS 无缓存（503 `KEY_UNAVAILABLE`）、`jti` 重放、`alg` 非 `SM3withSM2` |
| `spec/vectors/authz-decisions.json` | 12 | RBAC allow/deny、`defaultRole` 兜底、`auditor` 豁免、密级 `>`/`=`/`<`、`scope()` 受限/全量、完整性校验失败 fail-closed |
| `spec/vectors/config-mapping.json` | 29 | 29 个规范键 → 三语言读入后内部值等价（含默认值、`duration` 解析） |

---

*本文是实现口径，不是基线；与 `/docs/baseline/` 冲突处以 `decisions.md` 为准并已在 §6 登记。本文不含任何源文件，未修改基线，未执行 `git worktree` 操作。*

---

**校对快照（v8，供 Spec 抽查）**：`sdk-contract.md` / 行数 **790** 行（`wc -l`）/ md5 **`1ae574b8d633c59efbf0a0f45f0770ef`**（md5 覆盖范围 = **第 1..787 行**，即到末尾 `---` 之前的内容为止，不含分隔线与本节自身，避免自引用）。上一快照 = 786 行 / md5 `8c8e6c09…`（S9 首版）。**E37（本 PR 追加）**：附录 35 → **37 条**——补 `GUANJIN_GW_CLOCK_SKEW`（数据面时钟偏移**现名**，PR #121 改名）与 `GUANJIN_GATEWAY_CLOCK_SKEW`（**旧名**，滚动升级期旧配置可能仍在进程环境里，漏掉即 SDK 启动失败）；并按 `decisions.md` **D25** 交代两者关系（D25 = 平台侧 27 名、本附录 = SDK 侧完整 37 条，本表 ⊇ D25，差异两处已就地登记）。**29 项键名/编号/默认值/必填性、22 条能力、判据一处未改；基线正文未动。**
