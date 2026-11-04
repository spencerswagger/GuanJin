package factor

import (
	"context"
	"time"
)

// 能力（基线 §3 的冻结 10 个）。**字符串值即对外契约**（`GET /api/v1/factors` 的 capabilities[]）。
//
// 这张表是"文档枚举"，**不参与运行期判断**：一个因子具备哪些能力，永远由
// `Capabilities()` 用**接口断言**算出来（见下）。两者若分开维护，就会出现
// "声明能找回、实际没实现"这类漂移——而这正是基线守护规则要防的东西。
type Capability string

const (
	// CapEnrollable 能建立/解除绑定关系。
	CapEnrollable Capability = "enrollable"
	// CapIdentifierVerifying 能验证"某个标识符确实属于该用户"（channel 型）。
	CapIdentifierVerifying Capability = "identifier_verifying"
	// CapAuthenticatable 能作为认证因子。
	CapAuthenticatable Capability = "authenticatable"
	// CapACRContributing 能贡献保证级别。
	CapACRContributing Capability = "acr_contributing"
	// CapRecoverable 能作为"重新证明身份"的凭证参与找回。
	CapRecoverable Capability = "recoverable"
	// CapResettable 自己的凭据**能被重置**。
	CapResettable Capability = "resettable"
	// CapRegistrable 可用于注册（作为注册时的首个凭据）。
	CapRegistrable Capability = "registrable"
	// CapRevocable 有解绑/强制失效的联动语义。
	CapRevocable Capability = "revocable"
	// CapRateLimited 涉及外部资源消耗或可被爆破。
	CapRateLimited Capability = "rate_limited"
	// CapRiskReporting 能产生风险信号。
	CapRiskReporting Capability = "risk_reporting"
)

// AllCapabilities 返回冻结的 10 个能力（顺序固定，与基线 §3 表一致）。
func AllCapabilities() []Capability {
	return []Capability{
		CapEnrollable, CapIdentifierVerifying, CapAuthenticatable, CapACRContributing,
		CapRecoverable, CapResettable, CapRegistrable, CapRevocable,
		CapRateLimited, CapRiskReporting,
	}
}

// String 便于日志。
func (c Capability) String() string { return string(c) }

// Provider 是所有因子都必须实现的骨架：它只回答"我是谁、我是哪一类"。
//
// 行为全部在能力接口里 —— 一个因子**实现了哪些接口**，就等于它声明了哪些能力。
type Provider interface {
	// Type 因子类型（开放集合，但必须是合法小写蛇形）。
	Type() FactorType
	// Kind 承载类型（决定绑定/换绑/资源消耗语义）。
	Kind() Kind
}

// EnrollmentSpec 是 `Enrollable` 的声明参数（基线 §3 第 1 项）。
type EnrollmentSpec struct {
	// RequiresPreBoundIdentifier 是否需要前置标识符（channel 型必须 true）。
	RequiresPreBoundIdentifier bool
	// SelfServiceAllowed 能否自助绑定（管理员代绑为 false）。
	SelfServiceAllowed bool
	// VerifyRequired 绑定是否必须验证。
	VerifyRequired bool
}

// Enrollable 表示能建立/解除绑定关系。
type Enrollable interface {
	EnrollmentSpec() EnrollmentSpec
}

// AuthenticatableSpec 是 `Authenticatable` 的声明参数：可作第一因子 / 第二因子。
type AuthenticatableSpec struct {
	AsFirst  bool
	AsSecond bool
}

// Authenticatable 表示能作为认证因子。
type Authenticatable interface {
	// AuthenticatableSpec 声明可作第一/第二因子（至少一个为真）。
	AuthenticatableSpec() AuthenticatableSpec
	// BeginChallenge 开启一次挑战（channel 型在此触发投递）。
	BeginChallenge(ctx context.Context, req ChallengeRequest) (ChallengeTicket, error)
	// VerifyResponse 校验应答；成功即"该因子已达成"。
	//
	// **实现约束（I-F8 的 SPI 侧对应条款）**：本方法会在编排层持有 `flows` 行锁的
	// 短事务**之外**被调用（口令派生、TOTP 计算、短信投递都在锁外）。
	// 因此实现里**不许**把事情做成"必须持锁才能跑对"（例如依赖锁内读到的旧 amr）。
	VerifyResponse(ctx context.Context, req VerifyRequest) (VerifyResult, error)
}

// ACRSpec 是 `ACRContributing` 的声明参数：单独使用 / 组合后可达的级别。
type ACRSpec struct {
	// Sole 单独使用可达的级别。
	Sole ACR
	// Combined 与其他因子组合后可达的级别。
	Combined ACR
}

// ACRContributing 表示能贡献保证级别。
type ACRContributing interface {
	ACRSpec() ACRSpec
}

// IdentifierVerifyingSpec 是 `IdentifierVerifying` 的声明参数：能验证哪些标识符类型。
type IdentifierVerifyingSpec struct {
	// Targets 能验证的标识符类型（channel 型只有 phone / email）。
	Targets []IdentifierType
}

// IdentifierVerifying 表示能验证"某个标识符确实属于该用户"。
type IdentifierVerifying interface {
	IdentifierVerifyingSpec() IdentifierVerifyingSpec
}

// RecoverableSpec 是 `Recoverable` 的声明参数：信任等级（1..3，0 表示未验证不算信任）。
type RecoverableSpec struct {
	// TrustLevel 信任等级；对应 `identifiers.trust_level` 的 1/2/3。
	TrustLevel int
}

