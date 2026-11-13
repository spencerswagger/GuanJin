# Java SDK 依赖漏洞升级方案（D23③ SBOM 落地后的 33 条债）

> **状态：方案，未实施。** 实施等 spencer 按队列放行。
> 证据口径 = `docs/qa/vuln-allowlist.tsv`（33 条，到期 **2026-12-13**）+ Gate 的 33 条对账表 +
> 本机试跑（`mvn verify -Pintegration` × 1，全绿；试跑改动已回滚，本文只留结论）。
> 复算命令见 §7。

## 0. 一页纸结论

| | 条数 | 能不能一批做 | 结论 |
|---|---|---|---|
| **A 批（同线小跳）** | **24** | **能，已试跑验证** | 1 个 PR 全清：8 个模块、19 条 dependencyManagement 配置，单测 120 + 集成 10 全绿，无需改代码 |
| **B 批（撞 Java 17 墙）** | **9** | **不能** | Spring 6/7、Boot 3、Security 6 都要 Java 17；Java 8 轨（契约 §5.1）线内无补丁。要 spencer 拍板：加 Boot 3 轨道 / 风险接受并续期 |
| 合计 | 33 | | **最晚动手日：2026-12-12；死线 2026-12-13（白名单到期即重判红）** |

**不做会怎样**：2026-12-13 白名单到期，`vuln` job 对 33 条重判红 → main 全红、所有 PR 的合并门禁被堵；
且「到期就续期」等于把"不升级"制度化（D23 的教训写死了不许这么干）。

## 1. 33 条从哪来、当前形态

- 来源：**#136**（D23③ 离线 SBOM 收尾件）首次把 `sdk/java` 的 CycloneDX `bom.json` 纳入 `vuln` job，
  Maven 生态首扫命中 **CRITICAL 5 / HIGH 28**，Gate 按「先上白名单 + 到期重判」收口，33 条全到期 **2026-12-13**。
- 白名单文件：`docs/qa/vuln-allowlist.tsv`（Gate 维护；规则：到期即红、不再命中也红=不许只增不减）。
- 依赖来源（本机 `mvn dependency:tree` 实测，非推测）：

| 模块 | 当前版本 | 来源 | 引入方式 |
|---|---|---|---|
| `bcprov-jdk18on` | 1.78.1 | `guanjin-core` 直接依赖 | 我们自己的属性（可直接改） |
| `jackson-databind/core/annotations` | 2.13.5 | `guanjin-core` 直接依赖 + Boot BOM 管版本 | BOM |
| `commons-io` | 2.11.0 | **jcasbin 1.55.0 的传递依赖** | 无 BOM 管，要直管 |
| `netty-*`（8 个） | 4.1.101.Final | `spring-boot-starter-data-redis`（optional）→ lettuce | BOM |
| `logback-classic/core` | 1.2.12 | `spring-boot-starter-logging`（optional） | BOM |
| `snakeyaml` | 1.30 | `spring-boot`（compile） | BOM |
| `spring-core/web/webmvc/expression/context` | 5.3.31 | Boot BOM / `spring-webmvc`(optional) | BOM |
| `spring-security-core/crypto` | 5.7.11 | `spring-security-core`(optional) | BOM |
| `spring-boot`（含 autoconfigure） | 2.7.18 | 我们自己的 `spring-boot.version` 属性 | 属性 |

## 2. A 批：24 条同线小跳（可一批做，已验证）

### 2.1 目标版本与改法

| 模块 | 当前 → 目标 | 改法（`sdk/java/pom.xml`） | 清掉的公告 |
|---|---|---|---|
| `logback-classic` / `logback-core` | 1.2.12 → **1.2.13** | dependencyManagement 直管 2 条 | 3 |
| `jackson-*` | 2.13.5 → **2.18.11** | **`jackson-bom` 5.x 前置于 Boot BOM 导入** | 6 |
| `commons-io` | 2.11.0 → **2.14.0** | dependencyManagement 直管 1 条 | 1 |
| `netty-*` | 4.1.101.Final → **4.1.137.Final** | **`netty-bom` 前置于 Boot BOM 导入** | 5 |
| `bcprov-jdk18on` | 1.78.1 → **1.85** | 我们自己的 `<bcprov.version>` 属性 | 3 |
| `spring-framework`（core/web/webmvc/expression/context） | 5.3.31 → **5.3.39** | **`spring-framework-bom` 前置于 Boot BOM 导入** | 3 |
| `spring-security`（core/crypto） | 5.7.11 → **5.7.12** | **`spring-security-bom` 前置于 Boot BOM 导入** | 1 |
| `snakeyaml` | 1.30 → **2.0** | dependencyManagement 直管 1 条 | 2 |
| **合计** | | 4 个 BOM 导入（放在 `spring-boot-dependencies` **之前**）+ 4 条直管 | **24** |

