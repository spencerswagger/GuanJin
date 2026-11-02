# 关津 · 技术栈与工程约定

| 项目 | 内容 |
|---|---|
| 文档类型 | 实现规格（工程约定）· 索引 §6.2 缺口 5 的产出 |
| 编写日期 | 2026-10-11 |
| 解决的问题 | 技术栈只知「Go + Vue + Spring Boot Starter」，没有目录结构、依赖选型、构建与部署方式。本文给出**可直接照着搭工程**的目录树、依赖清单与版本、命令、配置样例与代码约定 |
| 上位依据 | `GuanJin-设计基线索引.md` §2（已生效决定，为准）、`GuanJin-数据模型规格.md`、`GuanJin-接口清单.md`、`GuanJin-SDK契约.md`、`GuanJin-数据面设计.md`、`GuanJin-权限模型设计.md`、`GuanJin-因子分类与能力模型.md` |
| 冲突裁决 | 与索引 §2 冲突以索引为准；本文未定义者一律标 **⚠️ 待定**（见索引 §4.2 / §5） |
| 地位 | 这是**工程约定**，不是产品设计。凡涉及安全语义，本文只做「怎么落地」，语义以基线为准 |

> **阅读约定**：每节关键决策后附「**为什么这么定**」，说明取舍理由，便于后续复议时知道动了什么。
> **表名/概念名**一律使用基线给定名字（14 张表），不得改名。

---

## 0. 已拍板的选型（本项目的工程约束基线）

| 决策 | 结论 | 本文如何承接 |
|---|---|---|
| 数据面形态 | **薄数据面 + 前置成熟组件**：关津只做依赖认证语义的 7 件事；TLS 终结 / WAF / 负载均衡 / 灰度 / 熔断全部前置给 nginx 或 Kong | §8.2 compose 含前置网关；§3 `internal/gateway` 只保留 7 件事 |
| 后端数据访问 | **GORM** | §5 五条工程约束；§6 schema 与 ORM 分离 |
| Spring Boot SDK | **只支持 2.x（javax）** | §3.4 SDK 分 `guanjin-core` + `guanjin-spring-boot2`；§4.3 依赖清单按 2.7.18 / javax |
| C 端个人中心 | **全量**（改密 + 换绑 + 会话管理 + 注销 + 数据导出） | §2 前端能力范围；对应端点见 `接口清单` §3.5 个人中心面 |

---

## 1. 一页结论：选型总原则

| # | 原则 | 含义 | 为什么这么定 |
|---|---|---|---|
| 1 | **薄核心 + 适配层** | 核心逻辑与框架解耦：Go 侧把「编排 / 因子 / 凭据」写在纯 Go 包里；Java 侧把一切写在 `guanjin-core`（零 Spring 依赖），Spring 只出现在 `guanjin-spring-boot2` 适配模块 | Spring Boot 2.7 已 EOL（见 §1.1），把 Spring 隔离在一个薄模块里，将来升 3.x 只新增一个适配模块，核心零改动 |
| 2 | **Schema 与 ORM 分离** | `golang-migrate` 是 schema 的唯一权威；GORM 只做**读写映射**，禁止 `AutoMigrate` | ORM 无法表达触发器 / 部分索引 / CHECK / 哈希链约束（数据模型规格里有大量此类）。让 ORM 反推 schema 必然丢约束 |
| 3 | **无状态优先、状态落库** | 应用副本无本地重要状态；挑战、会话的**权威状态落 PostgreSQL**；Redis 只承载限流计数、热缓存与 `jti` 防重放 | 承载数据面后必须多副本；本地状态会让多副本行为不一致。这也是接口清单 §5.9 已裁定的口径 |
| 4 | **国密固定最高级 + 算法可替换抽象** | 口令 PBKDF2-SM3 / 摘要 SM3 / 审计 HMAC-SM3 / 敏感字段 SM4 / 网关断言 SM2 / 对外令牌 SM3withSM2，均固定最高级、与档位无关；同时签名/摘要留可替换接口 | 把「最贵的变更」锁成常量，就等于让平台从「每次合规升级都可能全量重加密」中解放（数据模型 §5.5） |
| 5 | **fail-closed** | 依赖（Redis / DB / 密钥）不可用时**拒绝服务**，绝不降级放行 | 降级放行 = 被攻击时自动解除防护（基线硬规则） |
| 6 | **前置成熟组件** | TLS / WAF / 通用限流 / 负载均衡 / 灰度 / 熔断交给 nginx / Kong；关津只站在认证语义上 | 自研通用网关的成熟度天然低于成熟组件，剥得越干净长期维护成本越低（数据面设计 §5） |
| 7 | **简单 CRUD 用链式、复杂查询用裸 SQL、高频写入用批处理** | GORM 只用于简单路径；审计检索 / 按 Client 导出 / 多表 join 分页用 `Raw()`；`auth_events` / `audit_logs` 用 `CreateInBatches` 或 `COPY` | 见 §5——这是本项目 GORM 的真实风险点 |

### 1.1 Spring Boot 2.7 已 OSS EOL —— 缓解方案（必读）

**事实**：Spring Boot 2.7 于 **2023-11 停止开源维护**（仅商业支持）。

**风险**：用 EOL 框架做安全组件，在**等保测评 / 密评**中可能被质疑「组件已停止维护」，被视为引入高风险依赖。而 SDK 是装进**业务系统**里的，影响面是 100 个业务方。

**缓解方案（本文强制）**：

```text
sdk/
├── guanjin-core/            # ★ 框架无关：SM2/SM3 验签、JWT claim 校验、jti 重放、
│                          #   jcasbin 本地判定、Agent HMAC 客户端、JWKS 拉取与缓存
│                          #   —— 依赖只有 BouncyCastle / jcasbin / nimbus-jose-jwt
│                          #   —— 不 import 任何 org.springframework.*、不 import javax.servlet.*
├── guanjin-spring-boot2/    # ★ 适配层：javax.servlet Filter、AutoConfiguration、
│                          #   @ConfigurationProperties、SecurityContext 注入
└── pom.xml                # 多模块聚合
```

- **核心零 Spring 依赖**：`guanjin-core` 对「验签 / 判定 / 上报」是完整的，Spring 只负责把它接到 servlet 生命周期上。
- **将来加 3.x = 新增一个 `guanjin-spring-boot3` 模块**（`jakarta.servlet` 命名空间 + `AutoConfiguration.imports`），`guanjin-core` 与业务代码零改动。
- **文档口径**：向业务方明确建议**逐步升级 Spring Boot**；SDK 同时支持 2.x/3.x 的路径是「同核心 + 双适配模块」，不是「逼业务方锁死在 2.x」。

> **SDK 的 Spring Boot 版本**：只出 2.x（javax）产物（见索引 §2）；《SDK 契约》§4 的示例已按此对齐。

**为什么这么定**：把「框架世代」这个唯一的 EOL 风险点限制在一个可替换的薄模块内，是让 EOL 从「架构级风险」降级为「一个模块的替换成本」的最小代价做法。

---

## 2. 技术栈清单（版本 + 一句理由）

> 版本为**建议基线**，最终以 `go.mod` / `web/package.json` / `pom.xml` 锁定为准。已尽量与现有仓库（`go 1.25.7`、Vue 3.5、Vite 8、PostgreSQL 16）保持一致。

### 2.1 后端（Go）

| 项 | 版本 | 理由 |
|---|---|---|
| Go | 1.25（toolchain `go1.25.x`） | 与现有仓库一致；`net/http/httputil.ReverseProxy` 与 `pgx` 生态成熟 |
| HTTP 框架 | `github.com/gin-gonic/gin` v1.10.x | 生态与中间件最成熟、团队熟悉；数据面反代另用标准库 `httputil.ReverseProxy`（不套 Gin 做转发热路径） |
| ORM | `gorm.io/gorm` v1.25.x + `gorm.io/driver/postgres` v1.5.x | **已拍板 GORM**；仅用于简单 CRUD（见 §5） |
| 迁移 | `github.com/golang-migrate/migrate/v4` v4.19.x | **schema 唯一权威**；纯 SQL、可 up/down、与 ORM 解耦 |
| 数据库驱动（底层） | `github.com/jackc/pgx/v5` v5.5.x | GORM 的 postgres 驱动即基于 pgx；且 `pgconn.PgError` 是错误码映射的入口、`CopyFrom` 是高频写入的落点 |
| Redis 客户端 | `github.com/redis/go-redis/v9` v9.x | 限流计数 + 热缓存 + `jti` 防重放 |
| UUID | `github.com/google/uuid` v1.6.0 | 提供 **UUID v7**（`uuid.NewV7()`），与数据模型的「应用层生成 UUID v7」约定一致 |
| 国密 | `github.com/tjfoc/gmsm` v1.4.1 | SM2/SM3/SM4 纯 Go，无需 CGO；签名/摘要统一走此处封装 |
| JWT | `github.com/golang-jwt/jwt/v5` v5.2.x | 对外令牌（SM3withSM2/JWT）解析与签发 |
| 日志 | `go.uber.org/zap` v1.27.x | 结构化 JSON 日志，性能好 |
| 配置 | `github.com/caarlos0/env/v11` v11.x（+ `godotenv` 仅本地） | 环境变量强类型绑定；生产只用 env |
| 校验 | `github.com/go-playground/validator/v10` v10.x | 请求体校验 |
| 密码学缓冲 | `golang.org/x/crypto` v0.x | `pbkdf2`（配合 SM3 做 PBKDF2-SM3） |
| 测试 | `github.com/stretchr/testify` v1.9.x + `github.com/testcontainers/testcontainers-go` v0.33.x | 单测断言 + 集成测试起真实 PG16/Redis7 |
| 可观测（可选） | `go.opentelemetry.io/otel` v1.31.x | trace 贯穿（也可只用自实现 trace_id，见 §10） |
| 漏洞扫描 | `golang.org/x/vuln/cmd/govulncheck` | CI 门禁 |

**为什么 Gin + 标准库反代**：认证/管理端点是普通 REST，Gin 能显著降低样板；但数据面反代是热路径，直接用 `httputil.ReverseProxy` 可精确控制连接池、超时、header 改写，避免通用中间件的额外开销——**两条路径用不同工具，是「薄核心」原则在 HTTP 层的体现**。

### 2.2 前端（Vue）

