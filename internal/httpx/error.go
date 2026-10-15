package httpx

import "net/http"

type AppError struct {
	Code    string
	HTTP    int
	Message string
	Details map[string]any
}

func (e *AppError) Error() string { return e.Code }

func New(code, message string) *AppError {
	return &AppError{Code: code, HTTP: HTTPStatus(code), Message: message}
}

func (e *AppError) WithDetails(details map[string]any) *AppError {
	e.Details = details
	return e
}

var httpStatusByCode = map[string]int{
	// E1 请求 / 协议 (400 / 404)
	"INVALID_REQUEST":                   http.StatusBadRequest,
	"MISSING_PARAMETER":                 http.StatusBadRequest,
	"INVALID_PARAMETER":                 http.StatusBadRequest,
	"INVALID_CLIENT":                    http.StatusBadRequest,
	"INVALID_REDIRECT_URI":              http.StatusBadRequest,
	"UNSAFE_REQUEST":                    http.StatusBadRequest,
	"UNSUPPORTED_RESPONSE_TYPE":         http.StatusBadRequest,
	"UNSUPPORTED_GRANT_TYPE":            http.StatusBadRequest,
	"UNSUPPORTED_CODE_CHALLENGE_METHOD": http.StatusBadRequest,
	"UNSUPPORTED_TOKEN_TYPE":            http.StatusBadRequest,
	"FLOW_NOT_FOUND":                    http.StatusBadRequest,
	"FLOW_EXPIRED":                      http.StatusBadRequest,
	"PAGE_SIZE_EXCEEDED":                http.StatusBadRequest,
	"CONFIRMATION_REQUIRED":             http.StatusBadRequest,
	"NOT_FOUND":                         http.StatusNotFound,

	// E2 认证 / 凭据 (401)
	"INVALID_CREDENTIAL":       http.StatusUnauthorized,
	"OLD_PASSWORD_INCORRECT":   http.StatusUnauthorized,
	"TOKEN_INVALID":            http.StatusUnauthorized,
	"TOKEN_EXPIRED":            http.StatusUnauthorized,
	"TOKEN_REVOKED":            http.StatusUnauthorized,
	"REFRESH_TOKEN_REUSED":     http.StatusUnauthorized,
	"CHALLENGE_EXPIRED":        http.StatusUnauthorized,
	"CHALLENGE_CONSUMED":       http.StatusUnauthorized,
	"CHALLENGE_INVALID":        http.StatusUnauthorized,
	"ATTEMPTS_EXCEEDED":        http.StatusUnauthorized,
	"ACCOUNT_LOCKED":           http.StatusUnauthorized,
	"ACCOUNT_DISABLED":         http.StatusUnauthorized,
	"TEMPORAL_CODE_REUSED":     http.StatusUnauthorized,
	"PKCE_VERIFICATION_FAILED": http.StatusUnauthorized,
	"INVALID_CODE_VERIFIER":    http.StatusUnauthorized,
	"INVALID_GRANT":            http.StatusUnauthorized,

	// E3 授权 / 档位 (403)
	"ACR_INSUFFICIENT":   http.StatusForbidden,
	"MFA_REQUIRED":       http.StatusForbidden,
	"INSUFFICIENT_SCOPE": http.StatusForbidden,
	"INSUFFICIENT_ROLE":  http.StatusForbidden,
	"SESSION_REVOKED":    http.StatusForbidden,
	"SESSION_NOT_FOUND":  http.StatusForbidden,
	"SESSION_NOT_OWNED":  http.StatusForbidden,
	"RECOVER_DISABLED":   http.StatusForbidden,

	// E4 资源 / 不变式 (404 / 409 / 422)
	"IDENTIFIER_ALREADY_BOUND":         http.StatusConflict,
	"IDENTIFIER_NOT_VERIFIED":          http.StatusConflict,
	"IDENTIFIER_NOT_OWNED":             http.StatusConflict,
	"LAST_IDENTIFIER_CANNOT_UNBIND":    http.StatusConflict,
	"LAST_AUTHENTICATOR_CANNOT_REMOVE": http.StatusConflict,
	"COOLDOWN_ACTIVE":                  http.StatusConflict,
	"IDENTIFIER_REQUIRED_BY_POLICY":    http.StatusConflict,
	"POLICY_SNAPSHOT_CONFLICT":         http.StatusConflict,
	"IDENTIFIER_INVALID_FORMAT":        http.StatusUnprocessableEntity,
	"PASSWORD_POLICY_VIOLATION":        http.StatusUnprocessableEntity,
	"PASSWORD_REUSED":                  http.StatusUnprocessableEntity,
	"FACTOR_NOT_SUPPORTED":             http.StatusUnprocessableEntity,
	"FACTOR_DISABLED":                  http.StatusUnprocessableEntity,
	"NO_ELIGIBLE_FACTOR":               http.StatusUnprocessableEntity,
	"RECOVER_EXPIRED":                  http.StatusUnprocessableEntity,
	"RECOVERY_TOKEN_EXPIRED":           http.StatusUnprocessableEntity,
	"IDENTIFIER_NOT_FOUND":             http.StatusNotFound,
	"USER_NOT_FOUND":                   http.StatusNotFound,
	"CLIENT_NOT_FOUND":                 http.StatusNotFound,
	"KEY_NOT_FOUND":                    http.StatusNotFound,
	"SNAPSHOT_NOT_FOUND":               http.StatusNotFound,
	"AUDIT_LOG_NOT_FOUND":              http.StatusNotFound,

	// E5 风控 / 限流 (429)
	"RATE_LIMITED":         http.StatusTooManyRequests,
	"BUDGET_EXCEEDED":      http.StatusTooManyRequests,
	"RISK_BLOCKED":         http.StatusTooManyRequests,
	"CAPTCHA_REQUIRED":     http.StatusTooManyRequests,
	"ACCOUNT_TEMP_BLOCKED": http.StatusTooManyRequests,
	"EXPORT_IN_PROGRESS":   http.StatusTooManyRequests,

	// E6 服务端 / 依赖 (500 / 503)
	"INTERNAL_ERROR":         http.StatusInternalServerError,
	"DEPENDENCY_UNAVAILABLE": http.StatusServiceUnavailable,
	"REDIS_UNAVAILABLE":      http.StatusServiceUnavailable,
	"DB_UNAVAILABLE":         http.StatusServiceUnavailable,
	"KEY_UNAVAILABLE":        http.StatusServiceUnavailable,
	"UPSTREAM_UNAVAILABLE":   http.StatusServiceUnavailable,
	"SERVICE_DEGRADED":       http.StatusServiceUnavailable,

	// E7 管理面 / 策略 (403 / 404 / 409 / 422)
	"TIER_DIRECTION_VIOLATION":  http.StatusForbidden,
	"THREE_POWER_VIOLATION":     http.StatusForbidden,
	"UNAUTHORIZED_INTROSPECTOR": http.StatusForbidden,
	"CLIENT_LIMIT_REACHED":      http.StatusConflict,
	"KEY_ROTATION_IN_PROGRESS":  http.StatusConflict,
	"KEY_RETIRE_PREMATURE":      http.StatusConflict,
	"LAST_ADMIN_CANNOT_REMOVE":  http.StatusConflict,
	"BINDING_ALREADY_EXISTS":    http.StatusConflict,
	"SNAPSHOT_IMMUTABLE":        http.StatusConflict,
	"CONFIG_READONLY":           http.StatusConflict,
	"BINDING_NOT_FOUND":         http.StatusNotFound,
	"TIER_UNKNOWN":              http.StatusUnprocessableEntity,
	"ROLE_UNKNOWN":              http.StatusUnprocessableEntity,

	// E8 服务间签名 (401)
	"AGENT_SIGNATURE_INVALID": http.StatusUnauthorized,
	"AGENT_TIMESTAMP_SKEW":    http.StatusUnauthorized,
	"AGENT_NONCE_REPLAY":      http.StatusUnauthorized,
	"AGENT_UNKNOWN_CLIENT":    http.StatusUnauthorized,
	"AGENT_SECRET_DISABLED":   http.StatusUnauthorized,

	// 仓储层错误码（§5.3），与 E4/E1 同族
	"CLIENT_ID_ALREADY_EXISTS": http.StatusConflict,
	"KEY_KID_EXISTS":           http.StatusConflict,
	"REFERENCE_NOT_FOUND":      http.StatusUnprocessableEntity,
	"CONFLICT":                 http.StatusConflict,
}

