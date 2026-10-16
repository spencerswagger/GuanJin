package crypto

import (
	"crypto/rand"

	"github.com/tjfoc/gmsm/sm3"
	"golang.org/x/crypto/pbkdf2"
)

const (
	PBKDF2Algo          = "PBKDF2-SM3"
	MinPBKDF2Iterations = 100000
	MinPBKDF2SaltBytes  = 16
	DefaultPBKDF2KeyLen = 32
)

func PBKDF2SM3(password, salt []byte, iterations, keyLen int) []byte {
	if iterations < MinPBKDF2Iterations {
		iterations = MinPBKDF2Iterations
	}
	if keyLen < DefaultPBKDF2KeyLen {
		keyLen = DefaultPBKDF2KeyLen
	}
	return pbkdf2.Key(password, salt, iterations, keyLen, sm3.New)
}

func NewPBKDF2Salt() ([]byte, error) {
	salt := make([]byte, MinPBKDF2SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return salt, nil
}
