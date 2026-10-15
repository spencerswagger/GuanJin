# 关津 · SDK（Spring Boot Starter）完整能力契约

| 项目 | 内容 |
|---|---|
| 文档类型 | 设计规格（接入侧 SDK 契约，缺口 §6.1-3 的产出） |
| 编写日期 | 2026-10-11 |
| 解决的问题 | SDK 到底做什么、不做什么；`guanjin.*` 配置项全集；与平台的交互时序；与 Spring Security 及数据面两种形态的协作方式 |
| 上游基线 | `GuanJin-设计基线索引.md`（入口，§2 为裁决依据）、`GuanJin-权限模型设计.md`、`GuanJin-数据面设计.md`、`GuanJin-预设档位与配置点清单.html`；另参考 `关津-接入面契约与四方边界.md`（仅「配置归属表 / 禁止 `@RequireRole`」两节仍有效） |
| 形态约定 | 产物是 **Spring Boot Starter**（Java / Spring 服务，对应接入面契约 §1.5 的 L1 层） |
| 硬性裁决 | 与基线冲突处以 `GuanJin-设计基线索引.md` §2 为准；凡属冲突或基线未定者，本文一律标注 **⚠️ 待定**，不擅自定案 |

> 本文只定义 **接入侧 SDK**。平台侧（认证、数据面、Agnet 面服务端）不在本文范围。
> 表/概念名一律使用基线给定的名字：`users` / `identifiers` / `authenticators` / `challenges` / `sessions` / `clients` / `role_bindings` / `policy_snapshots` / `user_visited_clients`。

---

## 1. 一页结论：SDK 的职责边界

> **一句话**：SDK 是业务系统里的**验票口 + 本地判官**。它验平台签发的内部令牌、读出身份、在**本地**完成授权判定；它**不签发**任何凭证、**不定义**授权策略、**不主持**登录流程。

### 1.1 SDK 做什么（7 件，全部围绕"验证与判定"）

| # | 职责 | 依据 |
|---|---|---|
| 1 | **验签内部令牌**：校验 `X-GuanJin-Assertion`（SM2 签名，公钥来自 JWKS，`kid` 区分） | 数据面设计 §4.6 / §4.7 |
| 2 | **读身份**：把 claim（`sub`/`uname`/`roles[]`/`acr`/`amr[]`/`cid`）解析为请求上下文，供业务与 `Authz.*` 使用 | 数据面设计 §4.6；产品核心定义 §5.2 |
| 3 | **剥离 header**：在转发下游之前移除 `X-GuanJin-Assertion`，并掐断客户端伪造头 | 数据面设计 §4.6 第 5 步（"⚠️"项） |
| 4 | **重放检测**：用 `jti` + `exp` 在防重放窗口内判重（令牌 60s 有效期仅为防重放窗口） | 数据面设计 §4.6 |
| 5 | **本地授权判定**：一个 `model.conf` 三层逐层生效（RBAC / 行级 ABAC / 密级 BLP），提供 `AuthzModel.builder()`、`@RequirePermission`、`Authz.checkRow()`、`Authz.checkOwner()`、`Authz.checkLabel()`、`Authz.scope()` | 权限模型设计 §2、§3.4 |
| 6 | **策略上报与（可选）拉取**：启动时 + 变更时 `POST /api/v1/agent/report`；按需 `GET /api/v1/agent/policies`；均以每业务系统独立 secret 的 HMAC 签名鉴权 | 权限模型设计 §2.3；本文 §6 |
| 7 | **公开接口声明 + 与 Spring Security 集成**：`@PublicApi` + `guanjin.public-paths`；把身份注入 `SecurityContext`，让业务方原有 `@PreAuthorize` 继续可用 | 接入面契约 §1.3（仍有效部分） |

### 1.2 SDK 明确不做什么（越界清单，均对应基线的"生死线"）

| 不做 | 理由 | 依据 |
|---|---|---|
| **不定义授权策略** | 权限点、角色→权限映射是业务语义，只在业务代码里声明；SDK 只**执行**已声明的判定 | 权限模型设计 §0 ①、§6 |
| **不做跨业务系统的统一角色模型** | 100 个系统角色体系各异，强行统一 = "授权复杂度无限"陷阱 | 权限模型设计 §3.4、§6 |
| **不做登录流程** | 登录页、因子编排、会话、找回，全部属平台；SDK 只"验票" | 产品核心定义 §1.5；接入面契约 §1.2 |
| **不提供 `@RequireRole` 之类的授权注解** | 一旦 SDK 允许写角色鉴权，产品就侵入授权领域 | 接入面契约 §1.3 ③、§6 |
| **不签发令牌 / 不管会话与撤销** | 内部令牌由数据面逐请求现签；撤销由数据面负责（承载数据面后实时生效） | 数据面设计 §4.6；预设档位清单「撤销传播机制」 |
| **不自动登记回调地址** | 回调地址白名单由管理员在平台配置 | 本文 §3「固定行为」；预设档位清单 |
| **不做文件级访问控制** | 应用层模型管不到 OS 文件，交主机 / OS 加固层 | 权限模型设计 §3.3 |
| **不硬编码平台地址** | 平台地址必须来自配置 | 产品核心定义 §5.3 规则 1；接入面契约 §2.1 |

### 1.3 边界一句话判据

