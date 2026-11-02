# 关津 · 文档矛盾裁定台账（decisions.md）

| 项 | 内容 |
|---|---|
| 主笔 | 关津 架构 · Ark（TWKJSV） |
| 输入 | 6 份成员基线笔记（`notes/baseline-read.md`）+ Spec 收口《接手理解摘要》第 6 段 9 类冲突 + 移交 Prompt §七 |
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
- **受影响 / 落地**：本轮**不改基线 DDL**；今晚 M1 骨架的 `000006_create_challenges` 里 CHECK 按 6 值写并**显式命名** `challenges_purpose_valid`（不依赖 PG 自动命名，原因见 D4）；Core 编排层、Vue（注册/绑定页）、Gate 用例统一引用本文词表；新增词必须走 ADR。

## D3 三处口径双轨（分页 / 错误码 / 端点总数 / 失败计数）

- **冲突 A 分页**：索引 §4.2-28 定 `limit`+`cursor` vs 接口 §5.5 用 `page`/`page_size`（默认 50、上限 200，仅导出/Agent 用游标）。**裁定**：全站统一 **`limit`+`cursor`**（游标不透明、响应 `{items, next_cursor}`），`page`/`page_size` 与 `total` **作废**；`PAGE_SIZE_EXCEEDED` 保留，语义改为「`limit` 超上限 200」。依据：索引 §4.2-28 属「有默认值，按默认走」且与索引 §2 的「无状态/大表深分页」一致。
- **冲突 B 错误码**：数据模型 §4.1 的 `IAM_IDENT_LAST_*` 自称「待定稿」 vs 接口 §5.4 已冻结 `LAST_IDENTIFIER_CANNOT_UNBIND` / `LAST_AUTHENTICATOR_CANNOT_REMOVE` 等。**裁定**：**一律用接口 §5.4**，`IAM_*` 全部作废；I6（最后一个主标识符不可降级）**不设对外错误码**——接口清单 §3.4 与页面规格均无「设主标识符」端点/V1 不暴露该操作，故 I6 只在服务层内部校验；将来若暴露，按 §5.4 风格追加 E4 码并走 ADR。依据：②端点契约优先 + 事实核对（接口 §3.4 只有 4 条标识符端点，无 primary 操作）。
- **冲突 C 端点总数**：索引 §1.3 写「66 个具名端点」/ 接口 §2 首尾写 66 vs 接口文末统计逐面相加 = **65**；且 §2.10 标题「3 条」而表内只有 2 行。**裁定**：**具名端点 = 65**（10+2+2+4+3+2+3+35+2+2），"66" 是「65 具名 + 1 条数据面通配转发边界」被重复计入具名；**Agent 面 = 2 条**（`report`/`policies`），§2.10 标题「3 条」为笔误。依据：②端点契约（逐面表）优先，且文末统计自证 65；索引 §2 未规定端点总数。
- **冲突 D 失败计数权威源**：索引 §4.2-2（Redis 实时权威 + `authenticators.fail_count/locked_until` 为镜像）vs 接口 §3.2.2（「权威状态落 `challenges` 表，Redis 仅限流计数」）。**裁定**：**账号级失败计数/锁定 = Redis 权威**（原子 INCR + fail-closed，按 `user_id` 全局共享，`authenticators` 仅持久化镜像）；`challenges.attempts` 是**单次挑战的尝试次数**（另一个对象，非账号级计数），二者不冲突。**键名固定（Gate 回读 2026-11-02 补）**：账号级失败计数键 = **`login:fail:{user_id}`**（`user_id` 用 uuid 小写带连字符形式）、锁定标记键 = **`login:lock:{user_id}`**；两者 TTL 分别取 `login_policies.lock_duration_min` / `fail_threshold` 窗口，**键名不得由实现自行改写**（`AC-LOGIN-03` 的多副本用例直接按此键断言；改键须同步改判据与本文）。依据：①索引 §4.2-2 为已生效默认 + 索引 §2「挑战权威落库」指的是挑战本身。
- **裁定 E（收口 Gate §15-G1 之外的「同场景双码」，Gate 记作 D3-E）**：接口 §5.4 里同场景并列两码的，**按语义分工、不二选一**：①PKCE —— 缺/畸形 `code_verifier` → `INVALID_CODE_VERIFIER`；格式合法但与 `code_challenge` 不匹配 → `PKCE_VERIFICATION_FAILED`（均 E2/400~401）。②撤销 —— 令牌被 `POST /oauth/revoke` 显式撤销后再次使用 → **401 `TOKEN_REVOKED`**；会话被撤销（登出/管理面踢下线/refresh 复用）后访问 → **403 `SESSION_REVOKED`**（前者是凭证状态、后者是准入状态，HTTP 码不同）。③三员 —— 调用超出本员职责域的写操作 → `THREE_POWER_VIOLATION`（E7/403）；单纯没有该角色 → `INSUFFICIENT_ROLE`（E3/403）。验收侧对应 `AC-LOGIN-05`、`AC-SESS-01`、`AC-AUD-07`、`AC-FLOW-03` 已按此回填。
- **裁定 F（收口 Gate §15-G2：`done` 步骤被重输的拒绝码）**：**分配新码 `STEP_ALREADY_SATISFIED`**（E4 资源与不变式 / HTTP **409**），仅由 `internal/flow` 在「该 factor 在 `flows.completed_steps` 中已 `done` 时再次 `POST /auth/challenge/{factor}`」返回。**不借用** `CHALLENGE_CONSUMED`（语义是「某个 `challenge_id` 已消费」，会让客户端去换 `challenge_id`，而实际该步骤无需再挑战）与 `ACR_INSUFFICIENT`（语义是「档位不足需补步」，用在"无需补步"时会诱发补步死循环）。登记方式：接口 §5.4 是冻结表且基线只读，本码作为**E4 的追加项**记在此处，下一版契约/OpenAPI 落盘时并入；CI 增一条禁码扫描（`STEP_ALREADY_SATISFIED` 只允许出现在 `internal/flow`），与 `check-no-automigrate.sh` 同批落地。
- **受影响 / 落地**：接口清单 §5.5/§5.7 的示例响应由 Spec 在下版口径文档对齐（不改基线则写入 OpenAPI/契约文件）；Core（分页/风控计数/`STEP_ALREADY_SATISFIED`）、Edge（限流键）、Vue（列表组件改用 cursor）、Gate（用例预期）统一按本条；Core 需在 `challenges` 与 `risk` 之间显式区分两套计数（`attempts` vs `fail_count`）。

