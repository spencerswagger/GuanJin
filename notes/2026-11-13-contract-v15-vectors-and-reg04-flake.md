# 2026-11-13 · 契约 v15 收尾两件（向量 CM-13~CM-17 / AC-REG-04 计时 flake）

基线：`origin/main` = `528a30f`（#154，契约 v15 / 判据 v19.2）。分支 `guanjin/qa-vectors-154`。

## 一、向量按 Spec 清单落（`spec/vectors/config-mapping.json`）

新增 5 条（`CM-13`~`CM-17`）+ Gate 补强 1 条（`CM-17B`）：

| 向量 | 键 / 值 | 契约期望 | **Go 实测** | **Java 实测** | 落位 |
|---|---|---|---|---|---|
| `CM-13` | `gateway.clock-skew="0s"` | accept，0ms，≠60000（补口①） | `reject@validate`（必须为正） | `reject@load`（必须为正） | **`pending_cases`**（正向欠账） |
| `CM-14` | `gateway.replay-window="0s"` | reject（**安全约束**，非解析） | `reject@validate` | `reject@load` | `reject_cases` |
| `CM-15` | `platform.connect-timeout="9999999999999h"` | reject（超 `int64` 纳秒） | `reject@load` | **`accept`** | `reject_cases` |
| `CM-16` | `gateway.clock-skew="\u00A02s"` | reject（NBSP 不 trim） | **`accept`** | `reject@load` | `reject_cases` |
| `CM-17` | `agent.pull.interval="\t30s\r\n"` | 30000 | accept | accept | `cases` |
| `CM-17B` | `agent.pull.interval="\t45s\r\n"` | 45000 | accept | accept | `cases` |

- `known_divergences.parse_forms` **已不在**（#151 起由 `duration_rule` 取代），本次不再重复登记。
- `current` 词表从 `accept|reject` 扩到五种：`accept` / `reject@load` / `reject@validate` /
  `reject@load:no-key` / `reject@validate:no-key`。加 `@validate` 是因为 **Go 的安全约束只在 `Config.Validate` 里**：
  `replay-window=0s` 与 `clock-skew=0s` 都是"装载成功、校验期失败"，笼统写 `reject` 会把两段混为一谈。
- **`CM-17` 单独证明不了「没回落默认」**：30s 恰是 #23 的默认值（`DefaultPullInterval`/`GuanjinConfigKeys` 都是 `30s`），
  即使一侧"没真解析、直接给默认"也是 30000。故补 `CM-17B`（45s ≠ 30s → 45000），这条是 Gate 加严、不在 Spec 清单里。
- **`CM-13` 属契约 vs 实现的真实分歧，已报 Spec**：补口① 明写 `clock-skew=0s` 合法（零容忍），但两侧校验器都按
  「必须为正」拒绝。要么 Nova 放开零值，要么改补口① 措辞——**Gate 不自行判定，故挂 `pending_cases` 而不是塞进 `cases`**。

## 二、AC-REG-04 计时 flake（`cmd/guanjind/e2e_reg04_db_test.go`）

**现象**（Nova 报）：docs-only PR 也红，`median_A=1.1ms` / `median_B=100.5ms`，重跑即绿。

**定性**：不是注册面的时序泄漏，是**测试环境**。B 路径要写 `challenges`，`-p 8` 跨包并行时别的包
对 `challenges`/`flows` 做 TRUNCATE/DROP 会拿 ACCESS EXCLUSIVE 锁 → B 整轮被锁等抬到几十上百毫秒；
A 路径（盲索引查不到就 409）不写表，所以不被拖（这正是 A 仍 1.1ms 的原因）。同一 family：`notes/flake-ledger.md` FL-002/FL-003。

**改法（测量方法，不动判据）**：
- 每轮 15 次 × 交替测量、丢前 3 次预热、取中位数（原 9 次/丢 2）；
- **判定要求可复现**：连续 **3 轮**都超阈值才红；任一轮在阈值内即通过并留 `noise_pass` 日志；最多 6 轮。
- 阈值仍**只从 `runtime_config.register.timing.max_diff_ms` 读**（缺键/类型错/非正 → 红），不回硬编码。
- 失败打印**所有轮次**的完整原始样本。
- 判据文字未动：`AC-REG-04` 只写「耗时差异 ≤ 阈值（阈值取自 `runtime_config`）」，「中位数 / 多少轮」是 D21③ 的测量方法。

**A/B（都实测）**：
- **结构性必须红**：在 `StartRegistration` 的"不存在"分支注入 `time.Sleep(60ms)` →
  3 轮全超（`65.906 / 65.983 / 71.987ms`）→ `--- FAIL`（3.9s）。还原后 `git status` 干净。
- **单轮噪声必须绿**：只在第 1 轮给 B 的样本人为 +60ms → pass1 超阈（64.858ms）、pass2 在阈内（4.737ms）
  → `noise_pass` + `PASS`（1.19s）。
- 健康态实测（真 PG17 + 独立 Redis，一次一条）：A ≈ 0.7–1.2ms、B ≈ 5.2–5.9ms，diff 4.5–5.3ms ≪ 50ms；单轮 0.6–0.7s。

**给 Spec 的话（要改判据文字就他改）**：判据现在隐含"一次测量即判定"，我把它实现成"三轮全红才判红"。
若 Spec 要把「3 轮 / 中位数」写进 `AC-REG-04` 文字，我按新文字再改；**在此之前我不动判据文件**。

## 三、复跑

- Go：`go vet ./internal/agent/ ./sdk/go/guanjin/ ./scripts/vecgen/` 干净；`internal/agent` + `sdk/go/guanjin` 全绿；`gofmt -l` 干净。
- `go vet -tags=integration ./cmd/guanjind/` 干净。
- Java：`mvn -pl guanjin-core test` → **84 tests / 0 fail**（`SpecVectorsConformanceTest` 6 个）。
- 向量消费者 A/B：把 `CM-15` 的 `current.java` 改成 `reject@load` → Java 红并点名；
  把 `CM-13` 的 `current.go` 改成 `accept` → Go 红并提示"升级成正例"。

## 四、给后续的话

- `CM-13`/`CM-15`/`CM-16` 的 `current` 是**现状钉桩**，不是判定标准。Nova 按补口①③ 改完 → 消费者立刻红，
  提示改表（`CM-13` 要求升级进 `cases` 按 `expect_after_fix` 断言）。**别用 skip/pending 掩盖**。
- `scope_cases` / `integrity_cases` 仍是 UNIMPLEMENTED，不得记 PASS。
