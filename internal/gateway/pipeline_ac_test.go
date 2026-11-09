package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tjfoc/gmsm/sm2"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/signing"
)

// 本文件是**断言签发与剥离**的端到端单测（AC-DP-01/02/03/10/13）。
//
// 上游是真的 `httptest.Server`，断言由**真 SM2 密钥**签，验签走 **JWKS → 公钥点 → VerifyJWT**，
// 也就是"上游业务系统按 SDK 的方式验一遍"——不是拿内部结构自证。

// assertionClaims 解出断言并**用 JWKS 里的公钥真验签**（AC-DP-01 的核心动作）。
func assertionClaims(t *testing.T, keys *signing.Set, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("断言不是三段式：%q", token)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if err := json.Unmarshal(hb, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header.Alg != signing.AlgSM3WithSM2 {
		t.Fatalf("alg 期望 %s，实际 %q", signing.AlgSM3WithSM2, header.Alg)
	}
	if header.Kid == "" {
		t.Fatal("断言必须带 kid（SDK 靠它选公钥）")
	}

	// JWKS → 对应 kid 的公钥点（与业务系统 SDK 的取法一致）。
	var jwk signing.JWK
	found := false
	for _, candidate := range keys.JWKS() {
		if candidate.Kid == header.Kid {
			jwk, found = candidate, true
			break
		}
	}
	if !found {
		t.Fatalf("JWKS 里没有 kid=%q 的公钥", header.Kid)
	}
	if jwk.Kty != "EC" || jwk.Crv != "SM2" {
		t.Fatalf("JWK 形状不符：%+v", jwk)
	}
	x, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		t.Fatalf("decode jwk.x: %v", err)
	}
	y, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil {
		t.Fatalf("decode jwk.y: %v", err)
	}
	pub := &sm2.PublicKey{Curve: sm2.P256Sm2(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	claims, err := signing.VerifyJWT(pub, token)
	if err != nil {
		t.Fatalf("JWKS 公钥验签失败（AC-DP-01 的核心断言）：%v", err)
	}
	return claims
}

// decodeJWSPayload 解出 JWS 的 payload 原文（用于逐字节查看序列化形态）。
func decodeJWSPayload(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("不是三段式：%q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return string(raw)
}

// AC-DP-01：合法令牌 + 达标 ACR → 转发并注入断言；**claim 11 项齐全**、
// `exp-iat==60`、`roles` 空集是 `[]`（不是省略、不是 null）。
func TestAC_DP_01_AssertionIsCompleteAndVerifiable(t *testing.T) {
	tg := newTestGuard(t, nil)
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	values := up.assertionValues()
	if len(values) != 1 {
		t.Fatalf("上游收到的断言头数量期望 1，实际 %d（%v）", len(values), values)
	}
	claims := assertionClaims(t, tg.keys, values[0])

	// claim 11 项（D16① 的表格逐项对照）。
	required := []string{"iss", "sub", "aud", "cid", "uname", "roles", "acr", "amr", "iat", "exp", "jti"}
	for _, name := range required {
		if _, ok := claims[name]; !ok {
			t.Fatalf("断言缺 claim %q（全量：%v）", name, claims)
		}
	}
	if len(claims) != len(required) {
		t.Fatalf("claim 数量期望 %d，实际 %d（多出的会变成「悄悄新增契约面」）：%v",
			len(required), len(claims), claims)
	}
	if got := claims["iss"]; got != AssertionIssuer {
		t.Fatalf("iss 期望 %q，实际 %v", AssertionIssuer, got)
	}
	if got := claims["sub"]; got != testUserID {
		t.Fatalf("sub 期望会话归属用户 %q，实际 %v", testUserID, got)
	}
	if got := claims["aud"]; got != testClientID {
		t.Fatalf("aud 期望路由命中的 client %q，实际 %v", testClientID, got)
	}
	if got := claims["cid"]; got != testClientID {
		t.Fatalf("cid 期望会话 client %q，实际 %v", testClientID, got)
	}
	if got := claims["uname"]; got != "alice" {
		t.Fatalf("uname 期望 users.username %q，实际 %v", "alice", got)
	}
	if got := claims["acr"]; got != "AAL1" {
		t.Fatalf("acr 期望会话值 AAL1，实际 %v", got)
	}
	iat, ok := claims["iat"].(float64)
	if !ok {
		t.Fatalf("iat 不是数字：%v", claims["iat"])
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		t.Fatalf("exp 不是数字：%v", claims["exp"])
	}
	if int64(exp)-int64(iat) != int64(AssertionTTL/time.Second) {
		t.Fatalf("exp-iat 期望 %d 秒，实际 %d 秒", int64(AssertionTTL/time.Second), int64(exp)-int64(iat))
	}
	// 逐字节看 payload：`roles` 空集必须是 `[]`（`null` 或省略都会让 SDK 的数组解析炸）。
	payload := decodeJWSPayload(t, values[0])
	if !strings.Contains(payload, `"roles":[]`) {
		t.Fatalf("roles 空集必须序列化成 []（不是 null/省略）：%s", payload)
	}
	if !strings.Contains(payload, `"amr":["pwd"]`) {
		t.Fatalf("amr 期望 [\"pwd\"]，实际 %v", claims["amr"])
	}
	jti, _ := claims["jti"].(string)
	if len(jti) != 36 {
		t.Fatalf("jti 期望 UUID 字符串（36 字符），实际 %q", jti)
	}
}

// AC-DP-02/03（网关侧）：客户端自带伪造 `X-GuanJin-Assertion` → 上游收到该头**只有 1 个**，
// 且是网关现签的；伪造值零透传，且不影响 ACR/身份判定。
func TestAC_DP_02_ForgedAssertionIsReplacedNotAppended(t *testing.T) {
	tg := newTestGuard(t, nil)
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)

	token := issueUserToken(t, tg.keys, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/billing/orders", nil)
	req.Header.Set(BearerHeader, "Bearer "+token)
	// 伪造：一个不像 JWT 的值 + 大小写变体（`Header.Del` 只认 canonical 形式，
	// 非规范键必须靠 EqualFold 兜住——这是 proxy.go 的同一条坑）。
	req.Header.Set(AssertionHeader, "forged-assertion-value")
	req.Header["X-Guanjin-Assertion"] = []string{"forged-lowercase"}
	req.Header["X-GUANJIN-ASSERTION"] = []string{"forged-upper"}

	rec := serve(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200（伪造头不影响判定），实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	values := up.assertionValues()
	if len(values) != 1 {
		t.Fatalf("上游收到的断言头必须只有 1 个，实际 %d：%v", len(values), values)
	}
	for _, forged := range []string{"forged-assertion-value", "forged-lowercase", "forged-upper"} {
		if strings.Contains(values[0], forged) {
			t.Fatalf("伪造值 %q 透传到上游了：%s", forged, values[0])
		}
	}
	claims := assertionClaims(t, tg.keys, values[0])
	if claims["sub"] != testUserID || claims["acr"] != "AAL1" {
		t.Fatalf("伪造头影响了身份判定：%v", claims)
	}
}

// 同一请求只签一次：一次请求 = 一个 jti，两次请求 = 两个不同 jti。
func TestAssertionJtiIsPerRequest(t *testing.T) {
	tg := newTestGuard(t, nil)
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)
	token := issueUserToken(t, tg.keys, nil)

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		if rec := doRequest(p, http.MethodGet, "/api/billing/orders", token); rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次期望 200，实际 %d", i+1, rec.Code)
		}
		claims := assertionClaims(t, tg.keys, up.assertionValues()[0])
		jti, _ := claims["jti"].(string)
		if seen[jti] {
			t.Fatalf("jti %q 在同一进程内重复：每请求必须新建 UUIDv7", jti)
		}
		seen[jti] = true
	}
}

// 签名密钥不可用 → 503 `KEY_UNAVAILABLE`，且**不转发**（不让没有身份的请求进入业务系统）。
func TestSignerFailureIsKeyUnavailableAndNotForwarded(t *testing.T) {
	tg := newTestGuard(t, nil)
	up := newRecordedUpstream(t)
	router, err := NewRouter([]Route{{Prefix: "/api/billing", ClientID: testClientID, Upstream: up.url(t)}})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	forward, err := New(router, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// 空密钥集 = 签不出来（模拟启动后密钥行被删/解封失败的最坏情况）。
	signer := &AssertionSigner{Keys: newTestKeys(t), NewJTI: func() (string, error) { return "", io.ErrUnexpectedEOF }}
	p, err := NewPipeline(forward, tg.guard, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 503，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "KEY_UNAVAILABLE" {
		t.Fatalf("错误码期望 KEY_UNAVAILABLE，实际 %s", code)
	}
	if up.hits != 0 {
		t.Fatalf("上游被调用了 %d 次：签不出断言就不许转发", up.hits)
	}
}

// AC-DP-10（网关侧）：访问日志里**不得**出现令牌或断言原文。
func TestAC_DP_10_AccessLogCarriesNoTokenOrAssertion(t *testing.T) {
	tg := newTestGuard(t, nil)
	up := newRecordedUpstream(t)

	var buf strings.Builder
	logger := slog.New(slog.NewJSONHandler(&lockedWriter{buf: &buf}, nil))
	router, err := NewRouter([]Route{{Prefix: "/api/billing", ClientID: testClientID, Upstream: up.url(t)}})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	forward, err := New(router, Options{Logger: logger})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer, err := NewAssertionSigner(tg.keys)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	guard := tg.guard
	guard.Logger = logger
	p, err := NewPipeline(forward, guard, signer, logger)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}

	token := issueUserToken(t, tg.keys, nil)
	if rec := doRequest(p, http.MethodGet, "/api/billing/orders", token); rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	assertion := up.assertionValues()[0]
	logs := buf.String()
	if strings.Contains(logs, token) {
		t.Fatalf("日志里出现了用户令牌原文：%s", logs)
	}
	if strings.Contains(logs, assertion) {
		t.Fatalf("日志里出现了断言原文：%s", logs)
	}
	for _, banned := range []string{"forged-assertion", "pwd"} {
		if strings.Contains(logs, banned) {
			t.Fatalf("日志里出现了敏感片段 %q：%s", banned, logs)
		}
	}
}

// AC-DP-13：上游拨号失败 → 503 `UPSTREAM_UNAVAILABLE` + `Retry-After`，且**不注入**断言。
func TestAC_DP_13_UpstreamUnavailableKeepsSemantics(t *testing.T) {
	tg := newTestGuard(t, nil)
	// 上游指向一个刚关闭的端口：拨号必失败。
	dead := newRecordedUpstream(t)
	deadURL := dead.url(t)
	dead.srv.Close()

	router, err := NewRouter([]Route{{Prefix: "/api/billing", ClientID: testClientID, Upstream: deadURL}})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	forward, err := New(router, Options{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DialTimeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer, err := NewAssertionSigner(tg.keys)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	p, err := NewPipeline(forward, tg.guard, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 503，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("错误码期望 UPSTREAM_UNAVAILABLE，实际 %s", code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("503 必须带 Retry-After（httpx 的统一口径）")
	}
}

// 未匹配路由（生产路径 `Pipeline.ServeHTTP`）：404 `CLIENT_NOT_FOUND`，不是 `UPSTREAM_UNAVAILABLE`，
// 且**零注入**、上游一次都不调用——即使请求带了合法令牌。
//
// 这条与 `TestNoRouteIs404InvalidRequestAndNeverForwards` 成对：那条测基座 `Handler.ServeHTTP`，
// 这条测真正挂到 `cmd/guanjingw` 上的链路。两处口径必须一致，否则"改一处漏一处"就会
// 让数据面按不同码回同一个错误。
func TestNoRouteThroughPipelineIs404ClientNotFound(t *testing.T) {
	tg := newTestGuard(t, nil)
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)

	req := newRequest("/api/nope/orders")
	req.Header.Set(BearerHeader, "Bearer "+issueUserToken(t, tg.keys, nil))
	rec := serve(p, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("期望 404，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != httpx.CodeClientNotFound {
		t.Fatalf("错误码期望 CLIENT_NOT_FOUND，实际 %s", code)
	}
	if code := errorCode(t, rec); code == httpx.CodeUpstreamUnavailable {
		t.Fatal("未匹配路由不得返回 UPSTREAM_UNAVAILABLE（那是转发失败的码）")
	}
	// 部署漂移必须可观测：计数递增（WARN 日志同点产生）。扣掉本用例之前可能已有的计数。
	if got := MissingRouteTotal(); got < 1 {
		t.Fatalf("路由缺失计数应递增（当前 %d）——否则部署少了路由项没人知道", got)
	}
	if up.hits != 0 {
		t.Fatalf("上游被调用了 %d 次：无路由不得裸转发", up.hits)
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Error("404 不该带 Retry-After")
	}
	// 零注入：响应上不得出现断言头。
	for k := range rec.Header() {
		if strings.EqualFold(k, AssertionHeader) {
			t.Fatalf("响应上出现了断言头 %q：被拒时不得注入身份", k)
		}
	}
}
