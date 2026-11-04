package guanjin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestMemoryReplayGuardWindow(t *testing.T) {
	g := NewMemoryReplayGuard()
	now := time.Now()
	g.now = func() time.Time { return now }
	ctx := context.Background()

	if ok, err := g.FirstUse(ctx, "j1", now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("首次使用应通过：ok=%v err=%v", ok, err)
	}
	if ok, _ := g.FirstUse(ctx, "j1", now.Add(time.Minute)); ok {
		t.Fatal("窗口内重复使用必须拒（GJ-CAP-07）")
	}
	now = now.Add(2 * time.Minute)
	if ok, _ := g.FirstUse(ctx, "j1", now.Add(time.Minute)); !ok {
		t.Fatal("窗口过后应可再次使用（并顺带清理过期键）")
	}
	if len(g.seen) != 1 {
		t.Fatalf("过期键应被清理，实得 %d 条", len(g.seen))
	}
}

type fakeSetNX struct {
	ok   bool
	err  error
	last string
}

func (f *fakeSetNX) SetNX(_ context.Context, key string, _ any, _ time.Duration) (bool, error) {
	f.last = key
	return f.ok, f.err
}

func TestRedisReplayGuard(t *testing.T) {
	f := &fakeSetNX{ok: true}
	g := &RedisReplayGuard{rdb: f, window: 60 * time.Second}
	ok, err := g.FirstUse(context.Background(), "j2", time.Now().Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("应通过：ok=%v err=%v", ok, err)
	}
	if f.last != "guanjin:jti:j2" {
		t.Fatalf("Redis 键名不对：%s", f.last)
	}

	f.err = errors.New("redis down")
	if _, err := g.FirstUse(context.Background(), "j3", time.Time{}); err == nil {
		t.Fatal("存储报错必须上抛（fail-closed）")
	} else {
		var ge *Error
		if !errors.As(err, &ge) || ge.Code != CodeRedisUnavailable {
			t.Fatalf("应为 REDIS_UNAVAILABLE，实得 %v", err)
		}
	}
}

func TestNewReplayGuardByConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Authz.ReplayStore = ReplayStoreMemory
	if g, err := NewReplayGuard(cfg, nil); err != nil || g == nil {
		t.Fatalf("memory 应可构造：%v", err)
	}
	cfg.Authz.ReplayStore = ReplayStoreRedis
	if _, err := NewReplayGuard(cfg, nil); err == nil {
		t.Fatal("redis 无客户端必须报错（不降级）")
	}
	if _, err := NewReplayGuard(cfg, &fakeSetNX{ok: true}); err != nil {
		t.Fatalf("redis 有客户端应可构造：%v", err)
	}
	cfg.Authz.ReplayStore = "etcd"
	if _, err := NewReplayGuard(cfg, nil); err == nil {
		t.Fatal("未知 replay-store 必须报错")
	}
}

func TestPathMatcher(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PublicPaths = []string{"/api/public/*", "/orders/*/callback", "/exact"}
	m := NewPathMatcher(cfg)

	public := []string{"/healthz", "/livez", "/readyz", "/error", "/favicon.ico", "/robots.txt",
		"/api/public/a", "/api/public/a/b/c", "/exact"}
	for _, p := range public {
		if !m.IsPublic(p) {
			t.Fatalf("%s 应公开", p)
		}
	}
	private := []string{"/orders", "/orders/1", "/api/private", "/admin/users", "/exact/x"}
	for _, p := range private {
		if m.IsPublic(p) {
			t.Fatalf("%s 不应公开（deny-by-default，GJ-CAP-19）", p)
		}
	}
	if len(m.Patterns()) == 0 {
		t.Fatal("启动自检要打印放行清单（GJ-CAP-22）")
	}

	cfg.PublicPathsIncludeBuiltin = false
	m2 := NewPathMatcher(cfg)
	if m2.IsPublic("/healthz") {
		t.Fatal("关掉内置白名单后 /healthz 不再公开")
	}
}

