package identity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/identifier"
)

// 注册面处理器：**不建库**，用假服务把解码 / 码表 / 响应形状的分支全部走到。
// （准入、邀请码、唯一性等业务判定在服务层，由真库用例盖。）

type fakeRegistrar struct {
	startResult *StartRegistrationResult
	startErr    error
	compErr     error

	startReqs []StartRegistrationRequest
	compReqs  []CompleteRegistrationRequest
}

func (f *fakeRegistrar) StartRegistration(_ context.Context, req StartRegistrationRequest) (*StartRegistrationResult, error) {
	f.startReqs = append(f.startReqs, req)
	if f.startErr != nil {
		return nil, f.startErr
	}
	return f.startResult, nil
}

func (f *fakeRegistrar) CompleteRegistration(_ context.Context, req CompleteRegistrationRequest) (*RegisterResult, error) {
	f.compReqs = append(f.compReqs, req)
	if f.compErr != nil {
		return nil, f.compErr
	}
	return &RegisterResult{}, nil
}

func newRegisterServer(t *testing.T, fake *fakeRegistrar) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h, err := NewRegisterHTTP(fake, logger)
	if err != nil {
		t.Fatalf("NewRegisterHTTP: %v", err)
	}
	mux := http.NewServeMux()
	h.Routes(mux)
	srv := httptest.NewServer(httpx.TraceMiddleware(mux, "X-Trace-Id", logger))
	t.Cleanup(srv.Close)
	return srv
}

func postRegister(t *testing.T, url, body string) (*http.Response, httpx.ErrorResponse) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	if resp.StatusCode >= 400 {
		var env httpx.ErrorResponse
		if derr := decodeInto(resp.Body, &env); derr != nil {
			t.Fatalf("错误响应不是统一四字段：%v", derr)
		}
		return resp, env
	}
	return resp, httpx.ErrorResponse{}
}