> **任何"要不要放行某个请求"的最终决定，都在 SDK 本地完成；任何"这个请求是谁、可信到什么程度"的最终认定，都以验签通过的内部令牌为准；任何"角色该有哪些权限"的定义，都不在 SDK。**
>
> SDK 与平台的唯一耦合是**两个 Agent 面接口 + 一个 JWKS**（见 §6）。平台不在每个请求的路径上参与判定（与「承载数据面」形态下由**数据面**转发并存，见 §6）。

---

## 2. 能力清单

> 共 **22** 条。标注「必须 / 可选」；「必须」= 缺了会破坏默认安全或接入契约，不可关闭。

| # | 能力 | 必须/可选 | 说明 | 基线依据 |
|---|---|---|---|---|
| 1 | 验签内部令牌（SM2） | **必须** | 读 `X-GuanJin-Assertion`，用公钥验签；**不存在跳过验签的生产开关** | 数据面设计 §4.6/§4.7；预设档位清单「令牌必须验签」 |
| 2 | 用 `kid` 选择公钥 | **必须** | 内部令牌独立密钥对，与对外令牌分开，经 JWKS 用 `kid` 区分 | 数据面设计 §4.6 |
| 3 | JWKS 缓存 | **必须** | 启动拉取一次并缓存，避免每请求回源 | 系统架构说明 §2.1 |
| 4 | 未知 `kid` 强制刷新 JWKS | **必须** | 应对签名密钥轮换，避免"引入即全挂" | 接入面契约 §1.4 |
| 5 | 校验 `exp` / `iat`（含时钟偏移容忍） | **必须** | 默认 ±60s，可配 | 数据面设计 §4.6；接入面契约 §1.4 |
| 6 | 校验 `iss` / `aud` | **必须** | `iss=guanjin-gateway`；`aud` = 本业务系统 `client.id` | 数据面设计 §4.6 |
| 7 | 重放检测（`jti`） | **必须** | 窗口 ≥ 令牌 `exp`；`jti` 在窗口内唯一 | 数据面设计 §4.6 |
| 8 | 剥离 `X-GuanJin-Assertion` | **必须** | 转发下游前剥离；同时丢弃客户端伪造的同名头 | 数据面设计 §4.6 第 5 步 |
| 9 | 读身份、注入请求上下文 | **必须** | `sub`/`uname`/`roles[]`/`acr`/`amr[]`/`cid` | 数据面设计 §4.6 |
| 10 | 注入 Spring Security `SecurityContext` | **必须** | 让原有 `@PreAuthorize` 无缝可用 | 接入面契约 §1.3 ③（仍有效） |
| 11 | `GrantedAuthoritiesResolver` 扩展点 | 可选 | 业务方注册"身份属性→权限"映射（业务语义，归业务方） | 接入面契约 §1.3 ③ |
| 12 | 授权判定底座：三层 `model.conf` | **必须** | RBAC + 行级 ABAC + 密级 BLP 逐层生效，业务方零配置 | 权限模型设计 §3.4 |
| 13 | `AuthzModel.builder()` 声明式模型 | **必须** | 声明角色→权限、默认角色、客体密级 / 主体密级 | 权限模型设计 §2.1 |
| 14 | `@RequirePermission` 接口保护注解 | **必须** | 一行注解保护接口；**不提供** `@RequireRole` | 权限模型设计 §2.1 |
| 15 | `Authz.checkRow()` / `Authz.checkOwner()` | **必须** | 行级 / 数据实例级判定（`auditor` 角色自动豁免） | 权限模型设计 §2.1；本文 §4 |
| 16 | `Authz.checkLabel()` | **必须** | 密级判定（主体密级 ≥ 客体密级），`g)` 要求的落地 | 权限模型设计 §3.4 |
| 17 | `Authz.scope()` 列表过滤下推 | **必须** | 把行级条件下推到查询，避免"查出全量再过滤" | 权限模型设计 §2.1 |
| 18 | 默认角色 | **必须** | 未被绑任何角色时的兜底合成角色；**角色名与权限点由接入方自定义**（平台不预设），**权限尽量小**，不做"已登录即全部" | 权限模型设计 §4 |
| 19 | 公开接口声明（`@PublicApi` + `public-paths`） | **必须** | deny-by-default：内置白名单 + 显式声明的公开接口之外，全部需登录 | 预设档位清单「业务接口默认拒绝」；接入面契约 §1.3 ② |
| 20 | 策略上报（启动时 + 变更时） | **必须** | `POST /api/v1/agent/report`；业务方**不写上报代码** | 权限模型设计 §2.3 |
| 21 | 拉取"用户→角色"绑定（含缓存） | 可选 | `GET /api/v1/agent/policies`；采用「角色放进令牌」（索引 §4.2-24） | 权限模型设计 §5；本文 §8 |
| 22 | 启动自检 + 打印放行清单 | **必须** | 启动时打印全部放行路径，使"哪些接口对外"始终可见 | 接入面契约 §1.3 ② |

> **不做但易被误加的**：会话管理、令牌签发 / 刷新、撤销清单比对、回调地址自动登记、`user_visited_clients` 上报（属平台"生效配置"计算，SDK 不参与）。

---

## 3. 配置项清单

> 命名前缀 `guanjin.`。**平台地址必须来自配置（推荐环境变量），绝不硬编码**；**回调地址白名单由管理员在平台配置，SDK 不提供该配置项、也不自动登记**。

### 3.1 可配置项

