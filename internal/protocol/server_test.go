package protocol

import (
	"context"
	"crypto/sha256" // PKCE-S256（AC-AUD-12 唯一白名单，R5）
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/signing"
)

// ---- 测试桩（不碰 DB / Redis）----

type memStore struct {
	mu   sync.Mutex
	kv   map[string]string
	fail error
}

func newMemStore() *memStore { return &memStore{kv: map[string]string{}} }

func (m *memStore) Put(_ context.Context, key, val string, _ time.Duration) error {
	if m.fail != nil {
		return m.fail
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.kv[key] = val
	return nil
}

func (m *memStore) Take(_ context.Context, key string, gone error) (string, error) {
	if m.fail != nil {
		return "", m.fail
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.kv[key]
	if !ok {
		return "", gone
	}
	delete(m.kv, key)
	return v, nil
}

func (m *memStore) Get(_ context.Context, key string, gone error) (string, error) {
	if m.fail != nil {
		return "", m.fail
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.kv[key]
	if !ok {
		return "", gone
	}
	return v, nil
}

func (m *memStore) Delete(_ context.Context, key string) error {
	if m.fail != nil {
		return m.fail
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.kv, key)
	return nil
}

type fakeSessions struct {
	created int
	sid     string
	err     error
}

func (f *fakeSessions) Issue(_ context.Context, _ SessionSpec) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.created++
	if f.sid == "" {
		f.sid = "sid-1"
	}
	return f.sid, nil
}

type fakeClients struct {
	policy ClientPolicy
	err    error
}

func (f *fakeClients) Policy(context.Context, string) (ClientPolicy, error) {
	if f.err != nil {
		return ClientPolicy{}, f.err
	}
	return f.policy, nil
}

type recordingAudit struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (a *recordingAudit) Append(_ context.Context, ev AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
	return nil
}

func (a *recordingAudit) reasons() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.events))
	for _, e := range a.events {
		out = append(out, e.Reason)
	}
	return out
}

func testKeys(t *testing.T) *signing.Set {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 31)
	}
	mk, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	kw, err := mk.DeriveSM4(crypto.PurposeSM4Keywrap)
	if err != nil {
		t.Fatalf("DeriveSM4: %v", err)
	}
	oidc, err := signing.GenRow(nil, kw, "oidc-v1", signing.PurposeOIDC, signing.AlgSM3WithSM2)
	if err != nil {
		t.Fatalf("GenRow: %v", err)
	}
	gw, err := signing.GenRow(nil, kw, "gw-v1", signing.PurposeGateway, signing.AlgSM2)
	if err != nil {
		t.Fatalf("GenRow: %v", err)
	}
	set, err := signing.Load(kw, []signing.Row{oidc, gw})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return set
}

func newTestServer(t *testing.T, mutate func(*Deps)) (*Server, *httptest.Server, *memStore, *fakeSessions, *recordingAudit) {
	t.Helper()
	store := newMemStore()
	sessions := &fakeSessions{}
	audit := &recordingAudit{}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	d := Deps{
		Keys:     testKeys(t),
		Codes:    store,
		Sessions: sessions,
		Clients: &fakeClients{policy: ClientPolicy{
			ACR: "AAL1", AccessTTL: 10 * time.Minute, RefreshTTL: 8 * time.Hour,
			IdleTimeout: 30 * time.Minute, AbsTimeout: 12 * time.Hour,
		}},
		Audit:  audit,
		Issuer: "https://auth.example.com",
		Logger: logger,
		Now:    func() time.Time { return time.Unix(1_760_000_000, 0).UTC() },
	}
	if mutate != nil {
		mutate(&d)
	}
	srv, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	srv.Routes(mux)
	ts := httptest.NewServer(httpx.TraceMiddleware(mux, "X-Trace-Id", logger))
	t.Cleanup(ts.Close)
	return srv, ts, store, sessions, audit
}

func postForm(t *testing.T, u string, kv map[string]string) (*http.Response, map[string]any, httpx.ErrorResponse) {
	t.Helper()
	form := url.Values{}
	for k, v := range kv {
		form.Set(k, v)
	}
	resp, err := http.Post(u, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	if resp.StatusCode >= 400 {
		var env httpx.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			t.Fatalf("错误响应不是四字段：%v", err)
		}
		return resp, nil, env
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("解码：%v", err)
	}
	return resp, body, httpx.ErrorResponse{}
}

