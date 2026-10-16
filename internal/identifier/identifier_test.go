package identifier

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeEmail(t *testing.T) {
	got, err := Normalize(TypeEmail, "Alice@Example.COM ")
	require.NoError(t, err)
	require.Equal(t, "alice@example.com", got)

	_, err = Normalize(TypeEmail, "not-an-email")
	require.ErrorIs(t, err, ErrInvalidFormat)
}

func TestNormalizePhoneE164(t *testing.T) {
	got, err := Normalize(TypePhone, "+86 138-1234-8888")
	require.NoError(t, err)
	require.Equal(t, "+8613812348888", got)

	_, err = Normalize(TypePhone, "123")
	require.ErrorIs(t, err, ErrInvalidFormat)
}

func TestNormalizeUsername(t *testing.T) {
	got, err := Normalize(TypeUsername, "  Alice_A  ")
	require.NoError(t, err)
	require.Equal(t, "alice_a", got)

	_, err = Normalize(TypeUsername, "ab")
	require.ErrorIs(t, err, ErrInvalidFormat)
}

func TestMask(t *testing.T) {
	require.Equal(t, "138****8888", Mask(TypePhone, "+8613812348888"))
	require.Equal(t, "a***@x.com", Mask(TypeEmail, "alice@x.com"))
}

func TestBlindIndexDeterministicAndEncrypted(t *testing.T) {
	masterKey := bytes.Repeat([]byte{7}, 32)
	idx := NewBlindIndexer(masterKey)

	a := idx.Value("13812348888")
	b := idx.Value("13812348888")
	require.Equal(t, a, b)
	require.NotEqual(t, a, idx.Value("13812348889"))

	enc, err := idx.Encrypt("13812348888")
	require.NoError(t, err)
	require.NotEqual(t, []byte("13812348888"), enc)

	plain, err := idx.Decrypt(enc)
	require.NoError(t, err)
	require.Equal(t, "13812348888", plain)
}
