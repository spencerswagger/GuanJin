//go:build integration

package session

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是 S10 两个挂点在**真库**上的证据（`-tags=integration` + `GUANJIN_TEST_DSN`）：
//
//  1. `Options.Notifier`：撤销**已提交**之后才通知；通知 panic/失败**不改撤销结果**；
//     通知里的事实（user/session/client/reason/at）与库里那一行逐字段对齐（`AC-DP-12`）。
//  2. `AC-FC-02` 的会话面：仓储真的连不上库时，`CodeOf` 必须给出 `DB_UNAVAILABLE`
//     （不是 500、不是 401），这条用例走的是**真仓储**而不是假实现。

// openSessionDB 连真 PG17（`guanjin_test`，**带既有残留数据**，不做清库）。
func openSessionDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		// 门禁（CI）里一定设置；本地跑集成请显式给 DSN，不要靠"悄悄跳过"装绿。
		t.Fatal("必须设置 GUANJIN_TEST_DSN（真 PostgreSQL）")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{DSN: dsn})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Ping(ctx, db); err != nil {
		t.Fatalf("ping test db: %v", err)
	}
	if !db.Migrator().HasTable("sessions") {
		t.Fatal("测试库缺少 sessions 表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN")
	}
	t.Cleanup(func() { _ = store.Close(db) })
	return db
}

// sessionFixture 建一组隔离数据（唯一 client_id / username），结束按 id 清理。
type sessionFixture struct {
	userID   uuid.UUID
	clientID string
}