### 2.2 Maven 陷阱（试跑实测，写下来防踩）

**改了属性但版本不动**：在导入 BOM 的 POM 里写 `<jackson-bom.version>2.18.11</jackson-bom.version>` **无效**——
导入 BOM 的占位符按 **BOM 自己的 properties** 解析，不读引用方的。实测：只写属性时 `dependency:tree`
仍是 2.13.5 / 4.1.101.Final / 5.3.31；改为「我们的 BOM 导入排在 Boot BOM 之前」（先声明先赢）后，
全部按目标解析。四个 BOM 必须放在 `spring-boot-dependencies` **前面**。

### 2.3 可直接套用的 diff 骨架

```xml
<!-- <dependencyManagement><dependencies> 最前面，顺序即优先级 -->
<dependency><groupId>org.springframework</groupId><artifactId>spring-framework-bom</artifactId>
  <version>5.3.39</version><type>pom</type><scope>import</scope></dependency>
<dependency><groupId>org.springframework.security</groupId><artifactId>spring-security-bom</artifactId>
  <version>5.7.12</version><type>pom</type><scope>import</scope></dependency>
<dependency><groupId>com.fasterxml.jackson</groupId><artifactId>jackson-bom</artifactId>
  <version>2.18.11</version><type>pom</type><scope>import</scope></dependency>
<dependency><groupId>io.netty</groupId><artifactId>netty-bom</artifactId>
  <version>4.1.137.Final</version><type>pom</type><scope>import</scope></dependency>
<!-- 之后才是 spring-boot-dependencies 2.7.18 的 import -->

<!-- 直管 4 条（Boot BOM 不管的） -->
ch.qos.logback:logback-classic 1.2.13 / ch.qos.logback:logback-core 1.2.13
org.yaml:snakeyaml 2.0 / commons-io:commons-io 2.14.0

<!-- 属性：<bcprov.version>1.78.1 → 1.85</bcprov.version> -->
```

### 2.4 风险与验证证据（本机试跑，一次通过）

| 项 | 结果 |
|---|---|
| `dependency:tree` | 8 个模块全部按目标解析（jackson 2.18.11 / netty 4.1.137.Final / spring 5.3.39 / security 5.7.12 / logback 1.2.13 / snakeyaml 2.0 / commons-io 2.14.0 / bcprov 1.85） |
| 单测 | **120/120 绿**（core 73 + boot2 47） |
| 真 Redis 集成（`-Pintegration verify`） | **10/10 绿**，含 `Sm2FilterIT`（BC 1.85 上真 SM2 验签仍过）、`RedisReplayFilterIT`（lettuce 6.1 + netty 4.1.137 真连） |
| `scripts/check-sdk-java8.sh` | 三闸 OK |
| Java 8 兼容（class major 抽查） | bcprov 1.85=52、jackson-databind 2.18.11=52、netty-handler 4.1.137=50、logback 1.2.13=50、commons-io 2.14=52、spring-web 5.3.39=52、snakeyaml 2.0=51 —— **全部 ≤52** |
| snakeyaml 2.0 × Boot 2.7.18 | **实测可用**：用 boot2 的 classpath 跑 `YamlPropertySourceLoader`（Boot 2.7.18 的 `OriginTrackedYamlLoader` 已适配 `LoaderOptions` API），YAML 正常解析 |
| gate 复算（`vuln-gate.py --java-bom`） | 白名单命中 33→**9**；**无新增阻断级命中**（升级后只多出 MODERATE/LOW：logback-core ×5、spring-context/webmvc ×若干，均低于阻断阈值，记录即可） |

**风险判级**：
- `bcprov` 是密码学底座（SM2 验签）——唯一有实质风险的一项，已用 `Sm2FilterIT` 真验签兜住；合入前必须有这条 IT 的绿。
- `jackson 2.13→2.18` 跨 5 个 minor，但 Spring Boot 2.7 只用注解/`ObjectMapper` 基础面，全套单测 + IT 已覆盖。
- `netty` 只经 lettuce 进（Redis 客户端），受 `RedisReplay*IT` 覆盖。
- `snakeyaml 2.0` API 变（`Yaml(Constructor)` 移除）——Boot 2.7.18 已适配（上表实测），**宿主若自行锁 2.0 也安全**。
- 其余（logback/commons-io/spring 小跳/security 小跳）为补丁级，无 API 面。

