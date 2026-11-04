package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func mustUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	return id
}

func withTrace(r *http.Request) *http.Request {
	return r.WithContext(WithTraceID(r.Context(), "trace-test"))
}

func reqWithQuery(q string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/x"+q, nil)
}

func TestParsePageDefaults(t *testing.T) {
	q, err := ParsePage(reqWithQuery(""))
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if q.Limit != DefaultPageLimit {
		t.Fatalf("limit = %d, want %d", q.Limit, DefaultPageLimit)
	}
	if q.Cursor != "" {
		t.Fatalf("cursor = %q, want empty", q.Cursor)
	}
}

func TestParsePageLimitAndCursor(t *testing.T) {
	q, err := ParsePage(reqWithQuery("?limit=7&cursor=abc"))
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if q.Limit != 7 || q.Cursor != "abc" {
		t.Fatalf("q = %+v", q)
	}
	// 上限本身是合法的（> 200 才拒）。
	if _, err := ParsePage(reqWithQuery("?limit=200")); err != nil {
		t.Fatalf("limit=200 must be accepted: %v", err)
	}
}

func TestParsePageErrors(t *testing.T) {
	cases := []struct {
		query string
		want  error
	}{
		{"?limit=201", ErrPageSizeExceeded},
		{"?limit=0", ErrInvalidLimit},
		{"?limit=-3", ErrInvalidLimit},
		{"?limit=abc", ErrInvalidLimit},
	}
	for _, tc := range cases {
		if _, err := ParsePage(reqWithQuery(tc.query)); !errors.Is(err, tc.want) {
			t.Fatalf("%s: want %v, got %v", tc.query, tc.want, err)
		}
	}
}

func TestParsePageIgnoresDeprecatedParams(t *testing.T) {
	// CH-3 作废 `page`/`page_size`：传了不报错、也不生效。
	q, err := ParsePage(reqWithQuery("?page=3&page_size=999"))
	if err != nil {
		t.Fatalf("ParsePage: %v", err)
	}
	if q.Limit != DefaultPageLimit {
		t.Fatalf("deprecated page_size must be ignored, got %d", q.Limit)
	}
}

func TestParsePageNilRequest(t *testing.T) {
	q, err := ParsePage(nil)
	if err != nil || q.Limit != DefaultPageLimit {
		t.Fatalf("nil request = %+v, %v", q, err)
	}
}

func TestCodeOfPageError(t *testing.T) {
	cases := []struct {
		err  error
		want Code
	}{
		{ErrPageSizeExceeded, CodePageSizeExceeded},
		{ErrInvalidLimit, CodeInvalidParam},
		{errors.New("other"), ""},
		{nil, ""},
	}
	for i, tc := range cases {
		if got := CodeOfPageError(tc.err); got != tc.want {
			t.Fatalf("case %d: %q, want %q", i, got, tc.want)
		}
	}
}

func TestDenyAllResolver(t *testing.T) {
	var r CallerResolver = DenyAllResolver{}
	c, err := r.Resolve(nil, reqWithQuery(""))
	if !errors.Is(err, ErrNoCaller) || c != nil {
		t.Fatalf("DenyAllResolver must always refuse: %v, %v", c, err)
	}
}

func TestResolveCaller(t *testing.T) {
	user := mustUUID(t)
	okResolver := staticCaller{caller: &Caller{UserID: user, SessionID: mustUUID(t)}}

	rec := httptest.NewRecorder()
	req := withTrace(reqWithQuery(""))
	caller, ok := ResolveCaller(rec, req, okResolver)
	if !ok || caller == nil || caller.UserID != user {
		t.Fatalf("resolve failed: ok=%v caller=%+v", ok, caller)
	}

	// nil resolver → 401 TOKEN_INVALID（不 panic）。
	rec = httptest.NewRecorder()
	if _, ok := ResolveCaller(rec, withTrace(reqWithQuery("")), nil); ok {
		t.Fatal("nil resolver must refuse")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}

	// 解析成功但 user_id 为空 → 同样拒绝（"解析出来一个没人"等于没解析）。
	rec = httptest.NewRecorder()
	if _, ok := ResolveCaller(rec, withTrace(reqWithQuery("")), staticCaller{caller: &Caller{}}); ok {
		t.Fatal("empty user id must be refused")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestCallerHasRole(t *testing.T) {
	c := &Caller{Roles: []string{"auditor", "system_admin"}}
	if !c.HasRole("auditor") || !c.HasRole("system_admin") {
		t.Fatal("known roles must be found")
	}
	if c.HasRole("security_admin") {
		t.Fatal("unknown role must not match")
	}
	var nilCaller *Caller
	if nilCaller.HasRole("auditor") {
		t.Fatal("nil caller must not have roles")
	}
}

type staticCaller struct{ caller *Caller }

func (s staticCaller) Resolve(_ context.Context, _ *http.Request) (*Caller, error) {
	return s.caller, nil
}
