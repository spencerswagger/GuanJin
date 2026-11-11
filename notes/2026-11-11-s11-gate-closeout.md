# S11 收口 · 门禁证据（夜班 B · Gate）

> **冻结代码 sha**：`fa5969b1f0d1c67eae9f776760b64216d4518008`
> 分支 `ci/s11-gate-closeout`，基点 `origin/main` = `3b1d4fb9d1073e5c4d5278771d411b5d0a439ee0`。
> 三次全绿、反向实测、覆盖率**全部在 `fa5969b` 这一棵树上跑**；宿主按 sha 即可复算。
> 原始日志入库在 `notes/s11-gate-2026-11-11/`（下面每条都给文件名）。
> 本笔记所在提交**只加 `notes/` 与证据文件**，不再动 `.go` / CI 文件 —— 代码冻结点仍是 `fa5969b`
> （`git diff fa5969b <本提交> -- '*.go' '.github/**' 'scripts/**'` 为空可自证）。

## 0 · 结论

1. **前置阻塞已拆**：CI 第 5 步的 `apt-get install postgresql-client redis-tools` 改成**零网络解析**（镜像自带 / `docker exec` 进 service 容器），并在 job 里先跑脚本自测（三条分支）。不再把网络抖动当常态。
2. **三次全绿**（`-tags=integration -race -p 8 -count=1 ./...`，同一 sha）：每次 **23 包 ok / `^FAIL` 0 行 / `--- FAIL` 0 条 / SKIP 0**，48s / 49s / 52s。
3. **门禁会红**（反向实测）：摘掉审计链的 advisory 链锁 + 链尾 `FOR UPDATE` → `--- FAIL: TestAC_AUD_04_…`，点名 **`concurrent_serial_db_test.go:85`**（`chain is broken … at index 49 (prev_tag_mismatch)`）。恢复后同用例绿。
4. **覆盖率不降**：unit **67.8% → 67.8%**、integration **81.3% → 81.3%**，逐模块数字逐字相同，双闸 `[OK]`。

---

## 1 · 前置阻塞：CI 的 apt 步（根因 + 选法）

### 1.1 现象（有原文）

run **43050572912**（PR #124，纯 docs）integration job：

```
15:40:19  ##[group] Run sudo apt-get update -qq && sudo apt-get install -y --no-install-recommends postgresql-client redis-tools
15:43:43  W: Failed to fetch … Temporary failure resolving 'azure.archive.ubuntu.com'
15:46:03  Err:2 https://security.ubuntu.com/…  Temporary failure resolving 'azure.archive.ubuntu.com'
15:47:03  E: Failed to fetch …/libjemalloc2_5.3.0-2build1_amd64.deb  Temporary failure resolving 'azure.archive.ubuntu.com'
15:47:03  ##[error]Process completed with exit code 100.
```

其后 13 个 step 全 `skipped`（migrate / schema 断言 / 集成用例 / Redis 断言 / 覆盖率…）。
**全 skipped = 未验证 = 红**，`#124` 这类纯 docs PR 也起不来。

### 1.2 选了哪条：**(a) + (b) 都做，以 (a) 为主**

| | 做了什么 | 为什么 |
|---|---|---|
| (a) 修 CI | 新增 `scripts/ci-integration-deps.sh`：`psql` / `pg_isready` 优先用 runner 镜像自带（`PATH` → `/usr/lib/postgresql/*/bin`），`redis-cli` 优先 `PATH`；缺失时生成 shim，用 `docker exec` 转发进 job service 容器（Actions 的 services 本来就跑在 runner 的 docker daemon 上），并把 shim 目录写进 `$GITHUB_PATH`；两条都走不通 → `exit 1` | 跑测试只需要**客户端**工具，不需要装包。换镜像源/加重试仍然是"依赖外网 DNS"，还会白等 4~7 分钟；改成**零下载**才把这一类抖动从门禁路径上摘掉 |
| (b) 本地复跑 | 用真 PG 17 + 一次性 `redis-server`（每轮独立端口）跑 integration，命令见 §6 | CI 修好前后都要有本地可复算证据；也让"全 skipped"再也糊不过去 |

