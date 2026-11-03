// Package audit 是「审计：auth_events / audit_logs + HMAC-SM3 哈希链」的包边界占位。
//
// 口径（decisions.md / 基线）：

//   - 数据模型 §3.6/§3.7；D4-C（月分区 + 锚点 + 校验 Job）
//   - 单全局链 + pg_advisory_xact_lock 串行化链尾；预留分链演进（索引 §4.2-19）
//   - 锚点写 runtime_config：audit.chain.anchor.<YYYYMM> / segment.<YYYYMM>
//   - 指标：audit_chain_broken_total / audit_anchor_age_seconds
//
// 本包在 S1（骨架）只声明边界与依赖方向，**不含实现**；
// 实现按 m1-slices.md 的切片顺序由对应负责人在后续 wave 落地。
package audit
