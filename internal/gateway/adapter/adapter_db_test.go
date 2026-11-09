//go:build integration

package adapter

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/gateway"
	"github.com/spencerswagger/guanjin/internal/protocol"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是 `internal/gateway/adapter` 的**真库**证据（`-tags=integration` + `GUANJIN_TEST_DSN`）。
//
// 这一层最容易抄错的是**哨兵翻译**（store 的错误 → gateway 的错误）与**取法顺序**
// （`client_override` 优先、其次 `tier_default`），两者都只有在真库上才测得出来：
// 假实现对"哪条策略行命中"没有话语权。

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		// 刻意**不 Skip**：Gate 口径「SKIP = 未验证 = 红」。integration job 一定注入 DSN。
		t.Fatal("必须设置 GUANJIN_TEST_DSN（integration job 已注入）")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{DSN: dsn})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Ping(ctx, db); err != nil {
		t.Fatalf("ping test db: %v", err)
	}
	if !db.Migrator().HasTable("user_visited_clients") || !db.Migrator().HasTable("login_policies") {
		t.Fatalf("测试库缺表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN")
	}
	t.Cleanup(func() { _ = store.Close(db) })
	return db
}

type fixture struct {
	userID   uuid.UUID
	clientID string
	tier     string
	session  uuid.UUID
}

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	return id
}

// newFixture 建一套隔离数据（client + user + session），结束时按 id 清理。
//
// 注意：`guanjin_test` 是**带残留数据的共享库**（users≈1150 / clients≈1405 且历史用例不清理），
// 所以这里一律用带随机后缀的 client_id，并且只清理自己插入的行。
func newFixture(t *testing.T, db *gorm.DB, tier string) *fixture {
	t.Helper()
	suffix := uuid.NewString()[:8]
	f := &fixture{userID: mustV7(t), clientID: "s8-" + suffix, tier: tier, session: mustV7(t)}

	if err := db.Exec(`INSERT INTO clients (client_id, name, tier) VALUES (?, ?, ?)`,
		f.clientID, "S8 数据面夹具", tier).Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		f.userID, "s8-"+suffix).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	now := time.Now().UTC()
	if err := db.Exec(`INSERT INTO sessions
		(id, user_id, client_id, acr, amr, status, issued_at, expires_at, idle_expires_at)
		VALUES (?, ?, ?, 'AAL1', '{pwd}', 'active', ?, ?, ?)`,
		f.session, f.userID, f.clientID, now, now.Add(2*time.Hour), now.Add(30*time.Minute)).Error; err != nil {
		t.Fatalf("insert session: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM user_visited_clients WHERE user_id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM flows WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM role_bindings WHERE user_id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM login_policies WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, f.userID).Error
	})
	return f
}

// insertPolicy 插一行策略（`scope` 决定 tier/client_id 二选一）。
func insertPolicy(t *testing.T, db *gorm.DB, scope, tier, clientID, minACR string, disabled string) {
	t.Helper()
	var tierArg, clientArg any
	if scope == "tier_default" {
		tierArg = tier
	} else {
		clientArg = clientID
	}
	if err := db.Exec(`INSERT INTO login_policies (id, scope, tier, client_id, min_acr, disabled_factors)
		VALUES (?, ?, ?, ?, ?, ?::text[])`,
		mustV7(t), scope, tierArg, clientArg, minACR, disabled).Error; err != nil {
		t.Fatalf("insert login_policies(%s): %v", scope, err)
	}
}

// 取法顺序：`client_override` 优先于同档位的 `tier_default`（D16②）。
func TestDBLoadAdmissionPolicyPrefersClientOverride(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L3")
	// 三档 tier_default 由 000017 seed；这里另插一行**本 client 专属**的覆盖行。
	insertPolicy(t, db, "client_override", "", f.clientID, "AAL2", `{otp}`)

	repo := NewPolicies(db)
	policy, err := repo.LoadAdmissionPolicy(context.Background(), f.clientID)
	if err != nil {
		t.Fatalf("LoadAdmissionPolicy: %v", err)
	}
	if policy.MinACR != "AAL2" {
		t.Fatalf("覆盖行优先：期望 AAL2，实际 %q", policy.MinACR)
	}
	if policy.Tier != "L3" || policy.ClientName == "" {
		t.Fatalf("tier/name 期望来自 clients 行，实际 tier=%q name=%q", policy.Tier, policy.ClientName)
	}
	if len(policy.DisabledFactors) != 1 || policy.DisabledFactors[0] != "otp" {
		t.Fatalf("禁用因子期望 [otp]，实际 %v", policy.DisabledFactors)
	}
}

