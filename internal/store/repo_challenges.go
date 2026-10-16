package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/store/model"
)

var ErrChallengeNotFound = errors.New("CHALLENGE_INVALID")

type ChallengeRepo struct {
	db *gorm.DB
}

func NewChallengeRepo(db *gorm.DB) *ChallengeRepo {
	return &ChallengeRepo{db: db}
}

func (r *ChallengeRepo) Create(ctx context.Context, challenge *model.Challenge) error {
	return MapPgError(r.db.WithContext(ctx).Create(challenge).Error)
}

func (r *ChallengeRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Challenge, error) {
	var row model.Challenge
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrChallengeNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *ChallengeRepo) IncrementAttempts(ctx context.Context, id uuid.UUID) error {
	return MapPgError(r.db.WithContext(ctx).Model(&model.Challenge{}).
		Where("id = ? AND attempts < max_attempts", id).
		UpdateColumn("attempts", gorm.Expr("attempts + 1")).Error)
}

func (r *ChallengeRepo) Consume(ctx context.Context, id uuid.UUID) (bool, error) {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&model.Challenge{}).
		Where("id = ? AND consumed_at IS NULL", id).
		UpdateColumn("consumed_at", now)
	if res.Error != nil {
		return false, MapPgError(res.Error)
	}
	return res.RowsAffected > 0, nil
}
