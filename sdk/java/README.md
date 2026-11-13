# 关津 SDK · Java 8（`guanjin-core` + `guanjin-spring-boot2`）

> 权威契约：**`docs/sdk-contract.md`**（22 条能力 `GJ-CAP-01..22` + 29 项配置）。
> 本目录只放**实现**，口径一律以那份文档为准，本文不复制契约内容。
>
> **D23③（spencer 2026-11-13 追认通过；手段见 D28③）**：`sdk/java` 的依赖漏洞覆盖——
> 本目录聚合 `pom.xml` 已内置 `org.cyclonedx:cyclonedx-maven-plugin`（**2.8.0**，版本锁死；
> **不绑 phase**，默认 `mvn test` 行为零变化）。生成：`mvn cyclonedx:makeAggregateBom` →
> `target/bom.json`（CycloneDX **1.5** 聚合 BOM，两模块依赖一体收录，**每次构建重生成、不用人维护**）。
> 注意：2.8.0 的 goal 标注 `@RequiresOnline`，**生成需联网**（D23 写的「`mvn -o`」不可行，按
> 「复用当次已热的 `~/.m2`」执行——`sdk` job 拉依赖后仓库已热）；**消费侧离线**（`vuln` job 由
> 同一判定引擎 `scripts/vuln-gate.py` 读 artifact、不解析 pom、不打外网）。链路 = **Nova 产 → Gate 判**；
> **死线 2026-12-10**，逾期则 `AC-ENG-07` 账目降级为「Java 侧未覆盖」并如实记账。

## 交付结构（契约 §5.1）

| 模块 | 坐标 | 依赖边界 |
|---|---|---|
| `guanjin-core` | `com.guanjin:guanjin-core` | **零 Spring / 零 servlet**（AC-SDK-09）：29 项配置、启动自检、JWKS 缓存、`jti` 防重放、claim 校验、公开路径 |
| `guanjin-spring-boot2` | `com.guanjin:guanjin-spring-boot2` | Spring Boot **2.7.18**（`javax.*`）适配：`@ConfigurationProperties`、自动配置（`spring.factories`）、servlet 过滤器、Redis 重放存储 |

Java 8 双门禁（契约 §5.1.1）：JDK 9+ 构建走 `release=8`（挡 API 面）；CI 另用**真实 JDK 8** 跑一遍
（挡"用 17 编、目标 8"的虚假安全感）。`scripts/check-sdk-java8.sh` 另外机械校验
「core 依赖树无 spring/servlet」+「14 条禁用 API」+「class 文件 major version ≤ 52」。

## 已交付什么（S9 两刀：最小接入 + 验签/判定主路径）

