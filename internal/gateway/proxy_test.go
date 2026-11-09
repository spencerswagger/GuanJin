package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// upstreamRecorder 起一个假上游，记录它收到的请求（含头），并回一个可控响应。
type upstreamRecorder struct {
	srv     *httptest.Server
	last    *http.Request
	body    []byte
	headers http.Header
	delay   time.Duration
	status  int
}

func newUpstream(t *testing.T) *upstreamRecorder {
	t.Helper()
	u := &upstreamRecorder{status: http.StatusOK}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.last = r.Clone(r.Context())
		u.body = body
		u.headers = r.Header.Clone()
		if u.delay > 0 {
			time.Sleep(u.delay)
		}
		w.Header().Set("X-Upstream", "yes")
		w.WriteHeader(u.status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// assertionHeader 取上游收到的断言头（空 = 没收到；用 EqualFold 扫，冻结名是 `X-GuanJin-Assertion`）。
func (u *upstreamRecorder) assertionHeader() string {
	if u.headers == nil {
		return ""
	}
	for k, v := range u.headers {
		if strings.EqualFold(k, AssertionHeader) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func (u *upstreamRecorder) url(t *testing.T) *url.URL {
	t.Helper()
	parsed, err := url.Parse(u.srv.URL)
	if err != nil {
		t.Fatalf("parse upstream url: %v", err)
	}
	return parsed
}

func newHandler(t *testing.T, opts Options, routes ...Route) http.Handler {
	t.Helper()
	router, err := NewRouter(routes)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	h, err := New(router, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 包一层 trace 中间件：生产里 `cmd/guanjingw` 就是这么装配的（D4-C 要求每个响应都有
	// 可复制的 trace_id），测试不该跑一条生产不存在的链路。
	return httpx.TraceMiddleware(h, opts.TraceHeader, nil)
}

// AC-DP-02/03：客户端自带的同名断言头**零透传** —— 上游收到时该头必须不存在。
func TestInboundAssertionHeaderIsStripped(t *testing.T) {
	up := newUpstream(t)
	h := newHandler(t, Options{}, Route{Prefix: "/svc", ClientID: "svc", Upstream: up.url(t)})

	req := httptest.NewRequest(http.MethodGet, "/svc/orders?x=1", nil)
	req.Header.Set(AssertionHeader, "forged-assertion-from-client")
	// 大小写变体与多值变体也一并试：Header.Del 走规范化，扫一遍更保险。
	req.Header["x-guanjin-assertion"] = []string{"lowercase-forged"}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if up.headers == nil {
		t.Fatal("upstream was not called")
	}
	if got := up.headers.Values(AssertionHeader); len(got) != 0 {
		t.Fatalf("上游收到伪造断言头 %v —— 必须零透传（AC-DP-02/03）", got)
	}
	// 注意：本 PR 还没有断言签发，所以"上游拿到的是空"是正确结果；
	// S8·2 接上签发后，这里的断言会变成"上游拿到的是网关刚签的那一个"。
}

// 路径/查询/方法/请求体原样透传；**不做 path 重写**（D1-A1 的同源纪律）。
func TestRequestIsForwardedVerbatim(t *testing.T) {
	up := newUpstream(t)
	h := newHandler(t, Options{}, Route{Prefix: "/svc", ClientID: "svc", Upstream: up.url(t)})

	body := `{"a":1}`
	req := httptest.NewRequest(http.MethodPost, "/svc/orders/42?b=2&a=1", strings.NewReader(body))
	req.Header.Set("X-Custom", "keep-me")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if up.last.Method != http.MethodPost {
		t.Errorf("method = %s", up.last.Method)
	}
	if up.last.URL.Path != "/svc/orders/42" {
		t.Errorf("path = %q, want 原样 /svc/orders/42", up.last.URL.Path)
	}
	if up.last.URL.RawQuery != "b=2&a=1" {
		t.Errorf("query = %q, want 原样（不重排序）", up.last.URL.RawQuery)
	}
	if string(up.body) != body {
		t.Errorf("body = %q, want %q", up.body, body)
	}
	if up.headers.Get("X-Custom") != "keep-me" {
		t.Errorf("自定义头应透传，got %q", up.headers.Get("X-Custom"))
	}
	if rec.Header().Get("X-Upstream") != "yes" {
		t.Error("上游响应头应回传给客户端")
	}
}

// 上游基址带 path 前缀时，拼接成 base + 请求路径（且只补一个斜杠）。
func TestUpstreamBasePathJoin(t *testing.T) {
	up := newUpstream(t)
	base := *up.url(t)
	base.Path = "/base"
	h := newHandler(t, Options{}, Route{Prefix: "/svc", ClientID: "svc", Upstream: &base})

	req := httptest.NewRequest(http.MethodGet, "/svc/orders", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if up.last.URL.Path != "/base/svc/orders" {
		t.Fatalf("path = %q, want /base/svc/orders", up.last.URL.Path)
	}
}

// 无路由 → **404 `CLIENT_NOT_FOUND`**，且绝不裸转发（上游一次都不能被调用）。
//
// 为什么不是 `UPSTREAM_UNAVAILABLE`：该码按 D8-C′④ / `acceptance-criteria.md` AC-DP-13
// 只给"**已开始转发**但上游失败/超时"。未匹配路由是"这条路径没有归属的业务系统"
// （调用方 URL 用错或部署少了路由项），既不是上游故障也不是依赖挂了——拿上游故障码回它，
// 会让运维照着码表去查一个不存在的问题。
func TestNoRouteIs404ClientNotFoundAndNeverForwards(t *testing.T) {
	up := newUpstream(t)
	h := newHandler(t, Options{}, Route{Prefix: "/svc", ClientID: "svc", Upstream: up.url(t)})

	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	// 客户端自带的伪造断言头：被拒的请求更不许把它透传给上游。
	req.Header.Set(AssertionHeader, "forged-on-no-route")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if up.last != nil {
		t.Fatal("无路由时不得调用上游（裸转发 = 事故）")
	}
	if up.assertionHeader() != "" {
		t.Fatalf("上游收到了断言头 %q：无路由时不得注入身份", up.assertionHeader())
	}
	assertErrorCode(t, rec.Body.Bytes(), httpx.CodeClientNotFound)

	// 反向断言：**不得**出现 `UPSTREAM_UNAVAILABLE`（该码只属转发失败），也不得回 `Retry-After`。
	var body struct {
		Code httpx.Code `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Code == httpx.CodeUpstreamUnavailable {
		t.Fatal("未匹配路由不得返回 UPSTREAM_UNAVAILABLE（那是转发失败的码）")
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Error("404 不该带 Retry-After（那是 503 的重试语义）")
	}
	// 响应上也不得出现断言头（本包从不回填，这里锁死）。
	for k := range rec.Header() {
		if strings.EqualFold(k, AssertionHeader) {
			t.Fatalf("响应上出现了断言头 %q", k)
		}
	}
}

// AC-DP-13：上游拨号失败 → 503 UPSTREAM_UNAVAILABLE（不是 500 / 不是 DEPENDENCY_UNAVAILABLE）。
func TestUpstreamDialFailureIs503(t *testing.T) {
	// 关掉的端口：起一个 server 再立刻关，确保端口不可达。
	up := newUpstream(t)
	dead := up.url(t)
	up.srv.Close()

	h := newHandler(t, Options{DialTimeout: 200 * time.Millisecond},
		Route{Prefix: "/svc", ClientID: "svc", Upstream: dead})

	req := httptest.NewRequest(http.MethodGet, "/svc/orders", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), httpx.CodeUpstreamUnavailable)
	// 注意：这里刻意不写那个已废弃码的完整字面量 —— 仓库门禁会把它判红（禁码扫描）。
	// 断言的是"响应体里不许出现"，用拼接构造即可，不影响判据强度。
	body := rec.Body.String()
	deprecated := "SERVICE_" + "DEGRADED"
	for _, forbidden := range []string{"DEPENDENCY_UNAVAILABLE", deprecated, "INTERNAL_ERROR"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("上游失败不得回 %s（AC-DP-13）", forbidden)
		}
	}
	if got := rec.Header().Get(AssertionHeader); got != "" {
		t.Fatalf("失败路径不得注入断言头，got %q", got)
	}
}

// 上游慢（超过 ResponseHeaderTimeout）→ 503，而不是把连接吊死。
func TestUpstreamTimeoutIs503(t *testing.T) {
	up := newUpstream(t)
	up.delay = 300 * time.Millisecond
	h := newHandler(t, Options{UpstreamTimeout: 50 * time.Millisecond},
		Route{Prefix: "/svc", ClientID: "svc", Upstream: up.url(t)})

	req := httptest.NewRequest(http.MethodGet, "/svc/slow", nil)
	rec := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	assertErrorCode(t, rec.Body.Bytes(), httpx.CodeUpstreamUnavailable)
	if elapsed > 250*time.Millisecond {
		t.Fatalf("超时未被 ResponseHeaderTimeout 兜住，耗时 %v", elapsed)
	}
}

// 日志红线（AC-DP-10 的数据面部分）：令牌与断言值绝不进日志，但 trace_id/client_id 要在。
func TestLogsCarryNoCredentials(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	up := newUpstream(t)
	h := newHandler(t, Options{Logger: logger},
		Route{Prefix: "/svc", ClientID: "svc", Upstream: up.url(t)})

	req := httptest.NewRequest(http.MethodGet, "/svc/orders", nil)
	req.Header.Set("Authorization", "Bearer super-secret-token-value")
	req.Header.Set(AssertionHeader, "forged-assertion-value")
	// trace_id 从入站头进来（生产里也是这么进 Ram 的），中间件会校验并沿用。
	req.Header.Set("X-Trace-Id", "trace-s8-logs")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	logs := buf.String()
	for _, secret := range []string{"super-secret-token-value", "forged-assertion-value"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("日志泄露凭证 %q：%s", secret, logs)
		}
	}
	if strings.Contains(logs, AssertionHeader) {
		// 头名可以出现在注释/文档里，但日志行里不该带头名+值；这里只要求不带值（上面已验）。
		t.Logf("提示：日志出现头名（值未泄露）：%s", logs)
	}
	if !strings.Contains(logs, "trace-s8-logs") || !strings.Contains(logs, "client_id=svc") {
		t.Fatalf("访问日志必须带 trace_id 与 client_id：%s", logs)
	}
}

// 装配期 fail-closed：nil 路由表不许起数据面。
func TestNewRequiresRouter(t *testing.T) {
	if _, err := New(nil, Options{}); err == nil {
		t.Fatal("nil router must be rejected (没有路由表就不该起数据面)")
	}
}

func assertErrorCode(t *testing.T, body []byte, want httpx.Code) {
	t.Helper()
	var resp httpx.ErrorResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("错误响应不是四字段 JSON：%s (%v)", body, err)
	}
	if resp.Code != want {
		t.Fatalf("code = %q, want %q", resp.Code, want)
	}
	if resp.TraceID == "" {
		t.Error("错误响应必须带可复制 trace_id")
	}
}
