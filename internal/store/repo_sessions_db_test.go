//go:build integration

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是 S7 会话仓储的**真库**证据（`-tags=integration` + `GUANJIN_TEST_DSN`）。
//
// 覆盖的点都是"单测测不出来"的：
//   - `sessions` 的两条 CHECK（`sessions_revoked_consistency` / `sessions_time_order`）；
//   - 撤销是**条件 UPDATE**（已撤销的行不再被改，RowsAffected=0）；
//   - "挤掉最旧"的单语句 CTE（`FOR UPDATE SKIP LOCKED`）真的撤销最旧那条；
//   - 游标翻页不漏行、不重复（行值比较 `(issued_at, id)`）；
//   - 策略读取的**覆盖优先**顺序（client_override → tier_default）。

func TestDBSessionCreateAndGet(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	s := &model.Session{
		UserID:        f.userID,
		ClientID:      f.clientID,
		ACR:           "AAL1",
		AMR:           model.StringArray{"pwd"},
		Status:        model.SessionStatusActive,
		IssuedAt:      now,
		ExpiresAt:     now.Add(12 * time.Hour),
		IdleExpiresAt: now.Add(30 * time.Minute),
	}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, f.userID) })

	if s.ID == uuid.Nil {
		t.Fatal("Create must assign a uuid v7 primary key")
	}
	got, err := repo.GetByID(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.UserID != f.userID || got.ClientID != f.clientID || got.Status != model.SessionStatusActive {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if len(got.AMR) != 1 || got.AMR[0] != "pwd" {
		t.Fatalf("amr = %v", got.AMR)
	}
	if _, err := repo.GetByID(ctx, uuid.New()); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("missing session: %v", err)
	}
}

// 撤销后 `status` 与 `revoked_at` 必须同真同假（DB CHECK），且第二次撤销 0 行。
func TestDBRevokeIsConditionalAndConsistent(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()

	s := &model.Session{
		UserID: f.userID, ClientID: f.clientID, ACR: "AAL1", Status: model.SessionStatusActive,
		IssuedAt: now, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(30 * time.Minute),
	}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, f.userID) })

	n, err := repo.RevokeByID(ctx, s.ID, "admin", now)
	if err != nil || n != 1 {
		t.Fatalf("first revoke: n=%d err=%v", n, err)
	}
	got, err := repo.GetByID(ctx, s.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != model.SessionStatusRevoked || got.RevokedAt == nil || got.RevokeReason == nil ||
		*got.RevokeReason != "admin" {
		t.Fatalf("revoked row inconsistent: %+v", got)
	}

	n, err = repo.RevokeByID(ctx, s.ID, "admin", now.Add(time.Minute))
	if err != nil || n != 0 {
		t.Fatalf("second revoke must be a no-op: n=%d err=%v", n, err)
	}
}

// `idle_expires_at > expires_at` 必须被 DB CHECK 拒（应用侧已先拒一次，这里是 db 兜底证据）。
func TestDBSessionTimeOrderCheck(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	now := time.Now().UTC()

	err := db.Exec(`INSERT INTO sessions (id, user_id, client_id, acr, status, issued_at, expires_at, idle_expires_at)
		VALUES (?, ?, ?, 'AAL1', 'active', ?, ?, ?)`,
		mustV7(t), f.userID, f.clientID, now, now.Add(time.Hour), now.Add(2*time.Hour)).Error
	if err == nil {
		_ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, f.userID)
		t.Fatal("idle > absolute must be rejected by sessions_time_order")
	}
	var dbErr *Error
	if !errors.As(MapError(err), &dbErr) {
		t.Fatalf("want mapped *store.Error, got %v", err)
	}
	if dbErr.SQLState != "23514" || dbErr.Constraint != "sessions_time_order" {
		t.Fatalf("sqlstate/constraint = %s/%s", dbErr.SQLState, dbErr.Constraint)
	}
}

