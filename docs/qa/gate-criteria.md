# 关津 · 三闸准入判据（gate-criteria）

> **落位说明（临时）**：本文件暂落在仓根 `qa/` —— `origin/main` 的 `.gitignore` 目前仍忽略 `docs/`，该修复在 **PR #2**（`feat/docs-decisions-m1`）里、尚未合入；按 **P7** 不许 `git add -f`、也不许与 #2 撞 `.gitignore` 路径，故先落在不被忽略的 `qa/`。**#2 合入后整体迁到 `docs/qa/`，内容一字不改。** v1.1（本正本）同样**未推**：等 #3 合入后随「AC v11 + `qa→docs/qa` 迁移 + #3 的 P3」那个小 PR 一次提交。


| 项 | 内容 |
|---|---|
| 文档类型 | **执行判据**（工程门禁层）：把「三闸绿了」变成第三人/CI 都能照做的判定规则；**不是**新契约、**不改**任何 `AC-*` 编号与账目 |
| 版本 | **v1.1**（2026-11-03 归档调整：文件迁 `qa/` → `docs/qa/`；**退役 `compliance-precheck.md`**，其口径已并入 `decisions.md` D8-B/D8-E 第二版与 `acceptance-criteria.md` §11.1；判据与账目零改动）。**v1.1 判据**（2026-11-02 增补：**§9 门控 SKIP 判定 / 真库验收环境与责任 / 覆盖率分层**——应 Edge 报的「沙盒无 root、`initdb` 降权失败、7 条真库用例由 DSN 门控 SKIP」与「全局覆盖率 60% 是否够」两个口径问题；**判据口径、不替任何人定实现**）。v1（首版）：把「三闸绿」落成可照做的规则；依 hugo 裁决「只落判据、不写测试代码」 |
| 主笔 / 归属 | Gate（测试 · 合规）。**本文件 owner = Gate**，其他人只引用、不复制（P6 一人一文件） |
| 上位依据 | 《技术栈与工程约定》§9.2/§9.3/§9.4（CI 步骤与覆盖率下限）｜`docs/decisions.md` **P1**（不自动迁移）、**P6**（短特性分支）、**D12**（`acr` 白名单）、**D8-E 第二版**（密评不进强制项）｜`docs/m1-slices.md` **S0/S1/S11**｜`docs/acceptance-criteria.md` **`AC-ENG-01/03/05/06/07/08`**（本文件是这几条的**可执行化**，不新增期望值） |
| 判据对象 | ①骨架分支 `feat/skeleton`（Ark）②M1 各切片（S1–S11）③**任何改到代码面的 PR**；文档-only PR 见 §0.2 |
| 效力边界 | 本文件**只**判定「能不能提 PR」；**不判定**验收通过（那是 `AC-ENG-05` 映射表 + S11）、**不判定**密评（见 `docs/decisions.md` **D8-B【正式裁定】/ D8-E 第二版**（密评整体后置）+ `docs/acceptance-criteria.md` **§11.1**（重启密评的 7 项输入 I-1~I-7）） |

> **一句话**：三闸 = `go vet` + `go test` + `vue-tsc`；**绿 = 命令在本次 PR 的 head 提交上跑过 + 退出码与输出双条件成立 + 零跳过/零挑单/零排除 + 证据落盘**。**只看退出码一律不算绿**（本文 §5 有实测反例）。

---

## 0. 适用范围

### 0.1 判定对象与顺序

按**本次 PR 实际改到的文件面**决定要跑哪几闸，**不看 PR 标题、不看作者自述**：

| 改动面（以 `git diff --name-only origin/main...HEAD` 为准） | 必跑 |
|---|---|
| 含 `**/*.go`、`go.mod`、`go.sum`、`migrations/**` | `go vet` + `go test` |
| 含 `web/**`（`.ts` / `.vue` / `*.json` / `*.tsconfig*`） | `vue-tsc` |
| 含 `scripts/**`、`Makefile`、`.github/**` | **三闸全跑 + §5 变异注入全做**（改门禁的人必须先证明门禁会红） |
| 仅 `docs/**`、`*.md`、`.gitignore`、`LICENSE` | 三闸记 **`NOT-APPLICABLE（无代码面）`**，证据 = diff 文件清单；**不得**因为"跑不了"就记绿 |

**不许**把"本次只改了文档"当豁免：只要 diff 里出现一行代码，三闸全部必跑（文档改动不降低代码面的要求）。

### 0.2 不适用与不豁免

