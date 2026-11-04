package crypto

import (
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"github.com/tjfoc/gmsm/sm4"
)

// SM4BlockSize 是 SM4 分组字节数。
const SM4BlockSize = 16

// SM4 信封格式（ADR-005：enc-then-MAC，**不再产出无认证的 CBC 密文**）。
//
// v2 布局（`SecretEnc` 的原始字节，落库时外层再 base64）：
//
//	0x02 | iv(16) | SM4-CBC(PKCS7(明文)) | HMAC-SM3(mac 32)
//
// `mac = HMAC-SM3(K_mac, 0x02 || iv || ct)`——**版本号也进 MAC**，防止把版本字节
// 抹掉/改掉来降级。校验顺序固定：**先验 MAC，再解密**（解密只在 MAC 通过后发生）。
//
// 为什么必须这样（两次 CI 随机刷红的根因）：SM4-CBC 自身无认证，错钥解密时
// PKCS#7 有约 1/256 概率"恰好合法"，于是 padding 校验通过、返回一段乱码明文，
// 下游把"密钥不可用"误判成"凭据不对"。加了 MAC 之后错钥**必然**失败，不确定性归零。
const (
	// SM4EnvelopeVersionV2 是当前（唯一受支持）的密文格式版本。
	SM4EnvelopeVersionV2 byte = 0x02
	// SM4MACSize 是信封尾部 HMAC-SM3 标签字节数。
	SM4MACSize = SM3Size
	// SM4EnvelopeOverhead 是除密文外的最小开销：版本(1) + IV(16) + MAC(32)。
	SM4EnvelopeOverhead = 1 + SM4BlockSize + SM4MACSize
)

var (
	// ErrCiphertextTooShort 表示密文短于一个合法信封（版本 + IV + 至少一个分组 + MAC）。
	ErrCiphertextTooShort = errors.New("crypto: sm4 ciphertext shorter than iv + one block + mac")
	// ErrCiphertextNotBlockAligned 表示密文段不是分组整数倍（数据损坏）。
	ErrCiphertextNotBlockAligned = errors.New("crypto: sm4 ciphertext not block aligned")
	// ErrCiphertextVersion 表示密文版本不受支持：**包括 v1 的"IV||密文"无认证格式**。
	//
	// 这是**刻意的 fail-closed**：如果对 v1 保留"没有 MAC 就按 CBC 解"的分支，
	// 攻击者只要删掉 MAC 段就能把完整性校验降级成可选——那等于白做。
	ErrCiphertextVersion = errors.New("crypto: unsupported sm4 ciphertext version (unauthenticated v1 is rejected)")
	// ErrIntegrityMismatch 表示 MAC 校验失败：密钥不对**或**密文被篡改（两者不可区分，也不该区分）。
	ErrIntegrityMismatch = errors.New("crypto: sm4 envelope mac mismatch (wrong key or tampered ciphertext)")
	// ErrPaddingInvalid 表示 PKCS7 填充非法。**只有 MAC 已通过**才可能走到这条（属实现缺陷）。
	ErrPaddingInvalid = errors.New("crypto: sm4 pkcs7 padding invalid")
	// ErrKeyMissing 表示传入的派生密钥缺材料（加密键或配套 MAC 键为全零）。
	ErrKeyMissing = errors.New("crypto: derived sm4 key is missing (enc or mac material is zero)")
)

// EncryptSM4 用派生密钥加密明文，返回 v2 信封 `0x02 || iv || 密文 || mac`（IV 随机、每次不同）。
//
// 口径（数据模型 §3.3）：`identifiers.value_enc` 是「SM4 密文」，ADR-005 起带完整性标签。
// 随机 IV 保证同一明文两次加密得到不同密文——正因为如此，(type,value) 的唯一性
// 只能靠确定性盲索引（HMAC-SM3），不能靠密文本身。
//
// **不做隐式回退**：cipher 构造失败、随机源不可用、密钥缺材料都返回错误，绝不明文落库。
func EncryptSM4(key SM4Key, plaintext []byte) ([]byte, error) {
	return sealSM4(key, plaintext, rand.Reader)
}

