package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/store/model"
)

type PolicySnapshotRepo struct {
	db *gorm.DB
}

func NewPolicySnapshotRepo(db *gorm.DB) *PolicySnapshotRepo {
	return &PolicySnapshotRepo{db: db}
}

func (r *PolicySnapshotRepo) Create(ctx context.Context, snapshot *model.PolicySnapshot) error {
	return MapPgError(r.db.WithContext(ctx).Create(snapshot).Error)
}

func (r *PolicySnapshotRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.PolicySnapshot, error) {
	var row model.PolicySnapshot
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSnapshotNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *PolicySnapshotRepo) LatestByClient(ctx context.Context, clientID string) (*model.PolicySnapshot, error) {
	var row model.PolicySnapshot
	err := r.db.WithContext(ctx).
		Where("client_id = ?", clientID).
		Order("version DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSnapshotNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *PolicySnapshotRepo) MaxVersionByClient(ctx context.Context, clientID string) (int32, error) {
	var version int32
	err := r.db.WithContext(ctx).Model(&model.PolicySnapshot{}).
		Where("client_id = ?", clientID).
		Select("COALESCE(MAX(version), 0)").
		Scan(&version).Error
	return version, err
}

func (r *PolicySnapshotRepo) List(ctx context.Context, page, pageSize int) ([]model.PolicySnapshot, int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Model(&model.PolicySnapshot{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.PolicySnapshot
	err := r.db.WithContext(ctx).
		Order("reported_at DESC, id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rows).Error
	return rows, total, err
}

func (r *PolicySnapshotRepo) ListByClient(ctx context.Context, clientID string) ([]model.PolicySnapshot, error) {
	var rows []model.PolicySnapshot
	err := r.db.WithContext(ctx).
		Where("client_id = ?", clientID).
		Order("version DESC").
		Find(&rows).Error
	return rows, err
}
