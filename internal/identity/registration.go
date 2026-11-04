package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/identifier"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是**注册面**（`POST /auth/register/start` 与 `/auth/register/complete`）的服务层。
//
// 边界（接口 §3.3 + D14）：
//   - 两步之间**没有任何服务端会话**：上下文全在 `register_token` 里（自包含 + 签名），
//     所以多副本不需要粘滞，也不需要新加 Redis 键；
//   - **挑战行是权威**：`purpose=register` 的 `challenges` 行负责 TTL / 一次性消费 / 尝试次数，
//     令牌只负责承载"给谁建什么号"；
//   - 用户名型（仅"用户名 + 密码"，AC-REG-05）**可以没有挑战**：此时 `register_token` 不带
//     `chid`，`complete` 按令牌里的 `client_id` 重新做一次准入 + 唯一性校验（TOCTOU）；
//   - 邀请码：M1 没有邀请码存储，**任何非空邀请码都是无效的**（显式拒绝，AC-REG-08 的无效分支）。

var (
	// ErrInviteCodeInvalid 表示邀请码无效（对外 `INVITE_CODE_INVALID`，400）。
	ErrInviteCodeInvalid = errors.New("identity: invite code is invalid")
	// ErrIdentifierAlreadyBound 表示该标识符已被占用（对外 `IDENTIFIER_ALREADY_BOUND`，409）。
	ErrIdentifierAlreadyBound = errors.New("identity: identifier already bound")
	// ErrRegisterChallengeConsumed 表示该注册挑战已被消费（对外 `CHALLENGE_CONSUMED`，401）。
	ErrRegisterChallengeConsumed = errors.New("identity: register challenge already consumed")
	// ErrInvalidRegisterCode 表示验证码不对（对外 `INVALID_CREDENTIAL`，401）。
	ErrInvalidRegisterCode = errors.New("identity: invalid registration code")
	// ErrRegisterAttemptsExceeded 表示该挑战的尝试次数用尽（对外 `ATTEMPTS_EXCEEDED`，401）。
	ErrRegisterAttemptsExceeded = errors.New("identity: register challenge attempts exceeded")
)

// StartRegistrationRequest 是 `POST /auth/register/start` 的入参（接口 §3.3.1）。
type StartRegistrationRequest struct {
	// ClientID 必填（索引 §2：注册一定带业务系统上下文）。
	ClientID string
	// Type 标识符类型（phone / email / username）。
	Type string
	// Value 原始标识符值。
	Value string
	// InviteCode 邀请码（M1 无合法来源，非空即无效）。
	InviteCode string
	// TraceID 贯穿日志与 auth_events。
	TraceID string
}

// StartRegistrationResult 是 `register/start` 的产物（接口 §3.3.1 的响应要点）。
type StartRegistrationResult struct {
	// RegisterToken 自包含签名令牌（`v1.<payload>.<mac>`）。
	RegisterToken string
	// ChallengeID 需要验证码时非空（用户名免验证路径为空）。
	ChallengeID *string
	// NextAction `type_code`（等验证码）/ `input_password`（免验证，直接设口令）。
	NextAction factor.NextAction
	// TargetMasked 脱敏后的投递目标。
	TargetMasked string
	// AttemptsLeft 该挑战剩几次（= `challenge.max_attempts`，来自 runtime_config）。
	AttemptsLeft int
	// ExpiresAt 注册窗口的过期时刻（**库时间**算出）。
	ExpiresAt time.Time
}

