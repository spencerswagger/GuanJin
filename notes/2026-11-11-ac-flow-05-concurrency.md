# 夜班任务 A（2026-11-11）：AC-FLOW-05 判据 4「并发」两版取一

结论：**留 #120（head `0a6856b`），关 #115（head `23be9e5`）**。
理由不是倾向：在同一棵树（`origin/main` = `3b1d4fb`）上只换测试文件，用**同一次故障注入**复现真冲突，
#120 精确报红点名到用例，#115 全绿（它对判据 4 零覆盖）。性能与内存两版无差别（差在噪声内）。

## 0. 两版到底是什么（先纠正一个前提）

| PR | head sha | 改动文件 | AC-FLOW-05 用例数 | 判据 4（并发）覆盖 |
|---|---|---|---|---|
| **#120** | `0a6856b1b2bdcad135c1c1b63a2cffaf1efbab83` | `cmd/guanjind/acflow05_db_test.go`（改）+ `cmd/guanjind/acflow05_04_concurrency_db_test.go`（新，284 行） | **6**（01/02/02b/03/**04**/**04b**） | **有**：20 并发交替 PATCH + 缺行 fail-closed |
| **#115** | `23be9e524809fc9c4d853eb67e976d92fda2d74d` | 只有 `cmd/guanjind/acflow05_db_test.go` | **4**（01/02/02b/03） | **无**（文件里只有一句"判据 4"的注释，没有任何并发用例） |

- `git show 23be9e5:cmd/guanjind/acflow05_db_test.go | grep -n 'TestACFLOW05_04\|Concurren'` → **空**；
  `git diff --stat 23be9e5 0a6856b -- cmd/guanjind/acflow05_db_test.go` → 两版该文件只差**一个 hunk**
  （#120 把夹具还原从 `DELETE`+`UPDATE` 改成 **upsert**），其余内容（422 / `INVALID_REQUEST` / `02b` 行为交叉验证）**完全相同**。
- 即：**#115 的全部内容都是 #120 的子集**；#120 多出「判据 4 的两条用例 + 夹具还原修复」。
  所以「两版互斥」实际是「一版是另一版的子集 + 判据 4 缺失」。

## 1. 环境与复算方式（同一台机、同一参数）

- 机器：8 vCPU / 96 GiB RAM，`Linux 5.15.0-202-generic x86_64`；Go `1.25.7`；PostgreSQL `17.11`；Redis `8.0.2`。
- 库：**每次从 22 个 `migrations/*.up.sql` 逐个 `psql -f` 建的空库** `gj_acf05_measure`（`GUANJIN_TEST_DSN` 指过去），
  不依赖任何既有数据；seed 校验：`challenge.*` / `flow.ttl_seconds` / `factors.disabled=[]` / `register.timing.max_diff_ms=50` 全部就位。
- 压测夹具：`cmd/guanjind/zz_acflow05_measure_db_test.go`（**不入库**，本文件同目录存档 `artifacts/meas/harness.go.bak`；
  只调用两版共有的生产装配 `newFactorsAdminWiring`/`testConfig`/`openTestDB`/`fixedResolver`，不引用任何一版的测试助手）。
- 并发模型：**20 goroutine × 50 请求 = 1000 次 `PATCH /api/v1/factors/{type}`**，全局原子计数交替 `enabled=true/false`，
  每请求独立 `http.Client` 连接池（`MaxIdleConnsPerHost=40`），60s 超时；每版跑 **3 次**取中位数。
- 复算命令（两版树都是 `origin/main` + 对应 PR 的测试文件）：
  ```sh
  # 被测树（本地临时分支，未 push）
  git checkout -B tmp/meas-120 origin/main   # 3b1d4fb
  git checkout 0a6856b -- cmd/guanjind/acflow05_db_test.go cmd/guanjind/acflow05_04_concurrency_db_test.go
  git checkout -B tmp/meas-115 origin/main
  git checkout 23be9e5 -- cmd/guanjind/acflow05_db_test.go

  GUANJIN_TEST_DSN=postgres://…/gj_acf05_measure?sslmode=disable \
  GUANJIN_TEST_REDIS_ADDR=127.0.0.1:6390 GUANJIN_TEST_REDIS_PASSWORD=… \
  ZZAF05_LABEL=120-healthy ZZAF05_CONC=20 ZZAF05_ROUNDS=50 \
	  go test -tags=integration -run TestZZAF05Measure ./cmd/guanjind/ -count=1 -v
  ```
