package factor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type stubProvider struct {
	typ  FactorType
	kind Kind
	desc Descriptor
}

func (s stubProvider) Type() FactorType { return s.typ }

func (s stubProvider) Kind() Kind { return s.kind }

func (s stubProvider) Descriptor() Descriptor { return s.desc }

func TestRegistryRegisterAndLookup(t *testing.T) {
	reg := NewRegistry()
	require.NoError(t, reg.Register(stubProvider{typ: "password", kind: KindHolding, desc: Descriptor{Type: "password", Kind: KindHolding}}))
	require.NoError(t, reg.Register(stubProvider{typ: "sms_otp", kind: KindChannel, desc: Descriptor{Type: "sms_otp", Kind: KindChannel}}))

	got, ok := reg.Get("password")
	require.True(t, ok)
	require.Equal(t, FactorType("password"), got.Type())

	_, ok = reg.Get("nope")
	require.False(t, ok)

	require.Equal(t, []FactorType{"password", "sms_otp"}, reg.Types())
	require.Equal(t, []FactorType{"password", "sms_otp"}, reg.EnabledTypes())
	require.Len(t, reg.All(), 2)
	require.Len(t, reg.Descriptors(), 2)
}

func TestRegistryRejectsInvalidAndDuplicate(t *testing.T) {
	reg := NewRegistry()
	require.ErrorIs(t, reg.Register(nil), ErrInvalidProvider)
	require.ErrorIs(t, reg.Register(stubProvider{typ: "", kind: KindHolding}), ErrInvalidProvider)
	require.ErrorIs(t, reg.Register(stubProvider{typ: "weird", kind: Kind("bogus")}), ErrInvalidProvider)

	p := stubProvider{typ: "password", kind: KindHolding}
	require.NoError(t, reg.Register(p))
	require.ErrorIs(t, reg.Register(p), ErrInvalidProvider)
}

func TestRegistryEnableDisable(t *testing.T) {
	reg := NewRegistry()
	require.NoError(t, reg.Register(stubProvider{typ: "password", kind: KindHolding}))
	require.NoError(t, reg.Register(stubProvider{typ: "totp", kind: KindHolding}))

	require.True(t, reg.Enabled("password"))
	reg.SetEnabled("password", false)
	require.False(t, reg.Enabled("password"))
	require.Equal(t, []FactorType{"totp"}, reg.EnabledTypes())
	require.Equal(t, []FactorType{"password"}, reg.DisabledTypes())

	reg.SetEnabled("password", true)
	require.True(t, reg.Enabled("password"))
	require.Empty(t, reg.DisabledTypes())

	require.False(t, reg.Enabled("missing"))
}

func TestDescriptorHelpers(t *testing.T) {
	d := Descriptor{
		Capabilities: []string{CapAuthenticatable, CapRateLimited},
		ACR:          &ACRSpec{Sole: ACRAAL1, Combined: ACRAAL2},
	}
	require.True(t, d.Has(CapAuthenticatable))
	require.False(t, d.Has(CapRecoverable))
	require.Equal(t, ACRAAL1, d.SoleACR())
	require.Equal(t, ACRAAL2, d.CombinedACR())

	var empty Descriptor
	require.Equal(t, ACRAAL0, empty.SoleACR())
	require.Equal(t, ACRAAL0, empty.CombinedACR())
}

func TestParsePurpose(t *testing.T) {
	require.Equal(t, PurposeLogin, ParsePurpose("login"))
	require.Equal(t, PurposeStepUp, ParsePurpose("step_up"))
	require.Equal(t, PurposeStepUp, ParsePurpose("stepup"))
	require.Equal(t, PurposeBind, ParsePurpose("enroll"))
	require.Equal(t, PurposeRecover, ParsePurpose("recover"))
	require.Equal(t, PurposeLogin, ParsePurpose("unknown"))
	require.True(t, PurposeBind.Valid())
	require.False(t, Purpose("bogus").Valid())
}
