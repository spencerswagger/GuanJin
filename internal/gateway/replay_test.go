package gateway

import (
	"errors"
	"net/http"
	"testing"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是 **AC-DP-08 平台侧**的单测：同一 `jti` 在有效窗口内第二次 → 401 且不转发；
// 判重存储不可用 → 503 `REDIS_UNAVAILABLE`（fail-closed）。
//
// 口径：判据原文写的是「集成（真实 Redis）」、`AC-DP-04~08` 归 S8，故平台侧必须自己判——
// 真 Redis 版本的实现与集成用例在 `internal/gateway/adapter`（`RedisReplayGuard`）。

// AC-DP-08：固定 `jti` 的第二次请求必须 401，且**不触达上游**。
func TestAC_DP_08_DuplicateJtiIsRejected(t *testing.T) {
	tg := newTestGuard(t, nil)
	up := newRecordedUpstream(t)
	// 用确定性 jti 模拟"同一份断言被重放"（真实实现每请求新建 UUIDv7，重复只可能来自重放/缺陷）。
	fixed := "0193f0aa-0000-7000-8000-00000000beef"
	router, err := NewRouter([]Route{{Prefix: "/api/billing", ClientID: testClientID, Upstream: up.url(t)}})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	forward, err := New(router, Options{Logger: quietLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer, err := NewAssertionSigner(tg.keys)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	signer.NewJTI = func() (string, error) { return fixed, nil }
	p, err := NewPipeline(forward, tg.guard, signer, tg.replay, quietLogger())
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}

	token := issueUserToken(t, tg.keys, nil)
	if rec := doRequest(p, http.MethodGet, "/api/billing/orders", token); rec.Code != http.StatusOK {
		t.Fatalf("首次请求期望 200，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if up.hits != 1 {
		t.Fatalf("首次请求应该转发（上游命中 %d）", up.hits)
	}

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", token)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("同 jti 二次期望 401，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != httpx.CodeTokenInvalid {
		t.Fatalf("错误码期望 TOKEN_INVALID，实际 %s", code)
	}
	if up.hits != 1 {
		t.Fatalf("重放请求不该转发（上游命中 %d，期望停在 1）", up.hits)
	}
}

// 正常流量不得被误杀：每请求新 jti → 全部 200，且判重**每请求都执行**。
func TestReplayGuardIsConsultedEveryRequest(t *testing.T) {
	tg := newTestGuard(t, nil)
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)
	token := issueUserToken(t, tg.keys, nil)

	for i := 0; i < 4; i++ {
		if rec := doRequest(p, http.MethodGet, "/api/billing/orders", token); rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求期望 200（正常复用访问令牌不得被判重误杀），实际 %d（body=%s）",
				i+1, rec.Code, rec.Body.String())
		}
	}
	if tg.replay.calls != 4 {
		t.Fatalf("判重调用次数期望 4（每请求一次），实际 %d", tg.replay.calls)
	}
	if up.hits != 4 {
		t.Fatalf("四次请求都应转发，实际上游命中 %d", up.hits)
	}
}

// 判重存储不可用 → 503 `REDIS_UNAVAILABLE`，且**不转发**（无法确认是否重放就拒）。
func TestReplayStoreUnavailableIsRedisUnavailable(t *testing.T) {
	tg := newTestGuard(t, func(g *testGuard) {
		g.replay.err = errors.New("dial tcp 127.0.0.1:6379: connect: connection refused")
	})
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 503，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec); code != httpx.CodeRedisUnavailable {
		t.Fatalf("错误码期望 REDIS_UNAVAILABLE，实际 %s", code)
	}
	if up.hits != 0 {
		t.Fatalf("判重不可用时不该转发（上游命中 %d）", up.hits)
	}
}

// 装配期守卫：没有判重存储就不许起数据面（"看起来在跑但少一条判据"是最坏的形态）。
func TestNewPipelineRequiresReplayGuard(t *testing.T) {
	tg := newTestGuard(t, nil)
	up := newRecordedUpstream(t)
	router, err := NewRouter([]Route{{Prefix: "/api/billing", ClientID: testClientID, Upstream: up.url(t)}})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	forward, err := New(router, Options{Logger: quietLogger()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	signer, err := NewAssertionSigner(tg.keys)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	if _, err := NewPipeline(forward, tg.guard, signer, nil, quietLogger()); !errors.Is(err, ErrReplayGuardMissing) {
		t.Fatalf("期望 ErrReplayGuardMissing，实际 %v", err)
	}
}

// 判重窗口必须覆盖断言有效期（AC-DP-08："窗口过期后可再用"指的是断言过期之后）。
func TestReplayWindowCoversAssertionTTL(t *testing.T) {
	iat := timeNowForTest()
	a := Assertion{Token: "t", JTI: "j", Iat: iat, Exp: iat.Add(AssertionTTL)}
	if got := replayWindowFor(a); got != AssertionTTL {
		t.Fatalf("判重窗口期望 %s（= exp-iat），实际 %s", AssertionTTL, got)
	}
	// exp 早于 iat（签发缺陷）时不得退化成 0 窗口（那是"永不判重"）。
	if got := replayWindowFor(Assertion{Iat: iat, Exp: iat.Add(-AssertionTTL)}); got != AssertionTTL {
		t.Fatalf("退化窗口应回到 %s，实际 %s", AssertionTTL, got)
	}
}
