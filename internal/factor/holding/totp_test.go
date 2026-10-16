package holding

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/spencerswagger/GuanJin/internal/factor"
)

func testMasterKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func TestTotpDescriptorDeclarations(t *testing.T) {
	d := TotpDescriptor()
	require.Equal(t, TOTPType, d.Type)
	require.Equal(t, factor.KindHolding, d.Kind)

	require.True(t, d.Has(factor.CapEnrollable))
	require.True(t, d.Has(factor.CapAuthenticatable))
	require.True(t, d.Has(factor.CapACRContributing))
	require.True(t, d.Has(factor.CapRecoverable))
	require.True(t, d.Has(factor.CapResettable))
	require.True(t, d.Has(factor.CapRevocable))
	require.True(t, d.Has(factor.CapRateLimited))
	require.True(t, d.Has(factor.CapRiskReporting))
	require.False(t, d.Has(factor.CapRegistrable), "TOTP 不得声明可注册")
	require.False(t, d.Has(factor.CapIdentifierVerifying))

	require.NotNil(t, d.Authenticatable)
	require.False(t, d.Authenticatable.AsFirst)
	require.True(t, d.Authenticatable.AsSecond)

	require.NotNil(t, d.ACR)
	require.Equal(t, factor.ACRAAL1, d.ACR.Sole)
	require.Equal(t, factor.ACRAAL2, d.ACR.Combined)

	require.NotNil(t, d.Recoverable)
	require.Equal(t, 2, d.Recoverable.TrustLevel)

	require.Nil(t, d.Registrable, "TOTP descriptor 不得带 registrable 声明")
}

func TestTotpProviderCapabilities(t *testing.T) {
	p := NewTotpProvider(nil, nil, testMasterKey(), DefaultFailThreshold, DefaultLockDuration)

	var provider factor.Provider = p
	require.Equal(t, TOTPType, provider.Type())
	require.Equal(t, factor.KindHolding, provider.Kind())

	_, authenticatable := interface{}(p).(factor.Authenticatable)
	require.True(t, authenticatable)
	_, recoverable := interface{}(p).(factor.Recoverable)
	require.True(t, recoverable)
	_, resettable := interface{}(p).(factor.Resettable)
	require.True(t, resettable)
	_, revocable := interface{}(p).(factor.Revocable)
	require.True(t, revocable)
	_, rateLimited := interface{}(p).(factor.RateLimited)
	require.True(t, rateLimited)
	_, risk := interface{}(p).(factor.RiskReporting)
	require.True(t, risk)
	_, namer := interface{}(p).(factor.AmrNamer)
	require.True(t, namer)
	_, nextAction := interface{}(p).(factor.NextActionNamer)
	require.True(t, nextAction)

	_, registrable := interface{}(p).(factor.Registrable)
	require.False(t, registrable, "TOTP 不得实现 Registrable")
	_, identifierVerifying := interface{}(p).(factor.IdentifierVerifying)
	require.False(t, identifierVerifying)
}

func TestTotpProviderAccessors(t *testing.T) {
	p := NewTotpProvider(nil, nil, testMasterKey(), DefaultFailThreshold, DefaultLockDuration)
	require.Equal(t, "totp", p.AMR())
	require.Equal(t, factor.NextActionInputTOTP, p.NextAction())
	require.Equal(t, 2, p.RecoveryTrustLevel())

	require.Equal(t, factor.OnUnbindNone, p.RevocationPolicy().OnIdentifierUnbind)
	require.Equal(t, factor.ACRAAL2, p.RevocationPolicy().RequireACR)
	require.Equal(t, "5/15min", p.Limits().Verify)
	require.NotEmpty(t, p.RiskSignals())
	require.False(t, p.EnrollmentPolicy().RequiresPreBoundIdentifier)
	require.True(t, p.EnrollmentPolicy().VerifyRequired)
	require.Equal(t, factor.ACRAAL2, p.RequiredProofForReset().MinACR)
}

func TestTotpAlgorithmDeterministicVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	vectors := []struct {
		ts   int64
		code string
	}{
		{59, "614758"},
		{1111111109, "578389"},
		{1234567890, "982201"},
		{2000000000, "639624"},
	}
	for _, v := range vectors {
		got := TOTPCode(secret, time.Unix(v.ts, 0).UTC(), 6, 30)
		require.Equal(t, v.code, got, "ts=%d", v.ts)
		require.Len(t, got, 6)
	}

	require.Equal(t, "578389", TOTPCode(secret, time.Unix(1111111109, 0).UTC(), 0, 0),
		"digits/period 传 0 时应回退默认 6/30")
}

func TestVerifyTotpWindowAndRejection(t *testing.T) {
	secret := []byte("12345678901234567890")
	base := time.Unix(1111111109, 0).UTC()
	code := TOTPCode(secret, base, 6, 30)

	require.True(t, VerifyTOTP(secret, code, base, 6, 30, 1), "当前窗口应通过")
	require.True(t, VerifyTOTP(secret, code, base.Add(30*time.Second), 6, 30, 1), "前一窗口应通过（±1）")
	require.True(t, VerifyTOTP(secret, code, base.Add(-30*time.Second), 6, 30, 1), "后一窗口应通过（±1）")
	require.False(t, VerifyTOTP(secret, code, base.Add(60*time.Second), 6, 30, 1), "超出一个窗口应拒绝")
	require.False(t, VerifyTOTP(secret, code, base.Add(-60*time.Second), 6, 30, 1), "超出一个窗口应拒绝")

	require.False(t, VerifyTOTP(secret, "000000", base, 6, 30, 1), "错误码应拒绝")
	require.False(t, VerifyTOTP(secret, "", base, 6, 30, 1), "空码应拒绝")
	require.False(t, VerifyTOTP(secret, "57838", base, 6, 30, 1), "长度不符应拒绝")
	require.False(t, VerifyTOTP([]byte("another-secret-value"), code, base, 6, 30, 1), "错误密钥应拒绝")
}

func TestTotpCredentialRoundTrip(t *testing.T) {
	masterKey := testMasterKey()
	secret := []byte("totp-round-trip-secret-bytes")
	p := NewTotpProvider(nil, nil, masterKey, DefaultFailThreshold, DefaultLockDuration)

	env, err := NewTOTPCredential(masterKey, secret)
	require.NoError(t, err)

	require.Contains(t, string(env), `"algo":"TOTP"`)
	require.Contains(t, string(env), `"hash":"SM3"`)
	require.Contains(t, string(env), `"digits":6`)
	require.Contains(t, string(env), `"period":30`)
	require.Contains(t, string(env), `"secret_enc"`)
	require.NotContains(t, string(env), string(secret), "secret 不得明文落库")

	decoded, err := p.decodeSecret(env)
	require.NoError(t, err)
	require.Equal(t, secret, decoded)

	other := NewTotpProvider(nil, nil, []byte("a-completely-different-master-key"), DefaultFailThreshold, DefaultLockDuration)
	decodedOther, otherErr := other.decodeSecret(env)
	require.True(t, otherErr != nil || !bytes.Equal(decodedOther, secret), "不同主密钥派生的密钥不得解出相同明文")
}
