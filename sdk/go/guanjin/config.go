package guanjin

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 配置契约（docs/sdk-contract.md §3.2）：29 项，逐字符照抄。**多一个键即失败**
// （AC-FC-05）；三个 direct 形态键 `mode` / `direct.user-token-jwks-uri` /
// `direct.user-token-issuer` 属 `REMOVED`（契约 §3.3），出现即失败（AC-SDK-01）。
const (
	KeyEnabled                      = "guanjin.enabled"
	KeyPlatformBaseURL              = "guanjin.platform.base-url"
	KeyPlatformConnectTimeout       = "guanjin.platform.connect-timeout"
	KeyPlatformReadTimeout          = "guanjin.platform.read-timeout"
	KeyClientID                     = "guanjin.client.id"
	KeyClientHMACSecret             = "guanjin.client.hmac-secret"
	KeyGatewayAssertionHeader       = "guanjin.gateway.assertion-header"
	KeyGatewayIssuer                = "guanjin.gateway.issuer"
	KeyGatewayAud                   = "guanjin.gateway.aud"
	KeyGatewayClockSkew             = "guanjin.gateway.clock-skew"
	KeyGatewayReplayWindow          = "guanjin.gateway.replay-window"
	KeyGatewayJwksURI               = "guanjin.gateway.jwks-uri"
	KeyGatewayJwksCacheTTL          = "guanjin.gateway.jwks-cache-ttl"
	KeyGatewayJwksUnknownKidRefresh = "guanjin.gateway.jwks-unknown-kid-force-refresh"
	KeyAuthzEnabled                 = "guanjin.authz.enabled"
	KeyAuthzDefaultRole             = "guanjin.authz.default-role"
	KeyAuthzModelConf               = "guanjin.authz.model-conf"
	KeyAuthzReplayStore             = "guanjin.authz.replay-store"
	KeyAgentReportEnabled           = "guanjin.agent.report.enabled"
	KeyAgentReportOnStartup         = "guanjin.agent.report.on-startup"
	KeyAgentReportOnChange          = "guanjin.agent.report.on-change"
	KeyAgentPullEnabled             = "guanjin.agent.pull.enabled"
	KeyAgentPullInterval            = "guanjin.agent.pull.interval"
	KeyPublicPaths                  = "guanjin.public-paths"
	KeyPublicPathsIncludeBuiltin    = "guanjin.public-paths-include-builtin"
	KeySecurityInjectContext        = "guanjin.security.inject-security-context"
	KeySecurityAuthoritiesResolver  = "guanjin.security.authorities-resolver"
	KeySecurityRolePrefix           = "guanjin.security.role-prefix"
	KeyStartupPrintPublicPaths      = "guanjin.startup.print-public-paths"
)

// ConfigKeys 是 29 项配置键的**规范顺序**（契约 §3.2 编号 1..29）。
var ConfigKeys = []string{
	KeyEnabled,
	KeyPlatformBaseURL,
	KeyPlatformConnectTimeout,
	KeyPlatformReadTimeout,
	KeyClientID,
	KeyClientHMACSecret,
	KeyGatewayAssertionHeader,
	KeyGatewayIssuer,
	KeyGatewayAud,
	KeyGatewayClockSkew,
	KeyGatewayReplayWindow,
	KeyGatewayJwksURI,
	KeyGatewayJwksCacheTTL,
	KeyGatewayJwksUnknownKidRefresh,
	KeyAuthzEnabled,
	KeyAuthzDefaultRole,
	KeyAuthzModelConf,
	KeyAuthzReplayStore,
	KeyAgentReportEnabled,
	KeyAgentReportOnStartup,
	KeyAgentReportOnChange,
	KeyAgentPullEnabled,
	KeyAgentPullInterval,
	KeyPublicPaths,
	KeyPublicPathsIncludeBuiltin,
	KeySecurityInjectContext,
	KeySecurityAuthoritiesResolver,
	KeySecurityRolePrefix,
	KeyStartupPrintPublicPaths,
}

