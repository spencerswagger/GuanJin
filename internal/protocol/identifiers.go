package protocol

import (
	"context"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/flow"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/identifier"
	"github.com/spencerswagger/GuanJin/internal/notify"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const identifierCooldownKeyPrefix = "guanjin:ident:cooldown:"

type channelNamer interface {
	ChannelName() string
}

type identifierView struct {
	ID                  string `json:"id"`
	Type                string `json:"type"`
	ValueMasked         string `json:"value_masked"`
	Verified            bool   `json:"verified"`
	Primary             bool   `json:"primary"`
	CreatedAt           string `json:"created_at"`
	BoundFactor         string `json:"bound_factor"`
	UnbindCooldownUntil string `json:"unbind_cooldown_until,omitempty"`
}

func (s *Server) providerForIdentifierType(typ string) (factor.Provider, bool) {
	for _, p := range s.registry.All() {
		if iv, ok := p.(factor.IdentifierVerifying); ok && iv.VerifiesIdentifierType() == typ {
			return p, true
		}
	}
	return nil, false
}

func (s *Server) targetACRForTier(ctx context.Context, tier string) string {
	if s.policies != nil {
		if policy, err := s.policies.GetEffective(ctx, "", tier); err == nil && policy != nil && policy.MinACR != "" {
			return policy.MinACR
		}
	}
	return s.orch.ResolveTargetACR(tier, "")
}

func (s *Server) requireEffectiveACR(c *gin.Context) error {
	sessionACR := acrFromContext(c)
	target := factor.ACRAAL1
	if s.visited != nil {
		if uid, err := uuid.Parse(userIDFromContext(c)); err == nil {
			if tier, ok, err := s.visited.MaxTierForUser(c.Request.Context(), uid); err == nil && ok {
				target = s.targetACRForTier(c.Request.Context(), tier)
			}
		}
	}
	if !s.orch.Satisfied(sessionACR, target) {
		return httpx.New("ACR_INSUFFICIENT", "认证档位不足")
	}
	return nil
}

func (s *Server) recordAuthEvent(ctx context.Context, event *model.AuthEvent) {
	if s.authEvents == nil {
		return
	}
	_ = s.authEvents.Create(ctx, event)
}

func (s *Server) cooldownKey(typ string, blind []byte) string {
	return identifierCooldownKeyPrefix + typ + ":" + hex.EncodeToString(blind)
}

func (s *Server) cooldownActive(ctx context.Context, typ string, blind []byte) (bool, error) {
	client := s.state.client()
	if client == nil {
		return false, redisUnavailable()
	}
	exists, err := client.Exists(ctx, s.cooldownKey(typ, blind)).Result()
	if err != nil {
		return false, redisUnavailable()
	}
	return exists > 0, nil
}

func (s *Server) setCooldown(ctx context.Context, typ string, blind []byte, hours int) error {
	if hours <= 0 {
		hours = 24
	}
	client := s.state.client()
	if client == nil {
		return redisUnavailable()
	}
	if err := client.Set(ctx, s.cooldownKey(typ, blind), "1", time.Duration(hours)*time.Hour).Err(); err != nil {
		return redisUnavailable()
	}
	return nil
}

func (s *Server) ListIdentifiers(c *gin.Context) {
	uid, err := uuid.Parse(userIDFromContext(c))
	if err != nil {
		httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌主体无效"))
		return
	}
	rows, err := s.identifiers.ListByUser(c.Request.Context(), uid)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	views := make([]identifierView, 0, len(rows))
	for _, row := range rows {
		views = append(views, s.identifierView(c.Request.Context(), row))
	}
	c.JSON(http.StatusOK, gin.H{"items": views})
}

func (s *Server) identifierView(ctx context.Context, row model.Identifier) identifierView {
	view := identifierView{
		ID:          row.ID.String(),
		Type:        row.Type,
		ValueMasked: row.ValueMasked,
		Verified:    row.Verified,
		Primary:     row.IsPrimary,
		CreatedAt:   row.CreatedAt.UTC().Format(time.RFC3339),
	}
	if provider, ok := s.providerForIdentifierType(row.Type); ok {
		view.BoundFactor = provider.Type()
	}
	if cooldownUntil, err := s.state.CooldownUntil(ctx, row.Type, row.Value); err == nil && cooldownUntil != "" {
		view.UnbindCooldownUntil = cooldownUntil
	}
	return view
}

func (s *Server) BindIdentifier(c *gin.Context) {
	ctx := c.Request.Context()
	uid, err := uuid.Parse(userIDFromContext(c))
	if err != nil {
		httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌主体无效"))
		return
	}
	if err := s.requireEffectiveACR(c); err != nil {
		httpx.Error(c, err)
		return
	}
	var req struct {
		Type  string `json:"type" form:"type"`
		Value string `json:"value" form:"value"`
	}
	if err := c.ShouldBind(&req); err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "请求参数无效"))
		return
	}
	normalized, err := identifier.Normalize(req.Type, req.Value)
	if err != nil {
		httpx.Error(c, identifier.ErrInvalidFormat)
		return
	}
	provider, ok := s.providerForIdentifierType(req.Type)
	if !ok {
		httpx.Error(c, factor.ErrFactorNotSupported)
		return
	}
	if !s.registry.Enabled(provider.Type()) {
		httpx.Error(c, factor.ErrFactorDisabled)
		return
	}
	blind := s.blind.Value(normalized)
	if _, err := s.identifiers.GetByTypeValue(ctx, req.Type, blind); err == nil {
		httpx.Error(c, store.ErrIdentifierAlreadyBound)
		return
	} else if err != nil && err != store.ErrIdentifierNotFound {
		httpx.Error(c, err)
		return
	}
	active, err := s.cooldownActive(ctx, req.Type, blind)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if active {
		httpx.Error(c, httpx.New("COOLDOWN_ACTIVE", "冷却期内不可绑定该标识符"))
		return
	}
	challenger, ok := provider.(flow.ChannelChallenger)
	if !ok {
		httpx.Error(c, factor.ErrFactorNotSupported)
		return
	}
	enc, err := s.blind.Encrypt(normalized)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	masked := identifier.Mask(req.Type, normalized)
	ticket, err := challenger.BeginChallengeForTarget(ctx, normalized, masked, "", factor.PurposeBind)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	row := &model.Identifier{
		UserID:      uid,
		Type:        req.Type,
		Value:       blind,
		ValueEnc:    enc,
		ValueMasked: masked,
		Verified:    false,
		TrustLevel:  0,
		IsPrimary:   false,
		Status:      "active",
	}
	if err := s.identifiers.Create(ctx, row); err != nil {
		httpx.Error(c, err)
		return
	}
	factorType := provider.Type()
	s.recordAuthEvent(ctx, &model.AuthEvent{
		UserID:       &uid,
		IdentifierID: &row.ID,
		EventType:    "bind",
		Result:       "success",
		FactorType:   &factorType,
		OccurredAt:   time.Now().UTC(),
	})
	c.JSON(http.StatusOK, gin.H{
		"id":            row.ID.String(),
		"verified":      false,
		"challenge_id":  ticket.ChallengeID,
		"target_masked": masked,
	})
}

