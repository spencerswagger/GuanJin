package store

import (
	"context"

	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type LoginPolicyRepo struct {
	db *gorm.DB
}

func NewLoginPolicyRepo(db *gorm.DB) *LoginPolicyRepo {
	return &LoginPolicyRepo{db: db}
}

func (r *LoginPolicyRepo) GetEffective(ctx context.Context, clientID, tier string) (*model.LoginPolicy, error) {
	var override model.LoginPolicy
	res := r.db.WithContext(ctx).
		Where("scope = ? AND client_id = ?", "client_override", clientID).
		Limit(1).
		Find(&override)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected > 0 {
		return &override, nil
	}

	var def model.LoginPolicy
	res = r.db.WithContext(ctx).
		Where("scope = ? AND tier = ?", "tier_default", tier).
		Limit(1).
		Find(&def)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected > 0 {
		return &def, nil
	}
	return nil, nil
}
