package factor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 本文件用**能力碎片**拼出各种因子形态：每个 capXxx 结构实现一个能力接口，
// 把它们嵌入同一个结构就等于"这个因子具备这些能力"。
//
// 这样做正是为了让测试也走**能力断言**那条路：没有一处按 Type() 分支，
// 也没有一份手写的能力清单。

type prov struct {
	t FactorType
	k Kind
}

func (p prov) Type() FactorType { return p.t }
func (p prov) Kind() Kind       { return p.k }

type capEnroll struct{ spec EnrollmentSpec }

func (c capEnroll) EnrollmentSpec() EnrollmentSpec { return c.spec }

type capAuth struct{ spec AuthenticatableSpec }

func (c capAuth) AuthenticatableSpec() AuthenticatableSpec { return c.spec }
func (c capAuth) BeginChallenge(_ context.Context, _ ChallengeRequest) (ChallengeTicket, error) {
	return ChallengeTicket{NextAction: NextActionInputPassword}, nil
}
func (c capAuth) VerifyResponse(_ context.Context, _ VerifyRequest) (VerifyResult, error) {
	return VerifyResult{}, nil
}

type capACR struct{ spec ACRSpec }

func (c capACR) ACRSpec() ACRSpec { return c.spec }

type capIdent struct{ spec IdentifierVerifyingSpec }

func (c capIdent) IdentifierVerifyingSpec() IdentifierVerifyingSpec { return c.spec }

type capRecover struct{ spec RecoverableSpec }

func (c capRecover) RecoverableSpec() RecoverableSpec { return c.spec }
func (c capRecover) BeginRecovery(_ context.Context, _ ChallengeRequest) (ChallengeTicket, error) {
	return ChallengeTicket{}, nil
}

type capReset struct{ spec ProofRequirement }

func (c capReset) RequiredProofForReset() ProofRequirement { return c.spec }
func (c capReset) Reset(_ context.Context, _ ResetRequest) error {
	return nil
}

type capRegist struct{ spec RegistrableSpec }

func (c capRegist) RegistrableSpec() RegistrableSpec { return c.spec }

type capRevoke struct{ spec RevocableSpec }

func (c capRevoke) RevocableSpec() RevocableSpec { return c.spec }

type capRate struct{ spec RateLimits }

func (c capRate) Limits() RateLimits { return c.spec }

type capRisk struct{ spec []RiskSignal }

func (c capRisk) RiskSignals() []RiskSignal { return c.spec }

// ---- 与基线 §4.1 能力矩阵一一对应的四个"形态"（只用于测试）----

// passwordLike：holding + 可认证（第一）+ ACR + 可注册 + 可撤销 + 限流 + 风险 + 可重置。
// **不实现 Recoverable** —— 基线「密码不能找回自己」。
type passwordLike struct {
	prov
	capEnroll
	capAuth
	capACR
	capRegist
	capRevoke
	capRate
	capRisk
	capReset
}

// recoveryCodeLike：holding + 可找回（信任 3）+ 可重置 + 可撤销。**不实现 Authenticatable**。
type recoveryCodeLike struct {
	prov
	capEnroll
	capRecover
	capReset
	capRevoke
}

// smsLike：channel + 验标识符 + 可认证（第一+第二）+ ACR + 可找回 + 可注册（强制验证）+ 限流 + 风险。
// **不实现 Resettable**（信道型重置的是标识符）。
type smsLike struct {
	prov
	capEnroll
	capIdent
	capAuth
	capACR
	capRecover
	capRegist
	capRevoke
	capRate
	capRisk
}