| # | key | 类型 | 默认值 | 必填 | 说明 |
|---|---|---|---|:--:|---|
| 1 | `guanjin.enabled` | boolean | `true` | 否 | 总开关。仅供本地开发；生产环境置 `false` 应被视为误配并告警 |
| 2 | `guanjin.platform.base-url` | String(URL) | 无 | **是** | 平台地址。**必须来自配置 / 环境变量，绝不硬编码** |
| 3 | `guanjin.platform.connect-timeout` | Duration | `2s` | 否 | Agent 面连接超时 |
| 4 | `guanjin.platform.read-timeout` | Duration | `3s` | 否 | Agent 面读超时 |
| 5 | `guanjin.client.id` | String | 无 | **是** | 本业务系统 `client` id；同时作为 `aud` 期望值与 Agent 面 HMAC 标识（对应 `clients` 表） |
| 6 | `guanjin.client.hmac-secret` | String | 无 | **是** | Agent 面 HMAC 密钥。**每个业务系统一个独立 secret，不得共用**；环境变量注入，不落明文配置文件 |
| 8 | `guanjin.gateway.assertion-header` | String | `X-GuanJin-Assertion` | 否 | 内部令牌头名 |
| 9 | `guanjin.gateway.issuer` | String | `guanjin-gateway` | 否 | `iss` 期望值 |
| 10 | `guanjin.gateway.aud` | String | `${guanjin.client.id}` | 否 | `aud` 期望值 |
| 11 | `guanjin.gateway.clock-skew` | Duration | `60s` | 否 | 时钟偏移容忍（`exp`/`iat` 校验） |
| 12 | `guanjin.gateway.replay-window` | Duration | `60s` | 否 | `jti` 防重放窗口，**应 ≥ 令牌 `exp`** |
| 13 | `guanjin.gateway.jwks-uri` | String(URL) | `${guanjin.platform.base-url}/api/v1/jwks` | 否 | 内部令牌公钥地址。⚠️ 具体路径待澄清（见索引 §4.2 / §5） |
| 14 | `guanjin.gateway.jwks-cache-ttl` | Duration | `10m` | 否 | JWKS 缓存有效期 |
| 15 | `guanjin.gateway.jwks-unknown-kid-force-refresh` | boolean | `true` | 否 | 遇未知 `kid` 强制刷新一次 |
| 18 | `guanjin.authz.enabled` | boolean | `true` | 否 | 是否启用本地授权判定 |
| 19 | `guanjin.authz.default-role` | String | `user` | 否 | 默认（兜底）角色名；**权限尽量小**（见 §4 ②） |
| 20 | `guanjin.authz.model-conf` | Resource | `classpath:/guanjin/model.conf` | 否 | 内置三层模型；不建议修改 |
| 21 | `guanjin.authz.replay-store` | enum | `memory` | 否 | `jti` 存储：`memory` / `redis`。**多副本部署必须 `redis`** |
| 22 | `guanjin.agent.report.enabled` | boolean | `true` | 否 | 是否启用策略上报 |
| 23 | `guanjin.agent.report.on-startup` | boolean | `true` | 否 | 启动时上报策略快照 |
| 24 | `guanjin.agent.report.on-change` | boolean | `true` | 否 | 模型变更时重报 |
| 25 | `guanjin.agent.pull.enabled` | boolean | `true` | 否 | 是否拉取"用户→角色"绑定（能力 #21）；配合索引 §4.2-24 |
| 26 | `guanjin.agent.pull.interval` | Duration | `30s` | 否 | 拉取间隔（与数据面撤销传播窗口一致性，见 §6） |
| 27 | `guanjin.public-paths` | List\<String\> | `[]` | 否 | 公开接口声明（Ant 风格）。**放行即声明**，可评审、可 diff |
| 28 | `guanjin.public-paths-include-builtin` | boolean | `true` | 否 | 是否叠加内置白名单（健康检查、`/error`、静态资源、actuator、文档端点） |
| 29 | `guanjin.security.inject-security-context` | boolean | `true` | 否 | 是否把身份注入 `SecurityContext` |
| 30 | `guanjin.security.authorities-resolver` | String(beanName) | 无 | 否 | `GrantedAuthoritiesResolver` Bean 名（能力 #11） |
| 31 | `guanjin.security.role-prefix` | String | `ROLE_` | 否 | 角色转权限前缀，使 `hasRole('x')` 可用 |
| 32 | `guanjin.startup.print-public-paths` | boolean | `true` | 否 | 启动打印放行清单 |

### 3.2 固定行为（**不提供开关**，依据「能固定的不做开关」原则）

| 行为 | 为什么不做成开关 |
|---|---|
| **验签不可关** | 不存在跳过验签的生产开关（预设档位清单「令牌必须验签」） |
| **剥离 `X-GuanJin-Assertion` 不可关** | 不剥离即把内部令牌透传更深下游 / 落日志 |
| **fail-closed 不可关** | 依赖故障时降级放行 = 被攻击时自动解除防护（预设档位清单「依赖故障时的安全默认」） |
| **默认角色存在且权限最小** | 无默认角色 → 无角色用户被拒一切 → 系统不可用；但不得做成"已登录即全部" |
| **回调地址不登记** | 白名单由管理员在平台配置，SDK 不得自动登记 |
| **平台地址不内置默认值** | 硬编码平台地址违反"平台地址必须来自配置" |

---

## 4. 代码示例（可运行 Java 片段）

