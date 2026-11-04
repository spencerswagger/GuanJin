package crypto

import "fmt"

// Purpose 是 HKDF-SM3 的用途域（domain separator）。**封闭集合**：只有 D4-B 冻结的 5 个值。
//
// 口径（decisions.md D4-B）：主密钥只做派生输入、不进任何业务算法；派生时
// `salt = info = purpose`。用途域一旦可自造，「一密钥一用途」就形同虚设
// （任何人加个新字符串就能拿到一把"新钥匙"），故本包对未知用途直接报错。
type Purpose string

// 冻结的 5 个用途域（decisions.md D4-B；长度与用途绑定，不可协商）。
const (
	// PurposeSM4Field 敏感字段加密（identifiers.value_enc 等），16 字节。
	PurposeSM4Field Purpose = "guanjin/sm4/field"
	// PurposeSM4Keywrap 密钥封装（signing_keys.private_key_enc 等），16 字节。
	PurposeSM4Keywrap Purpose = "guanjin/sm4/keywrap"
	// PurposeHMACAudit 审计链 HMAC（audit_logs.integrity_tag），32 字节。
	PurposeHMACAudit Purpose = "guanjin/hmac/audit"
	// PurposeHMACIdentifier 标识符盲索引（identifiers.value），32 字节。
	PurposeHMACIdentifier Purpose = "guanjin/hmac/identifier"
	// PurposeHMACAgent Agent 面 HMAC（clients.secret_hash），32 字节。
	PurposeHMACAgent Purpose = "guanjin/hmac/agent"
)

// KeyKind 是派生结果的算法种类。种类错误（如拿 HMAC 用途派生 SM4 键）必须报错，
// 不能"先派生出来再截断"——截断会把两个用途的密钥空间混在一起。
type KeyKind uint8

const (
	// KindSM4 表示 SM4 对称密钥（16 字节）。
	KindSM4 KeyKind = iota + 1
	// KindHMAC 表示 HMAC-SM3 密钥（32 字节）。
	KindHMAC
)

// String 便于日志与错误信息（不泄露密钥材料）。
func (k KeyKind) String() string {
	switch k {
	case KindSM4:
		return "SM4"
	case KindHMAC:
		return "HMAC-SM3"
	default:
		return "unknown"
	}
}

// purposeSpec 是「用途 → (算法, 长度)」的唯一映射表。
type purposeSpec struct {
	kind   KeyKind
	length int
}

var purposeSpecs = map[Purpose]purposeSpec{
	PurposeSM4Field:       {kind: KindSM4, length: SM4KeySize},
	PurposeSM4Keywrap:     {kind: KindSM4, length: SM4KeySize},
	PurposeHMACAudit:      {kind: KindHMAC, length: HMACKeySize},
	PurposeHMACIdentifier: {kind: KindHMAC, length: HMACKeySize},
	PurposeHMACAgent:      {kind: KindHMAC, length: HMACKeySize},
}

// spec 返回用途的规格；未知用途一律报错（禁自造用途域）。
func (p Purpose) spec() (purposeSpec, error) {
	s, ok := purposeSpecs[p]
	if !ok {
		return purposeSpec{}, fmt.Errorf("crypto: unknown purpose %q (allowed: %s)", string(p),
			purposeList())
	}
	return s, nil
}

// Kind 返回该用途应有的密钥种类（未知用途报错）。
func (p Purpose) Kind() (KeyKind, error) {
	s, err := p.spec()
	if err != nil {
		return 0, err
	}
	return s.kind, nil
}

// Length 返回该用途应有的密钥字节数（未知用途报错）。
func (p Purpose) Length() (int, error) {
	s, err := p.spec()
	if err != nil {
		return 0, err
	}
	return s.length, nil
}

// Purposes 返回冻结的用途域清单（顺序固定，便于日志与文档对齐）。
func Purposes() []Purpose {
	return []Purpose{
		PurposeSM4Field,
		PurposeSM4Keywrap,
		PurposeHMACAudit,
		PurposeHMACIdentifier,
		PurposeHMACAgent,
	}
}

func purposeList() string {
	out := ""
	for i, p := range Purposes() {
		if i > 0 {
			out += ", "
		}
		out += string(p)
	}
	return out
}
