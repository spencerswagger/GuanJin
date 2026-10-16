package holding

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/spencerswagger/GuanJin/internal/factor"
)

func TestPasswordDescriptorDeclarations(t *testing.T) {
	d := PasswordDescriptor()
	require.Equal(t, PasswordType, d.Type)
	require.Equal(t, factor.KindHolding, d.Kind)

	require.True(t, d.Has(factor.CapEnrollable))
	require.True(t, d.Has(factor.CapAuthenticatable))
	require.True(t, d.Has(factor.CapACRContributing))
	require.True(t, d.Has(factor.CapResettable))
	require.True(t, d.Has(factor.CapRegistrable))
	require.True(t, d.Has(factor.CapRevocable))
	require.True(t, d.Has(factor.CapRateLimited))
	require.True(t, d.Has(factor.CapRiskReporting))
	require.False(t, d.Has(factor.CapRecoverable), "密码不得声明可找回")

	require.NotNil(t, d.Authenticatable)
	require.True(t, d.Authenticatable.AsFirst)
	require.False(t, d.Authenticatable.AsSecond)

	require.NotNil(t, d.ACR)
	require.Equal(t, factor.ACRAAL1, d.ACR.Sole)
	require.Equal(t, factor.ACRAAL2, d.ACR.Combined)

	require.NotNil(t, d.Registrable)
	require.False(t, d.Registrable.VerifyRequired)

	require.Nil(t, d.Recoverable, "密码 descriptor 不得带 recoverable 声明")
}

func TestPasswordProviderCapabilities(t *testing.T) {
	p := NewPasswordProvider(nil, nil, DefaultFailThreshold, DefaultLockDuration)

	var provider factor.Provider = p
	require.Equal(t, PasswordType, provider.Type())
	require.Equal(t, factor.KindHolding, provider.Kind())

	_, authenticatable := interface{}(p).(factor.Authenticatable)
	require.True(t, authenticatable)
	_, resettable := interface{}(p).(factor.Resettable)
	require.True(t, resettable)
	_, registrable := interface{}(p).(factor.Registrable)
	require.True(t, registrable)
	_, revocable := interface{}(p).(factor.Revocable)
	require.True(t, revocable)
	_, rateLimited := interface{}(p).(factor.RateLimited)
	require.True(t, rateLimited)
	_, risk := interface{}(p).(factor.RiskReporting)
	require.True(t, risk)

	_, recoverable := interface{}(p).(factor.Recoverable)
	require.False(t, recoverable, "密码不得实现 Recoverable——类型层面杜绝用密码找回密码")
}

func TestPasswordProviderAccessors(t *testing.T) {
	p := NewPasswordProvider(nil, nil, DefaultFailThreshold, DefaultLockDuration)
	require.Equal(t, "pwd", p.AMR())
	require.Equal(t, factor.NextActionInputPassword, p.NextAction())
	require.False(t, p.RegistrableVerifyRequired())

	require.Equal(t, factor.OnUnbindNone, p.RevocationPolicy().OnIdentifierUnbind)
	require.Equal(t, factor.ACRAAL2, p.RevocationPolicy().RequireACR)
	require.Equal(t, "5/15min", p.Limits().Verify)
	require.NotEmpty(t, p.RiskSignals())
	require.False(t, p.EnrollmentPolicy().RequiresPreBoundIdentifier)
	require.Equal(t, factor.ACRAAL2, p.RequiredProofForReset().MinACR)
}
