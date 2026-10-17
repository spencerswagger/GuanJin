package channel

import (
	"time"

	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/identifier"
)

const EmailOtpType factor.FactorType = "email_otp"

func EmailOtpDescriptor() factor.Descriptor {
	return factor.Descriptor{
		Type: EmailOtpType,
		Kind: factor.KindChannel,
		Capabilities: []string{
			factor.CapEnrollable,
			factor.CapIdentifierVerifying,
			factor.CapAuthenticatable,
			factor.CapACRContributing,
			factor.CapRecoverable,
			factor.CapRegistrable,
			factor.CapRevocable,
			factor.CapRateLimited,
			factor.CapRiskReporting,
		},
		Authenticatable:     &factor.AuthenticatableSpec{AsFirst: true, AsSecond: true},
		ACR:                 &factor.ACRSpec{Sole: factor.ACRAAL1, Combined: factor.ACRAAL2},
		IdentifierVerifying: &factor.IdentifierVerifyingSpec{Targets: []string{identifier.TypeEmail}},
		Recoverable:         &factor.RecoverableSpec{TrustLevel: 2},
		Registrable:         &factor.RegistrableSpec{VerifyRequired: true},
		Revocable: &factor.RevocableSpec{
			OnIdentifierUnbind: factor.OnUnbindImmediate,
			RequireACR:         factor.ACRAAL2,
			CooldownHours:      24,
		},
		RateLimit: &factor.RateLimitSpec{
			Send:        "1/60s",
			Verify:      "5/10min",
			DailySend:   20,
			BudgetAlert: false,
		},
		RiskSignals: []string{"send_frequency", "geo_jump", "device_change"},
	}
}

type EmailOtpProvider struct {
	*ChannelOtpProvider
}

func NewEmailOtpProvider(deps Deps) *EmailOtpProvider {
	return &EmailOtpProvider{NewChannelOtpProvider(EmailOtpType, EmailOtpDescriptor(), "email", identifier.TypeEmail, "email_otp", Config{
		CodeLength:  6,
		TTL:         5 * time.Minute,
		MaxAttempts: 5,
		DailySend:   20,
		SendWindow:  60 * time.Second,
		SendLimit:   1,
	}, deps)}
}
