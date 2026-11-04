// Package factor 是「因子 SPI」：能力接口 + 能力声明 + 注册表。
//
// 口径（基线 + decisions.md）：
//   - 因子分类与能力模型 §2/§3：`kind` × `capabilities` 两个正交维度；
//   - **禁因子类型分支**：平台按能力断言使用因子，代码里不许出现 `if type == "sms"`
//     （基线 §7 守护规则 1）。本包因此**不提供**任何"按类型判断"的辅助函数；
//   - D2：`purpose` 冻结 6 值（`login/register/step_up/enroll/recover/verify_identifier`），
//     作废值 `bind`/`stepup` 不再出现，**未知值报错、不静默回退**；
//   - D12：对外 `acr` 只有 `AAL1`/`AAL2`；`AAL3` 预留但 V1 任何路径不得产出；
//   - 新增因子必须**零 DDL**（`authenticators.type` 开放 + `credential_data` jsonb），
//     所以本包的单据（Descriptor）全部是内存结构，不落表、不碰库。
//
// 本包只定义契约与自检，**不含**任何具体因子实现：
//   - 具体 Provider（password / totp / sms_otp …）在 `internal/factor/holding|channel`，
//     它们需要 `challenges` / `authenticators` 仓储，随 S4b/S5 落地；
//   - 密码学在 `internal/crypto`，凭据语义在 `internal/credential`。
package factor

import (
	"errors"
	"fmt"
	"time"
)

// Kind 是因子的承载类型（基线 §2 的封闭三值）。
//
// kind 决定三件事（基线原文）：是否需前置绑定标识符、换绑语义、是否消耗外部资源。
// 因此 `kind` 是**一等领域概念**，按 kind 做校验不违反"禁类型分支"——基线自己就是这么分类的；
// 被禁的是按 `Type()`（如 "sms"）判断行为。
type Kind string

const (
	// KindHolding 持有型：用户持有一份长期密钥材料（密码 / TOTP / 恢复码 / WebAuthn）。
	KindHolding Kind = "holding"
	// KindChannel 信道型：通过已绑定的通信通道投递一次性码（短信 / 邮箱）。
	KindChannel Kind = "channel"
	// KindProofing 核验型：核验用户是否对应真实自然人（人脸实人核验）。
	//
	// **不属本平台**（基线 §2：归"身份核验"独立模块）。本包保留该值只为让
	// "上线了 proofing 因子"这类误装配在注册表处**显式报错**，而不是静默混进认证链。
	KindProofing Kind = "proofing"
)

// Valid 报告 kind 是否是冻结三值之一。
func (k Kind) Valid() bool {
	switch k {
	case KindHolding, KindChannel, KindProofing:
		return true
	default:
		return false
	}
}

// String 便于日志与错误信息。
func (k Kind) String() string { return string(k) }

// FactorType 是因子类型标识（**开放集合**：`authenticators.type` 是 text 无 CHECK）。
//
// 下面这些常量只是 V1 的**种子取值**（数据模型附录 A），不是封闭枚举：
// 新增因子只需在注册表里注册一个新 `FactorType`，不改 DDL、不改本文件。
type FactorType string

// V1 种子因子类型（数据模型附录 A / 因子模型 §4.1）。
const (
	FactorPassword     FactorType = "password"
	FactorTOTP         FactorType = "totp"
	FactorRecoveryCode FactorType = "recovery_code"
	FactorSMSOTP       FactorType = "sms_otp"
	FactorEmailOTP     FactorType = "email_otp"
	FactorWebAuthn     FactorType = "webauthn"
)

// ErrInvalidFactorType 表示因子类型标识不合规。
var ErrInvalidFactorType = errors.New("factor: invalid factor type")

// Validate 检查类型标识：非空、小写蛇形。
//
// 为什么要查格式：`challenges.factor_type` / `authenticators.type` 都是开放 text，
// 若允许 "Password" 与 "password" 并存，同一因子会分裂成两条数据线，而 DB 不会拦。
func (t FactorType) Validate() error {
	s := string(t)
	if s == "" {
		return fmt.Errorf("%w: empty", ErrInvalidFactorType)
	}
	if len(s) > 64 {
		return fmt.Errorf("%w: %q longer than 64", ErrInvalidFactorType, s)
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && (r == '_' || (r >= '0' && r <= '9')):
		default:
			return fmt.Errorf("%w: %q must be lowercase snake_case (start with a letter)", ErrInvalidFactorType, s)
		}
	}
	return nil
}

// String 便于日志。
func (t FactorType) String() string { return string(t) }

// Purpose 是一次挑战的用途。**封闭六值**（decisions.md D2）。
//
// 作废：DDL 原写法 `bind`/`stepup`（已由 D2 废止，`bind` → `enroll`、`stepup` → `step_up`）。
// 本包**不做别名映射**：老值传进来就是错误——静默改写会让"某个流程悄悄走了另一条分支"。
type Purpose string

