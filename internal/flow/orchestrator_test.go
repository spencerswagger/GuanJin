package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/spencerswagger/GuanJin/internal/factor"
)

type stubFactor struct {
	typ  string
	kind factor.Kind
	desc factor.Descriptor
}

func (s stubFactor) Type() string { return s.typ }

func (s stubFactor) Kind() factor.Kind { return s.kind }

func (s stubFactor) Descriptor() factor.Descriptor { return s.desc }

type stubAuth struct {
	stubFactor
}

func (s stubAuth) BeginChallenge(_ context.Context, userID string, purpose factor.Purpose) (factor.Ticket, error) {
	return factor.Ticket{ChallengeID: "c-" + userID, FactorType: s.typ, Purpose: purpose}, nil
}

func (s stubAuth) VerifyResponse(_ context.Context, _ factor.Ticket, _ string) error { return nil }

func holdingWithACR(typ string, sole, combined string) stubAuth {
	return stubAuth{stubFactor{typ: typ, kind: factor.KindHolding, desc: factor.Descriptor{
		Type: typ,
		Kind: factor.KindHolding,
		ACR:  &factor.ACRSpec{Sole: sole, Combined: combined},
	}}}
}

func newRegistry(t *testing.T, providers ...factor.Provider) *factor.Registry {
	t.Helper()
	reg := factor.NewRegistry()
	for _, p := range providers {
		require.NoError(t, reg.Register(p))
	}
	return reg
}

func TestResolveTargetACR(t *testing.T) {
	o := NewOrchestrator(factor.NewRegistry())
	require.Equal(t, factor.ACRAAL1, o.ResolveTargetACR("L1", ""))
	require.Equal(t, factor.ACRAAL1, o.ResolveTargetACR("L2", ""))
	require.Equal(t, factor.ACRAAL2, o.ResolveTargetACR("L3", ""))
	require.Equal(t, factor.ACRAAL2, o.ResolveTargetACR("L2", factor.ACRAAL2))
	require.Equal(t, factor.ACRAAL1, o.ResolveTargetACR("L3", factor.ACRAAL1))
}

func TestCurrentACROnlyCountsRegisteredAndEnabled(t *testing.T) {
	pw := holdingWithACR("pw", factor.ACRAAL1, factor.ACRAAL2)
	totp := holdingWithACR("tp", factor.ACRAAL1, factor.ACRAAL2)
	o := NewOrchestrator(newRegistry(t, pw, totp))

	require.Equal(t, factor.ACRAAL0, o.CurrentACR(nil, nil))
	require.Equal(t, factor.ACRAAL1, o.CurrentACR([]string{"pw"}, nil))
	require.Equal(t, factor.ACRAAL2, o.CurrentACR([]string{"pw", "tp"}, nil))
	require.Equal(t, factor.ACRAAL1, o.CurrentACR([]string{"pw", "ghost"}, nil))

	require.Equal(t, factor.ACRAAL0, o.CurrentACR([]string{"pw"}, []string{"pw"}))
	require.Equal(t, factor.ACRAAL1, o.CurrentACR([]string{"pw", "tp"}, []string{"tp"}))
}

func TestCurrentACRExcludesPlatformDisabled(t *testing.T) {
	pw := holdingWithACR("pw", factor.ACRAAL1, factor.ACRAAL2)
	totp := holdingWithACR("tp", factor.ACRAAL1, factor.ACRAAL2)
	reg := newRegistry(t, pw, totp)
	o := NewOrchestrator(reg)

	require.Equal(t, factor.ACRAAL2, o.CurrentACR([]string{"pw", "tp"}, nil))
	reg.SetEnabled("tp", false)
	require.Equal(t, factor.ACRAAL1, o.CurrentACR([]string{"pw", "tp"}, nil))
}

func TestAMRMapping(t *testing.T) {
	pw := holdingWithACR("pw", factor.ACRAAL1, factor.ACRAAL2)
	o := NewOrchestrator(newRegistry(t, pw))
	require.Equal(t, []string{"pw"}, o.AMR([]string{"pw"}, nil))
	require.Empty(t, o.AMR(nil, nil))
}

func TestNextStepSelectsByCapabilityNotType(t *testing.T) {
	channelFirst := stubAuth{stubFactor{typ: "chan_first", kind: factor.KindChannel, desc: factor.Descriptor{
		Type: "chan_first", Kind: factor.KindChannel,
		Authenticatable: &factor.AuthenticatableSpec{AsFirst: true, AsSecond: false},
	}}}
	holdingSecond := stubAuth{stubFactor{typ: "hold_second", kind: factor.KindHolding, desc: factor.Descriptor{
		Type: "hold_second", Kind: factor.KindHolding,
		Authenticatable: &factor.AuthenticatableSpec{AsFirst: false, AsSecond: true},
	}}}
	noAuth := stubFactor{typ: "no_auth", kind: factor.KindHolding, desc: factor.Descriptor{Type: "no_auth", Kind: factor.KindHolding}}
	o := NewOrchestrator(newRegistry(t, channelFirst, holdingSecond, noAuth))

	p, role, ok := o.NextStep(nil, nil)
	require.True(t, ok)
	require.Equal(t, "chan_first", p.Type())
	require.Equal(t, RoleFirst, role)

	p, role, ok = o.NextStep([]string{"chan_first"}, nil)
	require.True(t, ok)
	require.Equal(t, "hold_second", p.Type())
	require.Equal(t, RoleSecond, role)

	_, _, ok = o.NextStep([]string{"chan_first", "hold_second"}, nil)
	require.False(t, ok)

	_, _, ok = o.NextStep(nil, []string{"chan_first"})
	require.False(t, ok)
}

