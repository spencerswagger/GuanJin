package crypto

import (
	"bytes"
	"crypto/hmac"
	"errors"
	"strings"
	"testing"

	"github.com/tjfoc/gmsm/sm3"
)

func okParams() PasswordParams {
	return PasswordParams{Iterations: MinPBKDF2Iterations, KeyLen: 32}
}

// PBKDF2-SM3 必须与 RFC 8018 的参考实现（stdlib hmac + SM3）逐字节一致。
func TestDerivePasswordMatchesReference(t *testing.T) {
	salt := bytes.Repeat([]byte{0x11}, MinPasswordSaltLen)
	params := okParams()
	password := []byte("CorrectHorseBatteryStaple-2026")

	got, err := DerivePassword(password, salt, params)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(got) != params.KeyLen {
		t.Fatalf("derived length = %d, want %d", len(got), params.KeyLen)
	}
	if want := referencePBKDF2(password, salt, params.Iterations, params.KeyLen); !hmac.Equal(got, want) {
		t.Fatal("PBKDF2-SM3 does not match RFC 8018 reference")
	}
}

// 多块输出（keyLen > 32）也要对：块索引与异或链最易写错。
func TestDerivePasswordMultiBlock(t *testing.T) {
	salt := bytes.Repeat([]byte{0x22}, MinPasswordSaltLen)
	params := PasswordParams{Iterations: MinPBKDF2Iterations, KeyLen: 64}
	password := []byte("pw")
	got, err := DerivePassword(password, salt, params)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if want := referencePBKDF2(password, salt, params.Iterations, params.KeyLen); !hmac.Equal(got, want) {
		t.Fatal("multi-block PBKDF2-SM3 mismatch")
	}
}

// 参数下限：迭代 < 100000、盐 < 16B、输出 < 32B 一律拒绝（禁"顺手降参数"）。
func TestPasswordParamsFloor(t *testing.T) {
	salt := bytes.Repeat([]byte{0x33}, MinPasswordSaltLen)
	cases := []struct {
		name   string
		salt   []byte
		params PasswordParams
	}{
		{"short salt", bytes.Repeat([]byte{0x33}, MinPasswordSaltLen-1), okParams()},
		{"no salt", nil, okParams()},
		{"iterations too low", salt, PasswordParams{Iterations: MinPBKDF2Iterations - 1, KeyLen: 32}},
		{"zero iterations", salt, PasswordParams{Iterations: 0, KeyLen: 32}},
		{"key too short", salt, PasswordParams{Iterations: MinPBKDF2Iterations, KeyLen: 16}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DerivePassword([]byte("pw"), tc.salt, tc.params); !errors.Is(err, ErrWeakPBKDF2Params) {
				t.Fatalf("err = %v, want ErrWeakPBKDF2Params", err)
			}
		})
	}
	if _, err := DerivePassword(nil, salt, okParams()); !errors.Is(err, ErrEmptyPassword) {
		t.Fatalf("empty password err = %v, want ErrEmptyPassword", err)
	}
}

// 盐不同 → 结果不同（盐真的参与了运算）；同输入 → 确定。
func TestDerivePasswordSensitivity(t *testing.T) {
	p := okParams()
	saltA := bytes.Repeat([]byte{0x01}, MinPasswordSaltLen)
	saltB := bytes.Repeat([]byte{0x02}, MinPasswordSaltLen)

	a1, _ := DerivePassword([]byte("pw"), saltA, p)
	a2, _ := DerivePassword([]byte("pw"), saltA, p)
	b, _ := DerivePassword([]byte("pw"), saltB, p)
	c, _ := DerivePassword([]byte("pw2"), saltA, p)

	if !hmac.Equal(a1, a2) {
		t.Fatal("same inputs must derive the same key")
	}
	if hmac.Equal(a1, b) || hmac.Equal(a1, c) {
		t.Fatal("salt/password must affect the derived key")
	}
}

func TestVerifyPassword(t *testing.T) {
	p := okParams()
	salt, err := NewPasswordSalt(nil, MinPasswordSaltLen)
	if err != nil {
		t.Fatalf("salt: %v", err)
	}
	expected, err := DerivePassword([]byte("pw"), salt, p)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}

	ok, err := VerifyPassword([]byte("pw"), salt, expected, p)
	if err != nil || !ok {
		t.Fatalf("correct password must verify, got %v %v", ok, err)
	}
	ok, err = VerifyPassword([]byte("PW"), salt, expected, p)
	if err != nil || ok {
		t.Fatalf("wrong password must not verify, got %v %v", ok, err)
	}
	// 长度不同的期望值直接判否（不 panic、不越界）。
	ok, err = VerifyPassword([]byte("pw"), salt, expected[:8], p)
	if err != nil || ok {
		t.Fatalf("truncated expected must not verify, got %v %v", ok, err)
	}
}

func TestNewPasswordSalt(t *testing.T) {
	if _, err := NewPasswordSalt(nil, MinPasswordSaltLen-1); !errors.Is(err, ErrWeakPBKDF2Params) {
		t.Fatalf("short salt length must be rejected")
	}
	a, err := NewPasswordSalt(nil, MinPasswordSaltLen)
	if err != nil {
		t.Fatalf("salt: %v", err)
	}
	b, _ := NewPasswordSalt(nil, MinPasswordSaltLen)
	if len(a) != MinPasswordSaltLen {
		t.Fatalf("salt length = %d, want %d", len(a), MinPasswordSaltLen)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two salts must differ (random)")
	}
	if bytes.Equal(a, make([]byte, len(a))) {
		t.Fatal("salt must not be all zero")
	}
	long, err := NewPasswordSalt(nil, 32)
	if err != nil || len(long) != 32 {
		t.Fatalf("explicit longer salt must be honored: %d, %v", len(long), err)
	}
}

func TestPasswordParamsValidate(t *testing.T) {
	if err := okParams().Validate(); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
	bad := PasswordParams{Iterations: 1000, KeyLen: 32}
	if err := bad.Validate(); !errors.Is(err, ErrWeakPBKDF2Params) {
		t.Fatalf("weak params err = %v", err)
	}
	if msg := bad.Validate().Error(); !strings.Contains(msg, "iterations") {
		t.Fatalf("error message must say which parameter is weak: %s", msg)
	}
}

// referencePBKDF2 是 RFC 8018 §5.2 的独立实现（stdlib hmac + SM3），用于对拍。
func referencePBKDF2(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sm3.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen

	out := make([]byte, 0, blocks*hashLen)
	u := make([]byte, hashLen)
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		prf.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u = prf.Sum(u[:0])
		t := make([]byte, hashLen)
		copy(t, u)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for i := range t {
				t[i] ^= u[i]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}
