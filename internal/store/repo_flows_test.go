package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// captureLogger 把 GORM 生成的 SQL 记下来（不需要真数据库：配合 DryRun 使用）。
//
// 为什么要在没有库的情况下验 SQL：`FOR UPDATE` 漏掉、`Save` 覆盖全列这类错误
// 在单副本功能测试里**看不出来**，只在并发下丢数据。这里让它们变成一次普通的断言。
type captureLogger struct {
	queries []string
}

func (c *captureLogger) LogMode(logger.LogLevel) logger.Interface { return c }

func (c *captureLogger) Info(context.Context, string, ...any) {}
func (c *captureLogger) Warn(context.Context, string, ...any) {}
func (c *captureLogger) Error(context.Context, string, ...any) {
}

func (c *captureLogger) Trace(_ context.Context,
	_ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	c.queries = append(c.queries, sql)
}

// dryRunDB 打开一个**永不出网**的 GORM 句柄：DryRun + 不自动 ping + 捕获 SQL。
func dryRunDB(t *testing.T) (*gorm.DB, *captureLogger) {
	t.Helper()
	capture := &captureLogger{}
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "postgres://user:pass@127.0.0.1:1/guanjin_test", // 不会真的连
	}), &gorm.Config{
		DisableAutomaticPing: true,
		TranslateError:       false,
		Logger:               capture,
		// 只对**测试句柄**关掉默认事务包装：DryRun 不会跳过 `begin_transaction`，
		// 那一步是真连接（本测试第一版就是被它打到 127.0.0.1:1 的）。
		// 生产句柄（store.Open）保持 false —— 单条写也在事务里。
		SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db.Session(&gorm.Session{DryRun: true}), capture
}

func (c *captureLogger) last(t *testing.T) string {
	t.Helper()
	if len(c.queries) == 0 {
		t.Fatal("no SQL was generated")
	}
	return c.queries[len(c.queries)-1]
}

// LockByID 生成的 SQL 必须带 `FOR UPDATE`，且落在 flows 表上。
//
// 这是 AC-FLOW-11③（并发补步不丢不双写）的前提：没有行锁，两个副本读到同一份
// `achieved_amr` 各自累加再写回，后写的那次会把前一次覆盖掉。
func TestLockByIDEmitsForUpdate(t *testing.T) {
	db, capture := dryRunDB(t)
	id := uuid.MustParse("018f0000-0000-7000-8000-000000000010")

	_, _ = NewFlowRepo(db).LockByID(context.Background(), db, id) // DryRun 下查不到行是预期的

	sql := capture.last(t)
	if !strings.Contains(strings.ToUpper(sql), "FOR UPDATE") {
		t.Fatalf("LockByID must emit FOR UPDATE, got: %s", sql)
	}
	if !strings.Contains(sql, `"flows"`) {
		t.Fatalf("LockByID must query flows, got: %s", sql)
	}
	if !strings.Contains(sql, "WHERE id = ") {
		t.Fatalf("LockByID must filter by id, got: %s", sql)
	}
}

// 锁读必须显式给事务句柄：不给就报错，绝不能"自己开个事务"（出了函数锁就没了）。
func TestLockByIDRequiresTx(t *testing.T) {
	db, _ := dryRunDB(t)
	if _, err := NewFlowRepo(db).LockByID(context.Background(), nil, uuid.New()); !errors.Is(err, ErrNilTx) {
		t.Fatalf("err = %v, want ErrNilTx", err)
	}
	if _, err := NewFlowRepo(db).LockByID(context.Background(), db, uuid.Nil); !errors.Is(err, ErrFlowNotFound) {
		t.Fatalf("nil id err = %v, want ErrFlowNotFound", err)
	}
}

// 列更新必须是白名单：只有 patch 里点名的列出现在 UPDATE SET 里。
//
// 反向断言尤其重要：`trace_id` 永远不许被补步路径改写（D4-C(4)：只记创建那次）。
func TestApplyOnlyTouchesWhitelistedColumns(t *testing.T) {
	db, capture := dryRunDB(t)
	id := uuid.New()
	status := "completed"
	amr := model.StringArray{"pwd"}
	steps := model.StepList{{Factor: "pwd", ChallengeID: "c-1", At: time.Now().UTC()}}
	sessionID := uuid.New()

	updates := flowPatchUpdates(FlowPatch{
		Status:         &status,
		AchievedAMR:    &amr,
		CompletedSteps: &steps,
		SessionID:      &sessionID,
	})
	for _, col := range []string{"status", "achieved_amr", "completed_steps", "session_id"} {
		if _, ok := updates[col]; !ok {
			t.Errorf("patch column %q missing from updates", col)
		}
	}
	for _, forbidden := range []string{"trace_id", "client_id", "target_acr", "expires_at", "id", "created_at"} {
		if _, ok := updates[forbidden]; ok {
			t.Errorf("patch must never touch %q", forbidden)
		}
	}

	n, err := NewFlowRepo(db).Apply(context.Background(), db, id, FlowPatch{Status: &status})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if n != 0 {
		t.Fatalf("DryRun RowsAffected = %d, want 0", n)
	}
	sql := strings.ToUpper(capture.last(t))
	if !strings.Contains(sql, "UPDATE") || !strings.Contains(sql, `"FLOWS"`) {
		t.Fatalf("Apply must UPDATE flows, got: %s", capture.last(t))
	}
	if strings.Contains(sql, "TRACE_ID") {
		t.Fatalf("Apply must not write trace_id, got: %s", capture.last(t))
	}
}

