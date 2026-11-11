# 夜班任务 A（2026-11-11）：AC-FLOW-05 判据 4「并发」两版取一

结论：**留 #120（head `0a6856b`），关 #115（head `23be9e5`，已 CLOSED）**。
两版性能/内存差异在噪声内（20 并发档位差 5.8%，8 并发档位差 0.5%），
按「**宁严不宽**」取**断言更严、覆盖判据更多**的一版：#120 有判据 4 的两条用例并能挡住真冲突，#115 对判据 4 零覆盖。

## 0. 两版到底是什么（先纠正一个前提：不是「两个并发实现」，是超集关系）

| PR | head sha | 改动文件 | AC-FLOW-05 用例数 | 判据 4（并发）覆盖 |
|---|---|---|---|---|
| **#120** | `0a6856b1b2bdcad135c1c1b63a2cffaf1efbab83` | `cmd/guanjind/acflow05_db_test.go`（改）+ `cmd/guanjind/acflow05_04_concurrency_db_test.go`（新，284 行） | **6**（01/02/02b/03/**04**/**04b**） | **有**：20 并发交替 PATCH + 缺行 fail-closed |
| **#115** | `23be9e524809fc9c4d853eb67e976d92fda2d74d` | 只有 `cmd/guanjind/acflow05_db_test.go` | **4**（01/02/02b/03） | **无**（文件里只有一句"判据 4"注释，没有任何并发用例） |

- `git show 23be9e5:cmd/guanjind/acflow05_db_test.go | grep -n 'TestACFLOW05_04\|Concurren'` → **空**。
- `git diff --stat 23be9e5 0a6856b -- cmd/guanjind/acflow05_db_test.go` → 只差**一个 hunk**
  （#120 把夹具还原从 `DELETE`+`UPDATE` 改成 **upsert**）；其余内容（422 / `INVALID_REQUEST` / `02b`）**完全相同**。
- ⇒ **#115 的全部内容都是 #120 的子集**；#120 多出「判据 4 的两条用例 + 夹具还原修复」。

## 1. 参数冻结（要按 sha 复算只看这一节）

- 机器：8 vCPU / 96 GiB，`Linux 5.15.0-202-generic x86_64`；Go `1.25.7`；PostgreSQL `17.11`；Redis `8.0.2`。
- **`GOMAXPROCS=8` 显式固定**（= NumCPU，避免运行时默认漂移；第一批未显式固定的数字见 §6，结论一致）。
- 并发档位：**20**（判据 4 的原档位，20×50=1000 请求，这是主证据）与 **8**（敏感性档位，8×50=400 请求）。
- 时长：不设固定时长，跑满请求数即结束（实测每轮 1.2–8.2 s，逐轮 `elapsed_s` 在原始日志里）。
- 库：**每次从 22 个 `migrations/*.up.sql` 逐个 `psql -f` 新建的空库** `gj_acf05_measure`（`GUANJIN_TEST_DSN` 指过去）；
  seed 校验 `challenge.*` / `flow.ttl_seconds` / `factors.disabled=[]` / `register.timing.max_diff_ms=50` 全在位。
- 被测树（每次测量前 `git rev-parse HEAD` 取值）：
  - **#120 树** `33033d4edc4c4fb8095c7309b879cfc456798c01` = `origin/main`（`3b1d4fb9d1073e5c4d5278771d411b5d0a439ee0`）+ `0a6856b` 的两个测试文件
  - **#115 树** `de34f8bef678cbfae79aaa9ae5400520782b1aa2` = `origin/main`（`3b1d4fb`）+ `23be9e5` 的 `acflow05_db_test.go`

复算命令（原样）：

