package crypto

import (
	"crypto/subtle"
	"errors"
	"fmt"
)

// 密钥长度（与 D4-B 的用途域绑定，见 purposeSpecs）。
const (
	// SM4KeySize 是 SM4 密钥字节数（128 bit）。
	SM4KeySize = 16
	// HMACKeySize 是 HMAC-SM3 密钥字节数（256 bit）。
	HMACKeySize = 32
	// SM3Size 是 SM3 摘要字节数。
	SM3Size = 32
	// MinMasterKeyLen 是主密钥的最小字节数（技术栈 §7.2 与 config 共用同一常量）。
	MinMasterKeyLen = 32
)

// 密钥材料的错误（sentinel，便于调用方 fail-closed 分支）。
var (
	// ErrMasterKeyTooShort 表示主密钥长度不足；**不截断、不补齐**（补齐会悄悄降低熵）。
	ErrMasterKeyTooShort = errors.New("crypto: master key too short")
	// ErrMasterKeyEmpty 表示主密钥为空。
	ErrMasterKeyEmpty = errors.New("crypto: master key is empty")
	// ErrKeyKindMismatch 表示用途与派生方法不匹配（如用 SM4 用途派生 HMAC 键）。
	ErrKeyKindMismatch = errors.New("crypto: key kind mismatch")
)

// SM4Key 是派生得到的 SM4 密钥。**只由 MasterKey.DeriveSM4 构造**：
// 字段不导出、无导出的构造函数，因此算法入口（EncryptSM4/DecryptSM4）在编译期就
// 拿不到"未经派生的输入"——这正是 D4-B 要求的"主密钥不进业务算法"。
type SM4Key struct {
	material [SM4KeySize]byte
}

// HMACKey 是派生得到的 HMAC-SM3 密钥。构造路径与 SM4Key 相同（只由 MasterKey 派生）。
type HMACKey struct {
	material [HMACKeySize]byte
}

// Zero 清空密钥材料（用于进程内"用完即弃"的场景）。
func (k *SM4Key) Zero() {
	if k == nil {
		return
	}
	for i := range k.material {
		k.material[i] = 0
	}
}

// Zero 清空密钥材料。
func (k *HMACKey) Zero() {
	if k == nil {
		return
	}
	for i := range k.material {
		k.material[i] = 0
	}
}

// Equal 做常量时间比较（避免按字节提前返回泄露前缀）。
func (k SM4Key) Equal(other SM4Key) bool {
	return subtle.ConstantTimeCompare(k.material[:], other.material[:]) == 1
}

// Equal 做常量时间比较。
func (k HMACKey) Equal(other HMACKey) bool {
	return subtle.ConstantTimeCompare(k.material[:], other.material[:]) == 1
}

// IsZero 报告密钥是否为全零（只用于测试/断言，不用于业务判断）。
func (k SM4Key) IsZero() bool {
	var zero [SM4KeySize]byte
	return subtle.ConstantTimeCompare(k.material[:], zero[:]) == 1
}

// IsZero 报告密钥是否为全零。
func (k HMACKey) IsZero() bool {
	var zero [HMACKeySize]byte
	return subtle.ConstantTimeCompare(k.material[:], zero[:]) == 1
}

// String 固定返回脱敏文本：日志、错误、%v/%s 都不会打印密钥材料。
func (k SM4Key) String() string { return "crypto.SM4Key(<redacted>)" }

// String 固定返回脱敏文本。
func (k HMACKey) String() string { return "crypto.HMACKey(<redacted>)" }

// GoString 让 %#v 也脱敏（否则 fmt 会打印结构体字段）。
func (k SM4Key) GoString() string { return k.String() }

// GoString 让 %#v 也脱敏。
func (k HMACKey) GoString() string { return k.String() }

// MasterKey 是主密钥。只从环境变量注入（技术栈 §7.3），**不落库、不落盘**。
//
// 它只有"派生"这一个出口：没有任何导出方法把原文材料交出去，调用方拿到的最细粒度
// 就是按用途派生的 SM4Key / HMACKey。这样"把主密钥直传给算法"在编译期就写不出来。
type MasterKey struct {
	material []byte
}

// NewMasterKey 校验并接管主密钥材料。
//
// 校验（fail-closed，AC-FC-03 的启动期部分）：非空、长度 ≥ MinMasterKeyLen（32B）。
// 非法输入一律返回错误，由 main 以非 0 退出；**不存在"太短就补齐"的分支**。
func NewMasterKey(raw []byte) (*MasterKey, error) {
	if len(raw) == 0 {
		return nil, ErrMasterKeyEmpty
	}
	if len(raw) < MinMasterKeyLen {
		return nil, fmt.Errorf("%w: got %d bytes, need >= %d", ErrMasterKeyTooShort, len(raw), MinMasterKeyLen)
	}
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return &MasterKey{material: cp}, nil
}

// Len 返回主密钥字节数（不泄露内容）。
func (m *MasterKey) Len() int {
	if m == nil {
		return 0
	}
	return len(m.material)
}

// Zero 擦除主密钥材料（进程退出前或轮换后调用）。
func (m *MasterKey) Zero() {
	if m == nil {
		return
	}
	for i := range m.material {
		m.material[i] = 0
	}
	m.material = nil // 清空后长度归零：checkUsable 会拒绝任何后续派生
}

// String 固定返回脱敏文本。
func (m *MasterKey) String() string { return "crypto.MasterKey(<redacted>)" }

// GoString 让 %#v 也脱敏。
func (m *MasterKey) GoString() string { return m.String() }

// DeriveSM4 按用途派生 SM4 密钥（HKDF-SM3，salt = info = purpose）。
//
// 用途必须是 SM4 类（field / keywrap），否则返回 ErrKeyKindMismatch ——
// 种类不符是编码缺陷，不允许"改个长度凑合过"。
func (m *MasterKey) DeriveSM4(p Purpose) (SM4Key, error) {
	var out SM4Key
	spec, err := p.spec()
	if err != nil {
		return out, err
	}
	if spec.kind != KindSM4 {
		return out, fmt.Errorf("%w: purpose %q is %s, not SM4", ErrKeyKindMismatch, string(p), spec.kind)
	}
	if err := m.checkUsable(); err != nil {
		return out, err
	}
	dk, err := hkdfSM3(m.material, []byte(p), []byte(p), spec.length)
	if err != nil {
		return out, err
	}
	copy(out.material[:], dk)
	return out, nil
}

// DeriveHMAC 按用途派生 HMAC-SM3 密钥（HKDF-SM3，salt = info = purpose）。
func (m *MasterKey) DeriveHMAC(p Purpose) (HMACKey, error) {
	var out HMACKey
	spec, err := p.spec()
	if err != nil {
		return out, err
	}
	if spec.kind != KindHMAC {
		return out, fmt.Errorf("%w: purpose %q is %s, not HMAC-SM3", ErrKeyKindMismatch, string(p), spec.kind)
	}
	if err := m.checkUsable(); err != nil {
		return out, err
	}
	dk, err := hkdfSM3(m.material, []byte(p), []byte(p), spec.length)
	if err != nil {
		return out, err
	}
	copy(out.material[:], dk)
	return out, nil
}

func (m *MasterKey) checkUsable() error {
	if m == nil {
		return ErrMasterKeyEmpty
	}
	if len(m.material) < MinMasterKeyLen {
		return fmt.Errorf("%w: got %d bytes, need >= %d", ErrMasterKeyTooShort, len(m.material), MinMasterKeyLen)
	}
	return nil
}