- 项目**未上线**、`main` 目前为空 → 骨架落地前，**不允许**用"没有代码所以跳过"记绿，只允许记 `NOT-APPLICABLE（无代码面）`并附 diff 清单。
- 三闸**不是**可选项：任何 PR 若声明"三闸绿"但拿不出 §4 的证据，一律按**未验证**处理（= 红），由评审直接打回，不进 P0/P1/P2 讨论。

---

## 1. 「绿」的判定（总规则）

一条 PR 的「三闸绿」必须**同时**满足下列 6 条，缺一条即红：

1. **提交一致**：三闸跑在**同一个提交**上，且该提交 = PR 的 head（证据里记 `git rev-parse HEAD` 短号；跑完又 amend/push 就要重跑）。
2. **工作树干净**：跑闸前 `git status --porcelain` 为空。**本地未提交的改动算红**——那等于测了别的东西。
3. **命令原文一致**：用 §2 的命令原文，**不许**加收窄参数（见 §3 红名单）。
4. **退出码为 0**：三闸全部 `0`。
5. **输出面无红信号**：exit 0 但输出出现 §3 任一红信号（`--- SKIP`、`[no tests to run]`、`0 tests`、`// @ts-nocheck` 等）→ **红**。
6. **证据齐全**：§4 的目录与文件齐、且与 PR 描述里的结论一致。

### 1.1 `go vet`

- 命令：`CGO_ENABLED=0 go vet ./...`（`CGO_ENABLED=0` 与构建口径一致，见 PROJECT-MAP 技术栈；`go vet` 不受 cgo 影响，写死是为了三闸同一环境）。
- 绿：退出码 `0` **且** stdout/stderr 为空（`go vet` 有问题必输出到 stderr）。
- 附加（`AC-ENG-07` 的同一批，属"格式/静态"而非三闸，但同批复核）：`gofmt -l .` 输出**必须为空**。
  ⚠️ **`gofmt -l` 的退出码恒为 0（实测，见 §5-E）**，所以"看退出码"会漏；必须判**输出为空**。
- **禁止**用会**改写文件**的 `make fmt` / `gofmt -w` 作为判据步骤：它会把红自动修成绿、且掩盖作者是否真的格式化过。判定顺序固定：**先 `gofmt -l`（只读）→ 非空即红 → 之后才允许自行 `gofmt -w` 再重跑**。

### 1.2 `go test`

- 命令：`CGO_ENABLED=0 go test ./... -count=1`（`-count=1` 禁用结果缓存，保证真的执行）。
- 绿：退出码 `0`，**且**满足三条硬条件：
  1. **零跳过**：日志里 `--- SKIP` 出现次数 = 0；
  2. **零空跑**：不出现 `[no tests to run]`，且 `ok <pkg>` 行的**执行用例数 > 0**（`-v` 时按 `--- PASS` 计数；不开 `-v` 时至少要求非 `no test files` 的包有测试执行）；
  3. **真依赖**：涉及 PG/Redis 的用例必须连**真实** PG（本地 `guanjin_test` 库）与**真实** Redis（`127.0.0.1:6379`），**禁 mock 替代**；依赖连不上时用例必须 **FAIL**（fail-closed），**不许** `t.Skip("依赖不可用")`。
- **`no test files` 的处理**：骨架期允许出现，但出现该行的包**必须在 PR 描述里列出并说明"无业务逻辑"**；进入 M1 切片后，与本次切片判据相关的包若仍 `no test files` → 红（对应 `AC-ENG-05` 的 `NOT-IMPLEMENTED` 门禁失败）。
- **`-race` 的口径（与 §9.4 的差异已登记，见 §8-3）**：§9.4 写 `go test ./... -race`，而 `-race` **要求 cgo**（`CGO_ENABLED=0` 下直接报 `-race requires cgo`，实测见 §5-F）。因此：
  - **本地默认三闸** = `CGO_ENABLED=0 go test ./... -count=1`（不跑 `-race`）；
  - **并发类判据**（`AC-AUD-01` 链尾串行、`AC-SESS-*` 多副本、`AC-FLOW-11` 并发补步）**必须额外**在独立 job 用 `CGO_ENABLED=1 go test ./internal/... -race -count=1` 跑一次，并把日志一并留证；
  - 二者**都不得跳过**；"跑了 `-race` 就不跑普通 `go test`"不成立（`-race` 会改变调度与内存行为，不能替代）。

### 1.3 `vue-tsc`

