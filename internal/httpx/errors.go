// Package httpx 提供统一的 HTTP 错误响应、错误码常量与 trace 基础设施。
//
// 口径（decisions.md）：
//   - D3-B：错误码**只有一套**（接口清单 §5.4），数据模型里的 IAM_* 全部作废；
//   - D3-F：STEP_ALREADY_SATISFIED 只允许 internal/flow 返回；
//   - D8-C/C′：fail-closed 按具体依赖发具体码；SERVICE_DEGRADED **已废弃、永不返回**；
//   - 错误响应四字段：code / message / trace_id / 可选 details（面向用户可复制 requestId）；
//   - D4-C：trace_id 贯穿日志、auth_events、audit_logs（基础设施见 trace.go）。
package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// Code 是错误码类型。字符串值即对外错误码，**三语言 SDK 只消费不新增**。
type Code string

// 错误码常量（接口清单 §5.4 的冻结值 + D3-F 追加项）。
//
// 分级与 HTTP 状态照抄 §5.4：E1=400/404、E2=401、E3=403、E4=404/409/422、
// E6=500/503、E7=403/409/422。**不新增别名码**（D3-B）。
const (
	// 通用
	CodeInternalError       Code = "INTERNAL_ERROR"         // E6 / 500：未捕获的内部错误
	CodeRedisUnavailable    Code = "REDIS_UNAVAILABLE"      // E6 / 503（D8-C）
	CodeDBUnavailable       Code = "DB_UNAVAILABLE"         // E6 / 503（D8-C）
	CodeKeyUnavailable      Code = "KEY_UNAVAILABLE"        // E6 / 503（D8-C）
	CodeDependencyUnavail   Code = "DEPENDENCY_UNAVAILABLE" // E6 / 503：**仅未分类**第三方依赖
	CodeUpstreamUnavailable Code = "UPSTREAM_UNAVAILABLE"   // E6 / 503：仅数据面转发失败（D8-C′④）

	// 请求与协议（E1）
	CodeInvalidRequest   Code = "INVALID_REQUEST"    // E1 / 400
	CodeInvalidParam     Code = "INVALID_PARAMETER"  // E1 / 400
	CodeMissingParameter Code = "MISSING_PARAMETER"  // E1 / 400
	CodeInvalidClient    Code = "INVALID_CLIENT"     // E1 / 400
	CodeFlowNotFound     Code = "FLOW_NOT_FOUND"     // E1 / 404（I-F4：已完成流二次提交）
	CodeFlowExpired      Code = "FLOW_EXPIRED"       // E1 / 400（I-F3）
	CodePageSizeExceeded Code = "PAGE_SIZE_EXCEEDED" // E1 / 400（D3-A：limit > 200）

	// 认证与凭据（E2）
	CodeTokenInvalid   Code = "TOKEN_INVALID"   // E2 / 401（SDK 内部断言，D10）
	CodeTokenExpired   Code = "TOKEN_EXPIRED"   // E2 / 401
	CodeTokenRevoked   Code = "TOKEN_REVOKED"   // E2 / 401：令牌被 revoke 端点撤销（D3-E）
	CodeSessionRevoked Code = "SESSION_REVOKED" // E3 / 403：会话被撤销（D3-E）
	// CodeInvalidCredential 是"凭据本身不对"的唯一码（口令错、TOTP 码错、恢复码错）。
	// 接口 §5.4 E2；AC-LOGIN-02 要求它带可复制 trace_id 且 message 无区分性文案。
	CodeInvalidCredential Code = "INVALID_CREDENTIAL" // E2 / 401
	// CodeTemporalCodeReused 是"同一个动态口令被用第二次"（窗口内重放），与"码错"分开：
	// 前者是重放攻击信号（§5.4 E2 的 TEMPORAL_CODE_REUSED），后者是输错。
	CodeTemporalCodeReused Code = "TEMPORAL_CODE_REUSED" // E2 / 401
	// CodeAccountLocked 是"账号因连续失败被锁定"（§5.4 E2 的 ACCOUNT_LOCKED）。
	//
	// 与 `ATTEMPTS_EXCEEDED` 是两个对象：后者是**单次挑战**的尝试次数用尽（`challenges.attempts`），
	// 前者是**账号级**失败计数的结果（Redis `login:lock:{user_id}`，D3-D）。
	CodeAccountLocked Code = "ACCOUNT_LOCKED" // E2 / 401
	// 协议面（S6）四个在用码：**接口 §5.4 E2 行本来就有，本包此前漏登记**（与 `ACCOUNT_LOCKED` 同类，
	// 属"补登记"不是新增契约）。
	CodeInvalidGrant        Code = "INVALID_GRANT"            // E2 / 401（授权码二次使用、码无效/过期）
	CodeRefreshTokenReused  Code = "REFRESH_TOKEN_REUSED"     // E2 / 401（刷新令牌二次使用 → 撤销会话）
	CodePKCEFailed          Code = "PKCE_VERIFICATION_FAILED" // E2 / 401（verifier 与 challenge 不匹配）
	CodeInvalidCodeVerifier Code = "INVALID_CODE_VERIFIER"    // E2 / 400（缺/畸形 verifier，D3-E）

	// 授权与档位（E3）
	CodeInsufficientRole    Code = "INSUFFICIENT_ROLE"     // E3 / 403
	CodeThreePowerViolation Code = "THREE_POWER_VIOLATION" // E7 / 403
	CodeACRInsufficient     Code = "ACR_INSUFFICIENT"      // E3 / 403 + details.flow
	CodeSessionNotFound     Code = "SESSION_NOT_FOUND"     // E3 / 404

	// 资源与不变式（E4）
	CodeIdentifierAlreadyBound  Code = "IDENTIFIER_ALREADY_BOUND"  // E4 / 409
	CodeIdentifierNotFound      Code = "IDENTIFIER_NOT_FOUND"      // E4 / 404
	CodeUserNotFound            Code = "USER_NOT_FOUND"            // E4 / 404
	CodeClientNotFound          Code = "CLIENT_NOT_FOUND"          // E4 / 404
	CodeKeyNotFound             Code = "KEY_NOT_FOUND"             // E4 / 404
	CodeSnapshotNotFound        Code = "SNAPSHOT_NOT_FOUND"        // E4 / 404
	CodePolicySnapshotConflict  Code = "POLICY_SNAPSHOT_CONFLICT"  // E4 / 409
	CodeCooldownActive          Code = "COOLDOWN_ACTIVE"           // E4 / 409
	CodePasswordPolicyViolation Code = "PASSWORD_POLICY_VIOLATION" // E4 / 422
	// CodeWeakPassword / CodePasswordReused 是 AC-REG-10 冻结的三码之一（D3-B′ 补登记）：
	// 三码同时命中时对外只回**首个**，顺序固定 WEAK_PASSWORD → PASSWORD_REUSED → PASSWORD_POLICY_VIOLATION，
	// 全量命中项放 details.violations[]。三码都在 E4 组，HTTP 一律 422。
	CodeWeakPassword   Code = "WEAK_PASSWORD"   // E4 / 422
	CodePasswordReused Code = "PASSWORD_REUSED" // E4 / 422
	// CodeLastIdentifierCannotUnbind / CodeLastAuthenticatorCannotRemove 是 I1~I4 的**对外码**
	// （接口 §5.4 E4 里本来就有 · 409）。I6（最后一个主标识符不可降级）**按 D3-B 不设对外码**——
	// V1 没有暴露"设主标识符"端点，撞到即属内部缺陷（500）。
	CodeLastIdentifierCannotUnbind    Code = "LAST_IDENTIFIER_CANNOT_UNBIND"    // E4 / 409
	CodeLastAuthenticatorCannotRemove Code = "LAST_AUTHENTICATOR_CANNOT_REMOVE" // E4 / 409
	// 挑战状态机（E2 / 401）：挑战是一次性的，四种失败必须能被客户端区分。
	CodeChallengeExpired  Code = "CHALLENGE_EXPIRED"  // E2 / 401
	CodeChallengeConsumed Code = "CHALLENGE_CONSUMED" // E2 / 401
	CodeChallengeInvalid  Code = "CHALLENGE_INVALID"  // E2 / 401（不存在/不属于本流）
	CodeAttemptsExceeded  Code = "ATTEMPTS_EXCEEDED"  // E2 / 401（单挑战次数用尽）

	// CodeIdentifierNotVerified 表示"这个标识符还没验证过"，不能当作可用登录方式/找回通道。
	CodeIdentifierNotVerified Code = "IDENTIFIER_NOT_VERIFIED" // E4 / 409
	// CodeIdentifierInvalidFormat 表示标识符格式不合法（规范化阶段就拒，不落库）。
	CodeIdentifierInvalidFormat Code = "IDENTIFIER_INVALID_FORMAT" // E4 / 400
	// CodeRegistrationNotAllowed 表示目标业务系统未开放自助注册（D3-B″ 补登记：E4/403）。
	CodeRegistrationNotAllowed Code = "REGISTRATION_NOT_ALLOWED" // E4 / 403
	// CodeInviteCodeInvalid 表示邀请码无效/已使用/过期（D3-B″ 补登记：E1/400）。
	//
	// M1 没有邀请码存储（`clients`/`login_policies` 无该字段、库里也无 invite 表），
	// 因此**任何非空邀请码都是无效的**——显式拒绝，不静默忽略（AC-REG-08）。
	CodeInviteCodeInvalid Code = "INVITE_CODE_INVALID" // E1 / 400
	// 因子不可用（E4 / 422）：三种原因必须分开，否则前端不知道该"换因子"还是"找管理员"。
	CodeFactorDisabled     Code = "FACTOR_DISABLED"      // 该业务系统禁用了这个因子
	CodeFactorNotSupported Code = "FACTOR_NOT_SUPPORTED" // 平台没装配这个因子
	CodeNoEligibleFactor   Code = "NO_ELIGIBLE_FACTOR"   // 没有任何因子能推进这一步

	// 管理面与策略（E7）
	CodeBindingAlreadyExists   Code = "BINDING_ALREADY_EXISTS"   // E7 / 409
	CodeBindingNotFound        Code = "BINDING_NOT_FOUND"        // E7 / 404
	CodeTierDirectionViolation Code = "TIER_DIRECTION_VIOLATION" // E7 / 422（I8）
)

