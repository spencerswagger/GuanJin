package credential

import (
	"errors"
	"fmt"
	"testing"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 错误 → 对外码的映射必须**唯一且封闭**：路由层只调 CodeOf，不许自己 switch。
func TestCodeOf(t *testing.T) {
	cases := []struct {
		err  error
		want httpx.Code
	}{
		{nil, ""},
		{ErrTemporalCodeReused, httpx.CodeTemporalCodeReused},
		{ErrInvalidCredential, httpx.CodeInvalidCredential},
		{ErrMalformedCode, httpx.CodeInvalidCredential},
		{ErrMissingKey, httpx.CodeKeyUnavailable},
		{ErrWeakParams, httpx.CodeInternalError},
		{ErrMalformedEnvelope, httpx.CodeInternalError},
		{ErrUnknownAlgo, httpx.CodeInternalError},
		{ErrInvalidPolicy, httpx.CodeInternalError},
		{ErrInvalidOptions, httpx.CodeInternalError},
		{ErrInvalidSecret, httpx.CodeInternalError},
		{ErrEmptyPassword, httpx.CodePasswordPolicyViolation},
		{ErrPasswordTooLong, httpx.CodePasswordPolicyViolation},
		{fmt.Errorf("wrapped: %w", ErrTemporalCodeReused), httpx.CodeTemporalCodeReused},
	}
	for _, tc := range cases {
		got, ok := CodeOf(tc.err)
		if tc.want == "" {
			if ok {
				t.Errorf("CodeOf(nil) 应 (\"\",false)，实际 (%q,%v)", got, ok)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("CodeOf(%v) = (%q,%v)，期望 (%q,true)", tc.err, got, ok, tc.want)
		}
		if !httpx.IsKnownCode(got) {
			t.Errorf("%v 映射出的码 %q 不在冻结码表里", tc.err, got)
		}
	}
}

// 未知错误不猜码：返回 false，由调用方按 500 处理（禁止"认不出就当 400"）。
func TestCodeOfUnknownError(t *testing.T) {
	if _, ok := CodeOf(errors.New("something else")); ok {
		t.Fatal("未知错误不应被映射")
	}
}
