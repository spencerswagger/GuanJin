package holding

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	TOTPType factor.FactorType = "totp"

	totpChallengeTTL = 5 * time.Minute
	totpMaxAttempts  = 5

	totpAlgo   = "TOTP"
	totpHash   = "SM3"
	totpDigits = 6
	totpPeriod = 30
	totpSkew   = 1

	totpSecretKeyPurpose = "guanjin/sm4/totp-secret"
)

type totpCredential struct {
	Algo      string `json:"algo"`
	Hash      string `json:"hash"`
	Digits    int    `json:"digits"`
	Period    int    `json:"period"`
	SecretEnc string `json:"secret_enc"`
	BoundAt   string `json:"bound_at"`
}

type TotpProvider struct {
	*Base
	challenges *store.ChallengeRepo
	secretKey  []byte
}

func TotpDescriptor() factor.Descriptor {
	return factor.Descriptor{
		Type: TOTPType,
		Kind: factor.KindHolding,
		Capabilities: []string{
			factor.CapEnrollable,
			factor.CapAuthenticatable,
			factor.CapACRContributing,
			factor.CapRecoverable,
			factor.CapResettable,
			factor.CapRevocable,
			factor.CapRateLimited,
			factor.CapRiskReporting,
		},
		Authenticatable: &factor.AuthenticatableSpec{AsFirst: false, AsSecond: true},
		ACR:             &factor.ACRSpec{Sole: factor.ACRAAL1, Combined: factor.ACRAAL2},
		Recoverable:     &factor.RecoverableSpec{TrustLevel: 2},
		Revocable: &factor.RevocableSpec{
			OnIdentifierUnbind: factor.OnUnbindNone,
			RequireACR:         factor.ACRAAL2,
			CooldownHours:      24,
		},
		RateLimit: &factor.RateLimitSpec{
			Send:        "",
			Verify:      "5/15min",
			DailySend:   0,
			BudgetAlert: false,
		},
		RiskSignals: []string{"fail_burst", "geo_jump", "device_change"},
	}
}

func NewTotpProvider(auths *store.AuthenticatorRepo, challenges *store.ChallengeRepo, masterKey []byte, failThreshold int32, lockDuration time.Duration) *TotpProvider {
	return &TotpProvider{
		Base:       NewBase(TOTPType, TotpDescriptor(), auths, failThreshold, lockDuration),
		challenges: challenges,
		secretKey:  crypto.Derive(masterKey, totpSecretKeyPurpose, crypto.SM4KeySize),
	}
}

func (p *TotpProvider) AMR() string { return "totp" }

func (p *TotpProvider) NextAction() factor.NextAction { return factor.NextActionInputTOTP }

func (p *TotpProvider) EnrollmentPolicy() factor.EnrollmentPolicy {
	return factor.EnrollmentPolicy{
		RequiresPreBoundIdentifier: false,
		SelfServiceAllowed:         true,
		VerifyRequired:             true,
	}
}

func (p *TotpProvider) RecoveryTrustLevel() int { return 2 }

func (p *TotpProvider) BeginRecovery(ctx context.Context, userID string, target string) (factor.Ticket, error) {
	return p.BeginChallenge(ctx, userID, factor.PurposeRecover)
}

func (p *TotpProvider) RequiredProofForReset() factor.ProofRequirement {
	return factor.ProofRequirement{
		MinACR:      factor.ACRAAL2,
		MinTrust:    2,
		Description: "需其他因子证明身份（重新绑定 TOTP）",
	}
}

func (p *TotpProvider) Reset(ctx context.Context, userID, authenticatorID, proof string) error {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return factor.ErrInvalidCredential
	}
	aid, err := uuid.Parse(authenticatorID)
	if err != nil {
		return factor.ErrInvalidCredential
	}
	auth, err := p.Authenticators().GetByID(ctx, aid)
	if err != nil {
		return factor.ErrInvalidCredential
	}
	if auth.UserID != uid || auth.Type != string(TOTPType) {
		return factor.ErrInvalidCredential
	}
	if proof == "" {
		return factor.ErrInvalidCredential
	}
	encoded, err := p.encodeSecret([]byte(proof))
	if err != nil {
		return err
	}
	auth.CredentialData = encoded
	return p.Authenticators().Update(ctx, auth)
}

func (p *TotpProvider) RevocationPolicy() factor.RevocationPolicy {
	spec := TotpDescriptor().Revocable
	return factor.RevocationPolicy{
		OnIdentifierUnbind: spec.OnIdentifierUnbind,
		RequireACR:         spec.RequireACR,
		CooldownHours:      spec.CooldownHours,
	}
}

func (p *TotpProvider) Limits() factor.RateLimits {
	spec := TotpDescriptor().RateLimit
	return factor.RateLimits{
		Send:        spec.Send,
		Verify:      spec.Verify,
		DailySend:   spec.DailySend,
		BudgetAlert: spec.BudgetAlert,
	}
}

func (p *TotpProvider) RiskSignals() []string {
	return append([]string{}, TotpDescriptor().RiskSignals...)
}

