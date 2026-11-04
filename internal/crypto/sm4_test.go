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
// 这里刻意走**不含 MAC** 的内部 CBC 底座（ADR-005 之后网上传输用的是带 MAC 的信封）。
func TestSM4StandardVectorViaCBC(t *testing.T) {
	keyBytes, _ := hex.DecodeString("0123456789abcdeffedcba9876543210")
	plain, _ := hex.DecodeString("0123456789abcdeffedcba9876543210")
	want, _ := hex.DecodeString("681edf34d206965e86b3e94f536e4246")

	var key SM4Key
	copy(key.material[:], keyBytes) // 测试内直接构造（等价于"取一把固定测试键"）

	iv, body, err := sealSM4CBC(key, plain, zeroReader{})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	// IV(16) + 两个分组：PKCS7 把 16B 明文补到 32B（整块填充是必须的，否则无法区分
	// "明文末尾恰好是 0x01" 和 "填充了 1 个字节"）。
	if len(body) != SM4BlockSize*2 {
		t.Fatalf("ciphertext len = %d, want 32", len(body))
	}
	if !bytes.Equal(iv, make([]byte, SM4BlockSize)) {
		t.Fatalf("IV must be the injected zeros")
	}
	if got := body[:SM4BlockSize]; !bytes.Equal(got, want) {
		t.Fatalf("SM4 block = %x, want %x", got, want)
	}
}

