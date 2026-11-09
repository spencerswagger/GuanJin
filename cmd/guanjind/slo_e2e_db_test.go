//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/tjfoc/gmsm/sm2"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/gateway"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/signing"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是 **`AC-DP-12` 送达半边的端到端证据**（真 PG17 `guanjin_test` + 假上游 + 真 SM2 签名）：
//
//	撤销（走 Core 的 `session.Manager.cascade`）→ `session.RevokeNotifier`（Core 的挂点）
//	→ 本仓装配层 `sloRevokeNotifier` → `gateway.HTTPNotifier`（R12=A）→ 业务系统上游。
//
// 判据要的两件事都在这里证：
//  1. **送达**：假上游收到 **1 条** POST，路径 `/internal/guanjin/v1/logout`，
//     带 `X-GuanJin-Assertion`，用 **JWKS 公钥**验签通过，`aud/sub/exp-iat==60/jti` 齐（十项 claim）；
//  2. **失败不阻塞**：上游一律 500 时，**撤销照常成功且快速返回**（`<500ms`），不因通知失败回滚。

// sloUpstream 是"业务系统"的最小仿真：记录请求并可按需失败。
type sloUpstream struct {
	srv      *httptest.Server
	mu       sync.Mutex
	paths    []string
	headers  []http.Header
	failWith int
	// slow 让投递变慢，用于证"撤销不等投递"。
	slow time.Duration
}

func newSLOUpstream(t *testing.T, failWith int, slow time.Duration) *sloUpstream {
	t.Helper()
	u := &sloUpstream{failWith: failWith, slow: slow}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.paths = append(u.paths, r.URL.Path)
		u.headers = append(u.headers, r.Header.Clone())
		fail := u.failWith
		slow := u.slow
		u.mu.Unlock()
		if slow > 0 {
			time.Sleep(slow)
		}
		if fail != 0 {
			w.WriteHeader(fail)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *sloUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.paths)
}

func (u *sloUpstream) firstPath() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.paths) == 0 {
		return ""
	}
	return u.paths[0]
}

func (u *sloUpstream) assertion(t *testing.T) string {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.headers) == 0 {
		return ""
	}
	for k, v := range u.headers[0] {
		if strings.EqualFold(k, gateway.AssertionHeader) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// sloFixture 建一套隔离数据：client(路由) + user + role_binding + session。
type sloFixture struct {
	clientID  string
	userID    uuid.UUID
	sessionID uuid.UUID
}

func newSLOFixture(t *testing.T, db *gorm.DB, upstream string) *sloFixture {
	t.Helper()
	suffix := uuid.NewString()[:8]
	f := &sloFixture{clientID: "s10-" + suffix, userID: mustV7ForSLO(t), sessionID: mustV7ForSLO(t)}
	now := time.Now().UTC()
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier, redirect_uris) VALUES (?, ?, 'L3', '{}')`,
		f.clientID, "S10 SLO 夹具").Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		f.userID, "s10-"+suffix).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := db.Exec(`INSERT INTO role_bindings (id, user_id, client_id, role) VALUES (?, ?, ?, 'viewer')`,
		mustV7ForSLO(t), f.userID, f.clientID).Error; err != nil {
		t.Fatalf("insert role: %v", err)
	}
	if err := db.Exec(`INSERT INTO sessions
		(id, user_id, client_id, acr, amr, status, issued_at, expires_at, idle_expires_at)
		VALUES (?, ?, ?, 'AAL1', '{pwd}', 'active', ?, ?, ?)`,
		f.sessionID, f.userID, f.clientID, now, now.Add(2*time.Hour), now.Add(time.Hour)).Error; err != nil {
		t.Fatalf("insert session: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM role_bindings WHERE user_id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM sessions WHERE user_id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, f.userID).Error
	})
	// 该业务系统专属策略：`client_override`（`all_clients` 级联范围 + 明确的行，不依赖 seed）。
	if err := db.Exec(`INSERT INTO login_policies (id, scope, client_id, min_acr, sso_logout_scope)
		VALUES (?, 'client_override', ?, 'AAL1', 'current_client')`,
		mustV7ForSLO(t), f.clientID).Error; err != nil {
		t.Fatalf("insert login_policies: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM login_policies WHERE client_id = ?`, f.clientID).Error
	})
	_ = upstream
	return f
}

func mustV7ForSLO(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	return id
}

// sloKeys 生成一把 gateway 用途的真密钥（断言必须真签、真能被 JWKS 验）。
func sloKeys(t *testing.T) *signing.Set {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 3)
	}
	master, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	keywrap, err := master.DeriveSM4(crypto.PurposeSM4Keywrap)
	if err != nil {
		t.Fatalf("DeriveSM4: %v", err)
	}
	oidc, err := signing.GenRow(rand.Reader, keywrap, "slo-oidc", signing.PurposeOIDC, signing.AlgSM3WithSM2)
	if err != nil {
		t.Fatalf("GenRow(oidc): %v", err)
	}
	gw, err := signing.GenRow(rand.Reader, keywrap, "slo-gw", signing.PurposeGateway, signing.AlgSM3WithSM2)
	if err != nil {
		t.Fatalf("GenRow(gateway): %v", err)
	}
	set, err := signing.Load(keywrap, []signing.Row{oidc, gw})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return set
}