| 项 | 版本 | 理由 |
|---|---|---|
| Vue | ^3.5 | 与现有仓库一致；组合式 API 适合动态步骤渲染 |
| 构建 | Vite ^8 + `vue-tsc` ^3 | 现有仓库已用；构建快 |
| 语言 | TypeScript ~6.0 | 现有仓库已用；接口契约类型化 |
| 路由 | vue-router ^4.6 | 现有仓库已用 |
| 状态 | pinia ^3 | 会话语义集中（登录态、Flow Descriptor） |
| UI 组件库 | element-plus ^2.9 | 管理后台表格/表单密集；C 端个人中心复用同一套，减少依赖数 |
| 组件自动导入 | unplugin-auto-import + unplugin-vue-components | Element Plus 按需引入 |
| HTTP | axios ^1.7 | 拦截器统一注入 token / 处理错误码 |
| 工具 | @vueuse/core ^11 | 常用组合式工具 |
| 国密（按需） | sm-crypto ^0.5.5 | 现有仓库已用；仅用于前端确实需要的 SM3/SM2 场景（如前端脱敏校验），**不作为安全边界** |
| Lint/格式 | eslint ^9 + @vue/eslint-config-typescript + prettier ^3 | 见 §9 |
| 测试 | vitest ^2 + @vue/test-utils ^2 | 关键组件与逻辑单测 |

**C 端个人中心能力范围（全量）**：改密、换绑手机/邮箱、会话列表与踢下线、注销、数据导出（PIPL）。对应页面：`views/me/{Password,Identifiers,Sessions,Deactivate,Export}.vue`。

**为什么 Element Plus 一个库覆盖两端**：C 端个人中心与管理后台都以表单/表格为主，统一组件库可省掉一套设计令牌维护；若后期 C 端需要差异化皮肤，再抽主题层即可，不引入第二套组件库。

### 2.3 SDK（Java / Spring Boot 2.x）

| 项 | 版本 | 理由 |
|---|---|---|
| Java | 17（source/target） | Spring Boot 2.7 支持 Java 17；javax 命名空间是本决策关键 |
| Spring Boot | 2.7.18 | **EOL 缓解见 §1.1**；适配层用 `spring-boot-autoconfigure` |
| Spring Security | 5.7.x（由 Boot 2.7 BOM 管理） | 身份注入 `SecurityContext`，让业务方 `@PreAuthorize` 继续可用 |
| Servlet API | `javax.servlet-api` 4.0.1（provided） | **2.x 关键：`javax.servlet`，非 `jakarta.servlet`** |
| 国密 | `org.bouncycastle:bcprov-jdk18on` 1.78.x | SM2/SM3 实现（JDK8+ 兼容） |
| 授权判定 | `org.casbin:jcasbin` 1.55.x | 权限模型设计 §3 选型：一个 `model.conf` 逐层生效 |
| JWT 解析 | `com.nimbusds:nimbus-jose-jwt` 9.37.x | claim 校验、JWKS 解析；SM2 验签叠 BouncyCastle |
| 测试 | JUnit 5.10 + Mockito 5.11 | `guanjin-core` 必须被单测覆盖 |

**为什么 SDK 需要 `guanjin-core` 与 `guanjin-spring-boot2` 两个模块**：见 §1.1。

### 2.4 数据与基础设施

| 项 | 版本 | 理由 |
|---|---|---|
| PostgreSQL | 16（现有 compose） | 与现有仓库一致；⚠️ `uuidv7()` 需 PG18，故采用**应用层生成 UUID v7**（见 §5.0） |
| Redis | 7.x | 限流计数 + 热缓存 + `jti` 防重放 |
| 前置网关 | nginx 1.27（或 Kong 3.x） | TLS 终结、粗路由、WAF、通用限流、回源鉴权（mTLS）。**关津只看明文** |
| 容器 | Docker 24+ / Compose v2 | 本地与单机部署 |
| 编排（可选） | Kubernetes 1.29+ | 多副本、滚动发布、Secret 注入。多数场景可只用 compose + nginx |
| 密钥注入 | K8s Secret / Docker secret / Vault | 主密钥只从环境变量注入，不落库（§7.3） |

---

## 3. 仓库与目录结构

### 3.1 顶层目录树

```text
guanjin-next/
├── cmd/                              # 进程入口（多二进制：控制面与数据面分离）
│   ├── guanjind/                       # 控制面 + 认证服务 + 管理面 + Agent 面
│   │   └── main.go
│   └── guanjingw/                      # 数据面（反向代理 + 认证钩子 + 身份注入）
│       └── main.go
│
├── internal/                         # 平台私有代码（不被外部 import）
│   ├── protocol/                     # 【协议面】OIDC/OAuth2 标准端点
│   │   ├── discovery.go              #   /.well-known/*
│   │   ├── authorize.go              #   /authorize /authorize/flow /authorize/submit
│   │   ├── token.go                  #   /oauth/token /oauth/revoke /oauth/introspect
│   │   ├── userinfo.go               #   /userinfo
│   │   └── logout.go                 #   /logout（按档位级联 SLO）
│   ├── flow/                         # 【编排】Flow Descriptor + 阶梯认证状态机
│   │   ├── descriptor.go             #   目标档位/已达成/待补步骤/禁用因子
│   │   ├── orchestrator.go           #   按"能力断言"选因子，禁止 if type=="sms"
│   │   ├── register.go               #   /auth/register/{start,complete}（注册即登录）
│   │   └── recover.go                #   /auth/recover/{start,verify,complete}（统一恢复）
│   ├── factor/                       # 【因子 SPI】能力接口 + Provider 实现 + 注册表
│   │   ├── provider.go               #   Provider / Descriptor
│   │   ├── capabilities.go           #   10 个能力接口（Enrollable…RiskReporting）
│   │   ├── registry.go               #   按 type 注册；合法性在代码侧校验（非 DB CHECK）
│   │   ├── holding/                  #   password / totp / recovery_code（基类 + 实现）
│   │   └── channel/                  #   sms_otp / email_otp（基类 + Send 抽象）
│   ├── identifier/                   # 【标识符】规范化 / 盲索引 / 唯一性 / 不变式
│   │   ├── normalize.go              #   email 小写、phone E.164、username 小写
│   │   ├── blindindex.go             #   HMAC-SM3 盲索引 + SM4 密文 + 脱敏
│   │   └── invariants.go             #   I1/I2/I3/I4/I6 服务层校验（§4.1 数据模型）
│   ├── credential/                   # 【凭据】口令派生 / TOTP / 恢复码 的存储与比对
│   │   ├── password.go               #   PBKDF2-SM3（迭代≥100000，盐≥16B）+ 历史校验
│   │   ├── totp.go                   #   TOTP（SM3，时间窗）
│   │   └── recovery.go               #   一次性恢复码集（SM3 摘要存储、用后标记）
│   ├── gateway/                      # 【数据面】只做 7 件事（数据面设计 §1.1）
│   │   ├── proxy.go                  #   httputil.ReverseProxy：连接池/超时/背压
│   │   ├── admit.go                  #   令牌验签 + 会话校验 + 按 client 档位判 ACR
│   │   ├── inject.go                 #   逐请求签 X-GuanJin-Assertion（SM2，exp=60s）
│   │   ├── strip.go                  #   剥离客户端伪造的 X-GuanJin-Assertion
│   │   ├── revoke.go                 #   实时撤销判定（查询 sessions）
│   │   ├── slo.go                    #   单点登出签名通知
│   │   └── route.go                  #   route → client 映射（见索引 §4.2-18）
│   ├── authz/                        # 【授权】role_bindings + 门槛方向矩阵 + 会话撤销联动
│   │   ├── binding.go                #   绑定/解绑（副作用：撤销该用户会话）
│   │   └── direction.go              #   门槛项"只升不降"方向校验器（4 类方向）
│   ├── audit/                        # 【审计】auth_events / audit_logs + HMAC-SM3 哈希链
│   │   ├── event.go                  #   认证事件追加（高频，批处理）
│   │   ├── chain.go                  #   审计哈希链写入（pg_advisory_xact_lock，§5.6）
│   │   └── export.go                 #   按 Client 切分导出（裸 SQL + 游标）
│   ├── risk/                         # 【风控】失败计数 / 锁定 / 限流（Redis 权威计数）
│   │   ├── counter.go                #   账号级失败计数与锁定（全局共享，按 user_id）
│   │   ├── ratelimit.go              #   IP×标识符×设备；短信日配额
│   │   └── captcha.go                #   风险触发图形验证
│   ├── admin/                        # 【管理面】三员各自的接口
│   │   ├── users.go  clients.go  keys.go  config.go
│   │   ├── factors.go policies.go auditlogs.go sessions.go
│   │   └── rbac.go                   #   三员角色 → 接口的鉴权矩阵
│   ├── session/                      # 【会话】sessions 生命周期 + 撤销 + 并发上限
│   │   └── manager.go
│   ├── agent/                        # 【Agent 面】SDK ↔ 平台（HMAC）
│   │   ├── report.go policies.go hmac.go
│   ├── crypto/                       # 【国密】SM2/SM3/SM4 封装 + 主密钥派生 + JWKS
│   │   ├── sm2.go sm3.go sm4.go
│   │   ├── masterkey.go              #   HKDF-SM3 派生子密钥（SM4 键 / HMAC 键 / …）
│   │   ├── pwdcrypto.go              #   PBKDF2-SM3
│   │   └── jwks.go                   #   发布与缓存（kid 区分 user_token / gateway）
│   ├── store/                        # 【数据访问】GORM 模型 + 仓储 + 事务 + 错误映射
│   │   ├── db.go                     #   gorm.Open + 连接池 + 命名策略（绝不 AutoMigrate）
│   │   ├── model/                    #   GORM 模型（仅映射；无 DeletedAt）
│   │   │   ├── base.go               #   BaseModel / AppendOnlyModel / UUIDv7 hook
│   │   │   ├── users.go identifiers.go authenticators.go challenges.go
│   │   │   ├── sessions.go auth_events.go audit_logs.go clients.go
│   │   │   ├── login_policies.go signing_keys.go runtime_config.go
│   │   │   └── role_bindings.go policy_snapshots.go user_visited_clients.go
│   │   ├── pgxerr.go                 #   pgconn.PgError(23505) → 业务错误码
│   │   ├── repo_identifiers.go repo_sessions.go repo_audit.go …
│   │   └── query/                    #   复杂查询：裸 SQL（审计检索/导出/join 分页）
│   ├── config/                       # 启动配置（环境变量）+ 运行时配置（DB runtime_config）
│   │   ├── env.go                    #   环境变量绑定 + 校验（fail-fast）
│   │   └── runtime.go                #   runtime_config 读取与热更新（不缓存敏感项）
│   ├── httpx/                        # 统一响应 / 错误码 / 中间件
│   │   ├── error.go                  #   AppError{Code,HTTP,Details} + E1..E8 分层
│   │   ├── response.go               #   {code,message,trace_id,details}
│   │   └── middleware.go             #   trace_id / recover / access-log / ratelimit
│   └── observability/                # 结构化日志、指标、trace
│       ├── logger.go                 #   zap 配置（JSON）
│       ├── trace.go                  #   traceparent 贯穿 + context
│       └── metrics.go                #   Prometheus /metrics（仅内网）
│
├── migrations/                       # ★ golang-migrate 的 SQL（schema 唯一权威）
│   ├── 000000_common_functions.up.sql        # set_updated_at() / forbid_mutation()
│   ├── 000000_common_functions.down.sql
│   ├── 000001_create_users.up.sql / .down.sql
│   ├── 000002_create_clients.up.sql / .down.sql
│   ├── 000003_create_identifiers.up.sql / .down.sql
│   ├── 000004_create_authenticators.up.sql / .down.sql
│   ├── 000005_create_sessions.up.sql / .down.sql
│   ├── 000006_create_challenges.up.sql / .down.sql
│   ├── 000007_create_role_bindings.up.sql / .down.sql
│   ├── 000008_create_policy_snapshots.up.sql / .down.sql
│   ├── 000009_create_user_visited_clients.up.sql / .down.sql
│   ├── 000010_create_auth_events.up.sql / .down.sql
│   ├── 000011_create_audit_logs.up.sql / .down.sql
│   ├── 000012_create_login_policies.up.sql / .down.sql
│   ├── 000013_create_signing_keys.up.sql / .down.sql
│   └── 000014_create_runtime_config.up.sql / .down.sql
│
├── web/                              # Vue 3 前端
│   ├── src/
│   │   ├── api/                      #   axios 封装 + 接口契约类型
│   │   ├── views/
│   │   │   ├── auth/                 #   统一登录页（动态步骤渲染）注册/找回
│   │   │   ├── me/                   #   ★ C 端个人中心全量
│   │   │   │   ├── Profile.vue
│   │   │   │   ├── Password.vue      #   改密
│   │   │   │   ├── Identifiers.vue   #   换绑手机/邮箱
│   │   │   │   ├── Sessions.vue      #   会话管理
│   │   │   │   ├── Deactivate.vue    #   注销
│   │   │   │   └── Export.vue        #   数据导出
│   │   │   └── admin/                #   管理后台（三员）
│   │   ├── components/               #   FlowStep / CodeInput / QrCode …
│   │   ├── stores/                   #   pinia：登录态、Flow Descriptor
│   │   ├── router/                   #   路由 + 守卫
│   │   └── styles/
│   ├── package.json  vite.config.ts  tsconfig.json  eslint.config.js
│
├── sdk/                              # Java SDK（Maven 多模块）
│   ├── pom.xml                       #   聚合 + dependencyManagement(spring-boot-dependencies:2.7.18)
│   ├── guanjin-core/                   #   ★ 框架无关
│   ├── guanjin-spring-boot2/           #   ★ javax 适配层
│   └── (将来) guanjin-spring-boot3/    #   仅新增此模块即可支持 Boot 3
│
├── deploy/
│   ├── nginx/nginx.conf              #   前置网关：TLS 终结 / 粗路由 / WAF / 回源鉴权
│   ├── docker-compose.yml            #   含前置网关 + 多副本
│   ├── k8s/                          #   可选
│   └── secrets/                      #   ⚠️ 不入库，仅示例占位
│
├── docs/                             # 本文件所在设计基线
├── scripts/
│   ├── check-no-automigrate.sh       #   CI：禁止 AutoMigrate / 软删除
│   └── gen-master-key.sh             #   openssl rand -base64 32
├── .golangci.yml
├── .editorconfig
├── Makefile
├── go.mod  go.sum
├── Dockerfile
└── README.md
```