## D4 存储 / 密钥 / 审计链（4 处 DDL 级冲突）

- **冲突 A UUID 默认值**：数据模型 §1.1「全库统一：主键默认值写作 `uuidv7()`」vs 技术栈 §5.0/§6.2「migration 不写 `DEFAULT uuidv7()`」。**裁定**：**一律不写 DB 默认值**，UUID v7 由应用层 `uuid.NewV7()` 生成（GORM `BeforeCreate`），migration 主键只写 `uuid PRIMARY KEY`。依据：①索引 §4.2-13 明确「应用层生成 UUID v7」+ 本机 PG17 亦无 `uuidv7()`（PG18 才有）。
- **冲突 B 主密钥用法**：数据模型 §3.7 `HMAC-SM3(主密钥, …)` 与技术栈 §5.6 示例代码 `crypto.AuditTag(r.masterKey, …)` **直接使用主密钥** vs 技术栈 §7.3 强制 **HKDF-SM3 派生子密钥**（"不要把主密钥直接用在不同算法上"）。**裁定**：**统一走 HKDF-SM3 派生**，主密钥只做派生输入、不进任何业务算法。冻结用途隔离（`info = salt` domain separator）：`guanjin/sm4/field`→16B（标识符/私钥落库）、`guanjin/sm4/keywrap`→16B、`guanjin/hmac/audit`→32B（审计链）、`guanjin/hmac/identifier`→32B（盲索引）、`guanjin/hmac/agent`→32B（如平台侧需验 HMAC 干线）。依据：③技术栈 §7.3 为专章且与「一密钥一用途」一致；纯软路径（P2）下密钥全在进程内存，用途隔离是唯一可做的止血。
- **冲突 C 审计表分区与链**：索引 §4.2-12「按月分区 + `DROP PARTITION` 整段清理（365 天）」vs 数据模型 §3.7 DDL **无分区**、`PRIMARY KEY (id)`、`prev_tag` 首条为 NULL 且**无锚点、无整链校验 Job**，但页面 A11 要求展示「完整性校验状态」。**裁定**：(1) `audit_logs` 按 `occurred_at` **RANGE 月分区**，主键改 `(id, occurred_at)`（分区表主键必须含分区键）；`forbid_mutation` 触发器挂父表（PG≥11 自动下发各分区）。(2) 链**不因 DROP 而悬空**：只允许删除「已归档且已写锚」的最老分区；分区切换与每日定时任务把该段末条 `integrity_tag` 写入既有 `runtime_config`（键 `audit.chain.anchor.<YYYYMM>`，base64），删除分区时把被删段首尾 tag 另存为断点锚（键 `audit.chain.segment.<YYYYMM>`）；跨分区首条的 `prev_tag` 取上一分区末 tag（或断点锚），链保持连续。(3) 「完整性校验状态」= 分区内逐条复算 + 与 `runtime_config` 锚比对，枚举 `verified | broken | unverified(锚缺失)`；校验 Job 落在 `cmd/guanjind` 内定时任务（不新增二进制），Prometheus 暴露 `audit_chain_broken_total`、`audit_anchor_age_seconds`。**(4) `trace_id` 取证口径（Gate 回读 2026-11-02 补）**：`flows.trace_id` **只记创建该流那一次请求的 `trace_id`，补步时不覆盖**；因此同一次登录里 `flows.trace_id` 与后续 `auth_events.trace_id` **本来就会不同**——取证时按"`flows.trace_id` 定位这次登录的入口请求、按各 `auth_events.trace_id` 定位各步动作"，**两者不一致不得判为缺陷**（否则会把正常的补步链路误报成 trace 断链）。若需"一次登录一条链"，用 `flows.id`（`flow_id`）作为关联键，而不是要求 `trace_id` 全链相同。依据：①索引 §4.2-12 为已生效默认；③技术栈 §5.6 单链 + 咨询锁保留；D5 的 `trace_id` 列定义（只写创建那条）。
- **冲突 D `clients` 缺列 + 一条不可执行 CHECK**：接口/页面引用的 `agent_hmac_secret`、`is_confidential` 在 `clients` 无同名列（实有 `secret_hash` / `secret_enc` / `prev_secret_hash`）。**裁定**：`agent_hmac_secret` 是**概念名**，物理落点即 `secret_hash`（HMAC-SM3 盲索引，验签用）+ `secret_enc`（SM4，还原/重发用）+ 轮换期 `prev_secret_hash`，**不新增列**；`is_confidential` **删除引用**——它与索引 §2「平台不做密级」冲突，受信 introspect 改为「`clients.status='active'` + `runtime_config` 键 `introspect.trusted_clients` 白名单」。附带：数据模型 §3.8 的 `CONSTRAINT clients_redirect_unique CHECK (redirect_uris = (SELECT array_agg(DISTINCT x) FROM unnest(...)))` **PG 禁止 CHECK 内子查询**，照抄会让 `000002` 直接失败 → 迁移里**不写**该 CHECK，改由 `internal/admin` 去重校验 + 单测覆盖（实现细节，不需 spencer 裁决）。
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