// RemovedKeys 是契约 §3.3 废止的三个键（direct 形态）：**出现即启动失败**（AC-SDK-01）。
var RemovedKeys = []string{
	"guanjin.mode",
	"guanjin.direct.user-token-jwks-uri",
	"guanjin.direct.user-token-issuer",
}

// JwksPath 是契约 §3.2 #12 冻结的 JWKS 路径（旧值 `/api/v1/jwks` 全库不存在）。
const JwksPath = "/.well-known/jwks.json"

// 默认值（契约 §3.2；改这里等于改契约，必须同步 docs/sdk-contract.md）。
const (
	DefaultAssertionHeader = "X-GuanJin-Assertion"
	DefaultIssuer          = "guanjin-gateway"
	DefaultClockSkew       = 60 * time.Second
	DefaultReplayWindow    = 60 * time.Second
	DefaultJwksCacheTTL    = 10 * time.Minute
	DefaultConnectTimeout  = 2 * time.Second
	DefaultReadTimeout     = 3 * time.Second
	DefaultDefaultRole     = "user"
	DefaultModelConf       = "model.conf"
	DefaultPullInterval    = 30 * time.Second
	DefaultRolePrefix      = "ROLE_"

	// ReplayStoreMemory 只在 local|test 允许（契约 #18；多副本必须 redis）。
	ReplayStoreMemory = "memory"
	// ReplayStoreRedis 是默认值（契约 #18 ✅C-4）。
	ReplayStoreRedis = "redis"
)

// ErrConfig 是配置错误族：**一律启动失败**，且必须报出键名（契约 §3.6）。
var ErrConfig = errors.New("guanjin: config")

// Config 是 29 项配置的 Go 结构体形态（字段名 = 契约 §3.2 的 Go 列）。
type Config struct {
	Enabled bool

	Platform struct {
		BaseURL        string
		ConnectTimeout time.Duration
		ReadTimeout    time.Duration
	}
	Client struct {
		ID         string
		HMACSecret string
	}
	Gateway struct {
		AssertionHeader            string
		Issuer                     string
		Aud                        string
		ClockSkew                  time.Duration
		ReplayWindow               time.Duration
		JwksURI                    string
		JwksCacheTTL               time.Duration
		JwksUnknownKidForceRefresh bool
	}
	Authz struct {
		Enabled     bool
		DefaultRole string
		ModelConf   string
		ReplayStore string
	}
	Agent struct {
		Report struct {
			Enabled   bool
			OnStartup bool
			OnChange  bool
		}
		Pull struct {
			Enabled  bool
			Interval time.Duration
		}
	}
	PublicPaths               []string
	PublicPathsIncludeBuiltin bool
	Security                  struct {
		InjectContext       bool
		AuthoritiesResolver string
		RolePrefix          string
	}
	Startup struct {
		PrintPublicPaths bool
	}
}

// DefaultConfig 返回契约 §3.2 的默认值（必填项为空，Validate 会挡住）。
func DefaultConfig() Config {
	var c Config
	c.Enabled = true
	c.Platform.ConnectTimeout = DefaultConnectTimeout
	c.Platform.ReadTimeout = DefaultReadTimeout
	c.Gateway.AssertionHeader = DefaultAssertionHeader
	c.Gateway.Issuer = DefaultIssuer
	c.Gateway.ClockSkew = DefaultClockSkew
	c.Gateway.ReplayWindow = DefaultReplayWindow
	c.Gateway.JwksCacheTTL = DefaultJwksCacheTTL
	c.Gateway.JwksUnknownKidForceRefresh = true
	c.Authz.Enabled = true
	c.Authz.DefaultRole = DefaultDefaultRole
	c.Authz.ModelConf = DefaultModelConf
	c.Authz.ReplayStore = ReplayStoreRedis // ✅C-4：默认 redis，memory 仅 local|test
	c.Agent.Report.Enabled = true
	c.Agent.Report.OnStartup = true
	c.Agent.Report.OnChange = true
	c.Agent.Pull.Enabled = false // ✅C-5：默认 false（方案 A 零回源）
	c.Agent.Pull.Interval = DefaultPullInterval
	c.PublicPathsIncludeBuiltin = true
	c.Security.InjectContext = true
	c.Security.RolePrefix = DefaultRolePrefix
	c.Startup.PrintPublicPaths = true
	return c
}

