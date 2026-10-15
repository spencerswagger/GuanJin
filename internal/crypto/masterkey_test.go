package crypto

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func testKey(n int) []byte {
	key := make([]byte, n)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func TestParseMasterKeyValid(t *testing.T) {
	raw := testKey(32)
	encoded := base64.StdEncoding.EncodeToString(raw)

	got, err := ParseMasterKey(encoded)
	require.NoError(t, err)
	require.Equal(t, raw, got)
}

func TestParseMasterKeyNotBase64(t *testing.T) {
	_, err := ParseMasterKey("!!!not-base64!!!")
	require.Error(t, err)
}

func TestParseMasterKeyTooShort(t *testing.T) {
	_, err := ParseMasterKey(base64.StdEncoding.EncodeToString(testKey(16)))
	require.Error(t, err)
}

func TestDeriveIsDeterministic(t *testing.T) {
	key := testKey(32)
	a := Derive(key, "guanjin/sm4/field", 16)
	b := Derive(key, "guanjin/sm4/field", 16)
	require.Equal(t, a, b)
	require.Len(t, a, 16)
}

func TestDerivePurposeSeparated(t *testing.T) {
	key := testKey(32)
	audit := Derive(key, "guanjin/hmac/audit", 32)
	blind := Derive(key, "guanjin/hmac/blind-index", 32)
	require.False(t, bytes.Equal(audit, blind))
}

func TestDeriveDifferentMasterKeysDiffer(t *testing.T) {
	a := Derive(testKey(32), "guanjin/sm4/field", 16)
	other := testKey(32)
	other[0] ^= 0xff
	b := Derive(other, "guanjin/sm4/field", 16)
	require.False(t, bytes.Equal(a, b))
}

func TestDeriveLength(t *testing.T) {
	key := testKey(32)
	require.Len(t, Derive(key, "p", 1), 1)
	require.Len(t, Derive(key, "p", 48), 48)
	require.Nil(t, Derive(key, "p", 0))
}

func TestKeysReady(t *testing.T) {
	require.True(t, KeysReady(testKey(32)))
	require.False(t, KeysReady(testKey(16)))
}