- 命令：在 `web/` 内 `npx vue-tsc --noEmit`（基线 §9.2；与 `npm run build` 里的 `vue-tsc -b` **不是同一条**，以 `--noEmit` 为准）。
- 绿：退出码 `0` **且**输出为空（无 `error TS`）。
- 前置：`web/package.json` 存在且 `vue-tsc` 在 devDependencies 里且已 `npm ci` 安装。**`web/` 尚未落地时**，该闸按 §0.1 处理：有前端改动 → 红（未接线）；无前端改动 → `NOT-APPLICABLE（无代码面）`。
- 禁：报错数下降/转换成 `any` 逃逸；`// @ts-nocheck` / 新增 `// @ts-ignore` / 放宽 `tsconfig` 的 `strict`、`strictNullChecks`、`noImplicitAny`；只对单个文件跑；加 `--skipLibCheck`（tsconfig 里既有的除外，且**本次 PR 不得放宽**）。

---

## 2. 本地怎么跑（依赖 = PG 三库 + Redis 7）

### 2.1 环境准备（一次性）

```bash
. /.keys/env.sh          # 注入工具链、PG 三库 DSN、Redis 地址/口令
                         # 文档不落明文口令；只用变量名：GUANJIN_{DEV,TEST,STAGING}_DSN / REDIS_ADDR / REDIS_PASSWORD
```

| 依赖 | 用途 | 判定要求 |
|---|---|---|
| PG 17 三库 `guanjin_dev` / `guanjin_test` / `guanjin_staging` | 迁移与集测 | 集测**只许**连 `guanjin_test`；**禁**在 `dev`/`staging` 上跑破坏性用例 |
| Redis 7 `127.0.0.1:6379` | 限流计数 / jti 防重放 / 热缓存 | 用例结束自行清理键；**禁**依赖上一轮残留 |
| `migrations/`（`golang-migrate` 纯 SQL） | schema 唯一权威 | 每次集测前 `migrate down all && migrate up`（或独立 schema），不得依赖库里的旧结构 |

连通性自检（改动依赖配置后必须先跑）：

```bash
redis-cli ping                                    # 期望 PONG
psql "$GUANJIN_TEST_DSN" -Atc 'select 1'          # 期望 1
```

### 2.2 三闸命令原文（照抄，别改）

```bash
# 0) 前置：干净工作树 + 记录提交
git status --porcelain            # 期望空
git rev-parse --short HEAD        # 记进证据

# 1) 格式（只读）+ 静态检查
gofmt -l .                        # 期望空输出（退出码恒 0，只看输出）
CGO_ENABLED=0 go vet ./...        # 期望 exit 0 + 无输出

# 2) 单元/集成测试（真实 PG+Redis）
CGO_ENABLED=0 go test ./... -count=1

# 3) 前端类型检查
cd web && npm ci && npx vue-tsc --noEmit && cd ..
```

### 2.3 每闸的「故意红」自证（首次接线 / 改门禁时必须做一次，见 §5）

三闸**不能只证明"能绿"，必须证明"会红"**：每条闸至少注入一次已知错误、确认变红、再撤回。这是 `AC-ENG-03`/`AC-ENG-08` 变异测试的同一条原则，只是对象换成三闸本身。

---

## 3. 什么算红（红名单，命中任一即红）

> 说明：**退出码 0 不等于绿**。下表 R2/R4/R5/R6 都是**实测**在 exit 0 的情况下"啥也没测"（见 §5）。

| # | 情形 | 为什么红 | 怎么检出 |
|---|---|---|---|
| **R1** | 跳过测试：`t.Skip` / `t.SkipNow` / 条件 skip / `-short` 掩盖 | 通过是假的，等于没验 | 日志 `--- SKIP` 计数 > 0 |
| **R2** | 挑单跑：`-run <单个用例>` 或只跑某个包 | 其余用例根本没执行，却仍 exit 0 | 日志出现 `[no tests to run]`，或执行用例数 < 该包应有数 |
| **R3** | 排除：`-tags` 把测试文件排除、把某包从 `./...` 里挪走、CI 只跑子集 | 同上，且让"门禁覆盖率"失真 | 比对 `go list ./...` 与测试日志里出现的包清单 |
| **R4** | 全 skip / 零用例：日志 `ok <pkg>` 但一个用例都没跑 | exit 0 且完全无证据 | 执行用例数 = 0 |
| **R5** | 缓存结果冒充执行：`go test` 无 `-count=1` 显示 `(cached)` | 代码已改仍显示上次结果 | 日志出现 `(cached)` |
| **R6** | 依赖不可用就 skip（"连不上库 → 跳过"） | 违反 fail-closed，正是本项目最忌讳的降级 | 用例内 `Skip` + 依赖地址不可达同时出现 |
| **R7** | mock 冒充真实依赖（PG/Redis 用假实现） | 集成判据失效 | 代码评审 + 断言真实连接（`select 1` / `PING`） |
| **R8** | 抑制静态检查：`go vet` 换 `-vettool`、加 build tag 缩小范围、把告警写进忽略清单 | 等于关掉闸 | 命令原文比对 |
| **R9** | 前端抑制：`// @ts-nocheck`、新增 `// @ts-ignore`、放宽 `strict`/`noImplicitAny`、只查单文件、`--skipLibCheck` | 类型检查失效 | grep diff + tsconfig diff |
| **R10** | 格式掩盖：先 `gofmt -w` / `make fmt` 再判"绿"，或只看 `gofmt -l` 的退出码 | 把红自动改成绿、且看不出原始状态 | 判定顺序（§1.1）+ 只读命令 |
| **R11** | 证据与提交不符：日志里的 sha ≠ PR head、在未提交改动的树上跑、拿上一轮日志充数 | 验的不是这个 PR | `git rev-parse` 与日志头比对 |
| **R12** | 桩/空实现顶替：门禁脚本存在但没接线、`check-*` 脚本被 `|| true`、CI step 被注释 | 脚本存在不算通过（`AC-ENG-03/08` 原文） | 读 CI 配置 + 变异注入必须红 |
| **R13** | 用"只改了文档"解释代码面未跑 | §0.1 已定：以 diff 文件清单为准 | diff 清单 |

