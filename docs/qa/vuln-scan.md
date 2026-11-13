# 依赖漏洞门禁（AC-ENG-07）

> 判据原文（`docs/acceptance-criteria.md` §AC-ENG-07）：**govulncheck / trivy 必须真跑**，发现 **HIGH/CRITICAL** 判红。
> 实现形态的依据：**`docs/decisions.md` D28**（CI 零网络 → 改「离线快照 + 本地判定」；PASS 口径见 §5）。
>
> 落地物：`scripts/check-vuln.sh`（入口）、`scripts/vuln-gate.py`（判定引擎，纯 python3 标准库）、
> `docs/qa/vuln-advisories.tsv`（离线漏洞快照）、`docs/qa/vuln-allowlist.tsv`（白名单）、
> `scripts/vuln-snapshot-refresh.py`（快照生产者，**需外网、不在 CI 跑**）、CI job **`vuln`**（零网络）。

## 1. 为什么要"离线快照"，而不是 CI 里现跑 govulncheck / trivy

`ci.yml` 的 runner 解析不到外网源（#125：`apt-get` 退避 4 分钟后 `exit 100`，后续 13 个 step 全 skipped）。
govulncheck 要 `vuln.go.dev`、trivy 要 `ghcr.io` 的库；**在这个 runner 上它们跑不起来**，
而"跑不起来就跳过 / 只警告"等于把门禁关掉（`skipped` 不是绿，`|| true` 更不是）。

所以口径改为三句话：

1. **数据离线入库**：`docs/qa/vuln-advisories.tsv` 是唯一数据源，**PR 里是一行一行的 diff**（可评审、可复算）；
2. **判定在本地做**：`scripts/vuln-gate.py` 只读 `go.mod` / `web/package-lock.json` + 快照，零网络、毫秒级；
3. **数据与快照的"新鲜度"是判据的一部分**（三条，缺一条就会出现假绿）：
   - `generated_at` 超过 `MAX_AGE_DAYS`（默认 **90** 天）→ 红；写在未来日期 → 红；
   - **`coords_sha256`**：依赖**包名**集合的指纹（不含版本）。新增/换掉一个包名而没重刷快照 → 红
     （否则"这个坐标从没被 OSV 查过"，而整体 90 天计数器还没到期 = 假绿）。
     只对**包名**取指纹的理由：OSV 按包名查、区间在本地算，单纯升版本不需要重刷；
   - 快照必须覆盖**当前依赖里出现的每个生态**（Go 有坐标但快照里 0 条 Go 公告 → 红）。

> **没有降阈值**：Go 侧是**命中即红**（比"按 CVSS 分级"更严）；npm / Maven 侧照判据原文用 HIGH/CRITICAL，
> 且 **`severity` 缺失（`-`）一律阻断**（缺级别 ≠ 低危：`MAL-*` 恶意包公告常常没有 severity）。
> 扫描面（`go.mod` 全部坐标 + `web/package-lock.json` 全部包 + `sdk/java` 的 CycloneDX 聚合 BOM 全部第三方组件
> = **176 个坐标**；本地不传 `--java-bom` 时为 125 个 + 显式 `[UNCOVERED]`，见 §5）。

## 2. 判定口径（写死在引擎里，不许脚本外临时放宽）

| 生态 | 阻断阈值 | 理由 |
|---|---|---|
| **Go**（`go.mod`，含 indirect） | **命中即红**（含 `severity` 为 `-` 的官方 OSV 条目） | Go 官方 OSV 不带 CVSS（分数不可靠），且原方案的口径本就是"可达即红"。离线数据没有符号级可达性，只能按**坐标命中**判 → **更宽，只会多报不会漏报** |
| **npm**（`web/package-lock.json`） | **HIGH / CRITICAL 判红**；MODERATE / LOW 打 `[INFO]` 并计数；**缺失/未知级别一律阻断**（含 `MAL-*`） | 判据原文阈值；缺级别不能当低危 |

**fail-closed 清单**（任一条命中即 `exit 1`）：

