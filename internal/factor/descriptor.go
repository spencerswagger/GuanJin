package factor

const (
	CapEnrollable          = "enrollable"
	CapIdentifierVerifying = "identifier_verifying"
	CapAuthenticatable     = "authenticatable"
	CapACRContributing     = "acr_contributing"
	CapRecoverable         = "recoverable"
	CapResettable          = "resettable"
	CapRegistrable         = "registrable"
	CapRevocable           = "revocable"
	CapRateLimited         = "rate_limited"
	CapRiskReporting       = "risk_reporting"
)

const (
	ACRAAL0 = "AAL0"
	ACRAAL1 = "AAL1"
	ACRAAL2 = "AAL2"
)

const (
	OnUnbindNone      = "none"
	OnUnbindImmediate = "immediate"
	OnUnbindDeferred  = "deferred"
)

type Descriptor struct {
	Type                FactorType               `json:"type"`
	Kind                Kind                     `json:"kind"`
	Capabilities        []string                 `json:"capabilities"`
	Authenticatable     *AuthenticatableSpec     `json:"authenticatable,omitempty"`
	ACR                 *ACRSpec                 `json:"acr,omitempty"`
	IdentifierVerifying *IdentifierVerifyingSpec `json:"identifier_verifying,omitempty"`
	Recoverable         *RecoverableSpec         `json:"recoverable,omitempty"`
	Registrable         *RegistrableSpec         `json:"registrable,omitempty"`
	Revocable           *RevocableSpec           `json:"revocable,omitempty"`
	RateLimit           *RateLimitSpec           `json:"rate_limit,omitempty"`
	RiskSignals         []string                 `json:"risk_signals,omitempty"`
}

type AuthenticatableSpec struct {
	AsFirst  bool `json:"as_first"`
	AsSecond bool `json:"as_second"`
}

type ACRSpec struct {
	Sole     string `json:"sole"`
	Combined string `json:"combined"`
}

type IdentifierVerifyingSpec struct {
	Targets []string `json:"targets"`
}

type RecoverableSpec struct {
	TrustLevel int `json:"trust_level"`
}

type RegistrableSpec struct {
	VerifyRequired bool `json:"verify_required"`
}

type RevocableSpec struct {
	OnIdentifierUnbind string `json:"on_identifier_unbind"`
	RequireACR         string `json:"require_acr"`
	CooldownHours      int    `json:"cooldown_hours"`
}

type RateLimitSpec struct {
	Send        string `json:"send"`
	Verify      string `json:"verify"`
	DailySend   int    `json:"daily_send"`
	BudgetAlert bool   `json:"budget_alert"`
}

func (d Descriptor) Has(capability string) bool {
	for _, c := range d.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

func (d Descriptor) SoleACR() string {
	if d.ACR == nil || d.ACR.Sole == "" {
		return ACRAAL0
	}
	return d.ACR.Sole
}

func (d Descriptor) CombinedACR() string {
	if d.ACR == nil || d.ACR.Combined == "" {
		return d.SoleACR()
	}
	return d.ACR.Combined
}