---

## 4. 证据怎么留

**目录（沿用 `AC-ENG-06`）**：`artifacts/<short-sha>/gate/`

```
artifacts/<short-sha>/gate/
├─ cmd.txt        # 逐条命令原文 + 执行时间 + 提交短号 + 分支名
├─ status.txt     # 跑前 `git status --porcelain` 输出（期望空）
├─ gofmt.log      # gofmt -l 输出（期望空）
├─ vet.log        # stderr+stdout 合并（期望空）
├─ test.log       # 完整测试日志（含 --- SKIP / [no tests to run] / (cached) 可检索）
├─ tsc.log        # vue-tsc 输出（期望空）
└─ summary.txt    # 见下，四行，机器可读
```

`summary.txt` 固定四行（示例）：

```
sha=1a2b3c4  branch=feat/skeleton  when=2026-11-02T23:10+08:00
vet_exit=0  vet_output_lines=0
test_exit=0  tests_run=137  skips=0  cached=0  no_tests_to_run=0
tsc_exit=0  tsc_errors=0
```

**PR 描述里必须贴**同样四行（评审照抄核对；对不上 = R11 红）。
**日志必须能被检索**：`grep -c -- '--- SKIP' test.log`、`grep -c '\[no tests to run\]' test.log`、`grep -c '(cached)' test.log` 三个计数写进 `summary.txt`。

---

## 5. 闸自证（变异注入，必须各自单独验一次）

> 目的：证明本判据**不是纸面规则**——每条闸注入已知错误后必须红，撤回后必须绿。以下 A/B/C/D 已在本地实测（2026-11-02，Go 1.25.7 / Node 22.20 / PG17 / Redis 7）。

| # | 注入 | 期望 | 实测结论 |
|---|---|---|---|
| **A** | `fmt.Printf("%d\n", "x")`（`%d` 配字符串） | `go vet ./...` 非 0 | ✅ `exit=1`，stderr：`fmt.Printf format %d has arg "x" of wrong type string` |
| **B** | 断言失败用例 | `go test` 非 0 | ✅ FAIL（对照组：不注入时 `exit=0`） |
| **C** | 用例全 `t.Skip("…")` | 必须红（**日志**判红） | ⚠️ **exit=0、日志 `ok ... 0.003s`、无 `--- SKIP` 摘要**（非 `-v`）→ 证明**只看退出码会漏**，必须开 `-v` 或统计用例数 |
| **D** | `go test ./... -run NoSuchTest` | 必须红 | ⚠️ **exit=0**，日志 `ok ... [no tests to run]` → 必须按 `[no tests to run]` 判红 |
| **E** | 未格式化文件 | `gofmt -l` 非空 | ⚠️ **退出码恒 0**，只输出文件名 → 只能看输出 |
| **F** | `CGO_ENABLED=0 go test -race` | 应报错（故 `-race` 需独立 job） | ✅ `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1` |
| **G** | 前端类型错（`web/` 落地后） | `vue-tsc --noEmit` 非 0 | 待 `web/` 落地后补测（当前 `main` 无前端代码）——**未补测前，该闸不得声明"已验证会红"** |
| **H** | 四处禁码注入（属 `AC-ENG-08`，与三闸同批复核） | 每次单独注入都必须红 | ①`SERVICE_DEGRADED` 常量 ②`internal/admin` 返回 `STEP_ALREADY_SATISFIED` ③新增 `dataLabel(` ④新增 `.clearance(` |

