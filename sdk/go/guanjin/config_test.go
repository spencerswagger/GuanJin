package guanjin

import (
	"errors"
	"strings"
	"testing"
)

func TestConfigKeySetIsFrozen29(t *testing.T) {
	if len(ConfigKeys) != ContractConfigCount {
		t.Fatalf("配置键必须是 %d 项（契约 §3.2），实得 %d", ContractConfigCount, len(ConfigKeys))
	}
	if len(RemovedKeys) != 3 {
		t.Fatalf("REMOVED 键必须是 3 项（契约 §3.3），实得 %d", len(RemovedKeys))
	}
	seen := map[string]bool{}
	for _, k := range ConfigKeys {
		if seen[k] {
			t.Fatalf("配置键重复: %q", k)
		}
		seen[k] = true
		if EnvName(k) == "" {
			t.Fatalf("配置键 %q 的环境变量名为空", k)
		}
	}
}

func TestConfigDefaultsPerContract(t *testing.T) {
	c := DefaultConfig()
	if !c.Enabled || !c.Authz.Enabled {
		t.Fatal("enabled / authz.enabled 默认必须为 true")
	}
	if c.Gateway.JwksCacheTTL != DefaultJwksCacheTTL {
		t.Fatalf("jwks-cache-ttl 默认 %v", c.Gateway.JwksCacheTTL)
	}
	if c.Gateway.JwksUnknownKidForceRefresh != true {
		t.Fatal("jwks-unknown-kid-force-refresh 默认 true")
	}
	if c.Authz.ReplayStore != ReplayStoreRedis {
		t.Fatalf("replay-store 默认必须 redis（C-4），实得 %q", c.Authz.ReplayStore)
	}
	if c.Agent.Pull.Enabled {
		t.Fatal("agent.pull.enabled 默认必须 false（C-5，零回源）")
	}
	if !c.PublicPathsIncludeBuiltin || !c.Startup.PrintPublicPaths {
		t.Fatal("public-paths-include-builtin / startup.print-public-paths 默认 true")
	}
	// 契约 #17：resource 形态，三语言逐字节一致（历史缺陷：Go 曾给 "model.conf"）。
	if c.Authz.ModelConf != "classpath:/guanjin/model.conf" || DefaultModelConf != "classpath:/guanjin/model.conf" {
		t.Fatalf("authz.model-conf 默认值必须等于契约 #17 的 %q，实得 %q",
			"classpath:/guanjin/model.conf", c.Authz.ModelConf)
	}
}

func TestConfigRejectsUnknownAndRemovedKeys(t *testing.T) {
	base := map[string]string{KeyClientID: "x"}
	base[KeyPlatformBaseURL] = "https://gj.example.com"
	for _, tc := range []struct {
		name string
		key  string
	}{
		{"未知键", "guanjin.foo"},
		{"废止键 mode", "guanjin.mode"},
		{"废止键 direct", "guanjin.direct.user-token-issuer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := map[string]string{}
			for k, v := range base {
				in[k] = v
			}
			in[tc.key] = "direct"
			_, err := LoadFromMap(in)
			if err == nil {
				t.Fatalf("键 %q 必须导致启动失败", tc.key)
			}
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("错误应属 ErrConfig 族，实得 %v", err)
			}
		})
	}
}

func TestConfigValidateRequiredAndJwksPath(t *testing.T) {
	t.Run("缺 base-url", func(t *testing.T) {
		c := DefaultConfig()
		if err := c.Validate(EnvTest); err == nil {
			t.Fatal("缺 platform.base-url 必须失败")
		}
	})
	t.Run("base-url 非法", func(t *testing.T) {
		c := DefaultConfig()
		c.Platform.BaseURL = "gj.example.com"
		c.Client.ID, c.Client.HMACSecret = "x", "y"
		if err := c.Validate(EnvTest); err == nil {
			t.Fatal("无 scheme 的 base-url 必须失败")
		}
	})
	t.Run("jwks 路径只允许契约值", func(t *testing.T) {
		c := DefaultConfig()
		c.Platform.BaseURL = "https://gj.example.com"
		c.Client.ID, c.Client.HMACSecret = "x", "y"
		c.Gateway.JwksURI = "https://gj.example.com/api/v1/jwks"
		if err := c.Validate(EnvTest); err == nil {
			t.Fatal("旧路径 /api/v1/jwks 必须失败（C-2）")
		}
		c.Gateway.JwksURI = ""
		if err := c.Validate(EnvTest); err != nil {
			t.Fatalf("默认派生路径应通过：%v", err)
		}
		if got := c.JwksURI(); got != "https://gj.example.com"+JwksPath {
			t.Fatalf("默认 jwks-uri 派生不对：%s", got)
		}
	})
}

func TestValidateEnabledFalseOnlyInLocalOrTest(t *testing.T) {
	newCfg := func() Config {
		c := DefaultConfig()
		c.Platform.BaseURL = "https://gj.example.com"
		c.Client.ID, c.Client.HMACSecret = "x", "y"
		c.Enabled = false
		return c
	}
	if err := newCfg().Validate(EnvLocal); err != nil {
		t.Fatalf("local 下 enabled=false 应允许：%v", err)
	}
	if err := newCfg().Validate(EnvProd); err == nil {
		t.Fatal("prod 下 enabled=false 必须启动失败（C-1 / AC-SDK-02）")
	}
	c := newCfg()
	c.Enabled = true
	c.Authz.Enabled = false
	if err := c.Validate(EnvProd); err == nil {
		t.Fatal("prod 下 authz.enabled=false 必须启动失败")
	}
}

