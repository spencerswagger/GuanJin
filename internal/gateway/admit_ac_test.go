package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是数据面**拒绝路径与档位判定**的单测（AC-DP-04/05/06/07/09 + 会话失效四态）。
//
// 验签是**真的**：令牌由 `signing.SignJWT` 签、由 `protocol.CallerResolver` 用真公钥验，
// 篡改/换钥/未知 kid 都会在 SM2 验签上真失败——不是"mock 说它失败"。

// AC-DP-05：令牌缺失 → 401 deny-by-default，且不触达上游、不注入身份。
func TestAC_DP_05_MissingTokenIsDeniedByDefault(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
	}{
		{"完全没有 Authorization", ""},
		{"不是 Bearer", "Basic YWxpY2U6cHc="},
		{"Bearer 后为空", "Bearer   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tg := newTestGuard(t, nil)
			up := newRecordedUpstream(t)
			p := newTestPipeline(t, tg, up)

			req := newRequest("/api/billing/orders")
			if tc.header != "" {
				req.Header.Set(BearerHeader, tc.header)
			}
			rec := serve(p, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("期望 401，实际 %d（body=%s）", rec.Code, rec.Body.String())
			}
			if code := errorCode(t, rec); code != httpx.CodeTokenInvalid {
				t.Fatalf("错误码期望 TOKEN_INVALID，实际 %s", code)
			}
			if up.hits != 0 {
				t.Fatalf("上游被调用了 %d 次：令牌缺失不该转发", up.hits)
			}
		})
	}
}

// AC-DP-04：篡改 payload / 未知 kid / 结构坏 → 401，且不注入身份。
func TestAC_DP_04_TamperedTokenIsRejected(t *testing.T) {
	cases := []struct {
		name  string
		token func(t *testing.T, tg *testGuard) string
	}{
		{
			name: "篡改 payload（签名仍是原样）",
			token: func(t *testing.T, tg *testGuard) string {
				token := issueUserToken(t, tg.keys, nil)
				parts := strings.Split(token, ".")
				parts[1] = base64RawURL([]byte(`{"iss":"` + testIssuer + `","sub":"` + testUserID + `","aud":"other-client"}`))
				return strings.Join(parts, ".")
			},
		},
		{
			name: "未知 kid（用另一把 oidc 钥签）",
			token: func(t *testing.T, tg *testGuard) string {
				return issueUserToken(t, newTestKeys(t), nil)
			},
		},
		{
			name: "结构坏了（不是三段）",
			token: func(t *testing.T, tg *testGuard) string {
				return "not.a.jwt.at.all"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := newTestGuard(t, nil)
			up := newRecordedUpstream(t)
			p := newTestPipeline(t, tg, up)

			rec := doRequest(p, http.MethodGet, "/api/billing/orders", tc.token(t, tg))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("期望 401，实际 %d（body=%s）", rec.Code, rec.Body.String())
			}
			if code := errorCode(t, rec); code != httpx.CodeTokenInvalid {
				t.Fatalf("错误码期望 TOKEN_INVALID，实际 %s", code)
			}
			if up.hits != 0 {
				t.Fatalf("上游被调用了 %d 次：验签失败不该转发", up.hits)
			}
			if got := up.assertionValues(); len(got) != 0 {
				t.Fatalf("上游收到了断言 %v：验签失败不得注入身份", got)
			}
		})
	}
}