> **C/D 的实操修正（写进判据）**：`go test` 默认不开 `-v`，`--- SKIP` 不会逐条打印。因此**判绿的证据命令改为**：
> `CGO_ENABLED=0 go test ./... -count=1 -v > test.log 2>&1`，并在 `summary.txt` 记 `tests_run`（`--- PASS` 计数）、`skips`（`--- SKIP` 计数）。**只用不带 `-v` 的 `ok` 行不足以判绿**。

---

## 6. PR 准入清单（第三人 / CI 照此逐条打勾）

```
[ ] 1  diff 面已判（§0.1）：要跑哪几闸、是否有 NOT-APPLICABLE 项及理由
[ ] 2  工作树干净、sha 与 PR head 一致（§1-1/2、§3-R11）
[ ] 3  gofmt -l 输出为空（不是退出码）
[ ] 4  go vet ./... exit 0 且无输出
[ ] 5  go test ./... -count=1 -v：exit 0、skips=0、no_tests_to_run=0、cached=0、tests_run>0
[ ] 6  并发类判据另有 CGO_ENABLED=1 -race 日志（若本切片涉及）
[ ] 7  vue-tsc --noEmit exit 0 且无 error TS（若不涉及前端 → N/A 且有 diff 证明）
[ ] 8  未新增 @ts-nocheck/@ts-ignore、未放宽 tsconfig strict、未加收窄参数（§3-R8/R9）
[ ] 9  门禁脚本（check-no-automigrate / check-forbidden-error-codes）在变异注入下确实红（改了 scripts/ 或首接线时必做）
[ ] 10 证据目录齐全、summary.txt 四行与 PR 描述一致
[ ] 11 集成用例只连 guanjin_test + 真实 Redis；**环境不可用时是「门控 SKIP = 未验证」，不是 PASS**（判据见 §9.1）
[ ] 12 `summary.txt` 有 `tests_skip` / `db_verified` / `verdict` 三项，且 `verdict` 与事实相符（§9.3）
[ ] 13 覆盖率按 **§9.3 逐模块下限**判，全局 60% 只作兜底（§9.2）
```

**判定**：1–11 全过 = 三闸绿（可提 PR / 可转评审）；任一不过 = 红，**不进入 P0/P1/P2 分级讨论**，先修到绿。

---

## 7. 与既有判据的关系（不改账目）

| 既有条目 | 关系 |
|---|---|
| `AC-ENG-01`（覆盖率下限） | **不在三闸内**：`coverage-gate.sh` 单独一闸，M1 切片 PR 必须同时绿；三闸绿 ≠ 覆盖率达标 |
| `AC-ENG-03`（禁 `AutoMigrate`/`gorm.DeletedAt`） | 门禁脚本，与三闸同批复核；**脚本存在不算通过**，须变异注入变红 |
| `AC-ENG-05`（映射表六栏） | S11 判定；三闸是它的**前置**（先绿才有资格出映射表） |
| `AC-ENG-06`（证据留档） | 本文件 §4 是它的 gate 子集落地，格式不冲突（`artifacts/<commit>/<AC-ID>/` 可并存） |
| `AC-ENG-07`（govulncheck/trivy/gofmt/vue-tsc） | 本文件 §1.1/§1.3 覆盖 `gofmt` 与 `vue-tsc`；`govulncheck`/`trivy` 是独立步骤，仍必跑 |
| `AC-ENG-08`（禁码/禁词四注入） | 与三闸同批（S1 建、S11 复核），见 §5-H |
| `AC-AUD-10` / 密评 | **完全无关**：密评不进三闸、不进契约强制项，见 `docs/decisions.md` **D8-B【正式裁定】/ D8-E 第二版**（密评整体后置）+ `docs/acceptance-criteria.md` **§11.1**（重启密评的 7 项输入 I-1~I-7）；三闸**不得**因为密评口径未定而卡住或放宽 |

**账目未动**：本文件不新增、不重排任何 `AC-*`，不改 `acceptance-criteria.md` 的账目（**现 v11：90 = 83 `PASS-able` / 0 `PENDING` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置（DEFERRED）：本期不做判定」**；v1 写的是当时 v9 的 85/68/10，v11 追认后已变，本文件只引用、不记账）。

---

## 8. 落地时发现的不一致（报给 Ark / hugo，本文件不动他人文件）

