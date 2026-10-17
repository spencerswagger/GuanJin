package channel

import (
	"time"

	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/identifier"
)

const SmsOtpType factor.FactorType = "sms_otp"

func SmsOtpDescriptor() factor.Descriptor {
	return factor.Descriptor{
		Type: SmsOtpType,
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
		IdentifierVerifying: &factor.IdentifierVerifyingSpec{Targets: []string{identifier.TypePhone}},
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
			DailySend:   10,
			BudgetAlert: true,
		},
		RiskSignals: []string{"send_frequency", "geo_jump", "device_change"},
	}
}

type SmsOtpProvider struct {
	*ChannelOtpProvider
}

func NewSmsOtpProvider(deps Deps) *SmsOtpProvider {
	return &SmsOtpProvider{NewChannelOtpProvider(SmsOtpType, SmsOtpDescriptor(), "sms", identifier.TypePhone, "sms_otp", Config{
		CodeLength:  6,
		TTL:         5 * time.Minute,
		MaxAttempts: 5,
		DailySend:   10,
		SendWindow:  60 * time.Second,
		SendLimit:   1,
	}, deps)}
}