func newPassword() passwordLike {
	return passwordLike{
		prov:      prov{t: FactorPassword, k: KindHolding},
		capEnroll: capEnroll{EnrollmentSpec{RequiresPreBoundIdentifier: false, SelfServiceAllowed: true}},
		capAuth:   capAuth{AuthenticatableSpec{AsFirst: true}},
		capACR:    capACR{ACRSpec{Sole: ACRAAL1, Combined: ACRAAL2}},
		capRegist: capRegist{RegistrableSpec{VerifyRequired: false}},
		capRevoke: capRevoke{RevocableSpec{OnIdentifierUnbind: UnbindNone, RequireACR: ACRAAL2, CooldownHours: 24}},
		capRate:   capRate{RateLimits{VerifyPerWindow: 5, VerifyWindow: 15 * time.Minute}},
		capRisk:   capRisk{[]RiskSignal{RiskFailBurst, RiskGeoJump}},
		capReset:  capReset{ProofRequirement{MinACR: ACRAAL2, MinTrustLevel: 2, Description: "需其他因子证明身份"}},
	}
}

func newRecoveryCode() recoveryCodeLike {
	return recoveryCodeLike{
		prov:       prov{t: FactorRecoveryCode, k: KindHolding},
		capEnroll:  capEnroll{EnrollmentSpec{SelfServiceAllowed: false, VerifyRequired: true}},
		capRecover: capRecover{RecoverableSpec{TrustLevel: 3}},
		capReset:   capReset{ProofRequirement{MinACR: ACRAAL2, MinTrustLevel: 2}},
		capRevoke:  capRevoke{RevocableSpec{OnIdentifierUnbind: UnbindNone, RequireACR: ACRAAL1}},
	}
}

func newSMS() smsLike {
	return smsLike{
		prov:       prov{t: FactorSMSOTP, k: KindChannel},
		capEnroll:  capEnroll{EnrollmentSpec{RequiresPreBoundIdentifier: true, SelfServiceAllowed: true, VerifyRequired: true}},
		capIdent:   capIdent{IdentifierVerifyingSpec{Targets: []IdentifierType{IdentifierPhone}}},
		capAuth:    capAuth{AuthenticatableSpec{AsFirst: true, AsSecond: true}},
		capACR:     capACR{ACRSpec{Sole: ACRAAL1, Combined: ACRAAL2}},
		capRecover: capRecover{RecoverableSpec{TrustLevel: 2}},
		capRegist:  capRegist{RegistrableSpec{VerifyRequired: true}},
		capRevoke:  capRevoke{RevocableSpec{OnIdentifierUnbind: UnbindImmediate, RequireACR: ACRAAL2, CooldownHours: 24}},
		capRate: capRate{RateLimits{
			SendPerWindow: 1, SendWindow: time.Minute, VerifyPerWindow: 5, VerifyWindow: 10 * time.Minute,
			DailySend: 10, BudgetAlert: true,
		}},
		capRisk: capRisk{[]RiskSignal{RiskSendFrequency, RiskGeoJump, RiskDeviceChange}},
	}
}

// 基线矩阵（§4.1）的**可执行版本**：四个形态各自该有什么能力、不该有什么能力。
func TestCapabilityMatrixMatchesBaseline(t *testing.T) {
	cases := []struct {
		name   string
		p      Provider
		want   []Capability
		absent []Capability
	}{
		{
			name: "password",
			p:    newPassword(),
			want: []Capability{CapEnrollable, CapAuthenticatable, CapACRContributing, CapResettable,
				CapRegistrable, CapRevocable, CapRateLimited, CapRiskReporting},
			// 基线最容易设计错的一格：密码不能找回自己；holding 不验标识符。
			absent: []Capability{CapRecoverable, CapIdentifierVerifying},
		},
		{
			name: "recovery_code",
			p:    newRecoveryCode(),
			want: []Capability{CapEnrollable, CapRecoverable, CapResettable, CapRevocable},
			// 恢复码只能找回、不能日常登录。
			absent: []Capability{CapAuthenticatable, CapACRContributing, CapRegistrable},
		},
		{
			name: "sms_otp",
			p:    newSMS(),
			want: []Capability{CapEnrollable, CapIdentifierVerifying, CapAuthenticatable, CapACRContributing,
				CapRecoverable, CapRegistrable, CapRevocable, CapRateLimited, CapRiskReporting},
			// channel 型没有"凭据"可重置（重置的是标识符）。
			absent: []Capability{CapResettable},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Capabilities(tc.p)
			for _, c := range tc.want {
				if !Has(tc.p, c) {
					t.Errorf("%s 应具备能力 %s，实际 %v", tc.name, c, got)
				}
			}
			for _, c := range tc.absent {
				if Has(tc.p, c) {
					t.Errorf("%s 不应具备能力 %s（基线 §4.1）", tc.name, c)
				}
			}
			if len(got) != len(tc.want) {
				t.Errorf("%s 能力数 = %d，期望 %d（多出来的能力意味着基线矩阵被改了）: %v",
					tc.name, len(got), len(tc.want), got)
			}
		})
	}
}

