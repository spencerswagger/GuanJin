package protocol

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/session"
	"github.com/spencerswagger/GuanJin/internal/store"
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
	required := targetACR(client.Tier, policy)

	sess := s.currentSession(c)
	if sess != nil && session.IsActive(sess) && acrSatisfies(sess.ACR, required) {
		code, err := newOpaqueToken()
		if err != nil {
			httpx.Error(c, &httpx.AppError{Code: "INTERNAL_ERROR", HTTP: http.StatusInternalServerError, Message: httpx.MessageFor("INTERNAL_ERROR")})
			return
		}
		data := authCodeData{
			ClientID:      clientID,
			RedirectURI:   redirectURI,
			UserID:        sess.UserID.String(),
			SessionID:     sess.ID.String(),
			ACR:           sess.ACR,
			AMR:           []string(sess.AMR),
			Nonce:         q.Get("nonce"),
			Scope:         q.Get("scope"),
			CodeChallenge: challenge,
		}
		if err := s.state.PutAuthCode(ctx, code, data, authCodeTTL); err != nil {
			httpx.Error(c, err)
			return
		}
		c.Redirect(http.StatusFound, withQuery(redirectURI, map[string]string{
			"code":  code,
			"state": q.Get("state"),
		}))
		return
	}

	c.Redirect(http.StatusFound, "/login?"+c.Request.URL.RawQuery)
}
