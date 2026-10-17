package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type AuthEventRepo struct {
	db *gorm.DB
}

func NewAuthEventRepo(db *gorm.DB) *AuthEventRepo {
	return &AuthEventRepo{db: db}
}

func (r *AuthEventRepo) Create(ctx context.Context, event *model.AuthEvent) error {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	if event.AMR == nil {
		event.AMR = pq.StringArray{}
	}
	return MapPgError(r.db.WithContext(ctx).Create(event).Error)
}

func (r *AuthEventRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]model.AuthEvent, error) {
	var rows []model.AuthEvent
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("occurred_at ASC").
		Find(&rows).Error
	return rows, err
}