package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 集成测试（**需要真 PG**）：没有 `GUANJIN_TEST_DSN` 就跳过。
//
// 为什么用 DSN 门控而不是 testcontainers：本切片施工环境没有 docker/PG 进程，
// testcontainers 基架在这里**无法被验证**（写了也没跑过，等于交一段没证据的代码）。
// 先用 DSN 门控把断言写实，等有能跑容器/库的环境再换成 testcontainers 只改连接建立部分。
//
// 前置：目标库已按 migrations 手动迁移（P1：迁移不随应用启动，也不由测试代跑）。
// 命令：`make migrate-up DATABASE_URL="$GUANJIN_TEST_DSN"`。
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := firstNonEmpty(os.Getenv("GUANJIN_TEST_DSN"), os.Getenv("GUANJIN_DEV_DSN"))
	if dsn == "" {
		t.Skip("GUANJIN_TEST_DSN 未设置：跳过需要真 PostgreSQL 的集成测试")
	}
	ctx := context.Background()
	db, err := Open(ctx, Options{DSN: dsn})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := Ping(ctx, db); err != nil {
		t.Fatalf("ping test db: %v", err)
	}
	if !db.Migrator().HasTable("flows") {
		t.Fatalf("测试库缺少 flows 表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN")
	}
	t.Cleanup(func() { _ = Close(db) })
	return db
}

// fixture 建一组隔离数据（唯一 client_id / user 名），测试结束按 id 清理。
type fixture struct {
	userID   uuid.UUID
	clientID string
	userRow  uuid.UUID
}