> 依赖：**Spring Boot 2.7.x（`javax.*` 命名空间）** + Spring Security 5.7+；国密 `SM2`/`SM3` 由 BouncyCastle（`bcprov-jdk18on`，JDK 1.8+）提供。
>
> ⚠️ **版本决策（已拍板）**：本项目 **只交付 Spring Boot 2.x（javax）产物**。3.x 用 `jakarta.*`，与 2.x 包路径不兼容，不能同一份产物通吃。为降低将来接入 3.x 的成本，SDK 拆为两个模块：`guanjin-core`（**零 Spring / servlet 依赖**，全部判定与验签逻辑）+ `guanjin-spring-boot2`（javax 适配层）。将来支持 3.x 只需新增一个适配模块。
>
> ⚠️ **另需知悉**：Spring Boot 2.7 的开源维护已于 **2023-11** 结束（OSS EOL），不再有免费安全补丁。用已 EOL 的框架承载安全组件，在等保 / 密评中可能被质询。缓解方式就是上面的适配层隔离；同时建议业务系统逐步向 2.7 → 3.x 升级。

### 4.1 ① `application.yml` 配置

```yaml
spring:
  application:
    name: order-service

guanjin:
  enabled: true
  platform:
    base-url: ${GUANJIN_PLATFORM_URL}          # 必须来自配置 / 环境变量，绝不硬编码
    connect-timeout: 2s
    read-timeout: 3s
  client:
    id: order-service                        # aud 期望值 + Agent 面 HMAC 标识
    hmac-secret: ${GUANJIN_HMAC_SECRET}        # 每个业务系统独立 secret，环境变量注入
  mode: gateway                              # gateway | direct
  gateway:
    assertion-header: X-GuanJin-Assertion
    issuer: guanjin-gateway
    aud: order-service
    clock-skew: 60s
    replay-window: 60s
    jwks-uri: ${guanjin.platform.base-url}/api/v1/jwks
    jwks-cache-ttl: 10m
    jwks-unknown-kid-force-refresh: true
  authz:
    enabled: true
    default-role: user                       # 默认角色：名字与权限点由接入方自定义（此处仅示例）
    replay-store: redis                      # 多副本部署必须 redis
  agent:
    report:
      enabled: true
      on-startup: true
      on-change: true
    pull:
      enabled: true
      interval: 30s
  public-paths:                              # 公开接口：放行即声明，可评审、可 diff
    - /health
    - /actuator/**
    - /error
    - /v3/api-docs/**
    - /swagger-ui/**
  security:
    inject-security-context: true
    role-prefix: ROLE_
  startup:
    print-public-paths: true
```

对应的配置绑定类：

```java
@ConfigurationProperties(prefix = "guanjin")
public class GuanjinProperties {
    private boolean enabled = true;
    private Platform platform = new Platform();
    private Client client = new Client();
    private Mode mode = Mode.GATEWAY;
    private Gateway gateway = new Gateway();
    private Direct direct = new Direct();
    private Authz authz = new Authz();
    private Agent agent = new Agent();
    private List<String> publicPaths = new ArrayList<>();
    private boolean publicPathsIncludeBuiltin = true;
    private Security security = new Security();
    private Startup startup = new Startup();

    public enum Mode { GATEWAY, DIRECT }

    // getters / setters 省略
    public static class Platform {
        private String baseUrl;                 // 必填，无默认值
        private Duration connectTimeout = Duration.ofSeconds(2);
        private Duration readTimeout = Duration.ofSeconds(3);
        // getters / setters 省略
    }
    public static class Client {
        private String id;                      // 必填
        private String hmacSecret;              // 必填，环境变量注入
        // getters / setters 省略
    }
    public static class Gateway {
        private String assertionHeader = "X-GuanJin-Assertion";
        private String issuer = "guanjin-gateway";
        private String aud;
        private Duration clockSkew = Duration.ofSeconds(60);
        private Duration replayWindow = Duration.ofSeconds(60);
        private String jwksUri;
        private Duration jwksCacheTtl = Duration.ofMinutes(10);
        private boolean jwksUnknownKidForceRefresh = true;
        // getters / setters 省略
    }
    public static class Direct {
        private String userTokenJwksUri;
        private String userTokenIssuer = "guanjin";
        // getters / setters 省略
    }
    public static class Authz {
        private boolean enabled = true;
        private String defaultRole = "user";
        private String modelConf = "classpath:/guanjin/model.conf";
        private String replayStore = "memory";  // memory | redis
        // getters / setters 省略
    }
    public static class Agent {
        private Report report = new Report();
        private Pull pull = new Pull();
        public static class Report {
            private boolean enabled = true;
            private boolean onStartup = true;
            private boolean onChange = true;
            // getters / setters 省略
        }
        public static class Pull {
            private boolean enabled = true;
            private Duration interval = Duration.ofSeconds(30);
            // getters / setters 省略
        }
        // getters / setters 省略
    }
    public static class Security {
        private boolean injectSecurityContext = true;
        private String authoritiesResolver;
        private String rolePrefix = "ROLE_";
        // getters / setters 省略
    }
    public static class Startup {
        private boolean printPublicPaths = true;
        // getters / setters 省略
    }
    // 其余 getters / setters 省略
}
```

### 4.2 ② 声明权限模型（Builder）

> 基线 API：`AuthzModel.builder().role(...).can(...)`、`.defaultRole(...).can(...)`、`.build()`。
> 密级 / 主体密级 / 客体密级，以及 `checkOwner` 属本文在基线之上的**补全**（基线只给了 `Authz.*` 方法名，未给全套 Builder 签名 —— 见索引 §4.2-25）。

