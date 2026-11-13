package guanjin

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 契约 §3.2.1 的 6 个 duration 键（#3/#4/#10/#11/#13/#23）。
var durationKeysForTest = []string{
	KeyPlatformConnectTimeout,
	KeyPlatformReadTimeout,
	KeyGatewayClockSkew,
	KeyGatewayReplayWindow,
	KeyGatewayJwksCacheTTL,
	KeyAgentPullInterval,
}

// 契约 §3.2.1：合法形态唯一 = `trim` 后 `^[0-9]+(ms|s|m|h)$`；下列九类**必须启动失败**，
// 且错误里必须出现**键名**、不得回落默认值（AC-SDK-11）。
func TestAC_SDK_11_DurationRejectsNineIllegalForms(t *testing.T) {
	illegal := []struct {
		name string
		raw  string
	}{
		{"①纯数字", "3000"},
		{"②单位 d", "1d"},
		{"③单位 ns", "100ns"},
		{"③单位 us", "100us"},
		{"③微秒符号 µs", "100µs"},
		{"③微秒符号 μs", "100μs"},
		{"④小数", "1.5s"},
		{"⑤组合", "1h30m"},
		{"⑥ISO-8601", "PT60S"},
		{"⑦带符号加", "+60s"},
		{"⑦带符号减", "-1s"},
		{"⑧空串", ""},
		{"⑧纯空白", "   "},
		{"⑨内部空白", "60 s"},
		{"大写单位", "1S"},
		{"大写单位", "10M"},
		{"裸单位", "s"},
		{"负数", "--1s"},
	}
	for _, key := range durationKeysForTest {
		for _, tc := range illegal {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				_, err := LoadFromMap(map[string]string{key: tc.raw})
				if err == nil {
					t.Fatalf("键 %q 的非法时长 %q 必须启动失败（契约 §3.2.1）", key, tc.raw)
				}
				if !errors.Is(err, ErrConfig) {
					t.Fatalf("错误应属 ErrConfig 族，实得 %v", err)
				}
				if !strings.Contains(err.Error(), key) {
					t.Fatalf("错误信息必须含键名 %q（§3.6），实得 %q", key, err.Error())
				}
				if !strings.Contains(err.Error(), DurationPattern) {
					t.Fatalf("错误信息应指出合法形态 %q，实得 %q", DurationPattern, err.Error())
				}
			})
		}
	}
}

// 合法形态：4 个单位、非负整数、允许首尾空白（trim），换算成无隐式单位的时间值。
func TestAC_SDK_11_DurationAcceptsOnlyCanonicalForms(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"0ms", 0},
		{"300ms", 300 * time.Millisecond},
		{"2s", 2 * time.Second},
		{"60s", 60 * time.Second},
		{"10m", 10 * time.Minute},
		{"1h", time.Hour},
		{"007s", 7 * time.Second},
		{"  30s  ", 30 * time.Second},
	}
	for _, tc := range cases {
		for _, key := range durationKeysForTest {
			t.Run(key+"/"+strings.TrimSpace(tc.raw), func(t *testing.T) {
				cfg, err := LoadFromMap(map[string]string{key: tc.raw})
				if err != nil {
					t.Fatalf("键 %q 的合法时长 %q 不该失败：%v", key, tc.raw, err)
				}
				var got time.Duration
				switch key {
				case KeyPlatformConnectTimeout:
					got = cfg.Platform.ConnectTimeout
				case KeyPlatformReadTimeout:
					got = cfg.Platform.ReadTimeout
				case KeyGatewayClockSkew:
					got = cfg.Gateway.ClockSkew
				case KeyGatewayReplayWindow:
					got = cfg.Gateway.ReplayWindow
				case KeyGatewayJwksCacheTTL:
					got = cfg.Gateway.JwksCacheTTL
				case KeyAgentPullInterval:
					got = cfg.Agent.Pull.Interval
				}
				if got != tc.want {
					t.Fatalf("键 %q 的 %q 应换算成 %v，实得 %v", key, tc.raw, tc.want, got)
				}
			})
		}
	}
}

