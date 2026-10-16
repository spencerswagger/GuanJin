package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type IdentifierRepo struct {
	db *gorm.DB
}

func NewIdentifierRepo(db *gorm.DB) *IdentifierRepo {
	return &IdentifierRepo{db: db}
}

func (r *IdentifierRepo) Create(ctx context.Context, identifier *model.Identifier) error {
	return MapPgError(r.db.WithContext(ctx).Create(identifier).Error)
}

func (r *IdentifierRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]model.Identifier, error) {
	var rows []model.Identifier
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at ASC").
		Find(&rows).Error
	return rows, err
}

func (r *IdentifierRepo) GetByTypeValue(ctx context.Context, typ string, value []byte) (*model.Identifier, error) {
	var row model.Identifier
	err := r.db.WithContext(ctx).
		Where("type = ? AND value = ?", typ, value).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrIdentifierNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
