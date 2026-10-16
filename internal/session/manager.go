package session

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	DefaultAbsoluteTimeout = 12 * time.Hour
	DefaultIdleTimeout     = 30 * time.Minute
)

type Timeouts struct {
	Absolute time.Duration
	Idle     time.Duration
}

type CreateParams struct {
	UserID    uuid.UUID
	ClientID  string
	ACR       string
	AMR       []string
	IP        string
	UserAgent string
	DeviceID  string
	Timeouts  Timeouts
}

type Manager struct {
	repo *store.SessionRepo
}

func NewManager(repo *store.SessionRepo) *Manager {
	return &Manager{repo: repo}
}

func (m *Manager) Create(ctx context.Context, params CreateParams) (*model.Session, error) {
	absolute := params.Timeouts.Absolute
	if absolute <= 0 {
		absolute = DefaultAbsoluteTimeout
	}
	idle := params.Timeouts.Idle
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	if idle > absolute {
		idle = absolute
	}
	now := time.Now().UTC()
	sess := &model.Session{
		UserID:        params.UserID,
		ClientID:      params.ClientID,
		ACR:           params.ACR,
		AMR:           pq.StringArray(append([]string{}, params.AMR...)),
		Status:        "active",
		IssuedAt:      now,
		ExpiresAt:     now.Add(absolute),
		IdleExpiresAt: now.Add(idle),
	}
	if params.IP != "" {
		sess.IP = &params.IP
	}
	if params.UserAgent != "" {
		sess.UserAgent = &params.UserAgent
	}
	if params.DeviceID != "" {
		sess.DeviceID = &params.DeviceID
	}
	if err := m.repo.Create(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (m *Manager) Get(ctx context.Context, id uuid.UUID) (*model.Session, error) {
	return m.repo.GetByID(ctx, id)
}

func (m *Manager) Revoke(ctx context.Context, id uuid.UUID, reason string) error {
	return m.repo.Revoke(ctx, id, reason)
}

func (m *Manager) RevokeAllForUser(ctx context.Context, userID uuid.UUID, reason string) (int64, error) {
	return m.repo.RevokeAllForUser(ctx, userID, reason)
}

func (m *Manager) ListActiveByUser(ctx context.Context, userID uuid.UUID) ([]model.Session, error) {
	return m.repo.ListActiveByUser(ctx, userID)
}

func IsActive(s *model.Session) bool {
	if s == nil || s.Status != "active" {
		return false
	}
	now := time.Now().UTC()
	return now.Before(s.ExpiresAt) && now.Before(s.IdleExpiresAt)
}
