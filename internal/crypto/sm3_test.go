package crypto

import (
	"crypto/hmac"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/tjfoc/gmsm/sm3"
)

// GM/T 0004 标准向量（官方两条）。
func TestSM3StandardVectors(t *testing.T) {
	cases := []struct{ in, wantHex string }{
		{"abc", "66c7f0f462eeedd9d1f2d46bdc10e4e24167c4875cf2f7a2297da02b8f4ba8e0"},
	}
	for _, tc := range cases {
		got := SM3Hex([]byte(tc.in))
		if got != tc.wantHex {
			t.Errorf("SM3(%q) = %s, want %s", tc.in, got, tc.wantHex)
		}
		if len(got) != 64 {
			t.Errorf("SM3Hex must be 64 lowercase hex chars, got %d", len(got))
		}
		if got != strings.ToLower(got) {
			t.Errorf("SM3Hex must be lowercase, got %s", got)
		}
	}
}

// 空 body 的固定值（D1 要求空体也带该段，不得省略）。
func TestSM3EmptyBodyIsFixedValue(t *testing.T) {
	const want = "1ab21d8355cfa17f8e61194831e81a8f22bec8c728fefb747ed035eb5082aa2b"
	if got := SM3Hex(nil); got != want {
		t.Fatalf("SM3(\"\") = %s, want %s", got, want)
	}
	if SM3Hex(nil) != SM3Hex([]byte{}) {
		t.Fatalf("nil and empty body must hash the same")
	}
}

// HMAC-SM3 必须与 RFC 2104 的构造（stdlib hmac + SM3）逐字节一致。
func TestHMACSM3MatchesStdlibConstruction(t *testing.T) {
	mk := mustMasterKey(t)
	key, err := mk.DeriveHMAC(PurposeHMACAudit)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	for _, msg := range [][]byte{nil, []byte("a"), []byte(strings.Repeat("x", 1000))} {
		want := hmac.New(sm3.New, key.material[:])
		want.Write(msg)
		got := HMACSM3(key, msg)
		if !hmac.Equal(got, want.Sum(nil)) {
			t.Fatalf("HMAC-SM3 mismatch for len(msg)=%d", len(msg))
		}
		if len(got) != SM3Size {
			t.Fatalf("HMAC-SM3 must be %d bytes, got %d", SM3Size, len(got))
		}
	}
}

// 长键（> 64B 分组）要先哈希再补 pad —— 用参考实现单独验一遍，避免只测短键。
func TestHMACSM3LongKey(t *testing.T) {
	key := []byte(strings.Repeat("k", 100))
	msg := []byte("long-key-case")
	ref := hmac.New(sm3.New, key)
	ref.Write(msg)

	got := hmacSM3Raw(key, msg)
	if !hmac.Equal(got, ref.Sum(nil)) {
		t.Fatalf("long-key HMAC-SM3 must match reference")
	}
}

// 不同密钥/不同消息必须给出不同标签（防"密钥没参与运算"这类低级错）。
func TestHMACSM3KeyAndMessageSensitivity(t *testing.T) {
	mk := mustMasterKey(t)
	a, _ := mk.DeriveHMAC(PurposeHMACAudit)
	b, _ := mk.DeriveHMAC(PurposeHMACIdentifier)
	if HMACSM3Hex(a, []byte("m")) == HMACSM3Hex(b, []byte("m")) {
		t.Fatal("different purposes must not produce the same HMAC")
	}
	if HMACSM3Hex(a, []byte("m1")) == HMACSM3Hex(a, []byte("m2")) {
		t.Fatal("different messages must not produce the same HMAC")
	}
}

