package crypto

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type sdkVectorCase struct {
	File     string
	Claims   jwt.MapClaims
	Expected string
}

func TestExportSDKVector(t *testing.T) {
	dir := os.Getenv("SDK_VECTOR_DIR")
	if dir == "" {
		t.Skip("SDK_VECTOR_DIR not set; skip exporting SDK interop vectors")
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))

	priv, err := GenerateSM2Keypair()
	require.NoError(t, err)
	kid := "gw:sdk-vector-0001"

	cases := []sdkVectorCase{
		{
			File: "assertion.jwt",
			Claims: jwt.MapClaims{
				"iss": "guanjin-gateway", "sub": "u_12345", "aud": "order-service",
				"cid": "order-portal", "uname": "zhangsan", "roles": []string{"ADMIN"},
				"acr": "AAL2", "amr": []string{"pwd", "otp"},
				"iat": int64(1730000000), "exp": int64(4102444800),
				"jti": "2f8c1b90-2a3d-4e5f-8091-a2b3c4d5e6f7",
			},
		},
		{
			File: "assertion_viewer.jwt",
			Claims: jwt.MapClaims{
				"iss": "guanjin-gateway", "sub": "u_20002", "aud": "order-service",
				"cid": "order-portal", "uname": "lisi", "roles": []string{"order_viewer"},
				"acr": "AAL1", "amr": []string{"pwd"},
				"iat": int64(1730000000), "exp": int64(4102444800),
				"jti": "8a1c2d3e-4f50-4617-8829-0a1b2c3d4e5f",
			},
		},
		{
			File: "assertion_norole.jwt",
			Claims: jwt.MapClaims{
				"iss": "guanjin-gateway", "sub": "u_30003", "aud": "order-service",
				"cid": "order-portal", "uname": "wangwu", "roles": []string{},
				"acr": "AAL1", "amr": []string{"pwd"},
				"iat": int64(1730000000), "exp": int64(4102444800),
				"jti": "c7d8e9f0-1a2b-4c3d-9e4f-5a6b7c8d9e0f",
			},
		},
		{
			File: "assertion_expired.jwt",
			Claims: jwt.MapClaims{
				"iss": "guanjin-gateway", "sub": "u_40004", "aud": "order-service",
				"cid": "order-portal", "uname": "zhaoliu", "roles": []string{"order_viewer"},
				"acr": "AAL1", "amr": []string{"pwd"},
				"iat": int64(1730000000), "exp": int64(1730000060),
				"jti": "d1e2f3a4-b5c6-4d7e-8f90-1a2b3c4d5e6f",
			},
		},
		{
			File: "assertion_wrongaud.jwt",
			Claims: jwt.MapClaims{
				"iss": "guanjin-gateway", "sub": "u_50005", "aud": "other-service",
				"cid": "order-portal", "uname": "sunqi", "roles": []string{"order_viewer"},
				"acr": "AAL1", "amr": []string{"pwd"},
				"iat": int64(1730000000), "exp": int64(4102444800),
				"jti": "e2f3a4b5-c6d7-4e8f-9a01-2b3c4d5e6f70",
			},
		},
		{
			File: "assertion_wrongiss.jwt",
			Claims: jwt.MapClaims{
				"iss": "other-issuer", "sub": "u_60006", "aud": "order-service",
				"cid": "order-portal", "uname": "zhouba", "roles": []string{"order_viewer"},
				"acr": "AAL1", "amr": []string{"pwd"},
				"iat": int64(1730000000), "exp": int64(4102444800),
				"jti": "f3a4b5c6-d7e8-4f90-8a12-3c4d5e6f7081",
			},
		},
	}

	tokens := map[string]string{}
	for _, tc := range cases {
		token := jwt.NewWithClaims(SigningMethodSM3WithSM2, tc.Claims)
		token.Header["kid"] = kid
		signed, signErr := token.SignedString(priv)
		require.NoError(t, signErr)
		require.NoError(t, os.WriteFile(filepath.Join(dir, tc.File), []byte(signed), 0o644))
		tokens[tc.File] = signed
	}

	x, y := ExportPublicKeyXY(&priv.PublicKey)
	jwks := JWKSDocument{Keys: []JWK{{
		Kty: "EC", Use: "sig", Kid: kid, Alg: AlgoSM2, Crv: CurveSM2,
		X: base64.RawURLEncoding.EncodeToString(x),
		Y: base64.RawURLEncoding.EncodeToString(y),
	}}}
	jwksBytes, err := json.MarshalIndent(jwks, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "jwks.json"), jwksBytes, 0o644))

	admin := tokens["assertion.jwt"]
	parts := strings.Split(admin, ".")
	require.Len(t, parts, 3)

	meta := map[string]interface{}{
		"kid":            kid,
		"alg":            AlgoSM3WithSM2,
		"issuer":         "guanjin-gateway",
		"audience":       "order-service",
		"signing_input":  parts[0] + "." + parts[1],
		"claims":         cases[0].Claims,
		"tokens":         tokens,
		"generated_by":   "internal/crypto TestExportSDKVector",
		"signature_note": "digest=SM3(signing_input); SM2 signs the raw 32-byte digest (no ZA)",
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), metaBytes, 0o644))

	for _, tc := range cases {
		parsed, parseErr := jwt.Parse(tokens[tc.File], func(tk *jwt.Token) (interface{}, error) {
			return &priv.PublicKey, nil
		}, jwt.WithValidMethods([]string{AlgoSM3WithSM2}), jwt.WithoutClaimsValidation())
		require.NoError(t, parseErr)
		require.True(t, parsed.Valid)
	}
}