// DeprecatedCodes 是**已废弃、永不返回**的错误码（D8-C′⑤）。
// 实现里出现即视为缺陷：CI 有禁码扫描（scripts/check-forbidden-error-codes.sh）。
var DeprecatedCodes = []Code{"SERVICE_DEGRADED"}

// knownCodes 是「本包认识的码」全表：用来校验其他包（如 internal/store）抛出的业务码
// 没有跑出 §5.4 的词表。新增码必须同时进这里，避免"悄悄造码"。
var knownCodes = []Code{
	CodeInternalError, CodeRedisUnavailable, CodeDBUnavailable, CodeKeyUnavailable,
	CodeDependencyUnavail, CodeUpstreamUnavailable,
	CodeInvalidRequest, CodeInvalidParam, CodeMissingParameter, CodeInvalidClient,
	CodeFlowNotFound, CodeFlowExpired, CodePageSizeExceeded,
	CodeTokenInvalid, CodeTokenExpired, CodeTokenRevoked, CodeSessionRevoked,
	CodeInvalidCredential, CodeTemporalCodeReused, CodeAccountLocked,
	CodeInvalidGrant, CodeRefreshTokenReused, CodePKCEFailed, CodeInvalidCodeVerifier,
	CodeInsufficientRole, CodeThreePowerViolation, CodeACRInsufficient, CodeSessionNotFound,
	CodeIdentifierAlreadyBound, CodeIdentifierNotFound, CodeUserNotFound, CodeClientNotFound,
	CodeKeyNotFound, CodeSnapshotNotFound, CodePolicySnapshotConflict, CodeCooldownActive,
	CodePasswordPolicyViolation, CodeWeakPassword, CodePasswordReused,
	CodeLastIdentifierCannotUnbind, CodeLastAuthenticatorCannotRemove, CodeIdentifierNotVerified,
	CodeIdentifierInvalidFormat, CodeRegistrationNotAllowed, CodeInviteCodeInvalid,
	CodeChallengeExpired, CodeChallengeConsumed, CodeChallengeInvalid, CodeAttemptsExceeded,
	CodeFactorDisabled, CodeFactorNotSupported, CodeNoEligibleFactor,
	CodeBindingAlreadyExists, CodeBindingNotFound, CodeTierDirectionViolation,
}