const (
	// PurposeLogin 登录（含阶梯补步的第二因子）。
	PurposeLogin Purpose = "login"
	// PurposeRegister 注册（注册即登录；D2 明确不与 login 合并）。
	PurposeRegister Purpose = "register"
	// PurposeStepUp 已登录会话的阶梯升级。
	PurposeStepUp Purpose = "step_up"
	// PurposeEnroll 绑定新因子/新凭据（原 DDL 的 bind）。
	PurposeEnroll Purpose = "enroll"
	// PurposeRecover 找回（统一恢复模型：找回 = 换绑 = 重置同一链路）。
	PurposeRecover Purpose = "recover"
	// PurposeVerifyIdentifier 验证标识符归属（绑定新手机/邮箱时）。
	PurposeVerifyIdentifier Purpose = "verify_identifier"
)

// ErrUnknownPurpose 表示 purpose 不在 D2 的六值词表内。
var ErrUnknownPurpose = errors.New("factor: unknown purpose")

// Valid 报告 p 是否在六值词表内。
func (p Purpose) Valid() bool {
	switch p {
	case PurposeLogin, PurposeRegister, PurposeStepUp, PurposeEnroll, PurposeRecover, PurposeVerifyIdentifier:
		return true
	default:
		return false
	}
}

// String 便于日志。
func (p Purpose) String() string { return string(p) }

// ParsePurpose 严格解析：未知值直接报错（**禁止**"认不出来就当 login"）。
func ParsePurpose(raw string) (Purpose, error) {
	p := Purpose(raw)
	if !p.Valid() {
		return "", fmt.Errorf("%w: %q (allowed: %s)", ErrUnknownPurpose, raw, joinPurposes())
	}
	return p, nil
}

// Purposes 返回冻结六值（顺序固定，供文档与测试对齐）。
func Purposes() []Purpose {
	return []Purpose{
		PurposeLogin, PurposeRegister, PurposeStepUp,
		PurposeEnroll, PurposeRecover, PurposeVerifyIdentifier,
	}
}

func joinPurposes() string {
	out := ""
	for i, p := range Purposes() {
		if i > 0 {
			out += "|"
		}
		out += string(p)
	}
	return out
}

// ACR 是保证级别（Authentication Context Class Reference）。
//
// D12：对外只 `AAL1`/`AAL2`（`L3` 档位 → `AAL2`）；`AAL3` **预留在 CHECK 词表里但 V1 不得产出**；
// `AAL0` 只在前端展示"未认证"，**任何表都不得落库**——所以它在这个类型里根本不存在。
type ACR string

const (
	// ACRAAL1 单因子级别（索引 §4.2-15：L1/L2 → AAL1）。
	ACRAAL1 ACR = "AAL1"
	// ACRAAL2 双因子级别（索引 §4.2-15：L3 → AAL2）。
	ACRAAL2 ACR = "AAL2"
	// ACRAAL3Reserved 预留值：列 CHECK 里有、**V1 任何路径不得产出**（D12）。
	ACRAAL3Reserved ACR = "AAL3"
)

// Valid 报告 acr 是否是列 CHECK 认识的三个值之一（含预留的 AAL3）。
func (a ACR) Valid() bool {
	switch a {
	case ACRAAL1, ACRAAL2, ACRAAL3Reserved:
		return true
	default:
		return false
	}
}

// Issuable 报告 acr 是否**允许在本平台产出**（D12：只有 AAL1/AAL2）。
//
// 与 Valid 分开是刻意的：`AAL3` 合法（DB CHECK 认）但不许产出。
// 能力声明里出现 `AAL3` 就在注册期报错——这是"对外契约不实"的最早拦截点。
func (a ACR) Issuable() bool {
	return a == ACRAAL1 || a == ACRAAL2
}

// rank 给出级别强弱（仅用于比较，不对外暴露）。
func (a ACR) rank() int {
	switch a {
	case ACRAAL1:
		return 1
	case ACRAAL2:
		return 2
	case ACRAAL3Reserved:
		return 3
	default:
		return 0
	}
}

// AtLeast 报告 a 是否不低于 other（未知值一律 false）。
func (a ACR) AtLeast(other ACR) bool {
	if !a.Valid() || !other.Valid() {
		return false
	}
	return a.rank() >= other.rank()
}

// String 便于日志。
func (a ACR) String() string { return string(a) }

// Role 是某一步在整体流程中的位置（因子模型 §3 `Authenticatable` 的声明参数）。
type Role string

const (
	// RoleFirst 可作第一因子。
	RoleFirst Role = "first"
	// RoleSecond 可作第二因子。
	RoleSecond Role = "second"
)

// Valid 报告 role 是否合法。
func (r Role) Valid() bool { return r == RoleFirst || r == RoleSecond }

// String 便于日志。
func (r Role) String() string { return string(r) }

// IdentifierType 是标识符类型（**开放集合**：`identifiers.type` 是 text 无 CHECK）。
type IdentifierType string

const (
	// IdentifierPhone 手机号（占用外部稀缺资源，必须验证）。
	IdentifierPhone IdentifierType = "phone"
	// IdentifierEmail 邮箱（同上）。
	IdentifierEmail IdentifierType = "email"
	// IdentifierUsername 用户名（平台内部，无需验证）。
	IdentifierUsername IdentifierType = "username"
	// IdentifierEmployeeNo 工号（管理侧录入型，V1 可不启用）。
	IdentifierEmployeeNo IdentifierType = "employee_no"
)

