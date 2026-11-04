package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
)

// testEnv 把"一条流 + 三条冻结路径"装到 httptest 上（真的走 ServeMux 路由，不手调 handler）。
type testEnv struct {
	srv      *httptest.Server
	store    *fakeStore
	orch     *Orchestrator
	verifier *fakeVerifier
}

func testLogger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func newTestEnv(t *testing.T, opts ...func(*Deps)) *testEnv {
	t.Helper()
	st := &fakeStore{}
	verifier := &fakeVerifier{}
	orc := newOrc(t, st, verifier, opts...)
	handler, err := NewHTTPHandler(orc, testLogger())
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	mux := http.NewServeMux()
	handler.Routes(mux)
	// 与 `cmd/guanjind` 同样的外层：trace 中间件负责生成/透传 trace_id，
	// 错误响应里的 `trace_id` 才有值（这里不绕过它，否则测的就不是真实链路）。
	srv := httptest.NewServer(httpx.TraceMiddleware(mux, "X-Trace-Id", testLogger()))
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv, store: st, orch: orc, verifier: verifier}
}

// startFlow 在**同一个编排器**上开一条流（真实链路里由 S6 的 `/authorize` 建流）。
func (e *testEnv) startFlow(t *testing.T) string {
	t.Helper()
	res, err := e.orch.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return res.FlowID.String()
}

func postJSON(t *testing.T, url, body string) (*http.Response, httpx.ErrorResponse) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	var env httpx.ErrorResponse
	if resp.StatusCode >= 400 {
		if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
			t.Fatalf("错误响应不是统一四字段：%v", err)
		}
	}
	return resp, env
}