// KnownCodes 返回冻结的错误码清单（只读副本，供跨包一致性测试使用）。
func KnownCodes() []Code {
	out := make([]Code, len(knownCodes))
	copy(out, knownCodes)
	return out
}

// IsKnownCode 报告 c 是否在冻结清单内。
func IsKnownCode(c Code) bool {
	for _, k := range knownCodes {
		if k == c {
			return true
		}
	}
	return false
}

// ErrorResponse 是错误响应的唯一形态（四字段）。
type ErrorResponse struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	TraceID string `json:"trace_id"`          // 可复制 requestId，后端按此打详细日志
	Details any    `json:"details,omitempty"` // 如 ACR_INSUFFICIENT 的 flow descriptor
}

// HTTPStatus 把错误码映射到 HTTP 状态（D8-C：具体依赖 503 + Retry-After）。
func HTTPStatus(c Code) int {
	switch c {
	case CodeInvalidRequest, CodeInvalidParam, CodeMissingParameter, CodeInvalidClient,
		CodeFlowExpired, CodePageSizeExceeded, CodeIdentifierInvalidFormat, CodeInviteCodeInvalid,
		CodeInvalidCodeVerifier:
		return http.StatusBadRequest
	case CodeFlowNotFound, CodeSessionNotFound, CodeIdentifierNotFound, CodeUserNotFound,
		CodeClientNotFound, CodeKeyNotFound, CodeSnapshotNotFound, CodeBindingNotFound:
		return http.StatusNotFound
	case CodeTokenInvalid, CodeTokenExpired, CodeTokenRevoked,
		CodeInvalidCredential, CodeTemporalCodeReused, CodeAccountLocked,
		CodeInvalidGrant, CodeRefreshTokenReused, CodePKCEFailed,
		CodeChallengeExpired, CodeChallengeConsumed, CodeChallengeInvalid, CodeAttemptsExceeded:
		return http.StatusUnauthorized
	case CodeSessionRevoked, CodeInsufficientRole, CodeThreePowerViolation, CodeACRInsufficient:
		return http.StatusForbidden
	case CodeIdentifierAlreadyBound, CodePolicySnapshotConflict, CodeCooldownActive,
		CodeBindingAlreadyExists, CodeLastIdentifierCannotUnbind, CodeLastAuthenticatorCannotRemove,
		CodeIdentifierNotVerified:
		return http.StatusConflict
	case CodeRegistrationNotAllowed:
		// 接口 §3.3 / §5.4「接口可覆盖」：E4 默认 404/409/422，本条覆盖为 403（AC-REG-09）。
		return http.StatusForbidden
	case CodePasswordPolicyViolation, CodeTierDirectionViolation,
		CodeWeakPassword, CodePasswordReused,
		CodeFactorDisabled, CodeFactorNotSupported, CodeNoEligibleFactor:
		return http.StatusUnprocessableEntity
	case CodeRedisUnavailable, CodeDBUnavailable, CodeKeyUnavailable,
		CodeDependencyUnavail, CodeUpstreamUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// WriteError 写出统一错误响应；traceID 由调用方从请求上下文取（不得为空）。
func WriteError(w http.ResponseWriter, traceID string, c Code, msg string, details any) {
	WriteErrorStatus(w, traceID, c, HTTPStatus(c), msg, details)
}

// WriteErrorStatus 与 `WriteError` 相同，但允许调用方指定 HTTP 状态。
//
// 存在的理由只有一个：**码表之外那一个码**（`STEP_ALREADY_SATISFIED`，D3-F）。
// 它的常量按 D3-F 只能定义在 `internal/flow`（禁码扫描的边界），所以本包不认识它，
// `HTTPStatus` 会落到 default 变成 500 —— 而它必须回 409。**不要**用它绕过码表：
// 除了那一个码，其余一律走 `WriteError`。
func WriteErrorStatus(w http.ResponseWriter, traceID string, c Code, status int, msg string, details any) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	if status == http.StatusServiceUnavailable {
		h.Set("Retry-After", "5")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Code: c, Message: msg, TraceID: traceID, Details: details})
}