// 没有覆盖行时回落到 tier_default（000017 seed 的三档）。
func TestDBLoadAdmissionPolicyFallsBackToTierDefault(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L1")

	policy, err := NewPolicies(db).LoadAdmissionPolicy(context.Background(), f.clientID)
	if err != nil {
		t.Fatalf("LoadAdmissionPolicy: %v", err)
	}
	// 000017 seed：L1/L2 → AAL1，L3 → AAL2。
	if policy.MinACR != "AAL1" {
		t.Fatalf("L1 档位回落期望 AAL1，实际 %q", policy.MinACR)
	}
}

// 两侧都没有 → `ErrPolicyNotConfigured`（配置缺陷，不垫默认值）。
func TestDBLoadAdmissionPolicyNotConfigured(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	// 用一个不存在的 tier 制造"两侧都没有"：先删掉 000017 给该档位的默认行会污染共享库，
	// 故改为插入一个只有本 client 才有的档位——不可能，档位是 CHECK 限定的 L1/L2/L3。
	// 因此这里改测"业务系统不存在"这条更干净的配置缺陷分支。
	_, err := NewPolicies(db).LoadAdmissionPolicy(context.Background(), "no-such-client-"+f.clientID)
	if !errors.Is(err, gateway.ErrClientNotFound) {
		t.Fatalf("期望 ErrClientNotFound，实际 %v", err)
	}
}

// 客户端被停用 → `ErrClientDisabled`（不让停用的业务系统继续吃流量）。
func TestDBLoadAdmissionPolicyClientDisabled(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	if err := db.Exec(`UPDATE clients SET status = 'disabled' WHERE client_id = ?`, f.clientID).Error; err != nil {
		t.Fatalf("disable client: %v", err)
	}
	_, err := NewPolicies(db).LoadAdmissionPolicy(context.Background(), f.clientID)
	if !errors.Is(err, gateway.ErrClientDisabled) {
		t.Fatalf("期望 ErrClientDisabled，实际 %v", err)
	}
}

// 会话读取：真库 roundtrip + "不存在"翻成 gateway 哨兵。
func TestDBLoadSessionRoundTripAndNotFound(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	repo := NewSessions(db)

	got, err := repo.LoadSession(context.Background(), f.session.String())
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got.UserID != f.userID.String() || got.ClientID != f.clientID || got.Status != gateway.SessionActive {
		t.Fatalf("快照不符：%+v", got)
	}
	if got.ACR != "AAL1" || len(got.AMR) != 1 || got.AMR[0] != "pwd" {
		t.Fatalf("acr/amr 不符：acr=%q amr=%v", got.ACR, got.AMR)
	}

	if _, err := repo.LoadSession(context.Background(), uuid.NewString()); !errors.Is(err, gateway.ErrSessionNotFound) {
		t.Fatalf("不存在期望 ErrSessionNotFound，实际 %v", err)
	}
	if _, err := repo.LoadSession(context.Background(), "not-a-uuid"); !errors.Is(err, gateway.ErrSessionNotFound) {
		t.Fatalf("非法 sid 期望 ErrSessionNotFound，实际 %v", err)
	}
}

// 角色读取：只取 active，去重升序。
func TestDBRolesForUserClient(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	for _, role := range []string{"viewer", "admin"} {
		if err := db.Exec(`INSERT INTO role_bindings (id, user_id, client_id, role) VALUES (?, ?, ?, ?)`,
			mustV7(t), f.userID, f.clientID, role).Error; err != nil {
			t.Fatalf("insert role %s: %v", role, err)
		}
	}
	// 已撤销的绑定不应出现。
	if err := db.Exec(`INSERT INTO role_bindings (id, user_id, client_id, role, status, revoked_at)
		VALUES (?, ?, ?, 'auditor', 'revoked', now())`, mustV7(t), f.userID, f.clientID).Error; err != nil {
		t.Fatalf("insert revoked role: %v", err)
	}

	roles, err := NewRoles(db).RolesForUserClient(context.Background(), f.userID.String(), f.clientID)
	if err != nil {
		t.Fatalf("RolesForUserClient: %v", err)
	}
	if len(roles) != 2 || roles[0] != "admin" || roles[1] != "viewer" {
		t.Fatalf("期望 [admin viewer]（升序、不含已撤销），实际 %v", roles)
	}
}

