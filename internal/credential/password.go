package credential

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

const (
	// AlgoPBKDF2SM3 是口令信封唯一允许的算法名（索引 §2「均固定为最高级」）。
	//
	// 名字写进 `authenticators.credential_data.algo`，是**落库契约**：
	// 改成别的字符串等于让历史凭据全部读不出来。
	AlgoPBKDF2SM3 = "PBKDF2-SM3"
	// MaxPasswordBytes 是口令长度上限（字节）。与 `login_policies` 的 DB CHECK（8..128）同源。
	MaxPasswordBytes = 128
	// MinPasswordSaltBytes 是盐的最小字节数（索引 §2；`crypto.MinPasswordSaltLen` 同值）。
	//
	// 这里再写一遍是**故意的冗余**：这个数是验收判据（AC-AUD-12④）直接 grep 的对象，
	// 只出现在 crypto 里会让"口令实现是否守住 16B"变成一次跨包推理。
	MinPasswordSaltBytes = 16
)

// PasswordEnvelope 是口令凭据在 `authenticators.credential_data` 里的完整形态
// （数据模型 §3.3 的 `password` 信封）。
//
// 不可逆：只有派生值 + 盐 + 参数，没有任何可还原明文的东西。
type PasswordEnvelope struct {
	// Algo 固定 `PBKDF2-SM3`。
	Algo string `json:"algo"`
	// Iterations 迭代次数（≥100000）。
	Iterations int `json:"iterations"`
	// Salt 盐（base64 标准编码，解码后 ≥16B，随机生成）。
	Salt string `json:"salt"`
	// DK 派生密钥（base64 标准编码，长度 = KeyLen）。
	DK string `json:"dk"`
	// KeyLen 派生密钥字节数（≥32，SM3 输出长度）。
	KeyLen int `json:"key_len"`
	// UpdatedAt 最近一次设置时刻（RFC3339，UTC）。用于"口令最长有效期"判定。
	UpdatedAt string `json:"updated_at,omitempty"`
}

// NewPasswordEnvelope 用显式参数派生一个新信封。
//
// **参数必须由调用方给**（一般来自 `login_policies` + 平台冻结下限）：
// 本函数不提供默认迭代次数——那正是"顺手降参数"最常发生的入口。
func NewPasswordEnvelope(password string, params crypto.PasswordParams, now time.Time, random io.Reader) (PasswordEnvelope, error) {
	if len(password) == 0 {
		return PasswordEnvelope{}, ErrEmptyPassword
	}
	if len(password) > MaxPasswordBytes {
		return PasswordEnvelope{}, ErrPasswordTooLong
	}
	if err := params.Validate(); err != nil {
		// params.Validate 已经带 crypto 包的错误语义；这里换成本包的 ErrWeakParams，
		// 让上层只需要认一个错误族（避免把 crypto 的细节漏到 HTTP 层）。
		return PasswordEnvelope{}, wrapf(ErrWeakParams, "%v", err)
	}
	salt, err := crypto.NewPasswordSalt(random, MinPasswordSaltBytes)
	if err != nil {
		return PasswordEnvelope{}, wrapf(ErrWeakParams, "%v", err)
	}
	dk, err := crypto.DerivePassword([]byte(password), salt, params)
	if err != nil {
		// 空口令/弱参数已在上面挡过；走到这里属实现缺陷。
		return PasswordEnvelope{}, wrapf(ErrMalformedEnvelope, "%v", err)
	}
	return PasswordEnvelope{
		Algo:       AlgoPBKDF2SM3,
		Iterations: params.Iterations,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		DK:         base64.StdEncoding.EncodeToString(dk),
		KeyLen:     params.KeyLen,
		UpdatedAt:  now.UTC().Format(time.RFC3339),
	}, nil
}

