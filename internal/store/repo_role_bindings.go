package store

import (
	"context"
	"errors"

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

func (r *RoleBindingRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.RoleBinding, error) {
	var row model.RoleBinding
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrBindingNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *RoleBindingRepo) List(ctx context.Context, userID *uuid.UUID, clientID, role string) ([]model.RoleBinding, error) {
	query := r.db.WithContext(ctx).Model(&model.RoleBinding{})
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
	}
	if clientID != "" {
		query = query.Where("client_id = ?", clientID)
	}
	if role != "" {
		query = query.Where("role = ?", role)
	}
	var rows []model.RoleBinding
	err := query.Order("granted_at DESC, id DESC").Find(&rows).Error
	return rows, err
}

func (r *RoleBindingRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]model.RoleBinding, error) {
	var rows []model.RoleBinding
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("granted_at DESC, id DESC").
		Find(&rows).Error
	return rows, err
}

func (r *RoleBindingRepo) ActiveGroupedByUser(ctx context.Context, clientID string, userIDs []uuid.UUID) (map[uuid.UUID][]string, error) {
	query := r.db.WithContext(ctx).Model(&model.RoleBinding{}).
		Where("client_id = ? AND status = ?", clientID, "active")
	if len(userIDs) > 0 {
		query = query.Where("user_id IN ?", userIDs)
	}
	var rows []model.RoleBinding
	if err := query.Order("user_id ASC, role ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	grouped := make(map[uuid.UUID][]string)
	for _, row := range rows {
		grouped[row.UserID] = append(grouped[row.UserID], row.Role)
	}
	return grouped, nil
}
