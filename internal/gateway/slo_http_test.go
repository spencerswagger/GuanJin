package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/signing"
)

// 本文件是 **R12 = 候选 A**（SLO 最小投递契约）的单测：
// 路径默认值、`X-GuanJin-Assertion` 的十项 claim、目标取路由表 upstream、超时/重试、
// 以及"投不出去时如实报错（由 `SloBroadcaster` 吞掉、不阻塞撤销）"。

const (
	sloTestClient = "billing"
	sloTestUser   = "0193f0aa-0000-7000-8000-000000000001"
)

// fakeSloUpstream 记录收到的通知（路径/头/次数），并可按需返回失败。
type fakeSloUpstream struct {
	srv      *httptest.Server
	mu       sync.Mutex
	paths    []string
	headers  []http.Header
	failures int // 前 N 次返回 500（模拟"坚持要失败的端点"）
}

func newFakeSloUpstream(t *testing.T, failures int) *fakeSloUpstream {
	t.Helper()
	u := &fakeSloUpstream{failures: failures}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.paths = append(u.paths, r.URL.Path)
		u.headers = append(u.headers, r.Header.Clone())
		fail := len(u.paths) <= u.failures
		u.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *fakeSloUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.paths)
}

func (u *fakeSloUpstream) lastHeader() http.Header {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.headers) == 0 {
		return nil
	}
	return u.headers[len(u.headers)-1]
}

