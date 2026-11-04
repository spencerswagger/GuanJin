// Package identifier 负责标识符的**规范化、盲索引与不变式规则**。
//
// 口径（decisions.md D4 / 数据模型 §3.2 / §4.1）：
//   - 三段式存储：`value` = 盲索引 HMAC-SM3(派生键, 规范化值)、`value_enc` = SM4 密文、
//     `value_masked` = 脱敏展示（索引 §4.2-5）；
//   - **规范化必须在算盲索引之前**完成（否则同一个邮箱因大小写不同变成两条记录）；
//   - 本包是**纯函数层**：不碰数据库、不碰事务。跨表计数与事务由 `internal/identity` 负责，
//     错误 → 对外码的映射见 `CodeOf`；
//   - 密码学实现一律走 `internal/crypto`（SM3/HMAC-SM3/SM4 的唯一实现点）。
package identifier

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// 标识符类型（与 `factor.IdentifierType` 同值；那边是**消费方**，这里是归一化方）。
//
// 为什么两处各有一份常量：`internal/identifier` 是更下层（factor 会 import 它理解的值），
// 反向 import 会让"因子 SPI 依赖标识符归一化"变成循环。漂移由单测钉住
// （`TestIdentifierTypesMatchFactor`，在 _test 里 import factor 比对）。
const (
	TypePhone      = "phone"
	TypeEmail      = "email"
	TypeUsername   = "username"
	TypeEmployeeNo = "employee_no"
)

var (
	// ErrUnsupportedType 表示标识符类型不在 V1 归一化表里。
	//
	// 报错而不是"原样存"：未知类型的规范化规则不明（大写算不算同一个？去不去空格？），
	// 猜一个就等于给唯一约束埋一颗不定时炸弹。新增类型必须同时加归一化与掩码。
	ErrUnsupportedType = errors.New("identifier: unsupported identifier type")
	// ErrEmptyValue 表示规范化后为空。
	ErrEmptyValue = errors.New("identifier: empty value")
)

// Encoded 是一次规范化的全部产物（落库三段 + 投递用的明文）。
type Encoded struct {
	// Type 标识符类型。
	Type string
	// Normalized 规范化后的明文。
	//
	// ⚠️ **只在投递/审计比对的那一刻用**：不得落日志、不得进响应体、不得进错误信息。
	// 落库只用下面三段。
	Normalized string
	// BlindIndex 盲索引（HMAC-SM3，确定性）：承担 `(type,value)` 全局唯一与等值查询。
	BlindIndex []byte
	// Cipher  SM4 信封（`版本 || IV || 密文 || MAC`）：承担还原，先验 MAC 再解密（ADR-005）。
	Cipher []byte
	// Masked 脱敏展示值。
	Masked string
}

// Normalize 按类型规范化（不做任何"聪明"的等价折叠）。
func Normalize(itype, raw string) (string, error) {
	switch itype {
	case TypeEmail:
		return NormalizeEmail(raw)
	case TypePhone:
		return NormalizePhone(raw)
	case TypeUsername:
		return NormalizeUsername(raw)
	case TypeEmployeeNo:
		return normalizeEmployeeNo(raw)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedType, itype)
	}
}

// normalizeEmployeeNo 只做小写 + 去首尾空白（工号是**管理侧录入型**，格式由各业务方自定，
// V1 可不启用；这里不校验字符集，避免把"工号里有斜杠"这种合法情况挡掉）。
func normalizeEmployeeNo(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", ErrEmptyValue
	}
	if len(s) > 64 {
		return "", fmt.Errorf("%w: employee_no longer than 64", ErrInvalidIdentifier)
	}
	return s, nil
}

// Encode 规范化 + 计算盲索引 + SM4 加密 + 掩码。
//
// 两把键都必须来自 `crypto.MasterKey` 的派生（D4-B）：盲索引用
// `guanjin/hmac/identifier`，字段加密用 `guanjin/sm4/field`。
// **不允许**在这里接受主密钥或任意 []byte —— 否则"用途隔离"就被绕过去了。
func Encode(itype, raw string, indexKey crypto.HMACKey, fieldKey crypto.SM4Key) (Encoded, error) {
	normalized, err := Normalize(itype, raw)
	if err != nil {
		return Encoded{}, err
	}
	if fieldKey.IsZero() {
		return Encoded{}, errors.New("identifier: sm4 field key is missing (derive via crypto.MasterKey)")
	}
	blind := crypto.HMACSM3(indexKey, []byte(itype+"\x00"+normalized))
	cipher, err := crypto.EncryptSM4(fieldKey, []byte(normalized))
	if err != nil {
		return Encoded{}, fmt.Errorf("identifier: encrypt value: %w", err)
	}
	return Encoded{
		Type:       itype,
		Normalized: normalized,
		BlindIndex: blind,
		Cipher:     cipher,
		Masked:     Mask(itype, normalized),
	}, nil
}

// Decode 解回明文（投递验证码 / 换绑通知旧值时才用）。
func Decode(cipherText []byte, fieldKey crypto.SM4Key) (string, error) {
	if fieldKey.IsZero() {
		return "", errors.New("identifier: sm4 field key is missing (derive via crypto.MasterKey)")
	}
	plain, err := crypto.DecryptSM4(fieldKey, cipherText)
	if err != nil {
		return "", fmt.Errorf("identifier: decrypt value: %w", err)
	}
	return string(plain), nil
}
