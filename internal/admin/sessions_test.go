package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 管理面 sessions 段的单测：鉴权矩阵、过滤参数、分页、撤销路径与错误码。
// 真实 SQL 与 DB 约束在 `internal/store/repo_sessions_db_test.go`（integration）。

type fakeAdminSessions struct {
	page       *store.SessionPage
	listErr    error
	names      map[string]string
	revoked    []session.RevokeInput
	revokeErr  error
	bulkCalls  []uuid.UUID
	bulkErr    error
	users      map[uuid.UUID]bool
	lastQuery  store.SessionQuery
	namesInput [][]string
}

func (f *fakeAdminSessions) ListAll(_ context.Context, q store.SessionQuery) (*store.SessionPage, error) {
	f.lastQuery = q
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.page, nil
}

func (f *fakeAdminSessions) ClientNames(_ context.Context, ids []string) (map[string]string, error) {
	f.namesInput = append(f.namesInput, ids)
	return f.names, nil
}

func (f *fakeAdminSessions) Revoke(_ context.Context, in session.RevokeInput) error {
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.revoked = append(f.revoked, in)
	return nil
}

func (f *fakeAdminSessions) RevokeAllByUser(_ context.Context, userID uuid.UUID, reason session.RevokeReason,
	eventType string, _ *string) (int64, error) {
	if f.bulkErr != nil {
		return 0, f.bulkErr
	}
	if reason != session.ReasonAdmin || eventType != session.EventRevoke {
		return 0, errors.New("unexpected reason/event type")
	}
	f.bulkCalls = append(f.bulkCalls, userID)
	return 1, nil
}

func (f *fakeAdminSessions) UserExists(_ context.Context, id uuid.UUID) (bool, error) {
	return f.users[id], nil
}

type staticResolver struct {
	caller *httpx.Caller
	err    error
}

func (s staticResolver) Resolve(context.Context, *http.Request) (*httpx.Caller, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.caller, nil
}

func newAdminHTTP(t *testing.T, svc SessionAdmin, caller *httpx.Caller) *SessionsHTTP {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	h, err := NewSessionsHTTP(svc, staticResolver{caller: caller}, logger)
	if err != nil {
		t.Fatalf("NewSessionsHTTP: %v", err)
	}
	return h
}

func adminReq(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	return r.WithContext(httpx.WithTraceID(r.Context(), "trace-admin"))
}

func TestNewSessionsHTTPRequiresDependencies(t *testing.T) {
	svc := &fakeAdminSessions{}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	if _, err := NewSessionsHTTP(nil, staticResolver{}, logger); !errors.Is(err, ErrNilDependency) {
		t.Fatal("nil service must fail")
	}
	if _, err := NewSessionsHTTP(svc, nil, logger); !errors.Is(err, ErrNilDependency) {
		t.Fatal("nil resolver must fail")
	}
	if _, err := NewSessionsHTTP(svc, staticResolver{}, nil); !errors.Is(err, ErrNilDependency) {
		t.Fatal("nil logger must fail")
	}
}

