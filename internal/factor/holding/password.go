// Package holding 是持有型因子（holding）的具体实现：password / totp。
//
// 口径（基线 §4.1 能力矩阵 + 本仓 S4a 的 SPI）：
//   - 两个因子都**不实现** `IdentifierVerifying`（holding 不需要前置标识符，也不验标识符）；
//   - `password` **不实现** `Recoverable` —— 基线「密码不能找回自己」，
//     这条不是靠 `if type == "password"` 拦，而是靠**类型层面根本没有这个方法**；
//   - `totp` **不实现** `Registrable`（要先有绑定过程，注册流程里塞二维码很别扭）；
//   - 两者都不实现 `Resettable`：密码/TOTP 的"重置"走 M3 的统一恢复模型 + 管理面
//     `reset-password`，在 M1 里没有消费者。**实现一个没有调用方的写路径，比不实现更危险**
//     （它会让人以为"重置已经做了"）。
//
// 本包不做 SQL：凭据读写通过窄接口注入（`PasswordStore` / `TOTPStore`），
// 这样"因子怎么验"与"凭据存哪"分开，S5 的编排层可以按需接真库或测试桩。
package holding

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/factor"
)

// PasswordStore 是口令凭据的只读窄接口（实现见 `internal/identity/credentialstore.go`）。
//
// 只给"列出来"这一个能力：口令校验不需要写库（失败计数在 Redis，D3-D），
// 给了写能力只会让人在锁内顺手改点什么（I-F8 禁止重活/写扩散）。
type PasswordStore interface {
	// ListPasswordEnvelopes 返回该用户当前可用的口令信封（正常只有一条）。
	ListPasswordEnvelopes(ctx context.Context, userID string) ([]credential.PasswordEnvelope, error)
}

// PasswordProvider 是口令因子。
type PasswordProvider struct {
	store  PasswordStore
	params credential.Policy
}

// NewPassword 构造口令因子。`store` 为空或策略非法即报错（装配期 fail-fast）。
func NewPassword(store PasswordStore, policy credential.Policy) (*PasswordProvider, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &PasswordProvider{store: store, params: policy}, nil
}

// NewPasswordVerifier 构造**只用于认证校验**的口令因子（编排层 `internal/flow` 用）。
//
// 为什么不复用 `NewPassword`：那个构造器要求一份口令策略，而**校验路径不判策略**
// （策略只在注册/改密/绑定这些有 `client_id` 上下文的动作里判）。为了通过构造器自检
// 而在装配期塞一份"随便挑个档位"的策略数字，本身就是隐性默认 —— 那是第二套口径。
//
// 能力声明、`Authenticatable`、`ACRContributing` 与 `NewPassword` 完全相同（同一个类型）：
// 差别只在**是否持有策略**，而不是"两个不同的因子"。
func NewPasswordVerifier(store PasswordStore) (*PasswordProvider, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	return &PasswordProvider{store: store}, nil
}

// Type 实现 factor.Provider。
func (p *PasswordProvider) Type() factor.FactorType { return factor.FactorPassword }

// Kind 实现 factor.Provider（holding：用户持有一份长期密钥材料）。
func (p *PasswordProvider) Kind() factor.Kind { return factor.KindHolding }

// EnrollmentSpec 实现 factor.Enrollable（口令不需要前置标识符，可自助设置）。
func (p *PasswordProvider) EnrollmentSpec() factor.EnrollmentSpec {
	return factor.EnrollmentSpec{
		RequiresPreBoundIdentifier: false,
		SelfServiceAllowed:         true,
		VerifyRequired:             false,
	}
}

// AuthenticatableSpec 实现 factor.Authenticatable：口令只作**第一因子**
// （把它当第二因子没有意义：同一个口令输两遍不增加任何保证）。
func (p *PasswordProvider) AuthenticatableSpec() factor.AuthenticatableSpec {
	return factor.AuthenticatableSpec{AsFirst: true, AsSecond: false}
}

