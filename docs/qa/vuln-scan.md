# 依赖漏洞门禁（AC-ENG-07）

> 判据原文（`docs/acceptance-criteria.md` §AC-ENG-07）：**govulncheck / trivy 必须真跑**，发现 **HIGH/CRITICAL** 判红。
> 落地物：`scripts/check-vuln.sh`（门禁本体 + 正控自检）、CI job **`vuln`**、白名单 `docs/qa/vuln-allowlist.tsv` 与 `.trivyignore.yaml`。

## 1. 判定规则（fail-closed）

| 场景 | 结果 |
|---|---|
| `govulncheck` 报出**代码可达**的漏洞，且不在白名单 | **红**（点名 `GO-ID` / 模块坐标 / 修复版本） |
| 白名单条目**已过期**（`expires` < 今天 / `expired_at` 已过） | **红**（到期即重新判红） |
| 白名单条目**本次不再命中**（漏洞已修或依赖已换） | **红**（必须删行，白名单不许只增不减） |
| 白名单行不是四列 / 理由为空 / 日期格式不对 | **红**（不允许"无理由白名单"） |
| `trivy` 发现未白名单的 HIGH/CRITICAL | **红** |
| 扫描器自身失败、漏洞库拉不到、库龄 > `MAX_DB_AGE_DAYS`（默认 7 天） | **红**（扫描没跑成 = 未验证 = 红） |

**为什么 govulncheck 不按 CVSS 分级**：Go 官方 OSV 不带 CVSS 分数，硬套 severity 只能自欺；而 govulncheck 只报**符号级可达**（你的代码真的能调用到）的漏洞，本身比"按分数筛"更严。所以 Go 侧口径是**可达即红**，例外交白名单逐条给理由。trivy 侧按判据原文用 `--severity HIGH,CRITICAL --exit-code 1`。

## 2. 两条扫描各判什么

| | `govulncheck` | `trivy fs` |
|---|---|---|
| 输入 | Go 源码 + `go.mod`/`go.sum`（含标准库） | `go.mod` / `web/package-lock.json` 等 lockfile |
| 判定 | 可达性（`GO-ID` + 调用栈） | 依赖坐标上的 CVE/GHSA + 严重级 |
| 范围 | 全仓 `./...` | 全仓，**跳过** `sdk/`、`docs/`、`web/node_modules`、`.git` |
| 为什么跳过 `sdk/` | — | `sdk/java/pom.xml` 解析要访问 Maven 中央仓（实测 429 抖动）；改成离线 SBOM 是后续项（§6） |

## 3. 白名单（当前 11 条，逐条有理由 + 到期日）

`docs/qa/vuln-allowlist.tsv`（Go 侧，10 条，全部是 **Go 标准库**、修复只存在于 `go1.26.x`）：

| id | module | 为什么现在不能修 | 到期 |
|---|---|---|---|
| GO-2026-6809 | stdlib (`crypto/x509`) | 修复在 go1.26.10；项目基线钉 Go 1.25（技术栈 §2.3），升 1.26 需 Spec/Ark 决策 | 2026-12-31 |
| GO-2026-6617 / 6613 / 6612 / 6611 / 6610 / 6605 / 6603 | stdlib (`net/http`) | 修复在 go1.26.9；同上 | 2026-12-31 |
| GO-2026-6608 | stdlib (`net/textproto` / `mime/multipart`) | 修复在 go1.26.9；同上。**同一 ID 会挂在不同 stdlib 包上**（取决于找到的第一条调用路径），故标准库按 `id + stdlib` 登记 | 2026-12-31 |
| GO-2026-6607 | stdlib (`crypto/tls`) | 修复在 go1.26.9；同上 | 2026-12-31 |

> 这些不是"历史欠账被放过"，而是**一条决策就能清掉**：升到 Go 1.26 后删掉这 10 行即可（门禁会自动变严）。
> 本 PR 同时修掉了唯一一条**能修**的：`github.com/redis/go-redis/v9` `v9.7.0 → v9.7.3`（GO-2025-3540），因此它**不在**白名单里。

