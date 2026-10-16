package factor

import (
	"context"
	"errors"
	"time"
)

type Purpose string

const (
	PurposeLogin   Purpose = "login"
	PurposeStepUp  Purpose = "stepup"
	PurposeBind    Purpose = "bind"
	PurposeRecover Purpose = "recover"
)

var purposeAliases = map[string]Purpose{
	"login":             PurposeLogin,
	"stepup":            PurposeStepUp,
	"step_up":           PurposeStepUp,
	"enroll":            PurposeBind,
	"bind":              PurposeBind,
	"verify_identifier": PurposeBind,
	"recover":           PurposeRecover,
}

func ParsePurpose(raw string) Purpose {
	if p, ok := purposeAliases[raw]; ok {
		return p
	}
	return PurposeLogin
}

func (p Purpose) Valid() bool {
	switch p {
	case PurposeLogin, PurposeStepUp, PurposeBind, PurposeRecover:
		return true
	default:
		return false
	}
}

type NextAction string

const (
	NextActionTypeCode      NextAction = "type_code"
	NextActionInputTOTP     NextAction = "input_totp"
	NextActionInputPassword NextAction = "input_password"
)

type Ticket struct {
	ChallengeID  string
	FactorType   FactorType
	Purpose      Purpose
	TargetMasked string
	NextAction   NextAction
	TTL          time.Duration
	AttemptsLeft int
	ExpiresAt    time.Time
}

type Authenticatable interface {
	BeginChallenge(ctx context.Context, userID string, purpose Purpose) (Ticket, error)
	VerifyResponse(ctx context.Context, ticket Ticket, response string) error
}

type IdentifierVerifying interface {
	VerifiesIdentifierType() string
	VerifyOwnership(ctx context.Context, userID, identifierID, code string) error
}

type ACRContributing interface {
	ContributesACR() (sole string, combined string)
}

type Recoverable interface {
	RecoveryTrustLevel() int
	BeginRecovery(ctx context.Context, userID string, target string) (Ticket, error)
}

type ProofRequirement struct {
	MinACR      string
	MinTrust    int
	Description string
}

type Resettable interface {
	RequiredProofForReset() ProofRequirement
	Reset(ctx context.Context, userID, authenticatorID string, proof string) error
}

type EnrollmentPolicy struct {
	RequiresPreBoundIdentifier bool
	SelfServiceAllowed         bool
	VerifyRequired             bool
}

type Enrollable interface {
	EnrollmentPolicy() EnrollmentPolicy
}

type Registrable interface {
	RegistrableVerifyRequired() bool
}

type RevocationPolicy struct {
	OnIdentifierUnbind string
	RequireACR         string
	CooldownHours      int
}

type Revocable interface {
	RevocationPolicy() RevocationPolicy
}

type RateLimits struct {
	Send        string
	Verify      string
	DailySend   int
	BudgetAlert bool
}

type RateLimited interface {
	Limits() RateLimits
}

type RiskReporting interface {
	RiskSignals() []string
}

type NextActionNamer interface {
	NextAction() NextAction
}

type AmrNamer interface {
	AMR() string
}

var (
	ErrFactorNotSupported = errors.New("FACTOR_NOT_SUPPORTED")
	ErrFactorDisabled     = errors.New("FACTOR_DISABLED")
	ErrNoEligibleFactor   = errors.New("NO_ELIGIBLE_FACTOR")
	ErrChallengeExpired   = errors.New("CHALLENGE_EXPIRED")
	ErrChallengeConsumed  = errors.New("CHALLENGE_CONSUMED")
	ErrChallengeInvalid   = errors.New("CHALLENGE_INVALID")
	ErrInvalidCredential  = errors.New("INVALID_CREDENTIAL")
	ErrAttemptsExceeded   = errors.New("ATTEMPTS_EXCEEDED")
	ErrAccountLocked      = errors.New("ACCOUNT_LOCKED")
)