// Env 是**运行时环境标识**（不是 29 项配置键之一，等价于 Java 侧的
// `spring.profiles.active`）：`local` / `test` 之外的一切都按"生产口径"校验。
// 取 `GUANJIN_ENV`，缺省视作 `prod`（宁可判严）。
type Env string

// 环境取值。
const (
	EnvLocal Env = "local"
	EnvTest  Env = "test"
	EnvProd  Env = "prod"
)

// EnvIsLocalOrTest 判断是否处在允许 "关掉" 的环境（契约 C-1：仅 local|test）。
func (e Env) EnvIsLocalOrTest() bool { return e == EnvLocal || e == EnvTest }

// EnvFromEnv 读取 GUANJIN_ENV；空值按 prod 处理（fail-safe）。
func EnvFromEnv() Env {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GUANJIN_ENV"))) {
	case "local":
		return EnvLocal
	case "test":
		return EnvTest
	default:
		return EnvProd
	}
}

// reservedEnv 是**非配置命名空间**：宿主/平台自用的 `GUANJIN_*` 变量。它们不是 29 项配置键
// （契约 §3.2），SDK 不读、不校验、不参与默认值 —— 因此不进入严格白名单判定。
//
// **封闭枚举，禁止通配**（Spec 裁决）：保留命名空间 + 逐条封闭枚举；不得用前缀/后缀式例外，
// 因为开放式例外会与「29 项冻结 / 未知键必须失败」正面冲突。清单外的 `GUANJIN_*` 映射不到
// 29 项 → 照样启动失败并报键名（AC-FC-05）。
//
// 为什么需要它：`GUANJIN_` 前缀被两类东西共用——本 SDK 的 29 项配置键，与平台自己的启动配置
// （基线《技术栈与工程约定》§7.2 冻结 21 条）以及构建/测试夹具（Makefile、CI integration job
// 的 `GUANJIN_TEST_DSN` / `GUANJIN_TEST_REDIS_ADDR`）。不排除它们，SDK 在平台/测试进程里被加载
// 时会把宿主变量当未知配置键 → 启动失败（PR #51 integration job 的红即此）。
//
// 清单与 docs/sdk-contract.md §3.6 附录（37 条）逐条对应，**两侧必须同改**；新增/删除都会让
// TestReservedEnvNamespaceIsClosed（golden 列表）失败，防止清单悄悄长成"通配"。
// C 组后 6 条是**过渡条目**：测试夹具改名 GJTEST_* 后（单独 PR）即从本表删除。
var reservedEnv = map[string]struct{}{
	// A. 平台启动配置（基线《技术栈与工程约定》§7.2 冻结；SDK 不读）—— 21 条
	"GUANJIN_ENV":                      {},
	"GUANJIN_LOG_LEVEL":                {},
	"GUANJIN_LOG_FORMAT":               {},
	"GUANJIN_HTTP_ADDR":                {},
	"GUANJIN_GATEWAY_ADDR":             {},
	"GUANJIN_PUBLIC_BASE_URL":          {},
	"GUANJIN_ISSUER":                   {},
	"GUANJIN_DATABASE_URL":             {},
	"GUANJIN_DB_MAX_OPEN_CONNS":        {},
	"GUANJIN_DB_MAX_IDLE_CONNS":        {},
	"GUANJIN_DB_CONN_MAX_LIFETIME":     {},
	"GUANJIN_REDIS_URL":                {},
	"GUANJIN_REDIS_TIMEOUT":            {},
	"GUANJIN_MASTER_KEY":               {},
	"GUANJIN_MASTER_KEY_FILE":          {},
	"GUANJIN_BOOTSTRAP_ADMIN_USERNAME": {},
	"GUANJIN_BOOTSTRAP_ADMIN_PASSWORD": {},
	"GUANJIN_GATEWAY_UPSTREAM_TIMEOUT": {},
	"GUANJIN_GATEWAY_DIAL_TIMEOUT":     {},
	"GUANJIN_METRICS_ADDR":             {},
	"GUANJIN_OTEL_ENDPOINT":            {},
	// B. 平台实现自用（internal/config、cmd/guanjind、cmd/guanjingw、internal/gateway）—— 8 条
	"GUANJIN_DB_DSN":          {},
	"GUANJIN_REDIS_ADDR":      {},
	"GUANJIN_REDIS_PASSWORD":  {},
	"GUANJIN_TRACE_HEADER":    {},
	"GUANJIN_GATEWAY_ROUTES":  {},
	"GUANJIN_SLO_NOTIFY_PATH": {},
	// `GUANJIN_GW_CLOCK_SKEW` = 数据面时钟容忍偏移的**现名**（PR #121 改的名，原名
	// `GUANJIN_GATEWAY_CLOCK_SKEW`；改名理由：旧名正是 SDK 契约键 `guanjin.gateway.clock-skew`
	// 的机械映射名，同名两个所有者）。旧名一并保留：滚动升级期旧配置项还在进程环境里，
	// 漏掉它等于让 SDK 在升级窗口启动失败。
	"GUANJIN_GW_CLOCK_SKEW":      {},
	"GUANJIN_GATEWAY_CLOCK_SKEW": {},
	// C. 构建/本地/CI 夹具（Makefile、.github/workflows/ci.yml、测试护栏）—— 8 条，后 6 条为过渡条目
	"GUANJIN_DEV_DSN":             {},
	"GUANJIN_STAGING_DSN":         {},
	"GUANJIN_TEST_DSN":            {},
	"GUANJIN_TEST_REDIS_ADDR":     {},
	"GUANJIN_TEST_REDIS_PASSWORD": {},
	"GUANJIN_TEST_DURATION":       {},
	"GUANJIN_ALLOW_TEST_CLEANUP":  {},
	"GUANJIN_AUDIT_NO_ISOLATION":  {},
}

