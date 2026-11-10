package main

// `AC-FLOW-05` 的**端点级**证据（Gate 要求补：审计员写 → 403 `THREE_POWER_VIOLATION`）。
//
// 这里走的是**真实的 `NewFactorsHTTP` + 真实 mux 路由**（与 `cmd/guanjind` 里
// `newFactorsAdminWiring(...).Routes(mux)` 同一套），只把 token 解析器换成固定身份——
// 目的是验证"端点装配 + 三员矩阵"这一层，而不是重测业务逻辑。

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/admin"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
)

// fixedResolver 把任意 token 解析成固定身份（只在本文件用）。
type fixedResolver struct{ caller httpx.Caller }

func (f fixedResolver) Resolve(context.Context, string) (httpx.Caller, error) { return f.caller, nil }

func factorsMuxWithRoles(t *testing.T, roles []string) http.Handler {
	t.Helper()
	reg := factor.NewRegistry()
	h, err := admin.NewFactorsHTTP(reg, stubSwitcher{}, fixedResolver{caller: httpx.Caller{
		Sub:   uuid.NewString(),
		Roles: roles,
	}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewFactorsHTTP: %v", err)
	}
	mux := http.NewServeMux()
	h.Routes(mux)
	return mux
}

type stubSwitcher struct{}

func (stubSwitcher) Disabled(context.Context) ([]string, error)                    { return nil, nil }
func (stubSwitcher) SetEnabled(context.Context, store.SetFactorEnabledInput) error { return nil }

// 审计员（auditor）写 → **403 + THREE_POWER_VIOLATION**（不是 INSUFFICIENT_ROLE，两码要分开）。
func TestE2E_AC_FLOW_05_AuditorWriteIs403ThreePowerViolation(t *testing.T) {
	mux := factorsMuxWithRoles(t, []string{string(admin.RoleAuditor)})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/factors/password",
		strings.NewReader(`{"enabled":false}`))
	req.Header.Set("Authorization", "Bearer any")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("审计员写因子开关应 403，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if !containsCode(rec.Body.String(), "THREE_POWER_VIOLATION") {
		t.Fatalf("错误码必须是 THREE_POWER_VIOLATION，实际 body=%s", rec.Body.String())
	}
}

// 只读：审计员读 → 200（矩阵的另一半，防止"拦过头"）。
func TestE2E_AC_FLOW_05_AuditorReadIs200(t *testing.T) {
	mux := factorsMuxWithRoles(t, []string{string(admin.RoleAuditor)})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/factors", nil)
	req.Header.Set("Authorization", "Bearer any")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("审计员读因子列表应 200，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
}

func containsCode(body, code string) bool { return strings.Contains(body, code) }
