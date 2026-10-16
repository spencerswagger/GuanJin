package store

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type RoleBindingRepo struct {
	db *gorm.DB
}

func NewRoleBindingRepo(db *gorm.DB) *RoleBindingRepo {
	return &RoleBindingRepo{db: db}
}

func (r *RoleBindingRepo) ActiveRolesForUser(ctx context.Context, userID uuid.UUID, clientID string) ([]string, error) {
	var roles []string
	err := r.db.WithContext(ctx).Model(&model.RoleBinding{}).
		Where("user_id = ? AND client_id = ? AND status = ?", userID, clientID, "active").
		Distinct().
		Pluck("role", &roles).Error
	return roles, err
}
