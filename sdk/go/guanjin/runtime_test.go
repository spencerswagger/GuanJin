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
	private := []string{"/orders", "/orders/1", "/api/private", "/admin/users", "/exact/x",
		// 路径穿越必须被判私有（评审 P2：`..` 折掉后不再命中公开前缀，deny-by-default 不被绕过）
		"/api/public/../admin/secret", "/healthz/../admin/secret", "/api/public/./a/../../admin",
		// `*` 在中间的模式永不命中（只支持结尾形态）——声明错 = 不放行，fail-closed 而不是悄悄放行
		"/orders/1/callback",
	}
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
		// 下面三条专门证明「非配置命名空间是逐条封闭枚举，不是前缀/后缀通配」（Spec 裁决）：
		{"GUANJIN_TEST_FOO=1", "guanjin.test.foo"},               // TEST_ 前缀不再整体放行
		{"GUANJIN_ALLOW_TEST_CLEANUP_2=1", "guanjin.allow.test"}, // 保留名加尾巴不算保留名
		{"GUANJIN_GATEWAY_ROUTES_X=a", "guanjin.gateway.routes"}, // 同上，后缀不构成例外
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

// reservedEnvGolden 是**非配置命名空间的 golden 清单**：与 config.go 的 reservedEnv、
// 契约 §3.6 附录必须逐条一致。它同时是「不许变成通配」的守卫——多一条少一条都会红。
var reservedEnvGolden = []string{
	// A. 平台启动配置（基线《技术栈与工程约定》§7.2 冻结）21 条
	"GUANJIN_ENV", "GUANJIN_LOG_LEVEL", "GUANJIN_LOG_FORMAT", "GUANJIN_HTTP_ADDR",
	"GUANJIN_GATEWAY_ADDR", "GUANJIN_PUBLIC_BASE_URL", "GUANJIN_ISSUER", "GUANJIN_DATABASE_URL",
	"GUANJIN_DB_MAX_OPEN_CONNS", "GUANJIN_DB_MAX_IDLE_CONNS", "GUANJIN_DB_CONN_MAX_LIFETIME",
	"GUANJIN_REDIS_URL", "GUANJIN_REDIS_TIMEOUT", "GUANJIN_MASTER_KEY", "GUANJIN_MASTER_KEY_FILE",
	"GUANJIN_BOOTSTRAP_ADMIN_USERNAME", "GUANJIN_BOOTSTRAP_ADMIN_PASSWORD",
	"GUANJIN_GATEWAY_UPSTREAM_TIMEOUT", "GUANJIN_GATEWAY_DIAL_TIMEOUT",
	"GUANJIN_METRICS_ADDR", "GUANJIN_OTEL_ENDPOINT",
	// B. 平台实现自用 7 条（数据面时钟偏移的现名，见 config.go 注释）
	"GUANJIN_DB_DSN", "GUANJIN_REDIS_ADDR", "GUANJIN_REDIS_PASSWORD",
	"GUANJIN_TRACE_HEADER", "GUANJIN_GATEWAY_ROUTES", "GUANJIN_SLO_NOTIFY_PATH",
	// `GUANJIN_GW_CLOCK_SKEW` 是 #121 后的现名。**#121 旧名
	// `GUANJIN_GATEWAY_CLOCK_SKEW` 刻意不收**——它是配置键 #10 的机械映射名，收进保留名
	// 会把该键的 env 来源静默吃掉（#51 评审 P1）。
	"GUANJIN_GW_CLOCK_SKEW",
	// C. 构建/本地/CI 夹具 8 条（后 6 条是过渡条目，随 GJTEST_* 改名 PR 删除）
	"GUANJIN_DEV_DSN", "GUANJIN_STAGING_DSN", "GUANJIN_TEST_DSN", "GUANJIN_TEST_REDIS_ADDR",
	"GUANJIN_TEST_REDIS_PASSWORD", "GUANJIN_TEST_DURATION",
	"GUANJIN_ALLOW_TEST_CLEANUP", "GUANJIN_AUDIT_NO_ISOLATION",
}