// 口令的挑战下发：200 + `next_action=input_password`，且**不带** `challenge_id`（无状态挑战）。
func TestHTTPChallengePassword(t *testing.T) {
	env := newTestEnv(t)
	env.verifier.ticket = factor.ChallengeTicket{NextAction: factor.NextActionInputPassword}
	flowID := env.startFlow(t)

	resp, _ := postJSON(t, env.srv.URL+"/auth/challenge/password",
		`{"flow_id":"`+flowID+`","purpose":"login"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应为 200，实际 %d", resp.StatusCode)
	}
	var got challengeResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if got.ChallengeID != nil {
		t.Fatalf("口令不得有 challenge_id，实际 %v", *got.ChallengeID)
	}
	if got.NextAction != factor.NextActionInputPassword {
		t.Fatalf("next_action 应为 input_password，实际 %s", got.NextAction)
	}
	if got.FlowID == nil || *got.FlowID != flowID {
		t.Fatal("应回显 flow_id")
	}
	if got.TTLSeconds <= 0 {
		t.Fatalf("应给出正的 ttl，实际 %d", got.TTLSeconds)
	}
	if got.ExpiresAt == nil {
		t.Fatal("应回显 expires_at")
	}
}

// CH-1：`login` 缺 `flow_id` → 400 MISSING_PARAMETER（不是 FLOW_NOT_FOUND、更不是 500）。
func TestHTTPChallengeMissingFlowID(t *testing.T) {
	env := newTestEnv(t)
	resp, body := postJSON(t, env.srv.URL+"/auth/challenge/password", `{"purpose":"login"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("应为 400，实际 %d", resp.StatusCode)
	}
	if body.Code != httpx.CodeMissingParameter {
		t.Fatalf("应为 MISSING_PARAMETER，实际 %s", body.Code)
	}
	if body.TraceID == "" {
		t.Fatal("错误响应必须带可复制 trace_id")
	}
}

// AC-FLOW-03：已完成的步再下发 → 409 `STEP_ALREADY_SATISFIED`（HTTP 语义的唯一落点）。
func TestHTTPChallengeAlreadySatisfiedIs409(t *testing.T) {
	env := newTestEnv(t)
	env.verifier.ticket = factor.ChallengeTicket{NextAction: factor.NextActionInputPassword}
	flowID := env.startFlow(t)
	env.store.flow.AchievedAMR = []string{"pwd"}

	resp, body := postJSON(t, env.srv.URL+"/auth/challenge/password",
		`{"flow_id":"`+flowID+`","purpose":"login"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("应为 409，实际 %d", resp.StatusCode)
	}
	if body.Code != CodeStepAlreadySatisfied {
		t.Fatalf("应为 %s，实际 %s", CodeStepAlreadySatisfied, body.Code)
	}
}

// 畸形 JSON / 畸形 flow_id：分别 400 INVALID_REQUEST 与 404 FLOW_NOT_FOUND。
func TestHTTPChallengeBadInput(t *testing.T) {
	env := newTestEnv(t)

	resp, body := postJSON(t, env.srv.URL+"/auth/challenge/password", `{`)
	if resp.StatusCode != http.StatusBadRequest || body.Code != httpx.CodeInvalidRequest {
		t.Fatalf("坏 JSON 应 400 INVALID_REQUEST，实际 %d %s", resp.StatusCode, body.Code)
	}

	resp, body = postJSON(t, env.srv.URL+"/auth/challenge/password", `{"flow_id":"nope","purpose":"login"}`)
	if resp.StatusCode != http.StatusNotFound || body.Code != httpx.CodeFlowNotFound {
		t.Fatalf("畸形 flow_id 应 404 FLOW_NOT_FOUND，实际 %d %s", resp.StatusCode, body.Code)
	}
}

// 校验成功：200 `{verified:true, amr:["pwd"], acr:"AAL1"}`。
func TestHTTPVerifyPasswordSatisfied(t *testing.T) {
	env := newTestEnv(t)
	env.verifier.resp = factor.VerifyResult{Factor: factor.FactorPassword, AMR: "pwd", ACR: factor.ACRAAL1}
	flowID := env.startFlow(t)

	resp, _ := postJSON(t, env.srv.URL+"/auth/verify/password", `{"flow_id":"`+flowID+`","response":"pw"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应为 200，实际 %d", resp.StatusCode)
	}
	var got verifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if !got.Verified || got.ACR != factor.ACRAAL1 {
		t.Fatalf("应为 verified/AAL1，实际 %+v", got)
	}
	if len(got.AMR) != 1 || got.AMR[0] != "pwd" {
		t.Fatalf("amr 应为 [pwd]，实际 %v", got.AMR)
	}
}

// AC-FLOW-02：目标 AAL2 但只达成 AAL1 → 403 `ACR_INSUFFICIENT` + `details.flow` 非空。
func TestHTTPVerifyInsufficientReturnsDescriptor(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) {
		d.TierOf = func(context.Context, string) (string, error) { return "L3", nil }
	})
	env.verifier.resp = factor.VerifyResult{Factor: factor.FactorPassword, AMR: "pwd", ACR: factor.ACRAAL1}
	flowID := env.startFlow(t)

	resp, body := postJSON(t, env.srv.URL+"/auth/verify/password", `{"flow_id":"`+flowID+`","response":"pw"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("应为 403，实际 %d", resp.StatusCode)
	}
	if body.Code != httpx.CodeACRInsufficient {
		t.Fatalf("应为 ACR_INSUFFICIENT，实际 %s", body.Code)
	}
	details, ok := body.Details.(map[string]any)
	if !ok || details["flow"] == nil {
		t.Fatalf("details.flow 必须存在（前端据此补步），实际 %v", body.Details)
	}
	flow, _ := details["flow"].(map[string]any)
	if flow["terminal_state"] != TerminalNeedMore {
		t.Fatalf("应为 need_more，实际 %v", flow["terminal_state"])
	}
}

// 未装配的因子：422 FACTOR_NOT_SUPPORTED（不是 500）。
func TestHTTPChallengeUnsupportedFactor(t *testing.T) {
	env := newTestEnv(t)
	flowID := env.startFlow(t)

	resp, body := postJSON(t, env.srv.URL+"/auth/challenge/sms_otp",
		`{"flow_id":"`+flowID+`","purpose":"login"}`)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("应为 422，实际 %d", resp.StatusCode)
	}
	if body.Code != httpx.CodeFactorNotSupported {
		t.Fatalf("应为 FACTOR_NOT_SUPPORTED，实际 %s", body.Code)
	}
}

// 不认识的错误 → 500 INTERNAL_ERROR，且**不**把内部 err 文本回给客户端。
func TestHTTPInternalErrorIsOpaque(t *testing.T) {
	env := newTestEnv(t)
	env.verifier.err = errors.New("boom: internal detail")
	flowID := env.startFlow(t)

	resp, body := postJSON(t, env.srv.URL+"/auth/verify/password", `{"flow_id":"`+flowID+`","response":"pw"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("应为 500，实际 %d", resp.StatusCode)
	}
	if body.Code != httpx.CodeInternalError {
		t.Fatalf("应为 INTERNAL_ERROR，实际 %s", body.Code)
	}
	if strings.Contains(body.Message, "boom") {
		t.Fatalf("不得把内部错误文本透给客户端，实际 %q", body.Message)
	}
}

// `GET /authorize/flow`：200 + Descriptor；缺参 400；过期 400 FLOW_EXPIRED（AC-FLOW-10①）。
func TestHTTPDescribe(t *testing.T) {
	env := newTestEnv(t)
	flowID := env.startFlow(t)

	resp, err := http.Get(env.srv.URL + "/authorize/flow?flow_id=" + flowID)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应为 200，实际 %d", resp.StatusCode)
	}
	var d Descriptor
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if d.FlowID != flowID || d.TargetACR != factor.ACRAAL1 {
		t.Fatalf("Descriptor 不符：%+v", d)
	}

	resp2, err := http.Get(env.srv.URL + "/authorize/flow")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 flow_id 应 400，实际 %d", resp2.StatusCode)
	}

	env.store.flow.ExpiresAt = time.Unix(1_760_000_000, 0).UTC()
	resp3, err := http.Get(env.srv.URL + "/authorize/flow?flow_id=" + flowID)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	var body httpx.ErrorResponse
	if err := json.NewDecoder(resp3.Body).Decode(&body); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if resp3.StatusCode != http.StatusBadRequest || body.Code != httpx.CodeFlowExpired {
		t.Fatalf("过期流应 400 FLOW_EXPIRED，实际 %d %s", resp3.StatusCode, body.Code)
	}
}

