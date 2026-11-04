package holding

import (
	"context"
	"errors"
	"time"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
)

var (
	// ErrNilStore 表示构造因子时没给凭据仓储。
	ErrNilStore = errors.New("holding: credential store is required")
	// ErrNilClock 表示没给时钟（时间窗判定必须用**同一个钟**，否则窗口语义漂移）。
	ErrNilClock = errors.New("holding: clock is required")
	// ErrNilSecretKey 表示没给 TOTP 共享密钥的 SM4 键（必须来自 MasterKey 派生）。
	ErrNilSecretKey = errors.New("holding: totp secret key is required (derive via crypto.MasterKey)")
	// ErrNilRegistry 表示装配时没给注册表。
	ErrNilRegistry = errors.New("holding: factor registry is required")
	// ErrNilProvider 表示装配列表里混进了 nil。
	ErrNilProvider = errors.New("holding: nil provider in assembly list")
)

// TOTPRecord 把"凭据信封"与"它在库里的 id"绑在一起。
//
// 为什么不让 Store 只回信封：TOTP 的重放防护要**原子推进某一行**的 `last_step`
// （见 `TOTPStore.ConsumeStep`），没有 id 就无从下手；让调用方按 envelope 反查 id
// 又会多一次"读 → 写"的竞态窗口。
type TOTPRecord struct {
	AuthenticatorID string
	Envelope        credential.TOTPEnvelope
}

// TOTPStore 是动态口令凭据的读写窄接口（实现见 `internal/identity/credentialstore.go`）。
type TOTPStore interface {
	// ListTOTPRecords 返回该用户当前可用的 TOTP 凭据。
	ListTOTPRecords(ctx context.Context, userID string) ([]TOTPRecord, error)
	// ConsumeStep 原子推进"已消费时间步"，返回是否抢到（false = 重放/并发双花）。
	ConsumeStep(ctx context.Context, authenticatorID string, step int64) (bool, error)
}

// TOTPProvider 是动态口令因子（第二因子为主）。
type TOTPProvider struct {
	store   TOTPStore
	secrets crypto.SM4Key
	now     func() time.Time
}

// NewTOTP 构造 TOTP 因子。
func NewTOTP(store TOTPStore, secretKey crypto.SM4Key, now func() time.Time) (*TOTPProvider, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if secretKey.IsZero() {
		return nil, ErrNilSecretKey
	}
	if now == nil {
		return nil, ErrNilClock
	}
	return &TOTPProvider{store: store, secrets: secretKey, now: now}, nil
}

// Type 实现 factor.Provider。
func (p *TOTPProvider) Type() factor.FactorType { return factor.FactorTOTP }

// Kind 实现 factor.Provider（holding：TOTP 密钥是用户持有的长期材料）。
func (p *TOTPProvider) Kind() factor.Kind { return factor.KindHolding }

// EnrollmentSpec 实现 factor.Enrollable：绑定需要先验一次码（`VerifyRequired`），
// 否则用户可能绑了一个错误的密钥，下次就登不进来。
func (p *TOTPProvider) EnrollmentSpec() factor.EnrollmentSpec {
	return factor.EnrollmentSpec{
		RequiresPreBoundIdentifier: false,
		SelfServiceAllowed:         true,
		VerifyRequired:             true,
	}
}

// AuthenticatableSpec 实现 factor.Authenticatable：**第二因子为主**（基线 §4.1）。
//
// 为什么不 `AsFirst`：单独的 TOTP 是"持有即证明"，丢手机就等于丢账号；
// 基线的矩阵把它定成第二因子，是为了强制它与口令组合。
func (p *TOTPProvider) AuthenticatableSpec() factor.AuthenticatableSpec {
	return factor.AuthenticatableSpec{AsFirst: false, AsSecond: true}
}

// BeginChallenge 实现 factor.Authenticatable（TOTP 无状态：客户端自己算码）。
func (p *TOTPProvider) BeginChallenge(_ context.Context, req factor.ChallengeRequest) (factor.ChallengeTicket, error) {
	if req.Purpose == "" || !req.Purpose.Valid() {
		return factor.ChallengeTicket{}, factor.ErrUnknownPurpose
	}
	return factor.ChallengeTicket{NextAction: factor.NextActionInputTOTP}, nil
}