| # | 位置 | 现象 | 影响 / 处置建议 |
|---|---|---|---|
| 1 | `Makefile`（PR #2）`DATABASE_URL ?= postgres://guanjin:guanjin@…/guanjin_dev` | 与本地实际账号不符：该用户名口令**认证失败**（实测 `password authentication failed for user "guanjin"`）；可用的是 `/.keys/env.sh` 的 `GUANJIN_DEV_DSN`（`guanjin_dev`） | `make migrate-up` 按默认值会直接失败。建议改为从环境变量取（`DATABASE_URL ?= $(GUANJIN_DEV_DSN)`），**不落明文口令** |
| 2 | `Makefile` `lint: fmt check-gates` + `fmt: gofmt -l -w .` | `make lint` 会**先改写文件再检查**，把"未格式化"这个红自动修绿，且 `gofmt -l` 退出码恒 0 | 判据侧已用 §1.1/§3-R10 兜住；建议把 `fmt` 与 `lint` 拆开（`lint` 只读） |
| 3 | 技术栈 §9.4 `go test ./... -race` vs 项目 `CGO_ENABLED=0` | `-race` 需要 cgo，`CGO_ENABLED=0` 下直接报错（实测） | 已在本文件 §1.2 给出双 job 口径；若 Ark 认可，请把该口径写进 S1 的 Makefile/CI |
| 4 | `README.md`（PR #2）「密评整体后置」 | 与 D8-E 第二版统一表述不完全一致（缺「实现按三级条款固定」与「责任转移」） | 密评表述由 Spec/Ark 改；三闸不受影响（见 §7） |

---


---

## 9. 增补判据（v1.1）：门控 SKIP / 真库验收环境 / 覆盖率分层

> 起因：Edge 交 S3 时报了两个环境事实——①他的沙盒**跑不了 PG**（PG17 二进制在，但 `/etc/passwd` 非 root 不可读 → `initdb` 降权失败）、**也跑不了 docker**（testcontainers 同理不可用），故 **7 条真库用例由 `GUANJIN_TEST_DSN` 门控：无 DSN 时 SKIP**，他没有假称跑过；②覆盖率报了 crypto 94.9% / store 86.0% / model 88.3%，而骨架侧新接的是**全局 60% 下限**（自定值，基线未给）。本节只给判据，**不替任何人定实现**。

### 9.1 门控 SKIP 在「闸绿」里算什么 —— **算红（未验证）**

**一句话**：门控 SKIP = **未验证 = 红**。它**不是绿**，也**不是「绿但证据缺真库项」**——后一种说法会让人把「没跑过」当成「跑过了但没留证」，两者在证据链上是两件事。**只有同一条用例在指定环境上真跑过且 `skips=0`，才转绿。**

| 层 | 允许 SKIP 吗 | 状态名 | 能不能说「三闸绿」 |
|---|---|---|---|
| **本地开发闸**（owner 自测） | 允许，但**仅限「环境不可用」** | `PARTIAL（未验证）` | ❌ 不能说 |
| **PR 准入闸**（评审/合入前） | **不允许** | `skips>0` → **红** | ❌ 不能说（可以开 PR、可以评审，但**不许进合入队列**） |
| **验收闸**（S11 / `AC-ENG-05` 映射表） | **不允许** | 缺真库证据 = `NOT-IMPLEMENTED`（门禁失败） | — |

**为什么不能算绿**：`SKIP` 与「通过」在 CI 语义里是两回事，一旦允许「没环境就跳过并记绿」，等于把 **fail-closed** 换成 **fail-open**——正是基线红线（"不许为让测试通过而关掉 fail-closed"）禁止的那类降级。本项目 `AC-FC-*` 一组的全部前提就是"依赖不可用必须拒服"，测试侧同理：**依赖不可用不是"这条不重要"，而是"这条还没验"。**

**SKIP 的合法性边界（否则 SKIP 会变成第二个逃逸口）**

| 允许 | 禁止 |
|---|---|
| 环境缺失：无 `GUANJIN_TEST_DSN` / 无 docker / 无 Redis / 无 root 建库 | 因「切片不含」「用例难写」「偶发 flaky」而 SKIP（那属于删/改用例，必须走 ADR 登记） |
| 由一个**单一门控函数**（如 `requireDB(t)` / `requireRedis(t)`）统一发起，并打印 `SKIP reason=no-dsn`（**可检索**） | 手写散落的 `t.Skip("...")`（无法机器归因） |
| 在 `summary.txt` 里**如实记真数** | 把 `skips` 报成 0、把 SKIP 行从日志里过滤、把该包从 `./...` 摘掉、加 build tag 排除、用 mock 顶替（= §3 的 R3/R7） |

**同一用例在不同环境状态不一致时，报最差态**：本地 SKIP、别处 PASS → 记 `PARTIAL`；别处 PASS 也不能把本地的 SKIP 抹掉（证据要能对上 sha 与环境）。

### 9.2 真库验收在哪台环境跑、谁跑