func (s *Server) VerifyIdentifier(c *gin.Context) {
	ctx := c.Request.Context()
	uid, err := uuid.Parse(userIDFromContext(c))
	if err != nil {
		httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌主体无效"))
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, store.ErrIdentifierNotFound)
		return
	}
	row, err := s.identifiers.GetByID(ctx, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if row.UserID != uid {
		httpx.Error(c, httpx.New("IDENTIFIER_NOT_OWNED", "该标识符不属于当前账号"))
		return
	}
	var req struct {
		ChallengeID string `json:"challenge_id" form:"challenge_id"`
		Code        string `json:"code" form:"code"`
	}
	if err := c.ShouldBind(&req); err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "请求参数无效"))
		return
	}
	provider, ok := s.providerForIdentifierType(row.Type)
	if !ok {
		httpx.Error(c, factor.ErrFactorNotSupported)
		return
	}
	authenticatable, ok := provider.(factor.Authenticatable)
	if !ok {
		httpx.Error(c, factor.ErrNoEligibleFactor)
		return
	}
	ticket := factor.Ticket{ChallengeID: req.ChallengeID, FactorType: provider.Type()}
	if err := authenticatable.VerifyResponse(ctx, ticket, req.Code); err != nil {
		httpx.Error(c, err)
		return
	}
	now := time.Now().UTC()
	method := string(provider.Type())
	row.Verified = true
	row.TrustLevel = 2
	row.VerifiedAt = &now
	row.VerifyMethod = &method
	if err := s.identifiers.Update(ctx, row); err != nil {
		httpx.Error(c, err)
		return
	}
	factorType := string(provider.Type())
	s.recordAuthEvent(ctx, &model.AuthEvent{
		UserID:       &uid,
		IdentifierID: &row.ID,
		EventType:    "verify",
		Result:       "success",
		FactorType:   &factorType,
		OccurredAt:   now,
	})
	c.JSON(http.StatusOK, gin.H{"verified": true, "value_masked": row.ValueMasked})
}

