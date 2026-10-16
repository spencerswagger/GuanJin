package channel

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type Sender interface {
	Send(ctx context.Context, target, code string) error
}

type TargetResolver interface {
	ResolveTarget(ctx context.Context, userID string) (target string, masked string, err error)
}

type Config struct {
	Type        factor.FactorType
	Descriptor  factor.Descriptor
	Channel     string
	CodeLength  int
	TTL         time.Duration
	MaxAttempts int32
	DailySend   int
	SendWindow  time.Duration
	SendLimit   int
}

type Base struct {
	cfg        Config
	challenges *store.ChallengeRepo
	rdb        *store.Redis
	sender     Sender
	resolver   TargetResolver
}

func NewBase(cfg Config, challenges *store.ChallengeRepo, rdb *store.Redis, sender Sender, resolver TargetResolver) *Base {
	if cfg.CodeLength <= 0 {
		cfg.CodeLength = 6
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 5 * time.Minute
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.SendWindow <= 0 {
		cfg.SendWindow = 60 * time.Second
	}
	if cfg.SendLimit <= 0 {
		cfg.SendLimit = 1
	}
	return &Base{cfg: cfg, challenges: challenges, rdb: rdb, sender: sender, resolver: resolver}
}

func (b *Base) Type() factor.FactorType { return b.cfg.Type }

func (b *Base) Kind() factor.Kind { return factor.KindChannel }

func (b *Base) Descriptor() factor.Descriptor { return b.cfg.Descriptor }

func (b *Base) NextAction() factor.NextAction { return factor.NextActionTypeCode }

func (b *Base) BeginChallenge(ctx context.Context, userID string, purpose factor.Purpose) (factor.Ticket, error) {
	if b.resolver == nil {
		return factor.Ticket{}, factor.ErrNoEligibleFactor
	}
	target, masked, err := b.resolver.ResolveTarget(ctx, userID)
	if err != nil {
		return factor.Ticket{}, err
	}
	allowed, err := b.allowSend(ctx, b.rateSendKey(target))
	if err != nil {
		return factor.Ticket{}, err
	}
	if !allowed {
		return factor.Ticket{}, errors.New("RATE_LIMITED")
	}
	if b.cfg.DailySend > 0 {
		dailyOK, dailyErr := b.allowDaily(ctx, b.rateDailyKey(target))
		if dailyErr != nil {
			return factor.Ticket{}, dailyErr
		}
		if !dailyOK {
			return factor.Ticket{}, errors.New("BUDGET_EXCEEDED")
		}
	}
	code, err := randomCode(b.cfg.CodeLength)
	if err != nil {
		return factor.Ticket{}, err
	}
	uid, err := uuid.Parse(userID)
	if err != nil {
		return factor.Ticket{}, factor.ErrNoEligibleFactor
	}
	channel := b.cfg.Channel
	expires := time.Now().UTC().Add(b.cfg.TTL)
	challenge := &model.Challenge{
		UserID:       &uid,
		Purpose:      string(purpose),
		FactorType:   string(b.cfg.Type),
		Channel:      &channel,
		TargetMasked: &masked,
		ResponseHash: crypto.Sum([]byte(code)),
		MaxAttempts:  b.cfg.MaxAttempts,
		ExpiresAt:    expires,
	}
	if err := b.challenges.Create(ctx, challenge); err != nil {
		return factor.Ticket{}, err
	}
	if err := b.sender.Send(ctx, target, code); err != nil {
		return factor.Ticket{}, err
	}
	return factor.Ticket{
		ChallengeID:  challenge.ID.String(),
		FactorType:   b.cfg.Type,
		Purpose:      purpose,
		TargetMasked: masked,
		NextAction:   factor.NextActionTypeCode,
		TTL:          b.cfg.TTL,
		AttemptsLeft: int(b.cfg.MaxAttempts),
		ExpiresAt:    expires,
	}, nil
}

func (b *Base) VerifyResponse(ctx context.Context, ticket factor.Ticket, response string) error {
	if ticket.ChallengeID == "" {
		return factor.ErrChallengeInvalid
	}
	cid, err := uuid.Parse(ticket.ChallengeID)
	if err != nil {
		return factor.ErrChallengeInvalid
	}
	challenge, err := b.challenges.GetByID(ctx, cid)
	if err != nil {
		return factor.ErrChallengeInvalid
	}
	if challenge.FactorType != string(b.cfg.Type) {
		return factor.ErrChallengeInvalid
	}
	if challenge.ConsumedAt != nil {
		return factor.ErrChallengeConsumed
	}
	if time.Now().UTC().After(challenge.ExpiresAt) {
		return factor.ErrChallengeExpired
	}
	candidate := crypto.Sum([]byte(response))
	if len(candidate) != len(challenge.ResponseHash) || subtle.ConstantTimeCompare(candidate, challenge.ResponseHash) != 1 {
		_ = b.challenges.IncrementAttempts(ctx, cid)
		if challenge.Attempts+1 >= challenge.MaxAttempts {
			return factor.ErrAttemptsExceeded
		}
		return factor.ErrInvalidCredential
	}
	consumed, err := b.challenges.Consume(ctx, cid)
	if err != nil {
		return err
	}
	if !consumed {
		return factor.ErrChallengeConsumed
	}
	return nil
}

func (b *Base) rateSendKey(target string) string {
	return fmt.Sprintf("guanjin:ratelimit:otp:send:%s:%s", b.cfg.Type, target)
}

func (b *Base) rateDailyKey(target string) string {
	return fmt.Sprintf("guanjin:ratelimit:otp:daily:%s:%s", b.cfg.Type, target)
}

func (b *Base) allowSend(ctx context.Context, key string) (bool, error) {
	return b.incrWithin(ctx, key, b.cfg.SendWindow, b.cfg.SendLimit)
}

func (b *Base) allowDaily(ctx context.Context, key string) (bool, error) {
	return b.incrWithin(ctx, key, 24*time.Hour, b.cfg.DailySend)
}

func (b *Base) incrWithin(ctx context.Context, key string, window time.Duration, limit int) (bool, error) {
	client := b.rdb.Client()
	if client == nil {
		return false, errors.New("REDIS_UNAVAILABLE")
	}
	count, err := client.Incr(ctx, key).Result()
	if err != nil {
		return false, errors.New("REDIS_UNAVAILABLE")
	}
	if count == 1 {
		if err := client.Expire(ctx, key, window).Err(); err != nil {
			return false, errors.New("REDIS_UNAVAILABLE")
		}
	}
	return count <= int64(limit), nil
}

func randomCode(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, length)
	for i, b := range buf {
		out[i] = '0' + b%10
	}
	return string(out), nil
}