func newSessionFixture(t *testing.T, db *gorm.DB) *sessionFixture {
	t.Helper()
	suffix := uuid.NewString()
	f := &sessionFixture{
		userID:   mustV7(t),
		clientID: "s10-notify-" + suffix,
	}
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier) VALUES (?, ?, 'L2')`,
		f.clientID, "S10 notify client").Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		f.userID, "s10-"+suffix[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM auth_events WHERE user_id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, f.clientID).Error
	})
	return f
}

// insertActiveSession 建一条活跃会话（S7 只有读/撤销/续期，写入走 S6，所以这里用裸 SQL）。
func (f *sessionFixture) insertActiveSession(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	id := mustV7(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	err := db.Exec(`INSERT INTO sessions
		(id, user_id, client_id, acr, amr, status, issued_at, expires_at, idle_expires_at)
		VALUES (?, ?, ?, 'AAL1', '{pwd}', 'active', ?, ?, ?)`,
		id, f.userID, f.clientID, now, now.Add(12*time.Hour), now.Add(30*time.Minute)).Error
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return id
}

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	return id
}

// recordingNotifier 记录收到的通知（同步、线程安全）。
type recordingNotifier struct {
	mu      sync.Mutex
	notices []RevokeNotice
	panicOn bool
}

func (n *recordingNotifier) NotifyRevoked(_ context.Context, notice RevokeNotice) {
	n.mu.Lock()
	n.notices = append(n.notices, notice)
	n.mu.Unlock()
	if n.panicOn {
		panic("业务系统 500（故意）")
	}
}

func (n *recordingNotifier) waitFor(t *testing.T, want int) []RevokeNotice {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n.mu.Lock()
		got := append([]RevokeNotice(nil), n.notices...)
		n.mu.Unlock()
		if len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	t.Fatalf("等通知超时：want %d 条，实际 %d 条", want, len(n.notices))
	return nil
}

func (n *recordingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.notices)
}

// 真库撤销 → 会话行已 revoked、审计事件已写、通知一条且事实与库一致。
func TestDBNotifierFiresAfterCommittedRevoke(t *testing.T) {
	db := openSessionDB(t)
	f := newSessionFixture(t, db)
	sid := f.insertActiveSession(t, db)

	notifier := &recordingNotifier{}
	m, err := NewManager(store.NewSessionOpsRepo(db), store.NewFlowRepo(db),
		store.NewAuthEventRepo(db), Options{Notifier: notifier})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	before := time.Now().UTC()
	if err := m.Revoke(context.Background(), RevokeInput{
		SessionID: sid, Reason: ReasonAdmin, EventType: EventRevoke,
	}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// ① 撤销是本地事实：先看库（通知是副作用，晚到不算失败）。
	var row model.Session
	if err := db.Where("id = ?", sid).Take(&row).Error; err != nil {
		t.Fatalf("回读会话：%v", err)
	}
	if row.Status != model.SessionStatusRevoked {
		t.Fatalf("status = %q, want revoked", row.Status)
	}
	if row.RevokedAt == nil || row.RevokedAt.Before(before.Add(-2*time.Second)) {
		t.Fatalf("revoked_at 未落库或时刻不对：%v", row.RevokedAt)
	}
	if row.RevokeReason == nil || *row.RevokeReason != string(ReasonAdmin) {
		t.Fatalf("revoke_reason = %v, want admin", row.RevokeReason)
	}

	// ② 审计事件（cascade 的另一个副作用）也在。
	var events int64
	if err := db.Model(&model.AuthEvent{}).Where("session_id = ? AND event_type = ?", sid, EventRevoke).
		Count(&events).Error; err != nil {
		t.Fatalf("数 auth_events：%v", err)
	}
	if events == 0 {
		t.Fatal("撤销必须写一条 auth_events（审计链不能因为通知挂点而丢）")
	}

	// ③ 通知一条，且事实与库里那一行逐字段对齐（业务方按这些字段验签/对账）。
	got := notifier.waitFor(t, 1)[0]
	if got.SessionID != sid.String() || got.UserID != f.userID.String() {
		t.Fatalf("通知主体不对：%+v", got)
	}
	if got.Reason != string(ReasonAdmin) {
		t.Fatalf("reason = %q, want admin", got.Reason)
	}
	if d := got.At.Sub(*row.RevokedAt); d > time.Millisecond || d < -time.Millisecond {
		t.Fatalf("通知 At(%v) 必须等于 revoked_at(%v)", got.At, *row.RevokedAt)
	}
	if sub, ok := got.Subject.(*model.Session); !ok || sub.ClientID != f.clientID {
		t.Fatalf("通知必须带上被撤销的那一行（client_id 供投递方查路由），实际 %+v", got.Subject)
	}
}

// 通知实现 panic：撤销结果**一字不变**（不返回错误、行照样 revoked、事件照样写）。
func TestDBNotifierPanicDoesNotAffectRevoke(t *testing.T) {
	db := openSessionDB(t)
	f := newSessionFixture(t, db)
	sid := f.insertActiveSession(t, db)

	notifier := &recordingNotifier{panicOn: true}
	m, err := NewManager(store.NewSessionOpsRepo(db), store.NewFlowRepo(db),
		store.NewAuthEventRepo(db), Options{Notifier: notifier})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Revoke(context.Background(), RevokeInput{
		SessionID: sid, Reason: ReasonLogout, EventType: EventLogout,
	}); err != nil {
		t.Fatalf("通知 panic 不得让撤销报错，实际 %v", err)
	}
	notifier.waitFor(t, 1) // 确实被调用了（否则这条用例等于没测）

	var row model.Session
	if err := db.Where("id = ?", sid).Take(&row).Error; err != nil {
		t.Fatalf("回读会话：%v", err)
	}
	if row.Status != model.SessionStatusRevoked {
		t.Fatalf("status = %q, want revoked（通知 panic 不得回滚撤销）", row.Status)
	}
}

// 幂等重放（已撤销再撤一次）：**不再发通知**，也不报错。
func TestDBNotifierNotFiredOnIdempotentRevoke(t *testing.T) {
	db := openSessionDB(t)
	f := newSessionFixture(t, db)
	sid := f.insertActiveSession(t, db)

	notifier := &recordingNotifier{}
	m, err := NewManager(store.NewSessionOpsRepo(db), store.NewFlowRepo(db),
		store.NewAuthEventRepo(db), Options{Notifier: notifier})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx := context.Background()
	if err := m.Revoke(ctx, RevokeInput{SessionID: sid, Reason: ReasonLogout, EventType: EventLogout}); err != nil {
		t.Fatalf("第一次撤销：%v", err)
	}
	if err := m.Revoke(ctx, RevokeInput{SessionID: sid, Reason: ReasonLogout, EventType: EventLogout}); err != nil {
		t.Fatalf("第二次撤销（幂等）：%v", err)
	}
	notifier.waitFor(t, 1)
	time.Sleep(100 * time.Millisecond) // 给"可能多发的第二条"一点时间冒出来
	if n := notifier.count(); n != 1 {
		t.Fatalf("幂等重放不得重复通知，实际 %d 条", n)
	}
}

// AC-FC-02 的会话面：真仓储 + 真拨号失败（`127.0.0.1:1`）→ `DB_UNAVAILABLE`。
func TestDBUnavailableThroughRealRepo(t *testing.T) {
	ctx := context.Background()
	deadDB, err := store.Open(ctx, store.Options{
		DSN: "postgres://guanjin_test:x@127.0.0.1:1/guanjin_test?sslmode=disable",
	})
	if err != nil {
		t.Fatalf("open dead db: %v", err)
	}
	// `Open` 不做自动 Ping（DisableAutomaticPing），所以这里拿到的是"连不上的池"。
	t.Cleanup(func() { _ = store.Close(deadDB) })

	m, err := NewManager(store.NewSessionOpsRepo(deadDB), store.NewFlowRepo(deadDB),
		store.NewAuthEventRepo(deadDB), Options{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	_, err = m.Validate(ctx, mustV7(t))
	if err == nil {
		t.Fatal("库连不上时 Validate 必须报错（fail-closed）")
	}
	if code := CodeOf(err); code != httpx.CodeDBUnavailable {
		t.Fatalf("CodeOf = %q, want %q（真实仓储出口）", code, httpx.CodeDBUnavailable)
	}
	if status := httpx.HTTPStatus(CodeOf(err)); status != 503 {
		t.Fatalf("status = %d, want 503", status)
	}
	// 401/404 是"伪装"：把库挂了说成"你没登录/查不到"，运维会查错方向。
	if errors.Is(err, ErrSessionNotFound) {
		t.Fatal("库不可用不得伪装成 SESSION_NOT_FOUND")
	}
}
