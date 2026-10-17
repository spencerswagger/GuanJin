package protocol

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/spencerswagger/GuanJin/internal/flow"
	"github.com/spencerswagger/GuanJin/internal/httpx"
)

type registerStartRequest struct {
	ClientID   string `json:"client_id" form:"client_id"`
	Type       string `json:"type" form:"type"`
	Value      string `json:"value" form:"value"`
	InviteCode string `json:"invite_code" form:"invite_code"`
}

type registerCompleteRequest struct {
	RegisterToken string `json:"register_token" form:"register_token"`
	Code          string `json:"code" form:"code"`
	Password      string `json:"password" form:"password"`
}

func (s *Server) RegisterStart(c *gin.Context) {
	if s.registrar == nil {
		httpx.Error(c, httpx.New("INTERNAL_ERROR", "注册服务不可用"))
		return
	}
	var req registerStartRequest
	if err := c.ShouldBind(&req); err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "请求参数无效"))
		return
	}
	result, err := s.registrar.Start(c.Request.Context(), flow.RegisterStartInput{
		ClientID:   req.ClientID,
		Type:       req.Type,
		Value:      req.Value,
		InviteCode: req.InviteCode,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	resp := gin.H{
		"register_token": result.RegisterToken,
		"next_action":    result.NextAction,
		"expires_at":     result.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if result.ChallengeID != "" {
		resp["challenge_id"] = result.ChallengeID
	}
	if result.TargetMasked != "" {
		resp["target_masked"] = result.TargetMasked
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) RegisterComplete(c *gin.Context) {
	if s.registrar == nil {
		httpx.Error(c, httpx.New("INTERNAL_ERROR", "注册服务不可用"))
		return
	}
	var req registerCompleteRequest
	if err := c.ShouldBind(&req); err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "请求参数无效"))
		return
	}
	result, err := s.registrar.Complete(c.Request.Context(), flow.RegisterCompleteInput{
		RegisterToken: req.RegisterToken,
		Code:          req.Code,
		Password:      req.Password,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	client, err := s.clients.GetByID(c.Request.Context(), result.ClientID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	tokens, err := s.issueTokens(c.Request.Context(), tokenIssueParams{
		UserID:    result.UserID,
		Client:    client,
		SessionID: result.Session.ID,
		ACR:       result.ACR,
		AMR:       result.AMR,
		Scope:     "openid profile",
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":            true,
		"access_token":  tokens.AccessToken,
		"token_type":    tokens.TokenType,
		"expires_in":    tokens.ExpiresIn,
		"id_token":      tokens.IDToken,
		"refresh_token": tokens.RefreshToken,
	})
}