| 场景 | 结果 |
|---|---|
| 有未白名单的阻断级命中 | **红**（点名 `文件:行号` + 模块@版本 + 命中区间 + 公告 id + 级别） |
| 快照缺失 / 无 `generated_at` / 已过期 / 日期在未来 | **红**（没有数据 ≠ 没有漏洞） |
| 快照**0 行公告** | **红**（"扫了个空表"不是绿） |
| 快照**缺某个生态**（依赖里有、快照里没有） | **红**（该生态等于没扫） |
| 快照 `coords_sha256` 缺失 / 与当前依赖包名集合不一致 | **红**（新增依赖没重刷快照 = 假绿） |
| 快照行不合法（列数、区间表达式、生态未知） | **红**（数据坏了等于没扫） |
| 依赖坐标 0 个（`go.mod` 无 require / 锁文件无 `packages` / `lockfileVersion < 2`） | **红**（未验证 = 红） |
| `go.mod` / `package-lock.json` 里的版本号不可解析，或 lockfile 文本扫描与 JSON 条数不一致 | **红** |
| 白名单行不合法（非 5 列 / 理由空 / 到期日非法 / **到期日 > 180 天**） | **红**（不允许"无理由豁免"，也不允许"事实上的永久豁免"） |
| 白名单条目**已过期** | **红**（到期即重新判红） |
| 白名单条目**本次不再命中** | **红**（必须删行，白名单不许只增不减） |

**不许**：`continue-on-error`、`|| true`、把 `severity` 阈值调低、整类 ignore（如"忽略所有 devDependencies"）。

## 3. 快照怎么更新（有网环境，人工触发）

```bash
# 需要外网（api.osv.dev）。CI 里不跑这一步。
python3 scripts/vuln-snapshot-refresh.py            # 覆盖 docs/qa/vuln-advisories.tsv
python3 scripts/vuln-snapshot-refresh.py --stdout    # 只打不写（评审对比用）
python3 scripts/vuln-snapshot-refresh.py --osv http://<内网镜像>   # 内网镜像
```

- **节奏**：**动了依赖（`go.mod` / `web/package-lock.json`）就必刷**（否则坐标指纹对不上，门禁自己会红）；
  无依赖变更时每 30~60 天刷一次（90 天硬红线）。
- **评审**：刷新是一次普通提交，diff 里能看清"哪些公告新进、哪些区间变了、哪些消失了"。
- **`dropped` 不为 0 就是失败**：区间里出现不可比较的版本（如 `9.6.0b1`）会被丢弃并写进快照头
  `# dropped: N`，此时刷新脚本 **exit 1**、门禁**判红**——宁可让人复核，也不发布半份数据。
  （`SEMVER` 与 npm 风格 `ECOSYSTEM` 区间同时存在时取 `SEMVER`，这是权威区间。）
- **为什么不入库整份 OSV DB**：整库体积过大且绝大多数与本仓坐标无关；本快照**只收本仓坐标**的公告
  （Go 18 个模块 + npm 107 个包 + Maven 51 个组件 = **176 个坐标** → **602 行 / 279 个公告**，
  以快照头 `# coordinates:` / `# entries:` 为准；本地不传 `--java-bom` 时**只判其中 125 个坐标**（快照文件本身仍是 176 坐标的全集）。
- **代价（写清，不藏）**：依赖坐标集合变了就必须重刷——这条由 `coords_sha256` 机器强制，不靠人记得。

## 4. 白名单（当前 **33 条**，全部 Maven，到期 2026-12-13）

`docs/qa/vuln-allowlist.tsv`，5 列 TAB 分隔：`id / ecosystem / module / reason / expires`。

**当前 33 条，全部是 Maven（`sdk/java`）**，到期日统一 **2026-12-13**（登记日 2026-11-13，30 天）：
`sdk/java` 首次经 CycloneDX BOM 入闸（#136，squash 合入 `2df9879`，D23③②）一次性暴露的**阻断级真实命中**
（同一日志另有 **34 条低于阻断阈值**、只打 INFO 的命中：**LOW 7 / MODERATE 27**，不进本文件）——**不是**放宽阈值或整类豁免换来的绿；
CRITICAL **5** / HIGH **28**。分族与责任人见 §5 末（**A 12 / B 11 / C 10**，2026-11-13 修正，旧账 24/9 作废）。
到期由 `scripts/vuln-gate.py` **自动重新判红**，逾期未升级则 `AC-ENG-07` 降级。

> **汇总行怎么读**：`[OK] … 白名单 N` 里的 **N = 本次被放行的命中条数（`len(allowed)`）**，不是"文件里有几行"。
> 本地不传 `--java-bom` 时 Maven 生态不判 → 该 N 必为 **0**（33 条一行都没参与），**不能**当作"白名单已清空"的证据。