func TestLoadFromEnvStrictWhitelist(t *testing.T) {
	t.Setenv("GUANJIN_CLIENT_ID", "svc")
	t.Setenv("GUANJIN_PLATFORM_BASE_URL", "https://gj.example.com")
	t.Setenv("GUANJIN_GATEWAY_JWKS_CACHE_TTL", "5m")
	t.Setenv("GUANJIN_ENV", "test")
	t.Setenv("GUANJIN_TEST_DSN", "postgres://ignored")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("装载失败：%v", err)
	}
	if cfg.Client.ID != "svc" || cfg.Platform.BaseURL != "https://gj.example.com" {
		t.Fatalf("配置未生效：%+v", cfg)
	}
	if cfg.Gateway.JwksCacheTTL != 5*time.Minute {
		t.Fatalf("duration 解析不对：%v", cfg.Gateway.JwksCacheTTL)
	}
	if EnvFromEnv() != EnvTest {
		t.Fatalf("GUANJIN_ENV=test 应解析为 test，实得 %s", EnvFromEnv())
	}

	t.Setenv("GUANJIN_NOPE", "1")
	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("未知 GUANJIN_* 变量必须失败")
	}
}

func TestEnvFromEnvDefaultsToProd(t *testing.T) {
	t.Setenv("GUANJIN_ENV", "")
	if EnvFromEnv() != EnvProd {
		t.Fatal("未设置 GUANJIN_ENV 时应按 prod 处理（fail-safe）")
	}
	if (EnvProd).EnvIsLocalOrTest() {
		t.Fatal("prod 不属于 local|test")
	}
	_ = os.Unsetenv("GUANJIN_ENV")
}

func TestJwksFailureKeepsCache(t *testing.T) {
	priv := genKey(t)
	body := jwksBody("k1", &priv.PublicKey)
	failing := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	cfg := testConfig(t, srv.URL)
	cfg.Gateway.JwksCacheTTL = time.Nanosecond // 立刻过期，逼它走"刷新失败"分支
	cfg.Gateway.JwksUnknownKidForceRefresh = true
	j := NewJwksClient(cfg, nil)
	if err := j.Refresh(); err != nil {
		t.Fatalf("首次拉取应成功：%v", err)
	}
	failing = true
	time.Sleep(time.Millisecond)
	pub, err := j.PublicKey("k1")
	if err != nil || pub == nil {
		t.Fatalf("刷新失败但有缓存 → 应沿用缓存（非降级）: %v", err)
	}
}

func TestCapabilityErrorCarriesID(t *testing.T) {
	err := ErrCapabilityNotImplemented("GJ-CAP-13")
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("应可 errors.Is 到 ErrNotImplemented，实得 %v", err)
	}
	var ge *Error
	if !errors.As(err, &ge) || ge.Status != http.StatusForbidden {
		t.Fatalf("应为 403，实得 %v", err)
	}
	if err := ErrCapabilityNotImplemented("GJ-CAP-01"); err != nil {
		t.Fatalf("已实现的能力应返回 nil，实得 %v", err)
	}
}

func TestAudAndJwksURIDefaults(t *testing.T) {
	c := DefaultConfig()
	c.Platform.BaseURL = "https://gj.example.com"
	c.Client.ID = "svc"
	if c.Aud() != "svc" {
		t.Fatalf("aud 默认应跟随 client.id，实得 %s", c.Aud())
	}
	c.Gateway.Aud = "explicit"
	if c.Aud() != "explicit" {
		t.Fatal("显式 aud 应生效")
	}
	if c.JwksURI() != "https://gj.example.com/.well-known/jwks.json" {
		t.Fatalf("jwks-uri 默认派生不对：%s", c.JwksURI())
	}
}

func TestSplitListParsing(t *testing.T) {
	got := splitList(`["/a", "/b", '/c']`)
	if len(got) != 3 || got[0] != "/a" || got[2] != "/c" {
		t.Fatalf("列表解析不对：%v", got)
	}
	if splitList("") != nil {
		t.Fatal("空串应得 nil")
	}
}
