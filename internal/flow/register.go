package flow

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/spencerswagger/GuanJin/internal/credential"
	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/identifier"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	registerKeyPrefix = "guanjin:register:"
	RegisterTTL       = 15 * time.Minute

	passwordAuthenticatorType = "password"
)

type ChannelChallenger interface {
	BeginChallengeForTarget(ctx context.Context, target, masked, clientID string, purpose factor.Purpose) (factor.Ticket, error)
}

type RegisterContext struct {
	Token         string    `json:"token"`
	ClientID      string    `json:"client_id"`
	ClientTier    string    `json:"client_tier"`
	Type          string    `json:"type"`
	Value         string    `json:"value"`
	ValueMasked   string    `json:"value_masked"`
	Username      string    `json:"username"`
	StoreIdent    bool      `json:"store_identifier"`
	ChallengeID   string    `json:"challenge_id"`
	FactorType    string    `json:"factor_type"`
	AMR           string    `json:"amr"`
	Purpose       string    `json:"purpose"`
	VerifyMethod  string    `json:"verify_method"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type RegisterStore struct {
	rdb *store.Redis
}

func NewRegisterStore(rdb *store.Redis) *RegisterStore {
	return &RegisterStore{rdb: rdb}
}

func (s *RegisterStore) client() *redis.Client {
	if s.rdb == nil {
		return nil
	}
	return s.rdb.Client()
}

func (s *RegisterStore) Put(ctx context.Context, rc *RegisterContext) error {
	client := s.client()
	if client == nil {
		return ErrStoreUnavailable
	}
	blob, err := json.Marshal(rc)
	if err != nil {
		return err
	}
	ttl := RegisterTTL
	if !rc.ExpiresAt.IsZero() {
		if remaining := time.Until(rc.ExpiresAt); remaining > 0 && remaining < ttl {
			ttl = remaining
		}
	}
	if err := client.Set(ctx, registerKeyPrefix+rc.Token, blob, ttl).Err(); err != nil {
		return ErrStoreUnavailable
	}
	return nil
}

func (s *RegisterStore) Take(ctx context.Context, token string) (*RegisterContext, error) {
	client := s.client()
	if client == nil {
		return nil, ErrStoreUnavailable
	}
	blob, err := client.GetDel(ctx, registerKeyPrefix+token).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrStoreUnavailable
	}
	var rc RegisterContext
	if err := json.Unmarshal(blob, &rc); err != nil {
		return nil, nil
	}
	return &rc, nil
}

type RegistrarDeps struct {
	DB          *gorm.DB
	Clients     *store.ClientRepo
	Users       *store.UserRepo
	Identifiers *store.IdentifierRepo
	Challenges  *store.ChallengeRepo
	Policies    *store.LoginPolicyRepo
	Visited     *store.UserVisitedClientRepo
	AuthEvents  *store.AuthEventRepo
	Registry    *factor.Registry
	Blind       *identifier.BlindIndexer
	State       *RegisterStore
}

type Registrar struct {
	db          *gorm.DB
	clients     *store.ClientRepo
	users       *store.UserRepo
	identifiers *store.IdentifierRepo
	challenges  *store.ChallengeRepo
	policies    *store.LoginPolicyRepo
	visited     *store.UserVisitedClientRepo
	authEvents  *store.AuthEventRepo
	registry    *factor.Registry
	blind       *identifier.BlindIndexer
	state       *RegisterStore
}

func NewRegistrar(deps RegistrarDeps) *Registrar {
	return &Registrar{
		db:          deps.DB,
		clients:     deps.Clients,
		users:       deps.Users,
		identifiers: deps.Identifiers,
		challenges:  deps.Challenges,
		policies:    deps.Policies,
		visited:     deps.Visited,
		authEvents:  deps.AuthEvents,
		registry:    deps.Registry,
		blind:       deps.Blind,
		state:       deps.State,
	}
}

type RegisterStartInput struct {
	ClientID   string
	Type       string
	Value      string
	InviteCode string
}

type RegisterStartResult struct {
	RegisterToken string
	ChallengeID   string
	NextAction    string
	TargetMasked  string
	ExpiresAt     time.Time
}

type RegisterCompleteInput struct {
	RegisterToken string
	Code          string
	Password      string
}

type RegisterCompleteResult struct {
	UserID       uuid.UUID
	IdentifierID uuid.UUID
	ClientID     string
	ACR          string
	AMR          []string
	Session      *model.Session
}

func (r *Registrar) providerForIdentifierType(typ string) (factor.Provider, bool) {
	for _, p := range r.registry.All() {
		iv, ok := p.(factor.IdentifierVerifying)
		if ok && iv.VerifiesIdentifierType() == typ {
			return p, true
		}
	}
	return nil, false
}

func (r *Registrar) Start(ctx context.Context, in RegisterStartInput) (*RegisterStartResult, error) {
	if in.ClientID == "" {
		return nil, httpx.New("INVALID_REQUEST", "缺少 client_id")
	}
	client, err := r.clients.GetByID(ctx, in.ClientID)
	if err != nil {
		return nil, err
	}
	if client.Status != "active" {
		return nil, httpx.New("INVALID_CLIENT", "客户端不可用")
	}
	normalized, err := identifier.Normalize(in.Type, in.Value)
	if err != nil {
		return nil, identifier.ErrInvalidFormat
	}
	policy, err := r.policies.GetEffective(ctx, client.ClientID, client.Tier)
	if err != nil {
		return nil, err
	}
	allowed := policy == nil || policy.SelfRegistrationAllowed
	if !allowed {
		return nil, httpx.New("REGISTRATION_NOT_ALLOWED", "该业务系统未开放自助注册")
	}

	rc := &RegisterContext{
		ClientID:   client.ClientID,
		ClientTier: client.Tier,
		Type:       in.Type,
		Value:      normalized,
		ExpiresAt:  time.Now().UTC().Add(RegisterTTL),
	}

	provider, hasProvider := r.providerForIdentifierType(in.Type)
	if hasProvider {
		if !r.registry.Enabled(provider.Type()) || containsString(disabledFactors(policy), provider.Type()) {
			return nil, factor.ErrFactorDisabled
		}
		if _, err := r.identifiers.GetByTypeValue(ctx, in.Type, r.blind.Value(normalized)); err == nil {
			return nil, store.ErrIdentifierAlreadyBound
		} else if !errors.Is(err, store.ErrIdentifierNotFound) {
			return nil, err
		}
		challenger, ok := provider.(ChannelChallenger)
		if !ok {
			return nil, factor.ErrFactorNotSupported
		}
		masked := identifier.Mask(in.Type, normalized)
		ticket, err := challenger.BeginChallengeForTarget(ctx, normalized, masked, client.ClientID, factor.PurposeBind)
		if err != nil {
			return nil, err
		}
		rc.ValueMasked = masked
		rc.ChallengeID = ticket.ChallengeID
		rc.FactorType = string(ticket.FactorType)
		rc.StoreIdent = true
		rc.Username = deriveUsername(in.Type, normalized)
		if namer, ok := provider.(factor.AmrNamer); ok {
			rc.AMR = namer.AMR()
		} else {
			rc.AMR = provider.Type()
		}
		rc.VerifyMethod = string(ticket.FactorType)
		rc.Purpose = string(factor.PurposeBind)
	} else {
		if _, err := r.users.GetByUsername(ctx, normalized); err == nil {
			return nil, store.ErrIdentifierAlreadyBound
		} else if !errors.Is(err, store.ErrUserNotFound) {
			return nil, err
		}
		rc.Username = normalized
	}

	token, err := newOpaqueStateToken()
	if err != nil {
		return nil, err
	}
	rc.Token = token
	if err := r.state.Put(ctx, rc); err != nil {
		return nil, err
	}

	nextAction := string(factor.NextActionInputPassword)
	if rc.ChallengeID != "" {
		nextAction = string(factor.NextActionTypeCode)
	}
	return &RegisterStartResult{
		RegisterToken: token,
		ChallengeID:   rc.ChallengeID,
		NextAction:    nextAction,
		TargetMasked:  rc.ValueMasked,
		ExpiresAt:     rc.ExpiresAt,
	}, nil
}

func (r *Registrar) Complete(ctx context.Context, in RegisterCompleteInput) (*RegisterCompleteResult, error) {
	if in.RegisterToken == "" {
		return nil, httpx.New("INVALID_REQUEST", "缺少 register_token")
	}
	rc, err := r.state.Take(ctx, in.RegisterToken)
	if err != nil {
		return nil, err
	}
	if rc == nil || time.Now().UTC().After(rc.ExpiresAt) {
		return nil, httpx.New("INVALID_REQUEST", "register_token 无效或已过期")
	}
	client, err := r.clients.GetByID(ctx, rc.ClientID)
	if err != nil {
		return nil, err
	}
	if client.Status != "active" {
		return nil, httpx.New("INVALID_CLIENT", "客户端不可用")
	}
	policy, err := r.policies.GetEffective(ctx, client.ClientID, client.Tier)
	if err != nil {
		return nil, err
	}
	if policy != nil && !policy.SelfRegistrationAllowed {
		return nil, httpx.New("REGISTRATION_NOT_ALLOWED", "该业务系统未开放自助注册")
	}

	if rc.ChallengeID != "" {
		provider, ok := r.registry.Get(rc.FactorType)
		if !ok {
			return nil, factor.ErrFactorNotSupported
		}
		authenticatable, ok := provider.(factor.Authenticatable)
		if !ok {
			return nil, factor.ErrNoEligibleFactor
		}
		ticket := factor.Ticket{ChallengeID: rc.ChallengeID, FactorType: rc.FactorType}
		if verifyErr := authenticatable.VerifyResponse(ctx, ticket, in.Code); verifyErr != nil {
			_ = r.recordEvent(ctx, nil, &rc.ClientID, "register", "failure", rc.FactorType)
			return nil, verifyErr
		}
	}

	credPolicy := credential.PolicyFromLoginPolicy(policy)
	cred, err := credential.Hash(in.Password, credPolicy)
	if err != nil {
		return nil, err
	}

	acr := targetACR(client.Tier, policy)
	amr := []string{"pwd"}
	if rc.AMR != "" {
		amr = append(amr, rc.AMR)
	}
	timeouts := sessionTimeouts(policy)
	now := time.Now().UTC()

	var result RegisterCompleteResult
	txErr := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		user := &model.User{Username: rc.Username, Status: "active"}
		if err := store.MapPgError(tx.Create(user).Error); err != nil {
			return err
		}
		uid := user.ID

		var identifierID uuid.UUID
		if rc.StoreIdent {
			enc, encErr := r.blind.Encrypt(rc.Value)
			if encErr != nil {
				return encErr
			}
			verifyMethod := rc.VerifyMethod
			row := &model.Identifier{
				UserID:       uid,
				Type:         rc.Type,
				Value:        r.blind.Value(rc.Value),
				ValueEnc:     enc,
				ValueMasked:  rc.ValueMasked,
				Verified:     true,
				TrustLevel:   2,
				IsPrimary:    true,
				VerifiedAt:   &now,
				VerifyMethod: &verifyMethod,
				Status:       "active",
			}
			if err := store.MapPgError(tx.Create(row).Error); err != nil {
				return err
			}
			identifierID = row.ID
		}

		auth := &model.Authenticator{
			UserID:         uid,
			Type:           passwordAuthenticatorType,
			CredentialData: cred,
			Status:         "active",
		}
		if err := store.MapPgError(tx.Create(auth).Error); err != nil {
			return err
		}

		sess := &model.Session{
			UserID:        uid,
			ClientID:      rc.ClientID,
			ACR:           acr,
			AMR:           pq.StringArray(append([]string{}, amr...)),
			Status:        "active",
			IssuedAt:      now,
			ExpiresAt:     now.Add(timeouts.Absolute),
			IdleExpiresAt: now.Add(timeouts.Idle),
		}
		if err := store.MapPgError(tx.Create(sess).Error); err != nil {
			return err
		}

		event := &model.AuthEvent{
			UserID:     &uid,
			ClientID:   &rc.ClientID,
			EventType:  "register",
			Result:     "success",
			ACR:        &acr,
			AMR:        pq.StringArray(append([]string{}, amr...)),
			OccurredAt: now,
		}
		if identifierID != uuid.Nil {
			event.IdentifierID = &identifierID
		}
		if rc.FactorType != "" {
			factorType := rc.FactorType
			event.FactorType = &factorType
		}
		if err := store.MapPgError(tx.Create(event).Error); err != nil {
			return err
		}

		visited := &model.UserVisitedClient{
			UserID:      uid,
			ClientID:    rc.ClientID,
			VisitCount:  1,
			FirstSeenAt: now,
			LastSeenAt:  now,
		}
		tier := client.Tier
		visited.TierAtVisit = &tier
		if err := store.MapPgError(tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "client_id"}},
			DoNothing: true,
		}).Create(visited).Error); err != nil {
			return err
		}

		result = RegisterCompleteResult{
			UserID:       uid,
			IdentifierID: identifierID,
			ClientID:     rc.ClientID,
			ACR:          acr,
			AMR:          amr,
			Session:      sess,
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
	}
	return &result, nil
}

func (r *Registrar) recordEvent(ctx context.Context, userID *uuid.UUID, clientID *string, eventType, result, factorType string) error {
	event := &model.AuthEvent{
		UserID:     userID,
		ClientID:   clientID,
		EventType:  eventType,
		Result:     result,
		AMR:        pq.StringArray{},
		OccurredAt: time.Now().UTC(),
	}
	if factorType != "" {
		ft := factorType
		event.FactorType = &ft
	}
	return r.authEvents.Create(ctx, event)
}

func deriveUsername(typ, normalized string) string {
	sum := crypto.Sum([]byte(typ + ":" + normalized))
	return "u_" + hex.EncodeToString(sum)[:16]
}

func targetACR(tier string, policy *model.LoginPolicy) string {
	if policy != nil && policy.MinACR != "" {
		return policy.MinACR
	}
	if tier == "L3" {
		return factor.ACRAAL2
	}
	return factor.ACRAAL1
}

func sessionTimeouts(policy *model.LoginPolicy) (out struct {
	Absolute time.Duration
	Idle     time.Duration
}) {
	out.Absolute = 12 * time.Hour
	out.Idle = 30 * time.Minute
	if policy == nil {
		return out
	}
	if policy.SessionAbsoluteTimeoutMin > 0 {
		out.Absolute = time.Duration(policy.SessionAbsoluteTimeoutMin) * time.Minute
	}
	if policy.SessionIdleTimeoutMin > 0 {
		out.Idle = time.Duration(policy.SessionIdleTimeoutMin) * time.Minute
	}
	if out.Idle > out.Absolute {
		out.Idle = out.Absolute
	}
	return out
}

func newOpaqueStateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func disabledFactors(policy *model.LoginPolicy) []string {
	if policy == nil {
		return nil
	}
	return append([]string{}, policy.DisabledFactors...)
}