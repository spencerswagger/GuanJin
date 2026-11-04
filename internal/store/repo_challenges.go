package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// ErrChallengeNotFound 表示按 id 没查到挑战（对外 `CHALLENGE_INVALID`）。
var ErrChallengeNotFound = errors.New("store: challenge not found")

// ChallengeRepo 是 `challenges` 的仓储（状态机判定在 `internal/flow`，这里只做存）。
//
// 一次性消费与"计数不越界"都落成**单条条件 UPDATE**：
// 先读再写会在并发下被"两个请求都读到 attempts=4"这类竞态绕过，
// 而条件更新由数据库的行锁保证原子（`WHERE consumed_at IS NULL` / `attempts < max_attempts`）。
type ChallengeRepo struct{ db *gorm.DB }

// NewChallengeRepo 绑定连接。
func NewChallengeRepo(db *gorm.DB) *ChallengeRepo { return &ChallengeRepo{db: db} }

// WithTx 返回绑定事务的副本。
func (r *ChallengeRepo) WithTx(tx *gorm.DB) *ChallengeRepo { return &ChallengeRepo{db: tx} }

// Create 落一条挑战（主键由应用层给，D4-A）。
func (r *ChallengeRepo) Create(ctx context.Context, c *model.Challenge) error {
	if c == nil {
		return errors.New("store: nil challenge")
	}
	if c.Purpose == "" {
		return errors.New("store: challenge requires purpose")
	}
	if c.FactorType == "" {
		return errors.New("store: challenge requires factor_type")
	}
	if len(c.ResponseHash) == 0 {
		// 没有期望应答的挑战没有意义；更重要的是——**绝不允许明文码落库**。
		return errors.New("store: challenge requires response_hash (plaintext codes must never be stored)")
	}
	if c.ExpiresAt.IsZero() {
		return errors.New("store: challenge requires expires_at")
	}
	return MapError(r.db.WithContext(ctx).Clauses(clause.Returning{}).Create(c).Error)
}

// GetByID 读一条挑战（不加锁）。
func (r *ChallengeRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Challenge, error) {
	if id == uuid.Nil {
		return nil, ErrChallengeNotFound
	}
	var c model.Challenge
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrChallengeNotFound
	}
	if err != nil {
		return nil, MapError(err)
	}
	return &c, nil
}

// Consume 一次性消费：`UPDATE … WHERE id = ? AND consumed_at IS NULL`。
//
// 返回 1 = 抢到（本次校验有效）；0 = 已被消费（**调用方必须回 `CHALLENGE_CONSUMED`**）。
func (r *ChallengeRepo) Consume(ctx context.Context, id uuid.UUID, at time.Time) (int64, error) {
	if id == uuid.Nil {
		return 0, ErrChallengeNotFound
	}
	res := r.db.WithContext(ctx).Exec(
		`UPDATE challenges SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL`, at.UTC(), id)
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// RecordAttempt 记一次失败尝试：`attempts = attempts + 1`，且不越过 `max_attempts`。
//
// 返回 (剩余次数, 是否用尽, 错误)：用尽时调用方回 `ATTEMPTS_EXCEEDED`。
// **不清零**：清零是账号级计数在成功登录时做的事（D3-D），不是单挑战的事。
func (r *ChallengeRepo) RecordAttempt(ctx context.Context, id uuid.UUID, now time.Time) (remaining int32, exhausted bool, err error) {
	if id == uuid.Nil {
		return 0, false, ErrChallengeNotFound
	}
	db := r.db.WithContext(ctx)
	res := db.Exec(
		`UPDATE challenges SET attempts = attempts + 1
		  WHERE id = ? AND consumed_at IS NULL AND attempts < max_attempts`, id)
	if res.Error != nil {
		return 0, false, MapError(res.Error)
	}
	var c model.Challenge
	if err := db.Select("attempts", "max_attempts").Where("id = ?", id).Take(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, false, ErrChallengeNotFound
		}
		return 0, false, MapError(err)
	}
	remaining = c.MaxAttempts - c.Attempts
	return remaining, remaining <= 0, nil
}
