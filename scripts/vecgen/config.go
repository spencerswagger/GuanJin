// 生成 spec/vectors/config-mapping.json（29 项配置键 → 内部值）。
//
// 期望值由真实现 sdk/go/guanjin.LoadFromMap 产出（与 Go SDK 同源），Java 侧由
// SpecVectorsConformanceTest 用 GuanjinConfigLoader.fromMap 复算同一张表。
package main

import (
	"encoding/json"
	"fmt"
	"os"

	gj "github.com/spencerswagger/guanjin/sdk/go/guanjin"
)

type keyMeta struct {
	Index    int    `json:"index"`
	Key      string `json:"key"`
	Kind     string `json:"kind"` // bool | string | duration_ms | list
	Required bool   `json:"required"`
}

var keyMetas = []keyMeta{
	{1, gj.KeyEnabled, "bool", false},
	{2, gj.KeyPlatformBaseURL, "string", true},
	{3, gj.KeyPlatformConnectTimeout, "duration_ms", false},
	{4, gj.KeyPlatformReadTimeout, "duration_ms", false},
	{5, gj.KeyClientID, "string", true},
	{6, gj.KeyClientHMACSecret, "string", true},
	{7, gj.KeyGatewayAssertionHeader, "string", false},
	{8, gj.KeyGatewayIssuer, "string", false},
	{9, gj.KeyGatewayAud, "string", false},
	{10, gj.KeyGatewayClockSkew, "duration_ms", false},
	{11, gj.KeyGatewayReplayWindow, "duration_ms", false},
	{12, gj.KeyGatewayJwksURI, "string", false},
	{13, gj.KeyGatewayJwksCacheTTL, "duration_ms", false},
	{14, gj.KeyGatewayJwksUnknownKidRefresh, "bool", false},
	{15, gj.KeyAuthzEnabled, "bool", false},
	{16, gj.KeyAuthzDefaultRole, "string", false},
	{17, gj.KeyAuthzModelConf, "string", false},
	{18, gj.KeyAuthzReplayStore, "string", false},
	{19, gj.KeyAgentReportEnabled, "bool", false},
	{20, gj.KeyAgentReportOnStartup, "bool", false},
	{21, gj.KeyAgentReportOnChange, "bool", false},
	{22, gj.KeyAgentPullEnabled, "bool", false},
	{23, gj.KeyAgentPullInterval, "duration_ms", false},
	{24, gj.KeyPublicPaths, "list", false},
	{25, gj.KeyPublicPathsIncludeBuiltin, "bool", false},
	{26, gj.KeySecurityInjectContext, "bool", false},
	{27, gj.KeySecurityAuthoritiesResolver, "string", false},
	{28, gj.KeySecurityRolePrefix, "string", false},
	{29, gj.KeyStartupPrintPublicPaths, "bool", false},
}

