// Package httpx 提供统一的 HTTP 错误响应与错误码常量。
//
// 口径（decisions.md）：
//   - D3-B：错误码**只有一套**（接口清单 §5.4），数据模型里的 IAM_* 全部作废；
//   - D3-F：STEP_ALREADY_SATISFIED 只允许 internal/flow 返回；
//   - D8-C/C′：fail-closed 按具体依赖发具体码；SERVICE_DEGRADED **已废弃、永不返回**；
//   - 错误响应四字段：code / message / trace_id / 可选 details（面向用户可复制 requestId）。
package httpx

import (
	"encoding/json"
	"net/http"
)

// Code 是错误码类型。字符串值即对外错误码，**三语言 SDK 只消费不新增**。
type Code string

// 错误码常量（接口清单 §5.4 的冻结值 + D3-F 追加项）。
const (
	// 通用
	CodeInternalError       Code = "INTERNAL_ERROR"         // E6 / 500：未捕获的内部错误
	CodeRedisUnavailable    Code = "REDIS_UNAVAILABLE"      // E6 / 503（D8-C）
	CodeDBUnavailable       Code = "DB_UNAVAILABLE"         // E6 / 503（D8-C）
	CodeKeyUnavailable      Code = "KEY_UNAVAILABLE"        // E6 / 503（D8-C）
	CodeDependencyUnavail   Code = "DEPENDENCY_UNAVAILABLE" // E6 / 503：**仅未分类**第三方依赖
	CodeUpstreamUnavailable Code = "UPSTREAM_UNAVAILABLE"   // E6 / 503：仅数据面转发失败（D8-C′④）

	// 请求与参数
	CodeMissingParameter Code = "MISSING_PARAMETER" // E1 / 400
	CodeInvalidRequest   Code = "INVALID_REQUEST"   // E1 / 400

	// 认证与令牌
	CodeTokenInvalid   Code = "TOKEN_INVALID"   // 用户令牌无效（SDK 内部断言，D10/错误码界分）
	CodeTokenExpired   Code = "TOKEN_EXPIRED"   // 平台返回；SDK 永不产生
	CodeTokenRevoked   Code = "TOKEN_REVOKED"   // 401：令牌被 revoke 端点撤销（D3-E）
	CodeSessionRevoked Code = "SESSION_REVOKED" // 403：会话被撤销（D3-E）

	// 授权
	CodeInsufficientRole    Code = "INSUFFICIENT_ROLE"     // E3 / 403
	CodeThreePowerViolation Code = "THREE_POWER_VIOLATION" // E7 / 403
	CodeACRInsufficient     Code = "ACR_INSUFFICIENT"      // E3 / 403 + details.flow
)

// DeprecatedCodes 是**已废弃、永不返回**的错误码（D8-C′⑤）。
// 实现里出现即视为缺陷：CI 有禁码扫描（scripts/check-forbidden-error-codes.sh）。
var DeprecatedCodes = []Code{"SERVICE_DEGRADED"}

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
	case CodeMissingParameter, CodeInvalidRequest:
		return http.StatusBadRequest
	case CodeTokenInvalid, CodeTokenExpired, CodeTokenRevoked:
		return http.StatusUnauthorized
	case CodeSessionRevoked, CodeInsufficientRole, CodeThreePowerViolation, CodeACRInsufficient:
		return http.StatusForbidden
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