// TestReservedEnvNamespaceIsClosed 锁住「保留命名空间 = 逐条封闭枚举」：
// ① 代码里的 reservedEnv 必须与 golden 清单**完全相等**（多/少/改名都红，防止悄悄长成通配）；
// ② 清单内每一条都能与合法配置共存装载（这是 PR #51 假红的回归点）；
// ③ 清单外任何 `GUANJIN_*` 映射不到 29 项 → 必须失败（含"看起来像夹具"的名字）。
func TestReservedEnvNamespaceIsClosed(t *testing.T) {
	golden := make(map[string]struct{}, len(reservedEnvGolden))
	for _, n := range reservedEnvGolden {
		golden[n] = struct{}{}
	}
	if len(golden) != len(reservedEnvGolden) {
		t.Fatalf("golden 清单自身有重复项：%d 条 vs %d 个唯一名", len(reservedEnvGolden), len(golden))
	}
	if len(reservedEnv) != len(golden) {
		t.Fatalf("reservedEnv 有 %d 条，golden 清单有 %d 条：两侧必须逐条一致",
			len(reservedEnv), len(golden))
	}
	for n := range reservedEnv {
		if _, ok := golden[n]; !ok {
			t.Fatalf("reservedEnv 里多出 golden 清单没有的条目：%s（新增保留名必须同步 §3.6 与本清单）", n)
		}
	}
	for n := range golden {
		if !isReservedEnv(n) {
			t.Fatalf("golden 清单里的 %s 不在 reservedEnv：漏一个就是上线即挂", n)
		}
	}

	// ② 全部保留名共存时，合法配置仍然装载成功（= PR #51 的假红不再出现）
	env := baseEnviron()
	for _, n := range reservedEnvGolden {
		env = append(env, n+"=dummy")
	}
	cfg, err := LoadFromEnviron(env)
	if err != nil {
		t.Fatalf("保留命名空间被当成配置键：%v（PR #51 假红）", err)
	}
	if cfg.Client.ID != "svc" {
		t.Fatalf("保留变量挤掉了配置键：%+v", cfg)
	}

	// ③ 同一批 environ 里混一个清单外的名字，仍然必须失败（封闭枚举不是后门）
	for _, bad := range []string{"GUANJIN_PLATFORM_BASE_URLL=https://x", "GUANJIN_TEST_FOO=1"} {
		if _, err := LoadFromEnviron(append(env, bad)); err == nil {
			t.Fatalf("%s 必须装载失败（保留清单不得退化成通配）", bad)
		}
	}
}

// TestReservedEnvNeverShadowsConfigKeyMappings：保留清单 ∩ 29 项配置键的机械映射名 = ∅。
//
// 否则 `LoadFromEnviron` 先判保留名、把该键的**唯一 env 来源**静默跳过（设了不生效、
// 还 err=nil），「来源二、映射一」对该键不成立——#51 评审 P1 的真实缺陷：
// `GUANJIN_GATEWAY_CLOCK_SKEW`（配置键 #10 的映射名）曾被误列进保留名。
func TestReservedEnvNeverShadowsConfigKeyMappings(t *testing.T) {
	if len(ConfigKeys) != 29 {
		t.Fatalf("ConfigKeys 应为 29 项，实得 %d", len(ConfigKeys))
	}
	for _, k := range ConfigKeys {
		if name := EnvName(k); isReservedEnv(name) {
			t.Fatalf("配置键 %q 的机械映射名 %s 同时出现在保留清单：该键的 env 来源会被静默跳过", k, name)
		}
	}

	// 功能面证据（不只靠集合断言）：clock-skew（#10）的 env 来源必须真的生效。
	c, err := LoadFromEnviron([]string{"GUANJIN_GATEWAY_CLOCK_SKEW=90s"})
	if err != nil {
		t.Fatalf("clock-skew 的 env 装载不应报错：%v", err)
	}
	if c.Gateway.ClockSkew != 90*time.Second {
		t.Fatalf("GUANJIN_GATEWAY_CLOCK_SKEW 必须作为配置键 #10 生效（期望 90s，实得 %v）", c.Gateway.ClockSkew)
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
		if !strings.HasPrefix(name, "GUANJIN_") || isReservedEnv(name) {
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