**Go / npm 侧 0 条**：`#127`（D23②）等地把这两生态的命中项**全部真修掉**（vue 3.5.45 / vite 6.4.6 随 #127；go-redis 更早），清单留作口径先例：

| 依赖 | 原状态 | 处置 |
|---|---|---|
| `github.com/redis/go-redis/v9` | `v9.7.0`（命中 GO-2025-3540 / GHSA-92cp-5422-2mw7） | 升 **`v9.7.3`** |
| `vue` | `3.5.13`（`@vue/server-renderer` 命中 GHSA-g2v6-rqmx-r4w6 HIGH） | 升 **`3.5.45`**（D23② 的"升，不留豁免"） |
| `vite` | `6.0.7`（命中 2 条 HIGH：GHSA-587h-7wmj-h5pm / GHSA-p9ff-h696-f583） | 升 **`6.4.6`** |

**Go 标准库条目恒为 0 条**（D23①：stdlib 不许进白名单，只能升 Go）——不是"没有 stdlib 漏洞"，
而是本闸的扫描面不含 stdlib（见 §5 末行），别把它读成"Go 面全绿"。

新增条目必须写清「为什么现在不能修」+ 谁在什么期限内修，到期日 **≤180 天**（超过即红）。

## 5. 覆盖面与未覆盖（**不得**对外声称"全量覆盖"）

| 面 | 状态 |
|---|---|
| `go.mod`（含 indirect） | **覆盖**（18 个坐标） |
| `web/package-lock.json` | **覆盖**（107 个包，逐包 `文件:行号`） |
| `sdk/java` | **覆盖**（D23③②，2026-11-13 起）：CI `vuln` job 消费 `sdk` job 产出的 **CycloneDX 1.5 聚合 BOM** artifact（`--java-bom`），51 个第三方组件（`com.guanjin:*` 自有模块排除，两端规则逐字一致）；**不解析 pom**（要外网、实测 429），BOM 每次构建重生成、artifact 锚定本次 run（`bom-commit.txt` 逐字节核对，缺失或不符 = 红）。**覆盖边界：只含 compile/runtime 作用域组件**——cyclonedx 插件 `includeTestScope` 默认 false（pom 未打开），test 依赖（junit-jupiter、spring-boot-starter-test 及其传递链）**不入闸**（它们不进发布物；要覆盖 test 面属新判据，须另行立项）。本地**不传** `--java-bom` 时该生态不进判定，但**显式打** `[UNCOVERED] sdk/java：未提供 --java-bom …`（不许静默跳过）；`make gates` 的本地口径即此，且此时 **33 条 Maven 白名单条目完全不参与判定（汇总行 `白名单 0`）** |
| `go.mod` 里的**本地路径** `replace` | **不覆盖**，每次运行显式打 `[UNCOVERED] go.mod:<行号> …` |
| **Go 标准库** | **不覆盖**（快照只收 `go.mod` 的第三方坐标、引擎不读 `go` 指令；`grep -c stdlib docs/qa/vuln-advisories.tsv` = 0）→ D23① 的 10 条 stdlib 公告（修复只在 go1.26.x）在本闸**不产生输出**，**"阻断 0" ≠ "Go 面全绿"**。**D28⑤ 已于 2026-11-13 走「升 Go」路径收口**（`go.mod` = `1.26.10`，#133 `6cff27c`；CI `go-version-file: go.mod` **精确**装 `1.26.10`，实跑 `go version go1.26.10`）——那 10 条公告**不再适用**，所以 stdlib 白名单条目 = 0 是"没有需要豁免的公告"，**不是**"扫过且干净"。**结构性边界不因收口消失**：①扫描面仍不含 stdlib，**未来新增的 stdlib 公告本闸不会发现**；②`go.mod` 钉的是**精确补丁版**（`1.26.10`），`setup-go` 只装这一个版本、**不会自动跟后续 1.26.x 补丁**，跟上要靠人改 `go.mod`（这本身是条维护红线，不是门禁能力）；③也没有白名单能承载 stdlib（D23① 禁止）。这条边界须在 `AC-ENG-07` 账目里单列，不得记成"Go 面永久无债" |

