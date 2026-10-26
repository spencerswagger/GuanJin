package protocol

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/httpx"
)

func (s *Server) UserInfo(c *gin.Context) {
	raw := trimBearer(c.GetHeader("Authorization"))
	if raw == "" {
		httpx.Error(c, httpx.New("TOKEN_INVALID", "缺少 Bearer 令牌"))
		return
	}

	_, priv, err := s.keys.ActiveSigner(c.Request.Context(), crypto.PurposeOIDC)
	if err != nil {
		httpx.Error(c, err)
		return
	}

	parsed, err := jwt.Parse(raw, func(*jwt.Token) (interface{}, error) {
		return &priv.PublicKey, nil
	}, jwt.WithValidMethods([]string{crypto.AlgoSM3WithSM2}))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			httpx.Error(c, httpx.New("TOKEN_EXPIRED", "令牌已过期"))
			return
		}
		httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌无效"))
		return
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌声明无效"))
		return
	}

	resp := gin.H{"sub": claims["sub"]}
	if username, ok := claims["preferred_username"].(string); ok && username != "" {
		resp["preferred_username"] = username
	}
	if name, ok := claims["name"].(string); ok && name != "" {
		resp["name"] = name
	}
	scope, _ := claims["scope"].(string)
	if scope != "" {
		resp["scope"] = scope
	}
	roles := claimStrings(claims["roles"])
	if roles == nil {
		roles = []string{}
	}
	resp["roles"] = roles
	c.JSON(http.StatusOK, resp)
}