```sh
# 1) 被测树
git checkout -B tmp/meas-120 origin/main && git checkout 0a6856b -- \
  cmd/guanjind/acflow05_db_test.go cmd/guanjind/acflow05_04_concurrency_db_test.go
git checkout -B tmp/meas-115 origin/main && git checkout 23be9e5 -- cmd/guanjind/acflow05_db_test.go

# 2) 空库 + 全量迁移
ADMIN=$(echo "$GUANJIN_TEST_DSN" | sed 's#/guanjin_test?#/postgres?#')
psql "$ADMIN" -c "DROP DATABASE IF EXISTS gj_acf05_measure" -c "CREATE DATABASE gj_acf05_measure"
MEAS=$(echo "$GUANJIN_TEST_DSN" | sed 's#/guanjin_test?#/gj_acf05_measure?#')
for f in $(ls migrations/*.up.sql | sort); do psql "$MEAS" -q -v ON_ERROR_STOP=1 -f "$f" >/dev/null; done

# 3) 压测夹具（原样：notes/2026-11-11-acflow05-harness.go.txt → cmd/guanjind/zz_acflow05_measure_db_test.go）
cp notes/2026-11-11-acflow05-harness.go.txt cmd/guanjind/zz_acflow05_measure_db_test.go

# 4) 每一轮（共 6 轮/版：conc=20 与 conc=8 各 3 轮）
GOMAXPROCS=8 GUANJIN_TEST_DSN="$MEAS" GUANJIN_TEST_REDIS_ADDR=127.0.0.1:6390 \
ZZAF05_LABEL=120-c20 ZZAF05_CONC=20 ZZAF05_ROUNDS=50 \
  go test -tags=integration -run TestZZAF05Measure ./cmd/guanjind/ -count=1 -v
```

## 2. 2026-11-11 22:05 批次（`GOMAXPROCS=8` 冻结）

### 2.1 20 并发档位（主证据，1000 次 `PATCH`）

| 指标 | #120 树 | #115 树 |
|---|---|---|
| 失败请求 / 状态码 | **0** / `200:1000` | **0** / `200:1000` |
| 错误码分布 | 全 200（无 code） | 全 200（无 code） |
| 吞吐 req/s（中位） | **298.7**（298.7 / 296.3 / 360.0） | **281.4**（122.1 / 281.4 / 299.0） |
| p50 / p95（ms，中位） | 52.7 / 105.4 | 57.3 / 123.4 |
| RSS 峰值 VmHWM（中位） | **30 048 KB**（30544/29912/30048） | **30 220 KB**（30068/30372/30220） |
| 审计行数（真实变更数） | 987 / 991 / 987 | 991 / 991 / 987 |
| **审计链断点 / 重复写** | **0 / 0** | **0 / 0** |
| 链尾 vs 库终值 | 一致 | 一致 |

原始日志（三份，各自独立）：
- `notes/acflow05-2026-11-11/pin120-c20-run1.log`
- `notes/acflow05-2026-11-11/pin120-c20-run2.log`
- `notes/acflow05-2026-11-11/pin120-c20-run3.log`
- `notes/acflow05-2026-11-11/pin115-c20-run1.log`
- `notes/acflow05-2026-11-11/pin115-c20-run2.log`
- `notes/acflow05-2026-11-11/pin115-c20-run3.log`

### 2.2 8 并发档位（敏感性，400 次 `PATCH`）

| 指标 | #120 树 | #115 树 |
|---|---|---|
| 失败 / 错误码 | 0 / 全 200 | 0 / 全 200 |
| 吞吐 req/s（中位） | 275.7（323.1/275.7/252.6） | 274.4（274.4/268.6/290.6） |
| RSS 峰值（中位） | 25 956 KB | 26 012 KB |
| 审计链断点 | 0 | 0 |

日志：`notes/acflow05-2026-11-11/pin120-c8-run{1,2,3}.log`、`pin115-c8-run{1,2,3}.log`。

## 3. 故障注入：真冲突长什么样，谁能挡住

注入方式（只改工作树、不提交；`internal/store/repo_platform_factors.go`）：
```sh
sed -i 's/if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, platformFactorLockKey).Error; err != nil {/if err := error(nil); err != nil {/' internal/store/repo_platform_factors.go
sed -i 's/WHERE "key" = ? FOR UPDATE/WHERE "key" = ?/' internal/store/repo_platform_factors.go
```
（等于回到 2026-11-11 缺陷形态：既没有 advisory 锁，也没有行锁。）

20 并发档位、同参数、3 轮（`GOMAXPROCS=8`）：

| 指标 | 数值（中位） | 三轮原始 |
|---|---|---|
| HTTP 失败 / 错误码 | **0** / 全 200 | 0/0/0 |
| 吞吐 req/s | 551.9 | 551.9 / 412.1 / 556.0 |
| RSS 峰值 | 30 092 KB | 30092/30492/30000 |
| **审计行数**（健康 ≈ 990） | **664** | 673 / 664 / 663 |
| **审计链断点** | **316** | 312 / 322 / 316 |