// isReservedEnv 判断某个环境变量名是否属于非配置命名空间（**精确匹配**，无通配/无前缀）。
func isReservedEnv(name string) bool {
	_, ok := reservedEnv[name]
	return ok
}

// LoadFromEnv 从**当前进程环境**装载配置（= LoadFromEnviron(os.Environ())）。
func LoadFromEnv() (Config, error) { return LoadFromEnviron(os.Environ()) }

// LoadFromEnviron 从给定的 `KEY=VALUE` 列表装载配置（严格白名单：未知键即失败）。
//
// 参数化（而不是直接读 os.Environ）是为了让「白名单必须拦什么、必须放过什么」可以
// **脱离宿主环境**被断言——否则用例的结果取决于跑它的那台机器/那个 CI job 恰好导出了
// 哪些 `GUANJIN_*`（这正是 PR #51 红的原因）。
func LoadFromEnviron(environ []string) (Config, error) {
	in := map[string]string{}
	for _, kv := range environ {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		name, val := kv[:i], kv[i+1:]
		if !strings.HasPrefix(name, "GUANJIN_") {
			continue
		}
		if isReservedEnv(name) {
			continue
		}
		in[envNameToKey(name)] = val
	}
	c := DefaultConfig()
	if err := c.applyMap(in); err != nil {
		return Config{}, err
	}
	return c, nil
}

// envNameToKey 把 `GUANJIN_GATEWAY_JWKS_URI` 还原成 `guanjin.gateway.jwks-uri`。
// 规则见契约 §1.1：去前缀、`.`→`_`、`-`→`_`、全大写；因此反解靠 29 项白名单逐项
// 比对（唯一可靠，避免 `-`/`.` 混淆）。
func envNameToKey(name string) string {
	for _, k := range ConfigKeys {
		if EnvName(k) == name {
			return k
		}
	}
	return strings.ToLower(strings.ReplaceAll(name, "_", "."))
}

// EnvName 返回某个配置键对应的环境变量名（契约 §1.1 机械转换）。
func EnvName(key string) string {
	s := strings.TrimPrefix(key, "guanjin.")
	s = strings.ReplaceAll(s, ".", "_")
	s = strings.ReplaceAll(s, "-", "_")
	return "GUANJIN_" + strings.ToUpper(s)
}

