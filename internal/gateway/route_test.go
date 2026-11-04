package gateway

import (
	"errors"
	"net/url"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestNewRouterValidation(t *testing.T) {
	ok := Route{Prefix: "/billing", ClientID: "billing", Upstream: mustURL(t, "http://127.0.0.1:9101")}

	cases := []struct {
		name   string
		routes []Route
		want   error
	}{
		{"empty route", []Route{{}}, ErrRouteEmpty},
		{"no client", []Route{{Prefix: "/a", Upstream: ok.Upstream}}, ErrRouteEmpty},
		{"no upstream", []Route{{Prefix: "/a", ClientID: "a"}}, ErrRouteEmpty},
		{"prefix without slash", []Route{{Prefix: "a", ClientID: "a", Upstream: ok.Upstream}}, ErrRouteBadPrefix},
		{"prefix with trailing slash", []Route{{Prefix: "/a/", ClientID: "a", Upstream: ok.Upstream}}, ErrRouteBadPrefix},
		{"bad scheme", []Route{{Prefix: "/a", ClientID: "a", Upstream: mustURL(t, "ftp://x/y")}}, ErrRouteBadUpstream},
		{"no host", []Route{{Prefix: "/a", ClientID: "a", Upstream: mustURL(t, "http:///y")}}, ErrRouteBadUpstream},
		{"duplicate prefix", []Route{ok, ok}, ErrRouteDuplicate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewRouter(tc.routes); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	if _, err := NewRouter([]Route{ok}); err != nil {
		t.Fatalf("valid route rejected: %v", err)
	}
	// 根路由 `/` 允许（等价"兜底"），但 `/` 带尾斜杠规则不得误伤它。
	if _, err := NewRouter([]Route{{Prefix: "/", ClientID: "root", Upstream: ok.Upstream}}); err != nil {
		t.Fatalf("root prefix must be allowed: %v", err)
	}
}

// 匹配必须**按路径段**：`/api/bill` 不得命中 `/api/billing`。
//
// 这是越权转发风险：命中错路由等于把 A 业务系统的流量贴上 B 的 client 身份送进 B 的上游。
func TestRouterMatchIsSegmentAware(t *testing.T) {
	router, err := NewRouter([]Route{
		{Prefix: "/api", ClientID: "api", Upstream: mustURL(t, "http://127.0.0.1:9101")},
		{Prefix: "/api/billing", ClientID: "billing", Upstream: mustURL(t, "http://127.0.0.1:9102")},
		{Prefix: "/other", ClientID: "other", Upstream: mustURL(t, "http://127.0.0.1:9103")},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	cases := []struct {
		path, wantClient string
		wantOK           bool
	}{
		{"/api", "api", true},
		{"/api/", "api", true},
		{"/api/users", "api", true},
		{"/api/billing", "billing", true},          // 长前缀优先
		{"/api/billing/invoices", "billing", true}, // 段前缀
		{"/api/billingX", "api", true},             // 不是段边界 → 落回 /api
		{"/other", "other", true},
		{"/otherx", "", false}, // 段边界不成立且无根路由
		{"/", "", false},
		{"", "", false}, // 空 path 归一为 "/"，无路由
	}
	for _, tc := range cases {
		got, ok := router.Match(tc.path)
		if ok != tc.wantOK {
			t.Errorf("Match(%q) ok = %v, want %v", tc.path, ok, tc.wantOK)
			continue
		}
		if ok && got.ClientID != tc.wantClient {
			t.Errorf("Match(%q) client = %q, want %q", tc.path, got.ClientID, tc.wantClient)
		}
	}
}

// 无路由表时匹配一律 false（装配期就该拦住，这里只验兜底语义）。
func TestNilRouterMatch(t *testing.T) {
	var r *Router
	if _, ok := r.Match("/x"); ok {
		t.Fatal("nil router must not match")
	}
	if r.Routes() != nil {
		t.Fatal("nil router must return nil routes")
	}
}

func TestParseRoutes(t *testing.T) {
	routes, err := ParseRoutes(`[
	  {"prefix":"/billing","client_id":"billing","upstream":"http://127.0.0.1:9101"},
	  {"prefix":"/hr","client_id":"hr","upstream":"https://hr.internal:8443/svc"}
	]`)
	if err != nil {
		t.Fatalf("ParseRoutes: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %d, want 2", len(routes))
	}
	if routes[1].Upstream.Path != "/svc" {
		t.Fatalf("upstream path lost: %q", routes[1].Upstream.Path)
	}

	// 空串 = 无路由（不是错误）：保证"没配路由"与"配置写坏"区分开。
	if got, err := ParseRoutes("  "); err != nil || got != nil {
		t.Fatalf("empty spec = %v, %v; want nil, nil", got, err)
	}
	// 写坏的 JSON 必须报错（装配期 fail-fast），不许静默当空表。
	if _, err := ParseRoutes(`{"prefix":"/a"}`); err == nil {
		t.Fatal("non-array JSON must be rejected")
	}
	if _, err := ParseRoutes(`[{"prefix":"/a","client_id":"a","upstream":":::bad"}]`); err == nil {
		t.Fatal("bad upstream URL must be rejected")
	}
	// 解析后仍要过 NewRouter 的语义校验。
	if _, err := NewRouter(routes); err != nil {
		t.Fatalf("parsed routes must be valid: %v", err)
	}
}
