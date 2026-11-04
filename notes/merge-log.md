# 合入登记（客观事实）

本文件只登记**可复现的事实**：合并提交、时间、触发账号、门禁结论、评审状态。
不含推断、不含过程叙述。新增条目按时间追加，不改既有条目（与 `migrations/` 的"只增不改"同口径）。

## 取数命令

```bash
git log -1 --format='%H|%an <%ae>|%ci' <merge-commit>
gh pr view <PR> --json mergedAt,mergedBy,mergeCommit,statusCheckRollup
```

## 条目

### PR #29 —— S5 因子与挑战面（合入时未取得评审 READY）

| 项 | 值 |
|---|---|
| 合并提交 | `fa143b908d5dc0732ff47a07545656e15d4a6a33` |
| 提交者 | `SpencerSwagger <51152244+spencerswagger@users.noreply.github.com>` |
| 提交时间 | `2026-11-05 00:34:39 +0800`（= `2026-11-04T16:34:39Z`） |
| `mergedAt` | `2026-11-04T16:34:39Z` |
| `mergedBy` | `spencerswagger`（`is_bot=false`，GitHub 用户 `51152244`） |
| 合并方式 | merge commit（`fa143b9` 的父提交含 `55d49b6`） |
| 门禁结论（`statusCheckRollup`） | 8 个 check run 全部 `conclusion=SUCCESS`：`go`×2、`race`×2、`integration`×2、`web`×2（同一次推送的 `push` 与 `pull_request` 两个 run，均 success） |
| 评审状态 | 合入时无 `READY` 评审结论（PR 评论中只有作者自检清单，作者为 `spencerswagger` 账号） |

> 事实补充：本次合并由认证为 `spencerswagger` 的账号经 API 触发；`mergedAt` 与 `git log` 的提交时间一致（同一秒）。

### PR #32 —— S3/S4 补课（注册面 + 身份覆盖率 + 账号级失败计数）

| 项 | 值 |
|---|---|
| 合并提交 | `8cd751a1b0fd6f77e08dfa81952ce38387c0b860` |
| 提交者 | `SpencerSwagger <51152244+spencerswagger@users.noreply.github.com>` |
| 提交时间 | `2026-11-05 01:50:41 +0800`（= `2026-11-04T17:50:41Z`） |
| `mergedAt` | `2026-11-04T17:50:42Z` |
| `mergedBy` | `spencerswagger`（`is_bot=false`） |
| 门禁结论 | `go` / `integration` / `race` / `web` 四 job 全 `SUCCESS` |
| 评审状态 | PR #32 评论中记录了两轮评审意见与处置；最终评论的结论为 `P0 = 0` |

### PR #35 —— D15④ 信封完整性失败分流

| 项 | 值 |
|---|---|
| 合并提交 | `7ea144c0374f3bc1b5a32e6754938efdd3c57696` |
| 提交者 | `SpencerSwagger <51152244+spencerswagger@users.noreply.github.com>` |
| 提交时间 | `2026-11-05 02:44:52 +0800`（= `2026-11-04T18:44:52Z`） |
| `mergedAt` | `2026-11-04T18:44:53Z` |
| `mergedBy` | `spencerswagger`（`is_bot=false`） |
| 门禁结论 | `go` / `integration` / `race` / `web` 四 job 全 `SUCCESS` |
| 评审状态 | PR #35 评论中记录了评审意见与处置（含一条 P1 修复） |