- **索引（名字全部显式，不依赖 PG 自动命名，理由见 D4）**：主键 `flows_pkey (id)`；`flows_one_active_per_user_client_uniq (user_id, client_id) WHERE status='active' AND user_id IS NOT NULL`（同一用户对同一业务系统至多一条活跃流，匿名流不受限）；`flows_session_idx (session_id) WHERE session_id IS NOT NULL`（会话撤销时批量作废）；`flows_expires_idx (expires_at) WHERE status='active'`（TTL 清理器只扫活跃段）；`flows_client_created_idx (client_id, created_at DESC)`（按业务系统排查/取证）。**注意：带 `WHERE` 的部分唯一索引只能用 `CREATE UNIQUE INDEX`，不得写成 `CONSTRAINT … UNIQUE`**（PG 不支持）——这是 Edge 的 DDL 复核项之一。
- **不变式**：**I-F1 归属**——`status='completed'` 必须 `user_id NOT NULL`（DB CHECK 可表达）；`status='active'` 必须 `client_id NOT NULL`。**I-F2 amr 单调不可回退**——`achieved_amr` 只增不减（服务层事务内校验 + 二期可选 DB 触发器比对 `OLD ⊆ NEW`），任何"补步后 amr 变短"的写入直接拒绝。**I-F3 过期语义**——`expires_at <= now()` 即过期，惰性置 `status='expired'`，过期流**不得**再签发任何令牌（返回 `FLOW_EXPIRED`）。**I-F4 一次性**——`completed` 后 `consumed_at NOT NULL`（可加单行 `CHECK ((status='completed') = (consumed_at IS NOT NULL))`）；已完成流二次提交 → `FLOW_NOT_FOUND`，已过期 → `FLOW_EXPIRED`。**I-F5 并发不丢 amr**——并发补步只允许串行推进（`SELECT … FOR UPDATE` 锁该 flow 行），多副本下不得出现两条 flow 各自累加 amr。**I-F6 清理（改为两段式，Core 对齐结论）**——流不软删（无 `deleted_at`）、不入审计哈希链，但**"过期"不等于"删除"**：①第一段只置 `status='expired'`（行保留，`FLOW_EXPIRED` 才可能稳定返回）；②**保留 7 天**后第二段才 `DELETE`；只删 `status='expired' AND updated_at < now()-7d` 的行（走 `flows_expires_idx`）。**这条改变是为了消除 `AC-FLOW-10` 的 flaky**：若"过期即 DELETE"，行消失后只能返回 `FLOW_NOT_FOUND`，`FLOW_EXPIRED` 会时隐时现。**I-F7 已完成后的一次性回填例外**——`status='completed'` 的行**允许一次** `session_id: NULL → 非 NULL` 的回填（S5 编排完成、S6 才建会话的交接期必须允许），回填后**不得再改**；除此以外 `flows` 的 `session_id` 只允许"NULL→值"一次，`user_id` 同理（服务层事务内校验，不加 DB 触发器以免正常交接被拦）。**I-F8 锁序与锁内禁令**——全站加锁顺序统一 **`sessions → flows`**（禁止反向）；**`sessions` 行一律不做 `FOR UPDATE`**（会话状态用条件更新/版本列保证，避免长事务占住热表）；**`flows` 行锁内禁做任何重活**——PBKDF2-SM3（≥10 万迭代、百毫秒级）、短信/邮件投递、外部 HTTP、审计导出等**必须在锁外完成**，锁内只允许：读该行、校验 `status`/`expires_at`/`achieved_amr`、`challenges` 的一次性消费更新（同一事务）、写回 `achieved_amr`/`completed_steps`/`status`。**例外条件：无**——若某步确需长耗时计算，必须先算后锁（"预计算 → 短事务"），这是硬约束不是建议。
- **与 `challenges` 的职责边界与引用关系**：`challenges` = **单步挑战**（发给谁、应答摘要、`attempts`、一次性消费、TTL）；`flows` = **流级进度**（目标档位、已达成 amr、已完成步骤序列），**不存任何应答材料**。引用方向**只有一条**：`flows.completed_steps[].challenge_id → challenges.id`，且为**逻辑引用、不加 FK**（挑战会被 TTL 清理与分区归档，加 FK 会锁死清理路径；完整性由服务层校验）；`challenges` **不反向加 `flow_id` 列、也不做反查**（Core 对齐结论）——阶梯内的流归属由**请求显式携带的 `flow_id`** 表达，见下文「契约变更清单」CH-1。理由：匿名流的 `user_id`/`session_id` **双 NULL**，无法用「session + factor」反查定位；请求显式带 `flow_id` 是唯一可举证的解。
- **`flow.ttl_seconds` 的落地口径（Edge/Core 对齐结论）**：`000015` 迁移内 **seed 一行** `runtime_config('flow.ttl_seconds','600')`，必须带 **`ON CONFLICT DO NOTHING`**（不得覆盖运维已改过的值）；**运行时键缺失 = 启动 fail-fast**（`config/runtime.go` 启动校验，与 P1 的 fail-fast 风格一致）；**代码里不得出现 600 常量**（禁隐性默认值），测试用 `runtime_config` 显式注入。
- **依据**：索引 §2「多副本 + 无状态化；挑战落库、Redis 只承载限流计数与热缓存」；④「不许有隐性逻辑」——复用 `challenges` 冒充 flow 会引入无表可查的隐式语义，比新增一张表更危险；`AAL0` 不落库的依据是索引 §4.2-15（档位 → ACR 只有 AAL1/AAL2）。
- **落地方案**：**Ark** 出 `migrations/000015_create_flows.up/.down.sql`（无 `DEFAULT uuidv7()`、`expires_at` **禁 DB 默认值**、约束/索引显式命名、部分唯一索引走 `CREATE UNIQUE INDEX`、`down` 可回滚）+ seed `flow.ttl_seconds`。**Edge** 在 `internal/store/model/flows.go`（GORM 映射 13 列 + `TableName()`，无 `DeletedAt`）、`internal/store/repo_flows.go`（读写、惰性过期、两段式清理器、**必须暴露"带 tx 的锁读"接口**：`LockByID(tx, id) (*Flow, error)` 内部 `FOR UPDATE`，供 Core 在同一事务内"锁 → 校验 → 消费 challenge → 累加 amr → 写回"）与 `internal/store/pgxerr.go` 映射表扩展（按 **SQLSTATE + 约束名**双键：`flows_one_active_per_user_client_uniq` 的 23505 → 业务码 **409**，I-F1 的 23514 → **400**，不得让 23505 一律误报 409 / 23514 一律误报 400）落地；DDL 由 Edge 复核（复核清单 6 条，已知 3 条：部分唯一索引不得写成 CONSTRAINT、`expires_at` 禁 DB 默认、I-F4 可加单行 CHECK；其余 3 条按 Edge 笔记逐条签署，本文不代述以免失真）。**Core** 在 `internal/flow/`（`state.go` 推进/消费、`descriptor.go` 档位映射、amr 单调校验、`completed_steps` 追加）与 `internal/session/manager.go`（登录成功**一次性回填** `session_id`，见 I-F7；`Revoke` → 通过**窄接口 `AbandonBySession`** 把该会话 `active` 流置 `abandoned`，避免 `session ↔ flow` 包循环）落地。**Gate** 用 flows 行举证 `AC-FLOW-01/03/07/10/11`（M2 的 `AC-FLOW-08`）。**Vue** 用 `flow_id` 续流，不感知本表。

