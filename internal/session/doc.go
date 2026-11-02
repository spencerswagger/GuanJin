// Package session 是「会话生命周期 + 实时撤销 + 并发上限」的包边界占位。
//
// 口径（decisions.md / 基线）：

//   - 数据模型 §3.5；索引 §2「撤销实时生效」
//   - idle/absolute 双超时取 login_policies；溢出动作 deny_new/evict_oldest*
//   - 撤销级联 refresh/access；活跃流置 abandoned（D5 窄接口 AbandonBySession）
//   - 锁序：sessions → flows，sessions 一律不做 FOR UPDATE（D5 I-F8）
//
// 本包在 S1（骨架）只声明边界与依赖方向，**不含实现**；
// 实现按 m1-slices.md 的切片顺序由对应负责人在后续 wave 落地。
package session
