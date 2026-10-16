package protocol

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/flow"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/session"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

func (s *Server) Authorize(c *gin.Context) {
	ctx := c.Request.Context()
	q := c.Request.URL.Query()

	clientID := q.Get("client_id")
	if clientID == "" {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "缺少 client_id"))
		return
	}
	client, err := s.clients.GetByID(ctx, clientID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if client.Status != "active" {
		httpx.Error(c, httpx.New("INVALID_CLIENT", "客户端不可用"))
		return
	}
	if q.Get("response_type") != "code" {
		httpx.Error(c, httpx.New("UNSUPPORTED_RESPONSE_TYPE", "仅支持 response_type=code"))
		return
	}
	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" || !containsString(client.RedirectURIs, redirectURI) {
		httpx.Error(c, httpx.New("INVALID_REDIRECT_URI", "回调地址未登记"))
		return
	}
	method := q.Get("code_challenge_method")
	challenge := q.Get("code_challenge")
	if method != "" && method != "S256" {
		httpx.Error(c, httpx.New("UNSUPPORTED_CODE_CHALLENGE_METHOD", "仅支持 S256"))
		return
	}
	if store.IsPublicClient(client) && challenge == "" {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "公共客户端必须携带 code_challenge"))
		return
	}

	policy, err := s.policies.GetEffective(ctx, clientID, client.Tier)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	target := s.orch.ResolveTargetACR(client.Tier, policyMinACR(policy))
	disabled := disabledFactors(policy)

	sess := s.currentSession(c)
	if sess != nil && session.IsActive(sess) && s.orch.Satisfied(sess.ACR, target) {
		code, err := s.issueAuthCodeForSession(c, sess, clientID, redirectURI, q.Get("nonce"), q.Get("scope"), challenge)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		c.Redirect(http.StatusFound, withQuery(redirectURI, map[string]string{
			"code":  code,
			"state": q.Get("state"),
		}))
		return
	}

	flowID, err := newOpaqueToken()
	if err != nil {
		httpx.Error(c, &httpx.AppError{Code: "INTERNAL_ERROR", HTTP: http.StatusInternalServerError, Message: httpx.MessageFor("INTERNAL_ERROR")})
		return
	}
	now := time.Now().UTC()
	state := &flow.State{
		FlowID:        flowID,
		ClientID:      clientID,
		ClientName:    client.Name,
		Tier:          client.Tier,
		RedirectURI:   redirectURI,
		Scope:         q.Get("scope"),
		State:         q.Get("state"),
		Nonce:         q.Get("nonce"),
		CodeChallenge: challenge,
		TargetACR:     target,
		CreatedAt:     now,
		ExpiresAt:     now.Add(flow.FlowTTL),
	}
	if sess != nil && session.IsActive(sess) {
		state.UserID = sess.UserID.String()
		state.Achieved = s.factorTypesFromAMR(sess.AMR)
	}
	state.CurrentACR = s.orch.CurrentACR(state.Achieved, disabled)
	state.Satisfied = s.orch.Satisfied(state.CurrentACR, target)
	if err := s.flows.Put(ctx, state); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Redirect(http.StatusFound, "/login?flow_id="+flowID)
}

func (s *Server) loadFlow(c *gin.Context, flowID string) (*flow.State, error) {
	ctx := c.Request.Context()
	if flowID == "" {
		return nil, httpx.New("FLOW_NOT_FOUND", "缺少 flow_id")
	}
	state, err := s.flows.Get(ctx, flowID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, httpx.New("FLOW_NOT_FOUND", "登录流程不存在或已过期")
	}
	if state.Expired(time.Now().UTC()) {
		return nil, httpx.New("FLOW_EXPIRED", "登录流程已过期")
	}
	return state, nil
}

func (s *Server) factorTypesFromAMR(amr []string) []string {
	if len(amr) == 0 {
		return nil
	}
	out := make([]string, 0, len(amr))
	for _, name := range amr {
		if typ := s.factorTypeForAMR(name); typ != "" {
			out = append(out, typ)
		}
	}
	return out
}

func (s *Server) factorTypeForAMR(name string) string {
	if _, ok := s.registry.Get(name); ok {
		return name
	}
	for _, p := range s.registry.All() {
		if namer, ok := p.(factor.AmrNamer); ok && namer.AMR() == name {
			return p.Type()
		}
	}
	return ""
}

func (s *Server) amrForType(typ string) string {
	if p, ok := s.registry.Get(typ); ok {
		if namer, ok := p.(factor.AmrNamer); ok {
			return namer.AMR()
		}
	}
	return typ
}

func (s *Server) disabledForClient(c *gin.Context, clientID, tier string) []string {
	policy, err := s.policies.GetEffective(c.Request.Context(), clientID, tier)
	if err != nil {
		return nil
	}
	return disabledFactors(policy)
}

func (s *Server) issueAuthCodeForSession(c *gin.Context, sess *model.Session, clientID, redirectURI, nonce, scope, challenge string) (string, error) {
	code, err := newOpaqueToken()
	if err != nil {
		return "", err
	}
	data := authCodeData{
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		UserID:        sess.UserID.String(),
		SessionID:     sess.ID.String(),
		ACR:           sess.ACR,
		AMR:           []string(sess.AMR),
		Nonce:         nonce,
		Scope:         scope,
		CodeChallenge: challenge,
	}
	if err := s.state.PutAuthCode(c.Request.Context(), code, data, authCodeTTL); err != nil {
		return "", err
	}
	return code, nil
}

func policyMinACR(policy *model.LoginPolicy) string {
	if policy == nil {
		return ""
	}
	return policy.MinACR
}

func disabledFactors(policy *model.LoginPolicy) []string {
	if policy == nil {
		return nil
	}
	return append([]string{}, policy.DisabledFactors...)
}

func registrationMode(policy *model.LoginPolicy) string {
	if policy == nil || policy.SelfRegistrationAllowed {
		return "self"
	}
	return "invite"
}
