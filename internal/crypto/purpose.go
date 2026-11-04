package crypto

import "fmt"

// Purpose 是 HKDF-SM3 的用途域（domain separator）。**封闭集合**：只有 D4-B 冻结的 8 个值
// （原 5 个 + D14② 的 `hmac/register` + ADR-005 为 SM4 信封配套的两个完整性域）。
//
// 口径（decisions.md D4-B）：主密钥只做派生输入、不进任何业务算法；派生时
// `salt = info = purpose`。用途域一旦可自造，「一密钥一用途」就形同虚设
// （任何人加个新字符串就能拿到一把"新钥匙"），故本包对未知用途直接报错。
type Purpose string

// 冻结的 8 个用途域（decisions.md D4-B / D14② / ADR-005；长度与用途绑定，不可协商）。
//
// 加密与完整性**各用一把**：`sm4/*` 只做加密，`hmac/*` 只做 MAC。
// 「从 SM4 密钥再派生一把 MAC 密钥」这种写法本包没有，也不许有——那等于
// 用一把钥匙开两把锁，用途隔离立刻失效。
const (
	// PurposeSM4Field 敏感字段加密（identifiers.value_enc 等），16 字节。
	PurposeSM4Field Purpose = "guanjin/sm4/field"
	// PurposeHMACField 敏感字段信封的完整性（ADR-005：enc-then-MAC 的 MAC 键），32 字节。
	//
	// 与 `sm4/field` 成对使用：`SecretEnc = 0x02 || iv(16) || SM4-CBC(ct) || HMAC-SM3(mac)`，
	// MAC 覆盖 `版本号 || iv || ct`，**验 MAC 先于解密**。
	PurposeHMACField Purpose = "guanjin/hmac/field"
	// PurposeSM4Keywrap 密钥封装（signing_keys.private_key_enc 等），16 字节。
	PurposeSM4Keywrap Purpose = "guanjin/sm4/keywrap"
	// PurposeHMACKeywrap 密钥封装信封的完整性（ADR-005），32 字节。
	//
	// 与 `sm4/keywrap` 成对；**不与 `hmac/field` 合并**：把"被包裹的私钥"的密文
	// 挪到 `identifiers.value_enc` 里应当验不过，靠的正是两把 MAC 键互不通用。
	PurposeHMACKeywrap Purpose = "guanjin/hmac/keywrap"
	// PurposeHMACAudit 审计链 HMAC（audit_logs.integrity_tag），32 字节。
	PurposeHMACAudit Purpose = "guanjin/hmac/audit"
	// PurposeHMACIdentifier 标识符盲索引（identifiers.value），32 字节。
	PurposeHMACIdentifier Purpose = "guanjin/hmac/identifier"
	// PurposeHMACAgent Agent 面 HMAC（clients.secret_hash），32 字节。
	PurposeHMACAgent Purpose = "guanjin/hmac/agent"
	// PurposeHMACRegister 注册窗口令牌 `register_token` 的 HMAC（D14②），32 字节。
	//
	// **不复用 `hmac/identifier`**：register_token 签的是
	// `cid + typ + val + chid + exp`（谁、给谁、哪个标识符、哪次挑战、何时失效），
	// 与"按值算盲索引"是完全不同的用途。一密钥一用途是密评口径，复用即自废：
	// 一旦盲索引密钥被换（重建索引需要重算全表），在途的 register_token 要么全废、
	// 要么被迫与盲索引共用同一把钥匙——两条都不合规。
	PurposeHMACRegister Purpose = "guanjin/hmac/register"
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

// purposeSpec 是「用途 → (算法, 长度, 配套 MAC 用途)」的唯一映射表。
//
// `mac` 只有 SM4 类用途才有值：它指向"给这个信封算完整性"的 HMAC 用途域。
// 两把钥匙都由主密钥按**各自**的用途域派生（不是从 SM4 键再派生），所以
// 一密钥一用途不被破坏；反过来，任何 SM4 用途缺 `mac` 都会被一致性自检挡住
// （见 `validatePurposeSpecs`）——不允许出现"某条加密路径忘了带完整性"。
type purposeSpec struct {
	kind   KeyKind
	length int
	mac    Purpose
}

var purposeSpecs = map[Purpose]purposeSpec{
	PurposeSM4Field:       {kind: KindSM4, length: SM4KeySize, mac: PurposeHMACField},
	PurposeSM4Keywrap:     {kind: KindSM4, length: SM4KeySize, mac: PurposeHMACKeywrap},
	PurposeHMACField:      {kind: KindHMAC, length: HMACKeySize},
	PurposeHMACKeywrap:    {kind: KindHMAC, length: HMACKeySize},
	PurposeHMACAudit:      {kind: KindHMAC, length: HMACKeySize},
	PurposeHMACIdentifier: {kind: KindHMAC, length: HMACKeySize},
	PurposeHMACAgent:      {kind: KindHMAC, length: HMACKeySize},
	PurposeHMACRegister:   {kind: KindHMAC, length: HMACKeySize},
}

// validatePurposeSpecs 是用途表的**一致性自检**（单测调用；表是编译期常量，无需 init）。
//
// 三条：① 表里不许有"列表里有、表里没有"的用途；② 每个 SM4 用途必须有配套 MAC 用途；
// ③ 配套的必须真的是 32 字节 HMAC 用途。少了任何一条，完整性都可能被静默漏掉。
func validatePurposeSpecs() error {
	if len(purposeSpecs) != len(Purposes()) {
		return fmt.Errorf("crypto: %d purposes in list but %d in spec table",
			len(Purposes()), len(purposeSpecs))
	}
	for _, p := range Purposes() {
		spec, ok := purposeSpecs[p]
		if !ok {
			return fmt.Errorf("crypto: purpose %q listed but has no spec", string(p))
		}
		switch spec.kind {
		case KindSM4:
			if spec.mac == "" {
				return fmt.Errorf("crypto: SM4 purpose %q has no paired MAC purpose", string(p))
			}
			macSpec, ok := purposeSpecs[spec.mac]
			if !ok {
				return fmt.Errorf("crypto: purpose %q pairs unknown MAC purpose %q", string(p), string(spec.mac))
			}
			if macSpec.kind != KindHMAC || macSpec.length != HMACKeySize {
				return fmt.Errorf("crypto: paired MAC purpose %q must be %d-byte HMAC",
					string(spec.mac), HMACKeySize)
			}
			if macSpec.mac != "" {
				return fmt.Errorf("crypto: MAC purpose %q must not itself pair another MAC", string(spec.mac))
			}
		case KindHMAC:
			if spec.mac != "" {
				return fmt.Errorf("crypto: HMAC purpose %q must not pair a MAC purpose", string(p))
			}
		default:
			return fmt.Errorf("crypto: purpose %q has unknown kind %d", string(p), spec.kind)
		}
	}
	return nil
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

// Purposes 返回冻结的用途域清单（顺序固定，便于日志与文档对齐：成对的加密/完整性相邻）。
func Purposes() []Purpose {
	return []Purpose{
		PurposeSM4Field,
		PurposeHMACField,
		PurposeSM4Keywrap,
		PurposeHMACKeywrap,
		PurposeHMACAudit,
		PurposeHMACIdentifier,
		PurposeHMACAgent,
		PurposeHMACRegister,
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