var defaultMessages = map[string]string{
	"INVALID_REQUEST":               "请求无效",
	"MISSING_PARAMETER":             "缺少必填参数",
	"INVALID_PARAMETER":             "参数无效",
	"INVALID_CREDENTIAL":            "凭据无效",
	"TOKEN_INVALID":                 "令牌无效",
	"TOKEN_EXPIRED":                 "令牌已过期",
	"TOKEN_REVOKED":                 "令牌已撤销",
	"ACR_INSUFFICIENT":              "认证档位不足",
	"INSUFFICIENT_ROLE":             "权限不足",
	"SESSION_REVOKED":               "会话已撤销",
	"IDENTIFIER_ALREADY_BOUND":      "该标识符已被绑定",
	"LAST_IDENTIFIER_CANNOT_UNBIND": "不能解绑最后一个标识符",
	"RATE_LIMITED":                  "请求过于频繁",
	"BUDGET_EXCEEDED":               "超出预算配额",
	"DEPENDENCY_UNAVAILABLE":        "依赖服务不可用",
	"REDIS_UNAVAILABLE":             "Redis 不可用",
	"DB_UNAVAILABLE":                "数据库不可用",
	"KEY_UNAVAILABLE":               "密钥不可用",
	"INTERNAL_ERROR":                "内部错误",
	"NOT_FOUND":                     "资源不存在",
}

func HTTPStatus(code string) int {
	if status, ok := httpStatusByCode[code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

func knownCode(code string) bool {
	_, ok := httpStatusByCode[code]
	return ok
}

func MessageFor(code string) string {
	if msg, ok := defaultMessages[code]; ok {
		return msg
	}
	if knownCode(code) {
		return code
	}
	return defaultMessages["INTERNAL_ERROR"]
}