// 非法时长不得返回"可用实例"：结构体必须是零值（§3.6 Go ④）。
func TestAC_SDK_11_DurationIllegalFormReturnsNoUsableConfig(t *testing.T) {
	cfg, err := LoadFromMap(map[string]string{
		KeyPlatformBaseURL:     "https://gj.example.com",
		KeyClientID:            "orders",
		KeyGatewayJwksCacheTTL: "1h30m",
	})
	if err == nil {
		t.Fatal("组合形态 1h30m 必须失败")
	}
	if cfg.Platform.BaseURL != "" || cfg.Client.ID != "" || cfg.Gateway.JwksCacheTTL != 0 {
		t.Fatalf("失败时不得返回可用实例，实得 %+v", cfg)
	}
}

// Validate() 也要拒：装载期已拒一次，但程序化赋值/二次校验同样不能放过（AC-SDK-11）。
func TestAC_SDK_11_ValidateRechecksDurationTexts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Platform.BaseURL = "https://gj.example.com"
	cfg.Client.ID = "orders"
	cfg.Client.HMACSecret = strings.Repeat("a", 32)
	cfg.Gateway.JwksURI = "https://gj.example.com/.well-known/jwks.json"
	// 直接注入"装载时保留的原文"，模拟绕过 set() 的路径（同包测试可触达未导出字段）。
	cfg.rawDurations = map[string]string{KeyGatewayClockSkew: "3000"}

	err := cfg.Validate(EnvProd)
	if err == nil {
		t.Fatal("Validate() 必须对保留的非法原文报错（先正则再换算）")
	}
	if !strings.Contains(err.Error(), KeyGatewayClockSkew) {
		t.Fatalf("错误信息必须含键名，实得 %q", err.Error())
	}
}

// 环境变量形态同样按 §3.2.1 判（同名键、同一错误）。
func TestAC_SDK_11_DurationFromEnvironIsStrict(t *testing.T) {
	_, err := LoadFromEnviron([]string{
		"GUANJIN_PLATFORM_BASE_URL=https://gj.example.com",
		"GUANJIN_CLIENT_ID=orders",
		"GUANJIN_PLATFORM_CONNECT_TIMEOUT=3000",
	})
	if err == nil {
		t.Fatal("环境变量里的纯数字 3000 必须失败")
	}
	if !strings.Contains(err.Error(), KeyPlatformConnectTimeout) {
		t.Fatalf("错误信息必须含键名，实得 %q", err.Error())
	}
}

// 契约 §3.2.1「首尾空白」：字符集冻结为 4 个 ASCII 空白（U+0020/\t U+0009/\n U+000A/\r U+000D），
// **其它空白一律不 trim**（NBSP、全角空格、U+2000–200A、U+000B/U+000C、U+0000）→ 落正则外即失败。
// 这条同时钉住「不得用 strings.TrimSpace」——它会把 NBSP/全角当空白，与 Java/Node 分叉。
func TestAC_SDK_11_DurationTrimSetIsFrozenAsciiWhitespace(t *testing.T) {
	legal := []struct {
		raw  string
		want time.Duration
	}{
		{"\t30s\r\n", 30 * time.Second},
		{"\r\n 2s \t", 2 * time.Second},
		{" 10m ", 10 * time.Minute},
		{"\t\t1h\n\n", time.Hour},
	}
	for _, tc := range legal {
		_, err := LoadFromMap(map[string]string{KeyAgentPullInterval: tc.raw})
		if err != nil {
			t.Fatalf("4 个 ASCII 空白必须允许 trim，%q 不该失败：%v", tc.raw, err)
		}
		cfg, err := LoadFromMap(map[string]string{KeyAgentPullInterval: tc.raw})
		if err != nil {
			t.Fatalf("二次装载失败：%v", err)
		}
		if cfg.Agent.Pull.Interval != tc.want {
			t.Fatalf("%q 应换算成 %v，实得 %v", tc.raw, tc.want, cfg.Agent.Pull.Interval)
		}
	}

	illegal := []struct {
		name string
		raw  string
	}{
		{"NBSP 前缀 U+00A0", "\u00A02s"},
		{"NBSP 后缀 U+00A0", "2s\u00A0"},
		{"全角空格 U+3000", "\u30002s"},
		{"全角空格后缀", "2s\u3000"},
		{"U+2000 前缀", "\u20002s"},
		{"U+2003 后缀", "2s\u2003"},
		{"U+000B 垂直制表", "\u000B30s"},
		{"U+000C 换页", "\u000C30s"},
		{"U+0000 NUL", "\u000030s"},
		{"NUL 夹在中间", "3\u00000s"},
	}
	for _, tc := range illegal {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFromMap(map[string]string{KeyAgentPullInterval: tc.raw})
			if err == nil {
				t.Fatalf("%q 不属于 4 个 ASCII 空白，不得 trim，必须失败（契约 §3.2.1）", tc.raw)
			}
			if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), KeyAgentPullInterval) {
				t.Fatalf("错误应为 ErrConfig 且含键名，实得 %v", err)
			}
		})
	}
}