## D6 SDK 配置项（29 vs 32 / mode / jwks-uri / 暗开关）

- **冲突**：契约 §3.1 编号缺 #7/#16/#17、实有 **29** 行 vs 全项目口径「**32** 项」；`guanjin.mode: gateway|direct` 存在于 §4.1 示例且不在清单内，与索引 §2「只保留网关形态」冲突；`jwks-uri` 默认 `${base-url}/api/v1/jwks` 指向不存在的端点（真值见索引 §4.2-23 `/.well-known/jwks.json`）；`guanjin.enabled` 默认 `true` 且「生产置 false 应被视为误配并告警」，实为可跳过验签的暗开关。
- **裁定**：(1) **以 §3.1 实有 29 项为交付集**，"32 项" 作废；缺号 #7/#16/#17 视为**编号空洞（文档笔误）**，**不得新增 key 补齐编号**（禁止编造契约），Nova 侧连续编号 1..29 并在 README 标注此差异（ADR-003）。(2) **删除 `guanjin.mode`**，SDK 无模式开关，只有一个形态（流量必过数据面）。(3) `jwks-uri` 默认改为 `${guanjin.platform.base-url}/.well-known/jwks.json`（base-url 不带尾斜杠），与 `/.well-known/openid-configuration` 同基址。(4) `guanjin.enabled` 与 `guanjin.authz.enabled` **受 profile 门控**：仅 `spring.profiles.active` 含 `local|test` 时可置 `false`，其它 profile 置 `false` → **启动直接失败（fail-fast，不是告警）**；生产不存在跳过验签/跳过判定的开关。
- **裁定 (5) 三处默认值 = 纠错性默认值（团队收口 + Spec 复校 2026-11-02；**同类同标，不单列追认**）**：①`guanjin.authz.replay-store` 默认 = **`redis`**（原契约 `memory`）—— 默认 `memory` 在多副本下静默降级为单机内存，`jti` 防重放形同失效，正是索引 §2 禁止的降级放行，且须与 `AC-SESS-05` 一致；`memory` 只允许 `local|test` 显式选。②`guanjin.agent.pull.enabled` 默认 = **`false`**（原契约 `true`）—— 索引 §4.2-24 已定「角色放进内部令牌（方案 A，零回源）」，默认 `true` 等于默认回源，且 100 系统 × 30s 是无谓负载；需要兼容的接入方显式置 `true`。③`guanjin.gateway.jwks-uri` 默认 = **`${base-url}/.well-known/jwks.json`**（见 (3)）—— 旧默认指向**不存在的端点**（首启 404），属错标。
- **为什么这三条不算「对外契约变更」、不进 CH-5（唯一口径）**：三条都是把默认值**改回与已生效决定一致**（索引 §4.2-23 JWKS 路径 / 索引 §2 多副本无状态 / 索引 §4.2-24 零回源），性质是**纠错**而非新增契约面——key 名、数量（29 项）、类型、必填性一处未动。把纠错也挂「待追认」会造成**同类不同标**（先前 `replay-store`/`agent.pull` 已按纠错处理、`jwks-uri` 却留在 CH-5）且让 `PENDING` 账目虚高。**因此 `jwks-uri` 默认值从 CH-5 移出，归入本条**；CH-5 只保留真正改变契约面的两项（29 项交付集、删 `guanjin.mode`）。**副作用（由 Gate 定账，我不改其文件；Gate 回读 2026-11-02 已据此更新）**：`AC-SDK-03` 由 `PENDING·CH-5` 降为 `PASS-able·D6(3)/(5)`（**12 → 11**），`AC-SDK-02` 再因 (4) 属纯加固裁定降为 `PASS-able`（**11 → 10**）——**故现行 `PENDING` = 10、`PASS-able` = 68**，与 `acceptance-criteria.md` 一致。
- **裁定 (5) 与既有条目的关系**：两项均**不与 (1)~(4) 冲突**，也不改 29 项的数量与 key 名（只改"默认值"列）→ `AC-SDK-07` 的逐项对齐仍按 29 项执行；但 Nova 的 `sdk-contract.md` 需同步这两个默认值（其 §3.1 表格 + §8 修订点），**未同步前以本条为准**。
- **依据**：①②③ —— ③技术栈 §1.1（SDK 只出 2.x、能力不加不减）；①索引 §2「只保留网关形态」「令牌必须验签（预设档位清单）」；①索引 §4.2-23（JWKS 路径与 kid 约定）。
- **受影响 / 落地**：Nova（配置类、Properties、示例 yml、README、`guanjin-core` 的 fail-fast 校验）统一按本条；示例 yml 删掉 `mode:` 行与 `jwks-uri` 旧默认；Gate 增加「非 local profile + enabled=false → 启动失败」用例；契约文档的"32 项"文案由 Spec 在下版口径对齐。

## D7 Flow Descriptor 契约字段

- **冲突**：`FlowStep` 只有 `{factor, role, status, challenge_hint}`，缺 `challenge_id` / `purpose` / 该步可贡献的 `acr`；且所需因子全被禁用时只有 `satisfied:false`、无不可达终态 → 前端只能重渲染死循环。
- **裁定**：Descriptor 契约**补充**（`GET /authorize/flow` 与 `/authorize/submit` 的 `details.flow` 共用）：`steps[i]` 增 `challenge_id?: string|null`（该步已预签挑战时非空，前端据此直接提交而不必先发起挑战）、`purpose: 'login'|'step_up'|'enroll'|'recover'|'verify_identifier'`（取 D2 词表，`register` 不进 Descriptor）、`contributes_acr: 'AAL1'|'AAL2'|null`（该步完成后预计达到的级别）；顶层增 `terminal_state: 'satisfied'|'need_more'|'unreachable'` 与 `unreachable_reason?: {code:'FACTORS_DISABLED'|'POLICY_UNSATISFIABLE', missing_factors: FactorType[]}`。
- **依据**：②端点契约优先（`/authorize/flow` 是契约端点，可加字段而不破坏既有键）；页面规格 §4.1 明示「对齐接口清单 §3.1.4」，故以接口契约为准扩展；索引 §2「禁止隐性逻辑」——不可达必须是**显式枚举**而非由前端推断。
- **受影响 / 落地**：Edge/Core 计算并下发新字段；Vue 用 `terminal_state` 终止渲染（`unreachable` 渲染终态页并给出可操作建议：联系管理员/换业务系统），**不得**用 `disabled_factors` 自行推断；Gate 加「全部因子禁用 → `unreachable` + HTTP 200（不是错误页）」用例；页面规格 §4.1 的 TS 类型由 Vue 侧同步更新（不回写基线）。