// Validate 检查信封结构与参数下限。
//
// **读信封必先 Validate**：这条约束把"篡改数据库把迭代降到 1000"变成读失败，
// 而不是悄悄用一个弱参数继续校验（那样等于给了一条静默降级路径）。
func (e PasswordEnvelope) Validate() error {
	if e.Algo != AlgoPBKDF2SM3 {
		return wrapf(ErrUnknownAlgo, "%q (required: %s)", e.Algo, AlgoPBKDF2SM3)
	}
	if e.Iterations < crypto.MinPBKDF2Iterations {
		return wrapf(ErrWeakParams, "iterations %d < %d", e.Iterations, crypto.MinPBKDF2Iterations)
	}
	if e.KeyLen < crypto.MinPasswordKeyLen {
		return wrapf(ErrWeakParams, "key_len %d < %d", e.KeyLen, crypto.MinPasswordKeyLen)
	}
	salt, err := base64.StdEncoding.DecodeString(e.Salt)
	if err != nil {
		return wrapf(ErrMalformedEnvelope, "salt is not base64: %v", err)
	}
	if len(salt) < MinPasswordSaltBytes {
		return wrapf(ErrWeakParams, "salt %d bytes < %d", len(salt), MinPasswordSaltBytes)
	}
	dk, err := base64.StdEncoding.DecodeString(e.DK)
	if err != nil {
		return wrapf(ErrMalformedEnvelope, "dk is not base64: %v", err)
	}
	if len(dk) != e.KeyLen {
		return wrapf(ErrMalformedEnvelope, "dk %d bytes != key_len %d", len(dk), e.KeyLen)
	}
	if e.UpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339, e.UpdatedAt); err != nil {
			return wrapf(ErrMalformedEnvelope, "updated_at is not RFC3339: %v", err)
		}
	}
	return nil
}

// Verify 用**常量时间**比较校验口令。
func (e PasswordEnvelope) Verify(password string) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, err
	}
	if len(password) == 0 || len(password) > MaxPasswordBytes {
		// 不做派生直接判否：空/超长口令不可能是当初存下的那个（且超长会白烧 CPU）。
		return false, nil
	}
	salt, err := base64.StdEncoding.DecodeString(e.Salt)
	if err != nil {
		return false, wrapf(ErrMalformedEnvelope, "salt is not base64: %v", err)
	}
	dk, err := base64.StdEncoding.DecodeString(e.DK)
	if err != nil {
		return false, wrapf(ErrMalformedEnvelope, "dk is not base64: %v", err)
	}
	params := crypto.PasswordParams{Iterations: e.Iterations, KeyLen: e.KeyLen}
	ok, err := crypto.VerifyPassword([]byte(password), salt, dk, params)
	if err != nil {
		return false, wrapf(ErrMalformedEnvelope, "%v", err)
	}
	return ok, nil
}

// NeedsRehash 报告该信封是否应当用当前参数重算（登录成功后透明升级）。
//
// 触发条件只有两种：迭代/长度被调高，或算法名不是冻结值。**不因"参数变低"触发**——
// 变低是被篡改的信号，`Validate` 会直接报错，不该在这里被"顺手修复"。
func (e PasswordEnvelope) NeedsRehash(params crypto.PasswordParams) bool {
	if err := e.Validate(); err != nil {
		return false
	}
	if e.Algo != AlgoPBKDF2SM3 {
		return true
	}
	return e.Iterations < params.Iterations || e.KeyLen < params.KeyLen
}

// Marshal 序列化成落库用的 JSON（`credential_data` 列）。
func (e PasswordEnvelope) Marshal() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, wrapf(ErrMalformedEnvelope, "marshal: %v", err)
	}
	return raw, nil
}

// ParsePasswordEnvelope 从 JSON 还原信封（读取 `credential_data` 后**必须**走这里）。
//
// 解析即校验：坏信封在这里失败，不会带着弱参数进入校验路径。
func ParsePasswordEnvelope(raw []byte) (PasswordEnvelope, error) {
	var e PasswordEnvelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return PasswordEnvelope{}, wrapf(ErrMalformedEnvelope, "unmarshal: %v", err)
	}
	if err := e.Validate(); err != nil {
		return PasswordEnvelope{}, err
	}
	return e, nil
}

// UpdatedAtTime 解析 `UpdatedAt`；为空返回零值 + false（调用方据此决定"是否要判过期"）。
func (e PasswordEnvelope) UpdatedAtTime() (time.Time, bool, error) {
	if e.UpdatedAt == "" {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, e.UpdatedAt)
	if err != nil {
		return time.Time{}, false, wrapf(ErrMalformedEnvelope, "updated_at is not RFC3339: %v", err)
	}
	return t, true, nil
}

// String 固定脱敏：信封里有盐与派生值，日志里打全文等于把离线爆破材料抄进日志。
func (e PasswordEnvelope) String() string {
	return fmt.Sprintf("credential.PasswordEnvelope{algo=%s, iterations=%d, key_len=%d}", e.Algo, e.Iterations, e.KeyLen)
}

// GoString 让 %#v 也脱敏。
func (e PasswordEnvelope) GoString() string { return e.String() }
