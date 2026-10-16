package protocol

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (s *Server) Discovery(c *gin.Context) {
	issuer := s.issuer()
	c.JSON(http.StatusOK, gin.H{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + "/authorize",
		"token_endpoint":                        issuer + "/oauth/token",
		"jwks_uri":                              issuer + "/.well-known/jwks.json",
		"userinfo_endpoint":                     issuer + "/userinfo",
		"revocation_endpoint":                   issuer + "/oauth/revoke",
		"introspection_endpoint":                issuer + "/oauth/introspect",
		"end_session_endpoint":                  issuer + "/logout",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"id_token_signing_alg_values_supported": []string{"SM3withSM2"},
		"acr_values_supported":                  []string{"AAL1", "AAL2"},
		"scopes_supported":                      []string{"openid", "profile", "email", "phone"},
		"claims_supported": []string{
			"sub", "preferred_username", "name", "acr", "amr", "roles", "scope",
		},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
	})
}