func TestValidateMemoryReplayStoreOnlyInLocalOrTest(t *testing.T) {
	c := DefaultConfig()
	c.Platform.BaseURL = "https://gj.example.com"
	c.Client.ID, c.Client.HMACSecret = "x", "y"
	c.Authz.ReplayStore = ReplayStoreMemory
	if err := c.Validate(EnvProd); err == nil {
		t.Fatal("prod 下 replay-store=memory 必须失败（C-4）")
	}
	if err := c.Validate(EnvTest); err != nil {
		t.Fatalf("test 下应允许：%v", err)
	}
}

func TestValidateReplayWindowMustCoverExp(t *testing.T) {
	c := DefaultConfig()
	c.Platform.BaseURL = "https://gj.example.com"
	c.Client.ID, c.Client.HMACSecret = "x", "y"
	c.Gateway.ReplayWindow = 10 * 1e9 // 10s < 60s
	if err := c.Validate(EnvProd); err == nil {
		t.Fatal("replay-window < 令牌 exp 必须失败")
	}
}

func TestEnvNameMapping(t *testing.T) {
	cases := map[string]string{
		KeyPlatformBaseURL:              "GUANJIN_PLATFORM_BASE_URL",
		KeyGatewayJwksUnknownKidRefresh: "GUANJIN_GATEWAY_JWKS_UNKNOWN_KID_FORCE_REFRESH",
		KeyStartupPrintPublicPaths:      "GUANJIN_STARTUP_PRINT_PUBLIC_PATHS",
	}
	for key, want := range cases {
		if got := EnvName(key); got != want {
			t.Fatalf("EnvName(%q)=%q，期望 %q", key, got, want)
		}
	}
}

func TestCapabilitiesTableIsComplete(t *testing.T) {
	caps := Capabilities()
	if len(caps) != ContractCapabilityCount {
		t.Fatalf("能力必须是 %d 条（契约 §2.1），实得 %d", ContractCapabilityCount, len(caps))
	}
	for i, c := range caps {
		want := "GJ-CAP-" + twoDigits(i+1)
		if c.ID != want {
			t.Fatalf("第 %d 条能力 ID 应为 %s，实得 %s（顺序必须与契约一致）", i+1, want, c.ID)
		}
		if !c.Implemented && c.Reason == "" {
			t.Fatalf("能力 %s 未实现但没写 Reason（不许静默缺）", c.ID)
		}
	}
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// TestConfigRejectsCrossOriginJwksURI 落实 `notes/sdk-jwks-uri-同源校验-2026-11-10.md`
// （挂在 AC-SDK-04 下举证）：`guanjin.gateway.jwks-uri` 与 `guanjin.platform.base-url`
// **不同源（scheme/host/port 任一不一致）→ 启动失败**；port 按 scheme 默认值归一
// （http→80、https→443）；路径部分不参与同源判定。与 Java `GuanjinConfigValidator.sameOrigin`
// 同一形态（契约 §4.1）。变异自证：删掉 Validate 里的 sameOrigin 分支 → 下面三条全变绿。
func TestConfigRejectsCrossOriginJwksURI(t *testing.T) {
	mk := func(base, uri string) Config {
		cfg := DefaultConfig()
		cfg.Platform.BaseURL = base
		cfg.Client.ID = "order-service"
		cfg.Client.HMACSecret = "test-secret"
		cfg.Gateway.JwksURI = uri
		return cfg
	}
	bad := []struct{ name, base, uri string }{
		{"跨域主机", "https://iam.example.com", "https://evil.example.com/.well-known/jwks.json"},
		{"端口不同", "https://iam.example.com", "https://iam.example.com:8443/.well-known/jwks.json"},
		{"scheme 不同", "https://iam.example.com", "http://iam.example.com/.well-known/jwks.json"},
		{"base-url 本身非法（无 scheme）", "iam.example.com", "https://iam.example.com/.well-known/jwks.json"},
	}
	for _, c := range bad {
		err := mk(c.base, c.uri).Validate(EnvProd)
		if err == nil {
			t.Fatalf("%s：跨域/非法 jwks-uri 必须启动失败（换信任源 = 需 ADR，无开关）", c.name)
		}
		if !strings.Contains(err.Error(), "同源") && c.name != "base-url 本身非法（无 scheme）" {
			t.Fatalf("%s：错误信息必须点名「同源」（可排障），实得 %v", c.name, err)
		}
	}

	// 同源正例：不显式配端口（https 默认 443 归一）→ 与 base-url 同源，应通过
	if err := mk("https://iam.example.com", "https://iam.example.com/.well-known/jwks.json").Validate(EnvProd); err != nil {
		t.Fatalf("scheme+host+port 一致（默认端口归一后）必须通过：%v", err)
	}
}
