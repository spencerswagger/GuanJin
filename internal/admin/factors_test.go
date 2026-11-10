package admin

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

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是因子管理面（`AC-FLOW-05`）的**纯单测**：三员矩阵、请求校验、未装配因子、
// 平台级 enabled 的呈现，以及"PATCH 真的把入参交给了写入路径"。
//
// 真库/事务回滚的证据在 `internal/store/repo_platform_factors_db_test.go`（同事务写审计、
// 审计失败整体回滚）——那两条只能在真 PG 上验。

// stubFactor 是能力声明的最小 Provider（Type/Kind 足够 Describe 出 Descriptor）。
type stubFactor struct {
	typ  factor.FactorType
	kind factor.Kind
}

func (s stubFactor) Type() factor.FactorType { return s.typ }
func (s stubFactor) Kind() factor.Kind       { return s.kind }

// EnrollmentSpec 给一个最小能力声明：描述符要求"至少一个能力"，否则 `Describe` 直接报错。
func (s stubFactor) EnrollmentSpec() factor.EnrollmentSpec {
	return factor.EnrollmentSpec{SelfServiceAllowed: true}
}

// fakeSwitcher 记录写入并允许注入失败。
type fakeSwitcher struct {
	disabled []string
	err      error
	calls    []store.SetFactorEnabledInput
}

func (f *fakeSwitcher) Disabled(context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.disabled, nil
}

func (f *fakeSwitcher) SetEnabled(_ context.Context, in store.SetFactorEnabledInput) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, in)
	return nil
}

func newFactorsHTTP(t *testing.T, sw FactorSwitcher, caller *session.Caller) *FactorsHTTP {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	reg := factor.NewRegistry()
	reg.MustRegister(stubFactor{typ: factor.FactorPassword, kind: factor.KindHolding})
	h, err := NewFactorsHTTP(reg, sw, staticResolver{caller: caller}, logger)
	if err != nil {
		t.Fatalf("NewFactorsHTTP: %v", err)
	}
	return h
}

func factorsReq(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer good-token")
	return r.WithContext(httpx.WithTraceID(r.Context(), "trace-factors"))
}

func decodeFactorErr(t *testing.T, rec *httptest.ResponseRecorder) httpx.ErrorResponse {
	t.Helper()
	var env httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("错误响应不是统一四字段：%v", err)
	}
	return env
}