日志：`notes/acflow05-2026-11-11/pin-inject-c20-run{1,2,3}.log`。

⇒ **冲突是真的，而且是静默的**：1000 个请求**全部 200**，但库里只剩 664 条审计、链上 316 处
`before` ≠ 上一条 `after`（丢写 + 一条真实变更记错）。**只看失败率/错误码会判"健康"。**

谁能挡住（同一注入下跑 AC-FLOW-05 全量）：

| 场景 | #120 树 | #115 树 |
|---|---|---|
| 健康实现 | 6/6 PASS | 4/4 PASS |
| **注入故障** | **FAIL**：`--- FAIL: TestACFLOW05_04_ConcurrentPatches`；`acflow05_04_concurrency_db_test.go:171: 审计链不自洽：第 2 条 before="true"，但前一条 after="false"（丢写或重复记）` | **PASS 4/4**，日志里 `并发/不自洽/chain` 命中 **0** 行 → **漏检** |

日志：`notes/acflow05-2026-11-11/120-suite.log`、`115-suite.log`、`120-suite-injected.log`、`115-suite-injected.log`。

## 4. 噪声条款：为什么按「严」取版、放弃了哪项指标

- 实测差异：20 并发档位吞吐差 **5.8%**（298.7 vs 281.4），8 并发档位差 **0.5%**（275.7 vs 274.4）；
  RSS 峰值差 **0.6%** / **0.2%**。**两版各自的三轮离散度远大于版间差**（#115 的 20 并发档位三次是 122.1 / 281.4 / 299.0，跨度 145%）。
- 因此本裁决**明确放弃**用「吞吐」和「峰值内存」作为选版指标（两者都在噪声内，且方向不稳定：
  第一批未固定 GOMAXPROCS 时是 #120 略高，固定后差异进一步缩小）。
- 选版依据改为**严格性**：判据 4 的覆盖（6 条 vs 4 条）与**故障注入下是否报红**（#120 报红点名、#115 全绿）。
  即「宁严不宽」：宁可留断言更严的那版，不为了 0.5%–6% 的吞吐放弃对静默丢写的检测。
- 反证：**坏实现（无锁）吞吐 551.9 req/s，是健康实现（298.7）的 1.85 倍** —— 吞吐越高恰说明串行化被去掉了，
  用它选版会把坏实现选成"更好"。

## 5. 裁决与处置

- **留 #120**（超集 + 判据 4 覆盖 + 夹具还原改 upsert），head `0a6856b` 未改动。
- **关 #115**：评论已写「被 #120 取代 + 数字 + 复现命令」（2026-11-11 14:57Z 关闭）。
- 未改任何实现；两版都只是 `*_test.go`。

## 6. 第一批（未显式固定 GOMAXPROCS，结论一致，留档）

`GOMAXPROCS` 未设置（= NumCPU 8），参数同为 20×50：

| 指标（中位） | #120 树 | #115 树 | 注入故障 |
|---|---|---|---|
| 吞吐 req/s | 268.3（283.3/268.3/171.8） | 242.4（253.5/239.0/242.4） | 416.3（416.3/512.5/412.4） |
| RSS 峰值 | 30 128 KB | 30 308 KB | 29 608 KB |
| 审计行数 | 991 | 989 | 656 |
| 审计链断点 | 0 | 0 | 325 |

原始日志（本机 `artifacts/meas/`，gitignored）：`120-run{1,2,3}.log`、`115-run{1,2,3}.log`、`inject-run{1,2,3}.log`。
与 §2 冻结批次结论一致：两版健康指标不可区分；注入后 #120 红、#115 绿。

## 7. 夹具说明

压测夹具原样存档：`notes/2026-11-11-acflow05-harness.go.txt`（`//go:build integration`，放到 `cmd/guanjind/` 即可编译）。
它只调用两版共有的生产装配（`newFactorsAdminWiring`/`testConfig`/`openTestDB`/`fixedResolver`），
不引用任何一版的测试助手，所以同一份文件在两版树上原样编译 —— 变量只有「被测量的树」。
