# 审计链 canonical 数字口径（2026-11-10 定稿，Edge）

> **一句话**：数字的唯一规范口径是 `canonicalNumber`，**边界是 int64/uint64 的取值域，不是 2^53**。
> 写侧与读侧必须用同一个编码器产出文本（`encoding/json`），canonical 只依赖 JSON 文本、不依赖 Go 类型。

## 为什么要有这一条

审计链的 `integrity_tag = HMAC-SM3(K_audit, prev_tag || canonical(rec))`。`canonical` 只要对"同一个事实"
产出两种字节串，链就会**自断**（写进去的记录读回来验不过），表现为 `integrity_tag_mismatch`、
`audit_chain_broken_total` 每天 +1 —— 与真篡改无法区分，校验 Job 变噪声源。

本项目在 2026-11-10 一晚上因为这个"数字写法"改了四轮，根因依次是：

| 轮次 | 症状 | 根因 |
|---|---|---|
| 1 | 空 | 初始实现：`float64` 用 `%v`、`json.Number` 用原文 → 同值不同写法不同 tag |
| 2 | 写入/读回不一致 | 读侧 `json.Unmarshal` 把整数解成 `float64`（无 `UseNumber`），`int(1234567)` 写侧 `1234567`、读侧 `1.234567e+06` |
| 3 | 仍然不一致（2^53/float32） | 写侧自己拼 `FormatFloat`，与 `encoding/json` 的文本不同：`7.0888e16` → `'g'` = `7.088828233849594e+16`、json = `70888282338495940` |
| 4（定稿） | — | 写侧借 `json.Marshal` 产出文本（与读侧同一编码器）+ 读写共用 `canonicalNumber` |

## 口径（`internal/audit/chain.go`）

写侧 `numberText(v)`：
- `json.Number` → 原文；
- 其它数值类型（`int8/16/32/64`、`uint8/16/32/64`、`float32/float64`）→ `json.Marshal(v)` 的文本。

`canonicalNumber(txt)` 按序：
1. `ParseInt` 成功 → 定点十进制（保精度：雪花 ID、毫秒时间戳）；
2. `ParseUint` 成功 → 定点十进制；
3. 解析不出数（如 `1e400`）→ **原文**（确定性由输入文本决定）；
4. 浮点值是整数值且落在 int64 取值域 → `FormatInt`（`1e+18` 与 `1000000000000000000` 归一）；
5. 非负且落在 uint64 取值域 → `FormatUint`（覆盖 `2^63..2^64` 段）；
6. 其余 → `FormatFloat('g', -1, 64)`。

**边界说明**：`v == math.Trunc(v)` 在任何量级都精确，所以"能不能转整数"只取决于 int64/uint64 的取值域。
曾经写成 `|v| < 2^53` 是错的：`2^53` 本身被排除在外（off-by-one），而它用 `FormatInt` 完全精确。

## 已知边界（Owl II 复审登记，未处理）

1. **>1e308 的数字**：写侧保持指数原文，读侧被 `jsonb` 展开成 401 位定点文本，两侧都落进"退原文"分支 → 往返不一致。
   触发需要 1e309 量级，实际审计 payload 不会出现；出现即应当在写入方拒绝（不是 canonical 的职责）。
2. **回落 float64 带来的碰撞**：40 位整数两例、`0.1…001` 与 `0.1…002` 可能归到同一 tag（"不同值同写法"）。
   彻底解决要把 `canonicalNumber` 做成纯十进制文本规范化（`math/big.Rat` 或手写拆指数）——
   对审计 payload 属过度设计，登记为已知边界。
3. **口径变更需留档**：`chain.go` 头注释自己要求"改 canonical 等于改历史链，必须升 ADR"。
   本项目未上线（P1：不考虑迁移），所以本次不需要重算历史链，但**口径必须登记**：
   `docs/decisions.md` 是 Ark 的共享契约面，由 Ark 落 D 条目；本条 notes 是技术依据。
4. **性能**：`numberText` 每个数字一次 `json.Marshal`。canonical 是大 payload 的热点，当前量级无影响。

## 回归用例（防"下一个量级又漏"）

- `internal/audit/verify_unit_test.go::TestCanonicalWriteReadAgree` —— **属性断言**：
  36 个边界值（`2^53`、`2^53+2`、`1e15/16/18/21`、`1e-6/1e-7`、`float32(0.1)`、`uint64` 最大值、
  `9007199254740993`、`json.Number("1.00")` …）+ 200 个随机值（指数 ±30 铺开），
  逐个断言 `canonical(写侧值) == canonical(json 读回文本)`。
- `internal/audit/anchor_continuity_serial_db_test.go::TestCanonicalSurvivesDBRoundTrip` —— 真库往返：
  写 `int(1234567)` / `int64(1762900000000)` / `int64(9007199254740993)` / `float64(1e18)` /
  `float64(1<<53)` / `float32(0.1)` / `7.088828233849594e16` / `1.5` → `Load` → 重算 tag 必须等于落库 tag。

## A/B 证据（旧实现必红）

```
# 旧实现（写侧 FormatFloat + |v|<2^53）
--- FAIL: TestCanonicalSurvivesDBRoundTrip
    写进去的 tag 读回来对不上：落库=a1b6f372… 重算=d12267f4… —— canonical 读写口径不一致（链自断）
--- FAIL: TestCanonicalWriteReadAgree
    写侧 9.007199254740992e+15 与读回来的值 "9007199254740992" 算出不同 tag（链自断）

# 新实现
--- PASS（两条均绿）
```