| 判据 | 交付物 | 证据（测试名） |
|---|---|---|
| `AC-SDK-01` | 无 `mode`/`direct` 键；出现即启动失败 | `GuanjinConfigKeysTest.AC_SDK_01_*`、`GuanjinConfigLoaderTest.AC_SDK_01_*`、`GuanjinAutoConfigurationTest.AC_SDK_01_modeKeyFailsStartup` |
| `AC-SDK-02` | `enabled`/`authz.enabled=false` 仅 `local\|test`，其它 profile **启动失败** | `GuanjinConfigValidatorTest.AC_SDK_02_*`、`GuanjinAutoConfigurationTest.AC_SDK_02_*` |
| `AC-SDK-03` | `jwks-uri` 默认 = `${platform.base-url}/.well-known/jwks.json`，**首启 404 即失败** | `GuanjinConfigValidatorTest.AC_SDK_03_*`、`JwksClientTest.AC_SDK_03_404IsFailureNotWarning`、`GuanjinAutoConfigurationTest.AC_SDK_03_*` |
| `AC-SDK-04` | 平台地址缺失/非法 → 启动失败（不退化为匿名、不先跑起来）；**附加用例**：`gateway.jwks-uri` 与 `base-url` 不同源 → 启动失败（§3.2 #12 安全约束的可执行化，加固、不进 CH） | `GuanjinConfigValidatorTest.AC_SDK_04_*`、`GuanjinAutoConfigurationTest.AC_SDK_04_*` |
| `AC-SDK-06` | `jti` 存储不可用 → **拒请求**（503 `REDIS_UNAVAILABLE`） | `ReplayGuardTest.AC_SDK_06_*`、`RedisReplayStoreIT.AC_SDK_06_*`、`RedisReplayFilterIT.AC_SDK_06_redisOutageYields503RedisUnavailable` |
| `AC-SDK-07` | 29 项逐项一致（键名/类型/默认值，反射比对契约表） | `GuanjinPropertiesReflectionTest.AC_SDK_07_reflectionMatchesContractTableRowByRow` |
| `AC-SDK-08`（剥离/注入半边） | D10 六步：读头→验签(SPI)→claim→重放→注入→**替换请求视图** | `GuanjinAssertionFilterTest.AC_SDK_08_*` |
| `AC-SDK-10` | ①默认 `replay-store=redis`；②默认 `agent.pull.enabled=false`，30s 内 `/api/v1/agent/policies` **零调用** | `GuanjinConfigValidatorTest.AC_SDK_10_*`、`AgentPullZeroCallIT.AC_SDK_10_*` |
| `AC-SESS-05` | 多副本（非 `local\|test`）用 `memory` → 启动失败；默认即 `redis` | `GuanjinConfigValidatorTest.AC_SESS_05_*`、`RedisReplayStoreIT.AC_SESS_05_*` |
| `AC-DP-08`（SDK 半边） | 同 `jti` 二次 → **401 `TOKEN_INVALID`**（由验签方产生并记账，平台数据面不判重） | `ReplayGuardTest.AC_DP_08_*`、`GuanjinAssertionFilterTest.AC_DP_08_*`、`RedisReplayFilterIT.AC_DP_08_*`、`Sm2FilterIT.AC_DP_08_replayOfRealGatewayJtiIs401` |
| `AC-DP-03`（SDK 反向）/ `GJ-CAP-01/02/04` | **SM2 真验签**：`alg=SM2`（D29）+ `kid` 选键 + 未知 kid 强刷一次 + `alg` 不协商；篡改 payload / 未知 kid / `alg=SM3withSM2`（用户令牌用途）→ 401 | `Sm2AssertionVerifierTest`（6 条，向量由平台签发代码产出）、`Sm2FilterIT`（3 条，真 HTTP JWKS + 真 Redis） |
| `AC-SDK-08` ③ | **三处消费者**都取不到断言头：handler / 下游转发 / 日志 | `AssertionHeaderConsumersTest.AC_SDK_08_threeConsumersCannotSeeAssertionHeader` |
| `AC-SDK-08` ④ | Filter 排在 Spring Security **之前**（`HIGHEST_PRECEDENCE + 100` < `SecurityProperties.DEFAULT_FILTER_ORDER` = -100，直接引用常量断言） | `GuanjinFilterOrderTest` |
| `GJ-CAP-10`（Spring 落点） | `SecurityContext` 注入：`GuanjinAuthenticationConverter.toAuthentication(p)` 产出已认证 `Authentication`，principal = `GuanjinPrincipal`（**不含断言原文**，credentials/details 均 null），authorities = `role-prefix` + 可选 `GrantedAuthoritiesResolver`（GJ-CAP-11，抛异常即 fail-closed 403）；未认证取身份是 **401** 不是 500 | `GuanjinSecurityContextTest`（8 条，含 filter 注入→清理） |
| `GJ-CAP-12~16/18` | **jcasbin 真判定**：模型来自 `classpath:/guanjin/model.conf`（配置 #17），策略来自业务声明的 `AuthzModel`（不内联硬编码）；`@RequirePermission` 执行点；`checkRow`（auditor 豁免）/`checkLabel`（主体 ≥ 客体）；`defaultRole` 兜底 | `AuthzTest`（8 条）、`GuanjinAuthorizationInterceptorTest`（5 条） |

## 还没交付什么（显式标注，不静默缺）

