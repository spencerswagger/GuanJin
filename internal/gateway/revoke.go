package gateway

import (
	"errors"
	"time"

	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

var (
	errSessionRevoked = errors.New("SESSION_REVOKED")
	errSessionExpired = errors.New("TOKEN_EXPIRED")
)

func sessionRejectReason(s *model.Session, now time.Time) error {
	if s == nil {
		return store.ErrSessionNotFound
	}
	switch s.Status {
	case "revoked":
		return errSessionRevoked
	case "expired":
		return errSessionExpired
	case "active":
	default:
		return errSessionExpired
	}
	if !now.Before(s.ExpiresAt) || !now.Before(s.IdleExpiresAt) {
		return errSessionExpired
	}
	return nil
}