// 授权码换令牌：一次性消费 → access/id/refresh 三件 + 建一条会话（sid 进 id_token）。
func TestTokenExchangeIssuesTokensAndSession(t *testing.T) {
	_, ts, store, sessions, audit := newTestServer(t, nil)
	payload, _ := json.Marshal(codePayload{Sub: "u-1", ClientID: "c-1", ACR: "AAL1", AMR: []string{"pwd"}})
	if err := store.Put(context.Background(), CodeKey("code-1"), string(payload), time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}

	resp, body, _ := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "code-1", "client_id": "c-1",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应为 200，实际 %d", resp.StatusCode)
	}
	for _, k := range []string{"access_token", "id_token", "refresh_token"} {
		if body[k] == nil || body[k] == "" {
			t.Fatalf("缺少 %s：%v", k, body)
		}
	}
	if body["token_type"] != "Bearer" {
		t.Fatalf("token_type 应为 Bearer，实际 %v", body["token_type"])
	}
	if sessions.created != 1 || sessions.sid != "sid-1" {
		t.Fatalf("应建一条会话，实际 created=%d sid=%s", sessions.created, sessions.sid)
	}
	if got := audit.reasons(); len(got) == 0 || got[len(got)-1] != "code_consumed" {
		t.Fatalf("消费授权码应写一条审计事件，实际 %v", got)
	}

	// 码是一次性的：第二次换必须 `INVALID_GRANT`。
	resp2, _, env := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "code-1", "client_id": "c-1",
	})
	if resp2.StatusCode != http.StatusUnauthorized || env.Code != httpx.CodeInvalidGrant {
		t.Fatalf("二次换码应 401 INVALID_GRANT，实际 %d / %s", resp2.StatusCode, env.Code)
	}
}

// PKCE：缺/畸形 verifier → INVALID_CODE_VERIFIER(400)；不匹配 → PKCE_VERIFICATION_FAILED(401)。
func TestTokenPKCEDualCodes(t *testing.T) {
	_, ts, store, _, _ := newTestServer(t, nil)
	challenge := pkceChallenge("correct-verifier-0123456789012345678901234567890123")
	payload, _ := json.Marshal(codePayload{Sub: "u-1", ClientID: "c-1", ACR: "AAL1", AMR: []string{"pwd"}, Challenge: challenge})
	put := func(code string) {
		if err := store.Put(context.Background(), CodeKey(code), string(payload), time.Minute); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	put("code-a")
	resp, _, env := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "code-a", "client_id": "c-1",
	})
	if resp.StatusCode != http.StatusBadRequest || env.Code != httpx.CodeInvalidCodeVerifier {
		t.Fatalf("缺 verifier 应 400 INVALID_CODE_VERIFIER，实际 %d / %s", resp.StatusCode, env.Code)
	}

	put("code-b")
	resp, _, env = postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "code-b", "client_id": "c-1",
		"code_verifier": "wrong-verifier-0123456789012345678901234567890123",
	})
	if resp.StatusCode != http.StatusUnauthorized || env.Code != httpx.CodePKCEFailed {
		t.Fatalf("verifier 不匹配应 401 PKCE_VERIFICATION_FAILED，实际 %d / %s", resp.StatusCode, env.Code)
	}
}

// 刷新轮换：旧 rt 一次有效；再用 → REFRESH_TOKEN_REUSED（并留审计）。
func TestRefreshRotationDetectsReuse(t *testing.T) {
	_, ts, store, _, audit := newTestServer(t, nil)
	payload, _ := json.Marshal(codePayload{Sub: "u-1", ClientID: "c-1", ACR: "AAL1", AMR: []string{"pwd"}})
	if err := store.Put(context.Background(), CodeKey("code-2"), string(payload), time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, first, _ := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "code-2", "client_id": "c-1",
	})
	rt := first["refresh_token"].(string)

	_, second, _ := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "refresh_token", "refresh_token": rt,
	})
	if second["refresh_token"] == nil || second["refresh_token"].(string) == rt {
		t.Fatal("刷新必须轮换出新 refresh_token")
	}

	resp, _, env := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "refresh_token", "refresh_token": rt,
	})
	if resp.StatusCode != http.StatusUnauthorized || env.Code != httpx.CodeRefreshTokenReused {
		t.Fatalf("复用旧 rt 应 401 REFRESH_TOKEN_REUSED，实际 %d / %s", resp.StatusCode, env.Code)
	}
	found := false
	for _, r := range audit.reasons() {
		if r == "refresh_reused" {
			found = true
		}
	}
	if !found {
		t.Fatalf("复用应留审计事件，实际 %v", audit.reasons())
	}
}

// /userinfo 与 /introspect 本地解析：不查库（依赖里也没有 DB），只验签与 exp。
func TestUserinfoAndIntrospectAreLocal(t *testing.T) {
	_, ts, store, _, _ := newTestServer(t, nil)
	payload, _ := json.Marshal(codePayload{Sub: "u-9", ClientID: "c-1", ACR: "AAL1", AMR: []string{"pwd"}})
	if err := store.Put(context.Background(), CodeKey("code-3"), string(payload), time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, body, _ := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "code-3", "client_id": "c-1",
	})
	access := body["access_token"].(string)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /userinfo: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/userinfo 应为 200，实际 %d", resp.StatusCode)
	}
	var claims map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&claims)
	if claims["sub"] != "u-9" || claims["sid"] != "sid-1" || claims["acr"] != "AAL1" {
		t.Fatalf("userinfo 字段不符：%v", claims)
	}

	_, ins, _ := postForm(t, ts.URL+"/oauth/introspect", map[string]string{"token": access})
	if ins["active"] != true || ins["sub"] != "u-9" {
		t.Fatalf("introspect 应 active=true 且带 sub，实际 %v", ins)
	}
	_, ins2, _ := postForm(t, ts.URL+"/oauth/introspect", map[string]string{"token": access + "x"})
	if ins2["active"] != false {
		t.Fatalf("篡改令牌应 active=false，实际 %v", ins2)
	}
}