脚本自带 `--self-test`（先跑它，再解析），三条分支都验过：

```
[deps] self-test A OK：缺工具 → 3 个 docker exec shim + GITHUB_PATH
[deps] self-test B OK：镜像自带 → 零 shim、零网络
[deps] self-test C OK：无工具且无容器 → 红
[deps] self-test 全绿（3/3）
```

`ci.yml` 改动只有两处：把 apt 那行换成「先 self-test 再解析」，并在「等依赖就绪」步显式
`export PATH="${RUNNER_TEMP}/ci-deps-bin:$PATH"`（脚本是子进程，PATH 不回流；后续 step 靠 `$GITHUB_PATH`）。
**没有改任何产品逻辑**：`git diff --stat 3b1d4fb fa5969b` = 3 个文件（`.github/workflows/ci.yml`、`scripts/ci-integration-deps.sh`、`internal/identity/acflow09_db_test.go`）。

### 1.3 顺手修掉的一个真红（原来会让三次全绿不可能成立）

`3b1d4fb` 上第一次跑 `-tags=integration -race -p 8` 就红了，不是偶发：

```
WARNING: DATA RACE
  Write at … by goroutine 59: identity.doBind()  internal/identity/acflow09_db_test.go:76
  Previous read at … by goroutine 60: (*MeIdentifiersHTTP).bind()  internal/identity/me_http.go:83
--- FAIL: TestACFLOW09_04_Concurrent403NoInserts  (race detected during execution of test)
FAIL github.com/spencerswagger/guanjin/internal/identity 35.885s
```

`doBind` 写 `h.resolver`、`h.bind` 读它；并发用例 10 个 goroutine 共用同一个 handler。
真实现每进程只装配一次 resolver，**这是夹具竞争，不是产品缺陷**。修法：拆出 `bindOnce`（只发请求、不动 handler），
并发用例在起 goroutine **之前**单线程设定 resolver（`doBind` 保留原语义，注释写清"只许单线程"）。
原始日志：`pre-fix-3b1d4fb.log` / `pre-fix-3b1d4fb.summary.txt`。

---

## 2 · 硬标准 ①：三次 `-race -p 8` 全绿（同 sha）

命令（每次都是完整跑，`-count=1` 不吃缓存）：

```sh
CGO_ENABLED=1 \
GUANJIN_TEST_DSN=postgres://guanjin_test:***@127.0.0.1:5432/guanjin_s11_gate_test?sslmode=disable \
GUANJIN_TEST_REDIS_ADDR=127.0.0.1:1642N  GUANJIN_TEST_REDIS_PASSWORD=*** \
  go test -tags=integration -race -p 8 -count=1 ./...
```

| 次 | sha | 原始日志（入库） | ok 包 | `^FAIL` | `--- FAIL` | SKIP | 耗时 | Redis 端口 | sha256(日志) |
|---|---|---|---|---|---|---|---|---|---|
| 1 | `fa5969b` | `run1.log` / `run1.summary.txt` | **23** | 0 | 0 | 0 | 48s | 16420 | `5dab3a09a3207cc6…` |
| 2 | `fa5969b` | `run2.log` / `run2.summary.txt` | **23** | 0 | 0 | 0 | 49s | 16421 | `5e3c188b13b39c9f…` |
| 3 | `fa5969b` | `run3.log` / `run3.summary.txt` | **23** | 0 | 0 | 0 | 52s | 16422 | `0c9a0310ecc460ba…` |

```
packages_ok=23  packages_no_test_files=2   # 25 = `go list -tags=integration ./...`（authz / testdb 无测试文件）
caret_FAIL_lines=0  test_func_FAIL=0  test_func_SKIP=0  exit_code=0
```

日志是 `go test` 的**原始输出**（未加工、三份独立文件，不合并成摘要）；每份 25 行，
内容就是 23 行 `ok  <pkg>` + 2 行 `? <pkg> [no test files]`，无 `FAIL` 行、无 `DATA RACE` 行。