```java
@Configuration
public class AuthzConfig {

    @Bean
    public AuthzModel authzModel() {
        return AuthzModel.builder()
                // ── 业务角色：角色 → 权限（业务语义，只有业务方知道）────────────────
                .role("order_admin")
                    .can("orders", "read", "write")
                    .can("order_items", "read", "write")
                .role("order_viewer")
                    .can("orders", "read")
                .role("auditor")                       // 名为 auditor 的角色自动获得跨行级豁免
                    .can("orders", "read")
                // ── 主体密级：由角色推导（平台不需要额外维护"用户密级表"）──────────
                .role("secret_reader").clearance(3)    // 秘密
                .role("internal_user").clearance(1)    // 内部
                // ── 默认角色：权限尽量小，只兜底"公开接口 + 我的数据"─────────────
                //    禁止做成"已登录即可访问全部"（否则违反等保 e)）
                .defaultRole("user")                   // 名字须与 guanjin.authz.default-role 一致
                    .can("profile", "read", "write")
                    .can("orders", "read")             // 只读；写权限必须由安全管理员绑定角色获得
                // ── 客体密级：资源打标（数据分级结果，声明式）───────────────────
                .dataLabel("orders", 1)                // orders 表 = 内部
                .dataLabel("order_items", 2)           // order_items 表 = 秘密
                // ── 可选：资源分组（降维，避免逐表授权）──────────────────────────
                .resourceGroup("order_domain", "orders", "order_items")
                .build();
    }
}
```

启动时 SDK 自动把上述声明汇总成**策略快照**并 `POST /api/v1/agent/report`（权限点目录 + 角色目录 + 接口→权限映射 + 默认角色 + 数据敏感标记），业务方**不写上报代码**。

### 4.3 ③ 用注解保护接口 + 行级判定 + 列表 `scope`

```java
@RestController
@RequestMapping("/orders")
public class OrderController {

    private final OrderService orderService;
    private final OrderRepository orderRepo;

    public OrderController(OrderService orderService, OrderRepository orderRepo) {
        this.orderService = orderService;
        this.orderRepo = orderRepo;
    }

    // 单条：注解做表级 RBAC；checkOwner 做行级；checkLabel 做密级
    @RequirePermission(resource = "orders", action = "read")
    @GetMapping("/{id}")
    public Order get(@PathVariable String id) {
        Order o = orderService.get(id);
        Authz.checkOwner(o.getOwnerId());   // 行级：只能看自己的（auditor 豁免）
        Authz.checkLabel(o.getLabel());     // 密级：主体密级 ≥ 客体密级
        return o;
    }

    // 写入：注解保护
    @RequirePermission(resource = "orders", action = "write")
    @PostMapping
    public Order create(@RequestBody OrderReq req) {
        return orderService.create(req);
    }

    // 列表：把行级过滤条件下推到查询，避免"查出全量再逐条过滤"
    @RequirePermission(resource = "orders", action = "read")
    @GetMapping
    public List<Order> list() {
        // 受限角色 → 自动变成 AND owner_id = :me；order_admin → 不加过滤
        return orderRepo.findAll(Authz.scope("orders"));
    }
}
```

`Authz.scope(resource)` 的语义（与基线一致）：

| 调用者角色 | `Authz.scope("orders")` 的结果 |
|---|---|
| `order_viewer` / `user`（受限） | 追加 `owner_id = :currentUserId` |
| `order_admin`（具备全量读） | 不追加行级过滤 |
| `auditor`（豁免） | 不追加行级过滤 |

### 4.4 ④ 自定义 Servlet Filter：读 → 验签（SM2，JWKS 缓存）→ 校验 `exp`/`jti` → 注入上下文 → 剥离 header