## D8 验收 / 合规口径

- **冲突 A 验收**：基线无验收/测试文档，索引 §4.3-2 把「测试策略与验收清单」降为**建议级·可后补**，唯一可判定的口径只剩技术栈 §9.3 覆盖率表。**裁定**：验收权威 = **移交 Prompt §十一《验收（V1 完成定义）》的三条线（业务/合规/安全）+ 技术栈 §9.3 覆盖率门禁**，二者组合为唯一口径；"建议级"只表示「基线未给本文档」，不表示「可不验收」。Gate 据此出《验收清单》（条款→断言 ID），不另立第二套口径。
- **【正式裁定 D8-B】(2026-11-02，收 Gate §15-G12；本节即外部引用的「D8-B」)**：`AC-AUD-10` 的呈现唯一口径 —— 状态名 = **「后置（DEFERRED）：本期不做判定」**，**计入「后置」栏，`BLOCKED` 栏计数 = 0**；账目 **85 = 68 `PASS-able` / 10 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置（DEFERRED）：本期不做判定」**。**正式摘除 `BLOCKED` 的依据**（满足 Gate「摘 `BLOCKED` 需 decisions 正式裁定」的要求）：Gate 状态表里 `BLOCKED` 的**定义前提是「期望值依赖残余待裁项」**，而 `AC-AUD-10` 依赖的三项已不在「待裁」状态——**R2/R4 已按 spencer 口径后置（挂起）、R3 已关闭（TLS 出本平台边界）**，前提消失则标签失效；继续挂 `BLOCKED` 会让账目永远有一个永不收敛的红项，掩盖真实进度。**同时保留 Gate 的关切为硬约束（后置 ≠ 免验）**：`acceptance-criteria.md` §11.1 的 **7 项输入（I-1~I-7）仍须在 M2 之前备齐**，`AC-AUD-12` 继续覆盖应用侧可判定部分；「后置」项**不得写成通过、不得写"预检通过"、不得从账目里摘掉**（仍占 1 条、仍出现在映射表的「后置」栏）。
- **依据**：①索引 §2「密评」条 + spencer 口径（2026-11-02）「由 `BLOCKED` 改成「后置（DEFERRED）：本期不做判定」」；②Gate v6 的状态定义（`BLOCKED` = 依赖残余待裁）；③D8-A「不造假通过」——摘 `BLOCKED` 不等于记 PASS，而是换成更准确的状态名。
- **密评后置的三条内容（原「冲突 B」裁定，保留）**：①**密评整体后置**——**本期不做密评判定、不出证、不送检**（依 spencer 口径 2026-11-02），相关条目一律记「后置（DEFERRED）：本期不做判定」，**R2/R4 挂起、R3 关闭（TLS 已出本平台边界，不是"挂起"）**。②**等保相关的代码要求照基线实现、不受后置影响**：双因素/多因素组合鉴别、审计只增不可篡改（哈希链）、三员分权、留痕与脱敏等**实现要求保留**，只是**本期不做出证与送检判定**（"实现要求保留、判定与出证后置"）。③**传输层与关津无关**：TLS 终结/国密 TLS(TLCP)/证书校验**不属本平台范围**，由部署方前置（见 P2②），平台只见明文，因此「传输国密」不构成本平台的实现项，也不进验收判据。
- **裁定 E（密评等级口径，外部记作 `D8-E`，收口第 10 项）：等级只引用索引 §2「上限二级」，数据模型的「密评三级」不适用。**
  - **冲突**（四处互相矛盾）：①索引 §2「**密评上限为二级**（因不引入硬件密码模块）」；②数据模型 **§3.13 `policy_snapshots`** 的设计说明「**密评三级** 8.4 g)/8.3 c) 要求访问控制信息与策略数据在存储中完整性受保护」；③数据模型 **§3.8 `clients`** 的 `tier` 设计说明「默认 L2（新建业务系统不特别声明即满足**等保二级 + 密评二级**）」；④移交 Prompt §一「等保三级 + **密评**」（等级写「见 §七 待确认」，§七-3 明说要定二级还是三级）。
  - **裁定**：①**本项目对外只声明「等保三级」**；**密评等级一律引用索引 §2「上限二级」**（纯软实现、不接密码机 → 对应 GB/T 37092 二级口径），**不声明、不追三级**。②数据模型里「密评三级 8.4 g)/8.3 c)」属**引错级别**——该条文对应密码应用三级要求，本项目不适用；**仅保留其实现要求中与等级无关的部分**（策略数据完整性用 `payload_hash` + `integrity_tag`(HMAC-SM3) 保护，这条本来就由索引 §4.2-27 要求），**不得据此新增任何"三级专属"实现项或判据**。③「等保二级」那句**不与「等保三级」冲突**：`L2` 是**客户端档位默认值**（默认 L2、承诺 AAL1），「等保三级」是**平台整体定级**，二者是不同维度，**不得互相推导**（不得因"默认 L2"就说平台只满足等保二级，也不得因"等保三级"就把默认档位抬到 L3）。④**TLS 不参与等级判定**（P2②：不属本平台范围）。⑤**本期仍不做密评判定**（D8-B），本条只收口"写哪个等级"，不解除后置。
  - **依据**：①索引 §2 优先于正文引用（`D1` 同源规则）；②P2①（纯软国密、不接密码机）+ P2②（TLS 出边界）共同决定"上限二级"是自洽的那个数；③`D8-B②`（等保相关**代码要求**照基线实现、只是不出证）。
  - **受影响 / 落地**：**Gate** 不新增三级专属判据（`AC-AUD-02/03/04/07/09/12` 的等保代码要求一条不动）；**Spec** 对外统一一句「等保三级；密评本期不判定，引用上限二级」；**Ark** 不因"密评三级"扩大实现范围（不加三级专属密码模块要求）；**Nova/Vue/Core/Edge** 无动作（本条不改任何表 / 端点 / 字段）。**不改的冻结名**：`等保三级`、`密评`、档位 `L1/L2/L3`、`acr`(`AAL1/AAL2/AAL3`) 全部照抄。