**隔离细节**（为什么这三次可信，不是"刚好没撞上"）：

- 每轮独立库：`DROP DATABASE IF EXISTS guanjin_s11_gate_test WITH (FORCE)` → `CREATE` → `migrate up` 到 **v21**（不碰共享 `guanjin_test`）。
- 每轮独立 Redis：新起 `redis-server`（独立端口 + `requirepass`），起后校验 `INFO server.process_id == $!`（防端口被垂死实例占着 —— FL-001 的教训），跑完杀进程并断言端口不再应答。
- 三次之间零状态残留（库重建 + Redis 新建）。

### 补证（不是三次的一部分，但同一 sha）

- `go test -tags 'integration audit_serial' ./internal/audit -p 1 -count=3 -v`（CI 的串行套件命令）：
  **192 PASS / 0 FAIL**，`TestAC_AUD_04_Concurrent100AppendsKeepChainIntact` **3/3 PASS**、
  `TestAppendB2BothLocksRemovedMustFork` **3/3 PASS**（探针敏感性）。日志 `audit-serial-fa5969b.log`。
- 全量 `-v` 复跑一次：**760 PASS / 0 FAIL / 0 SKIP / 23 ok**，日志 `supplementary-verbose2-fa5969b.log`（证明"0 skip"不是非 verbose 输出看不见而已）。
- **如实记录一次运行时异常**：另一轮 `-v` 全量跑里 `internal/crypto` 崩在
  `fatal error: traceback did not unwind completely`（发生在 `TestDerivePasswordSensitivity` 之后），
  该包单独跑 **2/2 绿**、全量 5 次里 4 次绿，属于运行环境/工具链的一次性异常，**不是断言失败**。
  摘录：`runtime-crash-internal-crypto-excerpt.log`（完整日志在我的工作区 `generated/s11-gate/runs/supplementary-verbose-fa5969b.log`）。
  已记入 `notes/flake-ledger.md` FL-005，后续再复现就按工具链问题上报。

---

## 3 · 硬标准 ②：门禁反向实测（改坏 → 必须红 → 点名文件:行号）

**注入**（临时分支 `gate/negative-audit`，**未推送**）：`internal/audit/repo.go` 里把
`pg_advisory_xact_lock(chainLockKey)` 与链尾 `FOR UPDATE` **同时摘掉**（两道锁都失效）。
patch 落 `negative-check-3d21b64.patch`。

| 状态 | sha | 结果 |
|---|---|---|
| 改坏 | `3d21b64a0419b5a2ac6922c4b673be24a75951a7` | **红**：`--- FAIL: TestAC_AUD_04_Concurrent100AppendsKeepChainIntact`，`concurrent_serial_db_test.go:85`：`并发 100 写后整链必须通过，实际断在 audit: chain is broken: record 01a1cb7d-… at index 49 (prev_tag_mismatch)`；RC=1 |
| 恢复 | `fa5969b1f0d1c67eae9f776760b64216d4518008`（`git diff fa5969b --` = 0 行） | **绿**：同命令 `ok internal/audit 1.037s` |

日志：`negative-audit-3d21b64.log`（红）、`restored-fa5969b-audit.log`（绿）。

**附带的真发现（比红本身更重要）**：存储层的平台因子并发用例 `TestDBPlatformFactorConcurrentSetEnabledIsSerialized`
对"**只摘 advisory 锁**"不敏感 —— 我用 `0536a23`（只摘 `platformFactorLockKey` 的 advisory 锁，保留 `FOR UPDATE`）
跑它，**仍然绿**（`negative-check-0536a23.log`）。原因：用例自己的 `restore()` 先把
`runtime_config['factors.disabled']` 这行**建好了**（D22 要求 seed 行存在），行存在时 `FOR UPDATE`
就足以串行化 —— 与它注释里"前置：这一行必须不存在"自相矛盾。
也就是说：**AC-FLOW-05 判据 4 在 main 上的守线是漏检的**，真守线在 #120（HTTP 层）里，而 #120 至今 **OPEN 未合入**。
→ 见 §7：要么 Edge 把存储层用例改成真能复现"缺行"前提，要么在 #120 合入后以 HTTP 层用例为准；两者都不是我这边能自己改的。

