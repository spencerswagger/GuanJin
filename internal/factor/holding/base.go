package holding

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	DefaultFailThreshold int32 = 5
	DefaultLockDuration        = 15 * time.Minute
)

type Base struct {
	typ           factor.FactorType
	desc          factor.Descriptor
	auths         *store.AuthenticatorRepo
	failThreshold int32
	lockDuration  time.Duration
}

func NewBase(typ factor.FactorType, desc factor.Descriptor, auths *store.AuthenticatorRepo, failThreshold int32, lockDuration time.Duration) *Base {
	if failThreshold <= 0 {
		failThreshold = DefaultFailThreshold
	}
	if lockDuration <= 0 {
		lockDuration = DefaultLockDuration
	}
	return &Base{
		typ:           typ,
		desc:          desc,
		auths:         auths,
		failThreshold: failThreshold,
		lockDuration:  lockDuration,
	}
}

func (b *Base) Type() factor.FactorType { return b.typ }

func (b *Base) Kind() factor.Kind { return factor.KindHolding }

func (b *Base) Descriptor() factor.Descriptor { return b.desc }

func (b *Base) Authenticators() *store.AuthenticatorRepo { return b.auths }

func (b *Base) ActiveAuthenticator(ctx context.Context, userID uuid.UUID) (*model.Authenticator, error) {
	return b.auths.GetActiveByUserType(ctx, userID, b.typ)
}

func (b *Base) IsLocked(auth *model.Authenticator) bool {
	return auth != nil && auth.LockedUntil != nil && time.Now().UTC().Before(*auth.LockedUntil)
}

func (b *Base) RecordFailure(ctx context.Context, auth *model.Authenticator) error {
	auth.FailCount++
	if auth.FailCount >= b.failThreshold {
		until := time.Now().UTC().Add(b.lockDuration)
		auth.LockedUntil = &until
	}
	return b.auths.Update(ctx, auth)
}

func (b *Base) RecordSuccess(ctx context.Context, auth *model.Authenticator) error {
	now := time.Now().UTC()
	auth.FailCount = 0
	auth.LockedUntil = nil
	auth.LastUsedAt = &now
	return b.auths.Update(ctx, auth)
}