- 被测树的 sha（**两版各自的测试文件落在同一 main 上的树**）：
  - `tmp/meas-120` = `33033d4edc4c4fb8095c7309b879cfc456798c01`
  - `tmp/meas-115` = `de34f8bef678cbfae79aaa9ae5400520782b1aa2`
  - 基点 `origin/main` = `3b1d4fb9d1073e5c4d5278771d411b5d0a439ee0`

## 2. 数字（3 次，中位数）

### 2.1 健康实现（main 现状：advisory 锁 + 行锁）

| 指标 | #120 树 | #115 树 |
|---|---|---|
| 失败请求 / 状态码分布 | **0** / `200:1000` | **0** / `200:1000` |
| 错误码分布 | 无（全 200） | 无（全 200） |
| 吞吐 req/s（中位数，三次原始 283.3 / 268.3 / 171.8） | **268.3** | **242.4**（253.5 / 239.0 / 242.4） |
| p50 / p95（ms） | 59.1 / 144.3 | 69.3 / 136.7 |
| RSS 峰值 VmHWM（中位数） | **30 128 KB** | **30 308 KB** |
| 审计行数（真实变更数） | 991（三次 991/991/993） | 989（989/989/989） |
| **审计链断点 / 重复写** | **0 / 0** | **0 / 0** |
| 链尾 vs 库终值 | 一致 | 一致 |

→ 两版吞吐差 25.9 req/s（10%），**小于各版自身三次跑动的离散度**（#120 自己是 171.8–283.3，跨度 65%）；
RSS 差 180 KB（0.6%）。**性能/内存上两版不可区分**，不能据此取一版。

### 2.2 故障注入（复现"真冲突"）：去掉 advisory 锁 + 去掉 `FOR UPDATE`

注入方式（`internal/store/repo_platform_factors.go`，只改工作树、不提交）：
```sh
sed -i 's/if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, platformFactorLockKey).Error; err != nil {/if err := error(nil); err != nil {/' internal/store/repo_platform_factors.go
sed -i 's/WHERE "key" = ? FOR UPDATE/WHERE "key" = ?/' internal/store/repo_platform_factors.go
```

同参数（20×50=1000 次 PATCH）三跑：

| 指标 | 数值（中位数） | 三次原始 |
|---|---|---|
| HTTP 失败 / 错误码 | **0** / 全 200 | 0 / 0 / 0 |
| 吞吐 req/s | 416.3 | 416.3 / 512.5 / 412.4 |
| p50 / p95（ms） | 16.5 / 166.7 | — |
| RSS 峰值 | 29 608 KB | 29 608 / 28 784 / 29 660 |
| **审计行数**（健康应为 ≈991） | **656** | 656 / 647 / 661 |
| **审计链断点** | **325** | 325 / 334 / 312 |

→ **冲突是真的**：1000 个请求**全部 200**（对外零错误），但库里的审计只有 656 条、链上 325 处
`before` 与上一条 `after` 对不上 = **丢写 + 一条真实变更记错**。这就是判据 4 要挡的东西，
且它**不会**以 HTTP 失败的形式出现 —— 只看失败率/错误码会得出"没问题"。
（顺带：坏实现的吞吐**更高**（416 vs 268 req/s，因为不串行化），只用吞吐选版会把坏实现选成"更好"。）

## 3. 谁能挡住这个冲突（这才是取一版的判据）

| 场景 | #120 树 | #115 树 |
|---|---|---|
| 健康实现跑 AC-FLOW-05 全量 | **6/6 PASS**（含 `TestACFLOW05_04_ConcurrentPatches`、`04b_MissingRowIsFailClosed`） | **4/4 PASS**（无判据 4 用例） |
| **注入故障后**跑同一套 | **FAIL**：`--- FAIL: TestACFLOW05_04_ConcurrentPatches (0.14s)`；`acflow05_04_concurrency_db_test.go:171: 审计链不自洽：第 2 条 before="true"，但前一条 after="false"（丢写或重复记）` | **PASS 4/4**，日志里 `并发/不自洽/chain` 命中 **0** 行 → **漏检** |