### 2.5 A 批同 PR 必须做的两件事

1. **删掉 24 行白名单**（gate 规则：不再命中 = 红，必须删；不许只增不减）。
2. `sdk/java/target/bom.json` 重生成后跑 `vuln-gate.py` 复核（命令见 §7），确认剩 9 条与本文一致。

## 3. B 批：9 条跨线（Java 17 墙，要拍板）

这 9 条的共同点：**当前线的 OSS 版本已不可能修**（`<=X` = last_affected，或 fix 只在 6.x/7.x/3.x 线），
而 Spring 6/7、Boot 3、Security 6 均要求 **Java 17**，与本 SDK 的 Java 8 契约（`docs/sdk-contract.md` §5.1.1）正面冲突。

| # | 公告 | 模块@当前 | 级别 | 影响面（summary） | 线内为何无解 | 最低安全版本 |
|---|---|---|---|---|---|---|
| 1 | GHSA-rc42 | spring-boot 2.7.18 | HIGH | `EndpointRequest.to()` 在 actuator 端点未暴露时匹配错 | 2.7 线 OSS 止于 2.7.18；fix 在 3.3.x | 3.3.11（Java 17） |
| 2 | GHSA-wwpq | spring-boot 2.7.18 | HIGH | 可预测临时目录、未校验属主 | 同上；fix 在 3.5.x | 3.5.14（Java 17） |
| 3 | GHSA-mg83 | spring-security-crypto 5.7.12 | HIGH | 密码长度未强制 | 线内 fix 5.7.16 **Maven Central 未发布**（实测 404；5.7 OSS 止 5.7.14） | 6.3.8（Java 17） |
| 4 | GHSA-jmp9 | spring-core 5.3.39 | HIGH | 注解探测可导致越权 | 5.3 线 OSS 止于 5.3.39（`<=5.3.44` 只有商业版） | 6.2.11（Java 17） |
| 5 | GHSA-775g | spring-expression 5.3.39 | HIGH | SpEL 整数溢出 DoS | `<=5.3.39` 全线命中 | 6.0.0（Java 17） |
| 6 | GHSA-4wrc（CRITICAL） | spring-web 5.3.39 | CRITICAL | HttpInvoker 不安全反序列化（历史 CVE，6.0 移除此入口） | `<6.0.0` 全线命中，5.3 不修 | 6.0.0（Java 17） |
| 7 | GHSA-3chg | spring-webmvc 5.3.39 | HIGH | `JavaScriptUtils` XSS | `<=5.3.39` | 6.2.19（Java 17） |
| 8 | GHSA-g5vr | spring-webmvc 5.3.39 | HIGH | 路径穿越 | `<=5.3.40` | 6.1.14（Java 17） |
| 9 | GHSA-pc63（CRITICAL） | spring-webmvc 5.3.39 | CRITICAL | `XsltView` 路径限制不当 | `5.3.0–5.3.49` 全线命中 | 7.0.9（Java 17+） |

### 3.1 三个选项（需要 spencer 拍板）

- **α 加 Java 17 轨道（推荐，但要排期）**：新模块 `guanjin-spring-boot3`（jakarta、Boot 3.x、Java 17），
  `guanjin-core` 仍守 Java 8（Boot 2 与 Boot 3 双适配，pom 注释里本就是这个设计）。代价：新模块工作量在 M2 之后，
  且 **#136 的 Maven 扫描面要按模块定口径**（boot2 模块的 9 条是否仍算"SDK 债"）——这是 Gate/Spec 的口径问题，
  不归 SDK 单方面改。**在 α 落地前，9 条仍需白名单 + 到期日。**
- **β 风险接受 + 续期（最省，但要书面）**：9 条全部发生在**宿主应用代码路径**，SDK 自身不触达：
  不用 actuator 安全匹配（1）、不建临时目录（2）、不做口令认证（3）、不依赖 Spring Security 的注解探测做授权
  （SDK 的 `@RequirePermission` 是自己读 HandlerMethod，不经 security 注解扫描）（4）、不求值 SpEL（5）、
  不用 HttpInvoker（6）、不渲染视图（7/9）、不用静态资源/函数式路由（8）。处置 = 白名单续期 90 天 + 在 README
  写"宿主最低 Spring 版本"指引。**风险在宿主，不在 SDK 运行时**，但这是"接受"，不是"修好"，要 spencer 明文批。