// 能力表本身：10 个、顺序固定、无重复、无空值。
func TestAllCapabilitiesFrozen(t *testing.T) {
	all := AllCapabilities()
	if len(all) != 10 {
		t.Fatalf("冻结 10 个能力，实际 %d", len(all))
	}
	seen := map[Capability]bool{}
	for _, c := range all {
		if c == "" {
			t.Fatal("能力名不得为空")
		}
		if seen[c] {
			t.Fatalf("能力重复：%s", c)
		}
		seen[c] = true
	}
}

// nil / 无能力 的边界。
func TestCapabilitiesNilAndEmpty(t *testing.T) {
	if got := Capabilities(nil); got != nil {
		t.Fatalf("nil 因子应返回 nil，实际 %v", got)
	}
	empty := struct{ prov }{prov{t: "weird_factor", k: KindHolding}}
	if got := Capabilities(empty); len(got) != 0 {
		t.Fatalf("只实现 Provider 的因子不应有任何能力，实际 %v", got)
	}
	// Describe 必须拒绝"一个能力都没有"的因子。
	if _, err := Describe(empty); !errors.Is(err, ErrDescriptorInvalid) {
		t.Fatalf("无能力的因子必须被拒，got %v", err)
	}
}

// Describe 必须把各种 Spec 从**实现**里抽出来（而不是让调用方另填一份）。
func TestDescribeFillsSpecsFromImplementation(t *testing.T) {
	d, err := Describe(newSMS())
	if err != nil {
		t.Fatalf("Describe(sms): %v", err)
	}
	if d.Type != FactorSMSOTP || d.Kind != KindChannel {
		t.Fatalf("type/kind 不对: %+v", d)
	}
	if d.Enrollment == nil || !d.Enrollment.RequiresPreBoundIdentifier {
		t.Fatalf("enrollable spec 未抽出: %+v", d.Enrollment)
	}
	if d.Authenticatable == nil || !d.Authenticatable.AsFirst || !d.Authenticatable.AsSecond {
		t.Fatalf("authenticatable spec 未抽出: %+v", d.Authenticatable)
	}
	if d.ACR == nil || d.ACR.Sole != ACRAAL1 || d.ACR.Combined != ACRAAL2 {
		t.Fatalf("acr spec 未抽出: %+v", d.ACR)
	}
	if d.IdentifierVerifying == nil || len(d.IdentifierVerifying.Targets) != 1 {
		t.Fatalf("identifier_verifying spec 未抽出: %+v", d.IdentifierVerifying)
	}
	if d.Recoverable == nil || d.Recoverable.TrustLevel != 2 {
		t.Fatalf("recoverable spec 未抽出: %+v", d.Recoverable)
	}
	if d.Resettable != nil {
		t.Fatalf("channel 型不该有 resettable spec")
	}
	if d.Registrable == nil || !d.Registrable.VerifyRequired {
		t.Fatalf("registrable spec 未抽出: %+v", d.Registrable)
	}
	if d.Revocable == nil || d.Revocable.OnIdentifierUnbind != UnbindImmediate {
		t.Fatalf("revocable spec 未抽出: %+v", d.Revocable)
	}
	if d.RateLimit == nil || d.RateLimit.DailySend != 10 {
		t.Fatalf("rate_limit spec 未抽出: %+v", d.RateLimit)
	}
	if len(d.RiskSignals) != 3 {
		t.Fatalf("risk signals 未抽出: %+v", d.RiskSignals)
	}
}