func (u *fakeSloUpstream) upstream(t *testing.T) *url.URL {
	t.Helper()
	parsed, err := url.Parse(u.srv.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	return parsed
}

func sloNotice(clientIDs ...string) RevocationNotice {
	return RevocationNotice{
		UserID: sloTestUser, SessionID: "0193f0aa-0000-7000-8000-000000000002",
		ClientIDs: clientIDs, Reason: "logout", At: time.Now().UTC(),
		Subject: &AssertionInput{
			Sub: sloTestUser, CID: sloTestClient, ACR: "AAL1", AMR: []string{"pwd"},
			Uname: "alice", Roles: []string{"viewer"},
		},
	}
}

func newTestNotifier(t *testing.T, up *fakeSloUpstream, keys *signing.Set) *HTTPNotifier {
	t.Helper()
	signer, err := NewAssertionSigner(keys)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	notifier, err := NewHTTPNotifier(signer, func(clientID string) (*url.URL, bool) {
		if clientID != sloTestClient {
			return nil, false
		}
		return up.upstream(t), true
	}, HTTPNotifierConfig{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("NewHTTPNotifier: %v", err)
	}
	return notifier
}

// 候选 A 的路径默认值 + 十项 claim + 可被 JWKS 验签。
func TestSloNotifierUsesCandidateA(t *testing.T) {
	keys := newTestKeys(t)
	up := newFakeSloUpstream(t, 0)
	notifier := newTestNotifier(t, up, keys)

	if err := notifier.Notify(context.Background(), sloNotice(sloTestClient)); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	up.mu.Lock()
	gotPath := up.paths[0]
	up.mu.Unlock()
	if gotPath != DefaultSloNotifyPath {
		t.Fatalf("投递路径期望默认值 %q，实际 %q", DefaultSloNotifyPath, gotPath)
	}
	if up.count() != 1 {
		t.Fatalf("期望 1 次投递，实际 %d", up.count())
	}

	header := up.lastHeader()
	assertion := ""
	for k, v := range header {
		if strings.EqualFold(k, AssertionHeader) && len(v) > 0 {
			assertion = v[0]
		}
	}
	if assertion == "" {
		t.Fatal("通知里没有 X-GuanJin-Assertion")
	}
	claims := verifySloAssertion(t, keys, assertion)

	// 十项 claim（与数据面断言同一套；候选 A 明确"复用既有十项 claim"）。
	want := []string{"iss", "sub", "aud", "cid", "uname", "roles", "acr", "amr", "iat", "exp", "jti"}
	for _, name := range want {
		if _, ok := claims[name]; !ok {
			t.Fatalf("通知断言缺 claim %q：%v", name, claims)
		}
	}
	if claims["iss"] != AssertionIssuer {
		t.Fatalf("iss 期望 %q，实际 %v", AssertionIssuer, claims["iss"])
	}
	if claims["sub"] != sloTestUser || claims["aud"] != sloTestClient {
		t.Fatalf("sub/aud 不符：%v / %v", claims["sub"], claims["aud"])
	}
	if claims["uname"] != "alice" || claims["acr"] != "AAL1" {
		t.Fatalf("uname/acr 不符：%v / %v", claims["uname"], claims["acr"])
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if int64(exp)-int64(iat) != 60 {
		t.Fatalf("exp-iat 期望 60，实际 %d", int64(exp)-int64(iat))
	}
	if jti, _ := claims["jti"].(string); len(jti) != 36 {
		t.Fatalf("jti 期望 UUID，实际 %q", jti)
	}
}

// 重试：第一次 500 → 重试 1 次后成功（共 2 次投递），这是 A 的"重试 1 次"。
func TestSloNotifierRetriesOnce(t *testing.T) {
	keys := newTestKeys(t)
	up := newFakeSloUpstream(t, 1)
	notifier := newTestNotifier(t, up, keys)

	if err := notifier.Notify(context.Background(), sloNotice(sloTestClient)); err != nil {
		t.Fatalf("重试后应成功，实际 %v", err)
	}
	if up.count() != 2 {
		t.Fatalf("期望 2 次投递（首次 + 重试 1 次），实际 %d", up.count())
	}
}

// 端点一直失败 → 如实返回错误（由 broadcaster 计失败；**不影响**撤销结果）。
func TestSloNotifierReportsPersistentFailure(t *testing.T) {
	keys := newTestKeys(t)
	up := newFakeSloUpstream(t, 99)
	notifier := newTestNotifier(t, up, keys)

	err := notifier.Notify(context.Background(), sloNotice(sloTestClient))
	if err == nil {
		t.Fatal("端点持续 500 时必须返回错误（否则失败会被静默吞掉）")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("错误信息应带状态码，实际 %v", err)
	}
}

// 路由表里没有该 client 的上游 → 明确哨兵（配置缺失，不是"投递成功"）。
func TestSloNotifierRequiresUpstream(t *testing.T) {
	keys := newTestKeys(t)
	up := newFakeSloUpstream(t, 0)
	notifier := newTestNotifier(t, up, keys)

	err := notifier.Notify(context.Background(), sloNotice("crm"))
	if !errors.Is(err, ErrSloNoUpstream) {
		t.Fatalf("期望 ErrSloNoUpstream，实际 %v", err)
	}
	if up.count() != 0 {
		t.Fatalf("没有上游时不该发出任何请求，实际 %d 次", up.count())
	}
}

// 没有身份事实 → 拒签并报错（不投一个"没有 sub"的通知出去）。
func TestSloNotifierRequiresSubject(t *testing.T) {
	keys := newTestKeys(t)
	up := newFakeSloUpstream(t, 0)
	notifier := newTestNotifier(t, up, keys)

	notice := sloNotice(sloTestClient)
	notice.Subject = nil
	if err := notifier.Notify(context.Background(), notice); err == nil {
		t.Fatal("缺 Subject 必须报错")
	}
	if up.count() != 0 {
		t.Fatalf("不该发出请求，实际 %d 次", up.count())
	}
}

// 构造期校验：缺 signer / 缺 upstream 查询 / 非法 path → 直接失败（装配期 fail-fast）。
func TestNewHTTPNotifierValidation(t *testing.T) {
	keys := newTestKeys(t)
	signer, err := NewAssertionSigner(keys)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	lookup := func(string) (*url.URL, bool) { return nil, false }

	if _, err := NewHTTPNotifier(nil, lookup, HTTPNotifierConfig{}); err == nil {
		t.Fatal("缺 signer 必须报错")
	}
	if _, err := NewHTTPNotifier(signer, nil, HTTPNotifierConfig{}); err == nil {
		t.Fatal("缺 upstream lookup 必须报错")
	}
	if _, err := NewHTTPNotifier(signer, lookup, HTTPNotifierConfig{Path: "internal/relative"}); err == nil {
		t.Fatal("非绝对路径必须报错")
	}
	n, err := NewHTTPNotifier(signer, lookup, HTTPNotifierConfig{})
	if err != nil {
		t.Fatalf("默认配置应可用：%v", err)
	}
	if n.path != DefaultSloNotifyPath {
		t.Fatalf("默认路径不符：%q", n.path)
	}
}

// 自定义路径生效（部署方可以改，但默认值就是 A 定的那个）。
func TestSloNotifierCustomPath(t *testing.T) {
	keys := newTestKeys(t)
	up := newFakeSloUpstream(t, 0)
	signer, err := NewAssertionSigner(keys)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	notifier, err := NewHTTPNotifier(signer, func(string) (*url.URL, bool) { return up.upstream(t), true },
		HTTPNotifierConfig{Path: "/hooks/guanjin/logout", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("NewHTTPNotifier: %v", err)
	}
	if err := notifier.Notify(context.Background(), sloNotice(sloTestClient)); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.paths[0] != "/hooks/guanjin/logout" {
		t.Fatalf("自定义路径未生效：%q", up.paths[0])
	}
}

// `UpstreamsFromRouter`：client → upstream 的唯一折法（同 client 多条路由取首条）。
func TestUpstreamsFromRouter(t *testing.T) {
	a, _ := url.Parse("http://127.0.0.1:9101")
	b, _ := url.Parse("http://127.0.0.1:9102")
	router, err := NewRouter([]Route{
		{Prefix: "/api/bill", ClientID: "billing", Upstream: a},
		{Prefix: "/api/billing", ClientID: "billing", Upstream: b},
		{Prefix: "/api/crm", ClientID: "crm", Upstream: b},
	})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	lookup := UpstreamsFromRouter(router)
	if got, ok := lookup("billing"); !ok || got.Host != a.Host && got.Host != b.Host {
		t.Fatalf("billing 上游不符：%v %v", got, ok)
	}
	if got, ok := lookup("crm"); !ok || got.Host != b.Host {
		t.Fatalf("crm 上游不符：%v %v", got, ok)
	}
	if _, ok := lookup("unknown"); ok {
		t.Fatal("未知 client 不该有上游")
	}
}

// verifySloAssertion 用 JWKS 公钥验签（与业务方 SDK 同一条路径）。
func verifySloAssertion(t *testing.T, keys *signing.Set, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("断言不是三段式：%q", token)
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header struct {
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hb, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	for _, jwk := range keys.JWKS() {
		if jwk.Kid != header.Kid {
			continue
		}
		claims, err := signing.VerifyJWT(jwkPublicForTest(t, jwk), token)
		if err != nil {
			t.Fatalf("通知断言验签失败：%v", err)
		}
		return claims
	}
	t.Fatalf("JWKS 里没有 kid=%q", header.Kid)
	return nil
}