### 3.2 为什么后端按这 10 个包分

分包依据是**基线的「面」与「领域对象」**，不是技术分层：

| 包 | 对应基线 | 为什么单独成包 |
|---|---|---|
| `protocol` | 接口清单 §2.1 协议面（10 端点） | 标准 OIDC 语义冻结在此，禁止掺平台私有语义 |
| `flow` | 接口清单 §2.3/2.6 + 档位「阶梯认证」 | 编排状态机是**厚**逻辑（ACR 判定 + 状态机），必须独立可测 |
| `factor` | 因子分类与能力模型（全篇） | 「因子无限扩充」的落地点；分包以强制「按能力断言、禁止 type 分支」 |
| `identifier` | 数据模型 §3.2 + 不变式 I1/I2/I6 | 规范化/盲索引/唯一性是无法分散的安全细节 |
| `credential` | 数据模型 §3.3 + 算法归属表 | 口令/TOTP/恢复码的材料处理集中，避免散落 |
| `gateway` | 数据面设计 §1.1（7 件事） | 站在流量路径上，独立二进制、独立压测与扩容 |
| `authz` | 权限模型设计 §0 ③/§5 | 授权动作 + 门槛方向矩阵 + 会话撤销联动是同一事务语义 |
| `audit` | 数据模型 §3.6/§3.7 | 只增表 + 哈希链写序是全局唯一的约束点 |
| `risk` | 档位清单 §二/§三 | 全局共享的失败计数/限流状态 |
| `admin` | 接口清单 §2.7（34 端点） | 三员鉴权矩阵集中一处，便于审计每个接口的角色要求 |

**为什么 `gateway` 单独一个二进制**：数据面与认证服务的**负载特征差一个数量级**（数据面承受全量 QPS、认证服务低频），部署/扩容/熔断策略完全不同；分成两个 `cmd` 用同一组 `internal/*` 包，是「薄核心 + 可独立伸缩」的落地。

---

## 4. 关键依赖与版本清单

### 4.1 Go（`go.mod` 参考）

```go
module github.com/yourorg/guanjin-next

go 1.25

require (
	// HTTP / 反代
	github.com/gin-gonic/gin v1.10.1
	// 数据访问（仅简单 CRUD；schema 由 migrations 管）
	gorm.io/gorm v1.25.12
	gorm.io/driver/postgres v1.5.11
	github.com/jackc/pgx/v5 v5.5.4          // 底层驱动 + pgconn.PgError + CopyFrom
	github.com/golang-migrate/migrate/v4 v4.19.1
	github.com/redis/go-redis/v9 v9.22.0
	// 基础
	github.com/google/uuid v1.6.0           // UUID v7
	github.com/tjfoc/gmsm v1.4.1            // SM2/SM3/SM4
	github.com/golang-jwt/jwt/v5 v5.2.1     // 对外令牌 SM3withSM2
	golang.org/x/crypto v0.45.0             // pbkdf2
	// 工程
	go.uber.org/zap v1.27.0
	github.com/caarlos0/env/v11 v11.2.2
	github.com/go-playground/validator/v10 v10.22.1
	// 测试
	github.com/stretchr/testify v1.9.0
	github.com/testcontainers/testcontainers-go v0.33.0
)

// 可选：OpenTelemetry（若不用自实现 trace_id）
// go.opentelemetry.io/otel v1.31.0
```

### 4.2 前端（`web/package.json` 参考，在现有基础上增补）

```jsonc
{
  "dependencies": {
    "vue": "^3.5.40",
    "vue-router": "^4.6.4",
    "pinia": "^3.0.3",
    "element-plus": "^2.9.1",
    "axios": "^1.7.9",
    "@vueuse/core": "^11.3.0",
    "sm-crypto": "^0.5.5"
  },
  "devDependencies": {
    "vite": "^8.2.0",
    "typescript": "~6.0.2",
    "vue-tsc": "^3.3.8",
    "@vitejs/plugin-vue": "^6.0.8",
    "unplugin-auto-import": "^0.19.0",
    "unplugin-vue-components": "^0.28.0",
    "eslint": "^9.17.0",
    "@vue/eslint-config-typescript": "^14.2.0",
    "prettier": "^3.4.2",
    "vitest": "^2.1.8",
    "@vue/test-utils": "^2.4.6"
  }
}
```

### 4.3 Spring Boot Starter（`sdk/pom.xml` 参考）

```xml
<properties>
  <java.version>17</java.version>
  <spring-boot.version>2.7.18</spring-boot.version>   <!-- EOL 缓解见 §1.1 -->
</properties>

<dependencyManagement>
  <dependencies>
    <dependency>
      <groupId>org.springframework.boot</groupId>
      <artifactId>spring-boot-dependencies</artifactId>
      <version>${spring-boot.version}</version>
      <type>pom</type><scope>import</scope>
    </dependency>
  </dependencies>
</dependencyManagement>

<!-- guanjin-spring-boot2 依赖（javax 命名空间） -->
<dependencies>
  <dependency><groupId>${project.groupId}</groupId>
    <artifactId>guanjin-core</artifactId><version>${project.version}</version></dependency>
  <dependency>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-autoconfigure</artifactId>
  </dependency>
  <dependency>
    <groupId>org.springframework.boot</groupId>
    <artifactId>spring-boot-configuration-processor</artifactId>
    <optional>true</optional>
  </dependency>
  <dependency>
    <groupId>org.springframework.security</groupId>
    <artifactId>spring-security-core</artifactId>
  </dependency>
  <!-- ★ 2.x 关键：javax.servlet，非 jakarta.servlet -->
  <dependency>
    <groupId>javax.servlet</groupId>
    <artifactId>javax.servlet-api</artifactId>
    <version>4.0.1</version>
    <scope>provided</scope>
  </dependency>
</dependencies>

<!-- guanjin-core 依赖（无任何 Spring / servlet 依赖） -->
<dependencies>
  <dependency><groupId>org.bouncycastle</groupId>
    <artifactId>bcprov-jdk18on</artifactId><version>1.78.1</version></dependency>
  <dependency><groupId>org.casbin</groupId>
    <artifactId>jcasbin</artifactId><version>1.55.0</version></dependency>
  <dependency><groupId>com.nimbusds</groupId>
    <artifactId>nimbus-jose-jwt</artifactId><version>9.37.3</version></dependency>
</dependencies>
```

