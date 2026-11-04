//go:build integration

package flowstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 挑战适配层（`challenge.go`）的**真库**用例。
//
// 为什么在本包、且必须真库：覆盖率按测试二进制统计，只有 `package flowstore` 的用例
// 才会给 `challenge.go` 记语句；而"没查到 → `flow.ErrChallengeNotFound`"、
// "条件更新的影响行数"这两件事只有真表能证 —— 桩掉 DB 就是在断言自己写的桩。
//
// 断言口径（D3-D + D5）：状态判定在 `internal/flow`，本层只负责
// ①读那一行；②把"没查到"翻成编排层的哨兵；③如实回影响行数（0 行 = 被别人抢先消费）。

func newChallengeAdapter(t *testing.T, db *gorm.DB) (*ChallengeAdapter, *store.ChallengeRepo) {
	t.Helper()
	repo := store.NewChallengeRepo(db)
	a, err := NewChallengeAdapter(repo)
	if err != nil {
		t.Fatalf("NewChallengeAdapter: %v", err)
	}
	return a, repo
}

// 建一条真挑战行（`challenges.response_hash` NOT NULL：明文码永不落库，这里给的是摘要）。
func createChallenge(t *testing.T, db *gorm.DB, repo *store.ChallengeRepo, maxAttempts int32) *model.Challenge {
	t.Helper()
	row := &model.Challenge{
		UserID:       nil,
		Purpose:      "login",
		FactorType:   "password",
		ResponseHash: []byte{0x01, 0x02},
		MaxAttempts:  maxAttempts,
		ExpiresAt:    time.Now().UTC().Add(5 * time.Minute),
	}
	row.ID = mustV7(t)
	if err := repo.Create(context.Background(), row); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM challenges WHERE id = ?`, row.ID).Error })
	return row
}

// 缺依赖当场失败（装配期 fail-fast）。
func TestNewChallengeAdapterRequiresRepo(t *testing.T) {
	if _, err := NewChallengeAdapter(nil); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("nil 仓储应报 ErrMissingDependency，实际 %v", err)
	}
}

// 读一条挑战：查到返回行，"查不到"必须是编排层的哨兵（否则上层只能当 500）。
func TestDBChallengeAdapterGetByID(t *testing.T) {
	db := openTestDB(t)
	adapter, repo := newChallengeAdapter(t, db)
	row := createChallenge(t, db, repo, 3)

	got, err := adapter.GetByID(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Purpose != "login" || got.FactorType != "password" || got.MaxAttempts != 3 {
		t.Fatalf("读回的行不符：%+v", got)
	}

	if _, err := adapter.GetByID(context.Background(), uuid.New()); !errors.Is(err, flow.ErrChallengeNotFound) {
		t.Fatalf("不存在的挑战应报 flow.ErrChallengeNotFound，实际 %v", err)
	}
}

// 一次性消费：第一次 1 行、第二次 0 行（并发下"两个请求都算成功"必须被 DB 拦住）。
func TestDBChallengeAdapterConsumeIsOneShot(t *testing.T) {
	db := openTestDB(t)
	adapter, repo := newChallengeAdapter(t, db)
	row := createChallenge(t, db, repo, 3)
	// 消费时刻必须**晚于**建行时刻：`challenges_consumed_after_created` 是 DB CHECK，
	// 用 Go 侧 `time.Now()` 抢在 `created_at` 之前会被 23514 拒（这不是实现错，是夹具错）。
	now := time.Now().UTC().Add(time.Second)

	rows, err := adapter.Consume(context.Background(), row.ID, now)
	if err != nil || rows != 1 {
		t.Fatalf("首次消费应影响 1 行，实际 rows=%d err=%v", rows, err)
	}
	rows, err = adapter.Consume(context.Background(), row.ID, now)
	if err != nil {
		t.Fatalf("二次消费不该报错（影响 0 行即「已被消费」），实际 %v", err)
	}
	if rows != 0 {
		t.Fatalf("二次消费必须影响 0 行，实际 %d", rows)
	}
}

// 次数扣减：`attempts` 只增、不越过 `max_attempts`，用尽时 `exhausted=true`。
func TestDBChallengeAdapterRecordAttempt(t *testing.T) {
	db := openTestDB(t)
	adapter, repo := newChallengeAdapter(t, db)
	row := createChallenge(t, db, repo, 2)
	now := time.Now().UTC().Add(time.Second)

	remaining, exhausted, err := adapter.RecordAttempt(context.Background(), row.ID, now)
	if err != nil || exhausted {
		t.Fatalf("第一次失败不该用尽：remaining=%d exhausted=%v err=%v", remaining, exhausted, err)
	}
	remaining, exhausted, err = adapter.RecordAttempt(context.Background(), row.ID, now)
	if err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if !exhausted || remaining != 0 {
		t.Fatalf("最后一次失败应判用尽，实际 remaining=%d exhausted=%v", remaining, exhausted)
	}

	// 已消费的挑战不再扣次数（否则"用过的码"还能继续消耗用户配额）。
	consumed := createChallenge(t, db, repo, 2)
	if _, err := adapter.Consume(context.Background(), consumed.ID, now); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if _, exhausted, err := adapter.RecordAttempt(context.Background(), consumed.ID, now); err != nil || exhausted {
		t.Fatalf("已消费的挑战不该扣次数，实际 exhausted=%v err=%v", exhausted, err)
	}

	if _, _, err := adapter.RecordAttempt(context.Background(), uuid.New(), now); !errors.Is(err, flow.ErrChallengeNotFound) {
		t.Fatalf("不存在的挑战应报 flow.ErrChallengeNotFound，实际 %v", err)
	}
}