// AC-DP-06：`exp` 过期 → 401；`iat` 超前超过 skew → 401；边界按 `skew±1s` 打点。
//
// 本用例把 skew 设成 5s，于是"skew-1s 内放行、skew+1s 外拒绝"可以稳定打点：
//
//	iat = now+4s（< 5s）→ 放行；iat = now+6s（> 5s）→ 401。
//
// （`iat` 在 mutate 里**现取**时间：本用例要先解封两把 SM2 钥，取值过早会让
// "now+2s"在慢机器上落到 skew 之内，测试会随机假通过。）
func TestAC_DP_06_ClockSkewBoundaries(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(map[string]any)
		wantStatus int
		wantCode   httpx.Code
	}{
		{
			name:       "exp 已过期（1 秒前）",
			mutate:     func(c map[string]any) { c["exp"] = time.Now().UTC().Add(-time.Second).Unix() },
			wantStatus: http.StatusUnauthorized,
			wantCode:   httpx.CodeTokenInvalid,
		},
		{
			name:       "iat 超前 4s（skew 5s 内）",
			mutate:     func(c map[string]any) { c["iat"] = time.Now().UTC().Add(4 * time.Second).Unix() },
			wantStatus: http.StatusOK,
		},
		{
			name:       "iat 超前 6s（超出 skew 5s）",
			mutate:     func(c map[string]any) { c["iat"] = time.Now().UTC().Add(6 * time.Second).Unix() },
			wantStatus: http.StatusUnauthorized,
			wantCode:   httpx.CodeTokenInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := newTestGuard(t, func(g *testGuard) { g.skew = 5 * time.Second })
			up := newRecordedUpstream(t)
			p := newTestPipeline(t, tg, up)

			rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, tc.mutate))
			if rec.Code != tc.wantStatus {
				t.Fatalf("期望 %d，实际 %d（body=%s）", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if tc.wantCode != "" {
				if code := errorCode(t, rec); code != tc.wantCode {
					t.Fatalf("错误码期望 %s，实际 %s", tc.wantCode, code)
				}
			}
		})
	}
}

// AC-DP-07：`iss` 不符 / `aud` 不符或缺失 → 401（`aud` 必须等于**路由命中的 client**）。
func TestAC_DP_07_IssuerAndAudienceMismatch(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"iss 不是本平台", func(c map[string]any) { c["iss"] = "https://evil.example.com" }},
		{"aud 是别的业务系统", func(c map[string]any) { c["aud"] = "crm" }},
		{"aud 缺失", func(c map[string]any) { delete(c, "aud") }},
		{"iss 缺失", func(c map[string]any) { delete(c, "iss") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := newTestGuard(t, nil)
			up := newRecordedUpstream(t)
			p := newTestPipeline(t, tg, up)

			rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, tc.mutate))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("期望 401，实际 %d（body=%s）", rec.Code, rec.Body.String())
			}
			if up.hits != 0 {
				t.Fatalf("上游被调用了 %d 次：`aud`/`iss` 不符不该转发", up.hits)
			}
		})
	}
}

