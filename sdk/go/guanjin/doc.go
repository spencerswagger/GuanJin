// Package guanjin 是关津（GuanJin）的 **Go 侧最小接入 SDK**。
//
// 权威契约：`docs/sdk-contract.md`（22 条能力 `GJ-CAP-01..22` + 29 项配置）。
// 本包只实现 S9 的**最小集**（验签 → 读身份 → 剥离 → 公开路径 + 配置 fail-fast +
// 启动自检），其余能力在本版本内**显式**返回 ErrNotImplemented，并在
// `sdk/README.md` 的「能力状态表」里逐条列出——**不允许静默缺**（否则 S11 的
// AC 映射会漏项）。
//
// 命名口径（契约 §1.5）：概念层（能力 ID / 配置键 / claim / 错误码 / 端点）
// **逐字符相同**；API 层按契约 §2.1 的 Go 列冻结。配置键固定 29 项，多一个即
// 启动失败（`AC-FC-05`）；不提供 `mode` / `direct.*`（契约 §3.3 `REMOVED`）。
//
// 剥离时序（契约 §4.5 = D10 唯一六步，禁止换序）：
// ① 读原始头 → ② 验签 → ③ 校验 claim（`exp`/`iat`/`iss`/`aud`/`jti`）→
// ④ 注入上下文 → ⑤ **替换请求视图**（handler / 下游 / 日志都取不到）→ ⑥ handler。
// 本包用一个**新的 `*http.Request`** 实现第 ⑤ 步：`Header` 已重建、去掉断言头，
// 因此内层 handler 与任何日志中间件拿到的 `r` 上都不存在该头。
package guanjin

// 版本与契约锚点（供启动自检与排障打印；改契约版本必须同步 docs/sdk-contract.md）。
const (
	// SDKVersion 是本 SDK 的版本（随 M1 走 0.x）。
	SDKVersion = "0.1.0-m1"
	// ContractVersion 是所依据的契约文档版本。
	ContractVersion = "v8"
	// ContractCapabilityCount 是契约冻结的能力条数（22）。
	ContractCapabilityCount = 22
	// ContractConfigCount 是契约冻结的配置项数（29）。
	ContractConfigCount = 29
)
