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
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/store"
)

// AC-FLOW-09（Spec 冻结版，2026-11-10）的**逐条证据**，方法名前缀 `ACFLOW09_NN_`。
//
// 判据（无上下文按生效配置）四条 + 两条裁定：
//  1. L3 访问过 + AAL1 会话 → 403 `ACR_INSUFFICIENT`，body 含 code+trace_id，`identifiers` 0 新增；
//     目标 = `max(active client tier)` 对应的 `tier_default.min_acr`，且**同一用例内**断言它
//     等于索引 §2 #15 映射（L1/L2→AAL1、L3→AAL2）。
//  2. AAL2 → 2xx 且 `identifiers` 新增 1 行 `verified=false`；token 里 acr 篡改成 AAL2 但库中
//     会话是 AAL1 → **仍 403**（级别只信 `sessions.acr` 且 `status='active'`）。
//  3. 无访问记录 → 回退 L2→AAL1；关联 client 全 disabled → 同一回退；DB 查询失败 → 503
//     `DB_UNAVAILABLE`（"算不出门槛就放行" = 缺陷）。
//  4. 并发 10 次 403 → 全 403、`identifiers` 0 行、`user_visited_clients` 0 新增。
//
// 裁定：**只算 `clients.status='active'`**；**用 `clients.tier` 现值 join，不用 `tier_at_visit`**。
//
// 不改实现：Spec 实读 `origin/main` 后认定实现与判据一致，本文件只补"按 AC 命名"的证据。

