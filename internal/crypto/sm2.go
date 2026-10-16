package crypto

import (
	"crypto/rand"
	"encoding/asn1"
	"errors"
	"math/big"

	"github.com/tjfoc/gmsm/sm2"
)

const (
	SM2PrivateKeySize = 32
	SM2PublicKeySize  = 64
)

var (
	ErrSM2InvalidKey      = errors.New("sm2: invalid key")
	ErrSM2InvalidDigest   = errors.New("sm2: digest must be 32 bytes")
	ErrSM2InvalidEncoding = errors.New("sm2: invalid key encoding")
)

type SM2PrivateKey = sm2.PrivateKey

type SM2PublicKey = sm2.PublicKey

type sm2Signature struct {
	R, S *big.Int
}

func GenerateSM2Keypair() (*sm2.PrivateKey, error) {
	return sm2.GenerateKey(rand.Reader)
}

func SignSM2Digest(priv *sm2.PrivateKey, digest []byte) ([]byte, error) {
	if priv == nil || priv.D == nil || priv.Curve == nil {
		return nil, ErrSM2InvalidKey
	}
	if len(digest) != SM3DigestSize {
		return nil, ErrSM2InvalidDigest
	}
	c := priv.Curve
	n := c.Params().N
	one := big.NewInt(1)
	e := new(big.Int).SetBytes(digest)
	var r, s *big.Int
	for {
		k, err := rand.Int(rand.Reader, n)
		if err != nil {
			return nil, err
		}
		if k.Sign() == 0 {
			continue
		}
		x1, _ := c.ScalarBaseMult(k.Bytes())
		r = new(big.Int).Add(x1, e)
		r.Mod(r, n)
		if r.Sign() == 0 {
			continue
		}
		dPlusOneInv := new(big.Int).ModInverse(new(big.Int).Add(priv.D, one), n)
		if dPlusOneInv == nil {
			continue
		}
		s = new(big.Int).Mul(dPlusOneInv, new(big.Int).Sub(k, new(big.Int).Mul(r, priv.D)))
		s.Mod(s, n)
		if s.Sign() == 0 {
			continue
		}
		break
	}
	return asn1.Marshal(sm2Signature{R: r, S: s})
}

func VerifySM2Digest(pub *sm2.PublicKey, digest, sigDER []byte) bool {
	if pub == nil || pub.Curve == nil || pub.X == nil || pub.Y == nil {
		return false
	}
	if len(digest) != SM3DigestSize {
		return false
	}
	var sig sm2Signature
	if _, err := asn1.Unmarshal(sigDER, &sig); err != nil {
		return false
	}
	if sig.R == nil || sig.S == nil {
		return false
	}
	return sm2.Verify(pub, digest, sig.R, sig.S)
}

func ExportPublicKeyXY(pub *sm2.PublicKey) (x, y []byte) {
	if pub == nil || pub.X == nil || pub.Y == nil {
		return nil, nil
	}
	return pub.X.FillBytes(make([]byte, SM2PrivateKeySize)), pub.Y.FillBytes(make([]byte, SM2PrivateKeySize))
}

func MarshalPublicKey(pub *sm2.PublicKey) []byte {
	if pub == nil || pub.X == nil || pub.Y == nil {
		return nil
	}
	out := make([]byte, 0, SM2PublicKeySize)
	out = append(out, pub.X.FillBytes(make([]byte, SM2PrivateKeySize))...)
	out = append(out, pub.Y.FillBytes(make([]byte, SM2PrivateKeySize))...)
	return out
}

func UnmarshalPublicKey(raw []byte) (*sm2.PublicKey, error) {
	if len(raw) != SM2PublicKeySize {
		return nil, ErrSM2InvalidEncoding
	}
	curve := sm2.P256Sm2()
	pub := &sm2.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(raw[:SM2PrivateKeySize]),
		Y:     new(big.Int).SetBytes(raw[SM2PrivateKeySize:]),
	}
	if pub.X.Sign() == 0 && pub.Y.Sign() == 0 {
		return nil, ErrSM2InvalidEncoding
	}
	return pub, nil
}

func MarshalPrivateKey(priv *sm2.PrivateKey) []byte {
	if priv == nil || priv.D == nil {
		return nil
	}
	out := make([]byte, 0, SM2PrivateKeySize+SM2PublicKeySize)
	out = append(out, priv.D.FillBytes(make([]byte, SM2PrivateKeySize))...)
	out = append(out, MarshalPublicKey(&priv.PublicKey)...)
	return out
}

func UnmarshalPrivateKey(raw []byte) (*sm2.PrivateKey, error) {
	if len(raw) != SM2PrivateKeySize+SM2PublicKeySize {
		return nil, ErrSM2InvalidEncoding
	}
	curve := sm2.P256Sm2()
	priv := &sm2.PrivateKey{
		PublicKey: sm2.PublicKey{
			Curve: curve,
			X:     new(big.Int).SetBytes(raw[SM2PrivateKeySize : SM2PrivateKeySize+SM2PrivateKeySize]),
			Y:     new(big.Int).SetBytes(raw[SM2PrivateKeySize+SM2PrivateKeySize:]),
		},
		D: new(big.Int).SetBytes(raw[:SM2PrivateKeySize]),
	}
	if priv.D.Sign() == 0 {
		return nil, ErrSM2InvalidEncoding
	}
	return priv, nil
}