// VerifyResponse 实现 factor.Authenticatable：窗口校验 + **原子重放防护**。
//
// 两步必须分开做（顺序也不能反）：
//  1. 用共享密钥校验码（`credential.VerifyTOTP`，常量时间、±skew 窗口）；
//  2. 通过后再 `ConsumeStep` 原子推进"已消费步"——抢不到就是重放，必须回
//     `TEMPORAL_CODE_REUSED`（不能因为"码本身对"就放行）。
//
// 若把 2 省掉，同一个码在 30 秒窗口内可以被无限次提交（中间人抓到就能重放）。
func (p *TOTPProvider) VerifyResponse(ctx context.Context, req factor.VerifyRequest) (factor.VerifyResult, error) {
	records, err := p.store.ListTOTPRecords(ctx, req.UserID)
	if err != nil {
		return factor.VerifyResult{}, err
	}
	for _, rec := range records {
		secret, err := rec.Envelope.Open(p.secrets)
		if err != nil {
			// 解封失败 = 密钥轮换未铺开或密文被改：按密钥不可用报（fail-closed）。
			return factor.VerifyResult{}, err
		}
		period := time.Duration(rec.Envelope.Period) * time.Second
		result, err := credential.VerifyTOTP(secret, req.Response, p.now().UTC(), credential.TOTPOptions{
			Digits: rec.Envelope.Digits,
			Period: period,
			// 窗口固定 **±1 步**（±30s）：容得下客户端时钟小偏差，又不至于把可猜空间放大太多。
			// 不读配置：这是安全参数，不是"部署口味"（改了就等于放宽爆破面）。
			Skew: 1,
		})
		if err != nil {
			if errors.Is(err, credential.ErrInvalidCredential) || errors.Is(err, credential.ErrMalformedCode) {
				continue // 换下一个凭据试（用户可能有多个绑定设备）
			}
			return factor.VerifyResult{}, err
		}
		consumed, err := p.store.ConsumeStep(ctx, rec.AuthenticatorID, result.MatchedStep)
		if err != nil {
			return factor.VerifyResult{}, err
		}
		if !consumed {
			return factor.VerifyResult{}, credential.ErrTemporalCodeReused
		}
		return factor.VerifyResult{Factor: factor.FactorTOTP, AMR: amrTOTP, ACR: factor.ACRAAL1}, nil
	}
	return factor.VerifyResult{}, credential.ErrInvalidCredential
}

// ACRSpec 实现 factor.ACRContributing：单独 AAL1，与口令组合 AAL2。
func (p *TOTPProvider) ACRSpec() factor.ACRSpec {
	return factor.ACRSpec{Sole: factor.ACRAAL1, Combined: factor.ACRAAL2}
}

// RecoverableSpec 实现 factor.Recoverable：TOTP 可参与找回，信任等级 2（信道验证级）。
func (p *TOTPProvider) RecoverableSpec() factor.RecoverableSpec {
	return factor.RecoverableSpec{TrustLevel: 2}
}

// BeginRecovery 实现 factor.Recoverable：找回挑战与登录挑战同一套机制（purpose=recover）。
func (p *TOTPProvider) BeginRecovery(ctx context.Context, req factor.ChallengeRequest) (factor.ChallengeTicket, error) {
	req.Purpose = factor.PurposeRecover
	return p.BeginChallenge(ctx, req)
}

// RevocableSpec 实现 factor.Revocable（holding：与标识符解绑无关）。
func (p *TOTPProvider) RevocableSpec() factor.RevocableSpec {
	return factor.RevocableSpec{OnIdentifierUnbind: factor.UnbindNone, RequireACR: factor.ACRAAL2, CooldownHours: 0}
}

// Limits 实现 factor.RateLimited（动态口令可被爆破：6 位数字空间）。
func (p *TOTPProvider) Limits() factor.RateLimits {
	return factor.RateLimits{VerifyPerWindow: 5, VerifyWindow: 10 * time.Minute}
}

// RiskSignals 实现 factor.RiskReporting。
func (p *TOTPProvider) RiskSignals() []factor.RiskSignal {
	return []factor.RiskSignal{factor.RiskFailBurst, factor.RiskDeviceChange}
}

const (
	// amrTOTP 是动态口令的 `amr` 取值（数据模型附录 B）。
	amrTOTP = "totp"
)