```java
package com.example.guanjin.filter;

import com.example.guanjin.*;
import javax.servlet.FilterChain;
import javax.servlet.ServletException;
import javax.servlet.http.HttpServletRequest;
import javax.servlet.http.HttpServletRequestWrapper;
import javax.servlet.http.HttpServletResponse;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.web.filter.OncePerRequestFilter;

import java.io.IOException;
import java.util.Collections;
import java.util.Enumeration;
import java.util.List;
import java.util.stream.Collectors;

/**
 * 关津内部令牌过滤器（gateway 形态）。
 * 读 X-GuanJin-Assertion → SM2 验签（JWKS 缓存）→ 校验 exp / jti → 注入上下文 → 剥离 header。
 */
public class GuanjinAssertionFilter extends OncePerRequestFilter {

    private static final Logger log = LoggerFactory.getLogger(GuanjinAssertionFilter.class);

    private final GuanjinProperties props;
    private final AssertionVerifier verifier;      // SM2 验签 + claim 校验
    private final ReplayGuard replayGuard;         // jti 防重放
    private final GuanjinContextHolder contextHolder;
    private final PublicPathMatcher publicPathMatcher;
    private final GuanjinAuthenticationConverter authConverter;

    public GuanjinAssertionFilter(GuanjinProperties props,
                                AssertionVerifier verifier,
                                ReplayGuard replayGuard,
                                GuanjinContextHolder contextHolder,
                                PublicPathMatcher publicPathMatcher,
                                GuanjinAuthenticationConverter authConverter) {
        this.props = props;
        this.verifier = verifier;
        this.replayGuard = replayGuard;
        this.contextHolder = contextHolder;
        this.publicPathMatcher = publicPathMatcher;
        this.authConverter = authConverter;
    }

    /** 公开接口不进入验签流程（deny-by-default；命中白名单才跳过）。 */
    @Override
    protected boolean shouldNotFilter(HttpServletRequest request) {
        return publicPathMatcher.isPublic(request.getRequestURI(), request.getMethod());
    }

    @Override
    protected void doFilterInternal(HttpServletRequest request,
                                    HttpServletResponse response,
                                    FilterChain chain) throws ServletException, IOException {

        final String headerName = props.getGateway().getAssertionHeader();
        final String raw = request.getHeader(headerName);

        // ── 1) 读令牌；缺失 → 401（deny-by-default）────────────────────────────
        if (raw == null || raw.isBlank()) {
            reject(response, "missing " + headerName);
            return;
        }

        // ── 2) 立即剥离：从进入 SDK 的第一刻起，下游就再也拿不到该 header ──────
        //        （同时丢弃客户端可能伪造的同名头）
        HttpServletRequest sanitized = new HeaderStrippingRequestWrapper(request, headerName);

        try {
            // ── 3) 验签（SM2）：公钥来自 JWKS 缓存，未知 kid 强制刷新 ──────────
            GuanjinAssertion assertion = verifier.verify(raw);

            // ── 4) 校验 exp / iat / iss / aud（含时钟偏移）──────────────────────
            verifier.validateClaims(assertion, props.getGateway());

            // ── 5) 重放检测：jti 在窗口内必须唯一；重复 → 拒绝 ──────────────────
            if (!replayGuard.firstUse(assertion.getJti(), assertion.getExp())) {
                reject(response, "replayed assertion");
                return;
            }

            // ── 6) 注入上下文：请求上下文 + Spring Security（让 @PreAuthorize 可用）
            GuanjinPrincipal principal = GuanjinPrincipal.from(assertion);
            contextHolder.set(principal);
            if (props.getSecurity().isInjectSecurityContext()) {
                SecurityContextHolder.getContext().setAuthentication(
                        authConverter.toAuthentication(principal));
            }

            // ── 7) 放行；用后清理 ThreadLocal，避免线程复用串号 ─────────────────
            try {
                chain.doFilter(sanitized, response);
            } finally {
                contextHolder.clear();
                SecurityContextHolder.clearContext();
            }
        } catch (GuanjinAssertionException e) {
            // fail-closed：任何验签 / 校验失败一律拒绝，绝不降级放行
            log.warn("关津内部令牌校验失败: {}", e.getMessage());
            reject(response, e.getMessage());
        }
    }

    private void reject(HttpServletResponse resp, String reason) throws IOException {
        SecurityContextHolder.clearContext();
        contextHolder.clear();
        resp.setStatus(HttpServletResponse.SC_UNAUTHORIZED);
        resp.setContentType("application/json;charset=UTF-8");
        resp.getWriter().write("{\"error\":\"unauthorized\",\"reason\":\"" + reason + "\"}");
    }

    /** 转发下游前彻底抹掉内部令牌头。 */
    static final class HeaderStrippingRequestWrapper extends HttpServletRequestWrapper {
        private final String stripped;
        HeaderStrippingRequestWrapper(HttpServletRequest request, String stripped) {
            super(request);
            this.stripped = stripped;
        }
        @Override public String getHeader(String name) {
            return stripped.equalsIgnoreCase(name) ? null : super.getHeader(name);
        }
        @Override public Enumeration<String> getHeaders(String name) {
            return stripped.equalsIgnoreCase(name)
                    ? Collections.emptyEnumeration()
                    : super.getHeaders(name);
        }
        @Override public Enumeration<String> getHeaderNames() {
            List<String> names = Collections.list(super.getHeaderNames()).stream()
                    .filter(n -> !stripped.equalsIgnoreCase(n))
                    .collect(Collectors.toList());
            return Collections.enumeration(names);
        }
    }
}
```

配套的**验签与 JWKS 缓存**（SM2，公钥按 `kid` 缓存）：

