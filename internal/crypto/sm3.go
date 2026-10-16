package crypto

import (
	"crypto/hmac"

	"github.com/tjfoc/gmsm/sm3"
)

const SM3DigestSize = 32

func Sum(data []byte) []byte {
	h := sm3.New()
	_, _ = h.Write(data)
	return h.Sum(nil)
}

func HMACSM3(key, msg []byte) []byte {
	mac := hmac.New(sm3.New, key)
	_, _ = mac.Write(msg)
	return mac.Sum(nil)
}