**为什么 `guanjin-core` 明确不列 Spring/servlet**：这是「将来加 3.x 只新增适配模块」的物理保证——只要核心不 import 框架，适配层就是可替换的。

---

## 5. 数据访问约定（GORM 五条工程约束）

> 这五条是本项目 GORM 的**真实风险点**，全部写进 `.golangci.yml` / CI / 代码评审清单。

### 5.0 前置：`gorm.Open` 与 UUID v7

```go
// internal/store/db.go
package store

import (
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func Open(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: false, // 用扩展协议，支持 prepared statement
	}), &gorm.Config{
		// ★ schema 由 golang-migrate 管理，此处绝不 AutoMigrate（见 §5.2）
		NamingStrategy: schema.NamingStrategy{SingularTable: false}, // users / auth_events
		NowFunc:        func() time.Time { return time.Now().UTC() },
		Logger:         logger.Default.LogMode(logger.Warn), // 生产接 zap 适配器，见 §10
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}
```

> ⚠️ **UUID v7 由应用层生成**：数据模型 §1.1 建议「应用层生成后写入，摆脱 DB 版本约束」，而现有 compose 用的是 **PostgreSQL 16**（`uuidv7()` 需 PG18）。因此模型在 `BeforeCreate` 里生成 UUID v7，migration 里**不写 `DEFAULT uuidv7()`**（或仅在确认 PG18 时才写）。见索引 §4.2-13。

```go
// internal/store/model/base.go
package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BaseModel：所有「可变表」的公共列。
// ★★ 绝不添加 gorm.DeletedAt —— 见 §5.1 ★★
type BaseModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (m *BaseModel) BeforeCreate(*gorm.DB) error {
	if m.ID == uuid.Nil {
		id, err := uuid.NewV7() // 时间有序，利于 B-tree 顺序写
		if err != nil {
			return err
		}
		m.ID = id
	}
	return nil
}

// AppendOnlyModel：只增表（auth_events / audit_logs）——**没有 UpdatedAt**。
type AppendOnlyModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at;not null;autoCreateTime"`
}

func (m *AppendOnlyModel) BeforeCreate(*gorm.DB) error {
	if m.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		m.ID = id
	}
	return nil
}

// 每个模型显式声明表名，避免命名策略漂移。
func (User) TableName() string         { return "users" }
func (Identifier) TableName() string   { return "identifiers" }
func (AuthEvent) TableName() string    { return "auth_events" }
func (AuditLog) TableName() string     { return "audit_logs" }
// …其余同
```

### 5.1 约束一：禁用软删除

**规则**：不定义 `DeletedAt` 字段，不 import `gorm.DeletedAt`。业务表用 `status`（`active`/`disabled`）表达停用；审计表**物理保留**，由留存策略整段清理。

```go
// ✅ 正确：用 status 表达状态，无 DeletedAt
type User struct {
	model.BaseModel
	Username      string     `gorm:"column:username;not null"`
	Status        string     `gorm:"column:status;not null;default:active"`
	IsAdmin       bool       `gorm:"column:is_admin;not null;default:false"`
	DisabledAt    *time.Time `gorm:"column:disabled_at"`
	DisabledReason *string   `gorm:"column:disabled_reason"`
	EffectiveTier *string    `gorm:"column:effective_tier"`
	// ★ 禁止：DeletedAt gorm.DeletedAt `gorm:"index"` ★
}

// ✅ 停用 = 改 status；撤销会话在同一事务内做（见 §5.3 / authz）
func (r *UserRepo) Disable(ctx context.Context, userID uuid.UUID, reason string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		return tx.Model(&User{}).
			Where("id = ?", userID).
			Updates(map[string]any{
				"status":          "disabled",
				"disabled_at":     now,
				"disabled_reason": reason,
			}).Error
	})
}
```

**为什么这么定**：一旦模型里有 `gorm.DeletedAt`，GORM 会给**每一次查询**自动追加 `WHERE deleted_at IS NULL`——对认证流水（只增不改）毫无意义，还会掩盖真正的状态语义；基线要求「审计表不软删、其余用 `status`」，软删除模型正好与这条相反。

**CI 门禁**（`scripts/check-no-automigrate.sh`）：

```bash
#!/usr/bin/env bash
set -euo pipefail
# 禁止软删除字段
if grep -rn "gorm.DeletedAt\|DeletedAt \*time.Time" --include="*.go" internal/ ; then
  echo "❌ 禁止使用 gorm.DeletedAt（本项目用 status 字段，审计表物理保留）" >&2; exit 1
fi
# 禁止 AutoMigrate 上线
if grep -rn "AutoMigrate(" --include="*.go" internal/ cmd/ ; then
  echo "❌ 禁止调用 AutoMigrate（schema 由 golang-migrate 管理）" >&2; exit 1