```java
package com.example.guanjin;

import org.bouncycastle.jce.provider.BouncyCastleProvider;
import org.springframework.stereotype.Component;

import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.security.*;
import java.time.Duration;
import java.time.Instant;
import java.util.Base64;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

/** SM2 验签 + JWKS 公钥缓存。未知 kid 强制刷新一次；刷新失败仍沿用旧缓存。 */
@Component
public class AssertionVerifier {

    static { Security.addProvider(new BouncyCastleProvider()); }

    private final GuanjinProperties props;
    private final JwksClient jwksClient;

    public AssertionVerifier(GuanjinProperties props, JwksClient jwksClient) {
        this.props = props;
        this.jwksClient = jwksClient;
    }

    public GuanjinAssertion verify(String rawJwt) {
        String[] parts = rawJwt.split("\\.");
        if (parts.length != 3) {
            throw new GuanjinAssertionException("令牌格式非法");
        }
        String kid = readKid(parts[0]);                 // 从 header 读 kid
        PublicKey pub = jwksClient.publicKey(kid);      // JWKS 缓存；未知 kid 强制刷新

        byte[] signed = (parts[0] + "." + parts[1]).getBytes(StandardCharsets.US_ASCII);
        byte[] signature = Base64.getUrlDecoder().decode(parts[2]);

        try {
            Signature sig = Signature.getInstance("SM3withSM2", "BC");
            sig.initVerify(pub);
            sig.update(signed);
            if (!sig.verify(signature)) {
                throw new GuanjinAssertionException("签名校验失败");
            }
        } catch (GeneralSecurityException e) {
            throw new GuanjinAssertionException("验签异常: " + e.getMessage());
        }
        return GuanjinAssertion.fromJson(
                new String(Base64.getUrlDecoder().decode(parts[1]), StandardCharsets.UTF_8));
    }

    public void validateClaims(GuanjinAssertion a, GuanjinProperties.Gateway g) {
        long now = Instant.now().getEpochSecond();
        long skew = g.getClockSkew().getSeconds();
        if (!g.getIssuer().equals(a.getIss())) throw new GuanjinAssertionException("iss 不符");
        if (!g.getAud().equals(a.getAud()))   throw new GuanjinAssertionException("aud 不符");
        if (a.getExp() + skew < now)          throw new GuanjinAssertionException("令牌已过期");
        if (a.getIat() - skew > now)          throw new GuanjinAssertionException("令牌尚未生效");
    }

    private String readKid(String headerB64) {
        // {"alg":"SM2","kid":"...","typ":"JWT"}
        String json = new String(Base64.getUrlDecoder().decode(headerB64), StandardCharsets.UTF_8);
        return Json.readField(json, "kid");
    }

    /** JWKS 拉取与缓存。拉取失败不影响已缓存公钥（公钥公开，验签照常执行——非放行降级）。 */
    @Component
    static class JwksClient {
        private final GuanjinProperties props;
        private final HttpClient http = HttpClient.newHttpClient();
        private final Map<String, PublicKey> cache = new ConcurrentHashMap<>();
        private volatile Instant loadedAt = Instant.EPOCH;

        JwksClient(GuanjinProperties props) { this.props = props; }

        PublicKey publicKey(String kid) {
            PublicKey key = cache.get(kid);
            if (key != null && !stale()) {
                return key;
            }
            refreshQuietly();                       // 未知 kid 或缓存过期 → 尝试刷新一次
            key = cache.get(kid);
            if (key != null) {
                return key;
            }
            // 缓存里也没有可用公钥 → fail-closed（绝不跳过验签）
            throw new GuanjinAssertionException("未找到可用验签公钥 kid=" + kid);
        }

        private boolean stale() {
            return Instant.now().isAfter(
                    loadedAt.plus(props.getGateway().getJwksCacheTtl()));
        }

        private synchronized void refreshQuietly() {
            try {
                String uri = props.getGateway().getJwksUri();
                HttpRequest req = HttpRequest.newBuilder(java.net.URI.create(uri))
                        .timeout(Duration.ofSeconds(3))
                        .GET().build();
                HttpResponse<String> resp = http.send(req, HttpResponse.BodyHandlers.ofString());
                Map<String, PublicKey> parsed = JwksParser.parse(resp.body()); // 按 kid 解析 JWK（SM2 曲线点 / x5c）
                cache.putAll(parsed);
                loadedAt = Instant.now();
            } catch (Exception e) {
                // JWKS 拉取失败：若已有缓存则静默继续用缓存（验签仍执行）；
                // 若缓存为空，由 publicKey() 统一抛 GuanjinAssertionException → fail-closed。
                if (cache.isEmpty()) {
                    throw new GuanjinAssertionException("JWKS 不可用且无本地缓存: " + e.getMessage());
                }
            }
        }
    }
}
```

---

## 5. 与 Spring Security 的集成方式

> 基线要求：**身份注入 `SecurityContext`，让业务方原有 `@PreAuthorize` 继续可用；SDK 不提供 `@RequireRole`。**（接入面契约 §1.3 ③，该节仍有效）

### 5.1 身份 → `Authentication`

```java
package com.example.guanjin;

import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.authentication.AbstractAuthenticationToken;

import java.util.Collection;
import java.util.List;
import java.util.Set;
import java.util.stream.Collectors;

/** 把 关津身份转成 Spring Security 的 Authentication。 */
public class GuanjinAuthenticationConverter {

    private final GuanjinProperties props;
    private final GrantedAuthoritiesResolver resolver;   // 可选扩展点（能力 #11）

    public GuanjinAuthenticationConverter(GuanjinProperties props,
                                        GrantedAuthoritiesResolver resolver) {
        this.props = props;
        this.resolver = resolver;
    }

    public AbstractAuthenticationToken toAuthentication(GuanjinPrincipal p) {
        Set<String> roles = p.getRoles();
        List<GrantedAuthority> authorities = roles.stream()
                // 加 ROLE_ 前缀，使业务方 hasRole('order_admin') 生效
                .map(r -> (GrantedAuthority) new SimpleGrantedAuthority(
                        props.getSecurity().getRolePrefix() + r))
                .collect(Collectors.toList());

        // 业务方注册的"身份属性 → 权限"映射（业务语义，归业务方）
        if (resolver != null) {
            authorities.addAll(resolver.resolve(p));
        }
        return new GuanjinAuthenticationToken(p, authorities);
    }

    static final class GuanjinAuthenticationToken extends AbstractAuthenticationToken {
        private final GuanjinPrincipal principal;
        GuanjinAuthenticationToken(GuanjinPrincipal principal, Collection<GrantedAuthority> authorities) {
            super(authorities);
            this.principal = principal;
            setAuthenticated(true);     // 身份已在过滤器里验签通过
        }
        @Override public Object getCredentials() { return null; }
        @Override public Object getPrincipal()   { return principal; }
    }
}
```

### 5.2 业务方代码：原有写法不变

```java
// 业务方原有代码——引入依赖后无需任何改动即可继续用
@PreAuthorize("hasRole('order_admin')")
@PostMapping("/orders/{id}/refund")
public RefundDto refund(@PathVariable String id) { ... }
```

### 5.3 三条边界

| 边界 | 说明 |
|---|---|
| **SDK 只注入"你是谁"** | 注入的是 `sub` / `roles` / `acr` 等身份事实，不是"你能做什么" |
| **不提供 `@RequireRole`** | 需要方法级角色检查时，用 `@PreAuthorize`（业务方原有能力）或 `@RequirePermission`（关津权限点） |
| **扩展点归业务方** | `GrantedAuthoritiesResolver` 由业务方实现，把身份属性映射为业务权限——这正是"平台给身份、业务方定权限"的落地 |

