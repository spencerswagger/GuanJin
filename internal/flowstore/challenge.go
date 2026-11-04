package flowstore

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// ChallengeAdapter 把 `challenges` 仓储接到编排层的窄接口上（`flow.ChallengeStore`）。
//
// 与 `Adapter` 同一个理由：`internal/flow` 不能用 `store.ErrChallengeNotFound` 做判定
// （它会 import 整个 store），也不能自己判断"没查到"该叫什么码。转换集中在这里：
//   - `store.ErrChallengeNotFound` → `flow.ErrChallengeNotFound`（对外 `CHALLENGE_INVALID`）；
//   - 其余错误原样上抛（DB 不可用等，属 E6，不该被伪装成"挑战不存在"）。
//
// 这一层**不做状态判断**（过期/已消费/次数用尽都在 `internal/flow` 的状态机里）：
// 仓储只回答"这一行长什么样"，以及"条件更新影响了多少行"。
type ChallengeAdapter struct{ repo *store.ChallengeRepo }

// NewChallengeAdapter 绑定仓储；nil 直接报错（装配期 fail-fast）。
func NewChallengeAdapter(repo *store.ChallengeRepo) (*ChallengeAdapter, error) {
	if repo == nil {
		return nil, ErrMissingDependency
	}
	return &ChallengeAdapter{repo: repo}, nil
}

// GetByID 读一条挑战（不加锁；状态判定在编排层）。
func (a *ChallengeAdapter) GetByID(ctx context.Context, id uuid.UUID) (*model.Challenge, error) {
	c, err := a.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrChallengeNotFound) {
			return nil, flow.ErrChallengeNotFound
		}
		return nil, err
	}
	return c, nil
}

// Consume 一次性消费（`UPDATE … WHERE consumed_at IS NULL`）；返回影响行数。
func (a *ChallengeAdapter) Consume(ctx context.Context, id uuid.UUID, at time.Time) (int64, error) {
	rows, err := a.repo.Consume(ctx, id, at)
	if err != nil {
		if errors.Is(err, store.ErrChallengeNotFound) {
			return 0, flow.ErrChallengeNotFound
		}
		return 0, err
	}
	return rows, nil
}

// RecordAttempt 记一次失败尝试（`attempts = attempts + 1`，不越过 `max_attempts`）。
func (a *ChallengeAdapter) RecordAttempt(ctx context.Context, id uuid.UUID, now time.Time) (int32, bool, error) {
	remaining, exhausted, err := a.repo.RecordAttempt(ctx, id, now)
	if err != nil {
		if errors.Is(err, store.ErrChallengeNotFound) {
			return 0, false, flow.ErrChallengeNotFound
		}
		return 0, false, err
	}
	return remaining, exhausted, nil
}

// 编译期断言：适配器必须满足编排层的挑战接口（改名即构建失败）。
var _ flow.ChallengeStore = (*ChallengeAdapter)(nil)
