package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/protocol"
	"github.com/spencerswagger/guanjin/internal/signing"
)

// 本文件是 S8 的测试夹具：**真实密钥 + 真实验签**（不 mock SM2），
// 只有"库"和"上游"是假的——这样 AC-DP-01/04/06/07 才是"验签真的生效"而不是"mock 说了算"。

const (
	testIssuer   = "https://auth.guanjin.test"
	testClientID = "billing"
	testUserID   = "0193f0aa-0000-7000-8000-000000000001"
	testSID      = "0193f0aa-0000-7000-8000-000000000002"
)

// newTestKeys 生成一对真密钥（oidc 用于用户令牌，gateway 用于断言）并解封成 `signing.Set`。
func newTestKeys(t *testing.T) *signing.Set {
	t.Helper()
	master, err := crypto.NewMasterKey(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	keywrap, err := master.DeriveSM4(crypto.PurposeSM4Keywrap)
	if err != nil {
		t.Fatalf("DeriveSM4: %v", err)
	}
	oidcRow, err := signing.GenRow(rand.Reader, keywrap, "kid-oidc", signing.PurposeOIDC, signing.AlgSM3WithSM2)
	if err != nil {
		t.Fatalf("GenRow(oidc): %v", err)
	}
	gwRow, err := signing.GenRow(rand.Reader, keywrap, "kid-gw", signing.PurposeGateway, signing.AlgSM3WithSM2)
	if err != nil {
		t.Fatalf("GenRow(gateway): %v", err)
	}
	set, err := signing.Load(keywrap, []signing.Row{oidcRow, gwRow})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return set
}

// issueUserToken 用真密钥签一个**用户访问令牌**（与 S6 的 claim 形状一致）。
func issueUserToken(t *testing.T, keys *signing.Set, mutate func(map[string]any)) string {
	t.Helper()
	now := time.Now().UTC()
	claims := map[string]any{
		"iss": testIssuer, "sub": testUserID, "aud": testClientID, "cid": testClientID,
		"acr": "AAL1", "amr": []string{"pwd"}, "sid": testSID,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	}
	if mutate != nil {
		mutate(claims)
	}
	key, err := keys.For(signing.PurposeOIDC)
	if err != nil {
		t.Fatalf("keys.For(oidc): %v", err)
	}
	token, err := signing.SignJWT(key, claims)
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	return token
}

// ---- 假依赖（只有"库"与"上游"是假的；SM2 验签是真的）----

type fakeSessionStatus struct {
	status string
	err    error
}

func (f *fakeSessionStatus) SessionStatus(context.Context, string) (string, error) {
	return f.status, f.err
}

type fakeSessions struct {
	snap SessionSnapshot
	err  error
	// calls 记录读取次数（用于证明"每请求现读、零缓存"，AC-SESS-01/04）。
	calls int
}

func (f *fakeSessions) LoadSession(context.Context, string) (SessionSnapshot, error) {
	f.calls++
	if f.err != nil {
		return SessionSnapshot{}, f.err
	}
	return f.snap, nil
}

type fakePolicies struct {
	policy AdmissionPolicy
	err    error
	calls  int
}

func (f *fakePolicies) LoadAdmissionPolicy(context.Context, string) (AdmissionPolicy, error) {
	f.calls++
	if f.err != nil {
		return AdmissionPolicy{}, f.err
	}
	return f.policy, nil
}

type fakeRoles struct {
	roles []string
	err   error
}

func (f *fakeRoles) RolesForUserClient(context.Context, string, string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.roles, nil
}

type fakeUsers struct {
	name string
	err  error
}

func (f *fakeUsers) Username(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.name, nil
}

type fakeVisits struct {
	mu     sync.Mutex
	record []string
	err    error
}

func (f *fakeVisits) RecordVisit(_ context.Context, userID, clientID, tier string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.record = append(f.record, userID+"|"+clientID+"|"+tier)
	return nil
}

func (f *fakeVisits) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.record...)
}

type fakeFlows struct {
	flow *FlowSnapshot
	err  error
}

func (f *fakeFlows) FindActiveFlow(context.Context, string, string) (*FlowSnapshot, error) {
	return f.flow, f.err
}

// testGuard 装一套默认可用的假依赖 + 真验签。
type testGuard struct {
	guard    *Guard
	sessions *fakeSessions
	policies *fakePolicies
	roles    *fakeRoles
	users    *fakeUsers
	visits   *fakeVisits
	flows    *fakeFlows
	keys     *signing.Set
	// factors 是"平台已装配的因子"（默认只有 password，对应 M1 的真实能力面）。
	factors []flow.FactorInfo
	// skew 是令牌时钟偏移宽容度（默认 1s；AC-DP-06 的边界打点用）。
	skew time.Duration
	// replay 是判重存储（默认内存假实现；AC-DP-08）。
	replay *memReplayGuard
}