// start：200 + 令牌/挑战/下一步；`trace_id` 由中间件注入并透传给服务层。
func TestHTTPRegisterStartOK(t *testing.T) {
	expires := time.Unix(1_760_000_300, 0).UTC()
	chID := "0192f0a0-0000-7000-8000-0000000000aa"
	fake := &fakeRegistrar{startResult: &StartRegistrationResult{
		RegisterToken: "v1.payload.mac",
		ChallengeID:   &chID,
		NextAction:    factor.NextActionTypeCode,
		TargetMasked:  "a***@example.com",
		AttemptsLeft:  5,
		ExpiresAt:     expires,
	}}
	srv := newRegisterServer(t, fake)

	resp, _ := postRegister(t, srv.URL+"/auth/register/start",
		`{"client_id":"c-1","type":"email","value":"a@example.com"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应为 200，实际 %d", resp.StatusCode)
	}
	var got startResponse
	if err := decodeInto(resp.Body, &got); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if got.RegisterToken != "v1.payload.mac" || got.ChallengeID == nil || *got.ChallengeID != chID {
		t.Fatalf("响应不符：%+v", got)
	}
	if got.NextAction != string(factor.NextActionTypeCode) || got.AttemptsLeft != 5 {
		t.Fatalf("next_action/attempts_left 不符：%+v", got)
	}
	if got.ExpiresAt != "2025-10-09T08:58:20Z" { // 1_760_000_300 的 RFC3339
		t.Fatalf("expires_at 应为 RFC3339，实际 %q", got.ExpiresAt)
	}
	if len(fake.startReqs) != 1 || fake.startReqs[0].ClientID != "c-1" {
		t.Fatalf("入参未透传：%+v", fake.startReqs)
	}
}

// start：错误码按冻结表映射，且响应带可复制 trace_id。
func TestHTTPRegisterStartErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code httpx.Code
		http int
	}{
		{"缺 client_id", ErrMissingClientID, httpx.CodeMissingParameter, http.StatusBadRequest},
		{"准入关闭", ErrRegistrationNotAllowed, httpx.CodeRegistrationNotAllowed, http.StatusForbidden},
		{"邀请码无效", ErrInviteCodeInvalid, httpx.CodeInviteCodeInvalid, http.StatusBadRequest},
		{"标识符格式", identifier.ErrInvalidIdentifier, httpx.CodeIdentifierInvalidFormat, http.StatusBadRequest},
		{"已被占用", ErrIdentifierAlreadyBound, httpx.CodeIdentifierAlreadyBound, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRegisterServer(t, &fakeRegistrar{startErr: tc.err})
			resp, env := postRegister(t, srv.URL+"/auth/register/start", `{"client_id":"c-1","type":"email","value":"x"}`)
			if resp.StatusCode != tc.http {
				t.Fatalf("应为 %d，实际 %d", tc.http, resp.StatusCode)
			}
			if env.Code != tc.code {
				t.Fatalf("应为 %s，实际 %s", tc.code, env.Code)
			}
			if env.TraceID == "" {
				t.Fatal("错误响应必须带可复制 trace_id")
			}
		})
	}
}

// start：不认识的错误 → 500，且不回内部文本。
func TestHTTPRegisterStartInternalErrorIsOpaque(t *testing.T) {
	srv := newRegisterServer(t, &fakeRegistrar{startErr: errors.New("db exploded: secret detail")})
	resp, env := postRegister(t, srv.URL+"/auth/register/start", `{"client_id":"c-1","type":"email","value":"x"}`)
	if resp.StatusCode != http.StatusInternalServerError || env.Code != httpx.CodeInternalError {
		t.Fatalf("应为 500 INTERNAL_ERROR，实际 %d %s", resp.StatusCode, env.Code)
	}
	if strings.Contains(env.Message, "secret detail") {
		t.Fatalf("不得透出内部错误文本，实际 %q", env.Message)
	}
}

// start：坏 JSON → 400 INVALID_REQUEST（不是 500）。
func TestHTTPRegisterStartBadJSON(t *testing.T) {
	srv := newRegisterServer(t, &fakeRegistrar{})
	resp, env := postRegister(t, srv.URL+"/auth/register/start", `{`)
	if resp.StatusCode != http.StatusBadRequest || env.Code != httpx.CodeInvalidRequest {
		t.Fatalf("应为 400 INVALID_REQUEST，实际 %d %s", resp.StatusCode, env.Code)
	}
}

// complete：200 `{ok:true}`；口令/验证码错误按 401 回。
func TestHTTPRegisterComplete(t *testing.T) {
	fake := &fakeRegistrar{}
	srv := newRegisterServer(t, fake)
	resp, _ := postRegister(t, srv.URL+"/auth/register/complete",
		`{"register_token":"v1.a.b","code":"123456","password":"GuanJin#Strong1"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应为 200，实际 %d", resp.StatusCode)
	}
	var got completeResponse
	if err := decodeInto(resp.Body, &got); err != nil || !got.OK {
		t.Fatalf("应回 ok=true，实际 %+v err=%v", got, err)
	}
	if len(fake.compReqs) != 1 || fake.compReqs[0].Code != "123456" {
		t.Fatalf("入参未透传：%+v", fake.compReqs)
	}

	errCases := []struct {
		err  error
		code httpx.Code
		http int
	}{
		{ErrInvalidRegisterToken, httpx.CodeInvalidParam, http.StatusBadRequest},
		{ErrRegisterTokenExpired, httpx.CodeChallengeExpired, http.StatusUnauthorized},
		{ErrRegisterChallengeConsumed, httpx.CodeChallengeConsumed, http.StatusUnauthorized},
		{ErrInvalidRegisterCode, httpx.CodeInvalidCredential, http.StatusUnauthorized},
		{ErrRegisterAttemptsExceeded, httpx.CodeAttemptsExceeded, http.StatusUnauthorized},
		{&PasswordRejected{Result: credential.Result{Code: httpx.CodeWeakPassword}}, httpx.CodeWeakPassword, http.StatusUnprocessableEntity},
	}
	for _, tc := range errCases {
		srv := newRegisterServer(t, &fakeRegistrar{compErr: tc.err})
		resp, env := postRegister(t, srv.URL+"/auth/register/complete", `{"register_token":"v1.a.b","password":"x"}`)
		if resp.StatusCode != tc.http || env.Code != tc.code {
			t.Fatalf("%v 应为 %d %s，实际 %d %s", tc.err, tc.http, tc.code, resp.StatusCode, env.Code)
		}
	}
}

// 构造守卫：缺依赖即失败（不等到第一个请求）。
func TestNewRegisterHTTPRequiresDeps(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if _, err := NewRegisterHTTP(nil, logger); err == nil {
		t.Fatal("缺服务必须报错")
	}
	if _, err := NewRegisterHTTP(&fakeRegistrar{}, nil); err == nil {
		t.Fatal("缺 logger 必须报错")
	}
}

func decodeInto(r io.Reader, dst any) error {
	return json.NewDecoder(r).Decode(dst)
}

// AC-REG-10②：`code` 只回首个命中项，**全量**命中进 `details.violations[]`（顺序即判定顺序）。
func TestHTTPCompleteEmitsViolationsDetails(t *testing.T) {
	rejected := &PasswordRejected{Result: credential.Result{
		Code: httpx.CodeWeakPassword,
		Violations: []credential.Violation{
			{Kind: credential.ViolationWeak, Code: httpx.CodeWeakPassword, Rule: "weaklist", Message: "常见弱口令"},
			{Kind: credential.ViolationPolicy, Code: httpx.CodePasswordPolicyViolation, Rule: "min_length", Message: "长度不足"},
		},
	}}
	srv := newRegisterServer(t, &fakeRegistrar{compErr: rejected})
	resp, env := postRegister(t, srv.URL+"/auth/register/complete", `{"register_token":"v1.a.b","password":"x"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity || env.Code != httpx.CodeWeakPassword {
		t.Fatalf("应 422 WEAK_PASSWORD，实际 %d %s", resp.StatusCode, env.Code)
	}
	details, ok := env.Details.(map[string]any)
	if !ok {
		t.Fatalf("必须带 details，实际 %v", env.Details)
	}
	raw, ok := details["violations"].([]any)
	if !ok || len(raw) != 2 {
		t.Fatalf("details.violations 应含 2 条命中项，实际 %v", details["violations"])
	}
	first, _ := raw[0].(map[string]any)
	if first["rule"] != "weaklist" || first["code"] != string(httpx.CodeWeakPassword) {
		t.Fatalf("第一条应是弱口令（顺序固定 weak → reused → policy），实际 %v", first)
	}
}
