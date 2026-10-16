package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type SessionRepo struct {
	db *gorm.DB
}

func NewSessionRepo(db *gorm.DB) *SessionRepo {
	return &SessionRepo{db: db}
}

func (r *SessionRepo) Create(ctx context.Context, session *model.Session) error {
	return MapPgError(r.db.WithContext(ctx).Create(session).Error)
}

func (r *SessionRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Session, error) {
	var row model.Session
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *SessionRepo) Revoke(ctx context.Context, id uuid.UUID, reason string) error {
	now := time.Now().UTC()
	return MapPgError(r.db.WithContext(ctx).Model(&model.Session{}).
		Where("id = ? AND status = ?", id, "active").
		Updates(map[string]any{
			"status":        "revoked",
			"revoked_at":    now,
			"revoke_reason": reason,
		}).Error)
}

func (r *SessionRepo) RevokeAllForUser(ctx context.Context, userID uuid.UUID, reason string) (int64, error) {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&model.Session{}).
		Where("user_id = ? AND status = ?", userID, "active").
		Updates(map[string]any{
			"status":        "revoked",
			"revoked_at":    now,
			"revoke_reason": reason,
		})
	if res.Error != nil {
		return 0, MapPgError(res.Error)
	}
	return res.RowsAffected, nil
}

func (r *SessionRepo) ListActiveByUser(ctx context.Context, userID uuid.UUID) ([]model.Session, error) {
	var rows []model.Session
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND status = ?", userID, "active").
		Order("issued_at DESC").
		Find(&rows).Error
	return rows, err
}
