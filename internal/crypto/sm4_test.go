package crypto

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// zeroReader 返回全零随机源：用来把随机 IV 固定成 0，从而对上标准向量。
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// GM/T 0002 标准向量：key = 明文 = 0123456789ABCDEFFEDCBA9876543210 →
// 密文 681EDF34D206965E86B3E94F536E4246。
//
// 用 CBC + 全零 IV 验证：单分组明文的 CBC 首块等于 ECB，因此首块密文必须是该标准值。
// 这样既验了 SM4 原语接线（key 传对了、字节序没歪），又验了 CBC 实现本身。
func TestSM4StandardVectorViaCBC(t *testing.T) {
	keyBytes, _ := hex.DecodeString("0123456789abcdeffedcba9876543210")
	plain, _ := hex.DecodeString("0123456789abcdeffedcba9876543210")
	want, _ := hex.DecodeString("681edf34d206965e86b3e94f536e4246")

	var key SM4Key
	copy(key.material[:], keyBytes) // 测试内直接构造（等价于"取一把固定测试键"）

	out, err := encryptSM4CBC(key, plain, zeroReader{})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// IV(16) + 两个分组：PKCS7 把 16B 明文补到 32B（整块填充是必须的，否则无法区分
	// "明文末尾恰好是 0x01" 和 "填充了 1 个字节"）。
	if len(out) != SM4BlockSize*3 {
		t.Fatalf("ciphertext len = %d, want 48", len(out))
	}
	if !bytes.Equal(out[:SM4BlockSize], make([]byte, SM4BlockSize)) {
		t.Fatalf("IV prefix must be the injected zeros")
	}
	if got := out[SM4BlockSize : SM4BlockSize*2]; !bytes.Equal(got, want) {
		t.Fatalf("SM4 block = %x, want %x", got, want)
	}

	back, err := DecryptSM4(key, out)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatalf("round-trip mismatch: %x", back)
	}
}

// 随机 IV：同一明文两次加密必须不同，但都能解回原文。
func TestSM4RandomIV(t *testing.T) {
	mk := mustMasterKey(t)
	key, _ := mk.DeriveSM4(PurposeSM4Field)
	plain := []byte("13800138000")

	a, err := EncryptSM4(key, plain)
	if err != nil {
		t.Fatalf("encrypt a: %v", err)
	}
	b, err := EncryptSM4(key, plain)
	if err != nil {
		t.Fatalf("encrypt b: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same plaintext must differ (random IV)")
	}
	for _, ct := range [][]byte{a, b} {
		got, err := DecryptSM4(key, ct)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("round-trip mismatch: %q", got)
		}
	}
}

func TestSM4RoundTripEdgeCases(t *testing.T) {
	mk := mustMasterKey(t)
	key, _ := mk.DeriveSM4(PurposeSM4Keywrap)
	for _, plain := range [][]byte{
		nil,
		{},
		[]byte("a"),
		bytes.Repeat([]byte("x"), SM4BlockSize),
		bytes.Repeat([]byte("y"), SM4BlockSize+1),
		bytes.Repeat([]byte("z"), 4096),
	} {
		ct, err := EncryptSM4(key, plain)
		if err != nil {
			t.Fatalf("encrypt(len=%d): %v", len(plain), err)
		}
		got, err := DecryptSM4(key, ct)
		if err != nil {
			t.Fatalf("decrypt(len=%d): %v", len(plain), err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("round-trip mismatch for len=%d", len(plain))
		}
	}
}

// 错误密钥必须解不出来（且报填充错，不返回半截明文）。
func TestSM4WrongKeyFailsClosed(t *testing.T) {
	mk := mustMasterKey(t)
	key, _ := mk.DeriveSM4(PurposeSM4Field)
	other, _ := mk.DeriveSM4(PurposeSM4Keywrap)

	ct, err := EncryptSM4(key, []byte("sensitive-identifier"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := DecryptSM4(other, ct)
	if err == nil {
		t.Fatalf("wrong key must fail, got plaintext %q", got)
	}
	if got != nil {
		t.Fatalf("on failure must return nil plaintext, got %q", got)
	}
}

// 结构性损坏：各自返回**各自**的错误，调用方才能 fail-closed 并分类。
func TestSM4MalformedCiphertext(t *testing.T) {
	mk := mustMasterKey(t)
	key, _ := mk.DeriveSM4(PurposeSM4Field)

	if _, err := DecryptSM4(key, nil); !errors.Is(err, ErrCiphertextTooShort) {
		t.Errorf("nil ciphertext err = %v, want ErrCiphertextTooShort", err)
	}
	if _, err := DecryptSM4(key, make([]byte, SM4BlockSize-1)); !errors.Is(err, ErrCiphertextTooShort) {
		t.Errorf("short IV err = %v, want ErrCiphertextTooShort", err)
	}
	if _, err := DecryptSM4(key, make([]byte, SM4BlockSize)); !errors.Is(err, ErrCiphertextNotBlockAligned) {
		t.Errorf("IV-only err = %v, want ErrCiphertextNotBlockAligned", err)
	}
	if _, err := DecryptSM4(key, make([]byte, SM4BlockSize+7)); !errors.Is(err, ErrCiphertextNotBlockAligned) {
		t.Errorf("non-aligned err = %v, want ErrCiphertextNotBlockAligned", err)
	}

	// 篡改末字节 → 填充必然非法（PKCS7 的末字节等于填充长度）。
	ct, err := EncryptSM4(key, []byte("tamper-me"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	ct[len(ct)-1] ^= 0xff
	if _, err := DecryptSM4(key, ct); !errors.Is(err, ErrPaddingInvalid) {
		t.Errorf("tampered ciphertext err = %v, want ErrPaddingInvalid", err)
	}
}

func TestPKCS7Helpers(t *testing.T) {
	for n := 0; n <= 2*SM4BlockSize; n++ {
		src := bytes.Repeat([]byte{0xab}, n)
		padded := pkcs7Pad(src, SM4BlockSize)
		if len(padded)%SM4BlockSize != 0 || len(padded) <= n {
			t.Fatalf("bad padding length for %d: %d", n, len(padded))
		}
		back, err := pkcs7Unpad(padded, SM4BlockSize)
		if err != nil {
			t.Fatalf("unpad(%d): %v", n, err)
		}
		if !bytes.Equal(back, src) {
			t.Fatalf("unpad(%d) mismatch", n)
		}
	}
	bad := [][]byte{
		{},
		{0x00}, // 填充长度 0 非法
		bytes.Repeat([]byte{SM4BlockSize + 1}, 16), // 填充长度超过块长
		{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
			0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x03, 0x03}, // 末字节 3 但填充不一致
	}
	for _, b := range bad {
		if _, err := pkcs7Unpad(b, SM4BlockSize); !errors.Is(err, ErrPaddingInvalid) {
			t.Errorf("unpad(%x) err = %v, want ErrPaddingInvalid", b, err)
		}
	}
	if strings.TrimSpace("") != "" { // 保持 strings 导入用于错误信息可读性检查
		t.Log("")
	}
}