// StartRegistration 下发注册窗口（必要时落一条 `purpose=register` 的挑战）。
//
// 校验顺序固定（D14③，改顺序等于改用户看到的错误码）：
// `MISSING_PARAMETER` → `INVALID_CLIENT` → `REGISTRATION_NOT_ALLOWED` →
// `INVITE_CODE_INVALID` → `IDENTIFIER_INVALID_FORMAT` → `IDENTIFIER_ALREADY_BOUND`。
func (s *Service) StartRegistration(ctx context.Context, req StartRegistrationRequest) (*StartRegistrationResult, error) {
	if req.ClientID == "" {
		return nil, ErrMissingClientID
	}
	policy, err := s.policies.LoadRegistrationPolicy(ctx, nil, req.ClientID)
	if err != nil {
		return nil, err
	}
	if !policy.SelfRegistrationAllowed {
		return nil, ErrRegistrationNotAllowed
	}
	// 邀请码先于一切写入判定：AC-REG-08 要求"不得先建号后校验"，这里连挑战都不该落。
	if req.InviteCode != "" {
		return nil, ErrInviteCodeInvalid
	}

	encoded, err := identifier.Encode(req.Type, req.Value, s.cfg.IndexKey, s.cfg.FieldKey)
	if err != nil {
		return nil, err
	}

	now, err := store.DBNow(ctx, s.db)
	if err != nil {
		return nil, err
	}
	expiresAt := now.Add(s.cfg.ChallengeTTL)

	// 唯一性：`username` 型不建 `identifiers` 行（索引 §4.2-1），其占用由 `complete` 的
	// 唯一约束兜（`users.username`）；信道型先按盲索引查一次，给出 409 而不是等到建号。
	if encoded.Type != identifier.TypeUsername {
		if _, ferr := s.identifiers.FindByBlindIndex(ctx, encoded.Type, encoded.BlindIndex); ferr == nil {
			return nil, ErrIdentifierAlreadyBound
		} else if !errors.Is(ferr, store.ErrIdentifierNotFound) {
			return nil, ferr
		}
	}

	claims := RegisterClaims{
		V:         1,
		ClientID:  req.ClientID,
		Type:      encoded.Type,
		Value:     encoded.Normalized,
		ExpiresAt: expiresAt.Unix(),
	}
	out := &StartRegistrationResult{
		TargetMasked: encoded.Masked,
		ExpiresAt:    expiresAt,
	}

	if encoded.Type == identifier.TypeUsername {
		// 免验证路径（AC-REG-05）：没有挑战，`next_action` 直接是设口令。
		out.NextAction = factor.NextActionInputPassword
	} else {
		code, err := s.generateCode()
		if err != nil {
			return nil, err
		}
		digest := crypto.SM3Sum([]byte(code))
		row := &model.Challenge{
			ClientID:     &req.ClientID,
			Purpose:      string(factor.PurposeRegister),
			FactorType:   string(verifyFactorFor(encoded.Type)),
			Channel:      channelFor(encoded.Type),
			TargetMasked: &encoded.Masked,
			ResponseHash: digest[:],
			MaxAttempts:  int32(s.cfg.ChallengeMaxAttempts),
			ExpiresAt:    expiresAt,
		}
		row.ID = uuid.Must(uuid.NewV7())
		if err := s.challenges.Create(ctx, row); err != nil {
			return nil, err
		}
		id := row.ID.String()
		claims.ChallengeID = id
		out.ChallengeID = &id
		out.NextAction = factor.NextActionTypeCode
		out.AttemptsLeft = s.cfg.ChallengeMaxAttempts
	}

	token, err := s.sign(claims)
	if err != nil {
		return nil, err
	}
	out.RegisterToken = token
	return out, nil
}

// CompleteRegistrationRequest 是 `POST /auth/register/complete` 的入参（接口 §3.3.2）。
type CompleteRegistrationRequest struct {
	RegisterToken string
	// Code 验证码（有挑战时必填；用户名免验证路径不用）。
	Code string
	// Password 口令明文（策略按目标业务系统档位判定）。
	Password string
	TraceID  string
}

