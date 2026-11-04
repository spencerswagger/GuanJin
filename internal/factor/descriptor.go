package factor

import (
	"errors"
	"fmt"
)

// Descriptor 是因子的**能力声明**（基线 §6 的 YAML 契约在 Go 侧的形态，
// 也是 `GET /api/v1/factors` 里 `descriptor` 字段的内容）。
//
// 关键取舍：**Descriptor 里没有 `capabilities` 的手写字段**。
// 能力列表由 `Describe()` 用接口断言填进去（`Capabilities []Capability` 是**输出**，
// 不是输入）——若允许因子自己再写一份声明，就会出现"声明能找回、其实没实现"，
// 正是基线 §7 守护规则 5 要抓的那类错。同理，所有 `*Spec` 都必须由**该因子自己返回**
// （接口方法），而不是由调用方另填一份。
type Descriptor struct {
	Type                FactorType               `json:"type"`
	Kind                Kind                     `json:"kind"`
	Capabilities        []Capability             `json:"capabilities"`
	Enrollment          *EnrollmentSpec          `json:"enrollable,omitempty"`
	Authenticatable     *AuthenticatableSpec     `json:"authenticatable,omitempty"`
	ACR                 *ACRSpec                 `json:"acr,omitempty"`
	IdentifierVerifying *IdentifierVerifyingSpec `json:"identifier_verifying,omitempty"`
	Recoverable         *RecoverableSpec         `json:"recoverable,omitempty"`
	Resettable          *ProofRequirement        `json:"resettable,omitempty"`
	Registrable         *RegistrableSpec         `json:"registrable,omitempty"`
	Revocable           *RevocableSpec           `json:"revocable,omitempty"`
	RateLimit           *RateLimits              `json:"rate_limit,omitempty"`
	RiskSignals         []RiskSignal             `json:"risk_signals,omitempty"`
}

// 描述符校验错误。都包在 ErrDescriptorInvalid 下，便于注册表统一处理、
// 也便于测试用 errors.Is 断言"是哪一类错"而不必比字符串。
var (
	// ErrNilProvider 表示注册/描述一个 nil 因子。
	ErrNilProvider = errors.New("factor: nil provider")
	// ErrDescriptorInvalid 是描述符违规的总类（具体字段在错误文本里）。
	ErrDescriptorInvalid = errors.New("factor: descriptor violates capability model")
	// ErrProofingNotSupported 表示装配了核验型因子（归身份核验模块，不属本平台）。
	ErrProofingNotSupported = errors.New("factor: proofing kind is not part of this platform")
	// ErrDuplicateFactor 表示同一 FactorType 被注册两次。
	ErrDuplicateFactor = errors.New("factor: duplicate factor type")
	// ErrFactorNotFound 表示注册表里没有该类型。
	ErrFactorNotFound = errors.New("factor: factor type not registered")
)