// AC-DP-09：档位不足 → **403 `ACR_INSUFFICIENT` + `details.flow`**（含 current_acr /
// target_acr / terminal_state），**不是 302、不是 500**，且不注入身份。
func TestAC_DP_09_ACRInsufficientCarriesFlowDescriptor(t *testing.T) {
	tg := newTestGuard(t, func(g *testGuard) {
		// 目标业务系统要求 AAL2（L3 档位），会话只达成 AAL1（单口令）；
		// 平台装配了 password + otp → AAL2 可达 → 终态必须是 `need_more`（引导补步）。
		g.policies.policy.MinACR = "AAL2"
		g.policies.policy.Tier = "L3"
		g.factors = testFactorsTwo()
	})
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("期望 403（不是 302/500），实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	var body struct {
		Code    httpx.Code `json:"code"`
		Details struct {
			Flow struct {
				FlowID        string `json:"flow_id"`
				TargetACR     string `json:"target_acr"`
				CurrentACR    string `json:"current_acr"`
				TerminalState string `json:"terminal_state"`
				Steps         []any  `json:"steps"`
				Client        struct {
					ID string `json:"id"`
				} `json:"client"`
			} `json:"flow"`
		} `json:"details"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
	}
	if body.Code != httpx.CodeACRInsufficient {
		t.Fatalf("错误码期望 ACR_INSUFFICIENT，实际 %s", body.Code)
	}
	f := body.Details.Flow
	if f.TargetACR != "AAL2" || f.CurrentACR != "AAL1" {
		t.Fatalf("details.flow 级别不符：target=%q current=%q", f.TargetACR, f.CurrentACR)
	}
	if f.TerminalState != "need_more" {
		t.Fatalf("terminal_state 期望 need_more，实际 %q", f.TerminalState)
	}
	if len(f.Steps) == 0 {
		t.Fatal("details.flow.steps 为空：客户端无法据此补步")
	}
	if f.Client.ID != testClientID {
		t.Fatalf("details.flow.client.id 期望 %q，实际 %q", testClientID, f.Client.ID)
	}
	if up.hits != 0 {
		t.Fatalf("上游被调用了 %d 次：档位不足不该转发", up.hits)
	}
	if got := up.assertionValues(); len(got) != 0 {
		t.Fatalf("上游收到了断言 %v：档位不足不得注入身份", got)
	}
}

// AC-DP-09（M1 真实能力面）：只有一个 password 因子时，目标 AAL2 **不可达** →
// `terminal_state=unreachable` 且带 `unreachable_reason`（AC-FLOW-07 的同源构造）。
func TestAC_DP_09_TerminalStateUnreachableInM1(t *testing.T) {
	tg := newTestGuard(t, func(g *testGuard) {
		g.policies.policy.MinACR = "AAL2"
		g.policies.policy.Tier = "L3"
	})
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"terminal_state":"unreachable"`) {
		t.Fatalf("单因子下目标 AAL2 应判 unreachable：%s", body)
	}
	if !strings.Contains(body, `"unreachable_reason"`) {
		t.Fatalf("不可达必须带原因（D7），实际：%s", body)
	}
}

// AC-DP-09 的另一半：有活跃流时 `details.flow.flow_id` 必须是那条流的 id（数据面只读 flows）。
func TestAC_DP_09_UsesActiveFlowIDWhenPresent(t *testing.T) {
	tg := newTestGuard(t, func(g *testGuard) {
		g.policies.policy.MinACR = "AAL2"
		g.flows.flow = &FlowSnapshot{
			ID: "0193f0aa-0000-7000-8000-00000000abcd", TargetACR: "AAL2",
			AchievedAMR: []string{"pwd"}, ExpiresAt: time.Now().UTC().Add(5 * time.Minute),
		}
	})
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "0193f0aa-0000-7000-8000-00000000abcd") {
		t.Fatalf("details.flow 里没有活跃流的 flow_id：%s", rec.Body.String())
	}
}

// 会话失效四态：撤销/过期/超时到点 → 403 `SESSION_REVOKED`；未知状态 → 503（不当 active）。
func TestSessionLivenessGates(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name       string
		mutate     func(*testGuard)
		wantStatus int
		wantCode   httpx.Code
	}{
		{"已撤销", func(g *testGuard) { g.sessions.snap.Status = SessionRevoked },
			http.StatusForbidden, httpx.CodeSessionRevoked},
		{"已过期（status）", func(g *testGuard) { g.sessions.snap.Status = SessionExpired },
			http.StatusForbidden, httpx.CodeSessionRevoked},
		{"绝对超时已到点", func(g *testGuard) { g.sessions.snap.ExpiresAt = now.Add(-time.Second) },
			http.StatusForbidden, httpx.CodeSessionRevoked},
		{"空闲超时已到点", func(g *testGuard) { g.sessions.snap.IdleExpiresAt = now.Add(-time.Second) },
			http.StatusForbidden, httpx.CodeSessionRevoked},
		{"未知状态（fail-closed）", func(g *testGuard) { g.sessions.snap.Status = "weird" },
			http.StatusServiceUnavailable, httpx.CodeDBUnavailable},
		{"会话不存在", func(g *testGuard) { g.sessions.err = ErrSessionNotFound },
			http.StatusForbidden, httpx.CodeSessionRevoked},
		{"令牌 sub 与会话归属不一致", func(g *testGuard) {
			g.sessions.snap.UserID = "0193f0aa-0000-7000-8000-0000000000ff"
		}, http.StatusUnauthorized, httpx.CodeTokenInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := newTestGuard(t, tc.mutate)
			up := newRecordedUpstream(t)
			p := newTestPipeline(t, tg, up)

			rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("期望 %d，实际 %d（body=%s）", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if code := errorCode(t, rec); code != tc.wantCode {
				t.Fatalf("错误码期望 %s，实际 %s", tc.wantCode, code)
			}
			if up.hits != 0 {
				t.Fatalf("上游被调用了 %d 次：会话失效不该转发", up.hits)
			}
		})
	}
}

