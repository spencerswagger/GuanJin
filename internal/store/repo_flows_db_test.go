//go:build integration

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

	"github.com/spencerswagger/guanjin/internal/testdb"

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

// newSession 建一条真实会话（flows.session_id 有 FK 指向 sessions，
// 用随机 uuid 当会话 id 会撞 flows_session_id_fkey → SESSION_NOT_FOUND（404），
// 这也是外键双键映射在真库上的正面证据）。
func (f *fixture) newSession(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	id := mustV7(t)
	err := MapError(db.Exec(`INSERT INTO sessions (id, user_id, client_id, acr, expires_at, idle_expires_at)
		VALUES (?, ?, ?, 'AAL1', now() + interval '1 hour', now() + interval '30 min')`,
		id, f.userID, f.clientID).Error)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM sessions WHERE id = ?`, id).Error })
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

// 23505 命中 `flows_one_active_per_user_client_uniq` → 映射 409（AC-FLOW-11②），
// 并验 CH-7⑤ 的前置条件：正常路径「先查后建」能拿到同一条活跃流。
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

	// CH-7⑤：先查后建 —— 已有活跃流时正常路径应直接复用它（不是撞 409 让客户端等）。
	existing, err := repo.FindActiveByUserClient(ctx, f.userID, f.clientID)
	if err != nil {
		t.Fatalf("先查后建必须能查到已有活跃流: %v", err)
	}
	if existing.ID != first.ID {
		t.Fatalf("先查后建应返回同一条流，got %s want %s", existing.ID, first.ID)
	}

	second := f.newFlow(t)
	second.UserID = &f.userID
	// 绕过"先查后建"直接插入 = 模拟并发竞态，验证 23505 兜底映射（CH-7⑤）。
	err = repo.Create(ctx, second)
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

	// 两条过期流：一条验"触发器把时间戳改回 now()"，另一条验"满 7 天被删"。
	// 分开是必须的 —— 触发器会用 now() 覆盖 updated_at，做完触发器校验那条就"变新"了，
	// 再拿它验删除会永远删不掉。
	triggerProbe := f.newFlow(t)
	triggerProbe.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	doomed := f.newFlow(t)
	doomed.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	for _, fl := range []*model.Flow{triggerProbe, doomed} {
		if err := repo.Create(ctx, fl); err != nil {
			t.Fatalf("create expired flow: %v", err)
		}
	}

	n, err := repo.MarkExpired(ctx)
	if err != nil {
		t.Fatalf("MarkExpired: %v", err)
	}
	if n < 2 {
		t.Fatalf("MarkExpired rows = %d, want >= 2", n)
	}

	// 第一阶段只置 status，行必须还在（否则 FLOW_EXPIRED 会时隐时现，AC-FLOW-10③）。
	var count int64
	if err := db.Raw(`SELECT count(*) FROM flows WHERE id IN (?, ?) AND status = 'expired'`,
		triggerProbe.ID, doomed.ID).Scan(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("expired rows must survive phase 1, got count=%d", count)
	}

	// 第二阶段：cutoff 未到 → 一条都不删。
	deleted, err := repo.DeleteExpiredBefore(ctx, time.Now().UTC().Add(-ExpiredRetention))
	if err != nil {
		t.Fatalf("DeleteExpiredBefore(now-7d): %v", err)
	}
	if deleted != 0 {
		t.Fatalf("must not delete rows newer than the retention window, deleted=%d", deleted)
	}

	// 触发器语义：普通 UPDATE 之后 updated_at 必须是 now()（时间戳归 DB，应用写不进去）。
	if err := db.Exec(`UPDATE flows SET status = 'expired' WHERE id = ?`, triggerProbe.ID).Error; err != nil {
		t.Fatalf("touch row: %v", err)
	}
	var stale bool
	if err := db.Raw(`SELECT updated_at < now() - interval '1 hour' FROM flows WHERE id = ?`,
		triggerProbe.ID).Scan(&stale).Error; err != nil {
		t.Fatalf("check trigger: %v", err)
	}
	if stale {
		t.Fatal("flows_set_updated_at 没生效：普通 UPDATE 后 updated_at 仍是旧值")
	}

	// 造"8 天前就过期"：只能临时停触发器（它会把 updated_at 改回 now()），改完立刻恢复。
	// 这不是绕过约束，是为了把"时间流逝"这个外部条件造出来；生产里 updated_at 天然由 DB 维护。
	// **安全护栏**：改表触发器是删改类动作，必须确认是测试库。
	testdb.RequireTestDB(t, db)
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`ALTER TABLE flows DISABLE TRIGGER flows_set_updated_at`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE flows SET updated_at = now() - interval '8 days' WHERE id = ?`,
			doomed.ID).Error; err != nil {
			return err
		}
		return tx.Exec(`ALTER TABLE flows ENABLE TRIGGER flows_set_updated_at`).Error
	}); err != nil {
		t.Fatalf("disable trigger: %v", err)
	}

	// 到期的删掉、没到期的那条（刚被触发器刷新过）留着。
	if _, err := repo.DeleteExpiredBefore(ctx, time.Now().UTC().Add(-ExpiredRetention)); err != nil {
		t.Fatalf("DeleteExpiredBefore: %v", err)
	}
	var doomedCount, probeCount int64
	if err := db.Raw(`SELECT count(*) FROM flows WHERE id = ?`, doomed.ID).Scan(&doomedCount).Error; err != nil {
		t.Fatalf("count doomed: %v", err)
	}
	if err := db.Raw(`SELECT count(*) FROM flows WHERE id = ?`, triggerProbe.ID).Scan(&probeCount).Error; err != nil {
		t.Fatalf("count probe: %v", err)
	}
	if doomedCount != 0 {
		t.Fatalf("expired row past retention must be deleted, got count=%d", doomedCount)
	}
	if probeCount != 1 {
		t.Fatalf("row inside the retention window must be kept, got count=%d", probeCount)
	}
}

// 会话撤销 → 该会话下的 active 流置 abandoned（I-F6⑤）。
func TestDBAbandonBySession(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewFlowRepo(db)
	ctx := context.Background()

	sessionID := f.newSession(t, db)
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