### 5.4 过滤器链位置

`GuanjinAssertionFilter` 应排在 Spring Security 的 `UsernamePasswordAuthenticationFilter` **之前**（身份由 SDK 注入，不经过表单登录），
并确保 `SecurityFilterChain` 对受保护路径设置 `authenticated()`。SDK 通过自动配置完成注册，业务方无需手写 `SecurityConfig`：

> ⚠️ **2.x 的自动配置注册方式与 3.x 不同**：2.x 需要在 `src/main/resources/META-INF/spring.factories` 里声明
> `org.springframework.boot.autoconfigure.EnableAutoConfiguration=com.guanjin.sdk.GuanjinSecurityAutoConfiguration`；
> 3.x 才改用 `META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports`。本项目**只出 2.x 产物**，因此用前者。

```java
@Configuration                      // 2.x 用 @Configuration，不用 3.x 的 @AutoConfiguration
@EnableConfigurationProperties(GuanjinProperties.class)
public class GuanjinSecurityAutoConfiguration {

    @Bean
    public GuanjinAssertionFilter guanjinAssertionFilter(/* 依赖注入 */) {
        return new GuanjinAssertionFilter(/* ... */);
    }

    @Bean
    public FilterRegistrationBean<GuanjinAssertionFilter> guanjinFilterRegistration(
            GuanjinAssertionFilter filter, GuanjinProperties props) {
        var reg = new FilterRegistrationBean<>(filter);
        reg.setOrder(org.springframework.core.Ordered.HIGHEST_PRECEDENCE + 100); // 在 Security 过滤器之前
        reg.addUrlPatterns("/*");
        return reg;
    }
}
```

---

## 6. 与数据面的协作形态

> **已决策**：关津承载数据面（薄数据面 + 前置成熟组件）。**公共流量必过数据面**，SDK 只提供网关形态，不提供"业务系统直连"的兜底模式。
> 因此 SDK 只需处理一种形态：**读网关注入的 `X-GuanJin-Assertion` 并验签**。

### 6.1 唯一形态：请求经 关津数据面转发

```text
客户端 ── 用户令牌 ──▶ 关津数据面 ── 逐请求签发 X-GuanJin-Assertion(SM2) ──▶ 业务系统(SDK)
```

| 项目 | 内容 |
|---|---|
| SDK 职责 | 只验签内部令牌（SM2）+ 读身份 + 重放检测 + 剥离 header |
| 优点 | 撤销**实时生效**（数据面在路径上）；业务系统不接触用户令牌；重放窗口极短（60s） |
| 代价 | 数据面成为全局可用性单点，必须冗余（数据面设计 §5） |
| 剥离 | 必须（防止内部令牌被透传到更深下游 / 落日志） |

## 7. 异常与降级

> **总原则（基线硬约束）：依赖故障时 fail-closed，不降级放行**（预设档位清单「依赖故障时的安全默认」；接入面契约 §1.4「fail-closed on signature」）。**没有任何配置项可以关闭 fail-closed。**

| 场景 | 行为 | 是否 fail-closed |
|---|---|:--:|
| **验签失败**（签名不匹配） | 401，清除上下文，不注入身份 | ✅ |
| **令牌缺失** | 401（deny-by-default） | ✅ |
| **`exp` 过期 / `iat` 未生效（超出时钟偏移）** | 401 | ✅ |
| **`iss` / `aud` 不符** | 401（防止令牌被跨系统挪用） | ✅ |
| **`jti` 重放** | 401 | ✅ |
| **JWKS 拉取失败，但本地有缓存** | **沿用缓存公钥继续验签**（公钥公开，验签仍被真实执行 → 不构成"放行降级"） | 否（验签照常） |
| **JWKS 拉取失败，且本地无任何缓存** | 503（无法验签 = 无法确认身份） | ✅ |
| **`jti` 存储（`replay-store`）不可用** | 拒绝请求（无法确认是否重放） | ✅ |
| **Redis 不可用**（当 `replay-store=redis` 或用于撤销比对） | 拒绝请求；与平台侧"Redis 不可用时整体拒绝服务"一致 | ✅ |
| **平台不可达 · 策略上报失败** | **不阻塞业务请求**；本地持久化队列 + 退避重试；上报失败降级为"延后重试"，不影响判定 | 否（不影响判定） |
| **平台不可达 · 拉取绑定失败** | 若"角色已放进令牌"（方案 A）→ 判定不依赖平台，无影响；若依赖 `role_bindings` 缓存（方案 B）→ 无缓存时拒绝该用户的授权判定 | 视方案（索引 §4.2-24） |
| **模型 / 策略加载完整性校验失败** | 拒绝授权判定（对齐「权限数据完整性校验」），不加载可疑策略 | ✅ |
| **平台地址未配置 / 非法** | 启动失败（快速失败，而非静默放行） | ✅ |

**永不发生的两件事**（若发生即为缺陷）：
1. 因"验签失败 / 依赖不可用"而**放行**请求；
2. 提供任何"跳过验签 / 关闭 fail-closed"的生产开关。

---

---

*本文是 关津设计基线的一部分（能力清单 22 条、配置项 32 条）。与基线冲突处以 `GuanJin-设计基线索引.md` §2 为准；文中标注 ⚠️ 的待定项，集中清单见该索引 §4.2 / §5。*
