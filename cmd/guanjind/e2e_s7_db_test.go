//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/signing"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是 **S7 的真令牌端到端证据**（Owl 对 #47 的唯一必改项）：
//
//	真签名令牌 → `GET /api/v1/me/sessions` 200（`current=true`）→ `DELETE` 204 → 复查 403。
//
// **不 mock 任何一环**：令牌用 `signing.SignJWT` 真签（SM3withSM2），验签走 S6 的
// `protocol.CallerResolver`（验签 + exp/nbf + 查会话状态），撤销走真实的 `sessions` 表。
// 只有"签发"这一步用测试内的签名键（登录链路的正式签发在 S6 的 `/oauth/token`，
// 需要授权码与流；这里要证的是**撤销之后令牌失效**，签发来源不影响该结论）。
//
// 为什么复查断言 403 `SESSION_REVOKED` 而不是 401：D3-E 把"会话被撤销"定为 E3/403
// （`AC-SESS-01`①），401 留给"令牌本身不可信"（`TOKEN_INVALID`）。两种都在本文件里有断言。

func TestE2E_SessionRevokeMakesTokenUseless(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	cfg := testConfig(t)

	// 1) 签名键：`oidc` + `gateway` 各一把（与 `guanjind keygen` 的目标一致）。
	keywrap, err := cfg.MasterKey.DeriveSM4(crypto.PurposeSM4Keywrap)
	if err != nil {
		t.Fatalf("derive keywrap: %v", err)
	}
	repo := store.NewSigningKeyRepo(db)
	suffix := uuid.NewString()[:8]
	for _, target := range []struct{ kid, purpose, algorithm string }{
		{"oidc-e2e-" + suffix, signing.PurposeOIDC, signing.AlgSM3WithSM2},
		{"gateway-e2e-" + suffix, signing.PurposeGateway, signing.AlgSM2},
	} {
		row, err := signing.GenRow(rand.Reader, keywrap, target.kid, target.purpose, target.algorithm)
		if err != nil {
			t.Fatalf("GenRow: %v", err)
		}
		if _, err := repo.InsertIfAbsent(ctx, store.SigningKeyRow{
			Kid: row.Kid, Purpose: row.Purpose, Algorithm: row.Algorithm,
			PublicKey: row.PublicKey, PrivateEnc: row.PrivateEnc,
		}); err != nil {
			t.Fatalf("InsertIfAbsent: %v", err)
		}
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM signing_keys WHERE kid LIKE ?`, "%-e2e-"+suffix).Error
	})

	// 2) 隔离的业务系统 + 用户 + 会话行（会话由 S6 建，这里模拟"已登录"的既成事实）。
	clientID := "s7-e2e-" + suffix
	userID := mustV7(t)
	sessionID := mustV7(t)
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier) VALUES (?, ?, 'L2')`,
		clientID, "S7 e2e client").Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`, userID, "s7-"+suffix).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := db.Exec(`INSERT INTO sessions
		(id, user_id, client_id, acr, amr, status, issued_at, expires_at, idle_expires_at, ip, user_agent, device_id)
		VALUES (?, ?, ?, 'AAL2', '{pwd,totp}', 'active', now(), now() + interval '1 hour',
		        now() + interval '30 min', '203.0.113.42', 'e2e-agent', 'e2e-device')`,
		sessionID, userID, clientID).Error; err != nil {
		t.Fatalf("insert session: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM auth_events WHERE user_id = ?`, userID).Error
		_ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, userID).Error
		_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, clientID).Error
	})

	// 3) 装配（真实的 `newSessionWiring`：真 resolver + 真仓储），挂到 mux 上跑 HTTP。
	sessRoutes, adminRoutes, err := newSessionWiring(ctx, db, cfg, discardLogger())
	if err != nil {
		t.Fatalf("newSessionWiring: %v", err)
	}
	mux := http.NewServeMux()
	sessRoutes.Routes(mux)
	adminRoutes.Routes(mux)
	srv := httptest.NewServer(httpx.TraceMiddleware(mux, "X-Trace-Id", discardLogger()))
	t.Cleanup(srv.Close)

	// 4) 真签令牌（`sub`/`sid`/`roles` 由 claims 决定；验签与窗口由 S6 的 resolver 负责）。
	keys, err := loadSigningKeys(ctx, db, cfg)
	if err != nil {
		t.Fatalf("loadSigningKeys: %v", err)
	}
	key, err := keys.For(signing.PurposeOIDC)
	if err != nil {
		t.Fatalf("keys.For(oidc): %v", err)
	}
	now := time.Now().UTC()
	token, err := signing.SignJWT(key, map[string]any{
		"sub":   userID.String(),
		"sid":   sessionID.String(),
		"roles": []string{"security_admin"},
		"iat":   now.Unix(),
		"nbf":   now.Add(-time.Minute).Unix(),
		"exp":   now.Add(5 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}

	do := func(method, path, bearer string) (*http.Response, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp, body
	}

	// 5) 带真令牌 → 200，且列表里标记为 current。
	resp, body := do(http.MethodGet, "/api/v1/me/sessions", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("带令牌应 200，实际 %d（body=%v）", resp.StatusCode, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1（body=%v）", len(items), body)
	}
	item, _ := items[0].(map[string]any)
	if item["id"] != sessionID.String() {
		t.Fatalf("item.id = %v, want %s", item["id"], sessionID)
	}
	if item["current"] != true {
		t.Fatalf("current = %v, want true", item["current"])
	}
	if ip, _ := item["ip"].(string); ip != "203.0.***.***" {
		t.Fatalf("ip 脱敏失败：%v", item["ip"])
	}

	// 6) 撤销自己的会话 → 204。
	if resp, body := do(http.MethodDelete, "/api/v1/me/sessions/"+sessionID.String(), token); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("撤销应 204，实际 %d（body=%v）", resp.StatusCode, body)
	}

	// 7) 同一枚令牌复查 → 403 SESSION_REVOKED（撤销实时生效，无缓存窗口）。
	resp, body = do(http.MethodGet, "/api/v1/me/sessions", token)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("撤销后应 403，实际 %d（body=%v）", resp.StatusCode, body)
	}
	if code, _ := body["code"].(string); code != string(httpx.CodeSessionRevoked) {
		t.Fatalf("code = %v, want %s", body["code"], httpx.CodeSessionRevoked)
	}

	// 8) 反面：没有令牌 → 401 TOKEN_INVALID（与 403 分工照 D3-E）。
	resp, body = do(http.MethodGet, "/api/v1/me/sessions", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401，实际 %d", resp.StatusCode)
	}
	if code, _ := body["code"].(string); code != string(httpx.CodeTokenInvalid) {
		t.Fatalf("code = %v, want TOKEN_INVALID", body["code"])
	}

	// 9) 会话状态在库里确实是 revoked（不是只在内存里"看起来"撤销了）。
	var status string
	if err := db.Raw(`SELECT status FROM sessions WHERE id = ?`, sessionID).Scan(&status).Error; err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != model.SessionStatusRevoked {
		t.Fatalf("status = %q, want revoked", status)
	}
}

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	return id
}