// 游标翻页：三行、每页两行，第二页不得重复第一页行，且顺序稳定。
func TestDBSessionListCursorPaging(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)

	ids := make([]uuid.UUID, 0, 3)
	for i := 0; i < 3; i++ {
		s := &model.Session{
			UserID: f.userID, ClientID: f.clientID, ACR: "AAL1", Status: model.SessionStatusActive,
			IssuedAt:      base.Add(time.Duration(i) * time.Second),
			ExpiresAt:     base.Add(time.Hour),
			IdleExpiresAt: base.Add(30 * time.Minute),
		}
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		ids = append(ids, s.ID)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, f.userID) })

	userID := f.userID
	first, err := repo.List(ctx, SessionQuery{UserID: &userID, Limit: 2})
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(first.Items) != 2 {
		t.Fatalf("page1 items = %d, want 2", len(first.Items))
	}
	if first.NextCursor == "" {
		t.Fatal("page1 must carry a next_cursor")
	}
	// 最新签发的排在最前。
	if first.Items[0].ID != ids[2] {
		t.Fatalf("page1[0] = %s, want newest %s", first.Items[0].ID, ids[2])
	}

	second, err := repo.List(ctx, SessionQuery{UserID: &userID, Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(second.Items) != 1 {
		t.Fatalf("page2 items = %d, want 1", len(second.Items))
	}
	if second.Items[0].ID != ids[0] {
		t.Fatalf("page2[0] = %s, want oldest %s", second.Items[0].ID, ids[0])
	}
	if second.NextCursor != "" {
		t.Fatal("last page must not carry next_cursor")
	}
}

// `active=true` 只回 active；`active=false` 不加反向过滤（审计要全量视图）。
func TestDBSessionListActiveFilter(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()

	active := &model.Session{UserID: f.userID, ClientID: f.clientID, ACR: "AAL1",
		Status: model.SessionStatusActive, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		IdleExpiresAt: now.Add(time.Minute)}
	if err := repo.Create(ctx, active); err != nil {
		t.Fatalf("Create active: %v", err)
	}
	if _, err := repo.RevokeByID(ctx, active.ID, "admin", now); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	second := &model.Session{UserID: f.userID, ClientID: f.clientID, ACR: "AAL1",
		Status: model.SessionStatusActive, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		IdleExpiresAt: now.Add(time.Minute)}
	if err := repo.Create(ctx, second); err != nil {
		t.Fatalf("Create second: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, f.userID) })

	userID := f.userID
	yes := true
	onlyActive, err := repo.List(ctx, SessionQuery{UserID: &userID, Limit: 10, Active: &yes})
	if err != nil {
		t.Fatalf("List active: %v", err)
	}
	if len(onlyActive.Items) != 1 || onlyActive.Items[0].ID != second.ID {
		t.Fatalf("active filter wrong: %+v", onlyActive.Items)
	}

	no := false
	all, err := repo.List(ctx, SessionQuery{UserID: &userID, Limit: 10, Active: &no})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all.Items) != 2 {
		t.Fatalf("active=false must not filter out revoked rows: %d", len(all.Items))
	}
}

// 挤掉最旧：CTE 单语句撤销 issued_at 最早的那条，返回它的 id。
func TestDBEvictOldestActive(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)

	oldest := &model.Session{UserID: f.userID, ClientID: f.clientID, ACR: "AAL1",
		Status: model.SessionStatusActive, IssuedAt: base.Add(-time.Hour),
		ExpiresAt: base.Add(time.Hour), IdleExpiresAt: base.Add(30 * time.Minute)}
	newer := &model.Session{UserID: f.userID, ClientID: f.clientID, ACR: "AAL1",
		Status: model.SessionStatusActive, IssuedAt: base,
		ExpiresAt: base.Add(time.Hour), IdleExpiresAt: base.Add(30 * time.Minute)}
	for _, s := range []*model.Session{oldest, newer} {
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, f.userID) })

	evicted, err := repo.EvictOldestActive(ctx, f.userID, f.clientID, "concurrent_limit", base)
	if err != nil {
		t.Fatalf("EvictOldestActive: %v", err)
	}
	if evicted != oldest.ID {
		t.Fatalf("evicted = %s, want oldest %s", evicted, oldest.ID)
	}
	got, err := repo.GetByID(ctx, oldest.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != model.SessionStatusRevoked || got.RevokeReason == nil || *got.RevokeReason != "concurrent_limit" {
		t.Fatalf("evicted row = %+v", got)
	}
	keep, err := repo.GetByID(ctx, newer.ID)
	if err != nil {
		t.Fatalf("GetByID newer: %v", err)
	}
	if keep.Status != model.SessionStatusActive {
		t.Fatal("newer session must stay active")
	}

	// 没有活跃会话时返回 Nil（不是错误）。
	none, err := repo.EvictOldestActive(ctx, f.userID, "no-such-client", "concurrent_limit", base)
	if err != nil || none != uuid.Nil {
		t.Fatalf("empty evict = %s, %v", none, err)
	}
}

