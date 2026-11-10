package guanjin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// baseEnviron 是受控的「合法 environ」：故意不读 os.Environ()，这样「白名单放过什么」
// 不取决于跑用例那台机器/那个 CI job 导出了什么（PR #51 的红正是这么来的）。
func baseEnviron() []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin",
		"GUANJIN_ENV=test",
		"GUANJIN_CLIENT_ID=svc",
		"GUANJIN_PLATFORM_BASE_URL=https://gj.example.com",
		"GUANJIN_GATEWAY_JWKS_CACHE_TTL=5m",
	}
}

func TestLoadFromEnvironStrictWhitelist(t *testing.T) {
	cfg, err := LoadFromEnviron(baseEnviron())
	if err != nil {
		t.Fatalf("装载失败：%v", err)
	}
	if cfg.Client.ID != "svc" || cfg.Platform.BaseURL != "https://gj.example.com" {
		t.Fatalf("配置未生效：%+v", cfg)
	}
	if cfg.Gateway.JwksCacheTTL != 5*time.Minute {
		t.Fatalf("duration 解析不对：%v", cfg.Gateway.JwksCacheTTL)
	}

	// 未知键 / 拼错键 / 已废止键一律**装载失败且报键名**（AC-FC-05、AC-SDK-01）。
	// 注意第 3 条：旧实现用 `HasSuffix("_DSN")` 兜底，会把这种拼错当 "DSN" 静默吞掉；
	// 现在排除面是封闭名单，它必须失败。
	cases := []struct {
		env  string
		want string
	}{
		{"GUANJIN_NOPE=1", "guanjin.nope"},
		{"GUANJIN_GATEWAY_JWKS_URL=https://x/jwks.json", "guanjin.gateway.jwks.url"},
		{"GUANJIN_GATEWAY_JWKS_CACHE_TTL_DSN=1m", "guanjin.gateway.jwks.cache.ttl.dsn"},
		{"GUANJIN_MODE=direct", "guanjin.mode"},
	}
	for _, c := range cases {
		_, err := LoadFromEnviron(append(baseEnviron(), c.env))
		if err == nil {
			t.Fatalf("%s 必须装载失败", c.env)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s 的报错必须含键名 %q，实得：%v", c.env, c.want, err)
		}
	}
}

// TestLoadFromEnvironToleratesHarnessNamespace 是 PR #51 的回归用例：
// `.github/workflows/ci.yml` 的 integration job 导出 `GUANJIN_TEST_DSN` /
// `GUANJIN_TEST_REDIS_ADDR`（真库/真 Redis 用例要用），它们**不是** 29 项配置键；
// 曾被当成未知键 → SDK 装载失败 → `go test ./...` 在这个 job 里必红（假红）。
func TestLoadFromEnvironToleratesHarnessNamespace(t *testing.T) {
	env := append(baseEnviron(),
		"GUANJIN_TEST_DSN=postgres://ignored",
		"GUANJIN_TEST_REDIS_ADDR=localhost:6379",
		"GUANJIN_TEST_REDIS_PASSWORD=",
		"GUANJIN_DB_DSN=postgres://ignored",
		"GUANJIN_DEV_DSN=postgres://ignored",
		"GUANJIN_STAGING_DSN=postgres://ignored", // Makefile 提示开发者一并导出（收紧前的 _DSN 后缀会静默放过，现须在例外名单里）
	)
	cfg, err := LoadFromEnviron(env)
	if err != nil {
		t.Fatalf("非配置命名空间被当成配置键：%v（PR #51 假红）", err)
	}
	if cfg.Client.ID != "svc" {
		t.Fatalf("夹具变量挤掉了配置键：%+v", cfg)
	}

	// 排除面不是白名单漏洞：同一批 environ 里混一个拼错的配置键，仍然必须失败。
	if _, err := LoadFromEnviron(append(env, "GUANJIN_PLATFORM_BASE_URLL=https://x")); err == nil {
		t.Fatal("混入拼错的配置键必须装载失败（排除面不得成为后门）")
	}
}

// TestLoadFromEnvReadsProcessEnviron 验「真读进程环境」这条链路本身：
// 先把当前进程里参与白名单的 GUANJIN_* 清干净（结束还原），再按 CI integration job 的
// 现场导出一组变量，证明 LoadFromEnv() 在夹具变量共存时能正常装载。
func TestLoadFromEnvReadsProcessEnviron(t *testing.T) {
	scrubGuanjinEnv(t)
	t.Setenv("GUANJIN_ENV", "test")
	t.Setenv("GUANJIN_CLIENT_ID", "svc")
	t.Setenv("GUANJIN_PLATFORM_BASE_URL", "https://gj.example.com")
	t.Setenv("GUANJIN_TEST_REDIS_ADDR", "localhost:6379") // integration job 现场

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("装载失败：%v（PR #51 假红）", err)
	}
	if cfg.Client.ID != "svc" {
		t.Fatalf("进程环境未生效：%+v", cfg)
	}
	if EnvFromEnv() != EnvTest {
		t.Fatalf("GUANJIN_ENV=test 应解析为 test，实得 %s", EnvFromEnv())
	}
}

// scrubGuanjinEnv 把当前进程里**会参与白名单**的 GUANJIN_* 临时清掉，用例结束还原。
// 目的：任何人机器上跑都是同一结果，不因本地恰好 `export GUANJIN_FOO` 而红。
func scrubGuanjinEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		name, val := kv[:i], kv[i+1:]
		if !strings.HasPrefix(name, "GUANJIN_") || isNonConfigEnv(name) {
			continue
		}
		n, v := name, val
		if err := os.Unsetenv(n); err != nil {
			t.Fatalf("清理 %s 失败：%v", n, err)
		}
		t.Cleanup(func() { _ = os.Setenv(n, v) })
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