func TestAdminSessionsRequireCaller(t *testing.T) {
	h := newAdminHTTP(t, &fakeAdminSessions{}, nil)
	rec := httptest.NewRecorder()
	h.list(rec, adminReq(http.MethodGet, "/api/v1/sessions"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := adminErrCode(t, rec); got != httpx.CodeTokenInvalid {
		t.Fatalf("code = %q", got)
	}
}

func TestAdminSessionsRoleMatrix(t *testing.T) {
	cases := []struct {
		name   string
		roles  []string
		write  bool
		status int
		code   httpx.Code
	}{
		{"auditor read", []string{"auditor"}, false, http.StatusOK, ""},
		{"auditor write", []string{"auditor"}, true, http.StatusForbidden, httpx.CodeThreePowerViolation},
		{"no role read", []string{"order_admin"}, false, http.StatusForbidden, httpx.CodeInsufficientRole},
		{"no role write", nil, true, http.StatusForbidden, httpx.CodeInsufficientRole},
		{"sys admin write", []string{"system_admin"}, true, http.StatusNoContent, ""},
		{"sec admin write", []string{"security_admin"}, true, http.StatusNoContent, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeAdminSessions{
				page:  &store.SessionPage{Items: []store.SessionRecord{}},
				names: map[string]string{},
				users: map[uuid.UUID]bool{},
			}
			h := newAdminHTTP(t, svc, &httpx.Caller{UserID: uuid.New(), Roles: tc.roles})
			var rec *httptest.ResponseRecorder
			if tc.write {
				id := uuid.New()
				svc.users[id] = true
				req := adminReq(http.MethodDelete, "/api/v1/sessions/"+id.String())
				req.SetPathValue("id", id.String())
				rec = httptest.NewRecorder()
				h.revokeOne(rec, req)
			} else {
				rec = httptest.NewRecorder()
				h.list(rec, adminReq(http.MethodGet, "/api/v1/sessions"))
			}
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.code != "" && adminErrCode(t, rec) != tc.code {
				t.Fatalf("code = %q, want %q", adminErrCode(t, rec), tc.code)
			}
		})
	}
}

func TestAdminListFiltersAndShape(t *testing.T) {
	user := uuid.New()
	clientID := "web"
	created := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	reason := "admin"
	revokedAt := created.Add(time.Minute)
	ip := "198.51.100.7"

	svc := &fakeAdminSessions{
		page: &store.SessionPage{
			Items: []store.SessionRecord{{
				BaseModel:    model.BaseModel{ID: uuid.New(), CreatedAt: created},
				UserID:       user,
				ClientID:     clientID,
				Status:       store.SessionStatusRevoked,
				ACR:          "AAL2",
				AMR:          model.StringArray{"pwd", "totp"},
				IssuedAt:     created,
				ExpiresAt:    created.Add(12 * time.Hour),
				RevokedAt:    &revokedAt,
				RevokeReason: &reason,
				IP:           &ip,
			}},
			NextCursor: "next-1",
		},
		names: map[string]string{"web": "门户"},
	}
	h := newAdminHTTP(t, svc, &httpx.Caller{UserID: uuid.New(), Roles: []string{"auditor"}})

	rec := httptest.NewRecorder()
	h.list(rec, adminReq(http.MethodGet,
		"/api/v1/sessions?user_id="+user.String()+"&client_id=web&active=true&limit=10&cursor=abc"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.lastQuery.UserID == nil || *svc.lastQuery.UserID != user {
		t.Fatalf("user filter not passed: %+v", svc.lastQuery)
	}
	if svc.lastQuery.ClientID == nil || *svc.lastQuery.ClientID != "web" {
		t.Fatalf("client filter not passed: %+v", svc.lastQuery)
	}
	if svc.lastQuery.Active == nil || !*svc.lastQuery.Active {
		t.Fatalf("active filter not passed: %+v", svc.lastQuery)
	}
	if svc.lastQuery.Limit != 10 || svc.lastQuery.Cursor != "abc" {
		t.Fatalf("pagination not passed: %+v", svc.lastQuery)
	}

	var body adminListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 {
		t.Fatalf("items = %d", len(body.Items))
	}
	item := body.Items[0]
	if item.UserID != user.String() || item.Client.Name != "门户" || item.Status != store.SessionStatusRevoked {
		t.Fatalf("item = %+v", item)
	}
	if item.RevokeReason == nil || *item.RevokeReason != "admin" {
		t.Fatalf("revoke_reason = %v", item.RevokeReason)
	}
	if body.NextCursor == nil || *body.NextCursor != "next-1" {
		t.Fatalf("next_cursor = %v", body.NextCursor)
	}
}

func TestAdminListRejectsBadParams(t *testing.T) {
	cases := []struct {
		query  string
		code   httpx.Code
		status int
	}{
		{"?user_id=not-a-uuid", httpx.CodeInvalidParam, http.StatusBadRequest},
		{"?active=maybe", httpx.CodeInvalidParam, http.StatusBadRequest},
		{"?limit=201", httpx.CodePageSizeExceeded, http.StatusBadRequest},
		{"?limit=-1", httpx.CodeInvalidParam, http.StatusBadRequest},
	}
	for _, tc := range cases {
		svc := &fakeAdminSessions{page: &store.SessionPage{}, names: map[string]string{}}
		h := newAdminHTTP(t, svc, &httpx.Caller{UserID: uuid.New(), Roles: []string{"auditor"}})
		rec := httptest.NewRecorder()
		h.list(rec, adminReq(http.MethodGet, "/api/v1/sessions"+tc.query))
		if rec.Code != tc.status {
			t.Fatalf("%s: status = %d", tc.query, rec.Code)
		}
		if got := adminErrCode(t, rec); got != tc.code {
			t.Fatalf("%s: code = %q, want %q", tc.query, got, tc.code)
		}
	}
}

func TestAdminRevokeOne(t *testing.T) {
	id := uuid.New()
	svc := &fakeAdminSessions{users: map[uuid.UUID]bool{id: true}}
	h := newAdminHTTP(t, svc, &httpx.Caller{UserID: uuid.New(), Roles: []string{"security_admin"}})

	req := adminReq(http.MethodDelete, "/api/v1/sessions/"+id.String())
	req.SetPathValue("id", id.String())
	rec := httptest.NewRecorder()
	h.revokeOne(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(svc.revoked) != 1 {
		t.Fatalf("revoked calls = %d", len(svc.revoked))
	}
	if svc.revoked[0].SessionID != id || svc.revoked[0].Reason != session.ReasonAdmin {
		t.Fatalf("revoke input = %+v", svc.revoked[0])
	}
}

func TestAdminRevokeOneNotFound(t *testing.T) {
	svc := &fakeAdminSessions{revokeErr: session.ErrSessionNotFound}
	h := newAdminHTTP(t, svc, &httpx.Caller{UserID: uuid.New(), Roles: []string{"system_admin"}})

	req := adminReq(http.MethodDelete, "/api/v1/sessions/x")
	req.SetPathValue("id", uuid.New().String())
	rec := httptest.NewRecorder()
	h.revokeOne(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := adminErrCode(t, rec); got != httpx.CodeSessionNotFound {
		t.Fatalf("code = %q", got)
	}
}

func TestAdminRevokeOneMalformedID(t *testing.T) {
	svc := &fakeAdminSessions{}
	h := newAdminHTTP(t, svc, &httpx.Caller{UserID: uuid.New(), Roles: []string{"system_admin"}})

	req := adminReq(http.MethodDelete, "/api/v1/sessions/not-a-uuid")
	req.SetPathValue("id", "not-a-uuid")
	rec := httptest.NewRecorder()
	h.revokeOne(rec, req)

	if got := adminErrCode(t, rec); got != httpx.CodeSessionNotFound {
		t.Fatalf("code = %q", got)
	}
	if len(svc.revoked) != 0 {
		t.Fatal("malformed id must not reach the service")
	}
}

func TestAdminRevokeByUser(t *testing.T) {
	user := uuid.New()
	svc := &fakeAdminSessions{users: map[uuid.UUID]bool{user: true}}
	h := newAdminHTTP(t, svc, &httpx.Caller{UserID: uuid.New(), Roles: []string{"system_admin"}})

	rec := httptest.NewRecorder()
	h.revokeByUser(rec, adminReq(http.MethodDelete, "/api/v1/sessions?user_id="+user.String()))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(svc.bulkCalls) != 1 || svc.bulkCalls[0] != user {
		t.Fatalf("bulk calls = %v", svc.bulkCalls)
	}
}

func TestAdminRevokeByUserErrors(t *testing.T) {
	cases := []struct {
		name   string
		query  string
		users  map[uuid.UUID]bool
		code   httpx.Code
		status int
	}{
		{"missing user_id", "", nil, httpx.CodeMissingParameter, http.StatusBadRequest},
		{"bad user_id", "?user_id=nope", nil, httpx.CodeInvalidParam, http.StatusBadRequest},
		{"unknown user", "?user_id=" + uuid.New().String(), map[uuid.UUID]bool{}, httpx.CodeUserNotFound, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeAdminSessions{users: tc.users}
			h := newAdminHTTP(t, svc, &httpx.Caller{UserID: uuid.New(), Roles: []string{"system_admin"}})
			rec := httptest.NewRecorder()
			h.revokeByUser(rec, adminReq(http.MethodDelete, "/api/v1/sessions"+tc.query))
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := adminErrCode(t, rec); got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestAdminUnmappedErrorIsInternal(t *testing.T) {
	svc := &fakeAdminSessions{listErr: errors.New("boom")}
	h := newAdminHTTP(t, svc, &httpx.Caller{UserID: uuid.New(), Roles: []string{"auditor"}})
	rec := httptest.NewRecorder()
	h.list(rec, adminReq(http.MethodGet, "/api/v1/sessions"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := adminErrCode(t, rec); got != httpx.CodeInternalError {
		t.Fatalf("code = %q", got)
	}
}

func TestAdminMessageOf(t *testing.T) {
	codes := []httpx.Code{
		httpx.CodeInsufficientRole, httpx.CodeThreePowerViolation, httpx.CodeSessionNotFound,
		httpx.CodeUserNotFound, httpx.CodePageSizeExceeded, httpx.CodeInvalidParam,
		httpx.CodeMissingParameter,
	}
	for _, c := range codes {
		if msg := adminMessageOf(c); msg == "" || msg == "请求无法完成" {
			t.Fatalf("%q has no dedicated message", c)
		}
	}
}

func adminErrCode(t *testing.T, rec *httptest.ResponseRecorder) httpx.Code {
	t.Helper()
	var resp httpx.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error response: %v (body=%s)", err, rec.Body.String())
	}
	return resp.Code
}
