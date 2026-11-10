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

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是 `AC-FLOW-09` 的**纯单测**：门槛推导（max tier / 回退 L2 / 坏配置）、
// 403 的形状（**无 `details.flow`**，只给平铺 current/target），以及端点参数与会话校验。
//
// 真库证据（访问记录 join active clients、端到端 403）在 `me_http_db_test.go`。

// fakeVisited 是 `VisitedTierReader` 的桩。
type fakeVisited struct {
	tier   string
	found  bool
	minACR map[string]string
	err    error
}

func (f fakeVisited) MaxVisitedTier(context.Context, uuid.UUID) (string, bool, error) {
	return f.tier, f.found, f.err
}

func (f fakeVisited) TierDefaultMinACR(_ context.Context, tier string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.minACR[tier]
	if !ok {
		return "", errors.New("no min_acr")
	}
	return v, nil
}

// fakeSessions 是 `SessionACRReader` 的桩。
type fakeSessions struct {
	acr string
	err error
}

func (f fakeSessions) GetByID(context.Context, uuid.UUID) (*model.Session, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &model.Session{ACR: f.acr}, nil
}

// fakeBinder 记录绑定调用。
type fakeBinder struct {
	calls []BindRequest
	err   error
}

func (f *fakeBinder) BindIdentifier(_ context.Context, req BindRequest) (*model.Identifier, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, req)
	id, _ := uuid.NewV7()
	return &model.Identifier{BaseModel: model.BaseModel{ID: id}, Type: req.Type, Verified: false}, nil
}

// okResolver 认 `Bearer good-token`，返回固定主体。
type okResolver struct{ userID, sessionID uuid.UUID }

func (r okResolver) Resolve(_ context.Context, token string) (httpx.Caller, error) {
	if token != "good-token" {
		return httpx.Caller{}, httpx.ErrCallerUnauthenticated
	}
	return httpx.Caller{Sub: r.userID.String(), SID: r.sessionID.String()}, nil
}

func newMeHandler(t *testing.T, sessions SessionACRReader, visited VisitedTierReader,
	binder IdentifierBinder) *MeIdentifiersHTTP {
	t.Helper()
	gate, err := NewACRGate(visited, store.PlatformDefaultTier)
	if err != nil {
		t.Fatalf("NewACRGate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	h, err := NewMeIdentifiersHTTP(binder, sessions, gate,
		okResolver{userID: uuid.New(), sessionID: uuid.New()}, logger)
	if err != nil {
		t.Fatalf("NewMeIdentifiersHTTP: %v", err)
	}
	return h
}

func meReq(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/me/identifiers", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer good-token")
	return r.WithContext(httpx.WithTraceID(r.Context(), "trace-flow09"))
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) httpx.ErrorResponse {
	t.Helper()
	var env httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("错误响应不是统一四字段：%v", err)
	}
	return env
}

// 门槛推导：访问过 L3 → 目标 AAL2（tier_default L3 的 min_acr）。
func TestACRGateUsesMaxVisitedTier(t *testing.T) {
	gate, err := NewACRGate(fakeVisited{
		tier: "L3", found: true,
		minACR: map[string]string{"L1": "AAL1", "L2": "AAL1", "L3": "AAL2"},
	}, store.PlatformDefaultTier)
	if err != nil {
		t.Fatalf("NewACRGate: %v", err)
	}
	target, err := gate.Target(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Target: %v", err)
	}
	if target.Tier != "L3" || target.ACR != factor.ACRAAL2 || target.Fallback {
		t.Fatalf("want L3/AAL2 非回退，实际 %+v", target)
	}
}

// 无访问记录 → 回退平台默认 L2 → AAL1（索引 §2 #8）。
func TestACRGateFallsBackToPlatformDefault(t *testing.T) {
	gate, err := NewACRGate(fakeVisited{
		found:  false,
		minACR: map[string]string{"L1": "AAL1", "L2": "AAL1", "L3": "AAL2"},
	}, store.PlatformDefaultTier)
	if err != nil {
		t.Fatalf("NewACRGate: %v", err)
	}
	target, err := gate.Target(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Target: %v", err)
	}
	if target.Tier != "L2" || target.ACR != factor.ACRAAL1 || !target.Fallback {
		t.Fatalf("want L2/AAL1 回退，实际 %+v", target)
	}
}

// 库里 min_acr 是不可签发级别（AAL3 预留/未知）→ 报错，**不许**当成低门槛放行。
func TestACRGateRejectsNonIssuableMinACR(t *testing.T) {
	gate, err := NewACRGate(fakeVisited{
		tier: "L3", found: true,
		minACR: map[string]string{"L3": "AAL3"},
	}, store.PlatformDefaultTier)
	if err != nil {
		t.Fatalf("NewACRGate: %v", err)
	}
	if _, err := gate.Target(context.Background(), uuid.New()); !errors.Is(err, ErrInvalidACRConfig) {
		t.Fatalf("want ErrInvalidACRConfig，实际 %v", err)
	}
}

