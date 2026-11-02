// Package factor 是「因子 SPI（能力接口 + Provider 注册表）」的包边界占位。
//
// 口径（decisions.md / 基线）：

//   - 因子分类与能力模型 §2/§3；10 个能力接口
//   - **禁因子类型分支**：不许 `if type == "sms"`，一律能力断言（项目硬约束 4）
//   - V1 四类：password / sms_otp / email_otp / totp；类型开放、架构上无限扩充
//
// 本包在 S1（骨架）只声明边界与依赖方向，**不含实现**；
// 实现按 m1-slices.md 的切片顺序由对应负责人在后续 wave 落地。
package factor