func newFixture(t *testing.T, db *gorm.DB) *fixture {
	t.Helper()
	suffix := uuid.NewString()
	f := &fixture{
		userID:   mustV7(t),
		clientID: "s3-test-" + suffix,
	}
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier) VALUES (?, ?, 'L2')`,
		f.clientID, "S3 test client").Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		f.userID, "s3-"+suffix[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM flows WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, f.userID).Error
	})
	return f
}

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	return id
}

func (f *fixture) newFlow(t *testing.T) *model.Flow {
	t.Helper()
	return &model.Flow{
		ClientID:    f.clientID,
		TargetACR:   "AAL1",
		AchievedAMR: model.StringArray{},
		Status:      "active",
		ExpiresAt:   time.Now().UTC().Add(10 * time.Minute),
	}
}

// 23505 命中 `flows_one_active_per_user_client_uniq` → 映射 409（AC-FLOW-11②）。
func TestDBFlowActiveUniqueMapsTo409(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewFlowRepo(db)
	ctx := context.Background()

	first := f.newFlow(t)
	first.UserID = &f.userID
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("create first flow: %v", err)
	}
	second := f.newFlow(t)
	second.UserID = &f.userID
	err := repo.Create(ctx, second)
	if err == nil {
		t.Fatal("second active flow for the same (user, client) must be rejected")
	}
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("err = %T, want *store.Error", err)
	}
	if se.Constraint != "flows_one_active_per_user_client_uniq" {
		t.Fatalf("constraint = %q", se.Constraint)
	}
	if se.Code != codeCooldownActive || se.Status != 409 {
		t.Fatalf("code/status = %s/%d, want COOLDOWN_ACTIVE/409", se.Code, se.Status)
	}

	// 匿名流（user_id NULL）不受该部分唯一索引限制。
	anon1, anon2 := f.newFlow(t), f.newFlow(t)
	if err := repo.Create(ctx, anon1); err != nil {
		t.Fatalf("create anon flow: %v", err)
	}
	if err := repo.Create(ctx, anon2); err != nil {
		t.Fatalf("second anon flow must be allowed: %v", err)
	}
}

// 23514 命中 `flows_completed_requires_user` → 400（AC-FLOW-11①），不是 500。
func TestDBFlowCompletedRequiresUserMapsTo400(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewFlowRepo(db)

	flow := f.newFlow(t)
	flow.Status = "completed"
	consumed := time.Now().UTC()
	flow.ConsumedAt = &consumed
	err := repo.Create(context.Background(), flow) // user_id 仍为 NULL
	if err == nil {
		t.Fatal("completed flow without user_id must be rejected by the DB check")
	}
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("err = %T, want *store.Error", err)
	}
	if se.Constraint != "flows_completed_requires_user" {
		t.Fatalf("constraint = %q", se.Constraint)
	}
	if se.Code != codeInvalidParameter || se.Status != 400 {
		t.Fatalf("code/status = %s/%d, want INVALID_PARAMETER/400", se.Code, se.Status)
	}
}

// LockByID 真的会阻塞并发事务，且后到者读到前者的写入（AC-FLOW-11③ 的最小证据）。
func TestDBLockByIDSerializesWriters(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewFlowRepo(db)
	ctx := context.Background()

	flow := f.newFlow(t)
	flow.UserID = &f.userID
	if err := repo.Create(ctx, flow); err != nil {
		t.Fatalf("create flow: %v", err)
	}

	var (
		secondAcquired atomic.Bool
		secondAMR      model.StringArray
		wg             sync.WaitGroup
		secondErr      error
	)
	wg.Add(2)

	// 第一个事务：锁住行 → 等 200ms → 写入 amr → 提交。
	go func() {
		defer wg.Done()
		err := Tx(ctx, db, func(tx *gorm.DB) error {
			locked, err := repo.LockByID(ctx, tx, flow.ID)
			if err != nil {
				return err
			}
			amr := append(model.StringArray{}, locked.AchievedAMR...)
			amr = append(amr, "pwd")
			time.Sleep(200 * time.Millisecond) // 持锁期间做"重活"的反面例子，只为制造重叠
			_, err = NewFlowRepo(tx).Apply(ctx, tx, flow.ID, FlowPatch{AchievedAMR: &amr})
			return err
		})
		if err != nil {
			t.Errorf("first tx: %v", err)
		}
	}()

	// 第二个事务：稍后开始，LockByID 必须等第一个提交后才返回，然后看到 pwd。
	go func() {
		defer wg.Done()
		time.Sleep(50 * time.Millisecond)
		secondErr = Tx(ctx, db, func(tx *gorm.DB) error {
			locked, err := repo.LockByID(ctx, tx, flow.ID)
			if err != nil {
				return err
			}
			secondAcquired.Store(true)
			secondAMR = locked.AchievedAMR
			return nil
		})
	}()

	wg.Wait()
	if secondErr != nil {
		t.Fatalf("second tx: %v", secondErr)
	}
	if !secondAcquired.Load() {
		t.Fatal("second tx never acquired the lock")
	}
	if len(secondAMR) != 1 || secondAMR[0] != "pwd" {
		t.Fatalf("second tx must observe the first tx's committed amr, got %v", secondAMR)
	}
}

// 两段式清理：过期行先保留（能稳定返回 FLOW_EXPIRED），7 天后才被删（I-F6）。
func TestDBTwoPhaseCleanup(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewFlowRepo(db)
	ctx := context.Background()

	expired := f.newFlow(t)
	expired.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if err := repo.Create(ctx, expired); err != nil {
		t.Fatalf("create expired flow: %v", err)
	}

	n, err := repo.MarkExpired(ctx)
	if err != nil {
		t.Fatalf("MarkExpired: %v", err)
	}
	if n < 1 {
		t.Fatalf("MarkExpired rows = %d, want >= 1", n)
	}

	// 行还在（第一阶段不删）。
	var count int64
	if err := db.Raw(`SELECT count(*) FROM flows WHERE id = ? AND status = 'expired'`, expired.ID).
		Scan(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expired row must still exist after phase 1, got count=%d", count)
	}

	// 第二阶段：cutoff 未到 → 不删。
	deleted, err := repo.DeleteExpiredBefore(ctx, time.Now().UTC().Add(-ExpiredRetention))
	if err != nil {
		t.Fatalf("DeleteExpiredBefore(now-7d): %v", err)
	}
	if deleted != 0 {
		t.Fatalf("must not delete rows newer than the retention window, deleted=%d", deleted)
	}

	// cutoff 已过（假装 8 天前就过期了）→ 删掉。
	if err := db.Exec(`UPDATE flows SET updated_at = now() - interval '8 days' WHERE id = ?`, expired.ID).
		Error; err != nil {
		t.Fatalf("age the row: %v", err)
	}
	if _, err := repo.DeleteExpiredBefore(ctx, time.Now().UTC().Add(-ExpiredRetention)); err != nil {
		t.Fatalf("DeleteExpiredBefore: %v", err)
	}
	if err := db.Raw(`SELECT count(*) FROM flows WHERE id = ?`, expired.ID).Scan(&count).Error; err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("expired row past retention must be deleted, got count=%d", count)
	}
}

// 会话撤销 → 该会话下的 active 流置 abandoned（I-F6⑤）。
func TestDBAbandonBySession(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewFlowRepo(db)
	ctx := context.Background()

	sessionID := mustV7(t)
	flow := f.newFlow(t)
	flow.UserID = &f.userID
	flow.SessionID = &sessionID
	if err := repo.Create(ctx, flow); err != nil {
		t.Fatalf("create flow: %v", err)
	}

	n, err := repo.AbandonBySession(ctx, sessionID)
	if err != nil {
		t.Fatalf("AbandonBySession: %v", err)
	}
	if n != 1 {
		t.Fatalf("abandoned rows = %d, want 1", n)
	}
	got, err := repo.GetByID(ctx, flow.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != "abandoned" {
		t.Fatalf("status = %q, want abandoned", got.Status)
	}
}

// runtime_config：seed 的 flow.ttl_seconds 能读出来；删掉键 → fail-fast（D5）。
func TestDBRuntimeConfigFlowTTL(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	repo := NewRuntimeConfigRepo(db)

	ttl, err := LoadFlowTTL(ctx, repo)
	if err != nil {
		t.Fatalf("LoadFlowTTL: %v（迁移 000015 应已 seed flow.ttl_seconds）", err)
	}
	if ttl <= 0 {
		t.Fatalf("ttl = %v, want > 0", ttl)
	}

	// 备份 → 删除 → 断言 fail-fast → 还原（不破坏共享测试库）。
	var backup struct {
		Value     []byte
		ValueType string
	}
	if err := db.Raw(`SELECT value, value_type FROM runtime_config WHERE key = ?`, KeyFlowTTLSeconds).
		Scan(&backup).Error; err != nil {
		t.Fatalf("backup: %v", err)
	}
	if err := db.Exec(`DELETE FROM runtime_config WHERE key = ?`, KeyFlowTTLSeconds).Error; err != nil {
		t.Fatalf("delete key: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`INSERT INTO runtime_config (key, value, value_type) VALUES (?, ?, ?)
			ON CONFLICT (key) DO NOTHING`, KeyFlowTTLSeconds, string(backup.Value), backup.ValueType).Error
	})

	if _, err := LoadFlowTTL(ctx, repo); !errors.Is(err, ErrConfigKeyMissing) {
		t.Fatalf("err = %v, want ErrConfigKeyMissing (fail-fast)", err)
	}
}

// 模型主键由应用层生成（D4-A）：插入后 id 非空、且是 v7。
func TestDBModelGeneratesUUIDv7(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	flow := f.newFlow(t)
	if err := NewFlowRepo(db).Create(context.Background(), flow); err != nil {
		t.Fatalf("create: %v", err)
	}
	if flow.ID == uuid.Nil {
		t.Fatal("id must be assigned by BeforeCreate")
	}
	if flow.ID.Version() != 7 {
		t.Fatalf("id version = %d, want 7", flow.ID.Version())
	}
	// RETURNING 必须把库生成的时间戳带回来（模型里这两个字段只读）。
	if flow.CreatedAt.IsZero() || flow.UpdatedAt.IsZero() {
		t.Fatalf("created_at/updated_at must be read back from the DB, got %+v", flow.BaseModel)
	}
}