- **冲突 C fail-closed 触发码四选一**：`DEPENDENCY_UNAVAILABLE` / `REDIS_UNAVAILABLE` / `DB_UNAVAILABLE` / `KEY_UNAVAILABLE` 未定。**裁定**：按**具体依赖发具体码**（`REDIS_UNAVAILABLE` / `DB_UNAVAILABLE` / `KEY_UNAVAILABLE`），`DEPENDENCY_UNAVAILABLE` 仅用于未分类的第三方依赖；统一 HTTP **503** + `Retry-After`，并同步反映到 `/readyz`（不就绪）。
- **裁定 C′（收口 Gate §15-G1：E6 全组归位）**：接口 §5.4 的 E6 七个码一次性收口，杜绝"同一故障两个码"的双轨——①`INTERNAL_ERROR`＝未捕获的内部错误（500，缺陷，非预期路径）；②`REDIS_/DB_/KEY_UNAVAILABLE`＝具体依赖不可用（503，D8-C 主口径）；③`DEPENDENCY_UNAVAILABLE`＝**未分类**的第三方依赖（503，兜底，不得用于上面三个已知依赖）；④`UPSTREAM_UNAVAILABLE`＝**数据面转发上游失败/超时**（`GUANJIN_GATEWAY_UPSTREAM_TIMEOUT`/拨号失败，唯一明确判定点在 `cmd/guanjingw`）；⑤**`SERVICE_DEGRADED` 明确废弃、永不返回**——它与 `DEPENDENCY_UNAVAILABLE` 语义重叠且无独立判定口径，保留它必然产出"同故障双码"，与本条要消灭的问题同类；CI 增 `scripts/check-forbidden-error-codes.sh` 禁止该常量出现在任何返回值中（与 `check-no-automigrate.sh` 同批落地）。据此 E6 已无悬空码。
- **裁定 D′（收口 Gate §15 的 `/userinfo` 争议，Gate 记作 D8-D）**：`/userinfo` 按接口 §5.8 是**本地解析、不查库不回源**，因此 **DB 不可用时它仍返回 200**（SQL 计数必须为 0）——这**不构成降级放行**：该端点没有失去任何防护（令牌已验签、`exp`/`aud` 已校验），fail-closed 针对的是"依赖不可用时仍放行本该被拦的流量"，而本地解析不依赖该依赖。**反向约束**：一旦实现需要查库（例如要向库取最新角色），该路径**必须**返回 503 `DB_UNAVAILABLE` 而不是降级用缓存——即"要么零查库，要么如实 503"，不存在第三种。
- **受影响 / 落地**：Gate（验收清单与状态记账；**密评判定已后置 → `AC-AUD-10` 记「后置（DEFERRED）：本期不做判定」，不得写"预检通过"**）、Edge（就绪探针与 503 码 + `UPSTREAM_UNAVAILABLE` 判定点）、Core（异常映射表）、Ark（新增禁码扫描脚本）、Spec（对业务方口径）。**遗留**：R2/R4 后置（挂起）、R3 已出本平台边界（TLS 属部署方前置）——见残余待裁与 P2②。

## D9 引用缺章（数据模型 §6 / 页面规格 §7 / 接口清单 §6）

- **冲突**：三处被正文引用的章节在源文档中**不存在**（数据模型全文只有 §0–§5 + 附录 A/B；页面规格无 §7；接口清单 §5.5/§5.8 的 ⚠️ 正指向不存在的 §6），导致 C5/C10/A1 页面与分页/签名算法的「见 §x」全部断链。
- **裁定**：**引用缺章时的兜底顺序**（写进工程规范）：① 被引用文档内**同主题的端点契约/数据契约**（表格化、可执行的那部分）→ ② **索引 §2** → ③ **本台账已裁条目** → ④ 皆无则该行为**不实现**（禁推定默认），并登记 ADR 待补。逐条映射：数据模型 §6 → 以其 §4.1 不变式 + 索引 §4.2 各项默认值为准（数据模型正文各处的「见 §6」已知项均已落在 §4.2）；页面规格 §7「待补清单」→ 以页面规格 §2 页面清单总表 + 接口清单 §3 逐面详表为准，Vue 缺细项时**只用已冻结端点/字段**，不得自造；接口清单 §6 → 分页按 **D3-A**、幂等按接口 §5.6、签名算法按 **D1** 执行，不再等待 §6 补文。
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

## 契约变更清单（对外可见）—— 状态：**待追认，未获任何追认**

> 本表只收**对外可见**的变更（影响业务系统、SDK、前端或对外错误码的）。基线 `/docs/baseline/` 一个字不改，这些变更以本表为准并在**下版契约文档**（OpenAPI + `sdk-contract.md` + 页面规格口径）一次性落盘；落盘时机 = 全部 M1 判据回填后、M1 编码前。**本表状态一律为「待追认」**——本文件中只有 R1（D5）与 R7（P4）带「经 hugo 追认，2026-11-02」标注，本表任一条**不得**引用该标注。