- **`GJ-CAP-17 Scope`**（行级条件下推为 SQL 过滤）：未实现，避免在 SDK 里猜业务表结构。
- **`GJ-CAP-17 Scope` / `GJ-CAP-20 PolicyReport` / `GJ-CAP-21 PolicyPull`：M1 判据里没有对应 AC**，
  按纪律不实现、不自造 AC：
  - `GJ-CAP-12~18`（含 `Scope`）——`docs/acceptance-criteria.md:379` 明写「M1 只验『接入 + 验签 + 剥离』，
    **不验本地授权判定模型** → **M5**」；
  - `GJ-CAP-20/21`——对应判据 `AC-AGENT-01~03` 标 **`NOT-APPLICABLE · M2 前置`**（`acceptance-criteria.md:292-294`），
    且 `m1-slices.md` S8 行注明 `POST /api/v1/agent/report`「M1 只落验签件，判据归 M2」。
  - 已实现的相邻部分是 `AC-SDK-10②`（`agent.pull.enabled=false` → 30s 内零次 `/api/v1/agent/policies`，有正控制），
    这条是 M1 判据，未退化。
- **策略上报 / 拉取的 HMAC canonical 串**（`GJ-CAP-20`/`21`）：`agent.pull.enabled=true` 而没有
  `PullRequestSigner` bean 时启动失败（**不发未签名回源请求**）。
- **`AuthzModel` 的上报**（GJ-CAP-20 的 `PolicyReporter`）：未实现，故平台侧策略快照暂为空。

## 跨语言验签向量（不是自造 claim）

`sdk/java/guanjin-core/src/test/resources/vectors/assertion-vectors.json` 由**平台自己的签发代码**产出：

```bash
# 生成器在 artifacts/ 下（不入库），import internal/signing —— 即数据面签发断言的那条路径
go run ./artifacts/local/gen-sdk-vectors
```

8 条向量：`valid` / `tamperedPayload`（签名真、payload 被换）/ `unknownKid` / `expired` / `futureIat` /
`wrongIss` / `wrongAud` / `oidcAlg`（`alg=SM2` 的用户令牌用途键 → 拒绝算法降级）。
`valid` 的 `iat` 取过去、`exp` 取 2030，保证"运行时刻"的 `ClaimValidator` 也接受它；
时间敏感的 claim 断言用向量里的 `referenceNow` 判，不依赖跑测试的钟。

## 怎么验（真 Redis，不许 mock）

```bash
# 单测 + 集成（真 Redis + 真 HTTP 假平台；SKIP 必须为 0）
GUANJIN_TEST_REDIS_ADDR=127.0.0.1:6379 GUANJIN_TEST_REDIS_PASSWORD=123456 \
  mvn -B -f sdk/java/pom.xml -Pintegration verify

# 门禁（core 依赖树 / 禁用 API / class 版本）
./scripts/check-sdk-java8.sh
```

集成用例（`*IT.java`，failsafe）覆盖：`RedisReplayStoreIT`（`SET NX PX` 语义 + TTL + 连接被重置 + 端口不通）、
`RedisReplayFilterIT`（同 `jti` 二次 401、Redis 挂 503）、`AgentPullZeroCallIT`（30s 零调用 + 正控制）。

## 接入片段

```java
// 1) 引 guanjin-spring-boot2，配 3 个必填项（其余 26 项有默认值，改默认值等于改契约）
// guanjin.platform.base-url=https://guanjin.example.com
// guanjin.client.id=orders
// guanjin.client.hmac-secret=${GUANJIN_HMAC_SECRET}   # 环境变量注入，禁入仓库/禁日志

// 2) 声明授权模型（authz.enabled=true 时**必需**：没有策略依据就拒绝启动）
@Bean
AuthzModel authzModel() {
    return AuthzModel.builder()
        .role("order_admin").can("orders", "read", "write")
        .role("auditor").can("orders", "read").withClearance(3)  // auditor 行级豁免
        .defaultRole("user").can("profile", "read")              // 权限最小兜底
        .build();
}

// 3) 保护接口（一行注解；模型/策略都不写在代码里）
@RequirePermission(resource = "orders", action = "write")
@PostMapping("/orders")
public Order create(@RequestBody OrderReq req) {
    Authz.checkRow(req.getUserId());   // 行级：只能动自己的（auditor 豁免）
    Authz.checkLabel(req.getLabel());  // 密级：主体 ≥ 客体
    return orderService.create(req);
}

// 4) 取身份
GuanjinPrincipal p = GuanjinContextHolder.current();   // 公开路径为 null
```

**过滤器顺序** `HIGHEST_PRECEDENCE + 100`（在 Spring Security 之前）；剥离对象是**交给宿主的请求视图**
（`getHeader` 为 `null`、`getHeaderNames` 不含该头），不是"进入 SDK 就丢头"。