// discovery 与 JWKS：声明与签发一致（alg/kid/两用途）。
func TestDiscoveryAndJWKS(t *testing.T) {
	_, ts, _, _, _ := newTestServer(t, nil)
	resp, err := http.Get(ts.URL + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("GET discovery: %v", err)
	}
	var doc map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	if doc["issuer"] != "https://auth.example.com" {
		t.Fatalf("issuer 不符：%v", doc["issuer"])
	}
	algs, _ := doc["id_token_signing_alg_values_supported"].([]any)
	if len(algs) != 1 || algs[0] != "SM3withSM2" {
		t.Fatalf("alg 声明应只 SM3withSM2，实际 %v", algs)
	}

	resp2, err := http.Get(ts.URL + "/.well-known/jwks.json")
	if err != nil {
		t.Fatalf("GET jwks: %v", err)
	}
	var jwks struct {
		Keys []map[string]any `json:"keys"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&jwks)
	if len(jwks.Keys) != 2 {
		t.Fatalf("JWKS 应发布两把密钥，实际 %d", len(jwks.Keys))
	}
}

// 存储不可用 → fail-closed（503 REDIS_UNAVAILABLE），不签发任何令牌。
func TestStoreUnavailableFailsClosed(t *testing.T) {
	_, ts, store, _, _ := newTestServer(t, nil)
	store.fail = errors.Join(ErrStoreUnavailable, errors.New("redis down"))
	payload, _ := json.Marshal(codePayload{Sub: "u-1", ClientID: "c-1", ACR: "AAL1"})
	store.kv[CodeKey("code-x")] = string(payload)

	resp, _, env := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "code-x", "client_id": "c-1",
	})
	if resp.StatusCode != http.StatusServiceUnavailable || env.Code != httpx.CodeRedisUnavailable {
		t.Fatalf("存储不可用应 503 REDIS_UNAVAILABLE，实际 %d / %s", resp.StatusCode, env.Code)
	}
}

// 装配守卫：缺依赖即失败。
func TestNewRequiresDeps(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("缺依赖必须报错")
	}
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier)) // PKCE-S256（AC-AUD-12 唯一白名单，R5）
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// 错误分支覆盖（这些分支是用户真正会撞到的：缺参、错客户端、存储/策略故障）。
func TestTokenAndStoreErrorBranches(t *testing.T) {
	_, ts, store, sessions, _ := newTestServer(t, nil)
	ctx := context.Background()

	// ① 缺 code / client_id → MISSING_PARAMETER(400)
	resp, _, env := postForm(t, ts.URL+"/oauth/token", map[string]string{"grant_type": "authorization_code"})
	if resp.StatusCode != http.StatusBadRequest || env.Code != httpx.CodeMissingParameter {
		t.Fatalf("缺参应 400 MISSING_PARAMETER，实际 %d / %s", resp.StatusCode, env.Code)
	}

	// ② 未知 grant_type → INVALID_REQUEST(400)
	resp, _, env = postForm(t, ts.URL+"/oauth/token", map[string]string{"grant_type": "password"})
	if resp.StatusCode != http.StatusBadRequest || env.Code != httpx.CodeInvalidRequest {
		t.Fatalf("未知 grant 应 400 INVALID_REQUEST，实际 %d / %s", resp.StatusCode, env.Code)
	}

	// ③ 码内容损坏 → INVALID_GRANT
	_ = store.Put(ctx, CodeKey("bad"), "not-json", time.Minute)
	resp, _, env = postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "bad", "client_id": "c-1",
	})
	if resp.StatusCode != http.StatusUnauthorized || env.Code != httpx.CodeInvalidGrant {
		t.Fatalf("坏码应 401 INVALID_GRANT，实际 %d / %s", resp.StatusCode, env.Code)
	}

	// ④ 码不属于该客户端 → INVALID_GRANT
	payload, _ := json.Marshal(codePayload{Sub: "u-1", ClientID: "c-other", ACR: "AAL1"})
	_ = store.Put(ctx, CodeKey("other"), string(payload), time.Minute)
	resp, _, env = postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "other", "client_id": "c-1",
	})
	if resp.StatusCode != http.StatusUnauthorized || env.Code != httpx.CodeInvalidGrant {
		t.Fatalf("跨客户端用码应 401 INVALID_GRANT，实际 %d / %s", resp.StatusCode, env.Code)
	}

	// ⑤ 会话创建失败 → 500（不签发半套令牌）
	payload, _ = json.Marshal(codePayload{Sub: "u-1", ClientID: "c-1", ACR: "AAL1"})
	_ = store.Put(ctx, CodeKey("sess-fail"), string(payload), time.Minute)
	sessions.err = errors.New("db down")
	resp, _, env = postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "sess-fail", "client_id": "c-1",
	})
	if resp.StatusCode != http.StatusInternalServerError || env.Code != httpx.CodeInternalError {
		t.Fatalf("会话失败应 500，实际 %d / %s", resp.StatusCode, env.Code)
	}
	sessions.err = nil

	// ⑥ 客户端不存在/停用 → INVALID_CLIENT(400)
	_ = store.Put(ctx, CodeKey("bad-client"), string(payload), time.Minute)
	badClients := &fakeClients{err: errors.New("no policy")}
	srv2, ts2, store2, _, _ := newTestServer(t, func(d *Deps) { d.Clients = badClients })
	_ = srv2
	_ = store2.Put(ctx, CodeKey("bc"), string(payload), time.Minute)
	resp, _, env = postForm(t, ts2.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "bc", "client_id": "c-1",
	})
	if resp.StatusCode != http.StatusBadRequest || env.Code != httpx.CodeInvalidClient {
		t.Fatalf("客户端不可用应 400 INVALID_CLIENT，实际 %d / %s", resp.StatusCode, env.Code)
	}
}

// revoke / userinfo / introspect 的边界。
func TestRevokeAndUserinfoBranches(t *testing.T) {
	_, ts, store, _, audit := newTestServer(t, nil)

	// 缺 token → 400
	resp, _, env := postForm(t, ts.URL+"/oauth/revoke", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest || env.Code != httpx.CodeMissingParameter {
		t.Fatalf("缺 token 应 400，实际 %d / %s", resp.StatusCode, env.Code)
	}
	// 正常撤销（幂等：删不存在的键也 200）
	resp, _, _ = postForm(t, ts.URL+"/oauth/revoke", map[string]string{"token": "rt-x"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("撤销应 200，实际 %d", resp.StatusCode)
	}
	found := false
	for _, r := range audit.reasons() {
		if r == "token_revoked" {
			found = true
		}
	}
	if !found {
		t.Fatalf("撤销应留审计事件，实际 %v", audit.reasons())
	}

	// 存储故障 → 503
	store.fail = errors.Join(ErrStoreUnavailable, errors.New("redis down"))
	resp, _, env = postForm(t, ts.URL+"/oauth/revoke", map[string]string{"token": "rt-y"})
	if resp.StatusCode != http.StatusServiceUnavailable || env.Code != httpx.CodeRedisUnavailable {
		t.Fatalf("存储故障应 503，实际 %d / %s", resp.StatusCode, env.Code)
	}
	store.fail = nil

	// userinfo：缺 Bearer → MISSING_PARAMETER；坏令牌 → TOKEN_INVALID
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/userinfo", nil)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 Bearer 应 400，实际 %d", resp2.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/userinfo", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	resp3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("坏令牌应 401，实际 %d", resp3.StatusCode)
	}
}

// 缺 OIDC 签名密钥 → 503 KEY_UNAVAILABLE（不签发任何令牌，也不 panic）。
func TestMissingSigningKeyFailsClosed(t *testing.T) {
	_, ts, store, _, _ := newTestServer(t, func(d *Deps) {
		d.Keys = &signing.Set{} // 空集合：没有任何用途可用
	})
	payload, _ := json.Marshal(codePayload{Sub: "u-1", ClientID: "c-1", ACR: "AAL1"})
	_ = store.Put(context.Background(), CodeKey("k1"), string(payload), time.Minute)
	resp, _, env := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "k1", "client_id": "c-1",
	})
	if resp.StatusCode != http.StatusServiceUnavailable || env.Code != httpx.CodeKeyUnavailable {
		t.Fatalf("缺签名键应 503 KEY_UNAVAILABLE，实际 %d / %s", resp.StatusCode, env.Code)
	}
}

// 审计未配置时只降级为日志（并明确标记），不阻断签发。
func TestAuditSinkOptional(t *testing.T) {
	_, ts, store, _, _ := newTestServer(t, func(d *Deps) { d.Audit = nil })
	payload, _ := json.Marshal(codePayload{Sub: "u-1", ClientID: "c-1", ACR: "AAL1"})
	_ = store.Put(context.Background(), CodeKey("no-audit"), string(payload), time.Minute)
	resp, body, _ := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "no-audit", "client_id": "c-1",
	})
	if resp.StatusCode != http.StatusOK || body["access_token"] == nil {
		t.Fatalf("无审计实现也应签发，实际 %d", resp.StatusCode)
	}
}

// ---- 授权端点（/authorize + /authorize/submit）----

type fakeFlows struct {
	flowID    string
	err       error
	results   []FlowResult
	verifyErr error
}

func (f *fakeFlows) Begin(context.Context, string, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.flowID == "" {
		f.flowID = "flow-1"
	}
	return f.flowID, nil
}

func (f *fakeFlows) Verify(context.Context, string, string, string, string, string) (FlowResult, error) {
	if f.verifyErr != nil {
		return FlowResult{}, f.verifyErr
	}
	if len(f.results) == 0 {
		return FlowResult{Satisfied: false}, nil
	}
	res := f.results[0]
	f.results = f.results[1:]
	return res, nil
}

type fakeRedirects struct {
	uris map[string][]string
}

func (f *fakeRedirects) RedirectURIs(_ context.Context, clientID string) ([]string, error) {
	return f.uris[clientID], nil
}

func authorizeServer(t *testing.T, flows *fakeFlows, redirects *fakeRedirects) (*httptest.Server, *memStore) {
	t.Helper()
	srv, ts, store, _, _ := newTestServer(t, func(d *Deps) {
		d.Flows = flows
		d.FlowVerify = flows
		d.Redirects = redirects
		d.FlowCode = func(error) (httpx.Code, bool) { return "", false }
		d.FlowTTL = 10 * time.Minute
	})
	_ = srv
	return ts, store
}

// /authorize：校验通过 → 302 到登录页并落上下文；缺 PKCE / 未登记回调 → 400。
func TestAuthorizeRedirectsWithContext(t *testing.T) {
	flows := &fakeFlows{}
	redirects := &fakeRedirects{uris: map[string][]string{"c-1": {"https://rp.example.com/cb"}}}
	ts, store := authorizeServer(t, flows, redirects)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	u := ts.URL + "/authorize?response_type=code&client_id=c-1&redirect_uri=https%3A%2F%2Frp.example.com%2Fcb&code_challenge=abc&code_challenge_method=S256&state=st-1&nonce=n-1"
	resp, err := client.Get(u)
	if err != nil {
		t.Fatalf("GET /authorize: %v", err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("应为 302，实际 %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "flow_id=flow-1") {
		t.Fatalf("Location 应带 flow_id，实际 %q", loc)
	}
	if _, ok := store.kv[FlowKey("flow-1")]; !ok {
		t.Fatal("授权上下文应落 Redis")
	}

	// 缺 code_challenge → INVALID_CODE_VERIFIER(400)
	resp2, _ := client.Get(ts.URL + "/authorize?response_type=code&client_id=c-1&redirect_uri=https%3A%2F%2Frp.example.com%2Fcb")
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 PKCE 应 400，实际 %d", resp2.StatusCode)
	}

	// 未登记的 redirect_uri → 400
	resp3, _ := client.Get(ts.URL + "/authorize?response_type=code&client_id=c-1&redirect_uri=https%3A%2F%2Fevil.example.com%2Fcb&code_challenge=abc&code_challenge_method=S256")
	if resp3.StatusCode != http.StatusBadRequest {
		t.Fatalf("未登记回调应 400，实际 %d", resp3.StatusCode)
	}
}

// /authorize/submit：未达标回 need_more；达标发码（一次性）并回 redirect_uri/state。
func TestAuthorizeSubmitIssuesCode(t *testing.T) {
	flows := &fakeFlows{results: []FlowResult{{Satisfied: false}, {Satisfied: true, UserID: "u-1", ACR: "AAL1", AMR: []string{"pwd"}}}}
	redirects := &fakeRedirects{uris: map[string][]string{"c-1": {"https://rp.example.com/cb"}}}
	ts, store := authorizeServer(t, flows, redirects)

	// ① 未达标的应答 → need_more（上下文必须先存在）
	body := `{"flow_id":"flow-1","answers":[{"factor":"password","response":"pw"}]}`
	_ = store.Put(context.Background(), FlowKey("flow-1"),
		`{"cid":"c-1","rd":"https://rp.example.com/cb","st":"st-9","ch":"abc"}`, time.Minute)
	resp, err := http.Post(ts.URL+"/authorize/submit", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST submit: %v", err)
	}
	var needMore map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&needMore)
	if needMore["status"] != "need_more" {
		t.Fatalf("应为 need_more，实际 %v", needMore)
	}

	// ② 达标 → completed + code；上下文被删
	_ = store.Put(context.Background(), FlowKey("flow-1"),
		`{"cid":"c-1","rd":"https://rp.example.com/cb","st":"st-9"}`, time.Minute)
	resp2, err := http.Post(ts.URL+"/authorize/submit", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST submit: %v", err)
	}
	var done map[string]any
	_ = json.NewDecoder(resp2.Body).Decode(&done)
	if done["status"] != "completed" || done["code"] == nil || done["state"] != "st-9" {
		t.Fatalf("应 completed 且带 code/state，实际 %v", done)
	}
	if _, ok := store.kv[FlowKey("flow-1")]; ok {
		t.Fatal("发码后授权上下文必须删除（一次性）")
	}
	// 码可用：换令牌成功
	_, tok, _ := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": done["code"].(string), "client_id": "c-1",
		"redirect_uri": "https://rp.example.com/cb",
	})
	if tok["id_token"] == nil {
		t.Fatalf("用授权码应换到 id_token，实际 %v", tok)
	}
}

// 上下文不存在 / 缺参 → 明确错误码（不是 500）。
func TestAuthorizeSubmitGuards(t *testing.T) {
	flows := &fakeFlows{}
	redirects := &fakeRedirects{uris: map[string][]string{"c-1": {"https://rp.example.com/cb"}}}
	ts, _ := authorizeServer(t, flows, redirects)

	resp, err0 := http.Post(ts.URL+"/authorize/submit", "application/json", strings.NewReader(`{}`))
	if err0 != nil {
		t.Fatalf("POST: %v", err0)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺参应 400，实际 %d", resp.StatusCode)
	}
	var envBody httpx.ErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&envBody)
	if envBody.Code != httpx.CodeMissingParameter {
		t.Fatalf("缺参应 MISSING_PARAMETER，实际 %s", envBody.Code)
	}
	resp2, err := http.Post(ts.URL+"/authorize/submit", "application/json", strings.NewReader(`{"flow_id":"nope","answers":[{"factor":"password"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("未知 flow 应 404，实际 %d", resp2.StatusCode)
	}
}

// 授权端点其余失败分支：缺 client_id / 未知客户端 / 未装配流 / 校验失败。
func TestAuthorizeErrorBranches(t *testing.T) {
	flows := &fakeFlows{}
	redirects := &fakeRedirects{uris: map[string][]string{"c-1": {"https://rp.example.com/cb"}}}
	ts, _ := authorizeServer(t, flows, redirects)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// 缺 client_id → 400
	resp, _ := client.Get(ts.URL + "/authorize?response_type=code")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 client_id 应 400，实际 %d", resp.StatusCode)
	}
	// response_type 不是 code → 400
	resp, _ = client.Get(ts.URL + "/authorize?response_type=token&client_id=c-1")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("response_type 非 code 应 400，实际 %d", resp.StatusCode)
	}
	// 开流失败 → 500（不是 302）
	flowsErr := &fakeFlows{err: errors.New("boom")}
	tsErr, _ := authorizeServer(t, flowsErr, redirects)
	resp, _ = client.Get(tsErr.URL + "/authorize?response_type=code&client_id=c-1&redirect_uri=https%3A%2F%2Frp.example.com%2Fcb&code_challenge=abc&code_challenge_method=S256")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("开流失败应 500，实际 %d", resp.StatusCode)
	}
	// 未装配流（Flows=nil）→ 500
	tsNoFlow, _ := authorizeServer(t, nil, redirects)
	_ = tsNoFlow
}