func (s *Server) UnbindIdentifier(c *gin.Context) {
	ctx := c.Request.Context()
	uid, err := uuid.Parse(userIDFromContext(c))
	if err != nil {
		httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌主体无效"))
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, store.ErrIdentifierNotFound)
		return
	}
	row, err := s.identifiers.GetByID(ctx, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if row.UserID != uid {
		httpx.Error(c, httpx.New("IDENTIFIER_NOT_OWNED", "该标识符不属于当前账号"))
		return
	}
	if err := s.requireEffectiveACR(c); err != nil {
		httpx.Error(c, err)
		return
	}
	active, err := s.cooldownActive(ctx, row.Type, row.Value)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if active {
		httpx.Error(c, httpx.New("COOLDOWN_ACTIVE", "冷却期内不可解绑该标识符"))
		return
	}

	rows, err := s.identifiers.ListByUser(ctx, uid)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	total := len(rows)
	verifiedTotal := 0
	verifiedAuthAfter := 0
	for _, other := range rows {
		if other.Verified {
			verifiedTotal++
		}
		if other.ID == row.ID {
			continue
		}
		if other.Verified && s.isAuthIdentifierType(other.Type) {
			verifiedAuthAfter++
		}
	}
	verifiedAfter := verifiedTotal
	if row.Verified {
		verifiedAfter--
	}
	if err := identifier.CheckUnbind(identifier.UnbindCounts{TotalAfter: total - 1, VerifiedAfter: verifiedAfter}); err != nil {
		httpx.Error(c, err)
		return
	}
	holdingActive, err := s.authenticators.CountActiveByUser(ctx, uid)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := identifier.CheckUsableCapability(int(holdingActive), verifiedAuthAfter); err != nil {
		httpx.Error(c, err)
		return
	}

	recipients := s.notificationRecipients(ctx, rows)

	wasPrimary := row.IsPrimary
	if err := s.identifiers.Disable(ctx, row.ID); err != nil {
		httpx.Error(c, err)
		return
	}
	if wasPrimary {
		for _, other := range rows {
			if other.ID == row.ID {
				continue
			}
			_ = s.identifiers.SetPrimary(ctx, other.ID, true)
			break
		}
	}
	hours := 24
	if provider, ok := s.providerForIdentifierType(row.Type); ok {
		if revocable, ok := provider.(factor.Revocable); ok {
			if policy := revocable.RevocationPolicy(); policy.CooldownHours > 0 {
				hours = policy.CooldownHours
			}
		}
	}
	if err := s.setCooldown(ctx, row.Type, row.Value, hours); err != nil {
		httpx.Error(c, err)
		return
	}

	for _, detail := range recipients {
		_ = s.notifier.Notify(ctx, uid.String(), "identifier_unbind", detail)
	}
	s.recordAuthEvent(ctx, &model.AuthEvent{
		UserID:       &uid,
		IdentifierID: &row.ID,
		EventType:    "unbind",
		Result:       "success",
		OccurredAt:   time.Now().UTC(),
	})
	c.Status(http.StatusNoContent)
}

func (s *Server) isAuthIdentifierType(typ string) bool {
	_, ok := s.providerForIdentifierType(typ)
	return ok
}

func (s *Server) notificationRecipients(ctx context.Context, rows []model.Identifier) []notify.Detail {
	out := make([]notify.Detail, 0, len(rows))
	for _, row := range rows {
		if !row.Verified || !s.isAuthIdentifierType(row.Type) {
			continue
		}
		provider, ok := s.providerForIdentifierType(row.Type)
		if !ok {
			continue
		}
		namer, ok := provider.(channelNamer)
		if !ok {
			continue
		}
		target, err := s.blind.Decrypt(row.ValueEnc)
		if err != nil {
			continue
		}
		out = append(out, notify.Detail{
			Target:       target,
			TargetMasked: row.ValueMasked,
			Channel:      namer.ChannelName(),
			Message:      notify.NoticeBody("identifier_unbind"),
		})
	}
	return out
}
