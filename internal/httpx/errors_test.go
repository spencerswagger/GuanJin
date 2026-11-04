package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPStatusMapping(t *testing.T) {
	cases := map[Code]int{
		CodeMissingParameter:    http.StatusBadRequest,
		CodeTokenExpired:        http.StatusUnauthorized,
		CodeSessionRevoked:      http.StatusForbidden,
		CodeACRInsufficient:     http.StatusForbidden,
		CodeDBUnavailable:       http.StatusServiceUnavailable,
		CodeKeyUnavailable:      http.StatusServiceUnavailable,
		CodeUpstreamUnavailable: http.StatusServiceUnavailable,
		CodeInternalError:       http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := HTTPStatus(code); got != want {
			t.Errorf("HTTPStatus(%s) = %d, want %d", code, got, want)
		}
	}
}

// 错误响应必须是四字段形态，且 trace_id 一定在（可复制 requestId）。
func TestWriteErrorShape(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, "trace-123", CodeACRInsufficient, "need more factors", map[string]any{"flow": "f-1"})

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	var got ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got.Code != CodeACRInsufficient || got.TraceID != "trace-123" || got.Message == "" {
		t.Fatalf("unexpected body: %+v", got)
	}
	if got.Details == nil {
		t.Fatalf("details must be forwarded")
	}
}

// 依赖不可用必须带 Retry-After（D8-C 统一 503 + Retry-After）。
func TestWriteErrorSetsRetryAfterOn503(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, "t", CodeRedisUnavailable, "redis down", nil)
	if w.Header().Get("Retry-After") == "" {
		t.Fatalf("Retry-After must be set on 503")
	}
	if !strings.Contains(w.Body.String(), string(CodeRedisUnavailable)) {
		t.Fatalf("body must carry the code, got %s", w.Body.String())
	}
}

// 已废弃码表存在，但**不得**出现在任何映射/返回值里（CI 另有静态扫描）。
func TestDeprecatedCodesAreNotMapped(t *testing.T) {
	for _, c := range DeprecatedCodes {
		if HTTPStatus(c) != http.StatusInternalServerError {
			t.Errorf("deprecated code %s must not have a dedicated mapping", c)
		}
	}
}

// 口令三码 + 凭据两码的状态映射（AC-REG-10 / 接口 §5.4 分组表）。
//
// 判据钉的是**分组**而不是"某个实现恰好这么写"：E4 组一律 422（WEAK / REUSED / POLICY），
// E2 组一律 401（INVALID_CREDENTIAL / TEMPORAL_CODE_REUSED）。三码的**顺序**不在这里判，
// 它在 internal/credential 的 Policy.Evaluate 里（AC-REG-10 的排序部分）。
func TestPasswordAndCredentialCodeStatus(t *testing.T) {
	cases := map[Code]int{
		CodeWeakPassword:            http.StatusUnprocessableEntity,
		CodePasswordReused:          http.StatusUnprocessableEntity,
		CodePasswordPolicyViolation: http.StatusUnprocessableEntity,
		CodeInvalidCredential:       http.StatusUnauthorized,
		CodeTemporalCodeReused:      http.StatusUnauthorized,
	}
	for code, want := range cases {
		if !IsKnownCode(code) {
			t.Errorf("%s 必须在冻结码表里（新增码要同时进 knownCodes，否则跨包一致性测试查不到）", code)
		}
		if got := HTTPStatus(code); got != want {
			t.Errorf("HTTPStatus(%s) = %d, want %d", code, got, want)
		}
	}
}

// 完整性失败的三个出口必须自洽：稳定标签、单调计数、唯一文案。
func TestIntegrityMismatchSurface(t *testing.T) {
	wrapVersion := fmt.Errorf("credential: %w: %w", crypto.ErrIntegrityMismatch, crypto.ErrCiphertextVersion)
	if got := CryptoIntegritySentinel(wrapVersion); got != "ciphertext_version" {
		t.Fatalf("存量 v1 应压成 ciphertext_version，实际 %q", got)
	}
	wrapMac := fmt.Errorf("credential: %w: %w", crypto.ErrIntegrityMismatch, crypto.ErrIntegrityMismatch)
	if got := CryptoIntegritySentinel(wrapMac); got != "mac_mismatch" {
		t.Fatalf("MAC 不过应压成 mac_mismatch，实际 %q", got)
	}
	if got := CryptoIntegritySentinel(errors.New("other")); got != "unknown" {
		t.Fatalf("其它错误应回 unknown，实际 %q", got)
	}
	if got := CryptoIntegritySentinel(nil); got != "" {
		t.Fatalf("nil 应回空串，实际 %q", got)
	}

	before := IntegrityMismatchTotal()
	if after := IncIntegrityMismatch(); after != before+1 {
		t.Fatalf("计数应单调 +1：before=%d after=%d", before, after)
	}
	if IntegrityMessage == "" || !strings.Contains(IntegrityMessage, "不可重试") {
		t.Fatalf("文案必须写明不可重试，实际 %q", IntegrityMessage)
	}
}
