package crypto

import (
	"crypto/cipher"
	"crypto/rand"
	"errors"

	"github.com/tjfoc/gmsm/sm4"
)

const (
	SM4KeySize   = 16
	SM4BlockSize = 16
)

var (
	ErrSM4InvalidKey        = errors.New("sm4: key must be 16 bytes")
	ErrSM4InvalidCiphertext = errors.New("sm4: invalid ciphertext")
	ErrSM4InvalidPadding    = errors.New("sm4: invalid padding")
)

func SM4Encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := sm4.NewCipher(key)
	if err != nil {
		return nil, ErrSM4InvalidKey
	}
	iv := make([]byte, SM4BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plaintext, SM4BlockSize)
	out := make([]byte, SM4BlockSize+len(padded))
	copy(out, iv)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out[SM4BlockSize:], padded)
	return out, nil
}

func SM4Decrypt(key, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < SM4BlockSize || (len(ciphertext)-SM4BlockSize)%SM4BlockSize != 0 {
		return nil, ErrSM4InvalidCiphertext
	}
	block, err := sm4.NewCipher(key)
	if err != nil {
		return nil, ErrSM4InvalidKey
	}
	iv := ciphertext[:SM4BlockSize]
	body := ciphertext[SM4BlockSize:]
	out := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, body)
	return pkcs7Unpad(out, SM4BlockSize)
}

func pkcs7Pad(src []byte, blockSize int) []byte {
	padLen := blockSize - len(src)%blockSize
	padded := make([]byte, len(src)+padLen)
	copy(padded, src)
	for i := len(src); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	return padded
}

func pkcs7Unpad(src []byte, blockSize int) ([]byte, error) {
	if len(src) == 0 || len(src)%blockSize != 0 {
		return nil, ErrSM4InvalidPadding
	}
	padLen := int(src[len(src)-1])
	if padLen == 0 || padLen > blockSize || padLen > len(src) {
		return nil, ErrSM4InvalidPadding
	}
	for _, b := range src[len(src)-padLen:] {
		if int(b) != padLen {
			return nil, ErrSM4InvalidPadding
		}
	}
	return src[:len(src)-padLen], nil
}