| # | 变更点 | 依据（本文条） | 影响面 | 状态 |
|---|---|---|---|---|
| **CH-1** | `POST /auth/challenge/{factor}` **新增必填参数 `flow_id`**：当 `purpose ∈ {login, step_up}` 时**必填**（阶梯内的流必须显式指明），缺省 → `MISSING_PARAMETER`；其余 purpose（`recover`/`enroll`/`verify_identifier`）可空。理由：匿名流 `user_id`/`session_id` 双 NULL，无法用「session + factor」反查定位，显式带 `flow_id` 是唯一可举证解（Core 对齐结论；`challenges` 表不加 `flow_id` 列） | D5「与 `challenges` 的职责边界」 | OpenAPI / SDK 三语言（Java·Node·Go）/ Vue 登录页补步 / 判据 `AC-FLOW-01/03/10` | **待追认** |
| **CH-2** | Flow Descriptor **新增字段**：`steps[i].challenge_id`、`steps[i].purpose`、`steps[i].contributes_acr`，顶层 `terminal_state`（`satisfied/need_more/unreachable`）与 `unreachable_reason`。纯加字段、不删既有键，向后兼容 | D7 | SDK（Descriptor 解析）/ Vue（渲染终止条件）/ 判据 `AC-FLOW-01/07` | **待追认** |
| **CH-3** | 分页统一 **`limit`+`cursor`**，`page`/`page_size`/`total` 作废；`PAGE_SIZE_EXCEEDED` 语义改为「`limit` > 200」 | D3-A | 管理面全部列表 / 前端列表组件 / SDK 分页/ 判据 `AC-AUD-08`（M5） | **待追认** |
| **CH-4** | 错误码三处变更：①**新增** `STEP_ALREADY_SATISFIED`（E4/409）；②**废弃** `SERVICE_DEGRADED`（永不返回）；③同场景双码**按语义分工**（PKCE 两码 / `TOKEN_REVOKED` vs `SESSION_REVOKED` / `THREE_POWER_VIOLATION` vs `INSUFFICIENT_ROLE`） | D3-E、D3-F、D8-C′ | 错误码表 / SDK 异常映射 / 前端文案表 / 判据 **`AC-LOGIN-05`、`AC-FLOW-03`、`AC-SESS-01`、`AC-AUD-07`、`AC-ENG-08`（共 5 条，Spec 抽查订正：原漏前两条）** | **待追认** |
| **CH-5** | SDK 契约：配置以 **29 项**为交付集（"32 项"作废、缺号 #7/#16/#17 不补 key）、**删除 `guanjin.mode`**（无 direct 形态）。**注：`jwks-uri` 的默认值已移出本条**——它与 `replay-store`/`agent.pull.enabled` 同属**纠错性默认值**、按 **D6(5)** 处理，不单列追认 | D6 (1)(2)(5) | SDK 三语言配置类与示例 / `sdk-contract.md` / 判据 **`AC-SDK-01`、`AC-SDK-07`、`AC-FC-05`**（`AC-SDK-02` 已按 D6(4) 纯加固裁定移出 CH-5） | **待追认** |
| **CH-6** | 端点计数口径：**具名端点 65**（"66" 是 65 + 1 条数据面通配边界）、Agent 面 2 条 | D3-C | 无外部影响（内部计数与文档口径） | 内部口径，**无需追认** |

**合计 6 条，其中 5 条标注「待追认」（CH-1~CH-5），1 条为内部口径（CH-6）。** 未追认前的实现约束：CH-1 若被否，回退方案是「`challenges` 加 `flow_id` 列 + 反查」（需 ADR，且会引入双向耦合，D5 已论证不推荐）；CH-2/CH-4/CH-5 若被否，按基线原文实现并同步回改本文与 Gate 判据（不留在两套口径）。**纠错/加固性边界的完整口径（Gate v5 回读 2026-11-02 补，解决 G11）**：①「把错标默认值改回与已生效决定一致」= **纠错**，走 D6(3)/(5)，不进本清单（`jwks-uri` 路径、`replay-store=redis`、`agent.pull.enabled=false`）；②「把基线**已经禁止**的取值在非 `local|test` 下变成启动失败」= **加固**，走 D6(4)，同样不进本清单（`guanjin.enabled` / `guanjin.authz.enabled` 的 profile 门控即属此类——SDK 契约 §3.2「验签不可关」+ 移交 §九「不存在生产可跳过验签的开关」本已禁止，D6(4) 只是让禁令**可执行**，没有新增契约面，故 **`AC-SDK-02` 不进 CH、应为 `PASS-able`**）；③**只有**「新增 key / 改 key 名 / 改数量 / 改必填性 / 改变某 key 在 `local|test` 之外的可选值域（即新增一种合法取值）」才必须进本清单待追认。判据：**这条改动是否让业务方能用基线原本不允许的方式配置 SDK？** 能 → 进 CH；不能（只是让基线已禁止的用法启动失败）→ 加固，不进。

---

## 残余待裁（spencer 本轮未覆盖，本节不作裁定）

| # | 事项 | 现状 / 卡点 | 需要谁 |
|---|---|---|---|
| R2 | **纯软件国密库是否被密码测评机构认可**（`tjfoc/gmsm`、BouncyCastle 达 GB/T 37092 二级口径） | **后置（DEFERRED）：本期不做判定**（挂起）——依 spencer 口径（2026-11-02）密评整体后置；代码仍按 P2① 纯软国密实现，本期不出证/不送检 | —（本期不推进；如需恢复由 spencer 重开） |
| R3 | **前置网关国密 TLS（TLCP/SM2 证书）选型与责任方** | **不属本平台范围（部署方前置项），已关闭**——TLS 终结/TLCP/证书校验均由部署方承担，平台只见明文、不写代码、不进验收判据（见 P2②，依 spencer 口径 2026-11-02） | —（已出本平台边界） |
| R4 | **密评受理口径与送检时间** | **后置（DEFERRED）：本期不做判定**（挂起）——依 spencer 口径（2026-11-02）；等保相关**代码要求照基线实现**，只是本期不出证/不送检 | —（本期不推进；如需恢复由 spencer 重开） |
| R5 | **PKCE `code_challenge_method=S256` 与「全链路国密/禁 SHA 系」的兼容口径** | 接口清单 §3.1.3 要求 `S256`（OIDC 只定义 `plain`/`S256`），与索引 §2 的国密要求未对齐；M1 不阻塞（`AC-AUD-12` 已把 S256 列为唯一白名单） | spencer（可定为"传输层外不属签名，保留 S256"，需确认） |
| R8 | **数据面 QPS 目标**（索引 §4.3-1） | 峰值登录 200 QPS 已知，数据面全量转发 QPS 未估 → 压测基线与扩容比缺失 | Ark 提议 + spencer 确认 |
| R9 | **审计归档介质与归档密钥保管责任方** | 365 天留存与锚点机制已定（D4-C），归档到哪、谁持旧密钥副本（索引 §2 要求保留 365 天）未定 | spencer + 运维 |

*本文只登记裁定与口径，不改 `/docs/baseline/` 任何内容；今晚建骨架时与 12 份基线一并入库。ADR-001~004 由 Ark 在 `docs/adr/` 落地时引用本文 D 编号。*