// 校验应答失败（编排层报错）→ 用注入的映射函数给码，而不是一律 500。
func TestAuthorizeSubmitVerifyErrorMapped(t *testing.T) {
	flows := &fakeFlows{verifyErr: errors.New("step already satisfied")}
	redirects := &fakeRedirects{uris: map[string][]string{"c-1": {"https://rp.example.com/cb"}}}
	ts, store := authorizeServer(t, flows, redirects)
	_ = store.Put(context.Background(), FlowKey("flow-1"), `{"cid":"c-1","rd":"https://rp.example.com/cb"}`, time.Minute)

	resp, err := http.Post(ts.URL+"/authorize/submit", "application/json",
		strings.NewReader(`{"flow_id":"flow-1","answers":[{"factor":"password","response":"pw"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	// 映射函数返回 false → 落 INTERNAL_ERROR（500），但必须是统一四字段而不是 panic。
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("未识别错误应 500，实际 %d", resp.StatusCode)
	}
}

// 达标但缺用户 → 500（不签发无 sub 的码）。
func TestAuthorizeSubmitRequiresUser(t *testing.T) {
	flows := &fakeFlows{results: []FlowResult{{Satisfied: true, ACR: "AAL1"}}}
	redirects := &fakeRedirects{uris: map[string][]string{"c-1": {"https://rp.example.com/cb"}}}
	ts, store := authorizeServer(t, flows, redirects)
	_ = store.Put(context.Background(), FlowKey("flow-1"), `{"cid":"c-1","rd":"https://rp.example.com/cb"}`, time.Minute)
	resp, err := http.Post(ts.URL+"/authorize/submit", "application/json",
		strings.NewReader(`{"flow_id":"flow-1","answers":[{"factor":"password","response":"pw"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("达标缺用户应 500，实际 %d", resp.StatusCode)
	}
}

// ---- CallerResolver（③）：验签 → exp/nbf → 会话状态 ----

type fakeSessionState struct {
	status string
	err    error
}

func (f *fakeSessionState) SessionStatus(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.status, nil
}

func mintToken(t *testing.T, keys *signing.Set, claims map[string]any) string {
	t.Helper()
	key, err := keys.For(signing.PurposeOIDC)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	tok, err := signing.SignJWT(key, claims)
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	return tok
}

func TestCallerResolver(t *testing.T) {
	keys := testKeys(t)
	now := time.Unix(1_760_000_000, 0).UTC()
	base := map[string]any{"iss": "https://auth.example.com", "sub": "u-1", "sid": "sid-1", "exp": now.Add(time.Minute).Unix()}

	// 正常：active 会话 → Caller 带 sub/sid/roles
	r := &CallerResolver{Keys: keys, Sessions: &fakeSessionState{status: "active"}, Now: func() time.Time { return now }}
	tok := mintToken(t, keys, map[string]any{"iss": base["iss"], "sub": "u-1", "sid": "sid-1",
		"exp": now.Add(time.Minute).Unix(), "roles": []string{"user"}})
	caller, err := r.Resolve(context.Background(), tok)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if caller.Sub != "u-1" || caller.SID != "sid-1" || len(caller.Roles) != 1 || caller.Roles[0] != "user" {
		t.Fatalf("Caller 不符：%+v", caller)
	}

	// 会话已撤销 → ErrCallerSessionRevoked（不是"令牌无效"）
	rRevoked := &CallerResolver{Keys: keys, Sessions: &fakeSessionState{status: "revoked"}, Now: func() time.Time { return now }}
	if _, err := rRevoked.Resolve(context.Background(), tok); !errors.Is(err, httpx.ErrCallerSessionRevoked) {
		t.Fatalf("撤销会话应报 ErrCallerSessionRevoked，实际 %v", err)
	}

	// 会话不存在 → 也按已撤销处理（不区分，避免枚举）
	rGone := &CallerResolver{Keys: keys, Sessions: &fakeSessionState{err: ErrSessionGone}, Now: func() time.Time { return now }}
	if _, err := rGone.Resolve(context.Background(), tok); !errors.Is(err, httpx.ErrCallerSessionRevoked) {
		t.Fatalf("会话不存在应按已撤销处理，实际 %v", err)
	}

	// 过期 → ErrCallerUnauthenticated
	expired := mintToken(t, keys, map[string]any{"sub": "u-1", "sid": "sid-1", "exp": now.Add(-time.Second).Unix()})
	if _, err := r.Resolve(context.Background(), expired); !errors.Is(err, httpx.ErrCallerUnauthenticated) {
		t.Fatalf("过期令牌应报 ErrCallerUnauthenticated，实际 %v", err)
	}

	// nbf 在未来 → 拒（允许 skew 内）
	nbfFuture := mintToken(t, keys, map[string]any{"sub": "u-1", "exp": now.Add(time.Minute).Unix(), "nbf": now.Add(30 * time.Second).Unix()})
	if _, err := r.Resolve(context.Background(), nbfFuture); !errors.Is(err, httpx.ErrCallerUnauthenticated) {
		t.Fatalf("nbf 未到应拒，实际 %v", err)
	}

	// 篡改/空令牌 → ErrCallerUnauthenticated
	if _, err := r.Resolve(context.Background(), tok+"x"); !errors.Is(err, httpx.ErrCallerUnauthenticated) {
		t.Fatalf("篡改令牌应拒，实际 %v", err)
	}
	if _, err := r.Resolve(context.Background(), ""); !errors.Is(err, httpx.ErrCallerUnauthenticated) {
		t.Fatalf("空令牌应拒，实际 %v", err)
	}
}

// ① 换码时必须校验 redirect_uri：不一致 → INVALID_GRANT，且**不得**记成 code_consumed。
func TestTokenRejectsRedirectMismatch(t *testing.T) {
	_, ts, store, sessions, audit := newTestServer(t, nil)
	payload, _ := json.Marshal(codePayload{Sub: "u-1", ClientID: "c-1", ACR: "AAL1",
		Redirect: "https://rp.example.com/cb"})
	_ = store.Put(context.Background(), CodeKey("code-rd"), string(payload), time.Minute)

	resp, _, env := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "code-rd", "client_id": "c-1",
		"redirect_uri": "https://evil.example.com/cb",
	})
	if resp.StatusCode != http.StatusUnauthorized || env.Code != httpx.CodeInvalidGrant {
		t.Fatalf("回调不一致应 401 INVALID_GRANT，实际 %d / %s", resp.StatusCode, env.Code)
	}
	if sessions.created != 0 {
		t.Fatalf("拒绝路径不得建会话，实际 %d", sessions.created)
	}
	for _, r := range audit.reasons() {
		if r == "code_consumed" {
			t.Fatalf("拒绝路径不得记 code_consumed，实际 %v", audit.reasons())
		}
	}
	found := false
	for _, r := range audit.reasons() {
		if r == "redirect_mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应记 redirect_mismatch 审计，实际 %v", audit.reasons())
	}
}

// ② PKCE 失败必须记 failure/pkce_failed，且**不得**出现 success。
func TestPKCEFailureAuditsFailure(t *testing.T) {
	_, ts, store, _, audit := newTestServer(t, nil)
	challenge := pkceChallenge("correct-verifier-0123456789012345678901234567890123")
	payload, _ := json.Marshal(codePayload{Sub: "u-1", ClientID: "c-1", ACR: "AAL1", Challenge: challenge})
	_ = store.Put(context.Background(), CodeKey("code-pkce"), string(payload), time.Minute)

	resp, _, env := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "code-pkce", "client_id": "c-1",
		"code_verifier": "wrong-verifier-0123456789012345678901234567890123",
	})
	if resp.StatusCode != http.StatusUnauthorized || env.Code != httpx.CodePKCEFailed {
		t.Fatalf("PKCE 不匹配应 401，实际 %d / %s", resp.StatusCode, env.Code)
	}
	reasons := audit.reasons()
	if len(reasons) != 1 || reasons[0] != "pkce_failed" {
		t.Fatalf("应只记一条 pkce_failed，实际 %v", reasons)
	}
	for _, ev := range audit.events {
		if ev.Result == "success" {
			t.Fatalf("PKCE 失败不得出现 success 审计：%+v", ev)
		}
	}
}