// integrityMismatchTotal 是**完整性失败**的进程内计数器（D15④ / ADR-005 的运维切口）。
//
// 为什么单独一个计数器：这类失败被 `CodeOf` 归到 `INTERNAL_ERROR`(500)，与"其它未识别内部错误"
// 混在同一个码里；只有把它单调计数 + 结构化日志带 `crypto_sentinel`，运维才能区分
// 「存量 v1 未重加密（重灌/一次性重加密）」与「MAC 不过（按安全事件处理）」。
//
// **缺件说明（如实登记）**：平台目前还没有指标/告警底座（`audit_chain_broken_total` 等
// 同类指标要等 S10 的观测接线）。所以这里是"进程内计数 + 结构化日志"，抓取与告警规则
// 待 S10 接入；在那之前**不要**把它当成"已接告警"。
var integrityMismatchTotal atomic.Int64

// IncIntegrityMismatch 记一次完整性失败，返回累计值（用于日志里的 `count` 字段）。
func IncIntegrityMismatch() int64 { return integrityMismatchTotal.Add(1) }

// IntegrityMismatchTotal 读累计值（进程级；重启归零，这正是需要 S10 落指标的原因）。
func IntegrityMismatchTotal() int64 { return integrityMismatchTotal.Load() }

// IntegrityMessage 是完整性失败唯一允许的对外文案（**不可重试**）。
//
// 与其它 500 的区别是硬要求：客户端对 5xx 普遍带重试，而"存量 v1 未重加密 / 密文被改"
// 是**永久性缺陷**，重试只会打出重试风暴（Owl 追认 #35 的 P1-1）。
const IntegrityMessage = "数据完整性校验失败，不可重试，请携带 trace_id 联系支持"

// CryptoIntegritySentinel 把错误链里的 `crypto` 哨兵压成一个**稳定标签**，进日志/指标。
//
// 两个标签对应两套完全不同的运维动作：
//   - `ciphertext_version`：存量 v1（无认证格式）未重加密 → 重灌 / 一次性重加密；
//   - `mac_mismatch`：键对不上或密文被改 → 按安全事件处理。
func CryptoIntegritySentinel(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, crypto.ErrCiphertextVersion):
		return "ciphertext_version"
	case errors.Is(err, crypto.ErrIntegrityMismatch):
		return "mac_mismatch"
	default:
		return "unknown"
	}
}