// 计数、批量撤销、踢其它设备、续期、惰性过期。
func TestDBSessionBulkAndLifecycle(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond)

	keep, other := uuid.Nil, uuid.Nil
	for i := 0; i < 2; i++ {
		s := &model.Session{UserID: f.userID, ClientID: f.clientID, ACR: "AAL1",
			Status: model.SessionStatusActive, IssuedAt: base.Add(time.Duration(i) * time.Second),
			ExpiresAt: base.Add(time.Hour), IdleExpiresAt: base.Add(30 * time.Minute)}
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if i == 0 {
			keep = s.ID
		} else {
			other = s.ID
		}
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, f.userID) })

	n, err := repo.CountActiveByUserClient(ctx, f.userID, f.clientID)
	if err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}

	// 续期：只滚 idle，不滚 absolute。
	newIdle := base.Add(45 * time.Minute)
	if _, err := repo.TouchLastSeen(ctx, keep, base, newIdle); err != nil {
		t.Fatalf("TouchLastSeen: %v", err)
	}
	touched, err := repo.GetByID(ctx, keep)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if touched.LastSeenAt == nil || !touched.IdleExpiresAt.Equal(newIdle) {
		t.Fatalf("touch not applied: %+v", touched)
	}
	if !touched.ExpiresAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("absolute timeout must not roll: %s", touched.ExpiresAt)
	}

	// 踢其它设备：keep 保留、other 撤销。
	rows, err := repo.RevokeOthers(ctx, f.userID, keep, "password_change", base)
	if err != nil || rows != 1 {
		t.Fatalf("RevokeOthers = %d, %v", rows, err)
	}
	survivor, err := repo.GetByID(ctx, keep)
	if err != nil {
		t.Fatalf("GetByID keep: %v", err)
	}
	if survivor.Status != model.SessionStatusActive {
		t.Fatal("keep session must stay active")
	}
	gone, err := repo.GetByID(ctx, other)
	if err != nil {
		t.Fatalf("GetByID other: %v", err)
	}
	if gone.Status != model.SessionStatusRevoked {
		t.Fatal("other session must be revoked")
	}

	// 惰性过期：把 keep 的 idle 拨到过去，ExpireStale 应把它置 expired。
	if err := db.Exec(`UPDATE sessions SET idle_expires_at = ? WHERE id = ?`,
		base.Add(-time.Minute), keep).Error; err != nil {
		t.Fatalf("force idle expiry: %v", err)
	}
	if _, err := repo.ExpireStale(ctx, base); err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	expired, err := repo.GetByID(ctx, keep)
	if err != nil {
		t.Fatalf("GetByID expired: %v", err)
	}
	if expired.Status != model.SessionStatusExpired {
		t.Fatalf("status = %q, want expired", expired.Status)
	}

	// 批量撤销：此时已没有 active 行 → 0。
	if rows, err := repo.RevokeAllByUser(ctx, f.userID, "admin", base); err != nil || rows != 0 {
		t.Fatalf("RevokeAllByUser on settled rows = %d, %v", rows, err)
	}
	if ids, err := repo.ActiveIDsByUser(ctx, f.userID); err != nil || len(ids) != 0 {
		t.Fatalf("ActiveIDsByUser = %v, %v", ids, err)
	}
}