**收口记录（2026-11-02，经 hugo 追认）**：R1 → 转入 **D5**（新增第 15 张表 `flows`，只定表结构规格）；R6 → 转入 **P5**（旧仓 SpIAM 冻结归档、只读参考：不改/不推/不开分支，代码只进 `GuanJin`）；R7 → 转入 **P4**（Go module path = `github.com/spencerswagger/guanjin`）。本节现存 **R2–R5、R8、R9**，编号保持不变（不重排，避免打乱 Gate 判据与 Nova 契约的引用）；其中 **R2/R4 已后置（本期不做判定、挂起）、R3 已关闭（TLS 不属本平台范围）**——依 spencer 口径 2026-11-02，团队不再裁定、也不会再等任何"对口径"；R5/R8/R9 在外部结论到位前其状态仍如实保留为「未定」，**不得当已决引用**。

**Gate v2 回读后的追加收口（2026-11-02）**：`D3-E`（同场景双码按语义分工）、`D3-F`（新增 `STEP_ALREADY_SATISFIED`，收 G2）、`D8-C′`（E6 七码归位，`SERVICE_DEGRADED` 废弃，收 G1）、`D8-D′`（`/userinfo` 零查库 200 / 查库即 503）已全部写入本文，**不再有待补项**；Gate 的 §15-G1/G2 与"§6 待补 4 处"由此闭合，`acceptance-criteria.md` 无需再向本文追问。

**Gate v3 / Nova 回读后的追加收口（2026-11-02，团队收口）**：①**D1 补 A1（`PATH` 唯一取法，含 context-path、禁中间件 path 重写）与 A2（`SORTED_QUERY` 算法：保留重复键、编码先于排序、name→value 两级排序）**——Nova 判定这两条直接决定 SDK 与平台能否互验；②**D6 补两处默认值**（`authz.replay-store=redis`、`agent.pull.enabled=false`），只改默认值不动 29 项；③**D3-D 补失败计数键名** `login:fail:{user_id}` / `login:lock:{user_id}`；④**D4-C 补 `trace_id` 取证口径**（`flows.trace_id` 只记创建那次，与后续 `auth_events.trace_id` 不一致**不算缺陷**）。以上均标「Gate/Nova 回读（2026-11-02）」，**不含任何追认标注**（追认标注仅 P4/P5 与 D5 的 R1）。

**Spec 复校后的收口（2026-11-02，团队收口）**：①**新增 D10**——`X-GuanJin-Assertion` 剥离的**唯一六步时序**（读→验签→claim→注入→替换视图→handler），明确否决「验签前剥离」；②**D6(5) 扩为三处默认值并统一为「纠错性默认值」口径**，`jwks-uri` 从 **CH-5 移出**（解决同类不同标），CH-5 只留 29 项与删 `mode`；③**新增 D11**——`GJ-CAP-13` 密级方法冻结为 `withClearance(level)`，`dataLabel()` 作废。以上标「Spec 复校（2026-11-02）」，**未动用追认标注**。

**密评后置 + TLS 出边界（2026-11-02，依 spencer 口径）**：①**密评整体后置**——本期**不做判定、不出证、不送检**；R2/R4 改为「后置（DEFERRED）：本期不做判定」，R3 关闭（**TLS 不属本平台范围**）。②**等保相关的代码要求照基线实现、不受影响**（双因素、审计不可篡改、三员分权、脱敏留痕），只把「出证 / 送检判定」后置。③**P2 拆分并重写**：纯软国密/不接密码机保留为已生效前提；TLS 部分改为「不属于本平台范围（部署方前置）」，**不再有"只预留"的表述**，且**不写进代码、不进验收判据、不设配置项**。本条**未动用**「经 hugo 追认」标注。

**Spec 终校后的两处补齐（2026-11-02，团队收口）**：①**D-1 数字自洽**——D6(5) 的 `PENDING` 由 `12 → 11` 更正为 `12 → 11 → **10**`（`AC-SDK-03` 因 `jwks-uri` 纠错降 1、`AC-SDK-02` 因 D6(4) 加固再降 1），CH-5 判据列删去 `AC-SDK-02`，与 Gate v6 的 `68 / 10` 一致；②**D-2 客体密级入口**——D11 补选**方案①（不做声明式打标、调用点传 `Authz.checkLabel(objLevel)`）**，判定为**不进 CH 清单**（不新增 API 面）、**登记 ADR-004**。以上标「Spec 终校（2026-11-02）」，未动用追认标注。

**Spec 复校二轮 → D11 三处补口（2026-11-02，团队收口，未动用追认标注）**：①**C1** 裸 `clearance()` 与 `dataLabel()` 一并作废（唯一写法 `withClearance(level)`），门禁口径 = 正则 `(?<![A-Za-z0-9_])clearance\(` + 范围限 SDK 三语言仓 + 四个注入各验一次，**Gate 已在其 `acceptance-criteria.md` v9 落判据**（`AC-ENG-08`③ 由单一 `dataLabel(` 扩为两个禁词；账目与等保代码判据未动）；②**B1** `level` 词表冻结 **1=内部 / 2=秘密 / 3=机密**，「公开」= 不声明、无 0 级，随 ADR-004 入库（仅影响 M5）；③**C2** `checkLabel(objLevel)` 形参名统一（主体 `level` / 客体 `objLevel`）。本段**未动用**「经 hugo 追认」标注，也不改任何账目数字。

**Edge/Core 对齐结论（2026-11-02，团队收口，非追认）**：`flows` 表按两人复核并入 6 处——①新增第 13 列 `trace_id`（Edge/Core 均赞成）；②`flow.ttl_seconds` seed 600 + `ON CONFLICT DO NOTHING` + 键缺失启动 fail-fast + 代码不留常量；③加锁顺序 `sessions→flows`，并追加「`sessions` 禁 `FOR UPDATE`、PBKDF2/投递等重活必须在锁外」（I-F8）；④**清理器改两段式**（置 `expired` 不删 → 7 天后 `DELETE`），消除 `AC-FLOW-10` flaky；⑤`completed` 行允许**一次性**回填 `session_id`（I-F7 例外条款）；⑥`challenges` 不加 `flow_id`、不反查，改请求显式携带 → 已登记为对外契约变更 **CH-1（待追认）**。另采纳 Edge/Core 的落点划分（Core：`internal/flow` + `internal/session` 窄接口 `AbandonBySession`；Edge：`store/model` + `repo_flows`（含带 tx 的 `LockByID`）+ `pgxerr` 双键映射）。**本条不含任何追认标注。**
