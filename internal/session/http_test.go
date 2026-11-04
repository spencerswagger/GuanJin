package session

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是自助面 handler 的**纯单测**：解码、鉴权（经 CallerResolver）、响应形状、
// 归属校验都在不建库的前提下钉住。业务副作用（撤销/级联/事件）已在 manager_test 里覆盖。

// fakeResolver 模拟 S6 的验签实现：只认 `Bearer good-token`。
type fakeResolver struct {
	sub   uuid.UUID
	sid   uuid.UUID
	roles []string
	err   error
}

func (f fakeResolver) Resolve(_ context.Context, token string) (httpx.Caller, error) {
	if f.err != nil {
		return httpx.Caller{}, f.err
	}
	if token != "good-token" {
		return httpx.Caller{}, httpx.ErrCallerUnauthenticated
	}
	return httpx.Caller{Sub: f.sub.String(), SID: f.sid.String(), Roles: f.roles}, nil
}

func newSessionHTTP(t *testing.T, st *fakeStore, flows *fakeFlows, events *fakeEvents,
	caller *Caller, now time.Time) *HTTPHandler {
	t.Helper()
	m := newManager(t, st, flows, events, now)
	var resolver TokenResolver = fakeResolver{}
	if caller != nil {
		resolver = fakeResolver{sub: caller.UserID, sid: caller.SessionID}
	}
	h, err := NewHTTPHandler(m, resolver, discardLogger())
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	return h
}

func TestNewHTTPHandlerRequiresDependencies(t *testing.T) {
	m := newManager(t, newFakeStore(), &fakeFlows{}, &fakeEvents{}, time.Now())
	logger := discardLogger()
	cases := []struct {
		name   string
		mgr    *Manager
		caller httpx.CallerResolver
	}{
		{"no manager", nil, TokenResolver(nil)},
		{"no resolver", m, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewHTTPHandler(tc.mgr, tc.caller, logger); err == nil {
				t.Fatal("want ErrNilDependency")
			}
		})
	}
	if _, err := NewHTTPHandler(m, TokenResolver(nil), nil); err == nil {
		t.Fatal("want ErrNilDependency for nil logger")
	}
}

func TestListSessionsRequiresCaller(t *testing.T) {
	h := newSessionHTTP(t, newFakeStore(), &fakeFlows{}, &fakeEvents{}, nil, time.Now())
	rec := httptest.NewRecorder()
	h.list(rec, withTrace(httptest.NewRequest(http.MethodGet, "/api/v1/me/sessions", nil)))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	resp := decodeError(t, rec)
	if resp.Code != httpx.CodeTokenInvalid {
		t.Fatalf("code = %q", resp.Code)
	}
	if resp.TraceID == "" {
		t.Fatal("trace_id must not be empty")
	}
}

func TestListSessionsShapeAndMasking(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	current := mustID(t)
	other := mustID(t)
	ip := "203.0.113.42"
	device := "iphone-15"
	lastSeen := now.Add(-time.Minute)

	st := newFakeStore()
	st.names["web"] = "门户"
	created := now.Add(-time.Hour)
	st.sessions[current] = &model.Session{
		BaseModel: model.BaseModel{ID: current, CreatedAt: created},
		UserID:    user, ClientID: "web", Status: model.SessionStatusActive, ACR: "AAL2",
		AMR: model.StringArray{"pwd", "totp"}, IP: &ip, DeviceID: &device, LastSeenAt: &lastSeen,
		ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute),
	}
	st.sessions[other] = &model.Session{
		BaseModel: model.BaseModel{ID: other, CreatedAt: created},
		UserID:    user, ClientID: "web", Status: model.SessionStatusActive, ACR: "AAL1",
		ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute),
	}
	h := newSessionHTTP(t, st, &fakeFlows{}, &fakeEvents{}, &Caller{UserID: user, SessionID: current}, now)

	rec := httptest.NewRecorder()
	h.list(rec, withTrace(httptest.NewRequest(http.MethodGet, "/api/v1/me/sessions", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body listResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(body.Items))
	}
	if body.NextCursor != nil {
		t.Fatalf("next_cursor = %v, want null", *body.NextCursor)
	}
	var currentItem, otherItem *sessionView
	for i := range body.Items {
		if body.Items[i].ID == current.String() {
			currentItem = &body.Items[i]
		} else {
			otherItem = &body.Items[i]
		}
	}
	if currentItem == nil || otherItem == nil {
		t.Fatalf("unexpected items: %+v", body.Items)
	}
	if !currentItem.Current || otherItem.Current {
		t.Fatal("current flag must mark the caller's own session only")
	}
	if currentItem.Client.ID != "web" || currentItem.Client.Name != "门户" {
		t.Fatalf("client = %+v", currentItem.Client)
	}
	if currentItem.IP == nil || *currentItem.IP != "203.0.***.***" {
		t.Fatalf("ip = %v, want masked", currentItem.IP)
	}
	if currentItem.Device == nil || *currentItem.Device != device {
		t.Fatalf("device = %v", currentItem.Device)
	}
	if len(currentItem.AMR) != 2 || currentItem.AMR[0] != "pwd" {
		t.Fatalf("amr = %v", currentItem.AMR)
	}
	if otherItem.IP != nil {
		t.Fatalf("nil ip must stay nil, got %v", otherItem.IP)
	}
}

