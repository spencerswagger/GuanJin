package crypto

import (
	"encoding/hex"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestSM3KnownVector(t *testing.T) {
	got := hex.EncodeToString(Sum([]byte("abc")))
	require.Equal(t, "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0", got)
}

func TestSM3KnownVectorEmpty(t *testing.T) {
	got := hex.EncodeToString(Sum(nil))
	require.Equal(t, "1ab21d8355cfa17f8e61194831e81a8f22bec8c728fefb747ed035eb5082aa2b", got)
}

func TestHMACSM3Deterministic(t *testing.T) {
	key := testKey(32)
	a := HMACSM3(key, []byte("message"))
	b := HMACSM3(key, []byte("message"))
	require.Equal(t, a, b)
	require.NotEqual(t, a, HMACSM3(key, []byte("message2")))
}

func TestSM2SignVerifyRoundTrip(t *testing.T) {
	priv, err := GenerateSM2Keypair()
	require.NoError(t, err)

	digest := Sum([]byte("guanjin-sm2-digest"))
	require.Len(t, digest, SM3DigestSize)

	sig, err := SignSM2Digest(priv, digest)
	require.NoError(t, err)
	require.True(t, VerifySM2Digest(&priv.PublicKey, digest, sig))

	tampered := append([]byte(nil), digest...)
	tampered[0] ^= 0xff
	require.False(t, VerifySM2Digest(&priv.PublicKey, tampered, sig))
}

func TestSM2RejectsWrongDigestLength(t *testing.T) {
	priv, err := GenerateSM2Keypair()
	require.NoError(t, err)
	_, err = SignSM2Digest(priv, []byte("short"))
	require.ErrorIs(t, err, ErrSM2InvalidDigest)
}

func TestSM2KeySerializationRoundTrip(t *testing.T) {
	priv, err := GenerateSM2Keypair()
	require.NoError(t, err)

	raw := MarshalPrivateKey(priv)
	require.Len(t, raw, SM2PrivateKeySize+SM2PublicKeySize)

	restored, err := UnmarshalPrivateKey(raw)
	require.NoError(t, err)

	digest := Sum([]byte("payload"))
	sig, err := SignSM2Digest(restored, digest)
	require.NoError(t, err)
	require.True(t, VerifySM2Digest(&priv.PublicKey, digest, sig))

	pub, err := UnmarshalPublicKey(MarshalPublicKey(&priv.PublicKey))
	require.NoError(t, err)
	require.True(t, VerifySM2Digest(pub, digest, sig))
}

func TestSM4RoundTrip(t *testing.T) {
	key := testKey(SM4KeySize)
	plaintext := []byte("关津 SM4 secret material")

	ct, err := SM4Encrypt(key, plaintext)
	require.NoError(t, err)
	require.NotEqual(t, plaintext, ct)

	got, err := SM4Decrypt(key, ct)
	require.NoError(t, err)
	require.Equal(t, plaintext, got)
}

func TestSM4RandomIVPrefix(t *testing.T) {
	key := testKey(SM4KeySize)
	a, err := SM4Encrypt(key, []byte("same plaintext"))
	require.NoError(t, err)
	b, err := SM4Encrypt(key, []byte("same plaintext"))
	require.NoError(t, err)
	require.NotEqual(t, a, b, "随机 IV 应使密文不同")
}

func TestSM4RejectsInvalidKey(t *testing.T) {
	_, err := SM4Encrypt(testKey(8), []byte("x"))
	require.ErrorIs(t, err, ErrSM4InvalidKey)
}

func TestPBKDF2SM3Deterministic(t *testing.T) {
	salt := testKey(MinPBKDF2SaltBytes)
	a := PBKDF2SM3([]byte("password"), salt, 100000, 32)
	b := PBKDF2SM3([]byte("password"), salt, 100000, 32)
	require.Equal(t, a, b)
	require.Len(t, a, 32)

	c := PBKDF2SM3([]byte("password2"), salt, 100000, 32)
	require.NotEqual(t, a, c)
}

func TestPBKDF2SM3EnforcesMinimums(t *testing.T) {
	salt := testKey(16)
	low := PBKDF2SM3([]byte("password"), salt, 1, 8)
	enforced := PBKDF2SM3([]byte("password"), salt, MinPBKDF2Iterations, DefaultPBKDF2KeyLen)
	require.Len(t, low, DefaultPBKDF2KeyLen)
	require.Equal(t, enforced, low)
}

func TestJWTRegisterAndRoundTrip(t *testing.T) {
	require.NotNil(t, jwt.GetSigningMethod(AlgoSM3WithSM2))

	priv, err := GenerateSM2Keypair()
	require.NoError(t, err)

	token := jwt.NewWithClaims(SigningMethodSM3WithSM2, jwt.MapClaims{"sub": "user-1"})
	signed, err := token.SignedString(priv)
	require.NoError(t, err)

	parsed, err := jwt.Parse(signed, func(tk *jwt.Token) (interface{}, error) {
		return &priv.PublicKey, nil
	}, jwt.WithValidMethods([]string{AlgoSM3WithSM2}))
	require.NoError(t, err)
	require.True(t, parsed.Valid)
	require.Equal(t, "user-1", parsed.Claims.(jwt.MapClaims)["sub"])
}

func TestJWTRejectsTamperedToken(t *testing.T) {
	priv, err := GenerateSM2Keypair()
	require.NoError(t, err)
	other, err := GenerateSM2Keypair()
	require.NoError(t, err)

	token := jwt.NewWithClaims(SigningMethodSM3WithSM2, jwt.MapClaims{"sub": "u"})
	signed, err := token.SignedString(priv)
	require.NoError(t, err)

	_, err = jwt.Parse(signed, func(tk *jwt.Token) (interface{}, error) {
		return &other.PublicKey, nil
	})
	require.Error(t, err)
}

func TestNewJWK(t *testing.T) {
	priv, err := GenerateSM2Keypair()
	require.NoError(t, err)

	jwk := NewJWK("oidc:abc", AlgoSM3WithSM2, &priv.PublicKey)
	require.Equal(t, "EC", jwk.Kty)
	require.Equal(t, "SM2", jwk.Crv)
	require.Equal(t, "sig", jwk.Use)
	require.Equal(t, "oidc:abc", jwk.Kid)
	require.Equal(t, AlgoSM3WithSM2, jwk.Alg)
	require.NotEmpty(t, jwk.X)
	require.NotEmpty(t, jwk.Y)
}

func TestAlgAndKIDPrefixForPurpose(t *testing.T) {
	require.Equal(t, AlgoSM3WithSM2, AlgForPurpose(PurposeOIDC))
	require.Equal(t, AlgoSM2, AlgForPurpose(PurposeGateway))
	require.Equal(t, KIDPrefixOIDC, KIDPrefixForPurpose(PurposeOIDC))
	require.Equal(t, KIDPrefixGateway, KIDPrefixForPurpose(PurposeGateway))
}
