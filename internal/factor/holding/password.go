package holding

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/credential"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	PasswordType factor.FactorType = "password"

	passwordChallengeTTL = 10 * time.Minute
	passwordMaxAttempts  = 5
)

type PasswordProvider struct {
	*Base
	challenges *store.ChallengeRepo
}

func PasswordDescriptor() factor.Descriptor {
	return factor.Descriptor{
		Type: PasswordType,
		Kind: factor.KindHolding,
		Capabilities: []string{
			factor.CapEnrollable,
			factor.CapAuthenticatable,
			factor.CapACRContributing,
			factor.CapResettable,
			factor.CapRegistrable,
			factor.CapRevocable,
			factor.CapRateLimited,
			factor.CapRiskReporting,
		},
		Authenticatable: &factor.AuthenticatableSpec{AsFirst: true, AsSecond: false},
		ACR:             &factor.ACRSpec{Sole: factor.ACRAAL1, Combined: factor.ACRAAL2},
		Registrable:     &factor.RegistrableSpec{VerifyRequired: false},
		Revocable: &factor.RevocableSpec{
			OnIdentifierUnbind: factor.OnUnbindNone,
			RequireACR:         factor.ACRAAL2,
			CooldownHours:      24,
		},
		RateLimit: &factor.RateLimitSpec{
			Send:        "",
			Verify:      "5/15min",
			DailySend:   0,
			BudgetAlert: false,
		},
		RiskSignals: []string{"fail_burst", "geo_jump", "device_change"},
	}
}

func NewPasswordProvider(auths *store.AuthenticatorRepo, challenges *store.ChallengeRepo, failThreshold int32, lockDuration time.Duration) *PasswordProvider {
	return &PasswordProvider{
		Base:       NewBase(PasswordType, PasswordDescriptor(), auths, failThreshold, lockDuration),
		challenges: challenges,
	}
}

func (p *PasswordProvider) AMR() string { return "pwd" }

func (p *PasswordProvider) NextAction() factor.NextAction { return factor.NextActionInputPassword }

func (p *PasswordProvider) EnrollmentPolicy() factor.EnrollmentPolicy {
	return factor.EnrollmentPolicy{
		RequiresPreBoundIdentifier: false,
		SelfServiceAllowed:         true,
		VerifyRequired:             false,
	}
}

func (p *PasswordProvider) RegistrableVerifyRequired() bool { return false }

func (p *PasswordProvider) RequiredProofForReset() factor.ProofRequirement {
	return factor.ProofRequirement{
		MinACR:      factor.ACRAAL2,
		MinTrust:    2,
		Description: "需其他因子证明身份（改密或找回）",
	}
}

func (p *PasswordProvider) Reset(ctx context.Context, userID, authenticatorID, proof string) error {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return factor.ErrInvalidCredential
	}
	aid, err := uuid.Parse(authenticatorID)
	if err != nil {
		return factor.ErrInvalidCredential
	}
	auth, err := p.Authenticators().GetByID(ctx, aid)
	if err != nil {
		return factor.ErrInvalidCredential
	}
	if auth.UserID != uid || auth.Type != string(PasswordType) {
		return factor.ErrInvalidCredential
	}
	encoded, err := credential.Hash(proof, credential.DefaultPolicy())
	if err != nil {
		return err
	}
	auth.CredentialData = encoded
	return p.Authenticators().Update(ctx, auth)
}

func (p *PasswordProvider) RevocationPolicy() factor.RevocationPolicy {
	spec := PasswordDescriptor().Revocable
	return factor.RevocationPolicy{
		OnIdentifierUnbind: spec.OnIdentifierUnbind,
		RequireACR:         spec.RequireACR,
		CooldownHours:      spec.CooldownHours,
	}
}

func (p *PasswordProvider) Limits() factor.RateLimits {
	spec := PasswordDescriptor().RateLimit
	return factor.RateLimits{
		Send:        spec.Send,
		Verify:      spec.Verify,
		DailySend:   spec.DailySend,
		BudgetAlert: spec.BudgetAlert,
	}
}

func (p *PasswordProvider) RiskSignals() []string {
	return append([]string{}, PasswordDescriptor().RiskSignals...)
}

func (p *PasswordProvider) BeginChallenge(ctx context.Context, userID string, purpose factor.Purpose) (factor.Ticket, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return factor.Ticket{}, factor.ErrInvalidCredential
	}
	expires := time.Now().UTC().Add(passwordChallengeTTL)
	challenge := &model.Challenge{
		UserID:       &uid,
		Purpose:      string(purpose),
		FactorType:   string(PasswordType),
		ResponseHash: []byte{},
		MaxAttempts:  passwordMaxAttempts,
		ExpiresAt:    expires,
	}
	if err := p.challenges.Create(ctx, challenge); err != nil {
		return factor.Ticket{}, err
	}
	return factor.Ticket{
		ChallengeID:  challenge.ID.String(),
		FactorType:   PasswordType,
		Purpose:      purpose,
		NextAction:   factor.NextActionInputPassword,
		TTL:          passwordChallengeTTL,
		AttemptsLeft: passwordMaxAttempts,
		ExpiresAt:    expires,
	}, nil
}

func (p *PasswordProvider) VerifyResponse(ctx context.Context, ticket factor.Ticket, response string) error {
	if ticket.ChallengeID == "" {
		return factor.ErrChallengeInvalid
	}
	cid, err := uuid.Parse(ticket.ChallengeID)
	if err != nil {
		return factor.ErrChallengeInvalid
	}
	challenge, err := p.challenges.GetByID(ctx, cid)
	if err != nil {
		return factor.ErrChallengeInvalid
	}
	if challenge.FactorType != string(PasswordType) {
		return factor.ErrChallengeInvalid
	}
	if challenge.ConsumedAt != nil {
		return factor.ErrChallengeConsumed
	}
	if time.Now().UTC().After(challenge.ExpiresAt) {
		return factor.ErrChallengeExpired
	}
	if challenge.UserID == nil {
		return factor.ErrChallengeInvalid
	}
	auth, err := p.ActiveAuthenticator(ctx, *challenge.UserID)
	if err != nil {
		return factor.ErrInvalidCredential
	}
	if p.IsLocked(auth) {
		return factor.ErrAccountLocked
	}
	ok, verifyErr := credential.Verify(auth.CredentialData, []byte(response))
	if verifyErr != nil || !ok {
		_ = p.challenges.IncrementAttempts(ctx, cid)
		_ = p.RecordFailure(ctx, auth)
		if challenge.Attempts+1 >= challenge.MaxAttempts {
			return factor.ErrAttemptsExceeded
		}
		return factor.ErrInvalidCredential
	}
	consumed, err := p.challenges.Consume(ctx, cid)
	if err != nil {
		return err
	}
	if !consumed {
		return factor.ErrChallengeConsumed
	}
	return p.RecordSuccess(ctx, auth)
}
