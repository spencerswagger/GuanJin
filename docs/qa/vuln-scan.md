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

> **没有降阈值**：Go 侧是**命中即红**（比"按 CVSS 分级"更严）；npm 侧照判据原文用 HIGH/CRITICAL，
> 且 **`severity` 缺失（`-`）一律阻断**（缺级别 ≠ 低危：`MAL-*` 恶意包公告常常没有 severity）。
> 扫描面（`go.mod` 全部坐标 + `web/package-lock.json` 全部包 = **125 个坐标**）与原方案一致；
> `sdk/java` 仍未覆盖——但**显式打印** `[UNCOVERED]`（见 §5，D23③）。

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
  （Go 18 个模块 + npm 107 个包 = 125 个坐标 → **185 行 / 72 个公告**）。
- **代价（写清，不藏）**：依赖坐标集合变了就必须重刷——这条由 `coords_sha256` 机器强制，不靠人记得。

## 4. 白名单（当前 **0 条**）

`docs/qa/vuln-allowlist.tsv`，5 列 TAB 分隔：`id / ecosystem / module / reason / expires`。

**当前 0 条**：本轮把命中项**全部真修掉了**，没有留豁免：

| 依赖 | 原状态 | 处置 |
|---|---|---|
| `github.com/redis/go-redis/v9` | `v9.7.0`（命中 GO-2025-3540 / GHSA-92cp-5422-2mw7） | 升 **`v9.7.3`** |
| `vue` | `3.5.13`（`@vue/server-renderer` 命中 GHSA-g2v6-rqmx-r4w6 HIGH） | 升 **`3.5.45`**（D23② 的"升，不留豁免"） |
| `vite` | `6.0.7`（命中 2 条 HIGH：GHSA-587h-7wmj-h5pm / GHSA-p9ff-h696-f583） | 升 **`6.4.6`** |

新增条目必须写清「为什么现在不能修」+ 谁在什么期限内修，到期日 **≤180 天**（超过即红）。

## 5. 覆盖面与未覆盖（**不得**对外声称"全量覆盖"）

| 面 | 状态 |
|---|---|
| `go.mod`（含 indirect） | **覆盖**（18 个坐标） |
| `web/package-lock.json` | **覆盖**（107 个包，逐包 `文件:行号`） |
| `go.mod` 里的**本地路径** `replace` | **不覆盖**，每次运行显式打 `[UNCOVERED] go.mod:<行号> …` |
| `sdk/java` | **不覆盖**，每次运行显式打 `[UNCOVERED] sdk/java：pom 解析需外网（实测 429），本闸不覆盖——见 decisions.md D23③` |
| **Go 标准库** | **不覆盖**（快照只收 `go.mod` 的第三方坐标、引擎不读 `go` 指令；`grep -c stdlib docs/qa/vuln-advisories.tsv` = 0）→ D23① 的 10 条 stdlib 公告（修复只在 go1.26.x）在本闸**不产生输出**，**"阻断 0" ≠ "Go 面全绿"**；收口见 `decisions.md` **D28⑤**（升 Go 1.26 或快照加 `stdlib` 行，同一期限 2026-12-10） |

**`AC-ENG-07` 的 PASS 口径（`decisions.md` D28② 钉住；记账必须同时照抄 D28⑤ 的覆盖边界）** = `go.mod` + `web/package-lock.json` 两个生态的依赖坐标
无未白名单的阻断级漏洞 **且** `sdk/java` 未覆盖被显式标注。账目里**不得**写成"全量覆盖"；
2026-12-10 前未上线离线 SBOM（Nova 产 CycloneDX → 同一判定引擎消费，D28③）则降级为"Java 侧未覆盖"。

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
./scripts/check-vuln.sh              # 判本仓（与 CI 同一条命令；零网络）
./scripts/check-vuln.sh --self-test  # 正控/负控 24 项（临时夹具注入，不动工作树、不联网）
make gates                           # 上面这条已并入工程红线门禁
```

自检 24 项覆盖：Go HIGH 判红并点位、修复版判绿（版本敏感）、Go 无 CVSS 也阻断、npm HIGH 判红并点位、
npm MODERATE 只提示、npm 缺级别（`MAL-*`）阻断、白名单"未过期绿 / 过期红 / 不再命中红 / 到期日过远红 / 格式坏行红"、
快照"0 行红 / 缺生态红 / 过期红 / 未来日期红 / 坏行红 / 缺失红"、坐标指纹"不符红 / 缺失红"、
依赖坐标 0（Go / npm）红、依赖版本不可解析红、本地路径 replace 打 `[UNCOVERED]`。
自检还覆盖「缺 `# dropped:` 头 → 红」（与 `coords_sha256` 对称）。
CI 侧断言"逐项 `[OK]` 行数 **恰为 24**"（删用例让自己变绿会被抓；加用例要同步改这个数字）。

**快照防篡改的边界（写清，别误会）**：引擎只判**结构性缺失**（0 行 / 缺生态 / 指纹不符 / 缺头部字段）。
"快照里被删掉一行公告"引擎看不出来——**内容的增减只由 PR diff 保证**（这也是把数据入库、让评审能逐行看的原因）。
想查"某条公告还在不在"，看 `git log -p docs/qa/vuln-advisories.tsv`，不要指望门禁。