// 读：系统管理员/审计员可看；`enabled` 反映**平台级**状态（被禁用 → false）。
func TestFactorsListShowsPlatformEnabled(t *testing.T) {
	sw := &fakeSwitcher{disabled: []string{string(factor.FactorPassword)}}
	h := newFactorsHTTP(t, sw, &session.Caller{UserID: uuid.New(), Roles: []string{string(RoleAuditor)}})

	rec := httptest.NewRecorder()
	h.list(rec, factorsReq(t, http.MethodGet, "/api/v1/factors", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got []factorView
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 factor, got %d", len(got))
	}
	if got[0].Type != string(factor.FactorPassword) || got[0].Enabled {
		t.Fatalf("平台级已禁用时 enabled 必须为 false，实际 %+v", got[0])
	}
	if len(got[0].Capabilities) == 0 {
		// 能力声明是契约的一部分：空数组会让页面以为是"没有能力"而不是"没实现"。
		t.Logf("提示：stub 因子无能力声明（真实因子由 holding.Register 提供）")
	}
}

// 未装配的因子：详情与写入都回 `FACTOR_NOT_SUPPORTED`（422），不是 404、更不是 500。
func TestFactorsUnknownTypeIsNotSupported(t *testing.T) {
	h := newFactorsHTTP(t, &fakeSwitcher{}, &session.Caller{UserID: uuid.New(), Roles: []string{string(RoleSystemAdmin)}})

	rec := httptest.NewRecorder()
	h.detail(rec, pathValue(factorsReq(t, http.MethodGet, "/api/v1/factors/sms", ""), "type", "sms"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("detail status = %d, want 422", rec.Code)
	}
	if env := decodeFactorErr(t, rec); env.Code != httpx.CodeFactorNotSupported {
		t.Fatalf("code = %q, want FACTOR_NOT_SUPPORTED", env.Code)
	}

	rec = httptest.NewRecorder()
	h.patch(rec, pathValue(factorsReq(t, http.MethodPatch, "/api/v1/factors/sms", `{"enabled":false,"reason":"x"}`), "type", "sms"))
	if env := decodeFactorErr(t, rec); env.Code != httpx.CodeFactorNotSupported {
		t.Fatalf("patch code = %q, want FACTOR_NOT_SUPPORTED", env.Code)
	}
}

// 三员矩阵：审计员写 → `THREE_POWER_VIOLATION`（不是 `INSUFFICIENT_ROLE`）。
func TestFactorsAuditorWriteIsThreePowerViolation(t *testing.T) {
	h := newFactorsHTTP(t, &fakeSwitcher{}, &session.Caller{UserID: uuid.New(), Roles: []string{string(RoleAuditor)}})

	rec := httptest.NewRecorder()
	h.patch(rec, pathValue(factorsReq(t, http.MethodPatch, "/api/v1/factors/password",
		`{"enabled":false,"reason":"e2e"}`), "type", "password"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if env := decodeFactorErr(t, rec); env.Code != httpx.CodeThreePowerViolation {
		t.Fatalf("code = %q, want THREE_POWER_VIOLATION", env.Code)
	}
}

// 安全管理员可写（接口 §3.8：系统管理员/安全管理员）。
func TestFactorsSecurityAdminCanDisable(t *testing.T) {
	sw := &fakeSwitcher{}
	uid := uuid.New()
	h := newFactorsHTTP(t, sw, &session.Caller{UserID: uid, Roles: []string{string(RoleSecurityAdmin)}})

	rec := httptest.NewRecorder()
	h.patch(rec, pathValue(factorsReq(t, http.MethodPatch, "/api/v1/factors/password",
		`{"enabled":false,"reason":"通道故障"}`), "type", "password"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(sw.calls) != 1 {
		t.Fatalf("必须调用一次写入路径，实际 %d 次", len(sw.calls))
	}
	got := sw.calls[0]
	if got.Type != string(factor.FactorPassword) || got.Enabled {
		t.Fatalf("入参不对：%+v", got)
	}
	if got.Reason != "通道故障" {
		t.Fatalf("reason 必须透传（审计要回答为什么关）：%q", got.Reason)
	}
	if got.ActorUserID != uid || got.ActorRole != string(RoleSecurityAdmin) {
		t.Fatalf("审计主体不对：%+v", got)
	}
	if got.TraceID != "trace-factors" {
		t.Fatalf("trace_id 必须透传（审计可按 trace 串起来）：%q", got.TraceID)
	}
}

// 请求校验：缺 `enabled` / 缺 `reason` / 畸形 JSON 都是 4xx，且**不调用写入路径**
// （一次畸形请求绝不能变成一次关停）。
func TestFactorsPatchValidation(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		code   httpx.Code
	}{
		{"缺 enabled", `{"reason":"x"}`, http.StatusBadRequest, httpx.CodeMissingParameter},
		{"缺 reason", `{"enabled":false}`, http.StatusBadRequest, httpx.CodeMissingParameter},
		{"reason 全空白", `{"enabled":false,"reason":"   "}`, http.StatusBadRequest, httpx.CodeMissingParameter},
		{"畸形 JSON", `{`, http.StatusBadRequest, httpx.CodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sw := &fakeSwitcher{}
			h := newFactorsHTTP(t, sw, &session.Caller{UserID: uuid.New(), Roles: []string{string(RoleSystemAdmin)}})
			rec := httptest.NewRecorder()
			h.patch(rec, pathValue(factorsReq(t, http.MethodPatch, "/api/v1/factors/password", tc.body), "type", "password"))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
			if env := decodeFactorErr(t, rec); env.Code != tc.code {
				t.Fatalf("code = %q, want %q", env.Code, tc.code)
			}
			if len(sw.calls) != 0 {
				t.Fatalf("校验失败不得调用写入路径，实际 %d 次", len(sw.calls))
			}
		})
	}
}

// 存储层失败必须冒泡成 5xx（不吞、不假装成功）——审计写不进去时尤其不能回 200。
func TestFactorsPatchStoreFailurePropagates(t *testing.T) {
	sw := &fakeSwitcher{err: errors.New("audit append failed")}
	h := newFactorsHTTP(t, sw, &session.Caller{UserID: uuid.New(), Roles: []string{string(RoleSystemAdmin)}})

	rec := httptest.NewRecorder()
	h.patch(rec, pathValue(factorsReq(t, http.MethodPatch, "/api/v1/factors/password",
		`{"enabled":true,"reason":"恢复"}`), "type", "password"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if env := decodeFactorErr(t, rec); env.Code != httpx.CodeInternalError {
		t.Fatalf("code = %q, want INTERNAL_ERROR", env.Code)
	}
}

// pathValue 在纯单测里注入路径参数（`ServeMux` 不在场时 `PathValue` 为空）。
func pathValue(r *http.Request, key, val string) *http.Request {
	r.SetPathValue(key, val)
	return r
}
