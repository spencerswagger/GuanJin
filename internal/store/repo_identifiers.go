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
		Where("user_id = ? AND status = ?", userID, "active").
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

func (r *IdentifierRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Identifier, error) {
	var row model.Identifier
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrIdentifierNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *IdentifierRepo) Update(ctx context.Context, identifier *model.Identifier) error {
	return MapPgError(r.db.WithContext(ctx).Save(identifier).Error)
}

func (r *IdentifierRepo) DeleteByID(ctx context.Context, id uuid.UUID) error {
	return MapPgError(r.db.WithContext(ctx).Where("id = ?", id).Delete(&model.Identifier{}).Error)
}

func (r *IdentifierRepo) Disable(ctx context.Context, id uuid.UUID) error {
	return MapPgError(r.db.WithContext(ctx).Model(&model.Identifier{}).
		Where("id = ?", id).
		Updates(map[string]any{"status": "disabled", "is_primary": false}).Error)
}

func (r *IdentifierRepo) SetPrimary(ctx context.Context, id uuid.UUID, primary bool) error {
	return MapPgError(r.db.WithContext(ctx).Model(&model.Identifier{}).
		Where("id = ?", id).
		Updates(map[string]any{"is_primary": primary}).Error)
}