func TestListSessionsPageLimits(t *testing.T) {
	user := mustID(t)
	cases := []struct {
		query string
		want  httpx.Code
		code  int
	}{
		{"?limit=201", httpx.CodePageSizeExceeded, http.StatusBadRequest},
		{"?limit=abc", httpx.CodeInvalidParam, http.StatusBadRequest},
		{"?limit=0", httpx.CodeInvalidParam, http.StatusBadRequest},
	}
	for _, tc := range cases {
		st := newFakeStore()
		h := newSessionHTTP(t, st, &fakeFlows{}, &fakeEvents{}, &Caller{UserID: user}, time.Now())
		rec := httptest.NewRecorder()
		h.list(rec, withTrace(httptest.NewRequest(http.MethodGet, "/api/v1/me/sessions"+tc.query, nil)))
		if rec.Code != tc.code {
			t.Fatalf("%s: status = %d, want %d", tc.query, rec.Code, tc.code)
		}
		if got := decodeError(t, rec).Code; got != tc.want {
			t.Fatalf("%s: code = %q, want %q", tc.query, got, tc.want)
		}
	}
}

func TestRevokeOwnSession(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	id := mustID(t)
	st := newFakeStore()
	st.sessions[id] = &model.Session{UserID: user, ClientID: "web", ACR: "AAL1",
		Status: model.SessionStatusActive, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)}
	flows := &fakeFlows{}
	events := &fakeEvents{}
	h := newSessionHTTP(t, st, flows, events, &Caller{UserID: user, SessionID: mustID(t)}, now)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions/"+id.String(), nil)
	req = withTrace(req)
	req.SetPathValue("id", id.String())
	rec := httptest.NewRecorder()
	h.revokeOne(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if st.sessions[id].Status != model.SessionStatusRevoked {
		t.Fatal("session must be revoked")
	}
	if len(flows.abandoned) != 1 || len(events.events) != 1 {
		t.Fatalf("cascade not executed: flows=%v events=%d", flows.abandoned, len(events.events))
	}
}

func TestRevokeSessionNotOwned(t *testing.T) {
	now := time.Now().UTC()
	owner := mustID(t)
	caller := mustID(t)
	id := mustID(t)
	st := newFakeStore()
	st.sessions[id] = &model.Session{UserID: owner, Status: model.SessionStatusActive,
		ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)}
	h := newSessionHTTP(t, st, &fakeFlows{}, &fakeEvents{}, &Caller{UserID: caller}, now)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions/"+id.String(), nil)
	req = withTrace(req)
	req.SetPathValue("id", id.String())
	rec := httptest.NewRecorder()
	h.revokeOne(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := decodeError(t, rec).Code; got != httpx.CodeSessionNotOwned {
		t.Fatalf("code = %q", got)
	}
	if st.sessions[id].Status != model.SessionStatusActive {
		t.Fatal("other user's session must stay active")
	}
}