**`AC-ENG-07` 的 PASS 口径（D23③② 落地后）** = **三生态**（`go.mod` + `web/package-lock.json` + `sdk/java` 的 CycloneDX BOM）
依赖坐标无未白名单的阻断级漏洞 **且** CI `vuln` job 的 SBOM artifact 存在、锚定本次 commit。
本地口径（不传 `--java-bom`，如 `make gates`）= Go+npm 两生态判净 **且** `[UNCOVERED] sdk/java` 显式在案——
不得记成"全量覆盖"；CI 日志里的 `[OK] …（Go X / npm Y / Maven Z）` 行才是全量证据。

**Maven 版本语义（引擎里写死，保守近似——只许误红不许漏报）**：`.RELEASE`/`.Final`/`.GA` 尾缀=发布版本身（剥掉比）；
`.SEC<n>`/`.SP<n>`/`.SR<n>`=发布后补丁（剥掉后**末段 micro +n**，如 5.3.39.SEC03 ⇒ 5.3.39.3、2.5.7.SR5 ⇒ 2.5.7.5，
SR0<SR5<SR023 序与 Maven 一致）；`.RC<n>`/`.M<n>`/`.Alpha…`/`-jre` 等=预发布（< 同数字发布版），
预发布**序号数值感知**比较（RC10 > RC2，不按字典序）；四段数字（2.13.4.2）按 (maj,min,patch,micro) 比；
五段以上不可解析 → 红（fail-closed）。**残余边界（已钉）**：①"未知尾缀 vs 纯发布版"的相对序是近似；
②`.SEC/.SP/.SR`（micro+n）与**四段数字**上界混比会错序（`5.3.39.SEC03` 对 `<5.3.39.1` 判不命中）——
需"SEC 形态坐标 + 四段数字上界"同公告同时出现，当前快照 389 个 Maven 边界为 0；跨形态公告若出现，
refresh 端解析不了即丢弃 exit 1（人工复核），不会静默落库。
快照头除全集 `coords_sha256` 外另有 `coords_sha256_go/npm/maven` 分生态指纹：本地子集运行（无 BOM）按生态核对，
CI 全生态运行查全集指纹（口径没有变松）。

**Maven 白名单现状（33 条，到期 2026-12-13，30 天）**：`sdk/java` 首次入闸一次性暴露的真实命中
（bcprov 1.78.1、netty 4.1.101.Final、jackson 2.13.5、spring 5.3.31/2.7.18 系、snakeyaml 1.30、logback 1.2.12、commons-io 2.11.0），
**CRITICAL 5 条**（bcprov ×2：GHSA-9pwp-9qqc-pr26/GHSA-574f-3g2m-x479；spring-web GHSA-4wrc-f8pq-fpqp；
spring-webmvc GHSA-pc63-qcmh-9cmg；netty-handler GHSA-c4c3-7fpv-j4q5）。这些是**权宜账**，必须能回答"到底谁能修、怎么修"。

**分族口径（2026-11-13 修正，可复算）**：对每条公告取该模块在快照里的**全部受影响区间**，
到 Maven Central `maven-metadata.xml` 找「**已发布**且**不在任一区间**」的**最小**版本作目标，
用本仓 `vuln-gate.py` 的 `vparse/vcmp/vmatch` 判（口径与引擎一致），再与现装版本的 major/minor 比：

| 族 | 条数 | 判据 | 谁修 |
|---|---:|---|---|
| **A** 同 minor 线补丁已发布 | **12** | 目标与现装同 `major.minor`（如 spring-web 5.3.31→**5.3.34**、logback 1.2.12→**1.2.13**、netty 4.1.101.Final→**4.1.137.Final**、spring-security-core 5.7.11→**5.7.12**） | **Nova**：一行 `<properties>` 版本覆盖 + 回归 |
| **B** 同 major 内需升 minor | **11** | 目标同 major、跨 minor（jackson 2.13.5→**2.15.0/2.18.8/2.18.11**、bcprov 1.78.1→**1.80.2/1.85**、commons-io 2.11.0→**2.14.0**、snakeyaml 1.30→**1.31**） | **Nova**：升 minor + 回归（Jackson 跨 5 个 minor，需跑 SDK 全测） |
| **C** 同 major 内无干净版本（须跨 major） | **10** | 该模块在现 major 内**没有任何已发布版本**能清掉本公告全部区间 → 目标落在更高 major（spring-web/webmvc/spring-expression/spring-core→**6.x**、spring-security-crypto→**6.3.8**、snakeyaml→**2.0**、spring-boot 2.7.18→**3.0.0**） | **Nova+Spec**：需 Boot 3 / 契约同步，排期并评估 SDK 兼容 |