// 策略读取：client_override 优先于 tier_default；缺失 → ErrPolicyNotConfigured。
func TestDBLoadSessionPolicy(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewSessionRepo(db)
	ctx := context.Background()

	// fixture 的 client 是 L2，先读一次 tier_default（应由 000017 seed 提供）。
	def, err := repo.LoadSessionPolicy(ctx, f.clientID)
	if err != nil {
		t.Fatalf("tier_default policy: %v", err)
	}
	if def.Tier != "L2" || def.IdleTimeout <= 0 || def.AbsoluteTimeout <= 0 {
		t.Fatalf("unexpected default policy: %+v", def)
	}

	// 插一条 client_override，改掉 idle 与溢出动作。
	// refresh_token_ttl_sec 必须 <= session_absolute_timeout_min*60
	// （DB CHECK `login_policies_refresh_le_absolute`）；60 分钟绝对超时对应 3600 秒刷新。
	if err := db.Exec(`INSERT INTO login_policies
		(id, scope, client_id, session_idle_timeout_min, session_absolute_timeout_min,
		 refresh_token_ttl_sec, max_concurrent_sessions, concurrent_overflow_action, sso_logout_scope)
		VALUES (?, 'client_override', ?, 7, 60, 3600, 2, 'evict_oldest', 'current_client')`,
		mustV7(t), f.clientID).Error; err != nil {
		t.Fatalf("insert override: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM login_policies WHERE client_id = ? AND scope = 'client_override'`, f.clientID)
	})

	got, err := repo.LoadSessionPolicy(ctx, f.clientID)
	if err != nil {
		t.Fatalf("override policy: %v", err)
	}
	if got.IdleTimeout != 7*time.Minute || got.AbsoluteTimeout != time.Hour {
		t.Fatalf("override not applied: %+v", got)
	}
	if got.MaxConcurrent == nil || *got.MaxConcurrent != 2 || got.OverflowAction != "evict_oldest" ||
		got.SSOLogoutScope != "current_client" {
		t.Fatalf("override columns not read: %+v", got)
	}

	// 不存在的 client → CLIENT_NOT_FOUND。
	if _, err := repo.LoadSessionPolicy(ctx, "no-such-client-"+uuid.NewString()); !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("want ErrClientNotFound, got %v", err)
	}
}

// ClientNames / UserExists / RolesForUserClient 的真库行为。
func TestDBClientNamesUserExistsRoles(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewSessionRepo(db)
	ctx := context.Background()

	names, err := repo.ClientNames(ctx, []string{f.clientID, "no-such-client"})
	if err != nil {
		t.Fatalf("ClientNames: %v", err)
	}
	if names[f.clientID] != "S3 test client" {
		t.Fatalf("names = %v", names)
	}
	if _, ok := names["no-such-client"]; ok {
		t.Fatal("unknown client must not appear in the map")
	}

	ok, err := repo.UserExists(ctx, f.userID)
	if err != nil || !ok {
		t.Fatalf("UserExists(known) = %v, %v", ok, err)
	}
	ok, err = repo.UserExists(ctx, uuid.New())
	if err != nil || ok {
		t.Fatalf("UserExists(unknown) = %v, %v", ok, err)
	}

	roles := NewRoleBindingRepo(db)
	if err := db.Exec(`INSERT INTO role_bindings (id, user_id, client_id, role) VALUES (?, ?, ?, 'auditor')`,
		mustV7(t), f.userID, f.clientID).Error; err != nil {
		t.Fatalf("insert binding: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM role_bindings WHERE user_id = ?`, f.userID) })

	got, err := roles.RolesForUserClient(ctx, f.userID, f.clientID)
	if err != nil {
		t.Fatalf("RolesForUserClient: %v", err)
	}
	if len(got) != 1 || got[0] != "auditor" {
		t.Fatalf("roles = %v", got)
	}
}
