package httpx

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPStatusMapping(t *testing.T) {
	cases := map[string]int{
		// §10.3 显式映射
		"INVALID_REQUEST":               http.StatusBadRequest,
		"MISSING_PARAMETER":             http.StatusBadRequest,
		"INVALID_PARAMETER":             http.StatusBadRequest,
		"INVALID_CLIENT":                http.StatusBadRequest,
		"FLOW_NOT_FOUND":                http.StatusBadRequest,
		"FLOW_EXPIRED":                  http.StatusBadRequest,
		"INVALID_CREDENTIAL":            http.StatusUnauthorized,
		"TOKEN_INVALID":                 http.StatusUnauthorized,
		"TOKEN_EXPIRED":                 http.StatusUnauthorized,
		"TOKEN_REVOKED":                 http.StatusUnauthorized,
		"CHALLENGE_EXPIRED":             http.StatusUnauthorized,
		"ACCOUNT_LOCKED":                http.StatusUnauthorized,
		"ACR_INSUFFICIENT":              http.StatusForbidden,
		"INSUFFICIENT_ROLE":             http.StatusForbidden,
		"SESSION_REVOKED":               http.StatusForbidden,
		"IDENTIFIER_ALREADY_BOUND":      http.StatusConflict,
		"LAST_IDENTIFIER_CANNOT_UNBIND": http.StatusConflict,
		"RATE_LIMITED":                  http.StatusTooManyRequests,
		"BUDGET_EXCEEDED":               http.StatusTooManyRequests,
		"DEPENDENCY_UNAVAILABLE":        http.StatusServiceUnavailable,
		"REDIS_UNAVAILABLE":             http.StatusServiceUnavailable,
		"DB_UNAVAILABLE":                http.StatusServiceUnavailable,
		"KEY_UNAVAILABLE":               http.StatusServiceUnavailable,
		// E1..E8 分层补充
		"UNSUPPORTED_GRANT_TYPE":    http.StatusBadRequest,
		"CONFIRMATION_REQUIRED":     http.StatusBadRequest,
		"REFRESH_TOKEN_REUSED":      http.StatusUnauthorized,
		"MFA_REQUIRED":              http.StatusForbidden,
		"IDENTIFIER_INVALID_FORMAT": http.StatusUnprocessableEntity,
		"PASSWORD_POLICY_VIOLATION": http.StatusUnprocessableEntity,
		"USER_NOT_FOUND":            http.StatusNotFound,
		"CAPTCHA_REQUIRED":          http.StatusTooManyRequests,
		"INTERNAL_ERROR":            http.StatusInternalServerError,
		"UPSTREAM_UNAVAILABLE":      http.StatusServiceUnavailable,
		"TIER_DIRECTION_VIOLATION":  http.StatusForbidden,
		"TIER_UNKNOWN":              http.StatusUnprocessableEntity,
		"BINDING_ALREADY_EXISTS":    http.StatusConflict,
		"AGENT_SIGNATURE_INVALID":   http.StatusUnauthorized,
		// 仓储层错误码（§5.3）
		"CLIENT_ID_ALREADY_EXISTS": http.StatusConflict,
		"KEY_KID_EXISTS":           http.StatusConflict,
		"REFERENCE_NOT_FOUND":      http.StatusUnprocessableEntity,
		"CONFLICT":                 http.StatusConflict,
	}

	for code, want := range cases {
		require.Equalf(t, want, HTTPStatus(code), "code=%s", code)
	}
}

func TestHTTPStatusUnknownIsInternalError(t *testing.T) {
	require.Equal(t, http.StatusInternalServerError, HTTPStatus("TOTALLY_UNKNOWN_CODE"))
	require.Equal(t, http.StatusInternalServerError, HTTPStatus(""))
}

func TestAppErrorImplementsError(t *testing.T) {
	var err error = New("ACR_INSUFFICIENT", "认证档位不足")
	require.EqualError(t, err, "ACR_INSUFFICIENT")

	appErr, ok := err.(*AppError)
	require.True(t, ok)
	require.Equal(t, http.StatusForbidden, appErr.HTTP)
}

func TestToAppErrorUsesKnownCodeOrHidesInternals(t *testing.T) {
	known := toAppError(errorString("RATE_LIMITED"))
	require.Equal(t, "RATE_LIMITED", known.Code)
	require.Equal(t, http.StatusTooManyRequests, known.HTTP)

	unknown := toAppError(errorString("pq: relation \"users\" does not exist"))
	require.Equal(t, "INTERNAL_ERROR", unknown.Code)
	require.Equal(t, http.StatusInternalServerError, unknown.HTTP)
	require.NotContains(t, unknown.Message, "relation")
}

type errorString string

func (e errorString) Error() string { return string(e) }
