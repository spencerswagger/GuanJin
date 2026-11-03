// Package risk 是「风控：失败计数 / 锁定 / 限流（Redis 权威计数）」的包边界占位。
//
// 口径（decisions.md / 基线）：

//   - 索引 §4.2-2；D3-D（键名冻结）
//   - 账号级失败计数键 login:fail:{user_id}、锁定键 login:lock:{user_id}
//   - Redis 不可用 → 503 REDIS_UNAVAILABLE（fail-closed，绝不降级放行）
//
// 本包在 S1（骨架）只声明边界与依赖方向，**不含实现**；
// 实现按 m1-slices.md 的切片顺序由对应负责人在后续 wave 落地。
package risk