---

## 4 · 硬标准 ③：覆盖率前后（后 ≥ 前）

同一个 `scripts/coverage-gate.sh`，两次都在**干净重建的库**上跑。

| | 基线 `3b1d4fb` | 冻结 `fa5969b` |
|---|---|---|
| unit profile（`go test ./... -count=1 -coverprofile=unit.out`） | **total 67.8%**（min 60） | **total 67.8%**（min 60） |
| unit gate | `[OK]`，逐模块 admin 83 / credential 93 / crypto 91 / factor 95 / flow 87 / gateway 87 / identifier 93 / protocol 76 / risk 91 | 逐字相同，`[OK]` |
| integration profile（`go test -tags=integration ./... -count=1 -coverprofile=integration.out` + `audit_serial` 并集） | **total 81.3%** | **total 81.3%** |
| integration gate | `[OK]`：audit **91**/90、gateway **89**/80、identity **86**/85、flowstore **87**/85、store **80**/75、admin 83/70 … | 逐字相同，`[OK]` |

- 基线 profile：`generated/s11-gate/cov/before-3b1d4fb/{unit.out,integration.out,audit-serial.out}`
- 冻结 profile：`generated/s11-gate/cov/after-fa5969b/{unit.out,integration.out,audit-serial.out}`
- 判读输出已入库：`coverage-unit-before-3b1d4fb.txt` / `coverage-unit-after-fa5969b.txt` / `coverage-integration-before-3b1d4fb.txt` / `coverage-integration-after-fa5969b.txt`
- **为什么两次数字完全相等（不是"没测到"）**：本次改动只动了 `_test.go` 与 CI 文件；Go 的 coverprofile
  不统计 `_test.go`，所以对被测代码的覆盖**必然**逐字相同。diff 两次 gate 输出，唯一差异是 profile 路径。

---

## 5 · 复现命令（宿主照抄即可）

```sh
# 0) 环境（工具链 / 身份 / DSN 都在这里）
. /.keys/env.sh
cd /worktrees/GJ-QA                     # 或按冻结 sha 导出：git archive fa5969b | tar -x -C <dir>
git rev-parse HEAD                      # 期望 fa5969b1f0d1c67eae9f776760b64216d4518008

# 1) 独立测试库（库名必须以 _test 结尾；不碰共享 guanjin_test）
ADMIN_DSN=$(printf %s "$GUANJIN_TEST_DSN" | sed 's#/guanjin_test?#/postgres?#')
TEST_DSN=$(printf %s  "$GUANJIN_TEST_DSN" | sed 's#/guanjin_test?#/guanjin_s11_gate_test?#')
psql "$ADMIN_DSN" -c 'DROP DATABASE IF EXISTS guanjin_s11_gate_test WITH (FORCE)'
psql "$ADMIN_DSN" -c 'CREATE DATABASE guanjin_s11_gate_test OWNER guanjin_test'
MIGRATE=/.octop/workspaces/8WZP9H/generated/bin/migrate   # v4.19.1；CI 用 `make migrate-tool` 装到 ./bin
"$MIGRATE" -path migrations -database "$TEST_DSN" up
"$MIGRATE" -path migrations -database "$TEST_DSN" version    # 期望 21

# 2) 一次性 Redis（独立端口；共享 6379 会被别的项目污染，且会污染别人）
redis-server --port 16420 --bind 127.0.0.1 --requirepass 123456 \
  --save '' --appendonly no --daemonize no &
RPID=$!
redis-cli -h 127.0.0.1 -p 16420 -a 123456 --no-auth-warning ping        # PONG
kill -0 "$RPID"                                                          # 归属校验（FL-001）

# 3) 三道闸
go vet ./...
CGO_ENABLED=1 GUANJIN_TEST_DSN="$TEST_DSN" \
  GUANJIN_TEST_REDIS_ADDR=127.0.0.1:16420 GUANJIN_TEST_REDIS_PASSWORD=123456 \
  go test -tags=integration -race -p 8 -count=1 ./...
CGO_ENABLED=1 GUANJIN_TEST_DSN="$TEST_DSN" \
  GUANJIN_TEST_REDIS_ADDR=127.0.0.1:16420 GUANJIN_TEST_REDIS_PASSWORD=123456 \
  go test -tags 'integration audit_serial' ./internal/audit -p 1 -count=3

# 4) 覆盖率（unit / integration 双 profile）
COVERAGE_PROFILE=cov-unit.out COVERAGE_MIN=60 ./scripts/coverage-gate.sh
go test -tags=integration ./... -count=1 -coverprofile=cov-int.out
go test -tags 'integration audit_serial' ./internal/audit -p 1 -count=1 -coverprofile=cov-audit.out
COVERAGE_TAGS=integration COVERAGE_PROFILE=cov-int.out COVERAGE_EXTRA_PROFILE=cov-audit.out \
  COVERAGE_MIN=60 ./scripts/coverage-gate.sh

kill "$RPID"    # 收尾：端口不再应答即释放成功
```