func (p *TotpProvider) BeginChallenge(ctx context.Context, userID string, purpose factor.Purpose) (factor.Ticket, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return factor.Ticket{}, factor.ErrInvalidCredential
	}
	expires := time.Now().UTC().Add(totpChallengeTTL)
	challenge := &model.Challenge{
		UserID:       &uid,
		Purpose:      string(purpose),
		FactorType:   string(TOTPType),
		ResponseHash: []byte{},
		MaxAttempts:  totpMaxAttempts,
		ExpiresAt:    expires,
	}
	if err := p.challenges.Create(ctx, challenge); err != nil {
		return factor.Ticket{}, err
	}
	return factor.Ticket{
		ChallengeID:  challenge.ID.String(),
		FactorType:   TOTPType,
		Purpose:      purpose,
		NextAction:   factor.NextActionInputTOTP,
		TTL:          totpChallengeTTL,
		AttemptsLeft: totpMaxAttempts,
		ExpiresAt:    expires,
	}, nil
}

func (p *TotpProvider) VerifyResponse(ctx context.Context, ticket factor.Ticket, response string) error {
	if ticket.ChallengeID == "" {
		return factor.ErrChallengeInvalid
	}
	cid, err := uuid.Parse(ticket.ChallengeID)
	if err != nil {
		return factor.ErrChallengeInvalid
	}
	challenge, err := p.challenges.GetByID(ctx, cid)
	if err != nil {
		return factor.ErrChallengeInvalid
	}
	if challenge.FactorType != string(TOTPType) {
		return factor.ErrChallengeInvalid
	}
	if challenge.ConsumedAt != nil {
		return factor.ErrChallengeConsumed
	}
	if time.Now().UTC().After(challenge.ExpiresAt) {
		return factor.ErrChallengeExpired
	}
	if challenge.UserID == nil {
		return factor.ErrChallengeInvalid
	}
	auth, err := p.ActiveAuthenticator(ctx, *challenge.UserID)
	if err != nil {
		return factor.ErrInvalidCredential
	}
	if p.IsLocked(auth) {
		return factor.ErrAccountLocked
	}
	secret, decErr := p.decodeSecret(auth.CredentialData)
	if decErr != nil {
		_ = p.challenges.IncrementAttempts(ctx, cid)
		_ = p.RecordFailure(ctx, auth)
		return factor.ErrInvalidCredential
	}
	if !VerifyTOTP(secret, response, time.Now().UTC(), totpDigits, totpPeriod, totpSkew) {
		_ = p.challenges.IncrementAttempts(ctx, cid)
		_ = p.RecordFailure(ctx, auth)
		if challenge.Attempts+1 >= challenge.MaxAttempts {
			return factor.ErrAttemptsExceeded
		}
		return factor.ErrInvalidCredential
	}
	consumed, err := p.challenges.Consume(ctx, cid)
	if err != nil {
		return err
	}
	if !consumed {
		return factor.ErrChallengeConsumed
	}
	return p.RecordSuccess(ctx, auth)
}

func (p *TotpProvider) encodeSecret(secret []byte) ([]byte, error) {
	enc, err := crypto.SM4Encrypt(p.secretKey, secret)
	if err != nil {
		return nil, err
	}
	return json.Marshal(totpCredential{
		Algo:      totpAlgo,
		Hash:      totpHash,
		Digits:    totpDigits,
		Period:    totpPeriod,
		SecretEnc: base64.StdEncoding.EncodeToString(enc),
		BoundAt:   time.Now().UTC().Format(time.RFC3339),
	})
}

func (p *TotpProvider) decodeSecret(data []byte) ([]byte, error) {
	var env totpCredential
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Algo != totpAlgo {
		return nil, factor.ErrInvalidCredential
	}
	enc, err := base64.StdEncoding.DecodeString(env.SecretEnc)
	if err != nil {
		return nil, err
	}
	return crypto.SM4Decrypt(p.secretKey, enc)
}

func NewTOTPCredential(masterKey, secret []byte) ([]byte, error) {
	key := crypto.Derive(masterKey, totpSecretKeyPurpose, crypto.SM4KeySize)
	enc, err := crypto.SM4Encrypt(key, secret)
	if err != nil {
		return nil, err
	}
	return json.Marshal(totpCredential{
		Algo:      totpAlgo,
		Hash:      totpHash,
		Digits:    totpDigits,
		Period:    totpPeriod,
		SecretEnc: base64.StdEncoding.EncodeToString(enc),
		BoundAt:   time.Now().UTC().Format(time.RFC3339),
	})
}

func TOTPCode(secret []byte, t time.Time, digits, period int) string {
	if digits <= 0 {
		digits = totpDigits
	}
	if period <= 0 {
		period = totpPeriod
	}
	counter := uint64(t.Unix() / int64(period))
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)
	return dynamicTruncate(crypto.HMACSM3(secret, buf), digits)
}

func VerifyTOTP(secret []byte, candidate string, t time.Time, digits, period, skew int) bool {
	if digits <= 0 {
		digits = totpDigits
	}
	if period <= 0 {
		period = totpPeriod
	}
	matched := 0
	for i := -skew; i <= skew; i++ {
		shifted := t.Add(time.Duration(i) * time.Duration(period) * time.Second)
		expected := TOTPCode(secret, shifted, digits, period)
		matched |= subtle.ConstantTimeCompare([]byte(expected), []byte(candidate))
	}
	return matched == 1
}

func dynamicTruncate(mac []byte, digits int) string {
	offset := mac[len(mac)-1] & 0x0f
	binaryCode := (uint32(mac[offset])&0x7f)<<24 |
		uint32(mac[offset+1])<<16 |
		uint32(mac[offset+2])<<8 |
		uint32(mac[offset+3])
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, binaryCode%mod)
}