// 契约 §3.2.1「数值上界」：必须能**无损**换算成 int64 纳秒（= time.Duration 范围，≈292 年）；
// 超出（或 n 本身超 int64）→ 失败报键名，**不许截断到最大值、不许退化成 0**。
func TestAC_SDK_11_DurationUpperBoundIsInt64Nanos(t *testing.T) {
	legal := []struct {
		raw  string
		want time.Duration
	}{
		{"2562047h", 2562047 * time.Hour},                     // int64 ns 上限内的最大整小时
		{"153722867m", 153722867 * time.Minute},               // 同上，单位换成分
		{"9223372036s", 9223372036 * time.Second},             // 同上，换成秒
		{"9223372036854ms", 9223372036854 * time.Millisecond}, // 同上，换成毫秒
	}
	for _, tc := range legal {
		cfg, err := LoadFromMap(map[string]string{KeyGatewayJwksCacheTTL: tc.raw})
		if err != nil {
			t.Fatalf("%q 在上界内，不该失败：%v", tc.raw, err)
		}
		if cfg.Gateway.JwksCacheTTL != tc.want {
			t.Fatalf("%q 应换算成 %v，实得 %v", tc.raw, tc.want, cfg.Gateway.JwksCacheTTL)
		}
	}

	illegal := []string{
		"2562048h",              // 超 1 整小时
		"153722868m",            // 超 1 整分
		"9223372037s",           // 超 1 整秒
		"9223372036855ms",       // 超 1 毫秒
		"9999999999999h",        // 契约 CM-15 举的值
		"99999999999999999999s", // n 本身超 int64
	}
	for _, raw := range illegal {
		t.Run(raw, func(t *testing.T) {
			cfg, err := LoadFromMap(map[string]string{KeyGatewayJwksCacheTTL: raw})
			if err == nil {
				t.Fatalf("%q 超出 int64 纳秒上界，必须失败（契约 §3.2.1）", raw)
			}
			if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), KeyGatewayJwksCacheTTL) {
				t.Fatalf("错误应为 ErrConfig 且含键名，实得 %v", err)
			}
			// 文案必须点明「int64 纳秒上界」，不能只是 time.ParseDuration 的通用语法错
			// （否则换语言/换库就分不清"上界溢出"与"语法非法"，也证不了本判定真在跑）
			if !strings.Contains(err.Error(), "int64") {
				t.Fatalf("上界溢出的错误文案应点明 int64 纳秒上界，实得 %q", err.Error())
			}
			if cfg.Gateway.JwksCacheTTL != 0 {
				t.Fatalf("上界溢出不得退化成可用值/截断值，实得 %v", cfg.Gateway.JwksCacheTTL)
			}
		})
	}
}