一键脚本（我这边用的，参数化端口/库名）：`generated/s11-gate/{lib.sh,run-integration.sh,run-coverage.sh}`。

---

## 6 · S11 还差哪些判据（逐条，带归属）

| 判据 | 今晚状态 | 差什么 / 谁 |
|---|---|---|
| **AC-FLOW-05 判据 4**（并发 403/零新增/串行化） | **未闭环** | main 上只有存储层用例，且今晚实测**漏检**（只摘 advisory 锁仍绿，§3）；HTTP 层真守线在 **#120**（checks 全绿但 **OPEN 未合**）。→ Core 合 #120；Edge 修存储层用例的"缺行"前提（或明确降级为辅助）。合入后我按 §3 的注入复跑一遍做守线确认。 |
| **AC-AUD-04**（100 并发不断链，原记"未稳定"） | **本轮证据齐**：`-count=3` 192 PASS / AC_AUD_04 3/3 / b2 3/3 + 三次 `-p 8` 全量绿 | 按 D17 由我出复审记录转 PASS（账目改动走 `decisions`/账目表，不在这里自改结论）。 |
| **AC-ENG-07**（依赖漏洞门禁） | 门禁已落地（PR **#98** OPEN，`vuln` job SUCCESS） | 合入后我复跑 `scripts/check-vuln.sh`（含 `--self-test` 正控）并留证据。 |
| **AC-REG-04 后半句**（耗时差 ≤ 阈值） | 用例在 PR **#110 / #111**（同题两条，留一条），checks 绿 | 选一条合入；#111 是后提的，请主持人定留哪条（我不擅自关别人的 PR）。 |
| **AC-ENG-02**（migrate down 可回滚） | 已复跑通过（v21 → down -all → up2 → down2，schema_diff 0） | 无。 |
| **#124**（A 的裁决证据，纯 docs） | 被同一个 apt 步拖红 2 次 | 本修复合入后 rerun 即应绿；我盯到绿。 |
| 其余 6 N/A / 1 DEFERRED（AC-AUD-10） | 不变 | 无。 |

---

## 7 · 纪律声明

- **没有** `t.Skip` / `Skip` / `-short` / 删用例：`git diff 3b1d4fb fa5969b` 里测试文件只有
  `internal/identity/acflow09_db_test.go` 的**夹具去竞争**（拆分 `bindOnce` + 预先设定 resolver），
  断言一条没减（10 并发 × 403 / identifiers 0 新增 / visits 0 新增都还在）。
- **没有** force push、**没有**直推 main：本轮只在 `ci/s11-gate-closeout` 上提交；反向注入在临时分支且**从未推送**。
- 未触发 429 退避（全程无外网请求；`migrate`/`go` 走本地缓存）。
- 失败/异常都留了原文：`pre-fix-3b1d4fb.log`（race 红）、`negative-audit-3d21b64.log`（注入红）、
  `runtime-crash-internal-crypto-excerpt.log`（运行时崩溃）、`negative-check-0536a23.log`（守线漏检）。