// acflow09Handler 装出**真实现**的 `POST /api/v1/me/identifiers`：
// binder 用真 `Service`（要断言 `identifiers` 真新增/不新增，不能用桩），
// 会话读真仓储，门槛走真 `EffectiveACRRepo`。
func acflow09Handler(t *testing.T, db *gorm.DB, binder IdentifierBinder) *MeIdentifiersHTTP {
	t.Helper()
	visited, err := store.NewEffectiveACRRepo(db)
	if err != nil {
		t.Fatalf("NewEffectiveACRRepo: %v", err)
	}
	gate, err := NewACRGate(visited, store.PlatformDefaultTier)
	if err != nil {
		t.Fatalf("NewACRGate: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	h, err := NewMeIdentifiersHTTP(binder, store.NewSessionOpsRepo(db), gate,
		okResolver{}, logger)
	if err != nil {
		t.Fatalf("NewMeIdentifiersHTTP: %v", err)
	}
	return h
}

// acflow09Binder 用真 `Service`（真口令派生、真事务），并暴露"绑了几次"。
func acflow09Binder(t *testing.T, db *gorm.DB) *Service {
	t.Helper()
	svc, err := New(db, testConfig(t))
	if err != nil {
		t.Fatalf("identity.New: %v", err)
	}
	return svc
}

// doBind 打一次绑定请求（可选注入 claims/eacr 以模拟"令牌被篡改"）。
func doBind(t *testing.T, h *MeIdentifiersHTTP, resolver session.TokenResolver, trace, body string) (int, []byte) {
	t.Helper()
	h.resolver = resolver
	req := httptest.NewRequest(http.MethodPost, "/api/v1/me/identifiers", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer good-token")
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(httpx.WithTraceID(req.Context(), trace))
	rec := httptest.NewRecorder()
	h.bind(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// claimsResolver 是"令牌里多带了一份 acr 声明"的解析器：用来证明 handler **不读** claims。
type claimsResolver struct {
	userID, sessionID uuid.UUID
	claims            map[string]any
}

func (r claimsResolver) Resolve(context.Context, string) (httpx.Caller, error) {
	return httpx.Caller{Sub: r.userID.String(), SID: r.sessionID.String(), Claims: r.claims}, nil
}

func countIdentifiers(t *testing.T, db *gorm.DB, userID uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM identifiers WHERE user_id = ?`, userID).Scan(&n).Error; err != nil {
		t.Fatalf("count identifiers: %v", err)
	}
	return n
}

func countVisits(t *testing.T, db *gorm.DB, userID uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM user_visited_clients WHERE user_id = ?`, userID).Scan(&n).Error; err != nil {
		t.Fatalf("count visits: %v", err)
	}
	return n
}

// tierDefaultACR 读 `login_policies(scope='tier_default')` 的 tier → min_acr 映射（落地值）。
func tierDefaultACR(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	rows, err := db.Raw(`SELECT tier, min_acr FROM login_policies WHERE scope = 'tier_default'`).Rows()
	if err != nil {
		t.Fatalf("读 tier_default：%v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var tier, acr string
		if err := rows.Scan(&tier, &acr); err != nil {
			t.Fatalf("scan tier_default：%v", err)
		}
		out[tier] = acr
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("tier_default rows：%v", err)
	}
	return out
}

// ACFLOW09_01：L3 访问 + AAL1 → 403 `ACR_INSUFFICIENT`、0 新增、目标=映射值。
func TestACFLOW09_01_L3VisitBlocksAAL1WithZeroInsert(t *testing.T) {
	db := openFlow09DB(t)
	f := newFlow09Fixture(t, db, "AAL1")
	h := acflow09Handler(t, db, acflow09Binder(t, db))
	before := countIdentifiers(t, db, f.userID)

	const trace = "trace-acflow09-01"
	status, body := doBind(t, h, okResolver{userID: f.userID, sessionID: f.sessionID}, trace,
		`{"type":"email","value":"acflow09-01@example.com"}`)

	// ① 只允许 403：200/302/401/500 都算缺陷（判据原文）。
	if status != http.StatusForbidden {
		t.Fatalf("L3 访问后 AAL1 无上下文绑定应 403，实际 %d（body=%s）", status, body)
	}
	var env httpx.ErrorResponse
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("错误响应不是统一四字段：%v（body=%s）", err, body)
	}
	if env.Code != httpx.CodeACRInsufficient {
		t.Fatalf("code 应为 %s，实际 %q", httpx.CodeACRInsufficient, env.Code)
	}
	if env.TraceID != trace {
		t.Fatalf("body 必须含可复制 trace_id（期望 %q），实际 %q", trace, env.TraceID)
	}

	// ② 目标级别 = max(active tier) 对应的 tier_default.min_acr，且等于索引 §2 #15 映射。
	defaults := tierDefaultACR(t, db)
	want := map[string]string{"L1": "AAL1", "L2": "AAL1", "L3": "AAL2"}
	for tier, acr := range want {
		if got, ok := defaults[tier]; ok && got != acr {
			t.Fatalf("tier_default(%s).min_acr 应为 %s（索引 §2 #15），实际 %s", tier, acr, got)
		}
	}
	if defaults["L3"] == "" {
		t.Fatal("缺 tier_default(L3) 行：迁移 000017 应已 seed")
	}
	details, _ := env.Details.(map[string]any)
	if details["target_acr"] != defaults["L3"] {
		t.Fatalf("details.target_acr 应为 L3 的 min_acr=%q，实际 %v", defaults["L3"], details["target_acr"])
	}
	if details["current_acr"] != "AAL1" {
		t.Fatalf("details.current_acr 应来自 sessions.acr=AAL1，实际 %v", details["current_acr"])
	}
	if _, hasFlow := details["flow"]; hasFlow {
		t.Fatal("无 client 上下文不得带 details.flow（D18 草案）")
	}

	// ③ 零新增。
	if after := countIdentifiers(t, db, f.userID); after != before {
		t.Fatalf("被拒时 identifiers 不得新增：before=%d after=%d", before, after)
	}
}

// ACFLOW09_02：AAL2 → 2xx + 新增 1 行 verified=false；令牌 claims 里的 acr 不被采信。
func TestACFLOW09_02_AAL2WritesOneUnverifiedRow_ClaimIgnored(t *testing.T) {
	db := openFlow09DB(t)
	f := newFlow09Fixture(t, db, "AAL2")
	h := acflow09Handler(t, db, acflow09Binder(t, db))
	before := countIdentifiers(t, db, f.userID)

	status, body := doBind(t, h, okResolver{userID: f.userID, sessionID: f.sessionID},
		"trace-acflow09-02a", `{"type":"email","value":"acflow09-02@example.com"}`)
	if status != http.StatusCreated {
		t.Fatalf("AAL2 会话应放行（201），实际 %d（body=%s）", status, body)
	}
	if after := countIdentifiers(t, db, f.userID); after != before+1 {
		t.Fatalf("放行后 identifiers 应新增 1 行：before=%d after=%d", before, after)
	}
	var verified bool
	if err := db.Raw(`SELECT verified FROM identifiers WHERE user_id = ? ORDER BY created_at DESC LIMIT 1`,
		f.userID).Scan(&verified).Error; err != nil {
		t.Fatalf("读新绑定行：%v", err)
	}
	if verified {
		t.Fatal("自助绑定的新标识符必须是 verified=false（验证另走 verify 端点）")
	}

	// 令牌里塞 acr=AAL2，但库中会话只有 AAL1 → 仍必须 403（级别只信 sessions.acr）。
	other := newFlow09Fixture(t, db, "AAL1")
	before2 := countIdentifiers(t, db, other.userID)
	status2, body2 := doBind(t,
		h, claimsResolver{userID: other.userID, sessionID: other.sessionID,
			claims: map[string]any{"acr": "AAL2", "amr": []string{"pwd", "totp"}}},
		"trace-acflow09-02b", `{"type":"email","value":"acflow09-02b@example.com"}`)
	if status2 != http.StatusForbidden {
		t.Fatalf("令牌 claims 里写 acr=AAL2 也必须 403（不得采信客户端自报），实际 %d（body=%s）",
			status2, body2)
	}
	if got := countIdentifiers(t, db, other.userID); got != before2 {
		t.Fatalf("被拒时不得新增 identifiers：before=%d after=%d", before2, got)
	}
}

// ACFLOW09_03：三种回退 + DB 故障 → 503（"算不出门槛就放行" = 缺陷）。
func TestACFLOW09_03_FallbacksAndDBDown503(t *testing.T) {
	db := openFlow09DB(t)
	binder := acflow09Binder(t, db)

	// ① 无访问记录 → 回退平台默认 L2 → AAL1：AAL1 可写。
	userA, sessA := acflow09User(t, db, "AAL1", 0)
	h := acflow09Handler(t, db, binder)
	status, body := doBind(t, h, okResolver{userID: userA, sessionID: sessA},
		"trace-acflow09-03a", `{"type":"email","value":"acflow09-03a@example.com"}`)
	if status != http.StatusCreated {
		t.Fatalf("无访问记录应回退 AAL1 并放行（201），实际 %d（body=%s）", status, body)
	}

	// ② 有访问记录但关联 client 全部 disabled → 同一回退（裁定：只算 status='active'）。
	userB, sessB := acflow09User(t, db, "AAL1", 1)
	if err := db.Exec(`UPDATE clients SET status = 'disabled' WHERE client_id IN
		(SELECT client_id FROM user_visited_clients WHERE user_id = ?)`, userB).Error; err != nil {
		t.Fatalf("停用 client：%v", err)
	}
	status, body = doBind(t, h, okResolver{userID: userB, sessionID: sessB},
		"trace-acflow09-03b", `{"type":"email","value":"acflow09-03b@example.com"}`)
	if status != http.StatusCreated {
		t.Fatalf("client 全停用应同样回退 AAL1（201），实际 %d（body=%s）", status, body)
	}

	// ③ DB 查询失败 → 503 `DB_UNAVAILABLE`，**不得**降级放行。
	//    用死端口造真连接失败（#63 的口径：拨号失败 = 依赖不可用 = 503）。
	dead, err := store.Open(context.Background(), store.Options{
		DSN: "postgres://nobody:nobody@127.0.0.1:1/guanjin_test?sslmode=disable&connect_timeout=1",
	})
	if err != nil || dead == nil {
		t.Fatalf("构造死库句柄：%v", err)
	}
	deadHandler := acflow09Handler(t, dead, binder)
	// 会话读取仍走真库：否则 401 会先盖住 503（本判据要的是"门槛算不出来"这条路径）。
	deadHandler.sessions = store.NewSessionOpsRepo(db)
	status, body = doBind(t, deadHandler, okResolver{userID: userA, sessionID: sessA},
		"trace-acflow09-03c", `{"type":"email","value":"acflow09-03c@example.com"}`)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("DB 不可用应 503，实际 %d（body=%s）—— 降级放行或 500 都算缺陷", status, body)
	}
	var env httpx.ErrorResponse
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode：%v（body=%s）", err, body)
	}
	if env.Code != httpx.CodeDBUnavailable {
		t.Fatalf("code 应为 %s，实际 %q", httpx.CodeDBUnavailable, env.Code)
	}
}

// ACFLOW09_04：并发 10 次 403 —— 全 403、0 新增、访问记录不增。
func TestACFLOW09_04_Concurrent403NoInserts(t *testing.T) {
	db := openFlow09DB(t)
	f := newFlow09Fixture(t, db, "AAL1")
	h := acflow09Handler(t, db, acflow09Binder(t, db))
	beforeIdent := countIdentifiers(t, db, f.userID)
	beforeVisits := countVisits(t, db, f.userID)

	const n = 10
	var wg sync.WaitGroup
	codes := make([]int, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			status, _ := doBind(t, h, okResolver{userID: f.userID, sessionID: f.sessionID},
				"trace-acflow09-04-"+uuid.NewString()[:8],
				`{"type":"email","value":"acflow09-04-`+uuid.NewString()[:8]+`@example.com"}`)
			codes[i] = status
		}(i)
	}
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusForbidden {
			t.Fatalf("第 %d 个并发请求应为 403，实际 %d", i, c)
		}
	}
	if after := countIdentifiers(t, db, f.userID); after != beforeIdent {
		t.Fatalf("并发被拒后 identifiers 不得新增：before=%d after=%d", beforeIdent, after)
	}
	if after := countVisits(t, db, f.userID); after != beforeVisits {
		t.Fatalf("只读门槛判定不得写 user_visited_clients：before=%d after=%d", beforeVisits, after)
	}
}