// canonical 把 Config 摊平成 key → 规范值（duration 一律毫秒整数，list 一律数组）。
func canonical(c gj.Config) map[string]any {
	out := map[string]any{}
	for _, m := range keyMetas {
		switch m.Key {
		case gj.KeyEnabled:
			out[m.Key] = c.Enabled
		case gj.KeyPlatformBaseURL:
			out[m.Key] = c.Platform.BaseURL
		case gj.KeyPlatformConnectTimeout:
			out[m.Key] = c.Platform.ConnectTimeout.Milliseconds()
		case gj.KeyPlatformReadTimeout:
			out[m.Key] = c.Platform.ReadTimeout.Milliseconds()
		case gj.KeyClientID:
			out[m.Key] = c.Client.ID
		case gj.KeyClientHMACSecret:
			out[m.Key] = c.Client.HMACSecret
		case gj.KeyGatewayAssertionHeader:
			out[m.Key] = c.Gateway.AssertionHeader
		case gj.KeyGatewayIssuer:
			out[m.Key] = c.Gateway.Issuer
		case gj.KeyGatewayAud:
			out[m.Key] = c.Gateway.Aud
		case gj.KeyGatewayClockSkew:
			out[m.Key] = c.Gateway.ClockSkew.Milliseconds()
		case gj.KeyGatewayReplayWindow:
			out[m.Key] = c.Gateway.ReplayWindow.Milliseconds()
		case gj.KeyGatewayJwksURI:
			out[m.Key] = c.Gateway.JwksURI
		case gj.KeyGatewayJwksCacheTTL:
			out[m.Key] = c.Gateway.JwksCacheTTL.Milliseconds()
		case gj.KeyGatewayJwksUnknownKidRefresh:
			out[m.Key] = c.Gateway.JwksUnknownKidForceRefresh
		case gj.KeyAuthzEnabled:
			out[m.Key] = c.Authz.Enabled
		case gj.KeyAuthzDefaultRole:
			out[m.Key] = c.Authz.DefaultRole
		case gj.KeyAuthzModelConf:
			out[m.Key] = c.Authz.ModelConf
		case gj.KeyAuthzReplayStore:
			out[m.Key] = c.Authz.ReplayStore
		case gj.KeyAgentReportEnabled:
			out[m.Key] = c.Agent.Report.Enabled
		case gj.KeyAgentReportOnStartup:
			out[m.Key] = c.Agent.Report.OnStartup
		case gj.KeyAgentReportOnChange:
			out[m.Key] = c.Agent.Report.OnChange
		case gj.KeyAgentPullEnabled:
			out[m.Key] = c.Agent.Pull.Enabled
		case gj.KeyAgentPullInterval:
			out[m.Key] = c.Agent.Pull.Interval.Milliseconds()
		case gj.KeyPublicPaths:
			if c.PublicPaths == nil {
				out[m.Key] = []string{}
			} else {
				out[m.Key] = c.PublicPaths
			}
		case gj.KeyPublicPathsIncludeBuiltin:
			out[m.Key] = c.PublicPathsIncludeBuiltin
		case gj.KeySecurityInjectContext:
			out[m.Key] = c.Security.InjectContext
		case gj.KeySecurityAuthoritiesResolver:
			out[m.Key] = c.Security.AuthoritiesResolver
		case gj.KeySecurityRolePrefix:
			out[m.Key] = c.Security.RolePrefix
		case gj.KeyStartupPrintPublicPaths:
			out[m.Key] = c.Startup.PrintPublicPaths
		}
	}
	return out
}

// 契约 §3.2 #17 的默认值（Java 与契约一致；Go SDK 现在给 "model.conf"，缺陷归 Nova）。
const contractModelConf = "classpath:/guanjin/model.conf"