- **γ 缩 SBOM 面（不建议单用）**：把 boot2 的 optional 依赖从 SBOM 里排除后再扫——等于把"宿主提供"变成
  "不扫"，会把真问题藏起来（和"401 静默放过"同类），只能作为 α/β 的辅助口径，不单用。

### 3.2 不做会怎样（逐条口径）

9 条在 2026-12-13 后维持白名单 = 每天都是"到期即红"的边缘；一旦快照/白名单任何一边过期，`vuln` job 红，
main 合并门禁停摆。真正的解法只有 α（换 Java 17 线）或 β（书面风险接受 + 宿主指引），拖延只会让账目越来越难看。

## 4. 执行顺序与时间表

| 步 | 内容 | 建议日期 | 硬约束 |
|---|---|---|---|
| 1 | A 批 PR（pom 8 模块 + 白名单删 24 行 + 复扫）合入 | **2026-11-18 前** | 越早越好，已验证零代码改动 |
| 2 | B 批决策（α / β）+ 白名单 9 行按决策改写到期日/理由 | **2026-11-27 前** | 需 spencer 拍板 |
| 3 | 若选 α：`guanjin-spring-boot3` 排入里程碑，B 批白名单挂到该里程碑 | M2 之后 | — |
| 4 | 若选 β：README 写宿主最低版本指引 + 到期日 ≤ 90 天 | 同 #2 | 不许"到期自动续期" |
| **最晚** | **必须已动手** | **2026-12-12** | **2026-12-13 白名单到期 = 全红** |

## 5. 待拍板（3 项）

1. **B 批走 α（Boot 3/Java 17 轨道）还是 β（风险接受+续期）**——涉及 M2 之后的里程碑与人力，只有 spencer 能定。
2. **#136 的扫描口径**：boot2 模块的 optional 依赖（宿主提供）算不算"SDK 债"，是否要按模块/scope 收敛（Gate+Spec）。
3. **A 批的合入时机**：本文已验证可一批做；放行后 1 个 PR 收口（含白名单删行，不拆两次）。

## 6. 与其它在途工作的关系

- 与 #51（Go 侧 D29）：零重叠（Java 依赖升级不动 Go）。
- 与 D23②/③：本方案就是 D23③ 第二段落地后的**真实债**；A 批合入后 `AC-ENG-07` 账目可把 Java 侧从
  "未覆盖"改记为"已覆盖（余 9 条白名单）"。
- 与 P2 常量清理（`JwksKey.ALG_SM3_WITH_SM2`）：可并在 A 批 PR 顺手做（同文件域、零引用）。

## 7. 复算命令（evidence）

```bash
# 1) 当前解析版本（任何一台有网的机器）
mvn -f sdk/java/pom.xml -q dependency:tree -DoutputFile=/tmp/tree.txt

# 2) 生成 SBOM 并让门禁引擎按 Maven 生态判（与 CI 同一条引擎）
mvn -f sdk/java/pom.xml -q org.cyclonedx:cyclonedx-maven-plugin:2.8.0:makeAggregateBom
python3 scripts/vuln-gate.py --root . --java-bom sdk/java/target/bom.json

# 3) 试跑 A 批（本方案的数据来源；改 pom 后跑）
mvn -f sdk/java/pom.xml -q test                                   # 单测 120
GUANJIN_TEST_REDIS_ADDR=127.0.0.1:<port> \
  mvn -f sdk/java/pom.xml -q verify -Pintegration                  # 集成 10
./scripts/check-sdk-java8.sh                                       # 三闸
```

## 8. 审查证据包（供 Owl 分级，2026-11-13）

> 目的：把审查所需的对账/自查全部备齐，Owl 只做分级判断。全部为本机亲跑（非转述）。

### 8.1 A 批 24 条逐条对账（#136 清单 × 离线快照 × 试跑实测）

