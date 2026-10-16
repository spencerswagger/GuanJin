package flow

import (
	"github.com/spencerswagger/GuanJin/internal/factor"
)

type Orchestrator struct {
	reg *factor.Registry
}

func NewOrchestrator(reg *factor.Registry) *Orchestrator {
	return &Orchestrator{reg: reg}
}

func (o *Orchestrator) Registry() *factor.Registry { return o.reg }

func acrRank(acr string) int {
	switch acr {
	case factor.ACRAAL2:
		return 2
	case factor.ACRAAL1:
		return 1
	default:
		return 0
	}
}

func maxACR(a, b string) string {
	if acrRank(b) > acrRank(a) {
		return b
	}
	return a
}

func (o *Orchestrator) Satisfied(current, target string) bool {
	return acrRank(current) >= acrRank(target)
}

func (o *Orchestrator) ResolveTargetACR(tier, policyMinACR string) string {
	if policyMinACR != "" {
		return policyMinACR
	}
	if tier == "L3" {
		return factor.ACRAAL2
	}
	return factor.ACRAAL1
}

func (o *Orchestrator) activeFactors(achieved, disabled []string) []string {
	blocked := make(map[string]bool, len(disabled))
	for _, d := range disabled {
		blocked[d] = true
	}
	out := make([]string, 0, len(achieved))
	seen := map[string]bool{}
	for _, typ := range achieved {
		if blocked[typ] || seen[typ] {
			continue
		}
		if !o.reg.Enabled(typ) {
			continue
		}
		seen[typ] = true
		out = append(out, typ)
	}
	return out
}

func (o *Orchestrator) CurrentACR(achieved, disabled []string) string {
	active := o.activeFactors(achieved, disabled)
	switch len(active) {
	case 0:
		return factor.ACRAAL0
	case 1:
		return o.descriptorFor(active[0]).SoleACR()
	default:
		best := factor.ACRAAL0
		for _, typ := range active {
			d := o.descriptorFor(typ)
			best = maxACR(best, d.SoleACR())
			best = maxACR(best, d.CombinedACR())
		}
		return best
	}
}

func (o *Orchestrator) AMR(achieved, disabled []string) []string {
	active := o.activeFactors(achieved, disabled)
	out := make([]string, 0, len(active))
	for _, typ := range active {
		out = append(out, o.amrFor(typ))
	}
	return out
}

func (o *Orchestrator) amrFor(typ string) string {
	p, ok := o.reg.Get(typ)
	if !ok {
		return typ
	}
	if namer, ok := p.(factor.AmrNamer); ok {
		return namer.AMR()
	}
	return typ
}

func (o *Orchestrator) descriptorFor(typ string) factor.Descriptor {
	p, ok := o.reg.Get(typ)
	if !ok {
		return factor.Descriptor{Type: typ}
	}
	return p.Descriptor()
}

func (o *Orchestrator) NextStep(achieved, disabled []string) (factor.Provider, Role, bool) {
	blocked := map[string]bool{}
	for _, d := range disabled {
		blocked[d] = true
	}
	for _, a := range achieved {
		blocked[a] = true
	}
	needFirst := len(o.activeFactors(achieved, disabled)) == 0
	for _, p := range o.reg.All() {
		typ := p.Type()
		if blocked[typ] || !o.reg.Enabled(typ) {
			continue
		}
		if _, ok := p.(factor.Authenticatable); !ok {
			continue
		}
		spec := p.Descriptor().Authenticatable
		if spec == nil {
			continue
		}
		if needFirst {
			if !spec.AsFirst {
				continue
			}
			return p, RoleFirst, true
		}
		if !spec.AsSecond {
			continue
		}
		return p, RoleSecond, true
	}
	return nil, "", false
}

func (o *Orchestrator) nextActionFor(p factor.Provider) string {
	if namer, ok := p.(factor.NextActionNamer); ok {
		return string(namer.NextAction())
	}
	if p.Kind() == factor.KindChannel {
		return string(factor.NextActionTypeCode)
	}
	return string(factor.NextActionInputPassword)
}

func (o *Orchestrator) Descriptor(state *State, disabled []string, registrationMode string) *FlowDescriptor {
	current := o.CurrentACR(state.Achieved, disabled)
	satisfied := o.Satisfied(current, state.TargetACR)

	steps := make([]FlowStep, 0, len(state.Achieved)+1)
	for i, typ := range o.activeFactors(state.Achieved, disabled) {
		role := RoleFirst
		if i > 0 {
			role = RoleSecond
		}
		steps = append(steps, FlowStep{Factor: typ, Role: role, Status: StatusDone})
	}
	if !satisfied {
		if p, role, ok := o.NextStep(state.Achieved, disabled); ok {
			steps = append(steps, FlowStep{
				Factor:        p.Type(),
				Role:          role,
				Status:        StatusPending,
				ChallengeHint: &ChallengeHint{NextAction: o.nextActionFor(p)},
			})
		}
	}

	mode := registrationMode
	if mode == "" {
		mode = "self"
	}
	return &FlowDescriptor{
		FlowID:           state.FlowID,
		Client:           ClientRef{ID: state.ClientID, Name: state.ClientName, Tier: state.Tier},
		TargetACR:        state.TargetACR,
		CurrentACR:       current,
		Satisfied:        satisfied,
		Steps:            steps,
		AvailableFactors: o.reg.EnabledTypes(),
		DisabledFactors:  o.mergedDisabled(disabled),
		RegistrationMode: mode,
		ExpiresAt:        state.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (o *Orchestrator) mergedDisabled(disabled []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(disabled)+1)
	for _, d := range disabled {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	for _, d := range o.reg.DisabledTypes() {
		if seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}
