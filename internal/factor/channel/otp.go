package channel

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type Deps struct {
	Challenges     *store.ChallengeRepo
	Redis          *store.Redis
	ChannelSenders map[string]Sender
	Resolver       TargetResolver
}

func (d Deps) sender(channelName string) Sender {
	if d.ChannelSenders == nil {
		return nil
	}
	return d.ChannelSenders[channelName]
}

type ChannelOtpProvider struct {
	*Base
	channelName    string
	identifierType string
	amr            string
}

func NewChannelOtpProvider(typ factor.FactorType, desc factor.Descriptor, channelName, identifierType, amr string, cfg Config, deps Deps) *ChannelOtpProvider {
	cfg.Type = typ
	cfg.Descriptor = desc
	cfg.Channel = channelName
	base := NewBase(cfg, deps.Challenges, deps.Redis, deps.sender(channelName), deps.Resolver)
	return &ChannelOtpProvider{
		Base:           base,
		channelName:    channelName,
		identifierType: identifierType,
		amr:            amr,
	}
}

func (p *ChannelOtpProvider) AMR() string { return p.amr }

func (p *ChannelOtpProvider) ChannelName() string { return p.channelName }

func (p *ChannelOtpProvider) VerifiesIdentifierType() string { return p.identifierType }

func (p *ChannelOtpProvider) EnrollmentPolicy() factor.EnrollmentPolicy {
	return factor.EnrollmentPolicy{
		RequiresPreBoundIdentifier: true,
		SelfServiceAllowed:         true,
		VerifyRequired:             true,
	}
}

func (p *ChannelOtpProvider) RegistrableVerifyRequired() bool { return true }

func (p *ChannelOtpProvider) RecoveryTrustLevel() int { return 2 }

func (p *ChannelOtpProvider) BeginRecovery(ctx context.Context, userID string, target string) (factor.Ticket, error) {
	return p.BeginChallenge(ctx, userID, factor.PurposeRecover)
}

func (p *ChannelOtpProvider) RevocationPolicy() factor.RevocationPolicy {
	spec := p.Descriptor().Revocable
	if spec == nil {
		return factor.RevocationPolicy{}
	}
	return factor.RevocationPolicy{
		OnIdentifierUnbind: spec.OnIdentifierUnbind,
		RequireACR:         spec.RequireACR,
		CooldownHours:      spec.CooldownHours,
	}
}

func (p *ChannelOtpProvider) Limits() factor.RateLimits {
	spec := p.Descriptor().RateLimit
	if spec == nil {
		return factor.RateLimits{}
	}
	return factor.RateLimits{
		Send:        spec.Send,
		Verify:      spec.Verify,
		DailySend:   spec.DailySend,
		BudgetAlert: spec.BudgetAlert,
	}
}

func (p *ChannelOtpProvider) RiskSignals() []string {
	return append([]string{}, p.Descriptor().RiskSignals...)
}

func (p *ChannelOtpProvider) BeginChallengeForTarget(ctx context.Context, target, masked, clientID string, purpose factor.Purpose) (factor.Ticket, error) {
	if p.sender == nil {
		return factor.Ticket{}, factor.ErrFactorNotSupported
	}
	allowed, err := p.allowSend(ctx, p.rateSendKey(target))
	if err != nil {
		return factor.Ticket{}, err
	}
	if !allowed {
		return factor.Ticket{}, errors.New("RATE_LIMITED")
	}
	if p.cfg.DailySend > 0 {
		dailyOK, dailyErr := p.allowDaily(ctx, p.rateDailyKey(target))
		if dailyErr != nil {
			return factor.Ticket{}, dailyErr
		}
		if !dailyOK {
			return factor.Ticket{}, errors.New("BUDGET_EXCEEDED")
		}
	}
	code, err := randomCode(p.cfg.CodeLength)
	if err != nil {
		return factor.Ticket{}, err
	}
	channelName := p.cfg.Channel
	expires := time.Now().UTC().Add(p.cfg.TTL)
	challenge := &model.Challenge{
		Purpose:      string(purpose),
		FactorType:   string(p.cfg.Type),
		Channel:      &channelName,
		TargetMasked: &masked,
		ResponseHash: crypto.Sum([]byte(code)),
		MaxAttempts:  p.cfg.MaxAttempts,
		ExpiresAt:    expires,
	}
	if clientID != "" {
		cc := clientID
		challenge.ClientID = &cc
	}
	if err := p.challenges.Create(ctx, challenge); err != nil {
		return factor.Ticket{}, err
	}
	if err := p.sender.Send(ctx, target, code); err != nil {
		return factor.Ticket{}, err
	}
	return factor.Ticket{
		ChallengeID:  challenge.ID.String(),
		FactorType:   p.cfg.Type,
		Purpose:      purpose,
		TargetMasked: masked,
		NextAction:   factor.NextActionTypeCode,
		TTL:          p.cfg.TTL,
		AttemptsLeft: int(p.cfg.MaxAttempts),
		ExpiresAt:    expires,
	}, nil
}

func (p *ChannelOtpProvider) VerifyOwnership(ctx context.Context, userID, identifierID, code string) error {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return factor.ErrInvalidCredential
	}
	iid, err := uuid.Parse(identifierID)
	if err != nil {
		return factor.ErrInvalidCredential
	}
	challenge, err := p.challenges.LatestUnconsumedForIdentifier(ctx, iid)
	if err != nil {
		return factor.ErrChallengeInvalid
	}
	if challenge.UserID == nil || *challenge.UserID != uid {
		return factor.ErrChallengeInvalid
	}
	ticket := factor.Ticket{ChallengeID: challenge.ID.String(), FactorType: p.cfg.Type}
	return p.VerifyResponse(ctx, ticket, code)
}