// 信封布局：`0x02 | iv(16) | ct | mac(32)`，且长度 = 1+16+分组数*16+32。
func TestSM4EnvelopeLayout(t *testing.T) {
	mk := mustMasterKey(t)
	key, _ := mk.DeriveSM4(PurposeSM4Field)

	plain := []byte("****8000") // 9 字节 → 补到 16，一个分组
	ct, err := EncryptSM4(key, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if ct[0] != SM4EnvelopeVersionV2 {
		t.Fatalf("首字节必须是版本号 0x%02x，实际 0x%02x", SM4EnvelopeVersionV2, ct[0])
	}
	wantLen := 1 + SM4BlockSize + SM4BlockSize + SM4MACSize
	if len(ct) != wantLen {
		t.Fatalf("信封长度 = %d, want %d", len(ct), wantLen)
	}
	// MAC 必须覆盖 版本 || iv || ct：把同一份 (版本,iv,ct) 重算一遍应当一致。
	wantMAC := hmacSM3Raw(key.mac[:], ct[:len(ct)-SM4MACSize])
	if !bytes.Equal(wantMAC, ct[len(ct)-SM4MACSize:]) {
		t.Fatal("尾部 32 字节必须是 HMAC-SM3(版本||iv||ct)")
	}
}

// 随机 IV：同一明文两次加密必须不同，但都能解回原文。
func TestSM4RandomIV(t *testing.T) {
	mk := mustMasterKey(t)
	key, _ := mk.DeriveSM4(PurposeSM4Field)
	plain := []byte("****8000")

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

// **ADR-005 的核心回归**：错误密钥必须**必然**失败，而不是"1/256 概率恰好绕过去"。
//
// 这条是 CI 随机刷红的直接对策：以前只有 PKCS#7 校验，错钥解密有约 1/256 的概率
// padding 恰好合法 → 返回乱码明文 → 下游报"凭据不对"而不是"密钥不可用"。
// 这里跑 500 轮不同密钥，**一轮都不许成功**；就算将来有人把 MAC 校验挪到解密之后，
// 只要 MAC 还在就能挡住（而删掉 MAC 会让这条连同 TestSM4LegacyV1Rejected 一起红）。
func TestSM4WrongKeyAlwaysFails(t *testing.T) {
	const rounds = 500
	mk := mustMasterKey(t)
	key, _ := mk.DeriveSM4(PurposeSM4Field)

	ct, err := EncryptSM4(key, []byte("sensitive-identifier"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	for i := 0; i < rounds; i++ {
		// 每轮一把**确定但互不相同**的主密钥（用 SM3 打散轮次号，避免撞回 mustMasterKey）。
		sum := SM3Sum([]byte{byte(i), byte(i >> 8), 0x5a})
		raw := sum[:32]
		otherMK, err := NewMasterKey(raw)
		if err != nil {
			t.Fatalf("NewMasterKey: %v", err)
		}
		other, err := otherMK.DeriveSM4(PurposeSM4Field)
		if err != nil {
			t.Fatalf("DeriveSM4: %v", err)
		}
		got, err := DecryptSM4(other, ct)
		if err == nil {
			t.Fatalf("第 %d 轮：错误密钥竟然解开了（%q）——完整性校验没生效", i, got)
		}
		if !errors.Is(err, ErrIntegrityMismatch) {
			t.Fatalf("第 %d 轮：错钥必须报 ErrIntegrityMismatch，实际 %v", i, err)
		}
		if got != nil {
			t.Fatalf("第 %d 轮：失败时必须返回 nil 明文，实际 %q", i, got)
		}
	}
}

// 跨用途不可互换：用 `sm4/field` 封的信封，`sm4/keywrap` 的键解不开（两把 MAC 键互不通用）。
func TestSM4CrossPurposeEnvelopeRejected(t *testing.T) {
	mk := mustMasterKey(t)
	field, _ := mk.DeriveSM4(PurposeSM4Field)
	keywrap, _ := mk.DeriveSM4(PurposeSM4Keywrap)

	ct, err := EncryptSM4(field, []byte("identifier-value"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := DecryptSM4(keywrap, ct); !errors.Is(err, ErrIntegrityMismatch) {
		t.Fatalf("跨用途解封必须报 ErrIntegrityMismatch，实际 %v", err)
	}
}

// 篡改：版本 / IV / 密文 / MAC 任何一段被改都必须报错，且不得返回明文。
func TestSM4TamperDetected(t *testing.T) {
	mk := mustMasterKey(t)
	key, _ := mk.DeriveSM4(PurposeSM4Field)

	base, err := EncryptSM4(key, []byte("tamper-me-please"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	positions := []int{0, 1, SM4BlockSize, 1 + SM4BlockSize, len(base) - 1}
	for _, pos := range positions {
		bad := append([]byte(nil), base...)
		bad[pos] ^= 0x01
		got, err := DecryptSM4(key, bad)
		if err == nil {
			t.Fatalf("第 %d 字节被改竟然解开了（%q）", pos, got)
		}
		if got != nil {
			t.Fatalf("第 %d 字节被改：失败时必须返回 nil 明文", pos)
		}
	}

	// 截断 MAC 段（相当于"去掉完整性"）也必须被拒。
	if _, err := DecryptSM4(key, base[:len(base)-SM4MACSize]); err == nil {
		t.Fatal("截掉 MAC 段必须被拒")
	}
	// 截断一个分组。
	if _, err := DecryptSM4(key, base[:len(base)-SM4MACSize-SM4BlockSize]); err == nil {
		t.Fatal("截断密文必须被拒")
	}
}

// v1（`IV || 密文`，无 MAC）必须**显式拒绝**，不提供"没有 MAC 就按 CBC 解"的降级分支。
//
// 这是能解开的合法密文：如果实现里偷偷留了 v1 兼容，这条会立刻红。
func TestSM4LegacyV1Rejected(t *testing.T) {
	mk := mustMasterKey(t)
	key, _ := mk.DeriveSM4(PurposeSM4Field)

	iv, body, err := sealSM4CBC(key, []byte("legacy-ciphertext"), zeroReader{})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	v1 := append(append([]byte(nil), iv...), body...)
	got, err := DecryptSM4(key, v1)
	if !errors.Is(err, ErrCiphertextVersion) {
		t.Fatalf("v1 无认证密文必须报 ErrCiphertextVersion，实际 %v（明文 %q）", err, got)
	}
}

// 结构性损坏 + 缺材料：各自返回**各自**的错误，调用方才能 fail-closed 并分类。
func TestSM4MalformedCiphertext(t *testing.T) {
	mk := mustMasterKey(t)
	key, _ := mk.DeriveSM4(PurposeSM4Field)

	if _, err := DecryptSM4(key, nil); !errors.Is(err, ErrCiphertextTooShort) {
		t.Errorf("nil ciphertext err = %v, want ErrCiphertextTooShort", err)
	}
	// 版本对、长度不够（没有密文段）。
	truncated := make([]byte, SM4EnvelopeOverhead)
	truncated[0] = SM4EnvelopeVersionV2
	if _, err := DecryptSM4(key, truncated); !errors.Is(err, ErrCiphertextTooShort) {
		t.Errorf("无密文段的信封 err = %v, want ErrCiphertextTooShort", err)
	}
	// 版本对、长度够，但密文段不是分组整数倍。
	bad := make([]byte, SM4EnvelopeOverhead+SM4BlockSize+7)
	bad[0] = SM4EnvelopeVersionV2
	if _, err := DecryptSM4(key, bad); !errors.Is(err, ErrCiphertextNotBlockAligned) {
		t.Errorf("非对齐 err = %v, want ErrCiphertextNotBlockAligned", err)
	}

	// 只派生了加密材料、MAC 材料为零的"半把钥匙"必须在入口被挡（不许写出无完整性密文）。
	var half SM4Key
	copy(half.material[:], key.material[:])
	if !half.IsZero() {
		t.Fatal("只有加密材料的半把钥匙必须判为不可用")
	}
	if _, err := EncryptSM4(half, []byte("x")); !errors.Is(err, ErrKeyMissing) {
		t.Errorf("半把钥匙加密 err = %v, want ErrKeyMissing", err)
	}
	if _, err := DecryptSM4(half, make([]byte, SM4EnvelopeOverhead+SM4BlockSize)); !errors.Is(err, ErrKeyMissing) {
		t.Errorf("半把钥匙解密 err = %v, want ErrKeyMissing", err)
	}
	var zero SM4Key
	if _, err := EncryptSM4(zero, nil); !errors.Is(err, ErrKeyMissing) {
		t.Errorf("零值密钥 err = %v, want ErrKeyMissing", err)
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