// HMACSM3WithUserSecret 是 TOTP/HOTP 的唯一入口（键是**用户侧**共享密钥，不是平台密钥）。
//
// 它必须与 `hmacSM3Raw`（以及 stdlib 的 RFC 2104 构造）逐字节一致 —— 否则动态口令的
// 校验值与其他实现（含旧系统、第三方 authenticator）对不上，而这类错**只在联调时**才暴露。
func TestHMACSM3WithUserSecret(t *testing.T) {
	cases := [][]byte{
		[]byte("12345678901234567890"), // RFC 4226 附录 D 的经典 20B 种子
		[]byte(strings.Repeat("s", 20)),
		[]byte(strings.Repeat("k", 100)), // 长键（> 64B 分组）也要走"先哈希"分支
	}
	msgs := [][]byte{nil, []byte("hotp-counter-1"), []byte(strings.Repeat("z", 512))}
	for _, secret := range cases {
		for _, msg := range msgs {
			want := hmac.New(sm3.New, secret)
			want.Write(msg)
			got := HMACSM3WithUserSecret(secret, msg)
			if !hmac.Equal(got, want.Sum(nil)) {
				t.Fatalf("HMACSM3WithUserSecret mismatch (secret len %d, msg len %d)", len(secret), len(msg))
			}
			if len(got) != SM3Size {
				t.Fatalf("must be %d bytes, got %d", SM3Size, len(got))
			}
		}
	}
}

// 空密钥必须直接返回 nil：HMAC 的零长键在 RFC 2104 里会被补成全零 pad，
// 等于"任何人可算"，绝不能悄悄算出一个看起来合法的标签。
func TestHMACSM3WithUserSecretRejectsEmptyKey(t *testing.T) {
	if got := HMACSM3WithUserSecret(nil, []byte("m")); got != nil {
		t.Fatalf("empty secret must yield nil, got %x", got)
	}
}

// HKDF-SM3（RFC 5869 §2）与参考实现逐字节一致，且长度语义正确。
func TestHKDFSM3MatchesReference(t *testing.T) {
	mk := mustMasterKey(t)
	ikm := mk.material
	salt := []byte(PurposeHMACAudit)
	info := salt

	for _, length := range []int{1, 16, 32, 64, 100} {
		got, err := hkdfSM3(ikm, salt, info, length)
		if err != nil {
			t.Fatalf("hkdf(%d): %v", length, err)
		}
		if len(got) != length {
			t.Fatalf("hkdf(%d) returned %d bytes", length, len(got))
		}
		if want := referenceHKDF(t, ikm, salt, info, length); !hmac.Equal(got, want) {
			t.Fatalf("hkdf(%d) does not match RFC 5869 reference", length)
		}
	}
}

// 长度必须"前缀一致"（OKM 是流式的），不能每次重算就换一套字节。
func TestHKDFSM3IsPrefixStable(t *testing.T) {
	mk := mustMasterKey(t)
	short, _ := hkdfSM3(mk.material, []byte("s"), []byte("i"), 16)
	long, _ := hkdfSM3(mk.material, []byte("s"), []byte("i"), 32)
	if !hmac.Equal(short, long[:16]) {
		t.Fatal("HKDF output must be prefix-stable")
	}
	if _, err := hkdfSM3(mk.material, nil, nil, 0); err == nil {
		t.Fatal("length 0 must be rejected")
	}
	if _, err := hkdfSM3(mk.material, nil, nil, 255*SM3Size+1); err == nil {
		t.Fatal("length above 255*hashLen must be rejected")
	}
}

// referenceHKDF 是用 stdlib hmac 独立实现的 RFC 5869 参考版本，用于对拍。
func referenceHKDF(t *testing.T, ikm, salt, info []byte, length int) []byte {
	t.Helper()
	extract := hmac.New(sm3.New, salt)
	extract.Write(ikm)
	prk := extract.Sum(nil)

	out := make([]byte, 0, length)
	var prev []byte
	for i := byte(1); len(out) < length; i++ {
		mac := hmac.New(sm3.New, prk)
		mac.Write(prev)
		mac.Write(info)
		mac.Write([]byte{i})
		prev = mac.Sum(nil)
		out = append(out, prev...)
	}
	return out[:length]
}

func TestHexLower(t *testing.T) {
	if got, want := hexLower([]byte{0x00, 0x0f, 0xa5, 0xff}), "000fa5ff"; got != want {
		t.Fatalf("hexLower = %s, want %s", got, want)
	}
	// 与 encoding/hex 交叉校验，避免手写实现只在部分字节上对。
	b := make([]byte, 256)
	for i := range b {
		b[i] = byte(i)
	}
	if hexLower(b) != hex.EncodeToString(b) {
		t.Fatal("hexLower must equal encoding/hex for all byte values")
	}
}
