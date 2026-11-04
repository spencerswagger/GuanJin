# 运维处置：信封完整性失败（D15④ / ADR-005）

触发条件：任一端点返回 `500 INTERNAL_ERROR`，文案为
「数据完整性校验失败，不可重试，请携带 trace_id 联系支持」，
响应 `details.crypto_sentinel` 为 `ciphertext_version` 或 `mac_mismatch`。

日志行（`trace_id` 可用于关联）：

```
... "msg":"flow: envelope integrity check failed" ... "crypto_sentinel":"<标签>" "retryable":false "metric":"integrity_mismatch_total" "count":<n>
```

> `count` 是**进程内**累计值（重启归零）；指标抓取与告警规则待 S10 的观测接线，
> 在此之前只能靠日志检索（`crypto_sentinel` + `metric=integrity_mismatch_total`）。

## 两条处置路径（按 `crypto_sentinel` 分流）

| `crypto_sentinel` | 含义 | 处置 | 是否重试 |
|---|---|---|---|
| `ciphertext_version` | 存量密文是 v1 的「IV‖密文」无认证格式（ADR-005 之前的写入） | 重灌数据；线上数据走**一次性重加密任务**（不引入 v1 读路径） | **不可重试**（重试不会变好） |
| `mac_mismatch` | 键对不上（密钥变更后未重灌）**或**密文被篡改 | 先核对密钥版本与最近一次轮换；若密钥一致 → **按安全事件处理**（取证：`trace_id` / 表 / 行 id） | **不可重试** |
| `unknown` | 其它完整性错误 | 按 `trace_id` 取完整错误链 | 不可重试 |

## 客户端约定

- 该文案明确写了「不可重试」，客户端**不得**按 5xx 自动重试（否则存量 v1 会打出重试风暴）。
- 其它 500（未识别内部错误）仍可携带 `trace_id` 重试一次；两类不可混用同一重试策略。

## 相关判据 / 变更

- `ADR-005`（SM4 信封 v2：`0x02｜iv｜ct｜mac`，先验 MAC 再解密）、`D15`（用途域 6 → 8、v1 拒绝口径）。
- 用例：`internal/credential/totp_test.go`（错钥 / 零值键 / 篡改 / 存量 v1 保留哨兵）、
  `internal/flow/http_test.go` 与 `internal/identity/http_test.go`（不可重试文案 + `crypto_sentinel` 日志）。