// 会话**每请求现读**（零缓存窗口）：连续两次请求必须读两次库。
func TestSessionIsReadPerRequestNoCache(t *testing.T) {
	tg := newTestGuard(t, nil)
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)
	token := issueUserToken(t, tg.keys, nil)

	for i := 0; i < 3; i++ {
		if rec := doRequest(p, http.MethodGet, "/api/billing/orders", token); rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求期望 200，实际 %d（body=%s）", i+1, rec.Code, rec.Body.String())
		}
	}
	if tg.sessions.calls != 3 {
		t.Fatalf("会话读取次数期望 3（每次请求一次），实际 %d —— 存在缓存窗口", tg.sessions.calls)
	}
	if tg.policies.calls != 3 {
		t.Fatalf("门槛读取次数期望 3（每次请求现读，不缓存运维改动），实际 %d", tg.policies.calls)
	}
}

// 配置缺陷 → 500（不是 401/403：客户端没做错任何事），且不转发。
func TestAdmissionPolicyMissingIsServerError(t *testing.T) {
	for _, err := range []error{ErrPolicyNotConfigured, ErrClientNotFound, ErrClientDisabled} {
		tg := newTestGuard(t, func(g *testGuard) { g.policies.err = err })
		up := newRecordedUpstream(t)
		p := newTestPipeline(t, tg, up)

		rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%v 期望 500，实际 %d（body=%s）", err, rec.Code, rec.Body.String())
		}
		if up.hits != 0 {
			t.Fatalf("上游被调用了 %d 次：配置缺陷不该转发", up.hits)
		}
	}
}

// 库读失败 → 503 `DB_UNAVAILABLE`（不吞成 401：那是把"库挂了"伪装成"你没登录"）。
func TestDependencyFailureIsDBUnavailable(t *testing.T) {
	boom := errors.New("connection refused")
	cases := []struct {
		name   string
		mutate func(*testGuard)
	}{
		{"会话读取失败", func(g *testGuard) { g.sessions.err = boom }},
		{"角色读取失败", func(g *testGuard) { g.roles.err = boom }},
		{"用户名读取失败", func(g *testGuard) { g.users.err = boom }},
		{"门槛读取失败", func(g *testGuard) { g.policies.err = boom }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg := newTestGuard(t, tc.mutate)
			up := newRecordedUpstream(t)
			p := newTestPipeline(t, tg, up)

			rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("期望 503，实际 %d（body=%s）", rec.Code, rec.Body.String())
			}
			if code := errorCode(t, rec); code != httpx.CodeDBUnavailable {
				t.Fatalf("错误码期望 DB_UNAVAILABLE，实际 %s", code)
			}
			if got := up.assertionValues(); len(got) != 0 {
				t.Fatalf("上游收到了断言 %v：依赖失败不得注入身份", got)
			}
		})
	}
}

// 会话里的 `acr` 越界（AAL3 预留值 / 未知值）→ 500，不静默截断（D12(3)）。
func TestNonIssuableSessionACRIsInternalError(t *testing.T) {
	for _, acr := range []string{"AAL3", "AAL9", ""} {
		tg := newTestGuard(t, func(g *testGuard) { g.sessions.snap.ACR = acr })
		up := newRecordedUpstream(t)
		p := newTestPipeline(t, tg, up)

		rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("acr=%q 期望 500，实际 %d（body=%s）", acr, rec.Code, rec.Body.String())
		}
	}
}