// 用户名读取：断言 `uname` 必须取 `users.username`，不是 display_name。
func TestDBUsernameIsUsernameNotDisplayName(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	if err := db.Exec(`UPDATE users SET display_name = 'Alice A. Smith' WHERE id = ?`, f.userID).Error; err != nil {
		t.Fatalf("set display_name: %v", err)
	}
	name, err := NewUsers(db).Username(context.Background(), f.userID.String())
	if err != nil {
		t.Fatalf("Username: %v", err)
	}
	var want string
	if err := db.Raw(`SELECT username FROM users WHERE id = ?`, f.userID).Scan(&want).Error; err != nil {
		t.Fatalf("read username: %v", err)
	}
	if name != want {
		t.Fatalf("期望 users.username=%q，实际 %q（不得取 display_name）", want, name)
	}
	if name == "Alice A. Smith" {
		t.Fatal("取到了 display_name")
	}
}

// `user_visited_clients` 的 upsert：首次插入、再次累加、tier 刷新（D16② 的写时机由 gateway 决定，
// 这里只证"写法幂等且计数正确"）。
func TestDBRecordVisitUpserts(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	repo := NewVisits(db)
	ctx := context.Background()

	if err := repo.RecordVisit(ctx, f.userID.String(), f.clientID, "L2"); err != nil {
		t.Fatalf("RecordVisit #1: %v", err)
	}
	if err := repo.RecordVisit(ctx, f.userID.String(), f.clientID, "L3"); err != nil {
		t.Fatalf("RecordVisit #2: %v", err)
	}

	var row struct {
		VisitCount  int64  `gorm:"column:visit_count"`
		TierAtVisit string `gorm:"column:tier_at_visit"`
	}
	if err := db.Raw(`SELECT visit_count, tier_at_visit FROM user_visited_clients
		WHERE user_id = ? AND client_id = ?`, f.userID, f.clientID).Scan(&row).Error; err != nil {
		t.Fatalf("read visit row: %v", err)
	}
	if row.VisitCount != 2 {
		t.Fatalf("visit_count 期望 2（upsert 累加），实际 %d —— 说明走了插入失败或重复行", row.VisitCount)
	}
	if row.TierAtVisit != "L3" {
		t.Fatalf("tier_at_visit 期望刷新为 L3，实际 %q", row.TierAtVisit)
	}

	var rows int64
	if err := db.Raw(`SELECT count(*) FROM user_visited_clients WHERE user_id = ?`, f.userID).Scan(&rows).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("期望 1 行（唯一约束 + upsert），实际 %d 行", rows)
	}
}

// 活跃流读取：没有时返回 (nil, nil)（不是错误），有时返回 id/target/amr/expires。
func TestDBFindActiveFlow(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L3")
	ctx := context.Background()
	repo := NewFlows(db)

	if flow, err := repo.FindActiveFlow(ctx, f.userID.String(), f.clientID); err != nil || flow != nil {
		t.Fatalf("没有活跃流时期望 (nil,nil)，实际 (%v,%v)", flow, err)
	}

	flowID := mustV7(t)
	expires := time.Now().UTC().Add(8 * time.Minute).Truncate(time.Microsecond)
	if err := db.Exec(`INSERT INTO flows (id, user_id, session_id, client_id, target_acr, achieved_amr, expires_at)
		VALUES (?, ?, ?, ?, 'AAL2', '{pwd}', ?)`,
		flowID, f.userID, f.session, f.clientID, expires).Error; err != nil {
		t.Fatalf("insert flow: %v", err)
	}

	flow, err := repo.FindActiveFlow(ctx, f.userID.String(), f.clientID)
	if err != nil {
		t.Fatalf("FindActiveFlow: %v", err)
	}
	if flow == nil || flow.ID != flowID.String() || flow.TargetACR != "AAL2" || len(flow.AchievedAMR) != 1 {
		t.Fatalf("活跃流不符：%+v", flow)
	}
}

// 会话状态（协议面用）：active 与"不存在"两态。
func TestDBSessionStatusAdapter(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	repo := NewSessionStatus(db)
	ctx := context.Background()

	status, err := repo.SessionStatus(ctx, f.session.String())
	if err != nil || status != "active" {
		t.Fatalf("期望 (active,nil)，实际 (%q,%v)", status, err)
	}
	if _, err := repo.SessionStatus(ctx, uuid.NewString()); !errors.Is(err, protocol.ErrSessionGone) {
		t.Fatalf("不存在期望 protocol.ErrSessionGone，实际 %v", err)
	}
}