**权威环境只有一个：CI 的 `integration` job**（《技术栈》§9.3：testcontainers `postgres:16` + `redis:7`），产出 `artifacts/<sha>/gate/db/`。理由：可复现、不依赖任何人的沙盒，且能把「环境差异」从判据里剔掉。

**过渡期（CI 的 integration job 尚未接线时）**：由**宿主（hugo）指定的那一台**有 PG17 三库 + Redis 的环境代跑。**不按人复制环境**——现在的事实是「Ark 那台能跑、Edge/Core 的沙盒不能」，各自复制环境既慢又会引出"我的库版本和你不一样"的新争议。

| 环节 | 谁 |
|---|---|
| 提供「要跑哪条命令、对哪个 sha」 | **切片 owner**（Edge/Core/Nova） |
| 实际执行真库 job（或代跑） | 有环境者（CI runner / 指定那台）；**owner 提供可复制的命令块** |
| 复核并归档证据（`db_env` + 命令 + 结果） | **Gate** |
| 指定/更换那台环境 | **hugo**（不在团队内自行协商） |

**两条硬规则**：
1. **不得拿别人环境的绿当自己 PR 的证据**——真库证据必须绑定**该 PR 的 head sha**、并写明 `db_env`（引擎版本 + 库名 + Redis 版本）。Ark 那次 PG17 全绿是**骨架**的证据，**不是** Edge S3 的。
2. **一次就够，但必须有**：同一 PR 不要求每轮都跑，但**合入前必须有且只有一次对最终 head 的真库证据**；之后代码变了就重跑（sha 变即失效，同 §3-R11）。

**给 Edge 的具体口径**：他现在的 `PARTIAL（未验证）` 是**合规且诚实**的本地状态，**不是缺陷**；但 S3 的 PR **不能以「三闸绿」送评审**，要么等 CI integration job 上线后由它产出，要么把「7 条真库用例 + 命令块 + 目标 sha」交给指定那台环境代跑、由 Gate 归档后再转绿。

### 9.3 `summary.txt` 记什么（v1.1 扩为六行，旧四行不再判绿）

```
sha=<短号>  branch=<分支>  when=<ISO8601>
vet_exit=0   vet_output_lines=0
test_exit=0  tests_run=137  tests_pass=126  tests_skip=7  tests_fail=0  cached=0  no_tests_to_run=0
db_verified=no   db_env=no-dsn   db_skips=7
tsc_exit=0   tsc_errors=0
verdict=PARTIAL
```

**`verdict` 的唯一算法**（机器可判，人照抄）：

| verdict | 条件 |
|---|---|
| `GREEN` | 三闸 `exit=0`、`vet_output_lines=0`、`tsc_errors=0`、`tests_skip=0`、`tests_fail=0`、`cached=0`、`no_tests_to_run=0`、`tests_run>0`、**`db_verified=yes`** |
| `PARTIAL` | 上述全成立、但 `tests_skip>0` **且** `db_verified=no`（原因只能是环境缺失）——**这类仍按红处理，只用于区分"没验"与"验失败"** |
| `RED` | 其它一切（含任何 §3-R1…R13） |

**`db_env` 写法**：`no-dsn`（本地无 DSN）｜`PG17@127.0.0.1/guanjin_test+Redis7`（本地真库）｜`ci:postgres:16+redis:7`（CI 权威）——三种之外写清实际值，**不许空着**。

### 9.4 覆盖率是否分层 —— **按基线 §9.3 逐模块下限，全局 60% 只作兜底**

**权威 = 基线《技术栈与工程约定》§9.3 的逐模块下限**（`AC-ENG-01` 原文，本文件 §7 已引用）：

| 模块 | 下限 | 模块 | 下限 |
|---|---:|---|---:|
| `crypto` | **90%** | `audit` | **90%** |
| `factor` / `credential` / `identifier` / `flow` / `authz` | **85%** | `risk` / `gateway` | **80%** |
| `store` | **75%** | `protocol` / `admin` / `agent` | **70%** |

**判据三条**：
1. **逐模块判、取更严**：模块下限与全局总闸**同时生效**；全局值**不得低于任何单模块下限**（否则它会掩盖模块掉档），二者冲突时以**更严**者为准。
2. **全局 60% 只能当兜底，不能当唯一闸，也不能当免责理由**：它是骨架期的自定值，**基线没给过这个数**；用它替代 §9.3 = 把 `crypto 94.9%` 和 `admin 61%` 拉平看，等于放弃了对密码学模块的要求。
3. **不另设"更高"阈值**（不给 crypto/store 抬到 95/90 之类）：①§9.3 **已经是"单独设"**（crypto 90 / store 75），再抬没有基线依据，属数字游戏；②关键路径靠**用例存在性**保证（`crypto`：SM2 签/验、SM3/HMAC 向量、SM4 往返、HKDF 域隔离、PBKDF2-SM3 一致性向量；`store`：23505 各约束 → 业务码的双键映射），这些由 `AC-*` 断言管，不由百分比管；③历史缺陷显示某模块不足时，按 **ADR + 到期日** 提阈值（沿用放宽带到期日的机制），**不口头抬**。

