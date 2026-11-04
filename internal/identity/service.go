// Package identity 是**账号域服务**：注册、标识符生命周期、凭据挂载。
//
// 定位：数据模型 §4.1 里那一列「服务层（唯一事务内）」——所有跨表不变式
// （I1~I6）都在这里、在**同一个事务**里判，仓储只提供读数与写操作。
//
// 三条口径（都是决定实现形态的，不是风格）：
//  1. **一次请求一个事务**（`store.Tx`）：注册要同时落 `users` / `identifiers` /
//     `authenticators` / `flows` / `auth_events`，AC-REG-07 要求失败后三表零残留。
//  2. **锁序 `users → identifiers/authenticators`**：改某账号的标识符/凭据前先 `LockUser`，
//     否则"并发解绑最后两个标识符"会各自看到 2 条、各删一条，I1/I2 直接被绕过。
//  3. **本包不签发令牌**（S6 协议面负责）：注册只交付"账号已可登录"的事实，
//     避免"账号没落库就把令牌发出去了"这类顺序错误。
package identity

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/identifier"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本包的错误族。对外码的映射只在 `CodeOf`（单一出口）。
var (
	// ErrMissingClientID 对应 `MISSING_PARAMETER`（索引 §2：注册必带业务系统上下文）。
	ErrMissingClientID = errors.New("identity: client_id is required")
	// ErrRegistrationNotAllowed 对应 `REGISTRATION_NOT_ALLOWED`（E4/403，AC-REG-09）。
	ErrRegistrationNotAllowed = errors.New("identity: self registration is not allowed for this client")
	// ErrPasswordRequired 表示 M1 的注册必须有口令（仅用户名+口令是 M1 唯一完整路径）。
	ErrPasswordRequired = errors.New("identity: password is required for registration")
	// ErrIdentifierNotFound 表示该标识符不属于此用户（也可能真的不存在——两者对外同码，防枚举）。
	ErrIdentifierNotFound = errors.New("identity: identifier not found for this user")
	// ErrAuthenticatorNotFound 表示该认证器不属于此用户。
	ErrAuthenticatorNotFound = errors.New("identity: authenticator not found for this user")
	// ErrNilDB / ErrMissingKey / ErrInvalidTTL / ErrWeakParams 是构造期校验（装配缺陷 → 启动失败）。
	ErrNilDB       = errors.New("identity: db handle is required")
	ErrMissingKey  = errors.New("identity: derived keys are required (hmac/identifier + sm4/field)")
	ErrInvalidTTL  = errors.New("identity: a positive flow TTL is required (runtime_config flow.ttl_seconds)")
	ErrWeakParams  = errors.New("identity: password params below baseline minimum")
	ErrNilProvider = errors.New("identity: nil dependency")
	// ErrChannelHasNoAuthenticator 表示试图给**信道型**因子建 `authenticators` 行。
	//
	// 数据模型 §3.3：短信/邮箱的"凭据"就是**已验证的标识符**，本身没有可重置的凭据材料；
	// 强行建行会产出"标识符与认证器指向同一事实"的冗余，并让 I3 的并集口径失真。
	ErrChannelHasNoAuthenticator = errors.New("identity: channel factors must not create authenticator rows")
)

// PasswordRejected 承载"口令不合规"的**全量命中项**（AC-REG-10：code 只回首个，
// 全量进 `details.violations[]`）。用独立类型而不是包错误，是因为它必须带着 Result 走。
type PasswordRejected struct {
	Result credential.Result
}

// Error 实现 error。
func (e *PasswordRejected) Error() string {
	return fmt.Sprintf("identity: password rejected (code=%s, violations=%d)", e.Result.Code, len(e.Result.Violations))
}

// Violations 返回全量命中项（HTTP 层放进 `details.violations[]`，AC-REG-10）。
func (e *PasswordRejected) Violations() []credential.Violation { return e.Result.Violations }

