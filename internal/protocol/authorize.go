package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 授权端点（`/authorize` + `/authorize/submit`）。
//
// 为什么需要第三个 Redis 键 `auth:flow:{flow_id}`：`/authorize` 收下的
// `redirect_uri / state / nonce / code_challenge / client_id` 必须活到 `/authorize/submit`
// 才能据以发码，而 §3.1.5 的 submit 请求体只有 `{flow_id, answers}`——这些上下文没有别的落点。
// 键名沿用 `auth:` 前缀、TTL = 流 TTL，**不新增表**（裁定 A）。该键名已在 PR 风险段登记待 Ark 收录。
const flowKeyPrefix = "auth:flow:"

// FlowKey 返回授权上下文的键名（冻结口径同 `CodeKey`/`RefreshKey`）。
func FlowKey(flowID string) string { return flowKeyPrefix + flowID }

// ErrFlowContextGone 表示授权上下文不存在/已被消费（对外 `FLOW_NOT_FOUND`）。
var ErrFlowContextGone = errors.New("protocol: authorize context is gone")

// FlowStarter 开一条登录流（实现是 `flow.Orchestrator.Begin`，装配层适配）。
type FlowStarter interface {
	Begin(ctx context.Context, clientID, traceID string) (flowID string, err error)
}

// FlowResult 是"流是否达标"的结论（ACR/AMR 由编排层按 achieved_amr 现算，不在这里重算）。
type FlowResult struct {
	Satisfied bool
	UserID    string
	ACR       string
	AMR       []string
}

// FlowVerifier 校验一步应答（实现是 `flow.Orchestrator.VerifyChallenge`）。
type FlowVerifier interface {
	Verify(ctx context.Context, flowID, factor, challengeID, response, userID string) (FlowResult, error)
}

// RedirectReader 读登记的回调地址（`clients.redirect_uris`）。
type RedirectReader interface {
	RedirectURIs(ctx context.Context, clientID string) ([]string, error)
}

type authorizeContext struct {
	ClientID  string `json:"cid"`
	Redirect  string `json:"rd"`
	State     string `json:"st,omitempty"`
	Nonce     string `json:"nc,omitempty"`
	Challenge string `json:"ch"`
}

// authorize 校验参数并把上下文落到 Redis，然后 302 到登录页（携带 flow_id）。
//
// 校验顺序（顺序即错误码）：`INVALID_REQUEST` → `INVALID_CLIENT` → `INVALID_REDIRECT_URI`。
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("response_type") != "code" {
		s.fail(w, r, httpx.CodeInvalidRequest, "response_type 只支持 code")
		return
	}
	clientID := q.Get("client_id")
	if clientID == "" {
		s.fail(w, r, httpx.CodeMissingParameter, "缺少 client_id")
		return
	}
	if _, err := s.d.Clients.Policy(r.Context(), clientID); err != nil {
		s.fail(w, r, httpx.CodeInvalidClient, "客户端不存在或已停用")
		return
	}
	redirect := q.Get("redirect_uri")
	if redirect == "" {
		s.fail(w, r, httpx.CodeMissingParameter, "缺少 redirect_uri")
		return
	}
	if err := s.checkRedirect(r, clientID, redirect); err != nil {
		s.fail(w, r, httpx.CodeInvalidRequest, "redirect_uri 与登记的地址不匹配")
		return
	}
	challenge := q.Get("code_challenge")
	if challenge == "" || q.Get("code_challenge_method") != "S256" {
		// 公共客户端必须 PKCE（基线只支持 S256）。
		s.fail(w, r, httpx.CodeInvalidCodeVerifier, "缺少 code_challenge 或 method 不是 S256")
		return
	}
	if s.d.Flows == nil {
		s.fail(w, r, httpx.CodeInternalError, "授权流未装配")
		return
	}
	flowID, err := s.d.Flows.Begin(r.Context(), clientID, traceID(r))
	if err != nil {
		s.d.Logger.ErrorContext(r.Context(), "protocol: begin flow failed", "trace_id", traceID(r), "error", err)
		s.fail(w, r, httpx.CodeInternalError, "登录流创建失败")
		return
	}
	payload, _ := json.Marshal(authorizeContext{
		ClientID: clientID, Redirect: redirect,
		State: q.Get("state"), Nonce: q.Get("nonce"), Challenge: challenge,
	})
	if err := s.d.Codes.Put(r.Context(), FlowKey(flowID), string(payload), s.flowTTL()); err != nil {
		s.fail(w, r, mapStoreErr(err, httpx.CodeRedisUnavailable), "登录上下文存储不可用")
		return
	}
	// 302 到登录页（前端拿 flow_id 走 `/authorize/flow` + 挑战面）。
	login := s.d.Issuer + "/login?flow_id=" + url.QueryEscape(flowID)
	http.Redirect(w, r, login, http.StatusFound)
}