// 处理器构造：缺依赖立刻失败（不等到第一个请求）。
func TestNewHTTPHandlerRequiresDeps(t *testing.T) {
	if _, err := NewHTTPHandler(nil, testLogger()); err == nil {
		t.Fatal("缺编排器必须报错")
	}
	orc := newOrc(t, &fakeStore{}, &fakeVerifier{})
	if _, err := NewHTTPHandler(orc, nil); err == nil {
		t.Fatal("缺 logger 必须报错")
	}
}

// D15④ / ADR-005：完整性失败必须是**不可重试**的 500，且落一条带 `crypto_sentinel` 的结构化日志。
//
// 为什么这条必须有：`ErrIntegrityMismatch` 被 `CodeOf` 归到 `INTERNAL_ERROR`，走的是"已识别码"
// 分支 —— 不单独拦一道就**一条日志都不落**，那对 crypto 哨兵到不了任何日志/指标，
// 而客户端按 5xx 重试会对"存量 v1 未重加密"打出重试风暴。
func TestHTTPIntegrityMismatchIsNonRetryable(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	st := &fakeStore{}
	verifier := &fakeVerifier{err: fmt.Errorf("%w: open secret: %w",
		credential.ErrIntegrityMismatch, crypto.ErrCiphertextVersion)}
	orc := newOrc(t, st, verifier)
	h, err := NewHTTPHandler(orc, logger)
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	mux := http.NewServeMux()
	h.Routes(mux)
	srv := httptest.NewServer(httpx.TraceMiddleware(mux, "X-Trace-Id", logger))
	t.Cleanup(srv.Close)

	res, berr := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if berr != nil {
		t.Fatalf("Begin: %v", berr)
	}
	resp, body := postJSON(t, srv.URL+"/auth/verify/password",
		`{"flow_id":"`+res.FlowID.String()+`","response":"pw"}`)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("应为 500，实际 %d", resp.StatusCode)
	}
	if body.Code != httpx.CodeInternalError || body.Message != httpx.IntegrityMessage {
		t.Fatalf("文案必须是不可重试那句（%q），实际 %q / %q", httpx.IntegrityMessage, body.Code, body.Message)
	}
	details, _ := body.Details.(map[string]any)
	if details["crypto_sentinel"] != "ciphertext_version" || details["retryable"] != false {
		t.Fatalf("details 应标明哨兵与不可重试，实际 %v", body.Details)
	}

	logs := logBuf.String()
	if !strings.Contains(logs, `"crypto_sentinel":"ciphertext_version"`) {
		t.Fatalf("必须落一条带 crypto_sentinel 的日志，实际 %q", logs)
	}
	if !strings.Contains(logs, `"retryable":false`) || !strings.Contains(logs, "integrity_mismatch_total") {
		t.Fatalf("日志应带 retryable=false 与指标名，实际 %q", logs)
	}
	if !strings.Contains(logs, `"trace_id":"`) {
		t.Fatalf("日志必须带可复制的 trace_id，实际 %q", logs)
	}
}