// newSLORevoker 装出"管理器 + 本仓装配层"，返回撤销入口。
func newSLORevoker(t *testing.T, db *gorm.DB, cfg *config.Config, keys *signing.Set,
	routesJSON string, logger *slog.Logger) *session.Manager {
	t.Helper()
	t.Setenv("GUANJIN_GATEWAY_ROUTES", routesJSON)
	notifier, err := newSLORevokeNotifier(db, cfg, keys, logger)
	if err != nil {
		t.Fatalf("newSLORevokeNotifier: %v", err)
	}
	mgr, err := session.NewManager(store.NewSessionOpsRepo(db), store.NewFlowRepo(db),
		store.NewAuthEventRepo(db), session.Options{Logger: logger, Notifier: notifier})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

// AC-DP-12 送达半边：撤销 → 假上游收到 1 条，断言可被 JWKS 验签、十项 claim 齐。
func TestAC_DP_12_DeliversSignedNotice(t *testing.T) {
	db := openTestDB(t)
	up := newSLOUpstream(t, 0, 0)
	f := newSLOFixture(t, db, up.srv.URL)

	cfg := testConfig(t)
	keys := sloKeys(t)
	routes := fmt.Sprintf(`[{"prefix":"/api/x","client_id":%q,"upstream":%q}]`, f.clientID, up.srv.URL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := newSLORevoker(t, db, cfg, keys, routes, logger)

	ctx := context.Background()
	if err := mgr.Revoke(ctx, session.RevokeInput{
		SessionID: f.sessionID, Reason: session.ReasonLogout, EventType: "logout",
	}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// 投递是异步的：轮询等它到（最多 3s；正常 <100ms）。
	deadline := time.Now().Add(3 * time.Second)
	for up.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if up.count() != 1 {
		t.Fatalf("期望上游收到恰好 1 条通知，实际 %d", up.count())
	}
	if got := up.firstPath(); got != gateway.DefaultSloNotifyPath {
		t.Fatalf("投递路径期望 %q，实际 %q", gateway.DefaultSloNotifyPath, got)
	}

	claims := verifySLOAssertion(t, keys, up.assertion(t))
	for _, name := range []string{"iss", "sub", "aud", "cid", "uname", "roles", "acr", "amr", "iat", "exp", "jti"} {
		if _, ok := claims[name]; !ok {
			t.Fatalf("通知断言缺 claim %q：%v", name, claims)
		}
	}
	if claims["sub"] != f.userID.String() {
		t.Fatalf("sub 期望 %q，实际 %v", f.userID, claims["sub"])
	}
	if claims["aud"] != f.clientID {
		t.Fatalf("aud 期望目标业务系统 %q，实际 %v", f.clientID, claims["aud"])
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if int64(exp)-int64(iat) != 60 {
		t.Fatalf("exp-iat 期望 60，实际 %d", int64(exp)-int64(iat))
	}
	if jti, _ := claims["jti"].(string); len(jti) != 36 {
		t.Fatalf("jti 期望 UUID，实际 %q", jti)
	}

	// 会话确实被撤销（通知是副作用，不是撤销本身）。
	var status string
	if err := db.Raw(`SELECT status FROM sessions WHERE id = ?`, f.sessionID).Scan(&status).Error; err != nil {
		t.Fatalf("read session: %v", err)
	}
	if status != model.SessionStatusRevoked {
		t.Fatalf("会话状态期望 revoked，实际 %q", status)
	}
}

// AC-DP-12 失败隔离（端到端）：上游一律 500 + 慢投递，撤销仍**快速成功**、不回滚。
func TestAC_DP_12_UpstreamFailureDoesNotBlockRevocation(t *testing.T) {
	db := openTestDB(t)
	// 上游 500 且每次慢 300ms：若撤销在等投递，这里会明显超时。
	up := newSLOUpstream(t, http.StatusInternalServerError, 300*time.Millisecond)
	f := newSLOFixture(t, db, up.srv.URL)

	cfg := testConfig(t)
	keys := sloKeys(t)
	routes := fmt.Sprintf(`[{"prefix":"/api/x","client_id":%q,"upstream":%q}]`, f.clientID, up.srv.URL)
	mgr := newSLORevoker(t, db, cfg, keys, routes, slog.New(slog.NewTextHandler(io.Discard, nil)))

	start := time.Now()
	if err := mgr.Revoke(context.Background(), session.RevokeInput{
		SessionID: f.sessionID, Reason: session.ReasonLogout, EventType: "logout",
	}); err != nil {
		t.Fatalf("通知失败不得影响撤销：%v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("撤销被通知拖慢：耗时 %s（判据要求失败不阻塞）", elapsed)
	}

	var status string
	if err := db.Raw(`SELECT status FROM sessions WHERE id = ?`, f.sessionID).Scan(&status).Error; err != nil {
		t.Fatalf("read session: %v", err)
	}
	if status != model.SessionStatusRevoked {
		t.Fatalf("撤销必须已提交（status=%q）", status)
	}
}

// verifySLOAssertion 用 JWKS 公钥验签（与业务系统 SDK 同一路径）。
func verifySLOAssertion(t *testing.T, keys *signing.Set, token string) map[string]any {
	t.Helper()
	if token == "" {
		t.Fatal("上游没收到断言头")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("断言不是三段式：%q", token)
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header struct {
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hb, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	for _, jwk := range keys.JWKS() {
		if jwk.Kid != header.Kid {
			continue
		}
		x, _ := base64.RawURLEncoding.DecodeString(jwk.X)
		y, _ := base64.RawURLEncoding.DecodeString(jwk.Y)
		pub := &sm2.PublicKey{Curve: sm2.P256Sm2(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		claims, err := signing.VerifyJWT(pub, token)
		if err != nil {
			t.Fatalf("JWKS 验签失败：%v", err)
		}
		return claims
	}
	t.Fatalf("JWKS 里没有 kid=%q", header.Kid)
	return nil
}
