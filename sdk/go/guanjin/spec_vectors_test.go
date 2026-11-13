package guanjin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// spec/vectors/assertion-verify.json 的契约一致性向量。
//
// 每一条都用**本 SDK 真实现**跑：JWKS 用本地 httptest 服务（不联网），验签走 Verifier.Verify，
// claim 层走 ValidateClaims（时钟注入 vector 的 reference_now），重放走 MemoryReplayGuard。
// 期望值按 vector 的 expect.{decision,status,code,stage} 判，stage 只用于报错定位。
func TestSpecVectorsAssertionVerify(t *testing.T) {
	raw, err := os.ReadFile("../../../spec/vectors/assertion-verify.json")
	if err != nil {
		t.Fatalf("读不到 spec/vectors/assertion-verify.json: %v", err)
	}
	var doc struct {
		ClockSkewSeconds int64           `json:"clock_skew_seconds"`
		ReferenceNowUnix int64           `json:"reference_now_unix"`
		JWKS             json.RawMessage `json:"jwks"`
		Vectors          []struct {
			VectorID string `json:"vector_id"`
			Case     string `json:"case"`
			Token    string `json:"token"`
			JwksMode string `json:"jwks_mode"`
			Replay   int    `json:"replay_times"`
			Expect   struct {
				Decision string `json:"decision"`
				Status   int    `json:"status"`
				Code     string `json:"code"`
				Stage    string `json:"stage"`
			} `json:"expect"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("assertion-verify.json 非法 JSON: %v", err)
	}
	if len(doc.Vectors) < 10 {
		t.Fatalf("契约附录 C 要求 ≥10 条，得到 %d", len(doc.Vectors))
	}

	// D29 证据的前提：JWKS 里**确实**有 oidc 轨（alg=SM3withSM2）的公钥，
	// 否则 AV-12 的 401 可能只是"找不到键"，证明不了"拒的是 alg"。
	jwksText := strings.ReplaceAll(string(doc.JWKS), " ", "")
	for _, want := range []string{`"alg":"SM2"`, `"alg":"SM3withSM2"`, "oidc-2026-11", "gw-2026-11"} {
		if !strings.Contains(jwksText, want) {
			t.Fatalf("JWKS 缺 %s —— AV-12 的 alg 负例就失去前提", want)
		}
	}

	jwksOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(doc.JWKS)
	}))
	defer jwksOK.Close()
	jwksDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer jwksDown.Close()

	refNow := time.Unix(doc.ReferenceNowUnix, 0).UTC()

	for _, v := range doc.Vectors {
		baseURL := jwksOK.URL
		if v.JwksMode == "unavailable" {
			baseURL = jwksDown.URL
		}
		cfg, err := LoadFromMap(map[string]string{
			KeyPlatformBaseURL:     baseURL,
			KeyClientID:            "orders",
			KeyClientHMACSecret:    "s3cr3t-vector",
			KeyGatewayClockSkew:    "60s",
			KeyGatewayReplayWindow: "60s",
			KeyAuthzReplayStore:    "memory",
		})
		if err != nil {
			t.Fatalf("%s: 构造 cfg 失败: %v", v.VectorID, err)
		}
		jwks := NewJwksClient(cfg, nil)
		ver := NewVerifier(cfg, jwks)
		ver.now = func() time.Time { return refNow }

		dec, status, code := "allow", 0, ""
		claims, err := ver.Verify(context.Background(), v.Token)
		if err == nil {
			if err = ver.ValidateClaims(claims); err != nil {
				status, code = 401, CodeTokenInvalid
			}
		} else {
			status, code = statusOf(err)
		}

		if v.Replay > 1 {
			// 重放语义：同一 jti 第二次必拒（首次放行）。用内存 store 复现。
			guard := NewMemoryReplayGuard()
			first, err1 := guard.FirstUse(context.Background(), claims.Jti, time.Unix(claims.Exp, 0))
			second, err2 := guard.FirstUse(context.Background(), claims.Jti, time.Unix(claims.Exp, 0))
			if err1 != nil || err2 != nil {
				t.Fatalf("%s: replay store 出错: %v / %v", v.VectorID, err1, err2)
			}
			if !first || second {
				t.Fatalf("%s: 重放语义不对（first=%v second=%v，期望 first=true second=false）", v.VectorID, first, second)
			}
			status, code = 401, CodeTokenInvalid
		}

		if status != 0 {
			dec = "reject"
		}
		if dec != v.Expect.Decision || status != v.Expect.Status || code != v.Expect.Code {
			t.Errorf("%s(%s) stage=%s：got decision=%s status=%d code=%s；want decision=%s status=%d code=%s（err=%v）",
				v.VectorID, v.Case, v.Expect.Stage, dec, status, code, v.Expect.Decision, v.Expect.Status, v.Expect.Code, err)
		}

		// 关键负例（D29）单独再断言一次：alg 非 SM2 必须在取公钥之前就被拒（不是"找不到键"的 401）。
		if v.Case == "alg_not_sm2_oidc_track" || v.Case == "alg_hs256_downgrade" {
			if err == nil {
				t.Fatalf("%s: alg 降级竟然通过验签", v.VectorID)
			}
			var e *Error
			if !errors.As(err, &e) || e.Code != CodeTokenInvalid {
				t.Fatalf("%s: 期望 TOKEN_INVALID，得到 %v", v.VectorID, err)
			}
		}
	}
}

func statusOf(err error) (int, string) {
	var e *Error
	if errors.As(err, &e) {
		return e.Status, e.Code
	}
	return 0, err.Error()
}

// spec/vectors/config-mapping.json：29 项配置键在中/默认值/派生值上逐项等价。
func TestSpecVectorsConfigMapping(t *testing.T) {
	raw, err := os.ReadFile("../../../spec/vectors/config-mapping.json")
	if err != nil {
		t.Fatalf("读不到 spec/vectors/config-mapping.json: %v", err)
	}
	var doc struct {
		Keys []struct {
			Index int    `json:"index"`
			Key   string `json:"key"`
			Kind  string `json:"kind"`
		} `json:"keys"`
		Cases []struct {
			VectorID string            `json:"vector_id"`
			Case     string            `json:"case"`
			Input    map[string]string `json:"input"`
			Expect   map[string]any    `json:"expect"`
			Derived  map[string]any    `json:"expect_derived"`
		} `json:"cases"`
		Removed struct {
			Keys []string `json:"keys"`
		} `json:"removed_keys"`
		Divergences struct {
			Defects []struct {
				Key      string `json:"key"`
				Contract string `json:"contract"`
				Go       string `json:"go"`
			} `json:"defects"`
		} `json:"known_divergences"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("config-mapping.json 非法 JSON: %v", err)
	}
	if len(doc.Keys) != 29 {
		t.Fatalf("契约 §3.2 是 29 项，向量声明 %d 项", len(doc.Keys))
	}
	if len(ConfigKeys) != 29 {
		t.Fatalf("Go SDK 的 ConfigKeys 应为 29 项，得到 %d", len(ConfigKeys))
	}
	for i, k := range doc.Keys {
		if k.Index != i+1 {
			t.Errorf("keys[%d] 编号不连续：index=%d", i, k.Index)
		}
		if ConfigKeys[i] != k.Key {
			t.Errorf("第 %d 项键名不一致：向量=%q Go=%q", k.Index, k.Key, ConfigKeys[i])
		}
	}

	// 已定性的实现缺陷：Go 侧实际值被**钉死**（漂移即红），期望值按契约写。
	pinned := map[string]string{}
	for _, d := range doc.Divergences.Defects {
		pinned[d.Key] = d.Go
		if d.Contract == d.Go {
			t.Errorf("known_divergences 记的缺陷 %q 两侧已一致，请从表里删掉（表不能腐化）", d.Key)
		}
	}

	for _, c := range doc.Cases {
		cfg, err := LoadFromMap(c.Input)
		if err != nil {
			t.Errorf("%s(%s): LoadFromMap 失败: %v", c.VectorID, c.Case, err)
			continue
		}
		got := configCanonical(cfg)
		for k, want := range c.Expect {
			if pin, ok := pinned[k]; ok && c.Input[k] == "" {
				if !jsonEqual(got[k], pin) {
					t.Errorf("%s(%s) 键 %s：Go 实现已偏离被钉死的缺陷值 %q（got=%v）——若缺陷已修，请同步 config-mapping.json 的 known_divergences",
						c.VectorID, c.Case, k, pin, got[k])
				}
				if jsonEqual(want, pin) {
					t.Errorf("%s: 缺陷键 %s 的契约期望与 Go 实际值相同，表已腐化", c.VectorID, k)
				}
				continue
			}
			if !jsonEqual(got[k], want) {
				t.Errorf("%s(%s) 键 %s：got=%v want=%v", c.VectorID, c.Case, k, got[k], want)
			}
		}
		if c.Derived != nil {
			if s, _ := c.Derived["jwks_uri"].(string); s != "" && cfg.JwksURI() != s {
				t.Errorf("%s: 派生 jwks-uri got=%q want=%q", c.VectorID, cfg.JwksURI(), s)
			}
			if s, _ := c.Derived["aud"].(string); s != "" && cfg.Aud() != s {
				t.Errorf("%s: 派生 aud got=%q want=%q", c.VectorID, cfg.Aud(), s)
			}
		}
	}

	// REMOVED 键：出现即启动失败（AC-SDK-01）。
	for _, k := range doc.Removed.Keys {
		if _, err := LoadFromMap(map[string]string{k: "x"}); err == nil {
			t.Errorf("REMOVED 键 %q 竟被接受（契约 §3.3 要求出现即失败）", k)
		}
	}
}

func configCanonical(c Config) map[string]any {
	list := c.PublicPaths
	if list == nil {
		list = []string{}
	}
	return map[string]any{
		KeyEnabled:                      c.Enabled,
		KeyPlatformBaseURL:              c.Platform.BaseURL,
		KeyPlatformConnectTimeout:       c.Platform.ConnectTimeout.Milliseconds(),
		KeyPlatformReadTimeout:          c.Platform.ReadTimeout.Milliseconds(),
		KeyClientID:                     c.Client.ID,
		KeyClientHMACSecret:             c.Client.HMACSecret,
		KeyGatewayAssertionHeader:       c.Gateway.AssertionHeader,
		KeyGatewayIssuer:                c.Gateway.Issuer,
		KeyGatewayAud:                   c.Gateway.Aud,
		KeyGatewayClockSkew:             c.Gateway.ClockSkew.Milliseconds(),
		KeyGatewayReplayWindow:          c.Gateway.ReplayWindow.Milliseconds(),
		KeyGatewayJwksURI:               c.Gateway.JwksURI,
		KeyGatewayJwksCacheTTL:          c.Gateway.JwksCacheTTL.Milliseconds(),
		KeyGatewayJwksUnknownKidRefresh: c.Gateway.JwksUnknownKidForceRefresh,
		KeyAuthzEnabled:                 c.Authz.Enabled,
		KeyAuthzDefaultRole:             c.Authz.DefaultRole,
		KeyAuthzModelConf:               c.Authz.ModelConf,
		KeyAuthzReplayStore:             c.Authz.ReplayStore,
		KeyAgentReportEnabled:           c.Agent.Report.Enabled,
		KeyAgentReportOnStartup:         c.Agent.Report.OnStartup,
		KeyAgentReportOnChange:          c.Agent.Report.OnChange,
		KeyAgentPullEnabled:             c.Agent.Pull.Enabled,
		KeyAgentPullInterval:            c.Agent.Pull.Interval.Milliseconds(),
		KeyPublicPaths:                  list,
		KeyPublicPathsIncludeBuiltin:    c.PublicPathsIncludeBuiltin,
		KeySecurityInjectContext:        c.Security.InjectContext,
		KeySecurityAuthoritiesResolver:  c.Security.AuthoritiesResolver,
		KeySecurityRolePrefix:           c.Security.RolePrefix,
		KeyStartupPrintPublicPaths:      c.Startup.PrintPublicPaths,
	}
}

func jsonEqual(got, want any) bool {
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	return string(gb) == string(wb)
}