> **旧账 24/9 作废（记错，纠正）**：那版按"fix 上界与组件同 major"粗判，**没核该版本是否真的发布过**。
> 典型反例：`spring-security-crypto` 5.7 线要求 5.7.16，而 5.7 线止于 **5.7.14**、5.8 线止于 **5.8.16**（<5.8.18），
> 真正干净的只有 **6.3.8** → 属 C 族，不是"小跳"。每条的具体目标版本已写进白名单 `reason` 列，`grep '→'` 可看。
> 到期即重判红；升级落地后"不再命中必须删行"的收紧规则不变。

## 6. 与「国密/算法扫描门禁」的边界（不重叠）

| | `scripts/check-guomi-algorithms.sh`（AC-AUD-12⑤） | `scripts/check-vuln.sh`（AC-ENG-07） |
|---|---|---|
| 判什么 | **源码里出现的算法标识/调用**（`SHA-1/256`、`RS256`、`MessageDigest`…） | **依赖坐标上的漏洞**（`GO-*` / `CVE-*` / `GHSA-*` / `MAL-*`） |
| 输入 | `.go/.java/.ts/.js/.vue` 的代码行（剥注释） | `go.mod` / `web/package-lock.json` + 离线快照 |
| 典型红 | 测试文件里写了裸词 `RS256` | 某个库爆 CVE（如 go-redis GO-2025-3540） |
| 输出是否互相消费 | **否** | **否** |

一句话：**"源码里写没写 SHA/RSA"归国密闸，"用的库有没有洞"归本闸。**

## 7. 本地怎么跑 / 怎么复算

```bash
./scripts/check-vuln.sh              # 判本仓（Go + npm；sdk/java 显式 [UNCOVERED]；零网络）
./scripts/check-vuln.sh --java-bom sdk/java/target/bom.json   # 三生态口径（与 CI vuln job 同一条命令）
./scripts/check-vuln.sh --self-test  # 正控/负控 37 项（临时夹具注入，不动工作树、不联网）
make gates                           # 上面已并入工程红线门禁（本地口径）
```

自检 37 项覆盖：Go HIGH 判红并点位、修复版判绿（版本敏感）、Go 无 CVSS 也阻断、npm HIGH 判红并点位、
npm MODERATE 只提示、npm 缺级别（`MAL-*`）阻断、白名单"未过期绿 / 过期红 / 不再命中红 / 到期日过远红 / 格式坏行红"、
快照"0 行红 / 缺生态红 / 过期红 / 未来日期红 / 坏行红 / 缺失红"、坐标指纹"不符红 / 缺失红"、
依赖坐标 0（Go / npm）红、依赖版本不可解析红、本地路径 replace 打 `[UNCOVERED]`。
自检还覆盖「缺 `# dropped:` 头 → 红」（与 `coords_sha256` 对称）。
**Maven/BOM 13 项**（D23③②）：HIGH 命中红并点名 `bom.json:<组件序号>`、修复版绿（版本敏感）、
无 BOM → 显式 `[UNCOVERED] sdk/java`、BOM 文件缺失红、BOM 坏 JSON 红、BOM 0 组件红、
全是自有模块红（0 第三方坐标）、MODERATE 只提示、缺级别（`MAL-*`）红、四段微版本区间命中红、
无 BOM 时 Maven 白名单条目不误判"不再命中"、SR 发布后补丁区间命中红、预发布序号数值感知（RC10>RC2）命中红。
CI 侧断言"逐项 `[OK]` 行数 **恰为 37**"（删用例让自己变绿会被抓；加用例要同步改这个数字）。

**快照防篡改的边界（写清，别误会）**：引擎只判**结构性缺失**（0 行 / 缺生态 / 指纹不符 / 缺头部字段）。
"快照里被删掉一行公告"引擎看不出来——**内容的增减只由 PR diff 保证**（这也是把数据入库、让评审能逐行看的原因）。
想查"某条公告还在不在"，看 `git log -p docs/qa/vuln-advisories.tsv`，不要指望门禁。
