package crypto

import (
	"context"
	"encoding/base64"

	"github.com/tjfoc/gmsm/sm2"
)

const (
	KIDNamespaceUserToken = "user_token"
	KIDNamespaceGateway   = "gateway"

	PurposeOIDC    = "oidc"
	PurposeGateway = "gateway"

	KIDPrefixOIDC    = "oidc:"
	KIDPrefixGateway = "gw:"

	AlgoSM2 = "SM2"

	CurveSM2 = "SM2"
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

func AlgForPurpose(purpose string) string {
	if purpose == PurposeGateway {
		return AlgoSM2
	}
	return AlgoSM3WithSM2
}

func KIDPrefixForPurpose(purpose string) string {
	if purpose == PurposeGateway {
		return KIDPrefixGateway
	}
	return KIDPrefixOIDC
}

func NewJWK(kid, alg string, pub *sm2.PublicKey) JWK {
	x, y := ExportPublicKeyXY(pub)
	return JWK{
		Kty: "EC",
		Use: "sig",
		Kid: kid,
		Alg: alg,
		Crv: CurveSM2,
		X:   base64.RawURLEncoding.EncodeToString(x),
		Y:   base64.RawURLEncoding.EncodeToString(y),
	}
}