// Recoverable 表示能作为"重新证明身份"的凭证参与找回。
//
// 基线最容易设计错的一格：**口令不实现本接口**（`Recoverable = ❌`）——
// 用密码找回密码是循环论证。这不是靠 `if type == "password"` 拦，而是靠
// "`PasswordProvider` 根本不实现 Recoverable"，于是能力断言自动为 false。
type Recoverable interface {
	RecoverableSpec() RecoverableSpec
	// BeginRecovery 发起找回挑战（与登录挑战同一套机制，purpose=recover）。
	BeginRecovery(ctx context.Context, req ChallengeRequest) (ChallengeTicket, error)
}

// ProofRequirement 是"重置该凭据需要什么证明"。
type ProofRequirement struct {
	// MinACR 重置所需的最低会话/流程级别。
	MinACR ACR
	// MinTrustLevel 所需的标识符信任等级（0 = 不要求）。
	MinTrustLevel int
	// Description 面向管理员的说明（进审计与后台展示，不做逻辑判断）。
	Description string
}

// Resettable 表示"自己的凭据能被重置"。
//
// 基线：channel 型 **不实现** 本接口——短信/邮箱本身没有"凭据"可重置，
// 重置的是**标识符**（换手机号）。Descriptor 校验会在注册期拦住这个错配。
type Resettable interface {
	RequiredProofForReset() ProofRequirement
	Reset(ctx context.Context, req ResetRequest) error
}

// RegistrableSpec 是 `Registrable` 的声明参数：作为注册首个凭据时是否强制验证。
type RegistrableSpec struct {
	VerifyRequired bool
}

// Registrable 表示可用于注册（作为注册时的首个凭据）。
type Registrable interface {
	RegistrableSpec() RegistrableSpec
}

// UnbindBehavior 是标识符解绑时因子的联动失效时机。
type UnbindBehavior string

const (
	// UnbindNone 不联动（holding 型：解绑标识符与我的凭据无关）。
	UnbindNone UnbindBehavior = "none"
	// UnbindImmediate 立即失效（channel 型：手机号没了，短信因子当场不可用）。
	UnbindImmediate UnbindBehavior = "immediate"
	// UnbindDeferred 延迟失效（观察期内仍可用，给"换号"留缓冲）。
	UnbindDeferred UnbindBehavior = "deferred"
)

// Valid 报告联动时机是否是封闭三值之一。
func (u UnbindBehavior) Valid() bool {
	switch u {
	case UnbindNone, UnbindImmediate, UnbindDeferred:
		return true
	default:
		return false
	}
}

// String 便于日志。
func (u UnbindBehavior) String() string { return string(u) }

// RevocableSpec 是 `Revocable` 的声明参数（基线 §3 第 8 项）。
type RevocableSpec struct {
	// OnIdentifierUnbind 标识符解绑时的失效时机。
	OnIdentifierUnbind UnbindBehavior
	// RequireACR 解绑/撤销前需要达到的级别。
	RequireACR ACR
	// CooldownHours 冷却期（小时，0 = 无冷却）。
	CooldownHours int
}

// Revocable 表示有解绑/强制失效的联动语义。
type Revocable interface {
	RevocableSpec() RevocableSpec
}

// RateLimits 是限流声明（基线 §3 第 9 项）。
//
// 用**结构化整数 + 时长**而不是 `"5/10min"` 这种字符串：字符串要么被正则解析
// （解析失败怎么办？只能塞默认值——那就是隐性逻辑），要么原样展示（等于没约束）。
type RateLimits struct {
	// SendPerWindow / SendWindow 发送频率（channel 型必填）。
	SendPerWindow int
	SendWindow    time.Duration
	// VerifyPerWindow / VerifyWindow 校验频率（可爆破的因子必填）。
	VerifyPerWindow int
	VerifyWindow    time.Duration
	// DailySend 日配额（channel 型必填：短信要花钱）。
	DailySend int
	// BudgetAlert 触达预算阈值是否告警。
	BudgetAlert bool
}

// RateLimited 表示涉及外部资源消耗或可被爆破。
type RateLimited interface {
	Limits() RateLimits
}

// RiskReporting 表示能产生风险信号。
type RiskReporting interface {
	RiskSignals() []RiskSignal
}

// Capabilities 用**接口断言**算出一个因子具备哪些能力（唯一权威）。
//
// 这就是基线守护规则的落地：不读 `Type()`、不查声明表，只问 Go 的类型系统。
// 因此"声明"与"实现"在结构上不可能漂移 —— 因为根本没有第二份声明。
func Capabilities(p Provider) []Capability {
	if p == nil {
		return nil
	}
	out := make([]Capability, 0, len(AllCapabilities()))
	if _, ok := p.(Enrollable); ok {
		out = append(out, CapEnrollable)
	}
	if _, ok := p.(IdentifierVerifying); ok {
		out = append(out, CapIdentifierVerifying)
	}
	if _, ok := p.(Authenticatable); ok {
		out = append(out, CapAuthenticatable)
	}
	if _, ok := p.(ACRContributing); ok {
		out = append(out, CapACRContributing)
	}
	if _, ok := p.(Recoverable); ok {
		out = append(out, CapRecoverable)
	}
	if _, ok := p.(Resettable); ok {
		out = append(out, CapResettable)
	}
	if _, ok := p.(Registrable); ok {
		out = append(out, CapRegistrable)
	}
	if _, ok := p.(Revocable); ok {
		out = append(out, CapRevocable)
	}
	if _, ok := p.(RateLimited); ok {
		out = append(out, CapRateLimited)
	}
	if _, ok := p.(RiskReporting); ok {
		out = append(out, CapRiskReporting)
	}
	return out
}

// Has 报告因子是否具备某能力（能力断言的便捷入口）。
func Has(p Provider, c Capability) bool {
	for _, got := range Capabilities(p) {
		if got == c {
			return true
		}
	}
	return false
}
