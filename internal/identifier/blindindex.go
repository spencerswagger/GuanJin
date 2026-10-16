package identifier

import (
	"strings"

	"github.com/spencerswagger/GuanJin/internal/crypto"
)

type BlindIndexer struct {
	hmacKey []byte
	sm4Key  []byte
}

func NewBlindIndexer(masterKey []byte) *BlindIndexer {
	return &BlindIndexer{
		hmacKey: crypto.Derive(masterKey, "guanjin/hmac/blind-index", 32),
		sm4Key:  crypto.Derive(masterKey, "guanjin/sm4/field", 16),
	}
}

func (b *BlindIndexer) Value(normalized string) []byte {
	return crypto.HMACSM3(b.hmacKey, []byte(normalized))
}

func (b *BlindIndexer) Encrypt(value string) ([]byte, error) {
	return crypto.SM4Encrypt(b.sm4Key, []byte(value))
}

func (b *BlindIndexer) Decrypt(enc []byte) (string, error) {
	raw, err := crypto.SM4Decrypt(b.sm4Key, enc)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func Mask(typ, value string) string {
	switch typ {
	case TypeEmail:
		return maskEmail(value)
	case TypePhone:
		return maskPhone(value)
	default:
		return maskGeneric(value)
	}
}

func maskEmail(value string) string {
	at := strings.IndexByte(value, '@')
	if at <= 0 {
		return maskGeneric(value)
	}
	local := value[:at]
	domain := value[at:]
	if len(local) == 0 {
		return maskGeneric(value)
	}
	return string(local[0]) + "***" + domain
}

func maskPhone(value string) string {
	digits := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		if value[i] >= '0' && value[i] <= '9' {
			digits = append(digits, value[i])
		}
	}
	if len(digits) > 11 {
		digits = digits[len(digits)-11:]
	}
	if len(digits) < 7 {
		return maskGeneric(string(digits))
	}
	return string(digits[:3]) + "****" + string(digits[len(digits)-4:])
}

func maskGeneric(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 1 {
		return "***"
	}
	return string(value[0]) + "***"
}
