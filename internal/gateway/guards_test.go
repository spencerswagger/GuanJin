package gateway

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/signing"
)

// 本文件覆盖"装配期与边界"的一批小分支：它们不影响主路径，但**每一条都是 fail-closed 的开关**——
// 漏测一条就可能出现"少装配一个依赖却照样起服务"或"访问记录失败就 500"这类事故。

// NewGuard 的必需依赖校验：少一个都不许起数据面（否则等于把某条判据变成可选项）。
func TestNewGuardRequiresEveryDependency(t *testing.T) {
	base := func() *Guard {
		return &Guard{
			Caller:   stubCaller{},
			Sessions: &fakeSessions{},
			Policies: &fakePolicies{},
			Roles:    &fakeRoles{},
			Users:    &fakeUsers{},
			Visits:   &fakeVisits{},
		}
	}
	if _, err := NewGuard(nil); err == nil {
		t.Fatal("nil guard 必须报错")
	}
	cases := map[string]func(*Guard){
		"缺 Caller":   func(g *Guard) { g.Caller = nil },
		"缺 Sessions": func(g *Guard) { g.Sessions = nil },
		"缺 Policies": func(g *Guard) { g.Policies = nil },
		"缺 Roles":    func(g *Guard) { g.Roles = nil },
		"缺 Users":    func(g *Guard) { g.Users = nil },
		"缺 Visits":   func(g *Guard) { g.Visits = nil },
	}
	for name, mutate := range cases {
		g := base()
		mutate(g)
		if _, err := NewGuard(g); err == nil {
			t.Fatalf("%s 时 NewGuard 必须报错", name)
		}
	}
	// 负的 skew 归零（不许"负宽容度"让 iat 判据变得更严到自伤）。
	g := base()
	g.Skew = -time.Second
	got, err := NewGuard(g)
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	if got.Skew != 0 {
		t.Fatalf("负 skew 应归零，实际 %s", got.Skew)
	}
	if got.Now == nil || got.Logger == nil {
		t.Fatal("Now/Logger 必须补默认值")
	}
}

// 访问记录写失败**不阻塞转发**（已通过认证的请求不因统计表抖动被拒），但要留痕。
func TestVisitFailureDoesNotBlockRequest(t *testing.T) {
	var reported []error
	tg := newTestGuard(t, func(g *testGuard) {
		g.visits.err = errors.New("deadlock detected")
	})
	tg.guard.OnVisitError = func(err error) { reported = append(reported, err) }
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("访问记录失败不该影响转发，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if up.hits != 1 {
		t.Fatalf("应该照常转发，上游命中 %d", up.hits)
	}
	if len(reported) != 1 {
		t.Fatalf("失败必须有痕迹（OnVisitError 调用 %d 次）", len(reported))
	}
}

// 断言头写不进去（空断言）必须报错——静默跳过会让上游"看起来有身份"地收到空头。
func TestApplyRejectsEmptyAssertion(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.test/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := Apply(req, ""); err == nil {
		t.Fatal("空断言必须报错")
	}
	if err := Apply(nil, "token"); err == nil {
		t.Fatal("nil 请求必须报错")
	}
	// 正常写入会**覆盖**同名头（不是追加）。
	req.Header.Set(AssertionHeader, "old")
	if err := Apply(req, "new"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := req.Header.Values(AssertionHeader); len(got) != 1 || got[0] != "new" {
		t.Fatalf("断言头应被覆盖为单值，实际 %v", got)
	}
}

// `aud` 允许数组形态（RFC 7519），但必须包含路由命中的 client；类型不对必须报错。
func TestCheckAudienceAcceptsArrayForm(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		wantOK bool
	}{
		{"字符串命中", map[string]any{"iss": "i", "aud": "billing"}, true},
		{"数组命中", map[string]any{"iss": "i", "aud": []any{"crm", "billing"}}, true},
		{"数组未命中", map[string]any{"iss": "i", "aud": []any{"crm"}}, false},
		{"aud 类型错误", map[string]any{"iss": "i", "aud": 42}, false},
		{"无 claims", nil, false},
		{"无 iss", map[string]any{"aud": "billing"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkAudience(tc.claims, "billing")
			if tc.wantOK && err != nil {
				t.Fatalf("期望通过，实际 %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("期望拒绝，实际通过")
			}
		})
	}
}

// 签发器的边界：缺 sub/aud、缺 acr、空 jti、没有 gateway 用途的钥 → 一律拒绝签发。
func TestAssertionSignerRejectsBadInput(t *testing.T) {
	keys := newTestKeys(t)
	signer, err := NewAssertionSigner(keys)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	base := AssertionInput{Sub: "u", Aud: "billing", CID: "billing", ACR: "AAL1", Uname: "alice"}
	bad := map[string]func(*AssertionInput){
		"缺 sub": func(in *AssertionInput) { in.Sub = "" },
		"缺 aud": func(in *AssertionInput) { in.Aud = "" },
		"缺 acr": func(in *AssertionInput) { in.ACR = "" },
	}
	for name, mutate := range bad {
		in := base
		mutate(&in)
		if _, err := signer.Sign(in); err == nil {
			t.Fatalf("%s 时必须拒绝签发", name)
		}
	}

	// 空 jti：判重没有对象，拒签比"签出去但判不了重"安全。
	empty := *signer
	empty.NewJTI = func() (string, error) { return "", nil }
	if _, err := empty.Sign(base); err == nil {
		t.Fatal("空 jti 必须拒绝签发")
	}

	// 没有 gateway 用途的钥 → ErrNoSigner（不是 panic、不是签个空串）。
	oidcOnly := newTestKeys(t)
	if _, err := NewAssertionSigner(&signing.Set{}); !errors.Is(err, ErrNoSigner) {
		t.Fatalf("空密钥集期望 ErrNoSigner，实际 %v", err)
	}
	if _, err := NewAssertionSigner(nil); !errors.Is(err, ErrNoSigner) {
		t.Fatalf("nil 密钥集期望 ErrNoSigner，实际 %v", err)
	}
	var nilSigner *AssertionSigner
	if _, err := nilSigner.Sign(base); !errors.Is(err, ErrNoSigner) {
		t.Fatalf("nil 签发器期望 ErrNoSigner，实际 %v", err)
	}
	_ = oidcOnly
}
