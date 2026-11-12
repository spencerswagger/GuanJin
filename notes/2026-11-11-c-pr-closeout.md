# 夜班 C 第二棒 —— 4 张 PR 收口 + 1 张互斥单关闭（2026-11-11）

执行人：架构 Ark。纪律：只走 `gh pr merge --merge`（不直推 main、不 force push、不改实现代码）。
本文件只记录**可复算的 sha 与命令**，评审分级见 Owl II，不在此复述。

## 1. main 前后 sha（按时间顺序，逐张）

| 序 | PR | 分支 | PR head | 合入前 main | 合并后 main（merge commit） |
|----|----|------|---------|-------------|------------------------------|
| 1 | #120 | `feat/s11-acflow05-concurrency-v2` | `0a6856b1b2bdcad135c1c1b63a2cffaf1efbab83` | `dded9a1f9a07f1c3c3e9c08850dd49d1a698f57d` | `2dbbbc1603e417eed3d71c53818375344a197faa` |
| 2 | #110 | `test/ac-reg-04-timing` | `a2e6221665e5d18a75206c48329986feab89094a` | `2dbbbc1603e417eed3d71c53818375344a197faa` | `e90c50111e45abb6b03b34fd067ffd9fd02c2f11` |
| 3 | #123 | `docs/d27-segment-anchor` | `6b0b6eef2173d98b7b7ee6f42ce8ee8ebae38d10` | `e90c50111e45abb6b03b34fd067ffd9fd02c2f11` | `b17c4ba38ff3933ee09b81fb4ca4ff0a0d9f8bff` |
| 4 | #124 | `docs/acflow05-concurrency-decision` | `0162631d61da0add910def528cab9126794ad56e`（原 `55a8720`，见下） | `b17c4ba38ff3933ee09b81fb4ca4ff0a0d9f8bff` | `ae6894029101821d43fbda31cc7c31268031486f` |

- 起点 main = `dded9a1`（PR #125 合入，CI 客户端工具改零网络解析）；终点 main = `ae68940`。
- **#124 的 head 变更说明**：原 head `55a8720` 的两次 `integration` 运行（`43050561856` / `43050572912`，15:39Z）
  红在**前置步骤**「等依赖就绪」——当时 CI 还在 `apt-get install postgresql-client redis-tools`；#125（15:57Z 后）
  才把它改成零网络解析。故用平台的标准操作 `PUT /repos/{owner}/{repo}/pulls/124/update-branch` 把 main 并入该分支
  （新 head `0162631`，**未 force push、未改写历史**），重跑后 `integration` 2m12s 通过。
- 全部 4 张合入时 `mergeable_state=clean`，各自 checks 全绿（`go` / `race` / `integration` / `web` / `sdk (8)` / `sdk (17)`）。

## 2. 互斥单 #111 关闭

- 关闭评论已发：<https://github.com/spencerswagger/GuanJin/pull/111#issuecomment-6803834272>，
  内容按派工原文写死「被 #110 取代 —— 两版判据强度实测等强（同一 60ms 侧信道注入下 58.1ms vs 58.6ms 均报红、
  缺键均报红）；#111 取样为先 9 次 A 后 9 次 B，系统性漂移整段压到一组；且 #111 会把全局判据参数 UPDATE 到 51
  再还原，进程被杀会留下被放宽的值」。
- 状态：`CLOSED`（`gh pr view 111 --json state` → `CLOSED`）。

## 3. 合并后 main 不因合并变红（两条独立证据）

### 3.1 本地真库 + 真 Redis（`-tags=integration -race`）

```
# 代码树 = ae68940（终点 main），GOMAXPROCS 未特殊设定
export GUANJIN_TEST_DSN="postgres://jinliang:jinliang@127.0.0.1:5432/guanjin_test?sslmode=disable"
export GUANJIN_TEST_REDIS_ADDR="127.0.0.1:16379"
go test -tags=integration -race ./internal/... -count=1 -p 1
# → 21 包 ok（authz/testdb 无测试文件），0 FAIL
go test -tags=integration -race -run 'TestACFLOW05' ./cmd/guanjind/ -count=1 -v
# → 6/6 PASS（04 并发 / 04b 缺行 fail-closed / 01 / 02 / 03 / 02b）
go test -tags=integration -race -run 'TestE2E_AC_REG_04' ./cmd/guanjind/ -count=1 -v
# → 2/2 PASS：DuplicateEmailIs409 … PASS；TimingDiffWithinThreshold PASS
#    threshold_ms=50（来自 runtime_config）；median_A=1.899 median_B=4.203 diff=2.304 ms
```

首次运行时 `internal/store` 的 `TestDBAuditCreatedIDIndexOnParentAndEveryPartition` 红过一次，原因是**本地库漂移**：
本机 `guanjin_test` 是重灌库、`schema_migrations` 为空表，`audit_logs_created_id_idx`（迁移 `000021`）与
`register.timing.max_diff_ms`（迁移 `000019`）两个 seed/DDL 都没落。把这两份**迁移原文**用 `psql -f` 补到本地库后
（`000021` 建索引成功、`pg_inherits` 子索引 7 份；`000019` `INSERT 0 1`），同一 sha 上重跑即全绿。
**这不是合并引入的红**，CI 的 `integration` job 走 `golang-migrate up`，天然到最新版本。

### 3.2 main push CI（权威环境）

- `b17c4ba`（#120 + #110 + #123 后）：run `43076989128` → **success**
- `ae68940`（#124 合入，终点 main）：run `43078024758` → **success**，
  逐 job：`go` / `race` / `integration` / `web` / `sdk (8)` / `sdk (17)` 全 success。

## 4. 本轮**不做**的（照派工原文）

- 不合 #51（Nova，P0：`sdk/go/guanjin/jwks.go:122-128` Refresh 只 merge 不删）、不合 #98（Gate，P0：`.github/workflows/ci.yml:268-272` 扫 go1.25.13 而构建用 go.mod）、不合 #72（draft 且与 main 的 `docs/decisions.md` 有真冲突，等拍板）。
- 不顺手改实现代码。

## 5. 记下的 P1 待办（#123 条款措辞，不在本轮修）

- 现文写「与 `DROP PARTITION` 同一事务」：PG **没有 `DROP PARTITION` 这个语法**，且 `DETACH PARTITION … CONCURRENTLY`
  **不能进事务块**；应明写「用 `DROP TABLE <分区>`」。落点 `docs/decisions.md` 的 D27 段，由文档 owner 修。