// Describe 从**实现**推导出能力声明，并按基线能力模型校验。
//
// 顺序很重要：先取类型/承载类型（不合法就别往下走），再逐能力断言填 Spec，
// 最后跑一遍 kind 级不变式。这样任何"声明与实现不一致"都在**注册期**暴露，
// 而不是等某个用户在登录时踩到。
func Describe(p Provider) (Descriptor, error) {
	if p == nil {
		return Descriptor{}, ErrNilProvider
	}
	t := p.Type()
	if err := t.Validate(); err != nil {
		return Descriptor{}, err
	}
	k := p.Kind()
	if !k.Valid() {
		return Descriptor{}, fmt.Errorf("%w: type=%s kind=%q", ErrDescriptorInvalid, t, string(k))
	}

	d := Descriptor{Type: t, Kind: k, Capabilities: Capabilities(p)}

	if c, ok := p.(Enrollable); ok {
		s := c.EnrollmentSpec()
		d.Enrollment = &s
	}
	if c, ok := p.(Authenticatable); ok {
		s := c.AuthenticatableSpec()
		d.Authenticatable = &s
	}
	if c, ok := p.(ACRContributing); ok {
		s := c.ACRSpec()
		d.ACR = &s
	}
	if c, ok := p.(IdentifierVerifying); ok {
		s := c.IdentifierVerifyingSpec()
		d.IdentifierVerifying = &s
	}
	if c, ok := p.(Recoverable); ok {
		s := c.RecoverableSpec()
		d.Recoverable = &s
	}
	if c, ok := p.(Resettable); ok {
		s := c.RequiredProofForReset()
		d.Resettable = &s
	}
	if c, ok := p.(Registrable); ok {
		s := c.RegistrableSpec()
		d.Registrable = &s
	}
	if c, ok := p.(Revocable); ok {
		s := c.RevocableSpec()
		d.Revocable = &s
	}
	if c, ok := p.(RateLimited); ok {
		s := c.Limits()
		d.RateLimit = &s
	}
	if c, ok := p.(RiskReporting); ok {
		d.RiskSignals = append([]RiskSignal(nil), c.RiskSignals()...)
	}

	if err := validateDescriptor(d); err != nil {
		return Descriptor{}, err
	}
	return d, nil
}