// Config 是服务依赖的**显式配置**（没有一项有"默认值"）。
type Config struct {
	// IndexKey 标识符盲索引键（`guanjin/hmac/identifier`，D4-B）。
	IndexKey crypto.HMACKey
	// FieldKey 敏感字段加密键（`guanjin/sm4/field`，D4-B）。
	FieldKey crypto.SM4Key
	// PasswordParams 口令派生参数（来自 login_policies + 平台冻结下限）。
	PasswordParams crypto.PasswordParams
	// FlowTTL 注册流的 TTL（来自 `runtime_config.flow.ttl_seconds`；**代码不留 600 常量**）。
	FlowTTL time.Duration
	// RegisterTokenKey 注册窗口令牌的 HMAC 键（`guanjin/hmac/register`，D14②）。
	//
	// **独立用途域**：不复用盲索引键（D4-B 一密钥一用途），否则"换盲索引密钥要不要作废
	// 在途令牌"这件事就永远说不清。
	RegisterTokenKey crypto.HMACKey
	// ChallengeTTL / ChallengeMaxAttempts 一次性挑战的有效期与次数上限
	// （`runtime_config` 的 `challenge.ttl_seconds` / `challenge.max_attempts`，D14①）。
	//
	// 两者都是**必填**：代码里不留 300 / 5，读不到就是配置缺陷（AC-REG-01 要求"改键值必须跟着变"）。
	ChallengeTTL         time.Duration
	ChallengeMaxAttempts int
	// GenerateCode 生成一次性验证码（默认 6 位数字；测试可注入以拿到明文码）。
	GenerateCode func() (string, error)
	// Now 可注入时钟（测试用）；nil → time.Now。
	Now func() time.Time
	// Random 可注入随机源（口令盐/TOTP 密钥）；nil → crypto/rand。
	Random io.Reader
}

// Service 是账号域服务。
type Service struct {
	db             *gorm.DB
	users          *store.UserRepo
	identifiers    *store.IdentifierRepo
	authenticators *store.AuthenticatorRepo
	flows          *store.FlowRepo
	policies       *store.ClientPolicyRepo
	challenges     *store.ChallengeRepo
	events         *store.AuthEventRepo
	cfg            Config
}

// New 构造服务；任一依赖/配置不合规即报错（装配期 fail-fast，运行期不留半残对象）。
func New(db *gorm.DB, cfg Config) (*Service, error) {
	if db == nil {
		return nil, ErrNilDB
	}
	if cfg.IndexKey.IsZero() || cfg.FieldKey.IsZero() {
		return nil, ErrMissingKey
	}
	if err := cfg.PasswordParams.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWeakParams, err)
	}
	if cfg.FlowTTL <= 0 {
		return nil, ErrInvalidTTL
	}
	if cfg.RegisterTokenKey.IsZero() {
		// 没有签名键就没有注册窗口令牌：宁可不启动，也不发明"不签名的令牌"。
		return nil, fmt.Errorf("%w: register token key", ErrMissingKey)
	}
	if cfg.ChallengeTTL <= 0 || cfg.ChallengeMaxAttempts <= 0 {
		return nil, fmt.Errorf("%w: challenge ttl=%s max_attempts=%d",
			ErrInvalidTTL, cfg.ChallengeTTL, cfg.ChallengeMaxAttempts)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	return &Service{
		db:             db,
		users:          store.NewUserRepo(db),
		identifiers:    store.NewIdentifierRepo(db),
		authenticators: store.NewAuthenticatorRepo(db),
		flows:          store.NewFlowRepo(db),
		policies:       store.NewClientPolicyRepo(db),
		challenges:     store.NewChallengeRepo(db),
		events:         store.NewAuthEventRepo(db),
		cfg:            cfg,
	}, nil
}

// RegisterRequest 是注册入参。
//
// `Verified` / `VerifyMethod` / `TrustLevel` 是**事实输入**：验证码挑战的状态机在
// `challenges`（S5），本服务只接收"这个标识符已经被验证过"这一结论——与 I-F8
// "锁外算完再把事实交进来"是同一条纪律。
type RegisterRequest struct {
	// ClientID 必填（索引 §2：注册必带业务系统上下文）。
	ClientID string
	// Type 标识符类型（phone / email / username）。
	Type string
	// Value 原始标识符值（明文，本服务负责规范化+盲索引）。
	Value string
	// Password 口令明文（M1 注册必须有口令）。
	Password string
	// Verified 该标识符是否已通过验证（验证码校验在调用方完成）。
	Verified bool
	// VerifyMethod 验证方式（sms_otp / email_otp / admin …）。
	VerifyMethod string
	// TrustLevel 标识符信任等级（0..3；已验证的信道型应为 2）。
	TrustLevel int16
	// TraceID 贯穿日志与 auth_events（D4-C(4)）。
	TraceID string
}

