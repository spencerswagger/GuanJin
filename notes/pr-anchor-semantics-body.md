## 是什么问题（P0，线上会"每天一假告警"）

`RunRange` 与 `AnchorCurrentMonth` 对锚点键的语义**相反**：

- `AnchorCurrentMonth` 写的是**本月链尾记录的 `integrity_tag`**（该月"封条"）；
- `RunRange` 却把它当成**本段首条记录应有的 `prev_tag`** 传给 `chain.Verify`。

后果：封锚之后同一月再复核，第 0 条的 `prev_tag` 必然 ≠ 本月链尾 tag → **恒报 `prev_tag_mismatch`**，
`audit_chain_broken_total` 每天 +1、`anchor_age` 停在旧值。真断链会被每天的假告警淹没，
校验 Job 等于失效（AC-AUD-11 的判据本身也就失去了意义）。

同一根因的另一面：**上月没有锚点**时，段首 `prev_tag` 是"未知"，旧实现当成"应为 nil"，
非首月第一次复核同样假红。

## 怎么修的

唯一口径：**`anchor[M]` = 第 M 月最后一条已复核记录的 `integrity_tag`（封条）**。

| 位置 | 改动 |
|---|---|
| `RunRange` 段首 | 期望 `prev_tag` 取 **`anchor[M-1]`**（不是 `anchor[M]`） |
| `RunRange` 段尾 | 若 `anchor[M]` 已存在，最后一条 `integrity_tag` 必须等于它（整段尾部被删时只有锚点能发现） |
| 缺 `anchor[M-1]` | 段首 prev 视为**未知**：`Chain.VerifyFrom(..., checkFirst=false)` 跳过首条 prev 判定，但**仍按内容重算 tag**；只有"该月之前确实没有任何记录"时才坚持判 `prev=nil` |
| 错误分支 | 段首/段尾两个 `anchors.Get` 的错误各自带前缀返回（"锚点丢了"不许退化成"首段正常"） |

顺带（P2 ②）：`canonical` 把 `int / int64 / float64 / json.Number` 统一成最短可逆十进制，
避免"同值不同写法"算出不同 `integrity_tag`；超出 float64 范围的 `json.Number` 退回原文（仍然确定）。

## 明确拆出去的部分

P2 ①（`occurred_at` **上界守卫** `ErrOccurredAtTooNew`）**不在本 PR**：它要求把审计测试的月份口径
整体从"2040+"改成"当下月"，与现有 `freshMonth` 基建冲突；而它改的是**产品写路径行为**，
不该混进一个 P0 语义修复里。单独一个 PR 做（届时配套测试月份改造）。

## 证据（本地真 PG17 `guanjin_test` + 真 Redis 7）

**先红后绿**（同一条命令，只差产品代码）：

```
# 仅用例提交 37da7e0（不改产品代码）
go test -tags 'integration audit_serial' ./internal/audit -run TestAnchorContinuity -count=1 -v
  anchor_continuity_serial_db_test.go:85: 封锚后同一月再复核报了断链：index=0 reason=prev_tag_mismatch
  --- FAIL: TestAnchorContinuitySameMonth (0.05s)

# 本 PR HEAD
--- PASS: TestAnchorContinuitySameMonth (0.06s)
```

| 项 | 结果 |
|---|---|
| 单测 | 全绿（`go test ./... -count=1`） |
| 集成 `-tags=integration -race -p 8 -count=1` | 39s / `--- FAIL` 0 |
| 串行套件 `-tags 'integration audit_serial' -p 1 -count=3` | 9.3s / 全绿（含 AC-AUD-04、b1/b2 探针） |
| 覆盖率（§9.3 逐模块，integration+s erial 并集） | audit **93%**（下限 90）、总 80.8%，其余模块全达标 |
| `gofmt -l` / `go vet ./...` | 空 / 干净 |

用例侧：
- `anchor_continuity_serial_db_test.go`（真库）：封锚后同月再复核 → 不报断链、`audit_chain_broken_total` 不增、`anchor_age` 基准被刷新；
- `verify_unit_test.go`（纯计算）：段首 prev 未知时**不判 prev 但判内容**（改内容照样红）、checkFirst=true 时首条必断、不可序列化内容报 canonical 错误、超范围数字编码确定、Job 在依赖不可用时只 warn 不退出、`AnchorCurrentMonth` 扫表失败必须报错；
- 既有用例按新语义校正：`TestAnchorMismatchBreaksAtFirstRecord` 现在应在**段尾**（index=2, `integrity_tag_mismatch`）暴露；`seedSegmentAnchor` → `seedAnchorForMonth`（写本月封条）。

## 门禁

`gofmt -l` 空、`go vet`、单测、integration（真库+真 Redis）、串行审计套件、覆盖率逐模块下限、三个脚本门禁均绿（见 PR checks）。
