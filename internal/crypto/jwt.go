package crypto

import (
	"errors"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tjfoc/gmsm/sm2"
)

const AlgoSM3WithSM2 = "SM3withSM2"

var ErrJWTSigningKeyType = errors.New("jwt: signing key must be *sm2.PrivateKey or *sm2.PublicKey")

var SigningMethodSM3WithSM2 = &signingMethodSM3WithSM2{}

func init() {
	jwt.RegisterSigningMethod(AlgoSM3WithSM2, func() jwt.SigningMethod { return SigningMethodSM3WithSM2 })
}

type signingMethodSM3WithSM2 struct{}

func (m *signingMethodSM3WithSM2) Alg() string { return AlgoSM3WithSM2 }

func (m *signingMethodSM3WithSM2) Sign(signingString string, key interface{}) ([]byte, error) {
	priv, ok := key.(*sm2.PrivateKey)
	if !ok {
		return nil, ErrJWTSigningKeyType
	}
	return SignSM2Digest(priv, Sum([]byte(signingString)))
}

func (m *signingMethodSM3WithSM2) Verify(signingString string, sig []byte, key interface{}) error {
	var pub *sm2.PublicKey
	switch k := key.(type) {
	case *sm2.PublicKey:
		pub = k
	case *sm2.PrivateKey:
		pub = &k.PublicKey
	default:
		return ErrJWTSigningKeyType
	}
	if !VerifySM2Digest(pub, Sum([]byte(signingString)), sig) {
		return jwt.ErrSignatureInvalid
	}
	return nil
}