// 裁定 1：回调不一致 → 码作废；补一次正确回调也换不到令牌。
func TestRedirectMismatchBurnsCode(t *testing.T) {
	_, ts, store, _, _ := newTestServer(t, nil)
	payload, _ := json.Marshal(codePayload{Sub: "u-1", ClientID: "c-1", ACR: "AAL1",
		Redirect: "https://rp.example.com/cb"})
	_ = store.Put(context.Background(), CodeKey("code-burn"), string(payload), time.Minute)

	// 第一次：错误回调 → 401（码已被 GETDEL 取出，等于作废）
	resp, _, env := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "code-burn", "client_id": "c-1",
		"redirect_uri": "https://evil.example.com/cb",
	})
	if resp.StatusCode != http.StatusUnauthorized || env.Code != httpx.CodeInvalidGrant {
		t.Fatalf("错误回调应 401，实际 %d / %s", resp.StatusCode, env.Code)
	}
	// 第二次：正确回调 → **仍失败**（钉住"码不可再用"）
	resp2, _, env2 := postForm(t, ts.URL+"/oauth/token", map[string]string{
		"grant_type": "authorization_code", "code": "code-burn", "client_id": "c-1",
		"redirect_uri": "https://rp.example.com/cb",
	})
	if resp2.StatusCode != http.StatusUnauthorized || env2.Code != httpx.CodeInvalidGrant {
		t.Fatalf("作废后的码再用应仍失败，实际 %d / %s", resp2.StatusCode, env2.Code)
	}
}