func genConfig() {
	type cse struct {
		VectorID string            `json:"vector_id"`
		Case     string            `json:"case"`
		Input    map[string]string `json:"input"`
		Expect   map[string]any    `json:"expect"`
		Derived  map[string]any    `json:"expect_derived,omitempty"`
		Note     string            `json:"note,omitempty"`
	}
	run := func(id, name string, in map[string]string, note string) cse {
		if in == nil {
			in = map[string]string{}
		}
		cfg, err := gj.LoadFromMap(in)
		if err != nil {
			panic(fmt.Sprintf("%s: %v", id, err))
		}
		c := cse{VectorID: id, Case: name, Input: in, Expect: canonical(cfg), Note: note}
		if in[gj.KeyGatewayJwksURI] == "" || in[gj.KeyGatewayAud] == "" {
			if in[gj.KeyPlatformBaseURL] != "" {
				c.Derived = map[string]any{"jwks_uri": cfg.JwksURI(), "aud": cfg.Aud()}
			}
		}
		return c
	}

	base := "http://127.0.0.1:18080"
	cases := []cse{
		run("CM-01", "defaults", nil, "空输入 = 29 项默认值（必填项为空串时由 Validate 拦，不在本表判）"),
		run("CM-02", "all_explicit", map[string]string{
			gj.KeyEnabled:                      "true",
			gj.KeyPlatformBaseURL:              base + "/",
			gj.KeyPlatformConnectTimeout:       "1500ms",
			gj.KeyPlatformReadTimeout:          "5s",
			gj.KeyClientID:                     "orders",
			gj.KeyClientHMACSecret:             "s3cr3t-from-env",
			gj.KeyGatewayAssertionHeader:       "X-Custom-Assertion",
			gj.KeyGatewayIssuer:                "gw-prod",
			gj.KeyGatewayAud:                   "orders-api",
			gj.KeyGatewayClockSkew:             "2m",
			gj.KeyGatewayReplayWindow:          "90s",
			gj.KeyGatewayJwksURI:               base + "/.well-known/jwks.json",
			gj.KeyGatewayJwksCacheTTL:          "3m",
			gj.KeyGatewayJwksUnknownKidRefresh: "false",
			gj.KeyAuthzEnabled:                 "true",
			gj.KeyAuthzDefaultRole:             "guest",
			gj.KeyAuthzModelConf:               "classpath:/guanjin/model.conf",
			gj.KeyAuthzReplayStore:             "memory",
			gj.KeyAgentReportEnabled:           "false",
			gj.KeyAgentReportOnStartup:         "false",
			gj.KeyAgentReportOnChange:          "true",
			gj.KeyAgentPullEnabled:             "true",
			gj.KeyAgentPullInterval:            "45s",
			gj.KeyPublicPaths:                  "[ /health , /metrics , /public/** ]",
			gj.KeyPublicPathsIncludeBuiltin:    "false",
			gj.KeySecurityInjectContext:        "true",
			gj.KeySecurityAuthoritiesResolver:  "com.acme.Resolver",
			gj.KeySecurityRolePrefix:           "AUTH_",
			gj.KeyStartupPrintPublicPaths:      "false",
		}, "29 项全部显式；base-url 尾斜杠被剥（契约 #2 无尾斜杠）"),
		run("CM-03", "duration_forms", map[string]string{
			gj.KeyPlatformBaseURL:        base,
			gj.KeyPlatformConnectTimeout: "250ms",
			gj.KeyGatewayClockSkew:       "1h",
			gj.KeyGatewayReplayWindow:    "90m",
			gj.KeyAgentPullInterval:      "30s",
		}, "ms|s|m|h 四种单位（两侧都认的形态）"),
		run("CM-04", "derivation", map[string]string{
			gj.KeyPlatformBaseURL: base,
			gj.KeyClientID:        "orders",
		}, "#12 jwks-uri / #9 aud 未显式配置时派生（base-url + /.well-known/jwks.json；aud 跟随 client.id）"),
		run("CM-05", "list_and_prefix", map[string]string{
			gj.KeyPlatformBaseURL:           base,
			gj.KeyClientID:                  "orders",
			gj.KeyPublicPaths:               "/health,/metrics",
			gj.KeySecurityRolePrefix:        "ROLE_",
			gj.KeyPublicPathsIncludeBuiltin: "true",
		}, "list 无方括号（逗号分隔）与带方括号（CM-02）等价"),
	}

	// 向量期望一律按**契约**：默认值缺陷键（Go 给 "model.conf"，契约 #17 要求
	// `classpath:/guanjin/model.conf`）只在"未显式配置"的用例里被覆盖成契约值，
	// Go 侧的偏差由 sdk/go 的测试按 known_divergences 钉死（漂移即红）。
	for i := range cases {
		if _, explicit := cases[i].Input[gj.KeyAuthzModelConf]; !explicit {
			cases[i].Expect[gj.KeyAuthzModelConf] = contractModelConf
		}
	}

	out := map[string]any{
		"spec":         "docs/sdk-contract.md §3.2（29 项配置键）",
		"generated_by": "Gate 2026-11-13：期望值由真实现 sdk/go/guanjin.LoadFromMap 产出",
		"encoding":     "duration 一律毫秒整数（duration_ms）；list 一律数组；bool 一律 true/false 字面量",
		"keys":         keyMetas,
		"cases":        cases,
		"known_divergences": map[string]any{
			"note": "已定性的三语言不一致。第一类是**实现缺陷**（按契约判，Go 侧偏差钉死，漂移即红）；第二类是解析口径差异（向量不采用这些形态，须 Spec/Ark 收口）",
			"defects": []map[string]string{
				{"key": gj.KeyAuthzModelConf, "contract": contractModelConf, "go": "model.conf", "java": contractModelConf,
					"owner": "Nova（Go SDK config.go DefaultModelConf）", "tracking": "2026-11-13 向量首跑发现；Go 侧断言已钉死，修好后本表与 Go 测试要同步改"},
			},
			"parse_forms": []map[string]string{
				{"form": "纯数字 duration（如 3000）", "go": "红：time.ParseDuration 要求单位", "java": "绿：按毫秒"},
				{"form": "单位 d（如 1d）", "go": "红：Go duration 无 d 单位", "java": "绿：按天"},
			},
		},
		"removed_keys": map[string]any{
			"note": "契约 §3.3 REMOVED：出现即启动失败（AC-SDK-01）。Go/Java 判错文案不同，两个实现都必须抛错",
			"keys": gj.RemovedKeys,
		},
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		panic(err)
	}
	b = append(b, '\n')
	if err := os.WriteFile("spec/vectors/config-mapping.json", b, 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("wrote spec/vectors/config-mapping.json (%d bytes)\n", len(b))
}