func TestNextStepRespectsPlatformDisable(t *testing.T) {
	channelFirst := stubAuth{stubFactor{typ: "chan_first", kind: factor.KindChannel, desc: factor.Descriptor{
		Type: "chan_first", Kind: factor.KindChannel,
		Authenticatable: &factor.AuthenticatableSpec{AsFirst: true},
	}}}
	reg := newRegistry(t, channelFirst)
	o := NewOrchestrator(reg)

	_, _, ok := o.NextStep(nil, nil)
	require.True(t, ok)
	reg.SetEnabled("chan_first", false)
	_, _, ok = o.NextStep(nil, nil)
	require.False(t, ok)
}

func TestDescriptorBuild(t *testing.T) {
	pw := stubAuth{stubFactor{typ: "pw", kind: factor.KindHolding, desc: factor.Descriptor{
		Type: "pw", Kind: factor.KindHolding,
		ACR:             &factor.ACRSpec{Sole: factor.ACRAAL1, Combined: factor.ACRAAL2},
		Authenticatable: &factor.AuthenticatableSpec{AsFirst: true},
	}}}
	totp := stubAuth{stubFactor{typ: "tp", kind: factor.KindHolding, desc: factor.Descriptor{
		Type: "tp", Kind: factor.KindHolding,
		ACR:             &factor.ACRSpec{Sole: factor.ACRAAL1, Combined: factor.ACRAAL2},
		Authenticatable: &factor.AuthenticatableSpec{AsSecond: true},
	}}}
	reg := newRegistry(t, pw, totp)
	o := NewOrchestrator(reg)

	state := &State{
		FlowID:     "flow-1",
		ClientID:   "client-1",
		ClientName: "业务系统",
		Tier:       "L3",
		TargetACR:  factor.ACRAAL2,
		ExpiresAt:  time.Now().Add(time.Minute),
	}
	desc := o.Descriptor(state, nil, "self")
	require.Equal(t, "flow-1", desc.FlowID)
	require.Equal(t, "client-1", desc.Client.ID)
	require.Equal(t, "L3", desc.Client.Tier)
	require.Equal(t, factor.ACRAAL2, desc.TargetACR)
	require.Equal(t, factor.ACRAAL0, desc.CurrentACR)
	require.False(t, desc.Satisfied)
	require.Len(t, desc.Steps, 1)
	require.Equal(t, "pw", desc.Steps[0].Factor)
	require.Equal(t, RoleFirst, desc.Steps[0].Role)
	require.Equal(t, StatusPending, desc.Steps[0].Status)
	require.Contains(t, desc.AvailableFactors, "pw")
	require.Contains(t, desc.AvailableFactors, "tp")
	require.Equal(t, "self", desc.RegistrationMode)

	state.Achieved = []string{"pw"}
	desc = o.Descriptor(state, nil, "self")
	require.Equal(t, factor.ACRAAL1, desc.CurrentACR)
	require.False(t, desc.Satisfied)
	require.Len(t, desc.Steps, 2)
	require.Equal(t, StatusDone, desc.Steps[0].Status)
	require.Equal(t, "tp", desc.Steps[1].Factor)
	require.Equal(t, RoleSecond, desc.Steps[1].Role)
	require.Equal(t, StatusPending, desc.Steps[1].Status)

	state.Achieved = []string{"pw", "tp"}
	desc = o.Descriptor(state, nil, "self")
	require.Equal(t, factor.ACRAAL2, desc.CurrentACR)
	require.True(t, desc.Satisfied)
	require.Len(t, desc.Steps, 2)
}

func TestDescriptorMergesClientAndPlatformDisabled(t *testing.T) {
	pw := stubAuth{stubFactor{typ: "pw", kind: factor.KindHolding, desc: factor.Descriptor{
		Type: "pw", Kind: factor.KindHolding, ACR: &factor.ACRSpec{Sole: factor.ACRAAL1},
	}}}
	totp := stubAuth{stubFactor{typ: "tp", kind: factor.KindHolding, desc: factor.Descriptor{
		Type: "tp", Kind: factor.KindHolding, ACR: &factor.ACRSpec{Sole: factor.ACRAAL1},
	}}}
	reg := newRegistry(t, pw, totp)
	reg.SetEnabled("tp", false)
	o := NewOrchestrator(reg)

	state := &State{FlowID: "f", ClientID: "c", Tier: "L2", TargetACR: factor.ACRAAL1, ExpiresAt: time.Now().Add(time.Minute)}
	desc := o.Descriptor(state, []string{"pw"}, "invite")
	require.Equal(t, []string{"pw", "tp"}, desc.DisabledFactors)
	require.Equal(t, "invite", desc.RegistrationMode)
	require.Empty(t, desc.Steps)
}