// 端到端（单测层）：AAL1 会话 + 访问过 L3 → **403 `ACR_INSUFFICIENT`**，
// `details` 只有平铺 current/target/tier，**没有 `flow`**（无上下文开不出流，见 D18 草案）。
func TestMeIdentifiersInsufficient403Shape(t *testing.T) {
	binder := &fakeBinder{}
	h := newMeHandler(t, fakeSessions{acr: "AAL1"}, fakeVisited{
		tier: "L3", found: true,
		minACR: map[string]string{"L2": "AAL1", "L3": "AAL2"},
	}, binder)

	rec := httptest.NewRecorder()
	h.bind(rec, meReq(`{"type":"email","value":"a@example.com"}`))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	env := decodeErr(t, rec)
	if env.Code != httpx.CodeACRInsufficient {
		t.Fatalf("code = %q, want ACR_INSUFFICIENT", env.Code)
	}
	details, ok := env.Details.(map[string]any)
	if !ok {
		t.Fatalf("details 必须是对象，实际 %T", env.Details)
	}
	if _, hasFlow := details["flow"]; hasFlow {
		t.Fatal("无 client 上下文时**不得**带 details.flow（会把客户端引去调 /auth/challenge）")
	}
	if details["current_acr"] != "AAL1" || details["target_acr"] != "AAL2" {
		t.Fatalf("details 平铺字段不对：%v", details)
	}
	if len(binder.calls) != 0 {
		t.Fatal("档位不足时不得执行绑定")
	}
}

