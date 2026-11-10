//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/testdb"
)

// 本文件是 **`AC-FLOW-09` 的最小断言**（真 PG17）：
//
//	造一次 L3 访问记录 → 用 **AAL1** 会话调 `POST /api/v1/me/identifiers`
//	→ **403 `ACR_INSUFFICIENT`**（M1 只有密码因子，AAL2 物理不可达 → 403 才是正确结果）
//
// 门禁用**真仓储**（`store.EffectiveACRRepo` + `store.SessionOpsRepo`），不吃桩：
// 这条判据要证明的正是"访问过 L3 会把无上下文请求的门槛抬到 AAL2"。

func openFlow09DB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
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
	// 本用例会插/删自建夹具（#74 约定：删改数据的测试先过 RequireTestDB）。
	testdb.RequireTestDB(t, db)
	t.Cleanup(func() { _ = store.Close(db) })
	return db
}

type flow09Fixture struct {
	userID    uuid.UUID
	sessionID uuid.UUID
	clientID  string
}

func newFlow09Fixture(t *testing.T, db *gorm.DB, sessionACR string) *flow09Fixture {
	t.Helper()
	suffix := uuid.NewString()
	f := &flow09Fixture{
		userID:    flow09UUIDv7(t),
		sessionID: flow09UUIDv7(t),
		clientID:  "flow09-" + suffix,
	}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		f.userID, "flow09-"+suffix[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// L3 业务系统（active）：访问它之后，无上下文门槛应为 AAL2。
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier, status) VALUES (?, ?, 'L3', 'active')`,
		f.clientID, "flow09 L3 client").Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := db.Exec(`INSERT INTO sessions
		(id, user_id, client_id, acr, amr, status, issued_at, expires_at, idle_expires_at)
		VALUES (?, ?, ?, ?, '{pwd}', 'active', ?, ?, ?)`,
		f.sessionID, f.userID, f.clientID, sessionACR, now, now.Add(time.Hour), now.Add(30*time.Minute)).Error; err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if err := db.Exec(`INSERT INTO user_visited_clients
		(id, user_id, client_id, tier_at_visit, visit_count, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, 'L3', 1, now(), now())`,
		flow09UUIDv7(t), f.userID, f.clientID).Error; err != nil {
		t.Fatalf("insert visit: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM user_visited_clients WHERE user_id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, f.clientID).Error
	})
	return f
}

func flow09UUIDv7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	return id
}

// newFlow09Handler 用**真仓储**装出端点；binder 用桩（本判据不验绑定本身）。
func newFlow09Handler(t *testing.T, db *gorm.DB, userID uuid.UUID) (*MeIdentifiersHTTP, *fakeBinder) {
	t.Helper()
	visited, err := store.NewEffectiveACRRepo(db)
	if err != nil {
		t.Fatalf("NewEffectiveACRRepo: %v", err)
	}
	gate, err := NewACRGate(visited, store.PlatformDefaultTier)
	if err != nil {
		t.Fatalf("NewACRGate: %v", err)
	}
	binder := &fakeBinder{}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	h, err := NewMeIdentifiersHTTP(binder, store.NewSessionOpsRepo(db), gate,
		okResolver{userID: userID, sessionID: uuid.Nil}, logger)
	if err != nil {
		t.Fatalf("NewMeIdentifiersHTTP: %v", err)
	}
	return h, binder
}

// 最小断言：L3 访问 + AAL1 会话 → 403 `ACR_INSUFFICIENT`，且 details 无 flow。
func TestFlow09L3VisitBlocksAAL1Session(t *testing.T) {
	db := openFlow09DB(t)
	f := newFlow09Fixture(t, db, "AAL1")
	h, binder := newFlow09Handler(t, db, f.userID)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/identifiers",
		strings.NewReader(`{"type":"email","value":"flow09@example.com"}`))
	req.Header.Set("Authorization", "Bearer good-token")
	req = req.WithContext(httpx.WithTraceID(req.Context(), "trace-flow09-e2e"))
	// 用**真会话 id** 替换桩里的 Nil（handler 会按它读 `sessions.acr`）。
	h.resolver = okResolver{userID: f.userID, sessionID: f.sessionID}

	rec := httptest.NewRecorder()
	h.bind(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403（L3 访问后无上下文门槛为 AAL2，AAL1 会话必须被拒）: %s",
			rec.Code, rec.Body.String())
	}
	var env httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != httpx.CodeACRInsufficient {
		t.Fatalf("code = %q, want %q", env.Code, httpx.CodeACRInsufficient)
	}
	details, _ := env.Details.(map[string]any)
	if details["current_acr"] != "AAL1" || details["target_acr"] != "AAL2" {
		t.Fatalf("details 应为平铺 current=AAL1/target=AAL2，实际 %v", env.Details)
	}
	if _, hasFlow := details["flow"]; hasFlow {
		t.Fatal("无 client 上下文不得带 details.flow（见 D18 草案）")
	}
	if len(binder.calls) != 0 {
		t.Fatal("被拒时不得执行绑定")
	}
}

// 正控：**没有访问记录**的用户 → 回退平台默认 L2 → AAL1，AAL1 会话达标（不被拒）。
//
// 没有这条，"403" 可能只是"所有请求都被拒"，证明不了门槛是按访问记录算出来的。
func TestFlow09NoVisitsFallsBackToPlatformDefault(t *testing.T) {
	db := openFlow09DB(t)
	// 直接建用户 + AAL1 会话，**不写** user_visited_clients。
	suffix := uuid.NewString()
	userID, sessionID := flow09UUIDv7(t), flow09UUIDv7(t)
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`, userID, "flow09n-"+suffix[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	clientID := "flow09n-" + suffix
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier, status) VALUES (?, ?, 'L3', 'active')`,
		clientID, "flow09n client").Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := db.Exec(`INSERT INTO sessions
		(id, user_id, client_id, acr, amr, status, issued_at, expires_at, idle_expires_at)
		VALUES (?, ?, ?, 'AAL1', '{pwd}', 'active', ?, ?, ?)`,
		sessionID, userID, clientID, now, now.Add(time.Hour), now.Add(30*time.Minute)).Error; err != nil {
		t.Fatalf("insert session: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, userID).Error
		_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, clientID).Error
	})

	h, binder := newFlow09Handler(t, db, userID)
	h.resolver = okResolver{userID: userID, sessionID: sessionID}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/identifiers",
		strings.NewReader(`{"type":"email","value":"flow09n@example.com"}`))
	req.Header.Set("Authorization", "Bearer good-token")
	req = req.WithContext(httpx.WithTraceID(req.Context(), "trace-flow09-fallback"))

	rec := httptest.NewRecorder()
	h.bind(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("无访问记录应回退 L2→AAL1 并放行（201），实际 %d: %s", rec.Code, rec.Body.String())
	}
	if len(binder.calls) != 1 || binder.calls[0].UserID != userID {
		t.Fatalf("应绑定一次且主体为令牌用户，实际 %+v", binder.calls)
	}
}
