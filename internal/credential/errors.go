package credential

import (
	"errors"
	"fmt"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本包的显式错误。**不要**在别处再造同义错误：对外码的映射只发生在 `CodeOf`。
var (
	// ErrInvalidCredential 表示凭据本身不对（口令错 / 动态口令错 / 恢复码错）。
	ErrInvalidCredential = errors.New("credential: invalid credential")
	// ErrTemporalCodeReused 表示同一个动态口令码在同一窗口内被用了第二次（重放）。
	//
	// 与"码错"分开是**安全信号**的差别：码错可能只是手抖，重放是攻击特征。
	// 接口 §5.4 为它单列 `TEMPORAL_CODE_REUSED`，不合并进 `INVALID_CREDENTIAL`。
	ErrTemporalCodeReused = errors.New("credential: temporal code reused within its window")
	// ErrMalformedCode 表示动态口令码形态不对（非数字 / 长度不符）。
	ErrMalformedCode = errors.New("credential: malformed one-time code")
	// ErrMalformedEnvelope 表示凭据信封结构损坏（缺字段 / 非法 base64）。
	ErrMalformedEnvelope = errors.New("credential: malformed credential envelope")
	// ErrWeakParams 表示信封里的算法参数低于基线硬下限（被降级或被篡改）。
	//
	// 这是**数据完整性**问题，不是用户输错：读到一个 `iterations=1000` 的信封必须报错，
	// 而不是"照样校验放行"——那等于把基线 §1.3 的下限变成建议。
	ErrWeakParams = errors.New("credential: parameters below baseline minimum")
	// ErrUnknownAlgo 表示信封声明的算法不是本平台冻结的算法。
	ErrUnknownAlgo = errors.New("credential: unsupported algorithm")
	// ErrEmptyPassword 表示口令为空（空口令在派生层就拒绝，不留给策略层"看情况"）。
	ErrEmptyPassword = errors.New("credential: empty password")
	// ErrPasswordTooLong 表示口令超过上限。
	//
	// 上限不是洁癖：PBKDF2 的输入长度直接影响每次校验的开销，而登录是 200 QPS 的路径；
	// 该上限与 `login_policies.password_min_length` 的 DB CHECK（8..128）同源。
	ErrPasswordTooLong = errors.New("credential: password longer than 128 bytes")
	// ErrInvalidPolicy 表示调用方给的口令策略值不合法（配置错误，不是用户错）。
	ErrInvalidPolicy = errors.New("credential: invalid password policy")
	// ErrInvalidOptions 表示 TOTP 参数不合法（位数/周期/窗口越界）。
	ErrInvalidOptions = errors.New("credential: invalid totp options")
	// ErrInvalidSecret 表示 TOTP 共享密钥长度不足。
	ErrInvalidSecret = errors.New("credential: totp secret shorter than 20 bytes")
	// ErrMissingKey 表示没有拿到可用的 SM4 键（零值 / 解封失败）。
	//
	// 两种情况都按"密钥不可用"处理（`KEY_UNAVAILABLE` / 503，D8-C）：
	// 零值键几乎一定是"忘了派生"，解封失败则可能是密钥轮换未铺开或密文被改——
	// 无论哪种，都**不许**退化成"跳过校验放行"。
	ErrMissingKey = errors.New("credential: sm4 key missing or unusable")
)

// CodeOf 把本包的错误映射成**对外错误码**（接口 §5.4 / D3-B）。
//
// 单一映射点：路由层/编排层只调它，不许自己 `switch err`——否则同一种失败在不同端点
// 会返回不同码（D3-E 要消灭的正是这一类双轨）。
//
// 注意：口令三码（WEAK / REUSED / POLICY）**不在这里**——它们不是"出错"，
// 是策略判定结果，见 `Policy.Evaluate` 的 `Result`。
func CodeOf(err error) (httpx.Code, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, ErrTemporalCodeReused):
		return httpx.CodeTemporalCodeReused, true
	case errors.Is(err, ErrInvalidCredential), errors.Is(err, ErrMalformedCode):
		// 形态不对与值不对对外**同码**：`MALFORMED_CODE` 不存在于 §5.4，
		// 而且"你输的码格式都不对"本身也属于"凭据无效"，没有必要给攻击者更多信息。
		return httpx.CodeInvalidCredential, true
	case errors.Is(err, ErrMissingKey):
		// 密钥不可用属 fail-closed 的具体依赖失败（D8-C：按具体依赖发具体码 + 503）。
		return httpx.CodeKeyUnavailable, true
	case errors.Is(err, ErrWeakParams), errors.Is(err, ErrMalformedEnvelope),
		errors.Is(err, ErrUnknownAlgo), errors.Is(err, ErrInvalidPolicy),
		errors.Is(err, ErrInvalidOptions), errors.Is(err, ErrInvalidSecret):
		// 这些都是"库里的数据坏了"或"配置错了"，属缺陷路径：500 而不是 401/422。
		// 用 INTERNAL_ERROR 而不是 DEPENDENCY_UNAVAILABLE（后者是依赖不可用，D8-C）。
		return httpx.CodeInternalError, true
	case errors.Is(err, ErrEmptyPassword), errors.Is(err, ErrPasswordTooLong):
		// 口令本身不合格 → 由策略层给三码；走到这里说明调用方绕过了策略校验，
		// 仍按"策略不符"回，避免把内部顺序错误暴露成 500。
		return httpx.CodePasswordPolicyViolation, true
	default:
		return "", false
	}
}

// wrapf 统一加包前缀，避免错误文本漂移（也避免与 store 的错误文本混淆）。
func wrapf(err error, format string, args ...any) error {
	return fmt.Errorf("credential: %s: %w", fmt.Sprintf(format, args...), err)
}
