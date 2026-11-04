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
	"net/http"
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
	CodeInvalidCredential, CodeTemporalCodeReused,
	CodeInsufficientRole, CodeThreePowerViolation, CodeACRInsufficient, CodeSessionNotFound,
	CodeIdentifierAlreadyBound, CodeIdentifierNotFound, CodeUserNotFound, CodeClientNotFound,
	CodeKeyNotFound, CodeSnapshotNotFound, CodePolicySnapshotConflict, CodeCooldownActive,
	CodePasswordPolicyViolation, CodeWeakPassword, CodePasswordReused,
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
		CodeFlowExpired, CodePageSizeExceeded:
		return http.StatusBadRequest
	case CodeFlowNotFound, CodeSessionNotFound, CodeIdentifierNotFound, CodeUserNotFound,
		CodeClientNotFound, CodeKeyNotFound, CodeSnapshotNotFound, CodeBindingNotFound:
		return http.StatusNotFound
	case CodeTokenInvalid, CodeTokenExpired, CodeTokenRevoked,
		CodeInvalidCredential, CodeTemporalCodeReused:
		return http.StatusUnauthorized
	case CodeSessionRevoked, CodeInsufficientRole, CodeThreePowerViolation, CodeACRInsufficient:
		return http.StatusForbidden
	case CodeIdentifierAlreadyBound, CodePolicySnapshotConflict, CodeCooldownActive,
		CodeBindingAlreadyExists:
		return http.StatusConflict
	case CodePasswordPolicyViolation, CodeTierDirectionViolation,
		CodeWeakPassword, CodePasswordReused:
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
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	if HTTPStatus(c) == http.StatusServiceUnavailable {
		h.Set("Retry-After", "5")
	}
	w.WriteHeader(HTTPStatus(c))
	_ = json.NewEncoder(w).Encode(ErrorResponse{Code: c, Message: msg, TraceID: traceID, Details: details})
}