// 达标才绑：AAL2 会话 + 访问过 L3 → 201，且绑定用的是**令牌主体**（不是请求体自报）。
func TestMeIdentifiersBindsWhenACRSufficient(t *testing.T) {
	binder := &fakeBinder{}
	sessions := fakeSessions{acr: "AAL2"}
	uid := uuid.New()
	gate, err := NewACRGate(fakeVisited{
		tier: "L3", found: true,
		minACR: map[string]string{"L3": "AAL2"},
	}, store.PlatformDefaultTier)
	if err != nil {
		t.Fatalf("NewACRGate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	h, err := NewMeIdentifiersHTTP(binder, sessions, gate, okResolver{userID: uid, sessionID: uuid.New()}, logger)
	if err != nil {
		t.Fatalf("NewMeIdentifiersHTTP: %v", err)
	}

	rec := httptest.NewRecorder()
	h.bind(rec, meReq(`{"type":"email","value":"a@example.com"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if len(binder.calls) != 1 {
		t.Fatalf("应绑定一次，实际 %d", len(binder.calls))
	}
	if binder.calls[0].UserID != uid {
		t.Fatal("绑定必须用令牌主体，不能用请求体自报的 user")
	}
	var resp bindResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Verified {
		t.Fatal("新绑定的标识符必须是未验证状态")
	}
}

// 会话查不到（令牌已失效）→ 401，不是 403、不是 500。
func TestMeIdentifiersSessionMissingIs401(t *testing.T) {
	h := newMeHandler(t, fakeSessions{err: store.ErrSessionNotFound}, fakeVisited{
		tier: "L3", found: true, minACR: map[string]string{"L3": "AAL2"},
	}, &fakeBinder{})

	rec := httptest.NewRecorder()
	h.bind(rec, meReq(`{"type":"email","value":"a@example.com"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if env := decodeErr(t, rec); env.Code != httpx.CodeTokenInvalid {
		t.Fatalf("code = %q, want TOKEN_INVALID", env.Code)
	}
}

// 参数校验：缺 type/value、畸形 JSON → 4xx，且不触达门槛判定/绑定。
func TestMeIdentifiersValidation(t *testing.T) {
	h := newMeHandler(t, fakeSessions{acr: "AAL2"}, fakeVisited{
		tier: "L3", found: true, minACR: map[string]string{"L3": "AAL2"},
	}, &fakeBinder{})

	cases := []struct {
		body string
		code httpx.Code
	}{
		{`{"value":"a@example.com"}`, httpx.CodeMissingParameter},
		{`{"type":"email"}`, httpx.CodeMissingParameter},
		{`{`, httpx.CodeInvalidRequest},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.bind(rec, meReq(tc.body))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status = %d, want 400", tc.body, rec.Code)
		}
		if env := decodeErr(t, rec); env.Code != tc.code {
			t.Fatalf("body=%s code = %q, want %q", tc.body, env.Code, tc.code)
		}
	}
}

// Routes 必须挂上冻结路径（否则 `main` 里注册了也到不了 handler）。
func TestMeIdentifiersRoutesRegistered(t *testing.T) {
	h := newMeHandler(t, fakeSessions{acr: "AAL2"}, fakeVisited{
		tier: "L3", found: true, minACR: map[string]string{"L3": "AAL2"},
	}, &fakeBinder{})
	mux := http.NewServeMux()
	h.Routes(mux)
	rec := httptest.NewRecorder()
	// 未带令牌 → 401（证明路由命中且鉴权生效，不是 404）。
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/me/identifiers", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401（路由未命中会是 404/405）", rec.Code)
	}
}

// 构造函数缺依赖必须装配期失败（不留 nil 分支）。
func TestNewMeIdentifiersHTTPRequiresDependencies(t *testing.T) {
	gate, err := NewACRGate(fakeVisited{minACR: map[string]string{"L3": "AAL2"}}, store.PlatformDefaultTier)
	if err != nil {
		t.Fatalf("NewACRGate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	res := okResolver{userID: uuid.New(), sessionID: uuid.New()}
	cases := []struct {
		name string
		fn   func() (*MeIdentifiersHTTP, error)
	}{
		{"no binder", func() (*MeIdentifiersHTTP, error) {
			return NewMeIdentifiersHTTP(nil, fakeSessions{}, gate, res, logger)
		}},
		{"no sessions", func() (*MeIdentifiersHTTP, error) {
			return NewMeIdentifiersHTTP(&fakeBinder{}, nil, gate, res, logger)
		}},
		{"no gate", func() (*MeIdentifiersHTTP, error) {
			return NewMeIdentifiersHTTP(&fakeBinder{}, fakeSessions{}, nil, res, logger)
		}},
		{"no resolver", func() (*MeIdentifiersHTTP, error) {
			return NewMeIdentifiersHTTP(&fakeBinder{}, fakeSessions{}, gate, nil, logger)
		}},
		{"no logger", func() (*MeIdentifiersHTTP, error) {
			return NewMeIdentifiersHTTP(&fakeBinder{}, fakeSessions{}, gate, res, nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.fn(); err == nil {
				t.Fatal("缺依赖必须报错")
			}
		})
	}
	if _, err := NewACRGate(nil, "L2"); err == nil {
		t.Fatal("NewACRGate 缺 reader 必须报错")
	}
}

// 绑定失败按服务层哨兵映射（已绑定 → 409、格式非法 → 400）；未知错误 → 500。
func TestMeIdentifiersBindErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   httpx.Code
	}{
		{"已绑定", ErrIdentifierAlreadyBound, http.StatusConflict, httpx.CodeIdentifierAlreadyBound},
		{"未知错误", errors.New("boom"), http.StatusInternalServerError, httpx.CodeInternalError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newMeHandler(t, fakeSessions{acr: "AAL2"}, fakeVisited{
				tier: "L3", found: true, minACR: map[string]string{"L3": "AAL2"},
			}, &fakeBinder{err: tc.err})
			rec := httptest.NewRecorder()
			h.bind(rec, meReq(`{"type":"email","value":"a@example.com"}`))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
			if env := decodeErr(t, rec); env.Code != tc.code {
				t.Fatalf("code = %q, want %q", env.Code, tc.code)
			}
		})
	}
}

// 门槛推导失败（min_acr 不可签发 / 读库失败）→ 500，**不许**降级放行。
func TestMeIdentifiersGateErrorIs500(t *testing.T) {
	h := newMeHandler(t, fakeSessions{acr: "AAL2"}, fakeVisited{
		tier: "L3", found: true, minACR: map[string]string{"L3": "AAL3"},
	}, &fakeBinder{})
	rec := httptest.NewRecorder()
	h.bind(rec, meReq(`{"type":"email","value":"a@example.com"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if env := decodeErr(t, rec); env.Code != httpx.CodeInternalError {
		t.Fatalf("code = %q, want INTERNAL_ERROR", env.Code)
	}
}

// ACRInsufficientError 必须自带可读文案（日志/排障用），且 nil 安全。
func TestACRInsufficientErrorMessage(t *testing.T) {
	var nilErr *ACRInsufficientError
	if nilErr.Error() == "" {
		t.Fatal("nil 也不能 panic，且要有兜底文案")
	}
	e := &ACRInsufficientError{Current: factor.ACRAAL1, Target: factor.ACRAAL2, Tier: "L3"}
	if got := e.Error(); !strings.Contains(got, "AAL1") || !strings.Contains(got, "AAL2") {
		t.Fatalf("文案应含 current/target，实际 %q", got)
	}
}
