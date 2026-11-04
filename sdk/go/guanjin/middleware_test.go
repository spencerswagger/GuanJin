package guanjin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestSDK(t *testing.T) (*SDK, *[]byte) {
	t.Helper()
	priv := genKey(t)
	body := jwksBody("k1", &priv.PublicKey)
	srv := jwksServer(t, func() []byte { return body })
	cfg := testConfig(t, srv.URL)
	sdk, err := New(cfg, WithEnv(EnvTest), WithReplayGuard(NewMemoryReplayGuard()), WithLogger(func(string, ...any) {}))
	if err != nil {
		t.Fatalf("构造 SDK 失败：%v", err)
	}
	raw := []byte(signAssertion(t, priv, "k1", claims(time.Now().Unix())))
	return sdk, &raw
}

func TestMiddlewareD10SixSteps(t *testing.T) {
	sdk, rawPtr := newTestSDK(t)
	raw := *rawPtr

	var sawHeader string
	var sawPrincipal *Principal
	h := sdk.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ⑤ 之后：handler 与日志都取不到断言头（含 Header 名枚举）
		sawHeader = r.Header.Get(sdk.Config().Gateway.AssertionHeader)
		for name := range r.Header {
			if http.CanonicalHeaderKey(name) == http.CanonicalHeaderKey(sdk.Config().Gateway.AssertionHeader) {
				t.Errorf("Header 枚举里仍有断言头：%s", name)
			}
		}
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			t.Error("handler 应能从 context 取到身份")
		}
		sawPrincipal = p
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/orders/1", nil)
	req.Header.Set(sdk.Config().Gateway.AssertionHeader, string(raw))
	req.Header.Set("X-Trace-Id", "trace-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("应放行，实得 %d（%s）", rec.Code, rec.Body.String())
	}
	if sawHeader != "" {
		t.Fatalf("断言头必须被剥离，handler 仍读到 %q", sawHeader)
	}
	if sawPrincipal == nil || sawPrincipal.Sub != "u-1" {
		t.Fatalf("身份注入不对：%+v", sawPrincipal)
	}
	if got := req.Header.Get(sdk.Config().Gateway.AssertionHeader); got != "" {
		t.Fatal("原请求上的断言头也应被删除（防外层日志中间件读到）")
	}
}

func TestMiddlewareMissingAndTamperedAre401(t *testing.T) {
	sdk, rawPtr := newTestSDK(t)
	raw := *rawPtr
	h := sdk.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("不应进入 handler")
	}))

	t.Run("缺头", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders", nil))
		assertErrorCode(t, rec, http.StatusUnauthorized, CodeTokenInvalid)
	})
	t.Run("篡改令牌（反向断言：验签在剥离之前）", func(t *testing.T) {
		b := []byte(raw)
		i := indexByte(b, '.')
		if b[i+1] == 'A' {
			b[i+1] = 'B'
		} else {
			b[i+1] = 'A'
		}
		req := httptest.NewRequest(http.MethodGet, "/orders", nil)
		req.Header.Set(sdk.Config().Gateway.AssertionHeader, string(b))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assertErrorCode(t, rec, http.StatusUnauthorized, CodeTokenInvalid)
	})
}

func TestMiddlewareReplayRejected(t *testing.T) {
	sdk, rawPtr := newTestSDK(t)
	raw := *rawPtr
	h := sdk.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	do := func() int {
		req := httptest.NewRequest(http.MethodGet, "/orders", nil)
		req.Header.Set(sdk.Config().Gateway.AssertionHeader, string(raw))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := do(); code != http.StatusOK {
		t.Fatalf("首次应放行，实得 %d", code)
	}
	if code := do(); code != http.StatusUnauthorized {
		t.Fatalf("同一 jti 第二次必须 401（GJ-CAP-07），实得 %d", code)
	}
}

func TestMiddlewarePublicPathSkipsVerification(t *testing.T) {
	sdk, _ := newTestSDK(t)
	h := sdk.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := PrincipalFrom(r.Context()); ok {
			t.Error("公开路径不应注入身份")
		}
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz 应公开，实得 %d", rec.Code)
	}
}

func TestReplayUnavailableFailsClosed(t *testing.T) {
	priv := genKey(t)
	srv := jwksServer(t, func() []byte { return jwksBody("k1", &priv.PublicKey) })
	cfg := testConfig(t, srv.URL)
	// 注入一个永远报错的 ReplayGuard → 必须 503 REDIS_UNAVAILABLE（不许放行）
	sdk, err := New(cfg, WithEnv(EnvTest), WithReplayGuard(brokenReplay{}), WithLogger(func(string, ...any) {}))
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	h := sdk.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { t.Fatal("不得放行") }))
	req := httptest.NewRequest(http.MethodGet, "/orders", nil)
	req.Header.Set(cfg.Gateway.AssertionHeader, signAssertion(t, priv, "k1", claims(time.Now().Unix())))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assertErrorCode(t, rec, http.StatusServiceUnavailable, CodeRedisUnavailable)
}

type brokenReplay struct{}

func (brokenReplay) FirstUse(_ context.Context, _ string, _ time.Time) (bool, error) {
	return false, errors.New("redis down")
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("状态码应为 %d，实得 %d（%s）", wantStatus, rec.Code, rec.Body.String())
	}
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if body.Code != wantCode {
		t.Fatalf("错误码应为 %s，实得 %s", wantCode, body.Code)
	}
	if body.Message == "" {
		t.Fatal("message 不得为空")
	}
}

func TestNewFailsWhenRedisConfiguredWithoutClient(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Platform.BaseURL = "https://gj.example.com"
	cfg.Client.ID, cfg.Client.HMACSecret = "x", "y"
	// 默认 replay-store=redis 且未注入实现 → 必须失败（不静默降级为 memory）
	if _, err := New(cfg, WithEnv(EnvTest), WithLogger(func(string, ...any) {})); err == nil {
		t.Fatal("replay-store=redis 没有 Redis 客户端时必须启动失败")
	}
}