// LoadFromMap 从键值对装载配置（键用规范键名，如 `guanjin.client.id`）；供测试与
// 非 env 部署形态使用。未知键、已废止键都直接报错。
func LoadFromMap(in map[string]string) (Config, error) {
	c := DefaultConfig()
	if err := c.applyMap(in); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c *Config) applyMap(in map[string]string) error {
	allowed := make(map[string]bool, len(ConfigKeys))
	for _, k := range ConfigKeys {
		allowed[k] = true
	}
	removed := make(map[string]bool, len(RemovedKeys))
	for _, k := range RemovedKeys {
		removed[k] = true
	}
	var unknown []string
	for k := range in {
		if removed[k] {
			return fmt.Errorf("%w: 已废止键 %q（契约 §3.3，direct 形态已删除；AC-SDK-01）", ErrConfig, k)
		}
		if !allowed[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("%w: 未知键 %v（该键不属于 SDK 配置命名空间；只有 %d 项交付集，见 docs/sdk-contract.md §3.2；AC-FC-05）",
			ErrConfig, unknown, len(ConfigKeys))
	}

	for k, v := range in {
		if err := c.set(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) set(key, raw string) error {
	bad := func(want string, err error) error {
		if err != nil {
			return fmt.Errorf("%w: 键 %q 取不到 %s: %v", ErrConfig, key, want, err)
		}
		return fmt.Errorf("%w: 键 %q 取不到 %s", ErrConfig, key, want)
	}
	boolVal := func() (bool, error) { return strconv.ParseBool(strings.TrimSpace(raw)) }
	durVal := func() (time.Duration, error) { return time.ParseDuration(strings.TrimSpace(raw)) }

	switch key {
	case KeyEnabled:
		b, err := boolVal()
		if err != nil {
			return bad("bool", err)
		}
		c.Enabled = b
	case KeyPlatformBaseURL:
		c.Platform.BaseURL = strings.TrimSuffix(strings.TrimSpace(raw), "/")
	case KeyPlatformConnectTimeout:
		d, err := durVal()
		if err != nil {
			return bad("duration（如 2s）", err)
		}
		c.Platform.ConnectTimeout = d
	case KeyPlatformReadTimeout:
		d, err := durVal()
		if err != nil {
			return bad("duration（如 3s）", err)
		}
		c.Platform.ReadTimeout = d
	case KeyClientID:
		c.Client.ID = strings.TrimSpace(raw)
	case KeyClientHMACSecret:
		c.Client.HMACSecret = strings.TrimSpace(raw)
	case KeyGatewayAssertionHeader:
		c.Gateway.AssertionHeader = strings.TrimSpace(raw)
	case KeyGatewayIssuer:
		c.Gateway.Issuer = strings.TrimSpace(raw)
	case KeyGatewayAud:
		c.Gateway.Aud = strings.TrimSpace(raw)
	case KeyGatewayClockSkew:
		d, err := durVal()
		if err != nil {
			return bad("duration", err)
		}
		c.Gateway.ClockSkew = d
	case KeyGatewayReplayWindow:
		d, err := durVal()
		if err != nil {
			return bad("duration", err)
		}
		c.Gateway.ReplayWindow = d
	case KeyGatewayJwksURI:
		c.Gateway.JwksURI = strings.TrimSpace(raw)
	case KeyGatewayJwksCacheTTL:
		d, err := durVal()
		if err != nil {
			return bad("duration", err)
		}
		c.Gateway.JwksCacheTTL = d
	case KeyGatewayJwksUnknownKidRefresh:
		b, err := boolVal()
		if err != nil {
			return bad("bool", err)
		}
		c.Gateway.JwksUnknownKidForceRefresh = b
	case KeyAuthzEnabled:
		b, err := boolVal()
		if err != nil {
			return bad("bool", err)
		}
		c.Authz.Enabled = b
	case KeyAuthzDefaultRole:
		c.Authz.DefaultRole = strings.TrimSpace(raw)
	case KeyAuthzModelConf:
		c.Authz.ModelConf = strings.TrimSpace(raw)
	case KeyAuthzReplayStore:
		s := strings.ToLower(strings.TrimSpace(raw))
		if s != ReplayStoreMemory && s != ReplayStoreRedis {
			return fmt.Errorf("%w: 键 %q 只允许 memory|redis，得到 %q", ErrConfig, key, raw)
		}
		c.Authz.ReplayStore = s
	case KeyAgentReportEnabled:
		b, err := boolVal()
		if err != nil {
			return bad("bool", err)
		}
		c.Agent.Report.Enabled = b
	case KeyAgentReportOnStartup:
		b, err := boolVal()
		if err != nil {
			return bad("bool", err)
		}
		c.Agent.Report.OnStartup = b
	case KeyAgentReportOnChange:
		b, err := boolVal()
		if err != nil {
			return bad("bool", err)
		}
		c.Agent.Report.OnChange = b
	case KeyAgentPullEnabled:
		b, err := boolVal()
		if err != nil {
			return bad("bool", err)
		}
		c.Agent.Pull.Enabled = b
	case KeyAgentPullInterval:
		d, err := durVal()
		if err != nil {
			return bad("duration", err)
		}
		c.Agent.Pull.Interval = d
	case KeyPublicPaths:
		c.PublicPaths = splitList(raw)
	case KeyPublicPathsIncludeBuiltin:
		b, err := boolVal()
		if err != nil {
			return bad("bool", err)
		}
		c.PublicPathsIncludeBuiltin = b
	case KeySecurityInjectContext:
		b, err := boolVal()
		if err != nil {
			return bad("bool", err)
		}
		c.Security.InjectContext = b
	case KeySecurityAuthoritiesResolver:
		c.Security.AuthoritiesResolver = strings.TrimSpace(raw)
	case KeySecurityRolePrefix:
		c.Security.RolePrefix = raw
	case KeyStartupPrintPublicPaths:
		b, err := boolVal()
		if err != nil {
			return bad("bool", err)
		}
		c.Startup.PrintPublicPaths = b
	default:
		return fmt.Errorf("%w: 未知键 %q", ErrConfig, key)
	}
	return nil
}

func splitList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.Trim(strings.TrimSpace(p), `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Validate 做启动期校验（fail-fast，契约 §3.6 / §4.4）：
//   - 必填项：`platform.base-url`（且必须可解析、不带尾斜杠）、`client.id`、`client.hmac-secret`；
//   - `enabled=false` / `authz.enabled=false` **仅 local|test**，其余环境启动失败（C-1 / AC-SDK-02）；
//   - `replay-store=memory` 同样仅 local|test（C-4）；
//   - `jwks-uri` 只允许平台基址 + `/.well-known/jwks.json`（C-2 / AC-SDK-03 / AC-FC-04）；
//   - `replay-window` 必须 ≥ 令牌 exp（60s），否则漏检重放（契约 §3.2 #11）。
func (c Config) Validate(env Env) error {
	if strings.TrimSpace(c.Platform.BaseURL) == "" {
		return fmt.Errorf("%w: 必填键 %q 为空（平台地址必须来自配置/环境变量，禁硬编码；AC-SDK-04）",
			ErrConfig, KeyPlatformBaseURL)
	}
	u, err := url.Parse(c.Platform.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%w: 键 %q 非法（需要 scheme://host[:port]，无尾斜杠）：%q", ErrConfig, KeyPlatformBaseURL, c.Platform.BaseURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: 键 %q 不得带 query/fragment：%q", ErrConfig, KeyPlatformBaseURL, c.Platform.BaseURL)
	}
	if c.Client.ID == "" {
		return fmt.Errorf("%w: 必填键 %q 为空（同时作 aud 期望值与 Agent 面 HMAC 标识）", ErrConfig, KeyClientID)
	}
	if c.Client.HMACSecret == "" {
		return fmt.Errorf("%w: 必填键 %q 为空（环境变量注入，禁入仓库；每业务系统独立）", ErrConfig, KeyClientHMACSecret)
	}
	if !env.EnvIsLocalOrTest() {
		if !c.Enabled {
			return fmt.Errorf("%w: 键 %q 只允许在 local|test 置 false（C-1；非 local|test 置 false 即启动失败——不存在"+
				"生产关闭验签的开关，AC-SDK-02）", ErrConfig, KeyEnabled)
		}
		if !c.Authz.Enabled {
			return fmt.Errorf("%w: 键 %q 只允许在 local|test 置 false（C-1；AC-SDK-02）", ErrConfig, KeyAuthzEnabled)
		}
		if c.Authz.ReplayStore == ReplayStoreMemory {
			return fmt.Errorf("%w: 键 %q=memory 只允许在 local|test（多副本用 memory = jti 防重放失效；C-4 / AC-SESS-05）",
				ErrConfig, KeyAuthzReplayStore)
		}
	}
	if c.Gateway.ClockSkew <= 0 {
		return fmt.Errorf("%w: 键 %q 必须为正（默认 60s）", ErrConfig, KeyGatewayClockSkew)
	}
	if c.Gateway.ReplayWindow < DefaultReplayWindow {
		return fmt.Errorf("%w: 键 %q 必须 ≥ %s（令牌 exp；小于它会漏检重放）",
			ErrConfig, KeyGatewayReplayWindow, DefaultReplayWindow)
	}
	uri := c.JwksURI()
	ju, err := url.Parse(uri)
	if err != nil || ju.Scheme == "" || ju.Host == "" {
		return fmt.Errorf("%w: 键 %q 解析失败：%q", ErrConfig, KeyGatewayJwksURI, uri)
	}
	if ju.Path != JwksPath {
		return fmt.Errorf("%w: 键 %q 只允许 %q（C-2；旧值 /api/v1/jwks 全库不存在），得到 %q",
			ErrConfig, KeyGatewayJwksURI, JwksPath, ju.Path)
	}
	// 同源校验（`notes/sdk-jwks-uri-同源校验-2026-11-10.md`，与 Java `GuanjinConfigValidator.sameOrigin`
	// 同一形态）：`scheme + host + port` 三者一致（port 按 scheme 默认值归一），跨域 = 换信任源 =
	// **启动失败**。基线本已禁止跨域/第三方地址，这里只是把"已禁止"变成可执行（加固，不进 CH）；
	// **不存在任何开关**（`allow-cross-origin` 之类一律不许出现——那是生产后门）。
	if !sameOrigin(u, ju) {
		return fmt.Errorf("%w: 键 %q 必须与 %q 同源（跨域地址 = 换信任源，需 ADR）：%q vs %q",
			ErrConfig, KeyGatewayJwksURI, KeyPlatformBaseURL, uri, c.Platform.BaseURL)
	}
	return nil
}

// sameOrigin 比较 scheme + host + port（port 按 scheme 的默认值归一：http→80、https→443；
// host 大小写不敏感）。与 Java `sameOrigin(URI, URI)` 逐条同形（契约 §4.1 三语言同语义）。
func sameOrigin(a, b *url.URL) bool {
	return lower(a.Scheme) == lower(b.Scheme) &&
		lower(hostOf(a)) == lower(hostOf(b)) &&
		effectivePort(a) == effectivePort(b)
}

func lower(s string) string { return strings.ToLower(s) }

func hostOf(u *url.URL) string {
	if u.Hostname() == "" {
		return ""
	}
	return u.Hostname()
}

func effectivePort(u *url.URL) int {
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 {
			return -1 // 非法端口：调用方已在前面的解析校验里挡掉，这里兜底为"不等"
		}
		return n
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return 443
	default:
		return 80
	}
}

// JwksURI 返回生效的 JWKS 地址：未显式配置时按契约 #12 由 base-url 派生。
func (c Config) JwksURI() string {
	if c.Gateway.JwksURI != "" {
		return c.Gateway.JwksURI
	}
	return c.Platform.BaseURL + JwksPath
}

// Aud 返回生效的 aud 期望值：未配置时跟随 client.id（契约 #9）。
func (c Config) Aud() string {
	if c.Gateway.Aud != "" {
		return c.Gateway.Aud
	}
	return c.Client.ID
}