| # | 坐标（当前） | 级别 | 公告 | 本线命中区间 | 本线最低安全版本 | A 批目标（tree 实测） | 试跑结果 |
|---|---|---|---|---|---|---|---|
| A1 | `ch.qos.logback:logback-classic@1.2.12` | HIGH | `GHSA-vmq6-5m68-f53m` | `>=0.0.0,<1.2.13` | 1.2.13（fix） | **1.2.13** | gate 实测不再命中 ✓ |
| A2 | `ch.qos.logback:logback-core@1.2.12` | HIGH | `GHSA-gm62-rw4g-vrc4` | `>=1.2.12,<1.2.13` | 1.2.13（fix） | **1.2.13** | gate 实测不再命中 ✓ |
| A3 | `ch.qos.logback:logback-core@1.2.12` | HIGH | `GHSA-vmq6-5m68-f53m` | `>=0.0.0,<1.2.13` | 1.2.13（fix） | **1.2.13** | gate 实测不再命中 ✓ |
| A4 | `com.fasterxml.jackson.core:jackson-core@2.13.5` | HIGH | `GHSA-7hhh-6rmp-j9qf` | `>=2.8.0,<2.18.11` | 2.18.11（fix） | **2.18.11** | gate 实测不再命中 ✓ |
| A5 | `com.fasterxml.jackson.core:jackson-core@2.13.5` | HIGH | `GHSA-h46c-h94j-95f3` | `>=0.0.0,<2.15.0` | 2.15.0（fix） | **2.18.11** | gate 实测不再命中 ✓ |
| A6 | `com.fasterxml.jackson.core:jackson-core@2.13.5` | HIGH | `GHSA-r7wm-3cxj-wff9` | `>=0.0.0,<2.18.8` | 2.18.8（fix） | **2.18.11** | gate 实测不再命中 ✓ |
| A7 | `com.fasterxml.jackson.core:jackson-databind@2.13.5` | HIGH | `GHSA-cxp5-3px4-pw24` | `>=2.5.0,<2.18.11` | 2.18.11（fix） | **2.18.11** | gate 实测不再命中 ✓ |
| A8 | `com.fasterxml.jackson.core:jackson-databind@2.13.5` | HIGH | `GHSA-j3rv-43j4-c7qm` | `>=2.10.0,<2.18.8` | 2.18.8（fix） | **2.18.11** | gate 实测不再命中 ✓ |
| A9 | `com.fasterxml.jackson.core:jackson-databind@2.13.5` | HIGH | `GHSA-wv8q-qhhj-9h54` | `>=2.0.0,<2.18.11` | 2.18.11（fix） | **2.18.11** | gate 实测不再命中 ✓ |
| A10 | `commons-io:commons-io@2.11.0` | HIGH | `GHSA-78wr-2p64-hpwj` | `>=2.0,<2.14.0` | 2.14.0（fix） | **2.14.0** | gate 实测不再命中 ✓ |
| A11 | `io.netty:netty-codec@4.1.101.Final` | HIGH | `GHSA-558v-64gr-wgg4` | `>=0.0.0,<4.1.136.Final` | 4.1.136.Final（fix） | **4.1.137.Final** | gate 实测不再命中 ✓ |
| A12 | `io.netty:netty-codec@4.1.101.Final` | HIGH | `GHSA-mj4r-2hfc-f8p6` | `>=0.0.0,<4.1.133.Final` | 4.1.133.Final（fix） | **4.1.137.Final** | gate 实测不再命中 ✓ |
| A13 | `io.netty:netty-handler@4.1.101.Final` | HIGH | `GHSA-3qp7-7mw8-wx86` | `>=0.0.0,<4.1.135.Final` | 4.1.135.Final（fix） | **4.1.137.Final** | gate 实测不再命中 ✓ |
| A14 | `io.netty:netty-handler@4.1.101.Final` | HIGH | `GHSA-4g8c-wm8x-jfhw` | `>=4.1.91.Final,<4.1.118.Final` | 4.1.118.Final（fix） | **4.1.137.Final** | gate 实测不再命中 ✓ |
| A15 | `io.netty:netty-handler@4.1.101.Final` | CRITICAL | `GHSA-c4c3-7fpv-j4q5` | `>=0.0.0,<4.1.137.Final` | 4.1.137.Final（fix） | **4.1.137.Final** | gate 实测不再命中 ✓ |
| A16 | `org.bouncycastle:bcprov-jdk18on@1.78.1` | CRITICAL | `GHSA-574f-3g2m-x479` | `>=1.59,<1.80.2` | 1.80.2（fix） | **1.85** | gate 实测不再命中 ✓ |
| A17 | `org.bouncycastle:bcprov-jdk18on@1.78.1` | CRITICAL | `GHSA-9pwp-9qqc-pr26` | `>=0.0.0,<1.85` | 1.85（fix） | **1.85** | gate 实测不再命中 ✓ |
| A18 | `org.bouncycastle:bcprov-jdk18on@1.78.1` | HIGH | `GHSA-p93r-85wp-75v3` | `>=1.71,<1.80.2` | 1.80.2（fix） | **1.85** | gate 实测不再命中 ✓ |
| A19 | `org.springframework.security:spring-security-core@5.7.11` | HIGH | `GHSA-f3jh-qvm4-mg39` | `>=0.0.0,<5.7.12` | 5.7.12（fix） | **5.7.12** | gate 实测不再命中 ✓ |
| A20 | `org.springframework:spring-web@5.3.31` | HIGH | `GHSA-2wrp-6fg6-hmc5` | `>=0.0.0,<5.3.34` | 5.3.34（fix） | **5.3.39** | gate 实测不再命中 ✓ |
| A21 | `org.springframework:spring-web@5.3.31` | HIGH | `GHSA-ccgv-vj62-xf9h` | `>=5.3.0,<5.3.32` | 5.3.32（fix） | **5.3.39** | gate 实测不再命中 ✓ |
| A22 | `org.springframework:spring-web@5.3.31` | HIGH | `GHSA-hgjh-9rj2-g67j` | `>=0.0.0,<5.3.33` | 5.3.33（fix） | **5.3.39** | gate 实测不再命中 ✓ |
| A23 | `org.yaml:snakeyaml@1.30` | HIGH | `GHSA-3mc7-4q67-w48m` | `>=0.0.0,<1.31` | 1.31（fix） | **2.0** | gate 实测不再命中 ✓ |
| A24 | `org.yaml:snakeyaml@1.30` | HIGH | `GHSA-mjmj-j48q-9wg2` | `>=0.0.0,<2.0` | 2.0（fix） | **2.0** | gate 实测不再命中 ✓ |