// 裁定 2：ResolveCaller 的三种失败（无令牌 / 坏令牌 → 401 TOKEN_INVALID；
// 会话已撤销 → **403 SESSION_REVOKED**，按 D3-E 的"凭证状态 vs 准入状态"分工）。
func TestResolveCallerHTTPSemantics(t *testing.T) {
	keys := testKeys(t)
	now := time.Unix(1_760_000_000, 0).UTC()
	sessions := &fakeSessionState{status: "active"}
	resolver := &CallerResolver{Keys: keys, Sessions: sessions, Now: func() time.Time { return now }}

	srv, err := New(Deps{
		Keys: keys, Codes: newMemStore(), Sessions: &fakeSessions{},
		Clients: &fakeClients{policy: ClientPolicy{ACR: "AAL1", AccessTTL: time.Minute, RefreshTTL: time.Hour}},
		Issuer:  "https://auth.example.com",
		Logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)), Now: func() time.Time { return now },
		Caller: resolver,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_caller", func(w http.ResponseWriter, r *http.Request) {
		caller, ok := srv.ResolveCaller(w, r)
		if !ok {
			return
		}
		writeJSON(w, srv.d.Logger, http.StatusOK, map[string]any{"sub": caller.Sub})
	})
	ts := httptest.NewServer(httpx.TraceMiddleware(mux, "X-Trace-Id", srv.d.Logger))
	t.Cleanup(ts.Close)

	do := func(hdr string) (*http.Response, httpx.ErrorResponse) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/_caller", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		var env httpx.ErrorResponse
		if resp.StatusCode >= 400 {
			_ = json.NewDecoder(resp.Body).Decode(&env)
		}
		return resp, env
	}

	// ① 无令牌 → 401 TOKEN_INVALID
	resp, env := do("")
	if resp.StatusCode != http.StatusUnauthorized || env.Code != httpx.CodeTokenInvalid {
		t.Fatalf("无令牌应 401 TOKEN_INVALID，实际 %d / %s", resp.StatusCode, env.Code)
	}
	// ② 坏令牌 → 401 TOKEN_INVALID，且不泄露内部原因
	resp, env = do("Bearer garbage")
	if resp.StatusCode != http.StatusUnauthorized || env.Code != httpx.CodeTokenInvalid {
		t.Fatalf("坏令牌应 401，实际 %d / %s", resp.StatusCode, env.Code)
	}
	if env.Message == "" || strings.Contains(env.Message, "signature") || strings.Contains(env.Message, "sm2") {
		t.Fatalf("对外文案不得泄露内部细节：%q", env.Message)
	}
	// ③ 正常令牌 → 200
	tok := mintToken(t, keys, map[string]any{"sub": "u-1", "sid": "sid-1", "exp": now.Add(time.Minute).Unix()})
	resp, _ = do("Bearer " + tok)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("正常令牌应 200，实际 %d", resp.StatusCode)
	}
	// ④ 会话撤销 → 403 SESSION_REVOKED（D3-E）
	sessions.status = "revoked"
	resp, env = do("Bearer " + tok)
	if resp.StatusCode != http.StatusForbidden || env.Code != httpx.CodeSessionRevoked {
		t.Fatalf("会话撤销应 403 SESSION_REVOKED，实际 %d / %s", resp.StatusCode, env.Code)
	}
}