// acflow09User 建一个用户 + 一条会话，并按 visits 数量造 L3 访问记录（1 → 抬到 AAL2）。
func acflow09User(t *testing.T, db *gorm.DB, acr string, visits int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	suffix := uuid.NewString()
	userID, sessionID := flow09UUIDv7(t), flow09UUIDv7(t)
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		userID, "acflow09-"+suffix[:10]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	// 会话必须有 client_id（NOT NULL）：无论有没有访问记录都建一个 client，
	// 只有 visits>0 时才写 `user_visited_clients`（无访问记录 = 回退分支的前提）。
	clientID := "acflow09-" + uuid.NewString()
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier, status) VALUES (?, ?, 'L3', 'active')`,
		clientID, "acflow09 client").Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	clientIDs := []string{clientID}
	if visits > 0 {
		if err := db.Exec(`INSERT INTO user_visited_clients
			(id, user_id, client_id, tier_at_visit, visit_count, first_seen_at, last_seen_at)
			VALUES (?, ?, ?, 'L3', 1, now(), now())`,
			flow09UUIDv7(t), userID, clientID).Error; err != nil {
			t.Fatalf("insert visit: %v", err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := db.Exec(`INSERT INTO sessions
		(id, user_id, client_id, acr, amr, status, issued_at, expires_at, idle_expires_at)
		VALUES (?, ?, ?, ?, '{pwd}', 'active', ?, ?, ?)`,
		sessionID, userID, clientID, acr, now, now.Add(time.Hour), now.Add(30*time.Minute)).Error; err != nil {
		t.Fatalf("insert session: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM identifiers WHERE user_id = ?`, userID).Error
		_ = db.Exec(`DELETE FROM user_visited_clients WHERE user_id = ?`, userID).Error
		_ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, userID).Error
		for _, c := range clientIDs {
			_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, c).Error
		}
	})
	return userID, sessionID
}
