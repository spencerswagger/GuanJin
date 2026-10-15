package crypto

import (
	"encoding/base64"
	"fmt"
	"io"

	"github.com/tjfoc/gmsm/sm3"
	"golang.org/x/crypto/hkdf"
)

const MinMasterKeyBytes = 32

func ParseMasterKey(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("master key is not valid base64: %w", err)
	}
	if len(raw) < MinMasterKeyBytes {
		return nil, fmt.Errorf("master key must be at least %d bytes (got %d)", MinMasterKeyBytes, len(raw))
	}
	return raw, nil
}

func Derive(masterKey []byte, purpose string, length int) []byte {
	if length <= 0 {
		return nil
	}
	salt := []byte("guanjin/" + purpose)
	info := []byte(purpose)
	reader := hkdf.New(sm3.New, masterKey, salt, info)
	out := make([]byte, length)
	_, _ = io.ReadFull(reader, out)
	return out
}

func KeysReady(masterKey []byte) bool {
	if len(masterKey) < MinMasterKeyBytes {
		return false
	}
	sub := Derive(masterKey, "guanjin/health/keys", MinMasterKeyBytes)
	return len(sub) == MinMasterKeyBytes
}