> A 批 = 24 条（与 #136 清单逐条对上；全部在试跑中不再命中）

| # | 坐标（当前） | 级别 | 公告 | 本线命中区间 | 本线最低安全版本 | 线内可得？ |
|---|---|---|---|---|---|---|
| B1 | `org.springframework.boot:spring-boot@2.7.18` | HIGH | `GHSA-rc42-6c7j-7h5r` | `>=0.0.0,<=2.7.24.2` | 2.7.24.2（last_affected） | 否（last_affected，线内无 fix） |
| B2 | `org.springframework.boot:spring-boot@2.7.18` | HIGH | `GHSA-wwpq-f5c3-7hvx` | `>=0.0.0,<=2.7.32` | 2.7.32（last_affected） | 否（last_affected，线内无 fix） |
| B3 | `org.springframework.security:spring-security-crypto@5.7.11` | HIGH | `GHSA-mg83-c7gq-rv5c` | `>=0.0.0,<5.7.16` | 5.7.16（fix） | 否（该版本未在 Central 发布） |
| B4 | `org.springframework:spring-core@5.3.31` | HIGH | `GHSA-jmp9-x22r-554x` | `>=5.3.0,<=5.3.44` | 5.3.44（last_affected） | 否（last_affected，线内无 fix） |
| B5 | `org.springframework:spring-expression@5.3.31` | HIGH | `GHSA-775g-4xr8-78h8` | `>=0.0.0,<=5.3.39` | 5.3.39（last_affected） | 否（last_affected，线内无 fix） |
| B6 | `org.springframework:spring-web@5.3.31` | CRITICAL | `GHSA-4wrc-f8pq-fpqp` | `>=0.0.0,<6.0.0` | 6.0.0（fix） | 否（fix 在更高 major 线） |
| B7 | `org.springframework:spring-webmvc@5.3.31` | HIGH | `GHSA-3chg-m5w7-qfv5` | `>=0.0.0,<=5.3.39` | 5.3.39（last_affected） | 否（last_affected，线内无 fix） |
| B8 | `org.springframework:spring-webmvc@5.3.31` | HIGH | `GHSA-g5vr-rgqm-vf78` | `>=0.0.0,<=5.3.40` | 5.3.40（last_affected） | 否（last_affected，线内无 fix） |
| B9 | `org.springframework:spring-webmvc@5.3.31` | CRITICAL | `GHSA-pc63-qcmh-9cmg` | `>=5.3.0,<=5.3.49` | 5.3.49（last_affected） | 否（last_affected，线内无 fix） |

> B 批 = 9 条（试跑后仍命中，全部需换线/Java 17）