func newTestGuard(t *testing.T, mutate func(*testGuard)) *testGuard {
	t.Helper()
	keys := newTestKeys(t)
	now := time.Now().UTC()
	tg := &testGuard{
		sessions: &fakeSessions{snap: SessionSnapshot{
			ID: testSID, UserID: testUserID, ClientID: testClientID,
			ACR: "AAL1", AMR: []string{"pwd"}, Status: SessionActive,
			ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(30 * time.Minute),
		}},
		policies: &fakePolicies{policy: AdmissionPolicy{
			ClientID: testClientID, ClientName: "计费系统", Tier: "L2", MinACR: "AAL1",
		}},
		roles:   &fakeRoles{roles: []string{}},
		users:   &fakeUsers{name: "alice"},
		visits:  &fakeVisits{},
		flows:   &fakeFlows{},
		keys:    keys,
		factors: testFactors(),
		skew:    time.Second,
		replay:  newMemReplayGuard(),
	}
	if mutate != nil {
		mutate(tg)
	}
	guard, err := NewGuard(&Guard{
		Caller: &protocol.CallerResolver{
			Keys:     keys,
			Sessions: &fakeSessionStatus{status: "active"},
			Now:      time.Now,
		},
		Sessions: tg.sessions,
		Policies: tg.policies,
		Roles:    tg.roles,
		Users:    tg.users,
		Visits:   tg.visits,
		Flows:    tg.flows,
		Factors:  tg.factors,
		Issuer:   testIssuer,
		Skew:     tg.skew,
		Now:      time.Now,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	tg.guard = guard
	return tg
}

// testFactors 是 M1 的真实能力面：**只有 password 一个因子**（单独 AAL1、组合 AAL2）。
//
// 单因子下"目标 AAL2"必然不可达（`canReach` 要求两个可用因子）——`AC-DP-09` 的
// `terminal_state` 因此是 `unreachable`，这是 M1 的**诚实**结论（AC-FLOW-07 的构造同源）。
func testFactors() []flow.FactorInfo {
	return []flow.FactorInfo{
		{Type: "password", AMR: "pwd", AsFirst: true, AsSecond: true, Sole: "AAL1", Combined: "AAL2"},
	}
}

// testFactorsTwo 是 M2 的能力面（password + otp）：目标 AAL2 **可达** → `need_more`。
// 用它证明 `details.flow` 的两种终态都能如实渲染（而不是恒为某一种）。
func testFactorsTwo() []flow.FactorInfo {
	return []flow.FactorInfo{
		{Type: "password", AMR: "pwd", AsFirst: true, AsSecond: true, Sole: "AAL1", Combined: "AAL2"},
		{Type: "otp", AMR: "otp", AsFirst: false, AsSecond: true, Sole: "AAL1", Combined: "AAL2"},
	}
}

// ---- 上游与请求 ----

type recordedUpstream struct {
	srv     *httptest.Server
	headers http.Header
	path    string
	hits    int
}

func newRecordedUpstream(t *testing.T) *recordedUpstream {
	t.Helper()
	u := &recordedUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.headers = r.Header.Clone()
		u.path = r.URL.Path
		u.hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *recordedUpstream) url(t *testing.T) *url.URL {
	t.Helper()
	parsed, err := url.Parse(u.srv.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	return parsed
}

func (u *recordedUpstream) assertionValues() []string {
	if u.headers == nil {
		return nil
	}
	// 用 EqualFold 扫：冻结名是 `X-GuanJin-Assertion`（J 大写），`Header.Values` 只会
	// 命中 canonical 形式 `X-Guanjin-Assertion`——这是 proxy.go strip 的同一条坑。
	for k, v := range u.headers {
		if strings.EqualFold(k, AssertionHeader) {
			return v
		}
	}
	return nil
}

// newTestPipeline 装出"转发内核 + 准入 + 签发"的完整链路。
func newTestPipeline(t *testing.T, tg *testGuard, upstream *recordedUpstream) *Pipeline {
	t.Helper()
	router, err := NewRouter([]Route{{
		Prefix: "/api/billing", ClientID: testClientID, Upstream: upstream.url(t),
	}})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	forward, err := New(router, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("New(forwarder): %v", err)
	}
	signer, err := NewAssertionSigner(tg.keys)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	pipeline, err := NewPipeline(forward, tg.guard, signer, tg.replay,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	return pipeline
}

func doRequest(p http.Handler, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set(BearerHeader, "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) httpx.Code {
	t.Helper()
	var body struct {
		Code httpx.Code `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return body.Code
}

// newRequest 造一个带 trace 上下文的请求（`TraceMiddleware` 之外的直接调用路径用）。
func newRequest(path string) *http.Request {
	return httptest.NewRequest(http.MethodGet, path, nil)
}

// serve 直接跑 handler（不经 `httptest.Server`，少一个真实端口）。
func serve(p http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, r)
	return rec
}

// base64RawURL 是 JWS 段用的无填充 base64url。
func base64RawURL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// lockedWriter 是并发安全的日志缓冲（Pipeline 与 Handler 都会写同一条 logger）。
type lockedWriter struct {
	mu  sync.Mutex
	buf *strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// readSource 读包内源文件（用于"结构性禁令"的单测：断言某写法不存在）。
func readSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(raw)
}

// contains 是 strings.Contains 的短名字（避免测试文件反复带包名）。
func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// memReplayGuard 是判重的**假实现**（单元测试专用：真 Redis 由 adapter 的集成用例覆盖）。
//
// 语义必须与 `gateway.ReplayGuard` 一致：首次 true、窗口内重复 false、存储故障 error。
type memReplayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
	err  error
	// calls 记录判重调用次数（用于证明"每请求都判"）。
	calls int
}

func newMemReplayGuard() *memReplayGuard {
	return &memReplayGuard{seen: map[string]time.Time{}}
}

func (m *memReplayGuard) Claim(_ context.Context, jti string, window time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return false, m.err
	}
	if _, ok := m.seen[jti]; ok {
		return false, nil
	}
	m.seen[jti] = time.Now().Add(window)
	return true, nil
}

// timeNowForTest 是一个简短别名（测试里多处要"当前时刻"，写全名太吵）。
func timeNowForTest() time.Time { return time.Now().UTC() }

// stubCaller 是 `httpx.CallerResolver` 的最小实现（只用于装配期校验的用例）。
type stubCaller struct{}

func (stubCaller) Resolve(context.Context, string) (httpx.Caller, error) {
	return httpx.Caller{}, httpx.ErrCallerUnauthenticated
}