// 描述符校验必须逐条挡住基线禁止的形态（每条一个用例，防止"某条规则其实没跑"）。
func TestDescribeRejectsViolations(t *testing.T) {
	cases := []struct {
		name string
		p    Provider
		want error
	}{
		{
			name: "nil provider",
			p:    nil,
			want: ErrNilProvider,
		},
		{
			name: "proofing kind 不属本平台",
			p: struct {
				prov
				capAuth
				capACR
			}{prov{t: "face_proofing", k: KindProofing}, capAuth{AuthenticatableSpec{AsFirst: true}}, capACR{ACRSpec{Sole: ACRAAL1}}},
			want: ErrProofingNotSupported,
		},
		{
			name: "未知 kind",
			p:    struct{ prov }{prov{t: "password", k: Kind("magic")}},
			want: ErrDescriptorInvalid,
		},
		{
			name: "非法 FactorType（大写）",
			p:    struct{ prov }{prov{t: FactorType("Password"), k: KindHolding}},
			want: ErrInvalidFactorType,
		},
		{
			name: "channel 缺 IdentifierVerifying",
			p: func() Provider {
				s := newSMS()
				return struct {
					prov
					capEnroll
					capAuth
					capACR
					capRegist
					capRevoke
					capRate
					capRisk
				}{s.prov, s.capEnroll, s.capAuth, s.capACR, s.capRegist, s.capRevoke, s.capRate, s.capRisk}
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "channel targets 为空",
			p: func() Provider {
				s := newSMS()
				s.capIdent = capIdent{}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "channel targets 含 username",
			p: func() Provider {
				s := newSMS()
				s.capIdent = capIdent{IdentifierVerifyingSpec{Targets: []IdentifierType{IdentifierUsername}}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "channel targets 含非法格式",
			p: func() Provider {
				s := newSMS()
				s.capIdent = capIdent{IdentifierVerifyingSpec{Targets: []IdentifierType{IdentifierType("Phone")}}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "channel 缺 Enrollable",
			p: func() Provider {
				s := newSMS()
				return struct {
					prov
					capIdent
					capAuth
					capACR
					capRecover
					capRegist
					capRevoke
					capRate
					capRisk
				}{s.prov, s.capIdent, s.capAuth, s.capACR, s.capRecover, s.capRegist, s.capRevoke, s.capRate, s.capRisk}
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "channel 未声明前置标识符",
			p: func() Provider {
				s := newSMS()
				s.capEnroll = capEnroll{EnrollmentSpec{RequiresPreBoundIdentifier: false}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "channel 实现 Resettable",
			p: func() Provider {
				s := newSMS()
				return struct {
					smsLike
					capReset
				}{s, capReset{ProofRequirement{MinACR: ACRAAL2}}}
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "channel 缺限流",
			p: func() Provider {
				s := newSMS()
				return struct {
					prov
					capEnroll
					capIdent
					capAuth
					capACR
					capRecover
					capRegist
					capRevoke
					capRisk
				}{s.prov, s.capEnroll, s.capIdent, s.capAuth, s.capACR, s.capRecover, s.capRegist, s.capRevoke, s.capRisk}
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "channel 日配额为 0",
			p: func() Provider {
				s := newSMS()
				s.capRate = capRate{RateLimits{SendPerWindow: 1, SendWindow: time.Minute, DailySend: 0}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "channel 注册未强制验证",
			p: func() Provider {
				s := newSMS()
				s.capRegist = capRegist{RegistrableSpec{VerifyRequired: false}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "holding 实现 IdentifierVerifying",
			p: func() Provider {
				s := newPassword()
				return struct {
					passwordLike
					capIdent
				}{s, capIdent{IdentifierVerifyingSpec{Targets: []IdentifierType{IdentifierPhone}}}}
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "holding 要求前置标识符",
			p: func() Provider {
				s := newPassword()
				s.capEnroll = capEnroll{EnrollmentSpec{RequiresPreBoundIdentifier: true}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "authenticatable 既不能当第一也不能当第二",
			p: func() Provider {
				s := newPassword()
				s.capAuth = capAuth{AuthenticatableSpec{}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "authenticatable 缺 acr.sole",
			p: func() Provider {
				s := newPassword()
				return struct {
					prov
					capEnroll
					capAuth
					capRegist
					capRevoke
					capRate
					capRisk
					capReset
				}{s.prov, s.capEnroll, s.capAuth, s.capRegist, s.capRevoke, s.capRate, s.capRisk, s.capReset}
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "acr.sole = AAL3（D12 不得产出）",
			p: func() Provider {
				s := newPassword()
				s.capACR = capACR{ACRSpec{Sole: ACRAAL3Reserved}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "acr.combined = AAL3（D12 不得产出）",
			p: func() Provider {
				s := newPassword()
				s.capACR = capACR{ACRSpec{Sole: ACRAAL1, Combined: ACRAAL3Reserved}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "acr.combined 弱于 acr.sole",
			p: func() Provider {
				s := newPassword()
				s.capACR = capACR{ACRSpec{Sole: ACRAAL2, Combined: ACRAAL1}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "recoverable.trust_level = 0",
			p: func() Provider {
				s := newRecoveryCode()
				s.capRecover = capRecover{RecoverableSpec{TrustLevel: 0}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "recoverable.trust_level = 4",
			p: func() Provider {
				s := newRecoveryCode()
				s.capRecover = capRecover{RecoverableSpec{TrustLevel: 4}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "resettable.min_acr = AAL0",
			p: func() Provider {
				s := newPassword()
				s.capReset = capReset{ProofRequirement{MinACR: ACR("AAL0")}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "resettable.min_trust_level = 4",
			p: func() Provider {
				s := newPassword()
				s.capReset = capReset{ProofRequirement{MinACR: ACRAAL2, MinTrustLevel: 4}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "registrable 但没有 authenticatable",
			p: struct {
				prov
				capEnroll
				capRegist
			}{prov{t: "weird_factor", k: KindHolding}, capEnroll{EnrollmentSpec{}}, capRegist{RegistrableSpec{}}},
			want: ErrDescriptorInvalid,
		},
		{
			name: "revocable.on_identifier_unbind 非法",
			p: func() Provider {
				s := newPassword()
				s.capRevoke = capRevoke{RevocableSpec{OnIdentifierUnbind: UnbindBehavior("later")}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "revocable.require_acr 不可产出",
			p: func() Provider {
				s := newPassword()
				s.capRevoke = capRevoke{RevocableSpec{OnIdentifierUnbind: UnbindNone, RequireACR: ACRAAL3Reserved}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "revocable.cooldown_hours 为负",
			p: func() Provider {
				s := newPassword()
				s.capRevoke = capRevoke{RevocableSpec{OnIdentifierUnbind: UnbindNone, CooldownHours: -1}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "rate limit 为负",
			p: func() Provider {
				s := newPassword()
				s.capRate = capRate{RateLimits{VerifyPerWindow: -1}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "send_per_window 没有 send_window",
			p: func() Provider {
				s := newSMS()
				s.capRate = capRate{RateLimits{SendPerWindow: 1, DailySend: 10}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "verify_per_window 没有 verify_window",
			p: func() Provider {
				s := newPassword()
				s.capRate = capRate{RateLimits{VerifyPerWindow: 5}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
		{
			name: "空风险信号",
			p: func() Provider {
				s := newPassword()
				s.capRisk = capRisk{[]RiskSignal{""}}
				return s
			}(),
			want: ErrDescriptorInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Describe(tc.p); !errors.Is(err, tc.want) {
				t.Fatalf("期望 %v，实际 %v", tc.want, err)
			}
		})
	}
}

// 合规形态必须全部通过校验（反向断言：别把闸修成"什么都拒"）。
func TestDescribeAcceptsValidShapes(t *testing.T) {
	for name, p := range map[string]Provider{
		"password":      newPassword(),
		"recovery_code": newRecoveryCode(),
		"sms_otp":       newSMS(),
	} {
		if _, err := Describe(p); err != nil {
			t.Errorf("%s 应通过校验，实际 %v", name, err)
		}
	}
}
