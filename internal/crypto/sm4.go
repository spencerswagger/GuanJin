package crypto

import (
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/tjfoc/gmsm/sm4"
)

// SM4BlockSize 是 SM4 分组字节数。
const SM4BlockSize = 16

var (
	// ErrCiphertextTooShort 表示密文短于 IV 前缀（数据被截断）。
	ErrCiphertextTooShort = errors.New("crypto: sm4 ciphertext shorter than IV prefix")
	// ErrCiphertextNotBlockAligned 表示去掉 IV 后的密文不是分组整数倍（数据损坏）。
	ErrCiphertextNotBlockAligned = errors.New("crypto: sm4 ciphertext not block aligned")
	// ErrPaddingInvalid 表示 PKCS7 填充非法（密钥不对或密文被篡改）。
	ErrPaddingInvalid = errors.New("crypto: sm4 pkcs7 padding invalid")
)

// EncryptSM4 用派生密钥加密明文，返回 `IV || 密文`（IV 随机、每次不同）。
//
// 口径（数据模型 §3.3）：`identifiers.value_enc` 是「SM4 密文（随机 IV，含 IV 前缀）」。
// 随机 IV 保证同一明文两次加密得到不同密文——正因为如此，(type,value) 的唯一性
// 只能靠确定性盲索引（HMAC-SM3），不能靠密文本身。
//
// **不做隐式回退**：cipher 构造失败、随机源不可用都返回错误，绝不明文落库。
func EncryptSM4(key SM4Key, plaintext []byte) ([]byte, error) {
	return encryptSM4CBC(key, plaintext, rand.Reader)
}

// encryptSM4CBC 是 EncryptSM4 的可注入随机源版本（仅测试用于固定 IV 做标准向量对拍）。
func encryptSM4CBC(key SM4Key, plaintext []byte, random io.Reader) ([]byte, error) {
	block, err := sm4.NewCipher(key.material[:])
	if err != nil {
		return nil, fmt.Errorf("crypto: sm4 cipher: %w", err)
	}
	iv := make([]byte, SM4BlockSize)
	if _, err := io.ReadFull(random, iv); err != nil {
		return nil, fmt.Errorf("crypto: sm4 iv: %w", err)
	}
	padded := pkcs7Pad(plaintext, SM4BlockSize)
	out := make([]byte, len(iv)+len(padded))
	copy(out, iv)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out[len(iv):], padded)
	return out, nil
}

// DecryptSM4 解密 `IV || 密文`。任何结构性问题（太短、非对齐、填充非法）都返回**各自的**
// 错误：调用方据此 fail-closed（KEY_UNAVAILABLE / 数据损坏），不得返回半截明文。
func DecryptSM4(key SM4Key, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < SM4BlockSize {
		return nil, ErrCiphertextTooShort
	}
	iv, body := ciphertext[:SM4BlockSize], ciphertext[SM4BlockSize:]
	if len(body) == 0 || len(body)%SM4BlockSize != 0 {
		return nil, ErrCiphertextNotBlockAligned
	}

	block, err := sm4.NewCipher(key.material[:])
	if err != nil {
		return nil, fmt.Errorf("crypto: sm4 cipher: %w", err)
	}
	plain := make([]byte, len(body))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, body)
	return pkcs7Unpad(plain, SM4BlockSize)
}

func pkcs7Pad(src []byte, blockSize int) []byte {
	n := blockSize - len(src)%blockSize
	out := make([]byte, len(src)+n)
	copy(out, src)
	for i := len(src); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}

func pkcs7Unpad(src []byte, blockSize int) ([]byte, error) {
	if len(src) == 0 || len(src)%blockSize != 0 {
		return nil, ErrPaddingInvalid
	}
	n := int(src[len(src)-1])
	if n == 0 || n > blockSize || n > len(src) {
		return nil, ErrPaddingInvalid
	}
	for _, c := range src[len(src)-n:] {
		if int(c) != n {
			return nil, ErrPaddingInvalid
		}
	}
	return src[:len(src)-n], nil
}