// CompleteRegistration 校验注册窗口并建号（注册即"账号可登录"，令牌由 S6 签发）。
//
// 顺序：令牌（格式/签名 → 过期）→ 挑战（存在 → 用途 → 未消费 → 未过期 → 码）→ 一次性消费 → 建号。
// **先消费再建号**：建号失败（如口令不合规）时挑战已被用掉，用户需要重新 start ——
// 这是有意的：让"同一个窗口反复试口令"不可能，改口令只能重新走注册窗口。
func (s *Service) CompleteRegistration(ctx context.Context, req CompleteRegistrationRequest) (*RegisterResult, error) {
	if req.RegisterToken == "" {
		return nil, ErrInvalidRegisterToken
	}
	claims, err := parseRegisterToken(s.cfg.RegisterTokenKey, req.RegisterToken)
	if err != nil {
		return nil, err
	}
	now, err := store.DBNow(ctx, s.db)
	if err != nil {
		return nil, err
	}
	if claims.expired(now) {
		return nil, ErrRegisterTokenExpired
	}

	verified := false
	verifyMethod := ""
	trustLevel := int16(0)

	if claims.ChallengeID != "" {
		chID, perr := uuid.Parse(claims.ChallengeID)
		if perr != nil {
			return nil, ErrInvalidRegisterToken
		}
		row, cerr := s.challenges.GetByID(ctx, chID)
		if cerr != nil {
			// 挑战不存在（清理器/伪造）：按令牌不合规回，不泄漏"这个 id 曾经存在"。
			return nil, ErrInvalidRegisterToken
		}
		if row.Purpose != string(factor.PurposeRegister) {
			return nil, ErrInvalidRegisterToken
		}
		if row.ConsumedAt != nil {
			return nil, ErrRegisterChallengeConsumed
		}
		if !now.Before(row.ExpiresAt) {
			return nil, ErrRegisterTokenExpired
		}
		if req.Code == "" {
			return nil, ErrInvalidRegisterCode
		}
		digest := crypto.SM3Sum([]byte(req.Code))
		if subtle.ConstantTimeCompare(digest[:], row.ResponseHash) != 1 {
			_, exhausted, aerr := s.challenges.RecordAttempt(ctx, chID, now)
			if aerr != nil {
				return nil, aerr
			}
			if exhausted {
				return nil, ErrRegisterAttemptsExceeded
			}
			return nil, ErrInvalidRegisterCode
		}
		consumed, cerr := s.challenges.Consume(ctx, chID, now)
		if cerr != nil {
			return nil, cerr
		}
		if consumed == 0 {
			// 并发双发：另一个请求先消费了。
			return nil, ErrRegisterChallengeConsumed
		}
		verified = true
		verifyMethod = verifyMethodFor(claims.Type)
		trustLevel = 2
	}

	// 建号：准入、口令策略、唯一性都在 `Register` 的事务里再校一次（TOCTOU）。
	return s.Register(ctx, RegisterRequest{
		ClientID:     claims.ClientID,
		Type:         claims.Type,
		Value:        claims.Value,
		Password:     req.Password,
		Verified:     verified,
		VerifyMethod: verifyMethod,
		TrustLevel:   trustLevel,
		TraceID:      req.TraceID,
	})
}

// verifyFactorFor 给出该标识符类型对应的**验证因子**（写入 `challenges.factor_type`）。
func verifyFactorFor(itype string) factor.FactorType {
	switch itype {
	case identifier.TypePhone:
		return factor.FactorSMSOTP
	case identifier.TypeEmail:
		return factor.FactorEmailOTP
	default:
		// 走到这里说明调用方在没有挑战的路径上调了本函数（用户名型不该有挑战）。
		return factor.FactorEmailOTP
	}
}

// verifyMethodFor 是写进 `identifiers.verify_method` 的方法名（与因子类型同源）。
func verifyMethodFor(itype string) string {
	return string(verifyFactorFor(itype))
}

// channelFor 是 `challenges.channel` 的取值（holding 型为空）。
func channelFor(itype string) *string {
	switch itype {
	case identifier.TypePhone:
		v := "sms"
		return &v
	case identifier.TypeEmail:
		v := "email"
		return &v
	default:
		return nil
	}
}

// generateCode 生成 6 位数字验证码（用注入的随机源，测试可复现）。
func (s *Service) generateCode() (string, error) {
	if s.cfg.GenerateCode != nil {
		return s.cfg.GenerateCode()
	}
	buf := make([]byte, 4)
	if _, err := s.cfg.Random.Read(buf); err != nil {
		return "", fmt.Errorf("identity: generate registration code: %w", err)
	}
	n := (uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])) % 1000000
	return fmt.Sprintf("%06d", n), nil
}
