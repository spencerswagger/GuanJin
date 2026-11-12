# 依赖漏洞门禁（AC-ENG-07）

> 判据原文（`docs/acceptance-criteria.md` §AC-ENG-07）：**govulncheck / trivy 必须真跑**，发现 **HIGH/CRITICAL** 判红。
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
3. **数据新鲜度是判据的一部分**：快照 `generated_at` 超过 `MAX_AGE_DAYS`（默认 **90** 天）→ **判红**。
   "库旧等于没扫"这条没有放松，只是把"每次现拉"换成"定期刷新 + 过期即红"。

> **没有降阈值**：Go 侧是**命中即红**（比"按 CVSS 分级"更严）；npm 侧照判据原文用 HIGH/CRITICAL。
> 扫描面（`go.mod` 全部坐标 + `web/package-lock.json` 全部包）与 `trivy fs` 的原方案一致，
> `sdk/java` 仍未覆盖——但**显式打印** `[UNCOVERED]`（见 §5，D23③）。

## 2. 判定口径（写死在引擎里，不许脚本外临时放宽）

| 生态 | 阻断阈值 | 理由 |
|---|---|---|
| **Go**（`go.mod`，含 indirect） | **命中即红**（含 `severity` 为 `-` 的官方 OSV 条目） | Go 官方 OSV 不带 CVSS（分数不可靠），且原方案的口径本就是"可达即红"。离线数据没有符号级可达性，只能按**坐标命中**判 → **更宽，只会多报不会漏报** |
| **npm**（`web/package-lock.json`） | **HIGH / CRITICAL 判红**；MODERATE / LOW 打 `[INFO]` 并计数 | 判据原文阈值；非阻断项仍逐条打印 `文件:行号`，只是不拦合入 |

**fail-closed 清单**（任一条命中即 `exit 1`）：

| 场景 | 结果 |
|---|---|
| 有未白名单的阻断级命中 | **红**（点名 `文件:行号` + 模块@版本 + 命中区间 + 公告 id + 级别） |
| 快照缺失 / 无 `generated_at` 头 / 已过期 | **红**（没有数据 ≠ 没有漏洞） |
| 快照行不合法（列数、区间表达式、生态未知） | **红**（数据坏了等于没扫） |
| `go.mod` / `package-lock.json` 里的版本号不可解析，或 lockfile 文本扫描与 JSON 条数不一致 | **红**（未验证 = 红） |
| 白名单行不合法（非 5 列 / 理由空 / 到期日非法） | **红**（不允许"无理由豁免"） |
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

- **节奏**：随依赖变更随时刷；无依赖变更时**每 30~60 天刷一次**（快照到 90 天会自动判红，红起来的第一步就是刷它）。
- **评审**：刷新是一次普通提交，diff 里能看清"哪些公告新进、哪些区间变了、哪些消失了"。
- **为什么不入库整份 OSV DB**：整库体积过大且 99% 与本仓坐标无关；本快照**只收本仓坐标**的公告
  （Go 18 个模块 + npm 107 个包 = 121 个坐标 → 182 行 / 71 个公告）。
- **代价（写清，不藏）**：新增依赖后，如果没人跑刷新，新依赖不会立刻出现在快照里。缓解是
  §2 的过期即红 + 本文件 + PR 模板式要求"动了依赖请顺手刷快照"（见 `notes/98-vuln-gate-2026-11-12.md`）。

## 4. 白名单（当前 **0 条**）

`docs/qa/vuln-allowlist.tsv`，5 列 TAB 分隔：`id / ecosystem / module / reason / expires`。

**当前 0 条**：本轮把命中项**全部真修掉了**，没有留豁免：

| 依赖 | 原状态 | 处置 |
|---|---|---|
| `github.com/redis/go-redis/v9` | `v9.7.0`（命中 GO-2025-3540 / GHSA-92cp-5422-2mw7） | 升 **`v9.7.3`** |
| `vue` | `3.5.13`（`@vue/server-renderer` 命中 GHSA-g2v6-rqmx-r4w6 HIGH） | 升 **`3.5.45`**（D23② 的"升，不留豁免"） |
| `vite` | `6.0.7`（命中 2 条 HIGH：GHSA-587h-7wmj-h5pm / GHSA-p9ff-h696-f583） | 升 **`6.4.6`** |

新增条目必须写清「为什么现在不能修」+ 谁在什么期限内修，并给 ≤30 天的到期日。

## 5. 覆盖面与未覆盖（**不得**对外声称"全量覆盖"）

| 面 | 状态 |
|---|---|
| `go.mod`（含 indirect） | **覆盖**（18 个坐标） |
| `web/package-lock.json` | **覆盖**（107 个包，逐包 `文件:行号`） |
| `go.mod` 里的**本地路径** `replace` | **不覆盖**，每次运行显式打 `[UNCOVERED] go.mod:<行号> …` |
| `sdk/java` | **不覆盖**，每次运行显式打 `[UNCOVERED] sdk/java：pom 解析需外网（实测 429），本闸不覆盖——见 decisions.md D23③` |

**`AC-ENG-07` 的 PASS 口径（照 D23③ 钉住）** = `go.mod` + `web/package-lock.json` 两个生态的依赖坐标
无未白名单的阻断级漏洞 **且** `sdk/java` 未覆盖被显式标注。账目里**不得**写成"全量覆盖"；
2026-12-10 前未上线离线 SBOM（Nova 产 → Gate 判）则降级为"Java 侧未覆盖"。

## 6. 与「国密/算法扫描门禁」的边界（不重叠）

| | `scripts/check-guomi-algorithms.sh`（AC-AUD-12⑤） | `scripts/check-vuln.sh`（AC-ENG-07） |
|---|---|---|
| 判什么 | **源码里出现的算法标识/调用**（`SHA-1/256`、`RS256`、`MessageDigest`…） | **依赖坐标上的漏洞**（`GO-*` / `CVE-*` / `GHSA-*`） |
| 输入 | `.go/.java/.ts/.js/.vue` 的代码行（剥注释） | `go.mod` / `web/package-lock.json` + 离线快照 |
| 典型红 | 测试文件里写了裸词 `RS256` | 某个库爆 CVE（如 go-redis GO-2025-3540） |
| 输出是否互相消费 | **否** | **否** |

一句话：**"源码里写没写 SHA/RSA"归国密闸，"用的库有没有洞"归本闸。**

## 7. 本地怎么跑 / 怎么复算

```bash
./scripts/check-vuln.sh              # 判本仓（与 CI 同一条命令；零网络）
./scripts/check-vuln.sh --self-test  # 正控/负控 14 项（临时夹具注入，不动工作树、不联网）
make gates                           # 上面这条已并入工程红线门禁
```

自检 14 项覆盖：Go HIGH 判红并点位、修复版判绿（版本敏感）、Go 无 CVSS 也阻断、npm HIGH 判红并点位、
npm MODERATE 只提示、白名单"未过期绿 / 过期红 / 不再命中红"、快照"过期红 / 坏行红 / 缺失红"、
依赖版本不可解析红、白名单格式坏行红、本地路径 replace 打 `[UNCOVERED]`。