// validateDescriptor 是基线能力模型的**可执行版本**。
//
// 全部规则都按 `kind` 或"能力之间的蕴含关系"表达，**没有一条读 Type()**：
// "短信不能重置"这类格子由 `Kind == channel ⇒ 不得实现 Resettable` 覆盖；
// "密码不能找回"由"`PasswordProvider` 不实现 Recoverable"从类型层面保证。
func validateDescriptor(d Descriptor) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: type=%s %s", ErrDescriptorInvalid, d.Type,
			fmt.Sprintf(format, args...))
	}

	if d.Kind == KindProofing {
		// 基线 §4.2：proofing 不是认证因子，它产出的是"标识符可信度/实名状态"。
		return fmt.Errorf("%w: type=%s", ErrProofingNotSupported, d.Type)
	}
	if len(d.Capabilities) == 0 {
		return bad("declares no capability")
	}

	switch d.Kind {
	case KindChannel:
		// §2：channel 型"必须先有一个已验证的手机号/邮箱"→ 必须能验标识符、且绑定要求前置标识符。
		if d.IdentifierVerifying == nil {
			return bad("channel kind must implement IdentifierVerifying")
		}
		if err := validateIdentifierTargets(d.IdentifierVerifying.Targets); err != nil {
			return bad("%v", err)
		}
		if d.Enrollment == nil {
			return bad("channel kind must implement Enrollable (binding follows the identifier)")
		}
		if !d.Enrollment.RequiresPreBoundIdentifier {
			return bad("channel kind must declare RequiresPreBoundIdentifier=true")
		}
		// §4.3：channel 型 Resettable = —（重置的是标识符，不是"凭据"）。
		if d.Resettable != nil {
			return bad("channel kind must NOT implement Resettable (reset applies to the identifier)")
		}
		// §2 第 3 条：channel 型消耗外部资源 → 发送/日配额必须显式声明。
		if d.RateLimit == nil || d.RateLimit.SendPerWindow <= 0 || d.RateLimit.SendWindow <= 0 || d.RateLimit.DailySend <= 0 {
			return bad("channel kind must declare send rate limit and daily quota")
		}
		// §4.1：短信/邮箱"注册强制验证"。
		if d.Registrable != nil && !d.Registrable.VerifyRequired {
			return bad("channel kind must declare registrable.verify_required=true")
		}
	case KindHolding:
		// holding 型不需要前置标识符，也不验证标识符归属。
		if d.IdentifierVerifying != nil {
			return bad("holding kind must NOT implement IdentifierVerifying")
		}
		if d.Enrollment != nil && d.Enrollment.RequiresPreBoundIdentifier {
			return bad("holding kind must not require a pre-bound identifier")
		}
	}

	// 能力之间的蕴含关系（都是基线的硬约束，不是口味）。
	if d.Authenticatable != nil {
		if !d.Authenticatable.AsFirst && !d.Authenticatable.AsSecond {
			return bad("authenticatable must be usable as first or second factor")
		}
		// 能认证就必须说清"单独使用能到哪一级"（§3 第 4 项）。
		if d.ACR == nil || d.ACR.Sole == "" {
			return bad("authenticatable must declare acr.sole")
		}
	}
	if d.ACR != nil {
		if !d.ACR.Sole.Issuable() {
			return bad("acr.sole=%q is not issuable (D12: only AAL1/AAL2)", string(d.ACR.Sole))
		}
		if d.ACR.Combined != "" {
			if !d.ACR.Combined.Issuable() {
				return bad("acr.combined=%q is not issuable (D12: only AAL1/AAL2)", string(d.ACR.Combined))
			}
			if !d.ACR.Combined.AtLeast(d.ACR.Sole) {
				return bad("acr.combined=%q weaker than acr.sole=%q", string(d.ACR.Combined), string(d.ACR.Sole))
			}
		}
	}
	if d.Recoverable != nil {
		if d.Recoverable.TrustLevel < 1 || d.Recoverable.TrustLevel > 3 {
			return bad("recoverable.trust_level=%d out of range 1..3", d.Recoverable.TrustLevel)
		}
	}
	if d.Resettable != nil {
		if d.Resettable.MinACR != "" && !d.Resettable.MinACR.Issuable() {
			return bad("resettable.min_acr=%q is not issuable", string(d.Resettable.MinACR))
		}
		if d.Resettable.MinTrustLevel < 0 || d.Resettable.MinTrustLevel > 3 {
			return bad("resettable.min_trust_level=%d out of range 0..3", d.Resettable.MinTrustLevel)
		}
	}
	if d.Registrable != nil && d.Authenticatable == nil {
		// 注册产出的是"首个凭据"，它必须能认证，否则注册完就登不进去。
		return bad("registrable requires authenticatable")
	}
	if d.Revocable != nil {
		if !d.Revocable.OnIdentifierUnbind.Valid() {
			return bad("revocable.on_identifier_unbind=%q invalid", string(d.Revocable.OnIdentifierUnbind))
		}
		if d.Revocable.RequireACR != "" && !d.Revocable.RequireACR.Issuable() {
			return bad("revocable.require_acr=%q is not issuable", string(d.Revocable.RequireACR))
		}
		if d.Revocable.CooldownHours < 0 {
			return bad("revocable.cooldown_hours=%d must be >= 0", d.Revocable.CooldownHours)
		}
	}
	if d.RateLimit != nil {
		if err := validateRateLimits(*d.RateLimit); err != nil {
			return bad("%v", err)
		}
	}
	for _, s := range d.RiskSignals {
		if s == "" {
			return bad("empty risk signal")
		}
	}
	return nil
}

func validateIdentifierTargets(targets []IdentifierType) error {
	if len(targets) == 0 {
		return errors.New("identifier_verifying.targets is empty")
	}
	for _, t := range targets {
		if err := t.Validate(); err != nil {
			return err
		}
		// channel 型只能验证"能收码"的通道；username 不是通道。
		if t != IdentifierPhone && t != IdentifierEmail {
			return fmt.Errorf("channel kind cannot verify identifier type %q (only phone/email)", string(t))
		}
	}
	return nil
}

func validateRateLimits(l RateLimits) error {
	if l.SendPerWindow < 0 || l.VerifyPerWindow < 0 || l.DailySend < 0 {
		return errors.New("rate limits must not be negative")
	}
	if l.SendPerWindow > 0 && l.SendWindow <= 0 {
		return errors.New("send_window must be > 0 when send_per_window > 0")
	}
	if l.VerifyPerWindow > 0 && l.VerifyWindow <= 0 {
		return errors.New("verify_window must be > 0 when verify_per_window > 0")
	}
	return nil
}
