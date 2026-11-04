package crypto

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/tjfoc/gmsm/sm2"
)

// SM2 签名（GM/T 0003）。用途（decisions.md）：
//   - 对外令牌 `SM3withSM2`（S6，JWKS 发布）；
//   - 网关内部令牌 `X-GuanJin-Assertion`（S8）；
//   - 数据面注销通知签名（S8）。
//
// **本包不决定签名值的线上编码**（ASN.1 DER 还是 JOSE 的 r||s）。那是 S6 与 Nova 的
// 契约面，混进 S3 会把"算法落地"和"传输格式"绑死。这里只产出 (r, s) 两个大整数。

// DefaultSM2UID 是 GM/T 0003 规定的默认用户标识（"1234567812345678"）。
// 平台侧统一使用默认值：改用自定义 UID 会让 SDK 侧无法独立验签。
var DefaultSM2UID = []byte("1234567812345678")

// 公钥编码长度：SEC1 压缩点 = 1 字节前缀 + 32 字节 X。
const (
	// SM2CompressedPubKeyLen 是压缩公钥字节数。
	SM2CompressedPubKeyLen = 33
	// SM2CoordinateLen 是 X/Y 坐标字节数。
	SM2CoordinateLen = 32
)

// ErrNilSM2Key 表示传入的密钥为空。
var ErrNilSM2Key = errors.New("crypto: nil sm2 key")

// GenerateSM2Key 生成 SM2 私钥（含公钥）。random 为 nil 时用 crypto/rand。
func GenerateSM2Key(random io.Reader) (*sm2.PrivateKey, error) {
	if random == nil {
		random = rand.Reader
	}
	priv, err := sm2.GenerateKey(random)
	if err != nil {
		return nil, fmt.Errorf("crypto: sm2 keygen: %w", err)
	}
	return priv, nil
}

// SignSM2 对 msg 做 SM2 签名，返回签名数字 (r, s)。
//
// SM2 签名内部先算 ZA（含用户标识）再对 ZA||msg 做 SM3，因此调用方**不要**自己先
// 哈希一遍——重复哈希会得到 SDK 验不过的签名。
func SignSM2(priv *sm2.PrivateKey, msg []byte) (r, s *big.Int, err error) {
	if priv == nil {
		return nil, nil, ErrNilSM2Key
	}
	return sm2.Sm2Sign(priv, msg, DefaultSM2UID, rand.Reader)
}

// VerifySM2 验证 SM2 签名数字；任何非法输入（nil 密钥、nil 数字）一律返回 false。
func VerifySM2(pub *sm2.PublicKey, msg []byte, r, s *big.Int) bool {
	if pub == nil || r == nil || s == nil {
		return false
	}
	return sm2.Sm2Verify(pub, msg, DefaultSM2UID, r, s)
}

// MarshalSM2PublicKey 返回 **SEC1 压缩点**（33 字节：`0x02/0x03 || X(32B)`，前缀 = Y 的奇偶）。
//
// 为什么不直接用 gmsm 的 `sm2.Compress`：它把前缀写成 **0/1**（`getLastBit(Y)`）而不是
// SEC1 的 **0x02/0x03**，且不补 X 的定长。直接外发会让 SDK（Java/Node 用标准库或
// BouncyCastle）解不出来 —— 这是纯编码差异，不是算法差异，所以在本包里修正，
// 并保留 gmsm 的曲线运算（Decompress 的输入格式仍是 0/1）。
func MarshalSM2PublicKey(pub *sm2.PublicKey) ([]byte, error) {
	if pub == nil || pub.X == nil || pub.Y == nil {
		return nil, ErrNilSM2Key
	}
	x := pub.X.Bytes()
	if len(x) > SM2CoordinateLen {
		return nil, fmt.Errorf("crypto: sm2 X coordinate too long (%d bytes)", len(x))
	}
	out := make([]byte, SM2CompressedPubKeyLen)
	// 奇数 Y → 0x03；偶数 Y → 0x02（SEC1 §2.3.4）。
	if pub.Y.Bit(0) == 1 {
		out[0] = 0x03
	} else {
		out[0] = 0x02
	}
	copy(out[SM2CompressedPubKeyLen-len(x):], x) // 左补零到 32 字节
	return out, nil
}

// UnmarshalSM2PublicKey 解析 SEC1 压缩点（33 字节，前缀 0x02/0x03）。
//
// **必须做长度与前缀校验**：gmsm 的 `Decompress` 对任意长度输入都不报错
// （直接 `a[1:]` 取 X），喂垃圾进去会得到 nil 坐标，最终表现成"验签永远失败"
// 而不是"输入非法"——那是难以定位的静默错误。
func UnmarshalSM2PublicKey(data []byte) (*sm2.PublicKey, error) {
	if len(data) != SM2CompressedPubKeyLen {
		return nil, fmt.Errorf("crypto: compressed sm2 public key must be %d bytes, got %d",
			SM2CompressedPubKeyLen, len(data))
	}
	if data[0] != 0x02 && data[0] != 0x03 {
		return nil, fmt.Errorf("crypto: compressed sm2 public key prefix must be 0x02/0x03, got 0x%02x", data[0])
	}

	// 转成 gmsm 期望的形式：前缀 0/1（Y 的奇偶），X 定长 32 字节。
	gm := make([]byte, SM2CompressedPubKeyLen)
	if data[0] == 0x03 {
		gm[0] = 1
	}
	copy(gm[1:], data[1:])

	pub := sm2.Decompress(gm)
	if pub == nil || pub.X == nil || pub.Y == nil {
		return nil, errors.New("crypto: invalid sm2 public key")
	}
	// 防御性校验：解出来的 Y 奇偶必须与前缀一致（曲线点不在曲线上时 ModSqrt 会给错值）。
	wantBit := uint(0)
	if data[0] == 0x03 {
		wantBit = 1
	}
	if pub.Y.Bit(0) != wantBit {
		return nil, errors.New("crypto: sm2 public key parity mismatch")
	}
	return pub, nil
}
