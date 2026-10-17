package protocol

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/httpx"
)

const (
	ctxKeyUserID = "guanjin_user_id"
	ctxKeyACR    = "guanjin_acr"
	ctxKeyAMR    = "guanjin_amr"
	ctxKeyScope  = "guanjin_scope"
)

func (s *Server) UserAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := trimBearer(c.GetHeader("Authorization"))
		if raw == "" {
			httpx.Error(c, httpx.New("TOKEN_INVALID", "缺少 Bearer 令牌"))
			c.Abort()
			return
		}
		_, priv, err := s.keys.ActiveSigner(c.Request.Context(), crypto.PurposeOIDC)
		if err != nil {
			httpx.Error(c, err)
			c.Abort()
			return
		}
		parsed, err := jwt.Parse(raw, func(*jwt.Token) (interface{}, error) {
			return &priv.PublicKey, nil
		}, jwt.WithValidMethods([]string{crypto.AlgoSM3WithSM2}))
		if err != nil {
			if errors.Is(err, jwt.ErrTokenExpired) {
				httpx.Error(c, httpx.New("TOKEN_EXPIRED", "令牌已过期"))
			} else {
				httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌无效"))
			}
			c.Abort()
			return
		}
		claims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌声明无效"))
			c.Abort()
			return
		}
		sub, _ := claims["sub"].(string)
		if _, err := uuid.Parse(sub); err != nil {
			httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌主体无效"))
			c.Abort()
			return
		}
		c.Set(ctxKeyUserID, sub)
		c.Set(ctxKeyACR, claimString(claims["acr"]))
		c.Set(ctxKeyAMR, claimStrings(claims["amr"]))
		c.Set(ctxKeyScope, claimString(claims["scope"]))
		c.Next()
	}
}

func userIDFromContext(c *gin.Context) string {
	return c.GetString(ctxKeyUserID)
}

func acrFromContext(c *gin.Context) string {
	return c.GetString(ctxKeyACR)
}

func amrFromContext(c *gin.Context) []string {
	raw, ok := c.Get(ctxKeyAMR)
	if !ok {
		return nil
	}
	values, _ := raw.([]string)
	return values
}

func claimString(v any) string {
	s, _ := v.(string)
	return s
}

func claimStrings(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