`.trivyignore.yaml`（依赖坐标侧，1 条）：

| id | 坐标 | 理由 | 到期 |
|---|---|---|---|
| GHSA-g2v6-rqmx-r4w6 | `@vue/server-renderer` 3.5.13（`web/package-lock.json`） | 前端是纯 CSR（vite build → `go:embed` 静态托管），**无 SSR 入口**，该漏洞触发面不可达；修复需升 vue 到 3.5.42+，归前端（Vue）下一个依赖 PR | 2026-12-15 |

## 4. 漏洞库更新策略（"库不是三天前的"）

- **CI 每次冷启动**：不缓存 trivy DB（runner 无持久 cache），`trivy fs` 会先拉最新 `trivy-db`；govulncheck 每次向 `https://vuln.go.dev` 取最新索引。
- **机器判龄**：脚本读 `trivy version` 的 `UpdatedAt` 与 `govulncheck -version` 的 `DB updated`，超过 `MAX_DB_AGE_DAYS`（默认 **7**）直接判红——库旧等于没扫。
- **离线环境**（内网/断网）：先同步快照，再让门禁指向快照，并且**必须把快照时间打进证据**：
  - trivy：`TRIVY_SKIP_DB_UPDATE=true` + 预置 `~/.cache/trivy/db`（或 `--cache-dir`），此时 `UpdatedAt` 就是快照日；同步频率必须 ≤ 7 天，否则门禁自己红。
  - govulncheck：`-db file:///path/to/osv` 指向本地 OSV 快照（同样按 `DB updated` 判龄）。
  - 离线不是"关掉门禁"：判龄规则不变，只是库的来源换成本地快照。

## 5. 与「国密/算法扫描门禁」的边界（不重叠）

| | `scripts/check-guomi-algorithms.sh`（AC-AUD-12⑤） | `scripts/check-vuln.sh`（AC-ENG-07） |
|---|---|---|
| 判什么 | **源码里出现的算法标识/调用**（`SHA-1/256/512`、`RS256`、`MessageDigest`…） | **依赖坐标上的漏洞**（`GO-ID` / `CVE` / `GHSA`） |
| 输入 | `.go/.java/.ts/.js/.vue` 的代码行（剥注释） | `go.mod`/`go.sum`/`package-lock.json` + 漏洞库 |
| 典型红 | 测试文件里写了裸词 `RS256`（#82 那条） | 某个库爆 CVE（如 go-redis GO-2025-3540） |
| 会不会同一处被两套规则以不同理由判红 | **不会**：本闸不读源码文本，那条闸不读依赖库；两者输出互不消费。 | 同左 |

一句话：**源码里"写没写 SHA/RSA"归国密闸，"用的库有没有洞"归本闸**。两条闸各自 fail-closed，不互相豁免。

## 6. 已知待办（需决策，不在本 PR 擅自处理）

1. **Go 1.26 升级**（一次决策关掉 10 条 stdlib 白名单）：属基线变更（技术栈 §2.3 写的是 Go 1.25），需 Spec/Ark 拍。
2. **vue 升到 3.5.42+**（关掉 1 条 npm 白名单）：归前端（Vue），在下一个依赖升级 PR 里做。
3. **`sdk/java` 纳入扫描**：需要离线 SBOM（`mvn -o dependency:tree` 或 cyclonedx 插件导出），避免依赖 Maven 中央仓的可用性。

## 7. 本地怎么跑

```bash
# 工具（版本与 CI 一致）
GOBIN=$PWD/bin go install golang.org/x/vuln/cmd/govulncheck@v1.1.4
# trivy 0.76.2：从 GitHub Release 下载（CI 里带 sha256 校验）

# 注意：本地 Go 必须是 1.25 线**最新补丁**（`GOTOOLCHAIN=auto` 或自行升级），
# 旧补丁会把已修的标准库漏洞重新扫出来 —— 那不是门禁误报，是你的工具链旧了。
./scripts/check-vuln.sh            # 真扫（与 CI 同一条命令）
./scripts/check-vuln.sh --self-test # 正控/负控：注入已知漏洞依赖必须判红并点名
```