**统计口径（必须写进 `coverage-gate.sh`，否则数字可比性为零）**：
- **按包聚合**（`go test -coverprofile` → `go tool cover -func` 汇总到"模块"），**禁止用全仓平均**顶替模块值；
- **排除**：`_test.go`、`mocks/`、`generated/`、`*.pb.go`、`cmd/`（入口薄层）；
- **向下取整**（与骨架双闸一致），不四舍五入；
- 报告必须列**每个模块一行**（模块 / 实测 / 下限 / 判定），只给总数不算证据。

> **一处要登记的不一致（不改账目，只在判据侧点名）**：`AC-ENG-01` 现文只列了 **7 组下限**（crypto 90 / flow 85 / credential 85 / identifier 85 / gateway 80 / store 75 / protocol·admin·agent 70），**漏了基线 §9.3 里的 `factor 85`、`authz 85`、`audit 90`、`risk 80` 四行**。判据侧以**基线 §9.3 为准**（本表已列全 11 行）；`AC-ENG-01` 的补齐放在下一版（只补模块名与下限，**不加条数、不改账目**）。这一条正是「全局 60% 看着够、模块其实缺表」的成因，请 Ark/hugo 知情。

**对 Edge 这组数的核对（按 §9.3，不是按 60%）**：`internal/crypto 94.9% ≥ 90` ✅；`internal/store 86.0% ≥ 75` ✅；`internal/store/model 88.3%` 归入 `store` 口径（≥75 ✅，**`model` 不是 §9.3 的独立行，不得单列成"免检"**）。结论：**这三项按基线已过**；`PARTIAL` 只来自 §9.1 的真库缺位，与覆盖率无关。

---

## 10. 变更记录

- **v1.1 归档调整（2026-11-03，判据零改动）**：①本文件由 `qa/gate-criteria.md` **迁到 `docs/qa/gate-criteria.md`**（按 Ark 裁定统一到 `docs/`，仓库根不再放 `qa/`；门禁脚本对 `qa/*` 与 `docs/qa/*` 双豁免，迁移不点红）；②**删除 `qa/compliance-precheck.md`（退役，不留 RETIRED 副本）**——其口径已被 `decisions.md` **D8-B【正式裁定】/ D8-E 第二版**（密评整体后置、密评不进门禁）与 `acceptance-criteria.md` **§11.1**（重启密评的 I-1~I-7 输入清单）吸收，保留两份会造成"两套有效口径"；③正文两处指向该退役文件的引用改指上述两处。**未改任何 `AC-*`、未改账目（仍 v11 = 83/0/0/6/0/1）。**
- **v1.1（2026-11-02）**：增补 §9 —— ①**门控 SKIP = 未验证 = 红**（本地 `PARTIAL` 允许、PR 准入与 S11 不允许；`verdict` 三值算法；SKIP 合法性边界：只准环境缺失、单一门控函数、禁过滤/摘包/mock 顶替）；②**真库验收的权威环境 = CI integration job**，过渡期由 hugo 指定的那一台代跑、owner 提供命令与 sha、**Gate 复核归档**，**不得拿别人环境的绿当自己证据**；③**覆盖率按基线 §9.3 逐模块下限**（crypto 90 / store 75 …），**全局 60% 只作兜底、不得替代、也不另设更高阈值**，并写明按包聚合 / 排除项 / 向下取整的统计口径。`summary.txt` 由四行扩为**六行**（增 `tests_skip` / `db_verified`·`db_env` / `verdict`），旧四行自 v1.1 起不再判绿。**未改任何 `AC-*` 与账目（现 v11 = 83/0/0/6/0/1）。**
- **v1（2026-11-02）**：首版。把「三闸绿」落成可照做的判据：§1 逐闸绿的条件、§2 本地怎么跑（PG 三库 + Redis）、§3 十三条红名单、§4 证据格式（`artifacts/<sha>/gate/`）、§5 闸自证（A–F 已实测，G 待 `web/` 落地）、§6 十一项 PR 准入清单、§7 与 `AC-ENG-*` 的关系（**不改账目**）、§8 四处不一致。**只落判据、不写测试代码**（依 hugo 裁决）；测试与验收任务等骨架落地后再落。
