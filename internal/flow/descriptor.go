package flow

type Acr = string

type Tier = string

type UiAtom = string

const (
	AtomPassword UiAtom = "password"
	AtomOTP      UiAtom = "otp"
	AtomQrCode   UiAtom = "qrcode"
	AtomRedirect UiAtom = "redirect"
)

type Role = string

const (
	RoleFirst  Role = "first"
	RoleSecond Role = "second"
)

type StepStatus = string

const (
	StatusPending StepStatus = "pending"
	StatusDone    StepStatus = "done"
)

type ChallengeHint struct {
	TargetMasked string `json:"target_masked,omitempty"`
	NextAction   string `json:"next_action,omitempty"`
}

type FlowStep struct {
	Factor        string         `json:"factor"`
	Role          Role           `json:"role"`
	Status        StepStatus     `json:"status"`
	ChallengeHint *ChallengeHint `json:"challenge_hint,omitempty"`
}

type ClientRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Tier Tier   `json:"tier"`
}

type FlowDescriptor struct {
	FlowID           string     `json:"flow_id"`
	Client           ClientRef  `json:"client"`
	TargetACR        Acr        `json:"target_acr"`
	CurrentACR       Acr        `json:"current_acr"`
	Satisfied        bool       `json:"satisfied"`
	Steps            []FlowStep `json:"steps"`
	AvailableFactors []string   `json:"available_factors"`
	DisabledFactors  []string   `json:"disabled_factors"`
	RegistrationMode string     `json:"registration_mode"`
	ExpiresAt        string     `json:"expires_at"`
}
