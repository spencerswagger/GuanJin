package protocol

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/session"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Scope        string `json:"scope"`
}

func (s *Server) Token(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "无法解析表单"))
		return
	}
	switch c.PostForm("grant_type") {
	case "authorization_code":
		s.tokenAuthorizationCode(c)
	case "refresh_token":
		s.tokenRefresh(c)
	default:
		httpx.Error(c, httpx.New("UNSUPPORTED_GRANT_TYPE", "不支持的 grant_type"))
	}
}

func (s *Server) tokenAuthorizationCode(c *gin.Context) {
	ctx := c.Request.Context()
	clientID, secret := clientCredentials(c)
	if clientID == "" {
		httpx.Error(c, httpx.New("INVALID_CLIENT", "缺少 client_id"))
		return
	}
	client, err := s.clients.GetByID(ctx, clientID)
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_CLIENT", "客户端不存在"))
		return
	}
	if !store.IsPublicClient(client) && !s.clients.VerifySecret(client, secret) {
		httpx.Error(c, httpx.New("INVALID_CLIENT", "客户端密钥校验失败"))
		return
	}

	code := c.PostForm("code")
	if code == "" {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "缺少 code"))
		return
	}
	data, err := s.state.TakeAuthCode(ctx, code)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if data == nil {
		httpx.Error(c, httpx.New("INVALID_GRANT", "授权码无效或已被使用"))
		return
	}
	if data.ClientID != clientID {
		httpx.Error(c, httpx.New("INVALID_GRANT", "授权码与客户端不匹配"))
		return
	}
	if c.PostForm("redirect_uri") != data.RedirectURI {
		httpx.Error(c, httpx.New("INVALID_GRANT", "redirect_uri 与授权请求不一致"))
		return
	}
	if data.CodeChallenge != "" {
		verifier := c.PostForm("code_verifier")
		if verifier == "" {
			httpx.Error(c, httpx.New("INVALID_CODE_VERIFIER", "缺少 code_verifier"))
			return
		}
		if pkceS256(verifier) != data.CodeChallenge {
			httpx.Error(c, httpx.New("PKCE_VERIFICATION_FAILED", "PKCE 校验失败"))
			return
		}
	} else if store.IsPublicClient(client) {
		httpx.Error(c, httpx.New("INVALID_CODE_VERIFIER", "公共客户端必须使用 PKCE"))
		return
	}

	userID, err := uuid.Parse(data.UserID)
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_GRANT", "授权码数据损坏"))
		return
	}
	sessionID, err := uuid.Parse(data.SessionID)
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_GRANT", "授权码数据损坏"))
		return
	}

	resp, err := s.issueTokens(ctx, tokenIssueParams{
		UserID:    userID,
		Client:    client,
		SessionID: sessionID,
		ACR:       data.ACR,
		AMR:       data.AMR,
		Scope:     data.Scope,
		Nonce:     data.Nonce,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (s *Server) tokenRefresh(c *gin.Context) {
	ctx := c.Request.Context()
	clientID, secret := clientCredentials(c)
	if clientID == "" {
		httpx.Error(c, httpx.New("INVALID_CLIENT", "缺少 client_id"))
		return
	}
	client, err := s.clients.GetByID(ctx, clientID)
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_CLIENT", "客户端不存在"))
		return
	}
	if !store.IsPublicClient(client) && !s.clients.VerifySecret(client, secret) {
		httpx.Error(c, httpx.New("INVALID_CLIENT", "客户端密钥校验失败"))
		return
	}

	refreshToken := c.PostForm("refresh_token")
	if refreshToken == "" {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "缺少 refresh_token"))
		return
	}

	sessionIDStr, ok, err := s.state.LookupRefresh(ctx, refreshToken)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if !ok {
		httpx.Error(c, httpx.New("INVALID_GRANT", "refresh_token 无效"))
		return
	}
	sessionID, err := uuid.Parse(sessionIDStr)
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_GRANT", "refresh_token 无效"))
		return
	}

	current, err := s.state.CurrentSessionRefresh(ctx, sessionIDStr)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if current == "" {
		httpx.Error(c, httpx.New("INVALID_GRANT", "refresh_token 已失效"))
		return
	}
	if current != refreshToken {
		_ = s.sessionMgr.Revoke(ctx, sessionID, "refresh_reuse")
		_ = s.state.RevokeRefreshFamily(ctx, sessionIDStr)
		httpx.Error(c, httpx.New("REFRESH_TOKEN_REUSED", "refresh_token 重复使用，已撤销会话"))
		return
	}

	sess, err := s.sessions.GetByID(ctx, sessionID)
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_GRANT", "会话不存在"))
		return
	}
	if !session.IsActive(sess) {
		httpx.Error(c, httpx.New("TOKEN_REVOKED", "会话已失效"))
		return
	}
	if sess.ClientID != clientID {
		httpx.Error(c, httpx.New("INVALID_GRANT", "refresh_token 与客户端不匹配"))
		return
	}

	resp, err := s.issueTokens(ctx, tokenIssueParams{
		UserID:        sess.UserID,
		Client:        client,
		SessionID:     sess.ID,
		ACR:           sess.ACR,
		AMR:           []string(sess.AMR),
		Scope:         c.PostForm("scope"),
		RotateFrom:    refreshToken,
		RotateSession: sessionIDStr,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

type tokenIssueParams struct {
	UserID        uuid.UUID
	Client        *model.Client
	SessionID     uuid.UUID
	ACR           string
	AMR           []string
	Scope         string
	Nonce         string
	RotateFrom    string
	RotateSession string
}

func (s *Server) issueTokens(ctx context.Context, p tokenIssueParams) (*tokenResponse, error) {
	kid, priv, err := s.keys.ActiveSigner(ctx, crypto.PurposeOIDC)
	if err != nil {
		return nil, err
	}

	policy, err := s.policies.GetEffective(ctx, p.Client.ClientID, p.Client.Tier)
	if err != nil {
		return nil, err
	}
	var accessTTL, refreshTTL time.Duration
	if policy != nil {
		accessTTL = durationFromPolicy(policy.AccessTokenTTLSec, defaultAccessTokenTTL)
		refreshTTL = durationFromPolicy(policy.RefreshTokenTTLSec, defaultRefreshTokenTTL)
	}
	if accessTTL <= 0 {
		accessTTL = defaultAccessTokenTTL
	}
	if refreshTTL <= 0 {
		refreshTTL = defaultRefreshTokenTTL
	}

	var username, displayName string
	if user, err := s.users.GetByID(ctx, p.UserID); err == nil {
		username = user.Username
		if user.DisplayName != nil {
			displayName = *user.DisplayName
		}
	}
	roles, err := s.roles.ActiveRolesForUser(ctx, p.UserID, p.Client.ClientID)
	if err != nil {
		return nil, err
	}
	if roles == nil {
		roles = []string{}
	}

	now := time.Now().UTC()
	issuer := s.issuer()
	sid := p.SessionID.String()
	sub := p.UserID.String()

	accessJTI, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	accessClaims := jwt.MapClaims{
		"iss":                issuer,
		"sub":                sub,
		"aud":                p.Client.ClientID,
		"exp":                now.Add(accessTTL).Unix(),
		"iat":                now.Unix(),
		"jti":                accessJTI.String(),
		"sid":                sid,
		"acr":                p.ACR,
		"amr":                p.AMR,
		"roles":              roles,
		"scope":              p.Scope,
		"preferred_username": username,
	}
	if displayName != "" {
		accessClaims["name"] = displayName
	}
	accessToken, err := signJWT(accessClaims, kid, priv)
	if err != nil {
		return nil, err
	}

	idJTI, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	idClaims := jwt.MapClaims{
		"iss":       issuer,
		"sub":       sub,
		"aud":       p.Client.ClientID,
		"exp":       now.Add(accessTTL).Unix(),
		"iat":       now.Unix(),
		"auth_time": now.Unix(),
		"jti":       idJTI.String(),
		"acr":       p.ACR,
		"amr":       p.AMR,
	}
	if p.Nonce != "" {
		idClaims["nonce"] = p.Nonce
	}
	idToken, err := signJWT(idClaims, kid, priv)
	if err != nil {
		return nil, err
	}

	refreshToken, err := newOpaqueToken()
	if err != nil {
		return nil, err
	}
	if p.RotateFrom != "" && p.RotateSession != "" {
		if err := s.state.RotateRefresh(ctx, p.RotateSession, p.RotateFrom, refreshToken, refreshTTL); err != nil {
			return nil, err
		}
	} else {
		if err := s.state.IssueRefresh(ctx, sid, refreshToken, refreshTTL); err != nil {
			return nil, err
		}
	}

	return &tokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    int(accessTTL.Seconds()),
		RefreshToken: refreshToken,
		IDToken:      idToken,
		Scope:        p.Scope,
	}, nil
}

func signJWT(claims jwt.MapClaims, kid string, priv *crypto.SM2PrivateKey) (string, error) {
	token := jwt.NewWithClaims(crypto.SigningMethodSM3WithSM2, claims)
	token.Header["kid"] = kid
	return token.SignedString(priv)
}

func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