原始日志：
- `artifacts/meas/120-suite.log`（健康，6 PASS）
- `artifacts/meas/115-suite.log`（健康，4 PASS）
- `artifacts/meas/120-suite-injected.log`（注入 → 红，点名到用例）
- `artifacts/meas/115-suite-injected.log`（注入 → 绿，漏检）
- `artifacts/meas/120-run{1,2,3}.log` / `115-run{1,2,3}.log` / `inject-run{1,2,3}.log`（压测原始 JSON 行）

## 4. 裁决与处置

- **留 #120**：它是 `#115` 的**超集**（同样的 422 / `INVALID_REQUEST` / `02b`）+ 判据 4 的两条用例 +
  夹具还原改 upsert（`#115` 在"行本来不存在"时是 `DELETE`，还原用 `UPDATE` —— 对不存在的行是 0 行、静默不还原，
  会让同包后续夹具读不到 required 启动键 `factors.disabled`）。
- **关 #115**：评论「被 #120 取代」+ 上面第 2/3 节数字与复现命令。
- 未改任何实现；两版都只是 `*_test.go`。`#120` 的 head sha 保持 `0a6856b` 不动（本次测量不改它的树）。

## 5. 原始数据（压测 JSON 行，原样）

```
120-healthy run1: {"conc":20,"rounds":50,"requests":1000,"failures":0,"statuses":{"200":1000},"codes":{"(none)":1000},"elapsed_s":3.529859264,"rps":283.2974136387552,"p50_ms":57.019,"p95_ms":135.05,"max_ms":512.728,"rss_peak_kb":30224,"audit_rows":991,"chain_breaks":0,"dup_writes":0,"final_match":true}
120-healthy run2: {"elapsed_s":3.726824767,"rps":268.32493141473213,"p50_ms":59.132,"p95_ms":144.298,"max_ms":455.238,"rss_peak_kb":30056,"audit_rows":991,"chain_breaks":0,"dup_writes":0,"final_match":true}
120-healthy run3: {"elapsed_s":5.819774175,"rps":171.82797303299145,"p50_ms":96.073,"p95_ms":190.783,"max_ms":562.948,"rss_peak_kb":30128,"audit_rows":993,"chain_breaks":0,"dup_writes":0,"final_match":true}
115-healthy run1: {"elapsed_s":3.944259628,"rps":253.53300601742234,"p50_ms":63.109,"p95_ms":128.519,"max_ms":529.819,"rss_peak_kb":30308,"audit_rows":989,"chain_breaks":0,"dup_writes":0,"final_match":true}
115-healthy run2: {"elapsed_s":4.183398628,"rps":239.04009369484356,"p50_ms":69.31,"p95_ms":136.729,"max_ms":520.182,"rss_peak_kb":30056,"audit_rows":989,"chain_breaks":0,"dup_writes":0,"final_match":true}
115-healthy run3: {"elapsed_s":4.125769528,"rps":242.3790260734118,"p50_ms":70.19,"p95_ms":144.923,"max_ms":509.075,"rss_peak_kb":30360,"audit_rows":989,"chain_breaks":0,"dup_writes":0,"final_match":true}
inject run1:     {"elapsed_s":2.402186793,"rps":416.28736071400095,"p50_ms":17.566,"p95_ms":166.697,"max_ms":613.863,"rss_peak_kb":29608,"audit_rows":656,"chain_breaks":325,"dup_writes":0,"final_match":true}
inject run2:     {"elapsed_s":1.9511754749999999,"rps":512.5115669055855,"p50_ms":15.655,"p95_ms":134.384,"max_ms":419.54,"rss_peak_kb":28784,"audit_rows":647,"chain_breaks":334,"dup_writes":0,"final_match":true}
inject run3:     {"elapsed_s":2.424691318,"rps":412.42363206251235,"p50_ms":16.454,"p95_ms":169.891,"max_ms":692.524,"rss_peak_kb":29660,"audit_rows":661,"chain_breaks":312,"dup_writes":0,"final_match":true}
```
（每行都省略了 `"label"`、`"statuses"`、`"codes"`、`"chain_structural_broken":false`、`"final_db_disabled"/"final_chain_disabled":true` 等恒等字段；
完整行见本目录存档的原始日志。三次中位数由上面的 rps/audit_rows/chain_breaks 直接取中位。）

压测夹具原样存档在同目录 `2026-11-11-acflow05-harness.go.txt`（放到 `cmd/guanjind/` 下即可编译，`//go:build integration`）。