// BeginChallenge 实现 factor.Authenticatable。
//
// 口令是**无状态挑战**：服务端不需要预置一次性码（客户端直接提口令），
// 所以这里不产生 `challenges` 行 —— `ChallengeID` 留空，`NextAction=input_password`。
// 这不是"省略"，而是"口令的挑战就是那次提交本身"。
func (p *PasswordProvider) BeginChallenge(_ context.Context, req factor.ChallengeRequest) (factor.ChallengeTicket, error) {
	if req.Purpose == "" || !req.Purpose.Valid() {
		return factor.ChallengeTicket{}, factor.ErrUnknownPurpose
	}
	return factor.ChallengeTicket{NextAction: factor.NextActionInputPassword}, nil
}

// VerifyResponse 实现 factor.Authenticatable：**常量时间**比对，任一信封通过即成功。
//
// 注意这里不做失败计数与锁定：那是 `internal/risk` 的职责（Redis 权威计数，D3-D），
// 因子只管"这个值对不对"。
func (p *PasswordProvider) VerifyResponse(ctx context.Context, req factor.VerifyRequest) (factor.VerifyResult, error) {
	if req.UserID == "" {
		return factor.VerifyResult{}, credential.ErrInvalidCredential
	}
	envelopes, err := p.store.ListPasswordEnvelopes(ctx, req.UserID)
	if err != nil {
		return factor.VerifyResult{}, err
	}
	for _, env := range envelopes {
		ok, err := env.Verify(req.Response)
		if err != nil {
			// 坏信封（参数被降级/结构损坏）→ 如实报错，绝不"跳过继续试下一条"。
			return factor.VerifyResult{}, err
		}
		if ok {
			return factor.VerifyResult{Factor: factor.FactorPassword, AMR: amrPassword, ACR: factor.ACRAAL1}, nil
		}
	}
	return factor.VerifyResult{}, credential.ErrInvalidCredential
}

// ACRSpec 实现 factor.ACRContributing：单独 AAL1，与第二因子组合可达 AAL2。
func (p *PasswordProvider) ACRSpec() factor.ACRSpec {
	return factor.ACRSpec{Sole: factor.ACRAAL1, Combined: factor.ACRAAL2}
}

// RegistrableSpec 实现 factor.Registrable（注册时用口令，无需额外验证）。
func (p *PasswordProvider) RegistrableSpec() factor.RegistrableSpec {
	return factor.RegistrableSpec{VerifyRequired: false}
}

// RevocableSpec 实现 factor.Revocable：口令与标识符解绑无关（holding 型）。
func (p *PasswordProvider) RevocableSpec() factor.RevocableSpec {
	return factor.RevocableSpec{OnIdentifierUnbind: factor.UnbindNone, RequireACR: factor.ACRAAL2, CooldownHours: 0}
}

// Limits 实现 factor.RateLimited：口令可被爆破，所以**校验频率**必须有限制。
func (p *PasswordProvider) Limits() factor.RateLimits {
	return factor.RateLimits{VerifyPerWindow: 5, VerifyWindow: 15 * time.Minute}
}

// RiskSignals 实现 factor.RiskReporting。
func (p *PasswordProvider) RiskSignals() []factor.RiskSignal {
	return []factor.RiskSignal{factor.RiskFailBurst, factor.RiskGeoJump, factor.RiskDeviceChange}
}

// Policy 返回本实例使用的口令策略（供改密/注册复用同一份门槛，避免两套口径）。
func (p *PasswordProvider) Policy() credential.Policy { return p.params }

const (
	// amrPassword 是口令的 `amr` 取值（数据模型附录 B：pwd）。
	amrPassword = "pwd"
)

// ParseUserID 把 factor 层的字符串用户标识解析成 uuid（编排层传的是 UUID 字符串）。
//
// 单独一个函数是为了让"解析失败 → 凭据无效"这条判定只写一次：
// 六个因子各自 `uuid.Parse` 只会各自给出不同的错误文案。
func ParseUserID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, credential.ErrInvalidCredential
	}
	return id, nil
}