func TestRevokeAlreadyRevokedOwnIsIdempotent(t *testing.T) {
	now := time.Now().UTC()
	user := mustID(t)
	id := mustID(t)
	ts := now
	reason := "logout"
	st := newFakeStore()
	st.sessions[id] = &model.Session{UserID: user, Status: model.SessionStatusRevoked,
		RevokedAt: &ts, RevokeReason: &reason}
	h := newSessionHTTP(t, st, &fakeFlows{}, &fakeEvents{}, &Caller{UserID: user}, now)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions/"+id.String(), nil)
	req = withTrace(req)
	req.SetPathValue("id", id.String())
	rec := httptest.NewRecorder()
	h.revokeOne(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRevokeAlreadyRevokedOtherUserIsNotFound(t *testing.T) {
	now := time.Now().UTC()
	owner := mustID(t)
	caller := mustID(t)
	id := mustID(t)
	ts := now
	reason := "logout"
	st := newFakeStore()
	st.sessions[id] = &model.Session{UserID: owner, Status: model.SessionStatusRevoked,
		RevokedAt: &ts, RevokeReason: &reason}
	h := newSessionHTTP(t, st, &fakeFlows{}, &fakeEvents{}, &Caller{UserID: caller}, now)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions/"+id.String(), nil)
	req = withTrace(req)
	req.SetPathValue("id", id.String())
	rec := httptest.NewRecorder()
	h.revokeOne(rec, req)

	if got := decodeError(t, rec).Code; got != httpx.CodeSessionNotOwned {
		t.Fatalf("code = %q, want SESSION_NOT_OWNED", got)
	}
}

func TestRevokeUnknownAndMalformedID(t *testing.T) {
	now := time.Now().UTC()
	user := mustID(t)
	h := newSessionHTTP(t, newFakeStore(), &fakeFlows{}, &fakeEvents{}, &Caller{UserID: user}, now)

	for _, raw := range []string{mustID(t).String(), "not-a-uuid"} {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions/"+raw, nil)
		req = withTrace(req)
		req.SetPathValue("id", raw)
		rec := httptest.NewRecorder()
		h.revokeOne(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d", raw, rec.Code)
		}
		if got := decodeError(t, rec).Code; got != httpx.CodeSessionNotFound {
			t.Fatalf("%s: code = %q", raw, got)
		}
	}
}

func TestRevokeAllSessions(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	a, b := mustID(t), mustID(t)
	st := newFakeStore()
	for _, id := range []uuid.UUID{a, b} {
		st.sessions[id] = &model.Session{UserID: user, ClientID: "web", Status: model.SessionStatusActive,
			ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)}
	}
	events := &fakeEvents{}
	h := newSessionHTTP(t, st, &fakeFlows{}, events, &Caller{UserID: user}, now)

	rec := httptest.NewRecorder()
	h.revokeAll(rec, withTrace(httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions?scope=all", nil)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if st.sessions[a].Status != model.SessionStatusRevoked || st.sessions[b].Status != model.SessionStatusRevoked {
		t.Fatal("all sessions must be revoked")
	}
	if len(events.events) != 2 {
		t.Fatalf("events = %d, want 2", len(events.events))
	}
}

func TestRevokeAllRejectsUnknownScope(t *testing.T) {
	user := mustID(t)
	h := newSessionHTTP(t, newFakeStore(), &fakeFlows{}, &fakeEvents{}, &Caller{UserID: user}, time.Now())
	rec := httptest.NewRecorder()
	h.revokeAll(rec, withTrace(httptest.NewRequest(http.MethodDelete, "/api/v1/me/sessions?scope=some", nil)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := decodeError(t, rec).Code; got != httpx.CodeInvalidParam {
		t.Fatalf("code = %q", got)
	}
}

func TestMaskIP(t *testing.T) {
	cases := []struct {
		in   *string
		want *string
	}{
		{nil, nil},
		{ptr("203.0.113.42"), ptr("203.0.***.***")},
		{ptr("2001:db8:1234:5678::1"), ptr("2001:db8::/****")},
		{ptr("not-an-ip"), ptr("")},
	}
	for i, tc := range cases {
		got := maskIP(tc.in)
		switch {
		case tc.want == nil && got != nil:
			t.Fatalf("case %d: want nil, got %v", i, *got)
		case tc.want != nil && got == nil:
			t.Fatalf("case %d: want %q, got nil", i, *tc.want)
		case tc.want != nil && *got != *tc.want:
			t.Fatalf("case %d: got %q, want %q", i, *got, *tc.want)
		}
	}
}

func TestMessageOfCoversDocumentedCodes(t *testing.T) {
	codes := []httpx.Code{
		httpx.CodeSessionNotFound, httpx.CodeSessionNotOwned, httpx.CodeSessionRevoked,
		httpx.CodeTokenExpired, httpx.CodeTokenInvalid, httpx.CodePageSizeExceeded,
		httpx.CodeInvalidParam,
	}
	for _, c := range codes {
		if msg := messageOf(c); msg == "" || msg == "请求无法完成" {
			t.Fatalf("%q has no dedicated message", c)
		}
	}
	if got := messageOf(httpx.CodeInternalError); got == "" {
		t.Fatal("default message must not be empty")
	}
}

// ---- 测试助手 ----

// withTrace 给请求注入 trace_id（生产路径由 httpx.TraceMiddleware 注入）。
func withTrace(r *http.Request) *http.Request {
	r.Header.Set("Authorization", "Bearer good-token")
	return r.WithContext(httpx.WithTraceID(r.Context(), "trace-test"))
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) httpx.ErrorResponse {
	t.Helper()
	var resp httpx.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v (body=%s)", err, rec.Body.String())
	}
	return resp
}

func ptr(s string) *string { return &s }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}
