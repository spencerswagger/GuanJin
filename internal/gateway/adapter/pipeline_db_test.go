//go:build integration

package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tjfoc/gmsm/sm2"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/gateway"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/protocol"
	"github.com/spencerswagger/guanjin/internal/signing"
)

// 本文件是**数据面整链路**的真库证据（AC-DP-01/09、AC-SESS-01/04）。
//
// 放在 `adapter` 包里而不是 `gateway` 包里，是因为整链路必须有**真适配器**（读真 `sessions` /
// `role_bindings` / `login_policies` / `flows`），而 `adapter` 反过来 import `gateway`——
// 放进 gateway 包会成环。测试代码的落点服从依赖方向，不为了"看起来整齐"去拆接口。
//
// 唯一被假的只有"上游业务系统"（`httptest.Server`）与浏览器侧（不出网）：验签、注入、剥离、
// 准入、撤销全部走真实现与真库。

const (
	itIssuer = "https://auth.guanjin.test"
)

type itKeys struct {
	set *signing.Set
}

func newITKeys(t *testing.T) *itKeys {
	t.Helper()
	master, err := crypto.NewMasterKey(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	// 用非零字节填满，避免全零被当"未配置"。
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	master, err = crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	keywrap, err := master.DeriveSM4(crypto.PurposeSM4Keywrap)
	if err != nil {
		t.Fatalf("DeriveSM4: %v", err)
	}
	oidc, err := signing.GenRow(nil, keywrap, "it-oidc", signing.PurposeOIDC, signing.AlgSM3WithSM2)
	if err != nil {
		t.Fatalf("GenRow(oidc): %v", err)
	}
	gw, err := signing.GenRow(nil, keywrap, "it-gw", signing.PurposeGateway, signing.AlgSM3WithSM2)
	if err != nil {
		t.Fatalf("GenRow(gateway): %v", err)
	}
	set, err := signing.Load(keywrap, []signing.Row{oidc, gw})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return &itKeys{set: set}
}

func (k *itKeys) userToken(t *testing.T, userID, clientID, sid string) string {
	t.Helper()
	now := time.Now().UTC()
	key, err := k.set.For(signing.PurposeOIDC)
	if err != nil {
		t.Fatalf("keys.For(oidc): %v", err)
	}
	token, err := signing.SignJWT(key, map[string]any{
		"iss": itIssuer, "sub": userID, "aud": clientID, "cid": clientID,
		"acr": "AAL1", "amr": []string{"pwd"}, "sid": sid,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	return token
}

type itUpstream struct {
	srv     *httptest.Server
	headers http.Header
	hits    int
}

func newITUpstream(t *testing.T) *itUpstream {
	t.Helper()
	u := &itUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.headers = r.Header.Clone()
		u.hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *itUpstream) assertion() string {
	for k, v := range u.headers {
		if strings.EqualFold(k, gateway.AssertionHeader) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// newITPipeline 用**真适配器**装出数据面。
func newITPipeline(t *testing.T, db *gorm.DB, keys *itKeys, upstream *httptest.Server) *gateway.Pipeline {
	t.Helper()
	parsed, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	router, err := gateway.NewRouter([]gateway.Route{{
		Prefix: "/api", ClientID: itClientID(t, db), Upstream: parsed,
	}})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	forward, err := gateway.New(router, gateway.Options{Logger: logger})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	signer, err := gateway.NewAssertionSigner(keys.set)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	guard, err := gateway.NewGuard(&gateway.Guard{
		Caller: &protocol.CallerResolver{
			Keys: keys.set, Sessions: NewSessionStatus(db), Now: time.Now,
		},
		Sessions: NewSessions(db),
		Policies: NewPolicies(db),
		Roles:    NewRoles(db),
		Users:    NewUsers(db),
		Visits:   NewVisits(db),
		Flows:    NewFlows(db),
		Factors:  itFactors(),
		Issuer:   itIssuer,
		Now:      time.Now,
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	pipeline, err := gateway.NewPipeline(forward, guard, signer, logger)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	return pipeline
}

// itClientID 取当前测试客户端 id（由 `newITPipeline` 的路由表隐含：这里用包级变量传递太脏，
// 故由调用方先设置 `itCurrentClient`）。
var itCurrentClient string

func itClientID(t *testing.T, db *gorm.DB) string {
	t.Helper()
	if itCurrentClient == "" {
		t.Fatal("itCurrentClient 未设置（测试写错了）")
	}
	return itCurrentClient
}

// itFactors 与 M1 的真实能力面一致（只有 password）：目标 AAL2 因此不可达。
func itFactors() []flow.FactorInfo {
	return []flow.FactorInfo{
		{Type: "password", AMR: "pwd", AsFirst: true, AsSecond: true, Sole: "AAL1", Combined: "AAL2"},
	}
}

func itRequest(p http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(gateway.BearerHeader, "Bearer "+token)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func itCode(t *testing.T, rec *httptest.ResponseRecorder) httpx.Code {
	t.Helper()
	var body struct {
		Code httpx.Code `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body.Code
}

// AC-DP-01（真库）：断言里的 `uname`/`roles`/`acr` 全部来自库，且上游能用 JWKS 验签通过。
func TestAC_DP_01_RealDataFeedsAssertion(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	itCurrentClient = f.clientID
	t.Cleanup(func() { itCurrentClient = "" })
	if err := db.Exec(`UPDATE users SET display_name = '不应进入断言' WHERE id = ?`, f.userID).Error; err != nil {
		t.Fatalf("set display_name: %v", err)
	}
	for _, role := range []string{"viewer", "admin"} {
		if err := db.Exec(`INSERT INTO role_bindings (id, user_id, client_id, role) VALUES (?, ?, ?, ?)`,
			mustV7(t), f.userID, f.clientID, role).Error; err != nil {
			t.Fatalf("insert role: %v", err)
		}
	}

	keys := newITKeys(t)
	up := newITUpstream(t)
	pipeline := newITPipeline(t, db, keys, up.srv)

	rec := itRequest(pipeline, "/api/orders/42", keys.userToken(t, f.userID.String(), f.clientID, f.session.String()))
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	assertion := up.assertion()
	if assertion == "" {
		t.Fatal("上游没有收到断言")
	}

	// 用 JWKS 公钥真验一遍（等价于业务系统 SDK 的动作）。
	claims := verifyWithJWKS(t, keys.set, assertion)
	var username string
	if err := db.Raw(`SELECT username FROM users WHERE id = ?`, f.userID).Scan(&username).Error; err != nil {
		t.Fatalf("read username: %v", err)
	}
	if claims["uname"] != username {
		t.Fatalf("uname 期望库里的 username %q，实际 %v", username, claims["uname"])
	}
	roles, _ := claims["roles"].([]any)
	if len(roles) != 2 || roles[0] != "admin" || roles[1] != "viewer" {
		t.Fatalf("roles 期望 [admin viewer]（来自 role_bindings，升序），实际 %v", claims["roles"])
	}
	if claims["acr"] != "AAL1" || claims["aud"] != f.clientID || claims["sub"] != f.userID.String() {
		t.Fatalf("acr/aud/sub 不符：%v", claims)
	}
	if int64(claims["exp"].(float64))-int64(claims["iat"].(float64)) != 60 {
		t.Fatalf("exp-iat 必须正好 60：%v - %v", claims["exp"], claims["iat"])
	}

	// 访问记录：通过准入后落一行。
	var visits int64
	if err := db.Raw(`SELECT count(*) FROM user_visited_clients WHERE user_id = ? AND client_id = ?`,
		f.userID, f.clientID).Scan(&visits).Error; err != nil {
		t.Fatalf("count visits: %v", err)
	}
	if visits != 1 {
		t.Fatalf("通过准入后期望写入 1 条访问记录，实际 %d", visits)
	}
}

// AC-DP-09（真库）：`login_policies.min_acr=AAL2` 的覆盖行 + 真活跃流 → 403 + details.flow。
func TestAC_DP_09_RealPolicyThreshold(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L3")
	itCurrentClient = f.clientID
	t.Cleanup(func() { itCurrentClient = "" })
	insertPolicy(t, db, "client_override", "", f.clientID, "AAL2", "{}")

	flowID := mustV7(t)
	if err := db.Exec(`INSERT INTO flows (id, user_id, session_id, client_id, target_acr, achieved_amr, expires_at)
		VALUES (?, ?, ?, ?, 'AAL2', '{pwd}', ?)`,
		flowID, f.userID, f.session, f.clientID, time.Now().UTC().Add(5*time.Minute)).Error; err != nil {
		t.Fatalf("insert flow: %v", err)
	}

	keys := newITKeys(t)
	up := newITUpstream(t)
	pipeline := newITPipeline(t, db, keys, up.srv)

	rec := itRequest(pipeline, "/api/orders", keys.userToken(t, f.userID.String(), f.clientID, f.session.String()))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("期望 403（不是 302/500），实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if code := itCode(t, rec); code != httpx.CodeACRInsufficient {
		t.Fatalf("错误码期望 ACR_INSUFFICIENT，实际 %s", code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"current_acr":"AAL1"`, `"target_acr":"AAL2"`, flowID.String(), `"details"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("details.flow 缺 %s：%s", want, body)
		}
	}
	if up.hits != 0 {
		t.Fatalf("上游被调用了 %d 次：档位不足不该转发", up.hits)
	}

	// 被拒的请求**不写**访问记录（D16②：被拒的探测不得污染生效配置）。
	var visits int64
	if err := db.Raw(`SELECT count(*) FROM user_visited_clients WHERE user_id = ?`, f.userID).Scan(&visits).Error; err != nil {
		t.Fatalf("count visits: %v", err)
	}
	if visits != 0 {
		t.Fatalf("被 403 的请求竟写入了 %d 条访问记录", visits)
	}
}

// AC-SESS-01（真库）：撤销落库后**首个**请求即 403 `SESSION_REVOKED`（零缓存窗口）。
func TestAC_SESS_01_RevocationIsImmediate(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	itCurrentClient = f.clientID
	t.Cleanup(func() { itCurrentClient = "" })

	keys := newITKeys(t)
	up := newITUpstream(t)
	pipeline := newITPipeline(t, db, keys, up.srv)
	token := keys.userToken(t, f.userID.String(), f.clientID, f.session.String())

	if rec := itRequest(pipeline, "/api/orders", token); rec.Code != http.StatusOK {
		t.Fatalf("撤销前期望 200，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}

	// 模拟 S7 的撤销（条件 UPDATE；数据面不写会话）。
	if err := db.Exec(`UPDATE sessions SET status = 'revoked', revoked_at = now(), revoke_reason = 'logout'
		WHERE id = ?`, f.session).Error; err != nil {
		t.Fatalf("revoke session: %v", err)
	}

	rec := itRequest(pipeline, "/api/orders", token)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("撤销后首个请求期望 403，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if code := itCode(t, rec); code != httpx.CodeSessionRevoked {
		t.Fatalf("错误码期望 SESSION_REVOKED，实际 %s", code)
	}
	if up.hits != 1 {
		t.Fatalf("撤销后不该再转发（上游命中 %d 次，应停在撤销前的 1 次）", up.hits)
	}
}

// AC-SESS-04（真库）：两个独立数据面实例（各自一套适配器/连接）在撤销后**都**立即 403，
// 不依赖会话粘滞、不依赖副本间广播。
func TestAC_SESS_04_TwoInstancesSeeRevocationImmediately(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	itCurrentClient = f.clientID
	t.Cleanup(func() { itCurrentClient = "" })

	keys := newITKeys(t)
	upA := newITUpstream(t)
	upB := newITUpstream(t)
	// 两个实例：各自的 forwarder/guard/适配器（共享同一个库——这正是多副本的真实形态）。
	pipelineA := newITPipeline(t, db, keys, upA.srv)
	pipelineB := newITPipeline(t, db, keys, upB.srv)
	token := keys.userToken(t, f.userID.String(), f.clientID, f.session.String())

	if rec := itRequest(pipelineA, "/api/a", token); rec.Code != http.StatusOK {
		t.Fatalf("实例 A 撤销前期望 200，实际 %d", rec.Code)
	}
	if rec := itRequest(pipelineB, "/api/b", token); rec.Code != http.StatusOK {
		t.Fatalf("实例 B 撤销前期望 200，实际 %d", rec.Code)
	}

	if err := db.Exec(`UPDATE sessions SET status = 'revoked', revoked_at = now(), revoke_reason = 'admin_revoke'
		WHERE id = ?`, f.session).Error; err != nil {
		t.Fatalf("revoke session: %v", err)
	}

	for name, p := range map[string]http.Handler{"A": pipelineA, "B": pipelineB} {
		rec := itRequest(p, "/api/x", token)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("实例 %s 撤销后期望 403，实际 %d（依赖粘滞？）", name, rec.Code)
		}
		if code := itCode(t, rec); code != httpx.CodeSessionRevoked {
			t.Fatalf("实例 %s 错误码期望 SESSION_REVOKED，实际 %s", name, code)
		}
	}
}

// 过期会话（绝对超时到点）同样立即拒，即便后台还没把它扫成 `expired`。
func TestSessionExpiryIsEnforcedAtRequestTime(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	itCurrentClient = f.clientID
	t.Cleanup(func() { itCurrentClient = "" })
	// 两列一起推到过去：`sessions_time_order` 要求 idle ≤ absolute，只改一列会撞 CHECK。
	if err := db.Exec(`UPDATE sessions SET expires_at = now() - interval '1 second',
		idle_expires_at = now() - interval '2 second' WHERE id = ?`, f.session).Error; err != nil {
		t.Fatalf("expire session: %v", err)
	}

	keys := newITKeys(t)
	up := newITUpstream(t)
	pipeline := newITPipeline(t, db, keys, up.srv)

	rec := itRequest(pipeline, "/api/orders", keys.userToken(t, f.userID.String(), f.clientID, f.session.String()))
	if rec.Code != http.StatusForbidden || itCode(t, rec) != httpx.CodeSessionRevoked {
		t.Fatalf("绝对超时到点期望 403 SESSION_REVOKED，实际 %d / %s", rec.Code, itCode(t, rec))
	}
	if up.hits != 0 {
		t.Fatalf("过期会话不该转发（上游命中 %d）", up.hits)
	}
}

// verifyWithJWKS 用 JWKS 公钥验签（与业务系统 SDK 同一条路径）。
func verifyWithJWKS(t *testing.T, set *signing.Set, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("断言不是三段式")
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
	for _, jwk := range set.JWKS() {
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

var _ = context.Background
