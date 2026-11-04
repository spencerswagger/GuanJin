package crypto

import (
	"fmt"

	"github.com/tjfoc/gmsm/sm3"
)

// SM3 摘要（GM/T 0004）。本包是唯一调用点：其他包不得直接 import gmsm，
// 以便将来替换算法实现（索引 §4.2-20「签名/摘要留可替换接口」）。

// SM3Sum 返回 data 的 SM3 摘要（32 字节）。
func SM3Sum(data []byte) [SM3Size]byte {
	var out [SM3Size]byte
	copy(out[:], sm3.Sm3Sum(data))
	return out
}

// SM3Hex 返回 data 的 SM3 摘要**小写 hex**（64 字符）。
//
// 口径（decisions.md D1）：Agent 面 canonical 串里的 `<SM3HEX(body)>` 就是本函数的输出；
// 空 body 取 SM3("") 的固定值，**不允许省略该段**。
func SM3Hex(data []byte) string {
	sum := SM3Sum(data)
	return hexLower(sum[:])
}

// HMACSM3 计算 HMAC-SM3(key, msg)（32 字节）。
//
// key 只能是派生得到的 HMACKey —— 主密钥与任意 []byte 都无法传进来（D4-B 的编译期约束）。
func HMACSM3(key HMACKey, msg []byte) []byte {
	return hmacSM3Raw(key.material[:], msg)
}

// HMACSM3Hex 返回 HMAC-SM3 的小写 hex（64 字符），用于盲索引/审计标签的存储表示。
func HMACSM3Hex(key HMACKey, msg []byte) string {
	return hexLower(HMACSM3(key, msg))
}

// hkdfSM3 是 RFC 5869 定义的 HKDF，哈希原语固定为 SM3（D4-B：`HKDF(SM3, masterKey,
// salt=purpose, info=purpose)`）。
//
// 为什么自己写而不是引 x/crypto/hkdf：这里只有 20 行、结构可逐行对 RFC 复核，
// 且避免为一个函数引入额外依赖方向。结构严格照 RFC 5869 §2：
//
//	PRK  = HMAC-SM3(salt, IKM)
//	T(0) = ""
//	T(i) = HMAC-SM3(PRK, T(i-1) | info | byte(i))
//	OKM  = (T(1) | T(2) | …)[:length]
func hkdfSM3(ikm, salt, info []byte, length int) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("crypto: hkdf length must be > 0, got %d", length)
	}
	if length > 255*SM3Size {
		return nil, fmt.Errorf("crypto: hkdf length %d exceeds 255*%d", length, SM3Size)
	}

	prk := hmacSM3Raw(salt, ikm)

	out := make([]byte, 0, length)
	var prev []byte
	for i := byte(1); len(out) < length; i++ {
		block := make([]byte, 0, len(prev)+len(info)+1)
		block = append(block, prev...)
		block = append(block, info...)
		block = append(block, i)
		prev = hmacSM3Raw(prk, block)
		out = append(out, prev...)
	}
	return out[:length], nil
}

// hmacSM3Raw 是 HMAC-SM3 的底层实现（键为任意长度字节）。
//
// 业务代码**不允许**调它（未导出）：业务只能用 HMACKey（已派生、用途隔离），见 HMACSM3。
// RFC 2104：键补/哈希到分组长度 64，ipad = 0x36、opad = 0x5c。
func hmacSM3Raw(key, msg []byte) []byte {
	const blockSize = 64 // SM3 分组长度
	if len(key) > blockSize {
		sum := sm3.Sm3Sum(key)
		key = sum
	}
	var ipad, opad [blockSize]byte
	for i := 0; i < blockSize; i++ {
		var k byte
		if i < len(key) {
			k = key[i]
		}
		ipad[i] = k ^ 0x36
		opad[i] = k ^ 0x5c
	}

	inner := sm3.New()
	_, _ = inner.Write(ipad[:])
	_, _ = inner.Write(msg)
	innerSum := inner.Sum(nil)

	outer := sm3.New()
	_, _ = outer.Write(opad[:])
	_, _ = outer.Write(innerSum)
	return outer.Sum(nil)
}

const hexLowerDigits = "0123456789abcdef"

func hexLower(b []byte) string {
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, hexLowerDigits[c>>4], hexLowerDigits[c&0x0f])
	}
	return string(out)
}