**口径说明（三条取最大修正）**：
- `jackson` 3 条公告本线 fix 分别是 2.15.0/2.18.8/2.18.11 → 一次取 **2.18.11**；
- `bcprov` 本线 fix 1.80.2（两条）+ 1.85（`GHSA-9pwp`）→ 取 **1.85**；
- `netty` 本线 fix 4.1.118/4.1.133/4.1.135/4.1.136/4.1.137 → 取 **4.1.137.Final**；
- `spring-web` 本线 fix 5.3.32/5.3.33/5.3.34 → 取 **5.3.39**（5.3 线 OSS 最后一版，也是 B 批 `GHSA-775g`/`3chg` 的线内顶点）。
  `本线最低安全版本` 一列由离线快照的 `affected` 区间机算（`<X` → X=fix；`<=X` → last_affected），与 Gate 对账表逐条一致。

### 8.2 自查声明（试跑已回滚、树上无残留）

| 检查 | 命令 | 结果 |
|---|---|---|
| 试跑改动已回滚 | `git checkout -- sdk/java/pom.xml` | 已执行 |
| 未提交改动 | `git status --porcelain \| wc -l` | **0** |
| 已跟踪文件差异 | `git diff --stat \| wc -l` | **0** |
| 分支 HEAD | `git log --oneline -1` | `54c59dc docs(sdk/java): 依赖漏洞升级方案…`（只有方案文档） |
| 试跑产物 | `sdk/java/target/bom.json`、根目录 `.ev-*` | 全部为一次性证据，**未入库**（`target/` 本就 gitignore；`.ev-*` 已删） |

本 PR 只有 1 个文件（`sdk/java/SBOM-REMEDIATION-PLAN.md`）；A 批实施不在本 PR。

### 8.3 B 批 9 条逐条举证（"影响面落宿主、SDK 不触达"）

代码面统一证据：以下 grep 在 `sdk/java/guanjin-core/src/main` + `sdk/java/guanjin-spring-boot2/src/main` 上 **全部 0 命中**：

```bash
grep -rn -E 'EndpointRequest|actuator'                                   # → 0
grep -rn -E 'createTempFile|tmpdir|mkdtemp'                              # → 0
grep -rn -E 'PasswordEncoder|password'                                   # → 0
grep -rn -E 'SpelExpression|EvaluationContext|SpelParser'                # → 0
grep -rn -E 'HttpInvoker|RemoteInvocation|ObjectInputStream'             # → 0
grep -rn -E 'JavaScriptUtils'                                            # → 0
grep -rn -E 'addResourceHandlers|ResourceHandler'                        # → 0
grep -rn -E 'XSLT|XsltView|TransformerFactory'                           # → 0
grep -rn -E 'PreAuthorize|PostAuthorize|EnableMethodSecurity|@Secured'   # → 0
```

| 公告 | 影响面（快照 summary） | SDK 侧为什么落不到 | 宿主处置 |
|---|---|---|---|
| B1 `GHSA-rc42` (spring-boot) | `EndpointRequest.to()` 在 actuator 端点未暴露时匹配错 | SDK 不含 spring-boot-actuator（`mvn dependency:tree` 无此坐标）、不用 `EndpointRequest` 做安全匹配 | 宿主若用 actuator 安全匹配需自升 Boot 3 |
| B2 `GHSA-wwpq` (spring-boot) | 可预测临时目录且未校验属主 | SDK 不建临时目录/文件（grep 0 命中） | 宿主升级或收紧 tmpdir 权限 |
| B3 `GHSA-mg83` (spring-security-crypto) | 口令长度未强制 | SDK 无口令认证面（grep `PasswordEncoder|password` 0 命中）；`spring-security-core` 仅用于身份注入 | 宿主口令策略自担（5.7.16 未发布：Central 实测 **404**，5.7 OSS 止 5.7.14） |
| B4 `GHSA-jmp9` (spring-core) | 注解探测可致越权 | 授权执行点 `GuanjinAuthorizationInterceptor:38-40` 只做 `HandlerMethod.getMethodAnnotation(RequirePermission.class)` / `Class#getAnnotation` 的**存在性判定**；`HandlerMethod` 字节码无 `AnnotatedElementUtils`/`MergedAnnotations` 引用；SDK 不求值任何注解表达式（`@PreAuthorize*` grep 0 命中），权限结论来自 jcasbin `enforce` | 宿主方法级安全（Spring Security 注解）自行评估 |
| B5 `GHSA-775g` (spring-expression) | SpEL 整数溢出 DoS | SDK 不求值 SpEL（grep 0 命中） | 宿主如有表达式求值需升级 |
| B6 `GHSA-4wrc` (spring-web, CRITICAL) | HttpInvoker 不安全反序列化 | SDK 不用 HttpInvoker/`ObjectInputStream`（grep 0 命中）；该入口 6.0 已移除 | **宿主不得暴露 HttpInvoker 端点**（写进 README 宿主指引） |
| B7 `GHSA-3chg` (spring-webmvc) | `JavaScriptUtils` XSS | SDK 不渲染视图、不用 `JavaScriptUtils` | 宿主视图层自担 |
| B8 `GHSA-g5vr` (spring-webmvc) | 路径穿越 | SDK 不注册资源处理器/函数式路由（grep 0 命中） | 宿主静态资源/路由配置自担 |
| B9 `GHSA-pc63` (spring-webmvc, CRITICAL) | `XsltView` 路径限制不当 | SDK 不用 XSLT/`XsltView`（grep 0 命中） | 宿主不用 XSLT 视图即可规避 |