fi
echo "✅ 无软删除字段、无 AutoMigrate"
```

### 5.2 约束二：Schema 只由 `golang-migrate` 管，禁止 `AutoMigrate` 上线

**规则**：
- 生产代码**任何位置**不得出现 `db.AutoMigrate(...)`；
- schema 变更只走 `migrations/*.sql`（§6）；
- 允许的开发期例外**不存在**——即使本地也只用 migrate 命令。

**为什么这么定**：`AutoMigrate` 只做「加列 / 加索引 / 加外键」，**不会删列、不会安全改类型、不会加 CHECK、不会建触发器与部分索引**。基线数据模型里有大量这类约束（`forbid_mutation()` 触发器、`identifiers_one_primary_per_user` 部分索引、`runtime_config_no_master_key` CHECK、哈希链列）——用 AutoMigrate 必然丢约束，且没有 down 迁移。

**替代**：见 §6。GORM 只承担「把 Go 结构体映射到既有表」的读写职责。

### 5.3 约束三：唯一约束冲突必须映射成业务错误码

**规则**：仓储层统一捕获 `pgconn.PgError`，`Code == "23505"`（`unique_violation`）时按 `ConstraintName` 细分，映射为接口清单 §5.4 已定义的业务错误码；**不得把原始 DB 错误抛到 HTTP 层**。

```go
// internal/store/pgxerr.go
package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// 业务错误（由 httpx 中间件统一映射为 {code,message,trace_id}，见 §10.3）
var (
	ErrIdentifierAlreadyBound = errors.New("IDENTIFIER_ALREADY_BOUND") // 409
	ErrClientIDAlreadyExists  = errors.New("CLIENT_ID_ALREADY_EXISTS")  // 409
	ErrKeyKidExists           = errors.New("KEY_KID_EXISTS")            // 409
	ErrBindingAlreadyExists   = errors.New("BINDING_ALREADY_EXISTS")    // 409
	ErrPolicySnapshotConflict = errors.New("POLICY_SNAPSHOT_CONFLICT")  // 409
	ErrReferenceNotFound      = errors.New("REFERENCE_NOT_FOUND")       // 422（外键不存在）
	ErrInvalidParameter       = errors.New("INVALID_PARAMETER")         // 400（CHECK 违反）
	ErrConflict               = errors.New("CONFLICT")                  // 409（未分类唯一冲突）
)

// mapPgError：把 PostgreSQL 错误分类为业务错误。
// 约束名与 migrations 中的 CONSTRAINTS/INDEX 名严格对应（见 §6.3）。
func mapPgError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505": // unique_violation
		switch pgErr.ConstraintName {
		case "identifiers_type_value_uniq": // (type, value) 全局唯一
			return ErrIdentifierAlreadyBound
		case "users_username_lower_uidx": // lower(username) 唯一
			return ErrIdentifierAlreadyBound
		case "clients_pkey": // client_id 主键
			return ErrClientIDAlreadyExists
		case "signing_keys_kid_key": // kid 唯一
			return ErrKeyKidExists
		case "role_bindings_uniq": // (user_id, client_id, role)
			return ErrBindingAlreadyExists
		case "policy_snapshots_uniq": // (client_id, version)
			return ErrPolicySnapshotConflict
		case "user_visited_clients_uniq": // upsert 场景：由 ON CONFLICT 处理，不应到达这里
			return ErrConflict
		default:
			return ErrConflict
		}
	case "23503": // foreign_key_violation
		return ErrReferenceNotFound
	case "23514": // check_violation（如 runtime_config_no_master_key）
		return ErrInvalidParameter
	default:
		return err
	}
}
```

仓储使用（**每个写操作都必须过 `mapPgError`**）：

```go
// internal/store/repo_identifiers.go
func (r *IdentifierRepo) Create(ctx context.Context, m *model.Identifier) error {
	return mapPgError(r.db.WithContext(ctx).Create(m).Error)
}
```

**为什么这么定**：`23505` 是唯一的「并发下正常会发生」的错误（两个用户同时注册同一手机号）。若直接冒泡，HTTP 层只能返回 500 或泛化的 409，前端无法给出「该手机号已绑定」这类精确提示；把约束名 → 错误码做成**一张表**，也让「新增唯一约束必须同步补映射」成为可评审的改动。

### 5.4 约束四：复杂查询走 `Raw()` 裸 SQL

**约定**：**简单 CRUD 用链式 API，复杂查询用裸 SQL**。复杂 = 多表 join + 分页 + 动态条件，或需要走特定索引的检索。

```go
// internal/store/query/audit_query.go
package query

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AuditSearch struct {
	From, To *time.Time
	UserID   *uuid.UUID
	ClientID *string
	Action   *string
	Result   *string
	Page     int
	PageSize int
}

type AuditRow struct {
	ID         uuid.UUID  `gorm:"column:id"`
	Action     string     `gorm:"column:action"`
	TargetType *string    `gorm:"column:target_type"`
	TargetID   *string    `gorm:"column:target_id"`
	ClientID   *string    `gorm:"column:client_id"`
	ActorID    *uuid.UUID `gorm:"column:actor_user_id"`
	Result     string     `gorm:"column:result"`
	TraceID    *string    `gorm:"column:trace_id"`
	OccurredAt time.Time  `gorm:"column:occurred_at"`
}

// Search：审计检索（多条件 + 分页）。链式 API 会变得又慢又难读，故用裸 SQL。
func SearchAuditLogs(ctx context.Context, db *gorm.DB, q AuditSearch) (rows []AuditRow, total int64, err error) {
	where := " WHERE 1=1"
	args := make([]any, 0, 8)

	if q.From != nil {
		where += " AND occurred_at >= ?"
		args = append(args, *q.From)
	}
	if q.To != nil {
		where += " AND occurred_at < ?"
		args = append(args, *q.To)
	}
	if q.ClientID != nil {
		where += " AND client_id = ?"
		args = append(args, *q.ClientID)
	}
	if q.UserID != nil {
		where += " AND actor_user_id = ?"
		args = append(args, *q.UserID)
	}
	if q.Action != nil {
		where += " AND action = ?"
		args = append(args, *q.Action)
	}
	if q.Result != nil {
		where += " AND result = ?"
		args = append(args, *q.Result)
	}

	if err = db.WithContext(ctx).
		Raw("SELECT count(*) FROM audit_logs"+where, args...).
		Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	pageArgs := append(append([]any{}, args...), q.PageSize, (q.Page-1)*q.PageSize)
	err = db.WithContext(ctx).Raw(`
		SELECT id, action, target_type, target_id, client_id,
		       actor_user_id, result, trace_id, occurred_at
		FROM audit_logs`+where+`
		ORDER BY occurred_at DESC, id DESC
		LIMIT ? OFFSET ?`, pageArgs...).Scan(&rows).Error
	return rows, total, err
}

// ExportByClient：按 Client 切分导出（档位清单 §二 合规要求），游标分页避免深 OFFSET。
func ExportAuditByClient(ctx context.Context, db *gorm.DB, clientID string, after time.Time, limit int) ([]AuditRow, error) {
	var rows []AuditRow
	err := db.WithContext(ctx).Raw(`
		SELECT id, action, target_type, target_id, client_id,
		       actor_user_id, result, trace_id, occurred_at
		FROM audit_logs
		WHERE client_id = ? AND occurred_at > ?
		ORDER BY occurred_at ASC, id ASC
		LIMIT ?`, clientID, after, limit).Scan(&rows).Error
	return rows, err
}
```

> **导出脱敏在应用层完成**：`before`/`after` 存的是**脱敏后** jsonb（数据模型 §3.7），且导出结果里的标识符一律脱敏，裸 SQL 只负责取数。

**为什么这么定**：链式 API 的 `Where` 拼接在条件组合 + `count` + `order` + 游标时会生成大量临时对象，且生成的 SQL 难以对照 `EXPLAIN` 与索引；审计检索是本项目最需要按索引调优的查询（`audit_logs_client_time_idx` 等），必须能看到 SQL 原文。**约定「简单 CRUD 用链式、复杂查询用裸 SQL」，是把 GORM 用在它擅长的地方**。

### 5.5 约束五：高频写入用 `CreateInBatches` 或裸 SQL

**规则**：`auth_events` / `audit_logs` 是高频追加表，**禁止逐行 `Create`**；普通批量用 `CreateInBatches`，极高吞吐用 pgx `CopyFrom`（COPY 协议）。

```go
// internal/store/repo_audit.go
package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"gorm.io/gorm"

	"github.com/yourorg/guanjin-next/internal/store/model"
)

// ✅ 常规批量：假设一次写 200 条事件，分批 500 条/语句
func (r *AuthEventRepo) BatchAppend(ctx context.Context, events []*model.AuthEvent) error {
	if len(events) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).CreateInBatches(events, 500).Error
}

// ✅ 极高吞吐：走 COPY 协议（pgx），绕开 GORM 逐行反射。
// 适用：审计/事件回流、批量补写；需要传入事件的关键列（无 DB 默认值的列必须给全）。
func (r *AuthEventRepo) CopyAppend(ctx context.Context, events []*model.AuthEvent) (int64, error) {
	if len(events) == 0 {
		return 0, nil
	}
	return r.pgx.CopyFrom(ctx,
		pgx.Identifier{"auth_events"},
		[]string{
			"id", "user_id", "identifier_id", "session_id", "client_id",
			"event_type", "factor_type", "result", "reason", "risk_score",
			"acr", "amr", "ip", "user_agent", "device_id", "trace_id",
			"occurred_at", "created_at",
		},
		pgx.CopyFromSlice(len(events), func(i int) ([]any, error) {
			e := events[i]
			return []any{
				e.ID, e.UserID, e.IdentifierID, e.SessionID, e.ClientID,
				e.EventType, e.FactorType, e.Result, e.Reason, e.RiskScore,
				e.ACR, e.AMR, e.IP, e.UserAgent, e.DeviceID, e.TraceID,
				e.OccurredAt, e.CreatedAt,
			}, nil
		}),
	)
}

// ❌ 禁止：for _, e := range events { db.Create(e) } —— 反射开销累积
```

**为什么这么定**：GORM 的 `Create` 每行都要走回调链 + 反射构造 SQL，单条开销看似很小，但在峰值登录 200 QPS（每次登录产生多条事件）+ 批量任务下会线性累积；`CreateInBatches` 把 N 次往返压成 N/500 次，`CopyFrom` 直接走 PG 的 COPY 协议，是追加型宽表的正确落点。

### 5.6 附加约束：审计哈希链的写序（多副本串行化）

`audit_logs` 用 `prev_tag` 串成 **HMAC-SM3 哈希链**（数据模型 §3.7）。多副本下链尾必须全局有序，否则两条并发写入会拿到同一个 `prev_tag`，链断裂。

**方案（本文强制，与数据模型 §6 #9 的默认选择一致）**：写入走 `pg_advisory_xact_lock` 串行化链尾——**事务级锁**，事务结束自动释放，不需要额外单点。

```go
// internal/audit/chain.go
package audit

import (
	"context"
	"database/sql"
	"errors"

	"gorm.io/gorm"

	"github.com/yourorg/guanjin-next/internal/crypto"
	"github.com/yourorg/guanjin-next/internal/store"
	"github.com/yourorg/guanjin-next/internal/store/model"
)

// auditChainLockKey 是全局审计链的咨询锁键（固定常量，全副本一致）。
const auditChainLockKey int64 = 0x5349_414D_AU01 // 任意固定值；改名需评审

// Append 在单个事务内完成「取锁 → 读链尾 → 计算标签 → 追加」，保证链严格有序。
func (r *ChainRepo) Append(ctx context.Context, e *model.AuditLog) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1) 事务级咨询锁：全局串行化链尾。多副本安全，事务提交/回滚即释放。
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", auditChainLockKey).Error; err != nil {
			return err
		}
		// 2) 读链尾 integrity_tag（追加型表，故取最新一条即链尾）
		var prev []byte
		row := tx.Raw(
			"SELECT integrity_tag FROM audit_logs ORDER BY occurred_at DESC, id DESC LIMIT 1",
		).Row()
		if err := row.Scan(&prev); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// 3) 计算本条标签：HMAC-SM3(masterKey, prev_tag || 规范化记录内容)
		tag, err := crypto.AuditTag(r.masterKey, prev, e.Canonical())
		if err != nil {
			return err
		}
		e.PrevTag = prev // 首条为 NULL
		e.IntegrityTag = tag
		// 4) 追加（触发器 forbid_mutation 已保证只增不改不删）
		return store.MapPgError(tx.Create(e).Error)
	})
}
```

**可选演进（吞吐足够前不做）**：把单条全局链拆成**按维度分链**（如按 client 各一条链），每条链各持一把咨询锁，从而并行——代价是「跨链的删除」不再被单链检出，需额外的锚定机制。**V1 保持单链**，与数据模型 §6 #9 的默认一致。

**为什么用咨询锁而不是「专用单写者」**：单写者需要一个独立进程 + 队列，运维多一个组件、且引入「队列积压」这一新故障面；而咨询锁是 PG 原生、零额外组件、事务级自动释放。当审计写入真的成为瓶颈时，再拆「单写者」——那时拆分维度（分链）才有数据支撑。

---

## 6. Schema 与迁移

### 6.1 工具与命令

- 工具：`golang-migrate` CLI（生产/CI 用二进制），代码内可用 `migrate/v4` 库（仅测试）。
- 目录：`migrations/`（仓库根）。
- 迁移**不在应用启动时自动执行**（避免多副本同时迁移 + 长锁）；由发布流程的**一次性 Job / init container** 执行。

```bash
# 本地 / CI：升级到最新
migrate -path ./migrations -database "$GUANJIN_DATABASE_URL" -lock-timeout 30s up

# 查看当前版本
migrate -path ./migrations -database "$GUANJIN_DATABASE_URL" version

# 回滚一步（仅开发/应急；生产回滚需评估数据）
migrate -path ./migrations -database "$GUANJIN_DATABASE_URL" down 1

# 强制置版本（迁移失败卡住时，谨慎使用）
migrate -path ./migrations -database "$GUANJIN_DATABASE_URL" force 000014
```

### 6.2 命名规范

| 项 | 规范 | 示例 |
|---|---|---|
| 文件名 | `{6位零填充序号}_{snake_case描述}.{up\|down}.sql` | `000003_create_identifiers.up.sql` |
| 序号 | 全局单调递增，**不复用**；删除的迁移留空号，不重排 | — |
| 顺序 | 按数据模型 §1.2 依赖顺序：`users → clients → identifiers → authenticators → sessions → challenges → 其余` | 见 §3.1 |
| 内容 | 一份迁移做一件事；DDL 与数据回填分开；同一迁移内的 DDL 与 DML 不混 | — |
| 幂等 | 使用 `CREATE TABLE IF NOT EXISTS` / `DROP … IF EXISTS`，但**不依赖**它做幂等（migrate 自己记录 `schema_migrations`） | — |
| down | 必须可执行（`DROP TABLE` 需注意被引用顺序，用 `DROP … CASCADE` 需评审） | — |

**共享函数**放在 `000000_common_functions`，供后续迁移复用：

```sql
-- migrations/000000_common_functions.up.sql
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION forbid_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'append-only relation "%": % is not allowed', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'restrict_violation';
END $$;
```

```sql
-- migrations/000001_create_users.up.sql
CREATE TABLE users (
    id              uuid        PRIMARY KEY,          -- ★ 应用层生成 UUID v7（PG<18 无 uuidv7()）
    username        text        NOT NULL,
    display_name    text,
    status          text        NOT NULL DEFAULT 'active'
                                CHECK (status IN ('active','disabled')),
    is_admin        boolean     NOT NULL DEFAULT false,
    disabled_at     timestamptz,
    disabled_reason text,
    effective_tier  text        CHECK (effective_tier IN ('L1','L2','L3')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_username_len CHECK (char_length(username) BETWEEN 3 AND 64),
    CONSTRAINT users_disabled_consistency
        CHECK ((status = 'disabled') = (disabled_at IS NOT NULL))
);
CREATE UNIQUE INDEX users_username_lower_uidx ON users (lower(username));
CREATE INDEX users_status_idx ON users (status);
CREATE INDEX users_admin_partial_idx ON users (is_admin) WHERE is_admin;

CREATE TRIGGER users_set_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
```

> 迁移内容**逐字取自** `GuanJin-数据模型规格.md` §3，本文件不重复全部 14 张表——**唯一权威是数据模型规格**，本文件只规定「怎么落成迁移」。

### 6.3 GORM 模型与迁移的「不漂移」保障

漂移 = GORM 模型里有某个字段，但迁移里没有对应列（或反之），运行时才炸。三重保障：

**① 显式声明**：每个模型显式 `TableName()`，每个字段显式 `gorm:"column:..."` 标签，与迁移列名逐字一致。**不使用**隐式命名推断。

**② CI 漂移检查测试**（起真实 PG16，跑完 `migrate up` 后校验）：

```go
// internal/store/model/schema_drift_test.go
package model_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/yourorg/guanjin-next/internal/store/model"
)

func TestSchemaHasNoDrift(t *testing.T) {
	db := newTestDBWithMigrations(t) // testcontainers: postgres:16 + migrate up

	for _, m := range []any{
		&model.User{}, &model.Identifier{}, &model.Authenticator{}, &model.Challenge{},
		&model.Session{}, &model.AuthEvent{}, &model.AuditLog{}, &model.Client{},
		&model.LoginPolicy{}, &model.SigningKey{}, &model.RuntimeConfig{},
		&model.RoleBinding{}, &model.PolicySnapshot{}, &model.UserVisitedClient{},
	} {
		stmt := &gorm.Statement{DB: db}
		require.NoError(t, stmt.Parse(m))
		table := stmt.Schema.Table

		// 迁移里的真实列集合
		var dbCols []string
		require.NoError(t, db.Raw(`
			SELECT column_name FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = ?`, table).Scan(&dbCols).Error)
		colSet := map[string]bool{}
		for _, c := range dbCols {
			colSet[c] = true
		}

		// GORM 的每个字段都必须能在迁移里找到
		for _, f := range stmt.Schema.Fields {
			require.Truef(t, colSet[f.DBName],
				"GORM 模型 %s 的字段 %s（列 %s）在迁移中不存在 —— schema 漂移",
				table, f.Name, f.DBName)
		}
	}
}
```

**③ 反向断言（可选）**：把迁移里有、GORM 模型里没有的列打印出来人工确认（用于发现「迁移加了列但忘了加模型」）。方向是**迁移为权威**，模型缺失只需补模型，不需要改迁移。

**为什么这么定**：`AutoMigrate` 之所以诱人，是因为它「自动对齐」——代价是丢了约束、且对齐方向是「以模型为准」。本方案把对齐方向反过来（**以迁移为准**）+ 用一个可执行的测试卡住漂移，既保留了迁移的全部约束，又拿回了自动检查。

---

## 7. 配置管理

### 7.1 边界：启动配置（环境变量） vs 运行时配置（DB）

| 类别 | 载体 | 是否可热改 | 内容 |
|---|---|---|---|
| **启动配置** | 环境变量（前缀 `GUANJIN_`） | ❌ 改需重启 | DB/Redis 地址、端口、主密钥、进程级参数 |
| **运行时配置** | `runtime_config` 表 | ✅ 管理面 PATCH | 限流默认值、短信预算阈值、审计留存天数、日志外送开关、因子启用开关 |
| **业务配置** | `login_policies` / `clients` 等表 | ✅ 管理面 PATCH | 档位门槛项、禁用因子、回调地址 |

**为什么这么定**：DB 连接串、主密钥、端口是「进程启动即确定」的，做成热改会让连接池/密钥状态不一致；而限流阈值、因子开关需要在故障时**不改代码快速调整**，必须落 DB 可热改。**两者混在一起（全塞 DB 或全塞 env）都会在某一侧出问题。**

### 7.2 环境变量清单（启动配置）

| 变量 | 必填 | 默认 | 说明 |
|---|:--:|---|---|
| `GUANJIN_ENV` | 否 | `prod` | `dev`/`prod`；影响日志与错误详情 |
| `GUANJIN_LOG_LEVEL` | 否 | `info` | `debug`/`info`/`warn`/`error` |
| `GUANJIN_LOG_FORMAT` | 否 | `json` | 生产固定 `json` |
| `GUANJIN_HTTP_ADDR` | 否 | `:8080` | 控制面（认证/管理/Agent/静态资源）监听地址 |
| `GUANJIN_GATEWAY_ADDR` | 否 | `:8081` | 数据面（反代）监听地址 |
| `GUANJIN_PUBLIC_BASE_URL` | **是** | 无 | 对外基址（OIDC issuer、回调拼接）；**不内置默认值** |
| `GUANJIN_ISSUER` | 否 | = `GUANJIN_PUBLIC_BASE_URL` | OIDC `iss` |
| `GUANJIN_DATABASE_URL` | **是** | 无 | `postgres://user:pass@host:5432/guanjin?sslmode=require` |
| `GUANJIN_DB_MAX_OPEN_CONNS` | 否 | `50` | 连接池上限 |
| `GUANJIN_DB_MAX_IDLE_CONNS` | 否 | `10` | 空闲连接 |
| `GUANJIN_DB_CONN_MAX_LIFETIME` | 否 | `30m` | 连接生命周期 |
| `GUANJIN_REDIS_URL` | **是** | 无 | `redis://host:6379/0` |
| `GUANJIN_REDIS_TIMEOUT` | 否 | `2s` | 单命令超时 |
| `GUANJIN_MASTER_KEY` | **是** | 无 | **base64 编码、≥32 字节**的主密钥；不落库、不入镜像 |
| `GUANJIN_BOOTSTRAP_ADMIN_USERNAME` | 否 | 无 | 首次启动引导平台管理员的用户名（仅首次生效） |
| `GUANJIN_BOOTSTRAP_ADMIN_PASSWORD` | 否 | 无 | 首次启动引导口令；**首登强制改密** |
| `GUANJIN_GATEWAY_UPSTREAM_TIMEOUT` | 否 | `5s` | 数据面转发上游超时 |
| `GUANJIN_GATEWAY_DIAL_TIMEOUT` | 否 | `2s` | 上游连接超时 |
| `GUANJIN_METRICS_ADDR` | 否 | `127.0.0.1:9090` | Prometheus 指标（**仅内网/本机**） |
| `GUANJIN_OTEL_ENDPOINT` | 否 | 空 | 设置则启用 OTel 导出（否则用自实现 trace_id） |

**校验必须 fail-fast**（`internal/config/env.go`）：

```go
// internal/config/env.go
package config

import (
	"encoding/base64"
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

type Env struct {
	Env        string `env:"GUANJIN_ENV" envDefault:"prod"`
	LogLevel   string `env:"GUANJIN_LOG_LEVEL" envDefault:"info"`
	LogFormat  string `env:"GUANJIN_LOG_FORMAT" envDefault:"json"`

	HTTPAddr    string `env:"GUANJIN_HTTP_ADDR" envDefault:":8080"`
	GatewayAddr string `env:"GUANJIN_GATEWAY_ADDR" envDefault:":8081"`

	PublicBaseURL string `env:"GUANJIN_PUBLIC_BASE_URL,required"`
	Issuer        string `env:"GUANJIN_ISSUER"`

	DatabaseURL       string        `env:"GUANJIN_DATABASE_URL,required"`
	DBMaxOpenConns    int           `env:"GUANJIN_DB_MAX_OPEN_CONNS" envDefault:"50"`
	DBMaxIdleConns    int           `env:"GUANJIN_DB_MAX_IDLE_CONNS" envDefault:"10"`
	DBConnMaxLifetime time.Duration `env:"GUANJIN_DB_CONN_MAX_LIFETIME" envDefault:"30m"`

	RedisURL     string        `env:"GUANJIN_REDIS_URL,required"`
	RedisTimeout time.Duration `env:"GUANJIN_REDIS_TIMEOUT" envDefault:"2s"`

	MasterKey string `env:"GUANJIN_MASTER_KEY,required"` // base64

	GatewayUpstreamTimeout time.Duration `env:"GUANJIN_GATEWAY_UPSTREAM_TIMEOUT" envDefault:"5s"`
	GatewayDialTimeout     time.Duration `env:"GUANJIN_GATEWAY_DIAL_TIMEOUT" envDefault:"2s"`
}

func Load() (Env, error) {
	var e Env
	if err := env.Parse(&e); err != nil {
		return e, err // 缺必填 → 直接失败
	}
	if e.Issuer == "" {
		e.Issuer = e.PublicBaseURL
	}
	// 主密钥必须可解码且足够长（SM4 键 16B / HMAC 键派生用）
	raw, err := base64.StdEncoding.DecodeString(e.MasterKey)
	if err != nil {
		return e, fmt.Errorf("GUANJIN_MASTER_KEY 不是合法 base64: %w", err)
	}
	if len(raw) < 32 {
		return e, fmt.Errorf("GUANJIN_MASTER_KEY 至少 32 字节（当前 %d）", len(raw))
	}
	return e, nil
}
```

### 7.3 主密钥注入方式

| 环境 | 注入方式 | 命令 / 说明 |
|---|---|---|
| 本地 | `.env`（不入库，`.gitignore`） | `echo "GUANJIN_MASTER_KEY=$(openssl rand -base64 32)" >> .env` |
| Docker Compose | `deploy/secrets/master_key.txt` + `secrets:` | compose 顶层 `secrets` 挂载到 `/run/secrets/master_key`，启动脚本读取为 env |
| Kubernetes | `Secret`（`stringData` 由外部注入，不写进 Git） | `kubectl create secret generic guanjin-master-key --from-literal=GUANJIN_MASTER_KEY=...` |
| Vault / KMS | 启动时取一次，写入进程 env | 推荐生产；支持轮换 |

**主密钥不落库的强制保证**：`runtime_config` 表有 CHECK `runtime_config_no_master_key`，任何试图写入 `master_key` 类键名都会失败（数据模型 §3.11）。

**子密钥派生**（不要把主密钥直接用在不同算法上）：

```go
// internal/crypto/masterkey.go
package crypto

// Derive 通过 HKDF-SM3 从主密钥派生出用途隔离的子密钥。
func Derive(masterKey []byte, purpose string, length int) []byte {
	// salt/info 使用固定 domain separator（如 "guanjin/sm4/field"、"guanjin/hmac/audit"）
	// 实现：HKDF(SM3, masterKey, salt=purpose, info=purpose)[:length]
	// 结果：SM4 键 16B、审计 HMAC 键 32B、盲索引 HMAC 键 32B……
}
```

**为什么用 HKDF 派生子密钥**：主密钥同时服务 SM4（敏感字段）、HMAC-SM3（审计链、盲索引）等多种用途；直接复用同一把密钥会违反「一密钥一用途」，一旦某一用途的密钥泄露就波及全部。派生让每类用途有独立密钥，且**轮换主密钥时只需重派生**（旧数据处理走独立的重加密 Job）。

**轮换**：`scripts/rotate-master-key.sh` —— 读旧键解密 → 用新键重新加密所有 SM4 字段与 `private_key_enc`，写回；审计链不动（历史标签以旧键验证，需保留旧键的验证副本；保留期限见索引 §5-4）。

---

## 8. 构建、部署与运行

### 8.1 多阶段 Dockerfile 要点

```dockerfile
# syntax=docker/dockerfile:1.7

# ===== Stage 1: 前端构建（Vue 3 + Vite）=====
FROM node:22-alpine AS web-builder
WORKDIR /build
COPY web/package.json web/package-lock.json ./
RUN npm config set registry https://registry.npmmirror.com && npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build            # 产物 /build/dist

# ===== Stage 2: 后端构建（Go，CGO 关闭）=====
FROM golang:1.25-alpine AS go-builder
WORKDIR /build
RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories && \
    apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN GOPROXY=https://mirrors.aliyun.com/goproxy/,direct go mod download
COPY . .
COPY --from=web-builder /build/dist ./web/dist     # 前端产物嵌入
# 两个二进制：控制面 + 数据面
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/guanjind  ./cmd/guanjind && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/guanjingw ./cmd/guanjingw

# ===== Stage 3: 运行时（最小镜像）=====
FROM alpine:3.20
RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.aliyun.com|g' /etc/apk/repositories && \
    apk add --no-cache ca-certificates tzdata && \
    addgroup -S app && adduser -S -G app app
WORKDIR /app
COPY --from=go-builder /out/guanjind  /app/guanjind
COPY --from=go-builder /out/guanjingw /app/guanjingw
COPY migrations /app/migrations
RUN chown -R app:app /app
USER app
EXPOSE 8080 8081
ENTRYPOINT ["/app/guanjind"]        # 数据面用 guanjingw 覆盖 entrypoint
```

**要点**：① 前端在容器内构建，产物嵌入 Go 二进制（单制品）或交给 nginx；② `CGO_ENABLED=0` 静态构建，`trimpath + -s -w` 减小体积；③ 只用 `migrations/` 与二进制，**不带源码**；④ 非 root 运行；⑤ **绝不在镜像内烘焙主密钥**。

### 8.2 docker-compose 拓扑（含前置网关）

```yaml
# deploy/docker-compose.yml
services:
  # ── 前置成熟组件：TLS 终结 / 粗路由 / WAF / 通用限流 ──
  gateway:
    image: nginx:1.27-alpine
    volumes:
      - ./nginx/nginx.conf:/etc/nginx/nginx.conf:ro
      - ./certs:/etc/nginx/certs:ro
    ports: ["443:443", "80:80"]
    depends_on: [guanjind, guanjingw]
    restart: unless-stopped

  # ── 控制面（认证 / 管理 / Agent / 静态资源），可多副本 ──
  guanjind:
    build: { context: .., dockerfile: Dockerfile }
    command: ["/app/guanjind"]
    environment:
      GUANJIN_ENV: prod
      GUANJIN_HTTP_ADDR: ":8080"
      GUANJIN_PUBLIC_BASE_URL: "https://id.example.com"
      GUANJIN_DATABASE_URL: "postgres://guanjin:***@db:5432/guanjin?sslmode=require"
      GUANJIN_REDIS_URL: "redis://redis:6379/0"
      GUANJIN_MASTER_KEY_FILE: /run/secrets/master_key   # 见 §7.3
    secrets: [master_key]
    depends_on:
      db:    { condition: service_healthy }
      redis: { condition: service_healthy }
    deploy: { replicas: 2 }
    restart: unless-stopped

  # ── 数据面（反代），独立扩容 ──
  guanjingw:
    build: { context: .., dockerfile: Dockerfile }
    command: ["/app/guanjingw"]
    environment:
      GUANJIN_GATEWAY_ADDR: ":8081"
      GUANJIN_DATABASE_URL: "postgres://guanjin:***@db:5432/guanjin?sslmode=require"
      GUANJIN_REDIS_URL: "redis://redis:6379/0"
      GUANJIN_MASTER_KEY_FILE: /run/secrets/master_key
    secrets: [master_key]
    deploy: { replicas: 3 }      # 数据面承受全量 QPS，副本多于控制面
    restart: unless-stopped

  db:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: guanjin
      POSTGRES_PASSWORD_FILE: /run/secrets/db_password
      POSTGRES_DB: guanjin
    volumes: [ "pgdata:/var/lib/postgresql/data" ]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U guanjin"]
      interval: 5s
      timeout: 3s
      retries: 5
    secrets: [db_password]

  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 5

  # ── 迁移：一次性 Job，应用不自动迁移 ──
  migrate:
    image: migrate/migrate:v4.19.1
    volumes: [ "../migrations:/migrations:ro" ]
    command: ["-path=/migrations", "-database=postgres://guanjin:***@db:5432/guanjin?sslmode=require", "up"]
    depends_on:
      db: { condition: service_healthy }
    restart: "no"

volumes:
  pgdata:

secrets:
  master_key:  { file: ./secrets/master_key.txt }
  db_password: { file: ./secrets/db_password.txt }
```

**前置网关做什么（`deploy/nginx/nginx.conf` 要点）**：

```nginx
# TLS 终结 + 粗路由 + 回源鉴权（mTLS）+ 通用限流 + 安全头
server {
    listen 443 ssl;
    server_name id.example.com;                 # 认证域 → 控制面
    ssl_certificate     /etc/nginx/certs/id.crt;
    ssl_certificate_key /etc/nginx/certs/id.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    # 国密 TLS：需 nginx + 国密模块（如 Tongsuo）；见索引 §5-2

    limit_req zone=general burst=50 nodelay;     # 通用（无状态）限流
    client_max_body_size 1m;

    location / { proxy_pass http://guanjind:8080; }
}

server {
    listen 443 ssl;
    server_name app.example.com;                # 业务域 → 数据面（反代到业务系统）
    ssl_certificate     /etc/nginx/certs/app.crt;
    ssl_certificate_key /etc/nginx/certs/app.key;

    location / { proxy_pass http://guanjingw:8081; }   # 数据面再按 route → client → 上游
}
```

**为什么前置网关用 nginx 而非自研**：TLS、WAF、DDoS、通用限流是成熟组件的强项；关津自研这些既慢又不稳（数据面设计 §1.2）。**注意区分**：无状态通用限流归 nginx，**账号级失败计数与短信日配额归 关津**（需要跨业务系统状态）。

### 8.3 多副本与无状态化约束

| 状态 | 存放位置 | 副本间共享方式 | 说明 |
|---|---|---|---|
| 会话（`sessions`） | **PostgreSQL** | 直接查库（可选 Redis 热键） | 权威在库；数据面每请求判撤销 |
| 挑战（`challenges`） | **PostgreSQL** | 直接查库 | 权威在库（已裁定，接口清单 §5.9） |
| 用户/网关密钥（`signing_keys`） | **PostgreSQL**（`private_key_enc` 受主密钥保护） | 各副本同主密钥解密 | 主密钥必须全副本一致 |
| `jti` 防重放 | **Redis** | 共享（`replay-store=redis`） | 多副本必须 Redis，不能 `memory` |
| 限流计数 / 失败计数 / 短信配额 | **Redis** | 共享 | 权威计数；DB 列仅持久化镜像 |
| JWKS 公钥缓存 | 各副本本地内存 | 可容忍短暂不一致（公钥，10min TTL） | 抖动用旧缓存验签即可 |
| 审计哈希链顺序 | **PostgreSQL 咨询锁** | 全局串行 | 见 §5.6 |
| 无 | **本地文件系统** | — | **禁止写本地文件**（除日志外）；无状态化的前提 |

**硬约束**：
1. **禁止本地磁盘状态**：挑战/会话/密钥一律不落本地文件（现有旧仓库曾把自动生成的密钥写 `runtime_config`——**本设计的 `runtime_config` CHECK 禁止主密钥入库，必须 env 注入**）。
2. **`jti` 存储多副本必须 Redis**（`guanjin.authz.replay-store=redis`），否则同样的令牌在不同副本上被重复使用。
3. **不依赖会话粘滞**（session stickiness）：请求可落任意副本。
4. **Redis 不可用 = fail-closed**：`readyz` 反映依赖状态（`{checks:{db,redis,keys}}`），Redis 挂则整体拒绝服务，**不降级放行**。
5. **滚动发布安全**：新副本启动必须等 `readyz` 通过（DB/Redis/密钥/迁移版本一致）才接流；密钥轮换遵循 `pending → active → retiring → retired`（数据模型 §3.10）。

---

## 9. 代码质量门禁

### 9.1 Go lint（`.golangci.yml`）

```yaml
run:
  timeout: 5m
linters:
  enable:
    - errcheck        # 未处理的 error
    - govet
    - staticcheck
    - gosimple
    - ineffassign
    - unused
    - revive          # 风格
    - gocritic
    - gosec           # 安全（含硬编码密钥、弱随机）
    - sqlclosecheck   # rows/Stmt 必须 Close
    - bodyclose       # HTTP body 必须 Close
    - contextcheck    # context 传递
    - exhaustive      # switch 枚举必须穷尽（错误码/档位/结果）
    - misspell
    - gofmt
    - goimports
    - gci             # import 分组
linters-settings:
  gocritic:
    enabled-tags: [diagnostic, performance, style]
  revive:
    rules:
      - name: exported
        disabled: true
```

**自定义「反模式」门禁**（GORM 五条 + 因子守护规则）：

```bash
# scripts/check-no-automigrate.sh（见 §5.1）：
#   1) 禁止 gorm.DeletedAt
#   2) 禁止 db.AutoMigrate(
# 追加：
#   3) 禁止按因子类型写分支（守护规则 1）
grep -rn 'Type() == "sms"\|Type() == "email"\|factor == "password"' --include="*.go" internal/ && exit 1
#   4) 禁止在提交前忘记 mapPgError（启发式：仓储文件直接返回 .Error）
#      由 code review 清单兜底（见 §9.4）
```

**为什么把「禁止 type 分支」做成 CI 而非仅评审**：因子模型的守护规则 1 是「无限扩充」的命门，靠人工评审会漏；用机械检查把它钉死，是让架构约束可执行。

### 9.2 前端 lint

- `eslint`：`vue3-recommended` + `@typescript-eslint` + `vue/multi-word-component-names` 关闭策略按团队定。
- `prettier`：统一格式，`--check` 进 CI。
- `vue-tsc --noEmit`：类型检查进 CI（现有 `build` 脚本已含 `vue-tsc -b`）。

### 9.3 测试要求（哪些模块**必须**单测）

| 模块 | 必须测什么 | 覆盖率下限 |
|---|---|---|
| `crypto` | SM2 签/验、SM3/HMAC、SM4 加解密、HKDF 派生、PBKDF2-SM3 一致性向量 | 90% |
| `factor` | 能力矩阵、Descriptor 守护（新增因子零核心改动）、基类复用 | 85% |
| `credential` | 口令派生与历史校验、TOTP 时间窗、恢复码一次性 | 85% |
| `identifier` | 规范化（E.164/小写）、盲索引用例、I1/I2/I6 不变式 | 85% |
| `flow` | Flow Descriptor 计算、阶梯认证状态机、ACR 判定 | 85% |
| `authz` | 门槛方向矩阵（4 类方向）、绑定副作用（撤销会话） | 85% |
| `audit` | **哈希链完整性**（删中间一条能检出）、链尾串行化并发测试 | 90% |
| `risk` | 失败计数/锁定/限流/配额 | 80% |
| `gateway` | 验签失败/撤销/档位不足/header 剥离/路由 | 80% |
| `store` | 错误码映射（23505 各类约束）、集成测试（testcontainers） | 75% |
| `protocol`/`admin`/`agent` | handler + 三员鉴权矩阵 | 70% |

**必测的集成场景**（testcontainers：`postgres:16` + `redis:7`）：
- `migrate up` → 全部迁移可执行；
- 每个唯一约束触发 → 映射到正确业务错误码；
- 审计哈希链在并发写入下不断链。

### 9.4 CI 步骤（示例流水线）

```yaml
steps:
  - go fmt check（gofmt -l 必须为空）
  - golangci-lint run ./...
  - scripts/check-no-automigrate.sh          # GORM 五条 + 因子守护
  - go test ./... -race -coverprofile=cover.out
  - coverage-gate.sh                         # 按 §9.3 模块下限
  - integration test（testcontainers: PG16 + Redis7，含迁移与漂移检查）
  - govulncheck ./...
  - web: npm ci && npx vue-tsc --noEmit && npx eslint . && npx vitest run && npm run build
  - sdk: mvn -f sdk/pom.xml verify           # guanjin-core 单测 + 2.x 适配编译
  - docker build（多阶段）
  - trivy image scan（HIGH/CRITICAL 阻断）
  - （可选）国密合规测试套（SM2/SM3/SM4 标准向量）
```

### 9.5 代码评审清单（人工兜底）

- [ ] 新增/修改写操作是否过 `mapPgError`？
- [ ] 仓储新增查询是否是「简单链式 / 复杂裸 SQL」的恰当选择？
- [ ] 新增唯一约束是否同步补了错误码映射与测试？
- [ ] 是否引入了本地磁盘/内存状态？（应无）
- [ ] 新增因子是否零改核心层（附 diff 举证）？
- [ ] 日志是否可能记录令牌/凭据/标识符明文/主密钥？

---

## 10. 日志 / 可观测性 / 错误处理约定

### 10.1 结构化日志字段（统一）

所有日志为 **JSON**（zap），字段名固定：

| 字段 | 类型 | 说明 |
|---|---|---|
| `ts` | RFC3339 | UTC 时间 |
| `level` | string | `info`/`warn`/`error` |
| `service` | string | `guanjind` / `guanjingw` |
| `env` | string | `dev`/`prod` |
| `trace_id` | string | **贯穿全链路**（见 §10.2） |
| `span` | string | 逻辑步骤名（如 `verify.password`） |
| `method`/`path`/`status`/`latency_ms` | — | 访问日志 |
| `client_id` | string | 业务系统（可空） |
| `user_id` | uuid | 用户 id（可空） |
| `session_id` | uuid | 会话 id（可空） |
| `event_type` | string | 认证事件类型（可空） |
| `err_code` | string | 业务错误码（可空） |
| `msg` | string | 人类可读 |

**明令禁止入日志**：任何令牌原文（access/refresh/id_token/`X-GuanJin-Assertion`）、口令、一次性码、`identifiers.value/value_enc` 明文、`credential_data` 内容、主密钥与子密钥。日志中出现的标识符一律用 `value_masked`。

**为什么把「日志不落令牌」写成硬约定**：接口清单 §5.2 已规定「任何日志不得记录完整令牌」——但真正的泄漏点不是显式打印，而是**中间件把整个 header/body 打进 debug 日志**。约定字段名 + 用 zap 的 `zap.String` 显式选字段（不用 `%+v` 打整个结构体），才能从机制上避免。

### 10.2 trace_id 贯穿

- 入口（nginx 可透传 `traceparent`；关津中间件）生成：优先用 W3C `traceparent`，缺失则生成 UUID v7 作为 `trace_id`。
- 通过 `context.Context` 在进程内传递；出站调用（转发到业务系统、Agent 调用）带 `traceparent`。
- 落库：`auth_events.trace_id`、`audit_logs.trace_id` 均写入，实现「日志 ↔ 审计」互查。
- 错误响应体带回 `trace_id`（接口清单 §5.4 统一响应体）。

```go
// internal/httpx/middleware.go（要点）
func TraceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid := parseTraceparent(r.Header.Get("traceparent"))
		if tid == "" {
			tid, _ = uuid.NewV7().String(), nil
		}
		ctx := observability.WithTraceID(r.Context(), tid)
		w.Header().Set("X-Trace-Id", tid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
```

### 10.3 错误码分层与统一响应

沿用接口清单 §5.4 的 **E1..E8** 分层。Go 侧用一个携带 Code 与 HTTP 状态的错误类型：

```go
// internal/httpx/error.go
package httpx

import "net/http"

type AppError struct {
	Code    string         // 如 "IDENTIFIER_ALREADY_BOUND"
	HTTP    int            // HTTP 状态
	Message string         // 面向人，绝不泄露账号是否存在
	Details map[string]any // 可选：retry_after / flow / remaining_attempts
}

func (e *AppError) Error() string { return e.Code }

// 从业务错误码解析 HTTP 状态（分层规则的落地）
func HTTPStatus(code string) int {
	switch code {
	case "INVALID_REQUEST", "MISSING_PARAMETER", "INVALID_PARAMETER",
		"INVALID_CLIENT", "FLOW_NOT_FOUND", "FLOW_EXPIRED":
		return http.StatusBadRequest
	case "INVALID_CREDENTIAL", "TOKEN_INVALID", "TOKEN_EXPIRED", "TOKEN_REVOKED",
		"CHALLENGE_EXPIRED", "ACCOUNT_LOCKED":
		return http.StatusUnauthorized
	case "ACR_INSUFFICIENT", "INSUFFICIENT_ROLE", "SESSION_REVOKED":
		return http.StatusForbidden
	case "IDENTIFIER_ALREADY_BOUND", "LAST_IDENTIFIER_CANNOT_UNBIND":
		return http.StatusConflict
	case "RATE_LIMITED", "BUDGET_EXCEEDED":
		return http.StatusTooManyRequests
	case "DEPENDENCY_UNAVAILABLE", "REDIS_UNAVAILABLE", "DB_UNAVAILABLE", "KEY_UNAVAILABLE":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
```

统一响应体（与接口清单 §5.4 一致）：

```json
{ "code": "IDENTIFIER_ALREADY_BOUND", "message": "该手机号已被绑定", "trace_id": "…", "details": {} }
```

- **分层 → HTTP**：E1=400/404、E2=401、E3=403、E4=404/409/422、E5=429、E6=500/503、E7=403/409/422、E8=401。
- **`ACR_INSUFFICIENT` 不直接失败**：`details.flow` 回带当前档位与待补步骤，引导 `/auth/challenge/{factor}`。
- **anti-enumeration**：`/auth/recover/*`、注册的「账号是否存在」绝不通过 message/状态码/耗时泄露（接口清单 §3.6.1）。

### 10.4 指标（Prometheus，`/metrics` 仅内网）

| 指标 | 类型 | 用途 |
|---|---|---|
| `guanjin_http_requests_total{path,method,status}` | counter | 流量 |
| `guanjin_http_request_duration_seconds{path}` | histogram | 延迟（数据面重点） |
| `guanjin_auth_events_total{event_type,result}` | counter | 认证成功率 |
| `guanjin_factor_verify_total{factor,result}` | counter | 因子维度 |
| `guanjin_ratelimit_rejected_total{scope}` | counter | 限流触发 |
| `guanjin_dependency_up{dep="db\|redis\|keys"}` | gauge | readyz 同源 |
| `guanjin_audit_chain_lag_seconds` | gauge | 链尾写入延迟（并发串行化观察点） |

**为什么单独盯 `audit_chain_lag_seconds`**：审计链是全局串行点（§5.6），它的写入延迟直接反映系统是否已到「需要分链」的临界；没有这个指标，瓶颈会在事故时才暴露。

---

---

*本文是 关津设计基线的一部分。GORM 五条工程约束、Spring Boot 2.7 EOL 缓解、审计哈希链写序三项为**强制约定**；文中标注 ⚠️ 的待定项，集中清单见 `GuanJin-设计基线索引.md` §4.2 / §5。*
