package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type AuthenticatorRepo struct {
	db *gorm.DB
}

func NewAuthenticatorRepo(db *gorm.DB) *AuthenticatorRepo {
	return &AuthenticatorRepo{db: db}
}

func (r *AuthenticatorRepo) Create(ctx context.Context, authenticator *model.Authenticator) error {
	return MapPgError(r.db.WithContext(ctx).Create(authenticator).Error)
}

func (r *AuthenticatorRepo) GetActiveByUserType(ctx context.Context, userID uuid.UUID, typ string) (*model.Authenticator, error) {
	var row model.Authenticator
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND type = ? AND status = ?", userID, typ, "active").
		Order("created_at DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInvalidCredential
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *AuthenticatorRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Authenticator, error) {
	var row model.Authenticator
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrInvalidCredential
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *AuthenticatorRepo) Update(ctx context.Context, authenticator *model.Authenticator) error {
	return MapPgError(r.db.WithContext(ctx).Save(authenticator).Error)
}

func (r *AuthenticatorRepo) CountActiveByUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&model.Authenticator{}).
		Where("user_id = ? AND status = ?", userID, "active").
		Count(&count).Error
	return count, err
}