// 空 patch 必须拒绝：一个"什么都不改"的 UPDATE 会把 updated_at 推后，
// 干扰两段式清理（I-F6 用 updated_at 判断 7 天保留期）。
func TestApplyRejectsEmptyPatch(t *testing.T) {
	db, _ := dryRunDB(t)
	if _, err := NewFlowRepo(db).Apply(context.Background(), db, uuid.New(), FlowPatch{}); err == nil {
		t.Fatal("empty patch must be rejected")
	}
	if _, err := NewFlowRepo(db).Apply(context.Background(), nil, uuid.New(), FlowPatch{}); !errors.Is(err, ErrNilTx) {
		t.Fatalf("nil tx err = %v, want ErrNilTx", err)
	}
}

// 两段式清理（I-F6）：第一段只置 expired（行保留），第二段才按 cutoff 删。
func TestTwoPhaseCleanupSQL(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewFlowRepo(db)
	ctx := context.Background()

	if _, err := repo.MarkExpired(ctx); err != nil {
		t.Fatalf("MarkExpired: %v", err)
	}
	first := capture.last(t)
	if !strings.Contains(first, "UPDATE flows SET status = 'expired'") {
		t.Fatalf("phase 1 must only flip status, got: %s", first)
	}
	if strings.Contains(strings.ToUpper(first), "DELETE") {
		t.Fatalf("phase 1 must never delete, got: %s", first)
	}
	if !strings.Contains(first, "expires_at <= now()") {
		t.Fatalf("phase 1 must compare against DB clock, got: %s", first)
	}

	if _, err := repo.MarkExpiredIfActive(ctx, uuid.New()); err != nil {
		t.Fatalf("MarkExpiredIfActive: %v", err)
	}
	lazy := capture.last(t)
	if !strings.Contains(lazy, "status = 'active'") || !strings.Contains(lazy, "expires_at <= now()") {
		t.Fatalf("lazy expiry must be conditional on active+expired, got: %s", lazy)
	}

	cutoff := time.Now().UTC().Add(-ExpiredRetention)
	if _, err := repo.DeleteExpiredBefore(ctx, cutoff); err != nil {
		t.Fatalf("DeleteExpiredBefore: %v", err)
	}
	second := capture.last(t)
	if !strings.Contains(strings.ToUpper(second), "DELETE FROM FLOWS") {
		t.Fatalf("phase 2 must delete, got: %s", second)
	}
	if !strings.Contains(second, "status = 'expired'") {
		t.Fatalf("phase 2 must only delete expired rows, got: %s", second)
	}
	if !strings.Contains(second, "updated_at < ") {
		t.Fatalf("phase 2 must use the retention cutoff, got: %s", second)
	}

	if _, err := repo.DeleteExpiredBefore(ctx, time.Time{}); err == nil {
		t.Fatal("zero cutoff must be rejected (no implicit retention inside the repo)")
	}
}

// 会话撤销 → 该会话的活跃流置 abandoned（D5 I-F6⑤），且只动 active。
func TestAbandonBySessionSQL(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewFlowRepo(db)
	sessionID := uuid.New()

	if _, err := repo.AbandonBySession(context.Background(), sessionID); err != nil {
		t.Fatalf("AbandonBySession: %v", err)
	}
	sql := capture.last(t)
	if !strings.Contains(sql, "status = 'abandoned'") || !strings.Contains(sql, "session_id = ") {
		t.Fatalf("unexpected SQL: %s", sql)
	}
	if !strings.Contains(sql, "status = 'active'") {
		t.Fatalf("must only abandon active flows, got: %s", sql)
	}
	if _, err := repo.AbandonBySession(context.Background(), uuid.Nil); err == nil {
		t.Fatal("nil session id must be rejected")
	}
}

// Create 必须用 RETURNING 取回库生成的 created_at/updated_at（模型里这两个字段只读）。
func TestCreateUsesReturning(t *testing.T) {
	db, capture := dryRunDB(t)
	f := &model.Flow{
		ClientID:    "client-a",
		TargetACR:   "AAL1",
		AchievedAMR: model.StringArray{},
		Status:      "active",
		ExpiresAt:   time.Now().UTC().Add(time.Minute),
	}
	if err := NewFlowRepo(db).Create(context.Background(), f); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sql := capture.last(t)
	if !strings.Contains(strings.ToUpper(sql), "INSERT INTO \"FLOWS\"") {
		t.Fatalf("unexpected SQL: %s", sql)
	}
	if !strings.Contains(strings.ToUpper(sql), "RETURNING") {
		t.Fatalf("Create must use RETURNING to read DB-owned timestamps, got: %s", sql)
	}
	if err := NewFlowRepo(db).Create(context.Background(), nil); err == nil {
		t.Fatal("nil flow must be rejected")
	}
}

// WithTx 返回的仓储与原始仓储共享同一份逻辑（避免"以为在事务里其实在池上"）。
func TestWithTxBindsSameHandle(t *testing.T) {
	db, _ := dryRunDB(t)
	repo := NewFlowRepo(db)
	if repo.WithTx(db).db != db {
		t.Fatal("WithTx must bind the provided handle")
	}
}
