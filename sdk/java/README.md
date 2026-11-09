# 关津 SDK · Java 8（`guanjin-core` + `guanjin-spring-boot2`）

> 权威契约：**`docs/sdk-contract.md`**（22 条能力 `GJ-CAP-01..22` + 29 项配置）。
> 本目录只放**实现**，口径一律以那份文档为准，本文不复制契约内容。

## 交付结构（契约 §5.1）

| 模块 | 坐标 | 依赖边界 |
|---|---|---|
| `guanjin-core` | `com.guanjin:guanjin-core` | **零 Spring / 零 servlet**（AC-SDK-09）：29 项配置、启动自检、JWKS 缓存、`jti` 防重放、claim 校验、公开路径 |
| `guanjin-spring-boot2` | `com.guanjin:guanjin-spring-boot2` | Spring Boot **2.7.18**（`javax.*`）适配：`@ConfigurationProperties`、自动配置（`spring.factories`）、servlet 过滤器、Redis 重放存储 |

Java 8 双门禁（契约 §5.1.1）：JDK 9+ 构建走 `release=8`（挡 API 面）；CI 另用**真实 JDK 8** 跑一遍
（挡"用 17 编、目标 8"的虚假安全感）。`scripts/check-sdk-java8.sh` 另外机械校验
「core 依赖树无 spring/servlet」+「14 条禁用 API」+「class 文件 major version ≤ 52」。

## 本切片（S9 最小接入）已交付什么

| 判据 | 交付物 | 证据（测试名） |
|---|---|---|
| `AC-SDK-01` | 无 `mode`/`direct` 键；出现即启动失败 | `GuanjinConfigKeysTest.AC_SDK_01_*`、`GuanjinConfigLoaderTest.AC_SDK_01_*`、`GuanjinAutoConfigurationTest.AC_SDK_01_modeKeyFailsStartup` |
| `AC-SDK-02` | `enabled`/`authz.enabled=false` 仅 `local\|test`，其它 profile **启动失败** | `GuanjinConfigValidatorTest.AC_SDK_02_*`、`GuanjinAutoConfigurationTest.AC_SDK_02_*` |
| `AC-SDK-03` | `jwks-uri` 默认 = `${platform.base-url}/.well-known/jwks.json`，**首启 404 即失败** | `GuanjinConfigValidatorTest.AC_SDK_03_*`、`JwksClientTest.AC_SDK_03_404IsFailureNotWarning`、`GuanjinAutoConfigurationTest.AC_SDK_03_*` |
| `AC-SDK-04` | 平台地址缺失/非法 → 启动失败（不退化为匿名、不先跑起来） | `GuanjinConfigValidatorTest.AC_SDK_04_*`、`GuanjinAutoConfigurationTest.AC_SDK_04_*` |
| `AC-SDK-06` | `jti` 存储不可用 → **拒请求**（503 `REDIS_UNAVAILABLE`） | `ReplayGuardTest.AC_SDK_06_*`、`RedisReplayStoreIT.AC_SDK_06_*`、`RedisReplayFilterIT.AC_SDK_06_redisOutageYields503RedisUnavailable` |
| `AC-SDK-07` | 29 项逐项一致（键名/类型/默认值，反射比对契约表） | `GuanjinPropertiesReflectionTest.AC_SDK_07_reflectionMatchesContractTableRowByRow` |
| `AC-SDK-08`（剥离/注入半边） | D10 六步：读头→验签(SPI)→claim→重放→注入→**替换请求视图** | `GuanjinAssertionFilterTest.AC_SDK_08_*` |
| `AC-SDK-10` | ①默认 `replay-store=redis`；②默认 `agent.pull.enabled=false`，30s 内 `/api/v1/agent/policies` **零调用** | `GuanjinConfigValidatorTest.AC_SDK_10_*`、`AgentPullZeroCallIT.AC_SDK_10_*` |
| `AC-SESS-05` | 多副本（非 `local\|test`）用 `memory` → 启动失败；默认即 `redis` | `GuanjinConfigValidatorTest.AC_SESS_05_*`、`RedisReplayStoreIT.AC_SESS_05_*` |
| `AC-DP-08`（SDK 半边） | 同 `jti` 二次 → **401 `TOKEN_INVALID`**（由验签方产生并记账，平台数据面不判重） | `ReplayGuardTest.AC_DP_08_*`、`GuanjinAssertionFilterTest.AC_DP_08_*`、`RedisReplayFilterIT.AC_DP_08_*` |

## 本切片**没有**交付什么（显式标注，不静默缺）

- **SM2 验签实现**（`GJ-CAP-01`/`02`/`04` 的密码学部分）：`AssertionVerifier` 是 SPI，
  `Sm2AssertionVerifier`（BouncyCastle + `kid` 选键 + `alg` 固定 `SM3withSM2`）随验签切片落地。
  **在此之前**：容器里没有 `AssertionVerifier` bean 时启动**直接失败**——不会出现"没验签却在保护"的形态。
- **jcasbin 判定**（`GJ-CAP-12`~`18`）、**Spring Security `Authentication` 注入**（`GJ-CAP-10` 的 Spring 落点）、
  **策略上报 / 拉取的 HMAC 签名**（`GJ-CAP-20`/`21` 的 canonical 串）：均属后续切片。
  `agent.pull.enabled=true` 而没有 `PullRequestSigner` bean 时同样启动失败（**不发未签名回源请求**）。

## 怎么验（真 Redis，不许 mock）

```bash
# 单测 + 集成（真 Redis + 真 HTTP 假平台；SKIP 必须为 0）
GUANJIN_TEST_REDIS_ADDR=127.0.0.1:6379 GUANJIN_TEST_REDIS_PASSWORD=123456 \
  mvn -B -f sdk/java/pom.xml -Pintegration verify

# 门禁（core 依赖树 / 禁用 API / class 版本）
./scripts/check-sdk-java8.sh
```

集成用例（`*IT.java`，failsafe）覆盖：`RedisReplayStoreIT`（`SET NX PX` 语义 + TTL + AUTH 失败 + 端口不通）、
`RedisReplayFilterIT`（同 `jti` 二次 401、Redis 挂 503）、`AgentPullZeroCallIT`（30s 零调用 + 正控制）。

## 接入片段

```java
// 1) 引 guanjin-spring-boot2，配 3 个必填项（其余 26 项有默认值，改默认值等于改契约）
// guanjin.platform.base-url=https://guanjin.example.com
// guanjin.client.id=orders
// guanjin.client.hmac-secret=${GUANJIN_HMAC_SECRET}   # 环境变量注入，禁入仓库/禁日志

// 2) 什么都不用写：自动配置会做启动自检（拉 JWKS、打印放行清单），失败即进程退出非 0
// 3) 业务侧取身份
GuanjinPrincipal p = GuanjinContextHolder.current();   // 公开路径为 null
```

**过滤器顺序** `HIGHEST_PRECEDENCE + 100`（在 Spring Security 之前）；剥离对象是**交给宿主的请求视图**
（`getHeader` 为 `null`、`getHeaderNames` 不含该头），不是"进入 SDK 就丢头"。