// sealSM4 是 EncryptSM4 的可注入随机源版本（仅测试用于固定 IV 做标准向量对拍）。
func sealSM4(key SM4Key, plaintext []byte, random io.Reader) ([]byte, error) {
	if key.IsZero() {
		return nil, ErrKeyMissing
	}
	iv, body, err := sealSM4CBC(key, plaintext, random)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(iv)+len(body)+SM4MACSize)
	out = append(out, SM4EnvelopeVersionV2)
	out = append(out, iv...)
	out = append(out, body...)
	out = append(out, hmacSM3Raw(key.mac[:], out)...)
	return out, nil
}

// sealSM4CBC 只做 CBC 加密并返回 `iv || 密文`（**不含**版本与 MAC）。
//
// 存在的理由有两个，都不许绕过：① GM/T 0002 标准向量对拍要能拿到裸 CBC 输出；
// ② 造"v1 无认证密文"的测试夹具（证明它会被拒绝）。业务代码不得直接调它，
// 也**不做**密钥校验（只认 `key.material`，与 MAC 材料无关）。
func sealSM4CBC(key SM4Key, plaintext []byte, random io.Reader) (iv, body []byte, err error) {
	block, err := sm4.NewCipher(key.material[:])
	if err != nil {
		return nil, nil, fmt.Errorf("crypto: sm4 cipher: %w", err)
	}
	if random == nil {
		return nil, nil, errors.New("crypto: nil random source")
	}
	iv = make([]byte, SM4BlockSize)
	if _, err := io.ReadFull(random, iv); err != nil {
		return nil, nil, fmt.Errorf("crypto: sm4 iv: %w", err)
	}
	padded := pkcs7Pad(plaintext, SM4BlockSize)
	body = make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(body, padded)
	return iv, body, nil
}

// DecryptSM4 解密并**先验证完整性**：只有 MAC 通过才会解密 `0x02 || iv || ct || mac`。
//
// 任何结构性问题（太短、版本不支持、非对齐、MAC 不符）都返回**各自的**错误：
// 调用方据此 fail-closed（KEY_UNAVAILABLE / 数据损坏），不得返回半截明文。
func DecryptSM4(key SM4Key, ciphertext []byte) ([]byte, error) {
	if key.IsZero() {
		return nil, ErrKeyMissing
	}
	if len(ciphertext) == 0 {
		return nil, ErrCiphertextTooShort
	}
	// 先判版本：v1 的 `IV||密文` 首字节是随机 IV，必然不是 0x02（除 1/256 巧合，
	// 那种情况会落到下面的长度检查，同样被拒——不会因为"碰巧像 v2"就降级解密）。
	if ciphertext[0] != SM4EnvelopeVersionV2 {
		return nil, fmt.Errorf("%w: got version 0x%02x, want 0x%02x", ErrCiphertextVersion, ciphertext[0], SM4EnvelopeVersionV2)
	}
	if len(ciphertext) < SM4EnvelopeOverhead+SM4BlockSize {
		return nil, ErrCiphertextTooShort
	}

	body := ciphertext[1:]
	macStart := len(body) - SM4MACSize
	iv, ct := body[:SM4BlockSize], body[SM4BlockSize:macStart]
	if len(ct) == 0 || len(ct)%SM4BlockSize != 0 {
		return nil, ErrCiphertextNotBlockAligned
	}

	// ① 先验 MAC（常量时间比较），覆盖 版本 || iv || ct。
	want := hmacSM3Raw(key.mac[:], ciphertext[:1+SM4BlockSize+len(ct)])
	if !hmac.Equal(want, body[macStart:]) {
		return nil, ErrIntegrityMismatch
	}

	// ② MAC 通过后才解密（此时"密钥不对"已经被排除，padding 只可能因实现缺陷而非法）。
	block, err := sm4.NewCipher(key.material[:])
	if err != nil {
		return nil, fmt.Errorf("crypto: sm4 cipher: %w", err)
	}
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)
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
