package crypto

import "context"

const (
	KIDNamespaceUserToken = "user_token"
	KIDNamespaceGateway   = "gateway"
)

type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
}

type JWKSDocument struct {
	Keys []JWK `json:"keys"`
}

type Publisher interface {
	JWKS(ctx context.Context) (JWKSDocument, error)
}