// Validate 检查标识符类型格式（非空、小写蛇形；不限制取值域）。
func (t IdentifierType) Validate() error {
	if t == "" {
		return fmt.Errorf("factor: empty identifier type")
	}
	for i, r := range string(t) {
		switch {
		case r >= 'a' && r <= 'z':
		case i > 0 && (r == '_' || (r >= '0' && r <= '9')):
		default:
			return fmt.Errorf("factor: identifier type %q must be lowercase snake_case (start with a letter)", string(t))
		}
	}
	return nil
}

// String 便于日志。
func (t IdentifierType) String() string { return string(t) }

// NextAction 是挑战的下一步交互形态（接口 §3.2.1 响应字段 `next_action`，封闭三值）。
//
// 它是**前端渲染原子**的输入（页面规格 §4.2），所以必须是封闭集合：
// 新增交互形态要同时改前端原子映射，属契约变更。
type NextAction string

const (
	// NextActionTypeCode 输入短信/邮箱验证码。
	NextActionTypeCode NextAction = "type_code"
	// NextActionInputTOTP 输入动态口令。
	NextActionInputTOTP NextAction = "input_totp"
	// NextActionInputPassword 输入口令。
	NextActionInputPassword NextAction = "input_password"
)

// Valid 报告是否封闭三值之一。
func (n NextAction) Valid() bool {
	switch n {
	case NextActionTypeCode, NextActionInputTOTP, NextActionInputPassword:
		return true
	default:
		return false
	}
}

// String 便于日志。
func (n NextAction) String() string { return string(n) }

// RiskSignal 是因子能产生的风险信号类型（**开放集合**，基线 §3 `RiskReporting`）。
type RiskSignal string

// V1 种子信号（因子模型 §4.1 的 `risk_signals` 示例）。
const (
	RiskSendFrequency RiskSignal = "send_frequency"
	RiskGeoJump       RiskSignal = "geo_jump"
	RiskDeviceChange  RiskSignal = "device_change"
	RiskFailBurst     RiskSignal = "fail_burst"
)

// String 便于日志。
func (s RiskSignal) String() string { return string(s) }

// ChallengeRequest 是「开一次挑战」的入参。
//
// 注意 `TargetMasked`：**因子不解密标识符**。脱敏目标由标识符层给出
// （`identifiers.value_masked`），因子只负责"发到哪、发什么"，不碰 SM4 明文。
type ChallengeRequest struct {
	// FlowID 所属阶梯流；无流挑战（enroll / verify_identifier）为空。
	FlowID string
	// ClientID 业务系统上下文（登录/注册必带）。
	ClientID string
	// UserID 目标用户；注册前的匿名挑战为空。
	UserID string
	// Purpose 见 D2 六值。
	Purpose Purpose
	// Target 投递目标类型（channel 型必填；holding 型忽略）。
	Target IdentifierType
	// TargetMasked 已脱敏的投递目标，仅用于回显与日志。
	TargetMasked string
}

// ChallengeTicket 是一次挑战的对外凭据（编排层落库后回填 `ChallengeID`）。
//
// `ChallengeID` 为空表示"这个因子不落挑战行"（如口令：客户端直接提交口令，
// 无需服务端预置一次性码）。是否落库由编排层按 `NextAction` 判断，不在这里决定。
type ChallengeTicket struct {
	ChallengeID  string
	NextAction   NextAction
	TargetMasked string
	TTL          time.Duration
	AttemptsLeft int
	ExpiresAt    time.Time
}

// VerifyRequest 是「校验挑战应答」的入参。
type VerifyRequest struct {
	ChallengeID string
	FlowID      string
	UserID      string
	Response    string
}

// VerifyResult 是一次成功的因子校验产出。
//
// `AMR` 是 RFC 8176 语义的取值（附录 B：`pwd` / `sms_otp` / `totp` …）；
// `ACR` 是该因子**单独**可达的级别（组合后的级别由编排层按 `ACRSpec.Combined` 计算）。
type VerifyResult struct {
	Factor FactorType
	AMR    string
	ACR    ACR
}

// ResetRequest 是「重置凭据」的入参（`Resettable`）。
type ResetRequest struct {
	UserID          string
	AuthenticatorID string
	Proof           string
}

// AMRFor 返回因子对应的 `amr` 取值（数据模型附录 B 的冻结表）。
//
// 第二个返回值是"这张表认不认识它"：新增因子未登记时返回 `(type, false)`，
// 由调用方决定报错还是用类型名兜底——**不在这里替调用方决定**（禁隐性逻辑）。
func AMRFor(t FactorType) (string, bool) {
	switch t {
	case FactorPassword:
		return "pwd", true
	case FactorTOTP:
		return "totp", true
	case FactorSMSOTP:
		return "sms_otp", true
	case FactorEmailOTP:
		return "email_otp", true
	case FactorRecoveryCode:
		return "recovery_code", true
	case FactorWebAuthn:
		return "webauthn", true
	default:
		return string(t), false
	}
}