type submitRequest struct {
	FlowID  string `json:"flow_id"`
	Answers []struct {
		Factor      string `json:"factor"`
		ChallengeID string `json:"challenge_id"`
		Response    string `json:"response"`
	} `json:"answers"`
}

// authorizeSubmit 逐条校验应答；达标则发一次性码并回 `completed`（含 redirect_uri/state）。
func (s *Server) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	var body submitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		s.fail(w, r, httpx.CodeInvalidRequest, "请求体不是合法 JSON")
		return
	}
	if body.FlowID == "" || len(body.Answers) == 0 {
		s.fail(w, r, httpx.CodeMissingParameter, "缺少 flow_id 或 answers")
		return
	}
	if s.d.FlowVerify == nil {
		s.fail(w, r, httpx.CodeInternalError, "授权流未装配")
		return
	}
	raw, err := s.d.Codes.Get(r.Context(), FlowKey(body.FlowID), ErrFlowContextGone)
	if err != nil {
		s.fail(w, r, mapStoreErr(err, httpx.CodeFlowNotFound), "登录上下文不存在或已过期")
		return
	}
	var ctxInfo authorizeContext
	if err := json.Unmarshal([]byte(raw), &ctxInfo); err != nil {
		s.fail(w, r, httpx.CodeInternalError, "登录上下文损坏")
		return
	}

	var last FlowResult
	for _, a := range body.Answers {
		res, err := s.d.FlowVerify.Verify(r.Context(), body.FlowID, a.Factor, a.ChallengeID, a.Response, "")
		if err != nil {
			code, ok := flowCodeOf(s.d.FlowCode, err)
			if !ok {
				s.d.Logger.ErrorContext(r.Context(), "protocol: verify answer failed", "trace_id", traceID(r), "error", err)
				code = httpx.CodeInternalError
			}
			s.fail(w, r, code, "该步骤校验未通过")
			return
		}
		last = res
	}
	if !last.Satisfied {
		// 未达标不是失败（索引 §2）：如实回 need_more，前端继续补步。
		writeJSON(w, s.d.Logger, http.StatusOK, map[string]any{"status": "need_more"})
		return
	}
	if last.UserID == "" {
		s.fail(w, r, httpx.CodeInternalError, "流已达标但缺少用户")
		return
	}

	policy, err := s.d.Clients.Policy(r.Context(), ctxInfo.ClientID)
	if err != nil {
		s.fail(w, r, httpx.CodeInvalidClient, "客户端不存在或已停用")
		return
	}
	code, err := randomToken()
	if err != nil {
		s.fail(w, r, httpx.CodeInternalError, "授权码生成失败")
		return
	}
	codePayloadJSON, _ := json.Marshal(codePayload{
		Sub: last.UserID, ClientID: ctxInfo.ClientID, ACR: last.ACR, AMR: last.AMR,
		Nonce: ctxInfo.Nonce, Challenge: ctxInfo.Challenge, Redirect: ctxInfo.Redirect,
	})
	if err := s.d.Codes.Put(r.Context(), CodeKey(code), string(codePayloadJSON), policy.AccessTTL); err != nil {
		s.fail(w, r, mapStoreErr(err, httpx.CodeRedisUnavailable), "授权码存储不可用")
		return
	}
	// 上下文一次性：发码后立刻删，避免同一 flow 反复换码。
	_ = s.d.Codes.Delete(r.Context(), FlowKey(body.FlowID))
	s.audit(r, AuditEvent{EventType: "token", ClientID: ctxInfo.ClientID, UserID: last.UserID, Result: "success", Reason: "code_issued"})

	writeJSON(w, s.d.Logger, http.StatusOK, map[string]any{
		"status": "completed", "code": code,
		"redirect_uri": ctxInfo.Redirect, "state": ctxInfo.State,
	})
}

func (s *Server) checkRedirect(r *http.Request, clientID, redirect string) error {
	if s.d.Redirects == nil {
		return errors.New("redirect reader is not configured")
	}
	uris, err := s.d.Redirects.RedirectURIs(r.Context(), clientID)
	if err != nil {
		return err
	}
	for _, u := range uris {
		if u == redirect {
			return nil
		}
	}
	return errors.New("redirect_uri is not registered")
}

func (s *Server) flowTTL() time.Duration {
	if s.d.FlowTTL > 0 {
		return s.d.FlowTTL
	}
	return 10 * time.Minute // 装配层没给时的兜底（正常路径由 FlowTTL 注入）
}

// flowCodeOf 把编排层错误映射成对外码（映射表由装配层注入，本包不 import `internal/flow`）。
func flowCodeOf(mapper func(error) (httpx.Code, bool), err error) (httpx.Code, bool) {
	if err == nil || mapper == nil {
		return "", false
	}
	return mapper(err)
}