### 8.4 snakeyaml 2.0 归 A 批的实测记录（不是推断）

1. **字节码**：Boot 2.7.18 `OriginTrackedYamlLoader` 的 `createYaml` 签名为 `(LoaderOptions) → Yaml`，
   `OriginTrackingConstructor` 构造器吃 `LoaderOptions` —— 已是 snakeyaml 2.x API 形态。
2. **真跑 YAML 加载**（Owl 已独立复做）：
   ```bash
   mvn -o -pl sdk/java/guanjin-spring-boot2 dependency:build-classpath \
       -Dsnakeyaml.version=2.0 -Dmdep.outputFile=/tmp/cp.txt   # classpath 里确为 snakeyaml-2.0.jar
   java -cp "$(cat /tmp/cp.txt):sdk/java/guanjin-spring-boot2/target/classes" <probe>
   # probe: new YamlPropertySourceLoader().load("p", new ByteArrayResource(yml))
   # 输出：{guanjin.platform.base-url=http://127.0.0.1:8080, guanjin.enabled=true}
   ```
3. **全量回归**：试跑 `mvn verify -Pintegration` 全绿（单测 120 + 集成 10）。
→ 结论：`GHSA-3mc7`（fix 1.31）+ `GHSA-mjmj`（fix 2.0）两条都在 A 批清掉。

### 8.5 对账差异说明（Owl 指出的两处换边）

| 条目 | 白名单 reason 桶 | 本方案归属 | 依据 |
|---|---|---|---|
| `GHSA-mg83`（security-crypto） | 「同 line 小跳」 | **B 批** | 线内 fix 5.7.16 **Maven Central 未发布**（实测 404；5.7 OSS 止 5.7.14）→ 线内不可达 |
| `GHSA-mjmj`（snakeyaml） | 「跨 major」 | **A 批** | snakeyaml 2.0 **Java 8 兼容 + Boot 2.7.18 实测可用**（§8.4）→ 与「跨 major 必撞 Java 17」不是一回事 |

### 8.6 复算命令

```bash
mvn -f sdk/java/pom.xml -q test                                  # 120/120
GUANJIN_TEST_REDIS_ADDR=127.0.0.1:<port> mvn -f sdk/java/pom.xml -q verify -Pintegration  # 10/10
mvn -f sdk/java/pom.xml -q org.cyclonedx:cyclonedx-maven-plugin:2.8.0:makeAggregateBom
python3 scripts/vuln-gate.py --root . --java-bom sdk/java/target/bom.json   # 试跑后：命中 9、无新增阻断；白名单 24 行 stale → 按规则删行后即绿
```

**试跑后 gate 的退出码说明**：试跑后 `vuln-gate.py` 退出 1，原因是白名单里 24 行"本次不再命中"（规则：不许只增不减），
不是出现新漏洞——摘要行明确 `阻断 0 / 白名单 9 / 仅提示 20`。A 批 PR 里删掉这 24 行即转绿。

## 附：试跑留痕（2026-11-13）

- 试跑提交：无（pom 改动已 `git checkout --` 回滚，本文只留结论；避免"方案 PR 夹带实施"）。
- 试跑结果：`mvn verify -Pintegration` **BUILD SUCCESS**，单测 120 + 集成 10 全绿；gate 复算 33→9，无新增阻断级。
- 附注：升级到 5.3.39 后新增的 MODERATE/LOW 命中（logback-core 5 条、spring-context/webmvc 数条）不阻断，
  但说明 **5.3.39 之后 OSS 不再发版**，这些条目长期会累积——这也是 α 选项的战略理由。
