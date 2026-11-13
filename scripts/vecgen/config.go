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
	Kind     string `json:"kind"` // bool | string | resource | duration_ms | list（resource = 包内资源路径，见契约 §3.2.2）
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
	{17, gj.KeyAuthzModelConf, "resource", false},
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

// 契约 §3.2 #17 的默认值（resource 形态；三语言逐字节一致，Go 侧 2026-11-13 已对齐）。
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

	// §3.2.1 的「必须失败」形态抽 7 条落成负例（Spec E35 点名 5 条 + `+60s`/`1S` 两条实测偏差）。
	// `current` = 该语言对这条负例的**期望行为**：§3.2.1 于 2026-11-13 两侧落地（Java core 正则 +
	// boot2 绑定期守卫、Go 先正则再 time.ParseDuration），故全部 reject。消费者按 current 钉死：
	// 某语言一旦回退（又接受非法形态），断言立刻红（表不会腐化成假绿）。
	rejectCases := []any{
		map[string]any{"vector_id": "CM-06", "case": "duration_reject_plain_number", "key": gj.KeyGatewayClockSkew, "value": "3000",
			"reason": "§3.2.1 必须失败①：纯数字（隐式毫秒单位）", "current": map[string]string{"go": "reject", "java": "reject"}},
		map[string]any{"vector_id": "CM-07", "case": "duration_reject_unit_d", "key": gj.KeyGatewayClockSkew, "value": "1d",
			"reason": "§3.2.1 必须失败②：单位 `d`（不在闭合集 ms|s|m|h）", "current": map[string]string{"go": "reject", "java": "reject"}},
		map[string]any{"vector_id": "CM-08", "case": "duration_reject_combined", "key": gj.KeyGatewayClockSkew, "value": "1h30m",
			"reason": "§3.2.1 必须失败⑤：组合形态", "current": map[string]string{"go": "reject", "java": "reject"}},
		map[string]any{"vector_id": "CM-09", "case": "duration_reject_fraction", "key": gj.KeyPlatformReadTimeout, "value": "1.5s",
			"reason": "§3.2.1 必须失败④：小数", "current": map[string]string{"go": "reject", "java": "reject"}},
		map[string]any{"vector_id": "CM-10", "case": "duration_reject_iso8601", "key": gj.KeyPlatformConnectTimeout, "value": "PT60S",
			"reason": "§3.2.1 必须失败⑥：ISO-8601", "current": map[string]string{"go": "reject", "java": "reject"}},
		map[string]any{"vector_id": "CM-11", "case": "duration_reject_signed", "key": gj.KeyAgentPullInterval, "value": "+60s",
			"reason": "§3.2.1 必须失败⑦：带符号", "current": map[string]string{"go": "reject", "java": "reject"}},
		map[string]any{"vector_id": "CM-12", "case": "duration_reject_upper_unit", "key": gj.KeyGatewayJwksCacheTTL, "value": "1S",
			"reason": "§3.2.1 单位闭合集判据：大写单位不认（`1S`/`10M` 非法）", "current": map[string]string{"go": "reject", "java": "reject"}},
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

	// 向量期望一律按**契约**。历史上 #17（Go 曾给 "model.conf"）靠 known_divergences 钉死；
	// 2026-11-13 修好后改**生成期硬校验**：实现一旦漂移，生成向量时直接炸，不再静默覆盖。
	for i := range cases {
		if _, explicit := cases[i].Input[gj.KeyAuthzModelConf]; !explicit {
			if got := cases[i].Expect[gj.KeyAuthzModelConf]; got != contractModelConf {
				panic(fmt.Sprintf("%s: %s 默认值 = %v，契约 #17 要求 %q",
					cases[i].VectorID, gj.KeyAuthzModelConf, got, contractModelConf))
			}
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
			"note":    "口径（写死，别两种说法并存）：**已修就删表、未修才登记**。①首选**生成期硬校验** —— 实现一漂移，`go run ./scripts/vecgen` 直接 panic（例：#17 的默认值，见上方校验循环），不留任何\"静默覆盖期望\"的口子；②只有\"生成期判不死、又必须先把契约值当期望\"的偏差才进 `defects`，写法固定为「期望按契约 + 把实现现值钉死」，漂移即红；③**修好后必须删掉该条**（不保留\"已修历史\"，历史在 git 里）。当前 `defects` 为空：`guanjin.authz.model-conf`（Go 曾给 \"model.conf\"）已于 2026-11-13 修好并删表。duration 口径差异已由契约 §3.2.1 裁决（收紧 Java），落到 `duration_rule` + `reject_cases`，不再是\"差异\"",
			"defects": []map[string]string{},
			"duration_rule": map[string]any{
				"spec":              "docs/sdk-contract.md §3.2.1（v13 裁决：收紧 Java，不放宽契约）",
				"legal":             "trim 后整串匹配 ^[0-9]+(ms|s|m|h)$（单位闭合集只 4 个，大写不认）",
				"failure":           "启动失败（fail-fast），错误信息必须含键名；不得静默回落默认值",
				"former_divergence": "首跑时 3000/1d 只有 Java 接受（Spring Duration 转换器）、Go 拒；1h30m/1.5s/+60s 反过来是 Go 接受、Java 拒 → 按 §3.2.1 全部改成负例。2026-11-13 两侧均已收紧（Java core 正则 + boot2 绑定期守卫、Go 先正则再 time.ParseDuration），故 current 全部为 reject；这是**已落地断言**，不是现状记录",
				"owner":             "Nova 实施（Java + Go 同一正则）；Gate 出负例向量",
			},
		},
		"reject_cases": rejectCases,
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