// RegisterResult 是注册成功的**事实集合**（不含令牌：令牌由 S6 签发）。
type RegisterResult struct {
	UserID           uuid.UUID
	IdentifierID     uuid.UUID // username 型注册为空（V1 不为 username 建 identifiers 行，索引 §4.2-1）
	AuthenticatorID  uuid.UUID
	FlowID           uuid.UUID
	TargetACR        string
	IdentifierMasked string
	CreatedAt        time.Time
}

// Register 完成一次自注册（注册即"账号可登录"，但不发令牌）。
//
// 事务内顺序（顺序本身有含义）：
//  1. 读目标业务系统的注册政策（**必带 client_id**，不存在/停用 → INVALID_CLIENT）；
//  2. 自助注册开关（关闭 → REGISTRATION_NOT_ALLOWED 403）；
//  3. 口令策略（按目标档位；三码顺序由 `credential.Policy` 保证）；
//  4. 规范化 + 盲索引（格式不合法在落库前就拒）；
//  5. 落 users → identifiers → authenticators；
//  6. 落一条 **completed + consumed** 的 flows 行（AC-REG-06 / I-F4）；
//  7. 追加 `auth_events`（result=success, event_type=register）。
func (s *Service) Register(ctx context.Context, req RegisterRequest) (*RegisterResult, error) {
	if req.ClientID == "" {
		return nil, ErrMissingClientID
	}
	if req.Password == "" {
		return nil, ErrPasswordRequired
	}
	now := s.cfg.Now().UTC()

	var out *RegisterResult
	err := store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		policy, err := s.policies.LoadRegistrationPolicy(ctx, tx, req.ClientID)
		if err != nil {
			return err
		}
		if !policy.SelfRegistrationAllowed {
			return ErrRegistrationNotAllowed
		}

		// 口令策略：按**目标业务系统的档位**算（接口 §3.3）。
		pwdPolicy := credential.Policy{
			MinLength:    policy.PasswordMinLength,
			CharClasses:  policy.PasswordCharClasses,
			HistoryCount: 0, // 新账号没有历史
			MaxAgeDays:   policy.PasswordMaxAgeDays,
		}
		res, err := pwdPolicy.Evaluate(credential.EvaluateInput{Password: req.Password})
		if err != nil {
			return err
		}
		if !res.OK() {
			return &PasswordRejected{Result: res}
		}

		encoded, err := identifier.Encode(req.Type, req.Value, s.cfg.IndexKey, s.cfg.FieldKey)
		if err != nil {
			return err
		}

		user, err := s.createUser(ctx, tx, req, encoded)
		if err != nil {
			return err
		}

		identifierID, err := s.createIdentifier(ctx, tx, user.ID, req, encoded)
		if err != nil {
			return err
		}

		authID, err := s.createPasswordAuthenticator(ctx, tx, user.ID, req.Password, now)
		if err != nil {
			return err
		}

		flowRow, err := s.createCompletedFlow(ctx, tx, user.ID, req, policy.Tier, now)
		if err != nil {
			return err
		}

		event := &model.AuthEvent{
			UserID:     &user.ID,
			ClientID:   &req.ClientID,
			EventType:  "register",
			FactorType: strPtr(string(factor.FactorPassword)),
			Result:     "success",
			ACR:        strPtr(flowRow.TargetACR),
			AMR:        model.StringArray{amrPassword()},
			OccurredAt: now,
		}
		if req.TraceID != "" {
			event.TraceID = &req.TraceID
		}
		if identifierID != uuid.Nil {
			event.IdentifierID = &identifierID
		}
		if err := s.events.WithTx(tx).Append(ctx, event); err != nil {
			return err
		}

		out = &RegisterResult{
			UserID:           user.ID,
			IdentifierID:     identifierID,
			AuthenticatorID:  authID,
			FlowID:           flowRow.ID,
			TargetACR:        flowRow.TargetACR,
			IdentifierMasked: encoded.Masked,
			CreatedAt:        now,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// createUser 建身份主体。
//
// `users.username` 的取值口径（**显式约定**，避免隐性推断）：登录名 = 规范化后的标识符值
// （username 型就是它本身；phone/email 型也用规范化值）。理由：V1 不为 username 另建
// `identifiers` 行（索引 §4.2-1），登录名必须能独立定位一个账号。
func (s *Service) createUser(ctx context.Context, tx *gorm.DB, req RegisterRequest, enc identifier.Encoded) (*model.User, error) {
	u := &model.User{Username: enc.Normalized, Status: "active"}
	if err := s.users.WithTx(tx).Create(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// createIdentifier 建标识符行；`username` 型**不建行**（索引 §4.2-1）。
//
// 首条标识符自动成为主标识符（每用户至多一个，DB 有 partial unique）。
func (s *Service) createIdentifier(ctx context.Context, tx *gorm.DB, userID uuid.UUID, req RegisterRequest, enc identifier.Encoded) (uuid.UUID, error) {
	if enc.Type == identifier.TypeUsername {
		return uuid.Nil, nil
	}
	row := &model.Identifier{
		UserID:      userID,
		Type:        enc.Type,
		Value:       enc.BlindIndex,
		ValueEnc:    enc.Cipher,
		ValueMasked: enc.Masked,
		Verified:    req.Verified,
		TrustLevel:  req.TrustLevel,
		IsPrimary:   true,
		Status:      "active",
	}
	if req.Verified {
		now := s.cfg.Now().UTC()
		row.VerifiedAt = &now
		if req.VerifyMethod != "" {
			row.VerifyMethod = &req.VerifyMethod
		}
	}
	if err := s.identifiers.WithTx(tx).Create(ctx, row); err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}

// createPasswordAuthenticator 建口令凭据。
//
// 走 `createAuthenticator` 的统一入口，因此**信道型因子在这里会被拒绝**
// （`factor.KindChannel` → `ErrChannelHasNoAuthenticator`）；将来加 TOTP/WebAuthn
// 只需换成 `factor.KindHolding` + 对应信封，不需要动这条守卫。
func (s *Service) createPasswordAuthenticator(ctx context.Context, tx *gorm.DB, userID uuid.UUID, password string, now time.Time) (uuid.UUID, error) {
	envelope, err := credential.NewPasswordEnvelope(password, s.cfg.PasswordParams, now, s.cfg.Random)
	if err != nil {
		return uuid.Nil, err
	}
	raw, err := envelope.Marshal()
	if err != nil {
		return uuid.Nil, err
	}
	return s.createAuthenticator(ctx, tx, userID, string(factor.FactorPassword), factor.KindHolding, model.JSONValue(raw))
}

// createAuthenticator 是 `authenticators` 行的**唯一创建入口**（含"信道型不建行"守卫）。
func (s *Service) createAuthenticator(ctx context.Context, tx *gorm.DB, userID uuid.UUID, atype string, kind factor.Kind, data model.JSONValue) (uuid.UUID, error) {
	if kind != factor.KindHolding {
		return uuid.Nil, fmt.Errorf("%w: type=%s kind=%s", ErrChannelHasNoAuthenticator, atype, kind)
	}
	if len(data) == 0 {
		return uuid.Nil, errors.New("identity: empty credential payload")
	}
	row := &model.Authenticator{
		UserID:         userID,
		Type:           atype,
		CredentialData: data,
		Status:         "active",
	}
	if err := s.authenticators.WithTx(tx).Create(ctx, row); err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}

// createCompletedFlow 落一条"注册即完成"的流。
//
// 为什么注册也要有 flows 行：AC-REG-06 要求 `status='completed'` 且 `consumed_at` 非空
// （I-F4），这样"注册走的是同一条阶梯链路"在数据上成立，而不是一条绕过编排的旁路。
// 会话 id 留空：注册即登录的会话由 S6/S7 建，之后按 I-F7 一次性回填。
func (s *Service) createCompletedFlow(ctx context.Context, tx *gorm.DB, userID uuid.UUID, req RegisterRequest, tier string, now time.Time) (*model.Flow, error) {
	acr, err := targetACRForTier(tier)
	if err != nil {
		return nil, err
	}
	row := &model.Flow{
		UserID:      &userID,
		ClientID:    req.ClientID,
		TargetACR:   acr,
		AchievedAMR: model.StringArray{amrPassword()},
		CompletedSteps: model.StepList{{
			Factor: string(factor.FactorPassword),
			At:     now,
		}},
		Status:     string(flow.StatusCompleted),
		ExpiresAt:  now.Add(s.cfg.FlowTTL),
		ConsumedAt: &now,
	}
	if req.TraceID != "" {
		row.TraceID = &req.TraceID
	}
	if err := s.flows.WithTx(tx).Create(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// targetACRForTier 把档位映射成目标级别（索引 §4.2-15 / D12：L1|L2 → AAL1，L3 → AAL2）。
//
// 映射**只此一处**；`AAL3` 永不产出（D12），所以这里没有对应分支。
func targetACRForTier(tier string) (string, error) {
	switch tier {
	case "L1", "L2":
		return string(factor.ACRAAL1), nil
	case "L3":
		return string(factor.ACRAAL2), nil
	default:
		return "", fmt.Errorf("identity: unknown client tier %q", tier)
	}
}

func amrPassword() string {
	amr, ok := factor.AMRFor(factor.FactorPassword)
	if !ok {
		// 冻结表里没有 password 是不可能的；真发生就是表被改坏了。
		panic("identity: AMR table lost the password entry")
	}
	return amr
}

func strPtr(s string) *string { return &s }

// CodeOf 把本包（及其依赖）的错误映射成对外码（接口 §5.4）。**单一出口**。
func CodeOf(err error) (httpx.Code, bool) {
	var rejected *PasswordRejected
	switch {
	case err == nil:
		return "", false
	case errors.As(err, &rejected):
		return rejected.Result.Code, true
	case errors.Is(err, ErrMissingClientID):
		return httpx.CodeMissingParameter, true
	case errors.Is(err, ErrRegistrationNotAllowed):
		return httpx.CodeRegistrationNotAllowed, true
	case errors.Is(err, ErrInviteCodeInvalid):
		return httpx.CodeInviteCodeInvalid, true
	case errors.Is(err, ErrIdentifierAlreadyBound):
		return httpx.CodeIdentifierAlreadyBound, true
	case errors.Is(err, ErrInvalidRegisterToken):
		return httpx.CodeInvalidParam, true
	case errors.Is(err, ErrRegisterTokenExpired):
		return httpx.CodeChallengeExpired, true
	case errors.Is(err, ErrRegisterChallengeConsumed):
		return httpx.CodeChallengeConsumed, true
	case errors.Is(err, ErrInvalidRegisterCode):
		return httpx.CodeInvalidCredential, true
	case errors.Is(err, ErrRegisterAttemptsExceeded):
		return httpx.CodeAttemptsExceeded, true
	case errors.Is(err, ErrPasswordRequired):
		return httpx.CodeWeakPassword, true
	case errors.Is(err, ErrIdentifierNotFound), errors.Is(err, ErrAuthenticatorNotFound):
		return httpx.CodeIdentifierNotFound, true
	case errors.Is(err, identifier.ErrInvalidIdentifier), errors.Is(err, identifier.ErrUnsupportedType),
		errors.Is(err, identifier.ErrEmptyValue):
		return httpx.CodeIdentifierInvalidFormat, true
	case errors.Is(err, store.ErrClientNotFound), errors.Is(err, store.ErrClientDisabled):
		return httpx.CodeInvalidClient, true
	case errors.Is(err, store.ErrUserNotFound):
		return httpx.CodeUserNotFound, true
	case errors.Is(err, store.ErrIdentifierNotFound):
		return httpx.CodeIdentifierNotFound, true
	}
	if code, ok := identifier.CodeOf(err); ok {
		return code, true
	}
	if code, ok := credential.CodeOf(err); ok {
		return code, true
	}
	// DB 业务码（23505/23514/23503 的映射结果）：取出它自带的码，不再自己猜。
	var dbErr *store.Error
	if errors.As(err, &dbErr) && dbErr.Mapped() {
		return dbErr.Code, true
	}
	return "", false
}
