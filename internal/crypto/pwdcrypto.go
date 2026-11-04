package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// PBKDF2-SM3 的硬下限（数据模型 §1.3 / §3.4：迭代 ≥ 100000、盐 ≥ 16B）。
//
// 这两个数**不是默认值、是下限**：任何低于下限的参数直接报错，不存在"参数缺失就用
// 推荐值"的分支（禁隐性默认值）。具体参数由 login_policies / 凭据信封显式给出，
// 本包不提供默认参数集合。
const (
	// MinPBKDF2Iterations 是最小迭代次数（100000）。
	MinPBKDF2Iterations = 100000
	// MinPasswordSaltLen 是最小盐字节数（16B）。
	MinPasswordSaltLen = 16
	// MinPasswordKeyLen 是最小派生密钥字节数（32B = SM3 输出长度）。
	MinPasswordKeyLen = 32
)

var (
	// ErrWeakPBKDF2Params 表示迭代次数/盐长/输出长度低于基线硬下限。
	ErrWeakPBKDF2Params = errors.New("crypto: pbkdf2-sm3 parameters below baseline minimum")
	// ErrEmptyPassword 表示口令为空（空口令不得进入派生：那等于把策略校验挪到别处）。
	ErrEmptyPassword = errors.New("crypto: password must not be empty")
)

// PasswordParams 是 PBKDF2-SM3 的显式参数（对应 `authenticators.credential_data` 里的
// `{algo, iterations, salt, len}`）。没有零值可用的"默认参数"。
type PasswordParams struct {
	// Iterations 是迭代次数。
	Iterations int
	// KeyLen 是派生密钥字节数。
	KeyLen int
}

// Validate 检查参数是否满足基线硬下限。
func (p PasswordParams) Validate() error {
	if p.Iterations < MinPBKDF2Iterations {
		return fmt.Errorf("%w: iterations %d < %d", ErrWeakPBKDF2Params, p.Iterations, MinPBKDF2Iterations)
	}
	if p.KeyLen < MinPasswordKeyLen {
		return fmt.Errorf("%w: key length %d < %d", ErrWeakPBKDF2Params, p.KeyLen, MinPasswordKeyLen)
	}
	return nil
}

// NewPasswordSalt 生成随机盐（默认取 MinPasswordSaltLen = 16B；调用方要更长可显式传 length）。
func NewPasswordSalt(random io.Reader, length int) ([]byte, error) {
	if random == nil {
		random = rand.Reader
	}
	if length < MinPasswordSaltLen {
		return nil, fmt.Errorf("%w: salt length %d < %d", ErrWeakPBKDF2Params, length, MinPasswordSaltLen)
	}
	salt := make([]byte, length)
	if _, err := io.ReadFull(random, salt); err != nil {
		return nil, fmt.Errorf("crypto: password salt: %w", err)
	}
	return salt, nil
}

// DerivePassword 计算 PBKDF2-SM3(password, salt, iterations, keyLen)。
//
// 参数全部显式（不给默认值）；任一项低于下限即报错 —— 口令派生是最容易被"顺手降参数"
// 的地方，降参数必须过不去。
func DerivePassword(password, salt []byte, params PasswordParams) ([]byte, error) {
	if len(password) == 0 {
		return nil, ErrEmptyPassword
	}
	if len(salt) < MinPasswordSaltLen {
		return nil, fmt.Errorf("%w: salt length %d < %d", ErrWeakPBKDF2Params, len(salt), MinPasswordSaltLen)
	}
	if err := params.Validate(); err != nil {
		return nil, err
	}
	return pbkdf2SM3(password, salt, params.Iterations, params.KeyLen), nil
}

// pbkdf2SM3 是 RFC 8018 §5.2 的 PBKDF2，PRF 固定 HMAC-SM3。
//
// **为什么自己写、不用 golang.org/x/crypto/pbkdf2**（技术栈 §2 列了它）：
// `tjfoc/gmsm` 的 `SM3.Sum(b)` **不满足 `hash.Hash` 接口约定** —— 它把 `b` 当数据再
// Write 进状态，并把摘要写回 `b` 的底层数组后返回 `b[len(b):]` 而非 `append` 语义。
// x/crypto/pbkdf2 的 `dk = prf.Sum(dk)` 依赖 append 语义，接上 SM3 会：
// ①每块把前一块的结果覆盖掉；②`dk` 长度不再增长，keyLen > 32 时直接
// `slice bounds out of range` panic（本包单测 TestDerivePasswordMultiBlock 复现过）。
// 因此这里按 RFC 直接实现 20 行，只用 `Sum(nil)`（安全用法），不碰坏掉的那条路径。
// 判定依据：实测 panic（见 internal/crypto/pwdcrypto_test.go），不是"风格偏好"。
func pbkdf2SM3(password, salt []byte, iter, keyLen int) []byte {
	hashLen := SM3Size
	blocks := (keyLen + hashLen - 1) / hashLen

	out := make([]byte, 0, blocks*hashLen)
	for block := 1; block <= blocks; block++ {
		var idx [4]byte
		binary.BigEndian.PutUint32(idx[:], uint32(block))

		msg := make([]byte, 0, len(salt)+4)
		msg = append(msg, salt...)
		msg = append(msg, idx[:]...)

		// U_1 = PRF(password, salt || INT(i))
		u := hmacSM3Raw(password, msg)
		t := make([]byte, hashLen)
		copy(t, u)

		// T_i = U_1 ^ U_2 ^ … ^ U_iter, U_n = PRF(password, U_{n-1})
		for n := 2; n <= iter; n++ {
			u = hmacSM3Raw(password, u)
			for i := range t {
				t[i] ^= u[i]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// VerifyPassword 用**常量时间**比较重算结果与已存派生值。
//
// 不做"先比长度再比内容"的短路（会泄露前缀信息）；长度不同直接判否。
func VerifyPassword(password, salt, expected []byte, params PasswordParams) (bool, error) {
	got, err := DerivePassword(password, salt, params)
	if err != nil {
		return false, err
	}
	if len(got) != len(expected) {
		return false, nil
	}
	return subtle.ConstantTimeCompare(got, expected) == 1, nil
}
