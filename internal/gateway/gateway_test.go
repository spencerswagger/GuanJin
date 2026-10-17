package gateway_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/tjfoc/gmsm/sm2"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/factor/holding"
	"github.com/spencerswagger/GuanJin/internal/gateway"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/observability"
	"github.com/spencerswagger/GuanJin/internal/session"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const testIssuer = "http://localhost:8080"

func testMasterKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 3)
	}
	return key
}

type upstreamRecorder struct {
	mu         sync.Mutex
	requests   int
	assertions []string
}

func (u *upstreamRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.requests++
		u.assertions = append(u.assertions, r.Header.Values(gateway.AssertionHeader)...)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

func (u *upstreamRecorder) snapshot() (int, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests, append([]string{}, u.assertions...)
}

type fixture struct {
	db      *gorm.DB
	keys    *store.SigningKeyRepo
	gw      *gateway.Gateway
	srv     *httptest.Server
	rec     *gateway.RecordingSLO
	userID  uuid.UUID
	clientA string
	clientB string
	clientL string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dbURL := testEnv(t, "GUANJIN_TEST_DATABASE_URL")
	redisURL := testEnv(t, "GUANJIN_TEST_REDIS_URL")

	require.NoError(t, store.MigrateUp(dbURL))
	db, err := store.Open(store.Options{DSN: dbURL})
	require.NoError(t, err)
	rdb, err := store.NewRedis(redisURL, 2*time.Second)
	require.NoError(t, err)
	require.NoError(t, rdb.Ping(context.Background()))

	masterKey := testMasterKey()
	ctx := context.Background()
	keys := store.NewSigningKeyRepo(db, masterKey)
	require.NoError(t, db.Exec("DELETE FROM signing_keys").Error)
	_, err = keys.EnsureKeypair(ctx, crypto.PurposeOIDC)
	require.NoError(t, err)
	_, err = keys.EnsureKeypair(ctx, crypto.PurposeGateway)
	require.NoError(t, err)

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	clientA := "gw-a-" + suffix
	clientB := "gw-b-" + suffix
	clientL := "gw-l3-" + suffix

	seedClient(t, db, clientA, "L2")
	seedClient(t, db, clientB, "L2")
	seedClient(t, db, clientL, "L3")

	user := &model.User{Username: "gw-user-" + suffix, Status: "active"}
	require.NoError(t, store.NewUserRepo(db).Create(ctx, user))
	require.NoError(t, db.Create(&model.RoleBinding{
		UserID: user.ID, ClientID: clientA, Role: "order_admin", Status: "active", GrantedAt: time.Now().UTC(),
	}).Error)

	rec := gateway.NewRecordingSLO()
	upstream := httptest.NewServer((&upstreamRecorder{}).handler())
	t.Cleanup(upstream.Close)

	router := gateway.NewTableRouter([]gateway.Route{
		{MatchType: gateway.MatchPrefix, Match: "/appA", ClientID: clientA, Upstream: upstream.URL},
		{MatchType: gateway.MatchPrefix, Match: "/appB", ClientID: clientB, Upstream: upstream.URL},
		{MatchType: gateway.MatchPrefix, Match: "/appL3", ClientID: clientL, Upstream: upstream.URL},
	})

	registry := factor.NewRegistry()
	require.NoError(t, registry.Register(holding.NewPasswordProvider(
		store.NewAuthenticatorRepo(db), store.NewChallengeRepo(db),
		holding.DefaultFailThreshold, holding.DefaultLockDuration,
	)))

	gw := gateway.New(gateway.Deps{
		Redis:           rdb,
		Keys:            keys,
		Clients:         store.NewClientRepo(db, masterKey),
		Sessions:        store.NewSessionRepo(db),
		Policies:        store.NewLoginPolicyRepo(db),
		Roles:           store.NewRoleBindingRepo(db),
		Users:           store.NewUserRepo(db),
		Visited:         store.NewUserVisitedClientRepo(db),
		AuthEvents:      store.NewAuthEventRepo(db),
		Router:          router,
		Notifier:        rec,
		Registry:        registry,
		Issuer:          testIssuer,
		UpstreamTimeout: 3 * time.Second,
		DialTimeout:     time.Second,
		RequireRedis:    true,
	})

	srv := httptest.NewServer(httpx.NetHTTPTrace(gw))
	t.Cleanup(srv.Close)

	return &fixture{db: db, keys: keys, gw: gw, srv: srv, rec: rec, userID: user.ID, clientA: clientA, clientB: clientB, clientL: clientL}
}

func testEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("set %s to run gateway integration tests", name)
	}
	return v
}

func seedClient(t *testing.T, db *gorm.DB, clientID, tier string) {
	t.Helper()
	require.NoError(t, db.Create(&model.Client{
		ClientID:               clientID,
		Name:                   clientID,
		Tier:                   tier,
		Status:                 "active",
		RedirectURIs:           pq.StringArray{},
		PostLogoutRedirectURIs: pq.StringArray{},
	}).Error)
}

func (f *fixture) newSession(t *testing.T, clientID, acr string, amr []string) *model.Session {
	t.Helper()
	mgr := session.NewManager(store.NewSessionRepo(f.db))
	sess, err := mgr.Create(context.Background(), session.CreateParams{
		UserID:    f.userID,
		ClientID:  clientID,
		ACR:       acr,
		AMR:       amr,
		IP:        "127.0.0.1",
		UserAgent: "gateway-test",
		Timeouts:  session.Timeouts{Absolute: time.Hour, Idle: time.Hour},
	})
	require.NoError(t, err)
	return sess
}

func (f *fixture) userToken(t *testing.T, clientID string, sess *model.Session) string {
	t.Helper()
	kid, priv, err := f.keys.ActiveSigner(context.Background(), crypto.PurposeOIDC)
	require.NoError(t, err)
	now := time.Now().UTC()
	jti, err := uuid.NewV7()
	require.NoError(t, err)
	claims := jwt.MapClaims{
		"iss": testIssuer,
		"sub": f.userID.String(),
		"aud": clientID,
		"sid": sess.ID.String(),
		"acr": sess.ACR,
		"amr": []string(sess.AMR),
		"iat": now.Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"jti": jti.String(),
	}
	token := jwt.NewWithClaims(crypto.SigningMethodSM3WithSM2, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(priv)
	require.NoError(t, err)
	return signed
}

func (f *fixture) do(t *testing.T, method, path, token, forged string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, nil)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if forged != "" {
		req.Header.Set(gateway.AssertionHeader, forged)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, body
}

func TestGatewayForwardInjectsSignedAssertion(t *testing.T) {
	f := setup(t)
	up := &upstreamRecorder{}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()
	f.rewireUpstream(t, upstream.URL)

	sess := f.newSession(t, f.clientA, "AAL1", []string{"pwd"})
	token := f.userToken(t, f.clientA, sess)

	resp, body := f.do(t, http.MethodGet, "/appA/orders/42", token, "forged-assertion-value")
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))
	require.JSONEq(t, `{"ok":true}`, string(body))

	requests, assertions := up.snapshot()
	require.Equal(t, 1, requests)
	require.Len(t, assertions, 1, "上游只能看到平台签发的单个 X-GuanJin-Assertion")
	require.NotEqual(t, "forged-assertion-value", assertions[0])
	require.NotContains(t, assertions[0], "forged")

	claims := verifyAssertion(t, f.keys, assertions[0])
	require.Equal(t, gateway.AssertionIssuer, claims["iss"])
	require.Equal(t, f.userID.String(), claims["sub"])
	require.Equal(t, f.clientA, claims["aud"])
	require.Equal(t, f.clientA, claims["cid"])
	require.NotEmpty(t, claims["uname"])
	require.Equal(t, "AAL1", claims["acr"])
	require.Contains(t, claims["amr"], "pwd")
	require.NotEmpty(t, claims["jti"])
	require.Contains(t, claims["roles"], "order_admin")
	iat := int64(claims["iat"].(float64))
	exp := int64(claims["exp"].(float64))
	require.EqualValues(t, 60, exp-iat, "断言有效期必须为 60s")
	t.Logf("assertion ok: aud=%v cid=%v uname=%v acr=%v amr=%v roles=%v ttl=%ds",
		claims["aud"], claims["cid"], claims["uname"], claims["acr"], claims["amr"], claims["roles"], exp-iat)

	_, _ = f.do(t, http.MethodGet, "/appA/orders/43", token, "")
	require.Equal(t, 2, f.visitCount(t, f.clientA), "user_visited_clients 应被 upsert（第二次访问 visit_count=2）")
	require.Equal(t, 0, f.visitCount(t, f.clientB), "未访问的 client 不应有记录")
	t.Logf("visited upsert ok: %s visit_count=2", f.clientA)
}

func TestGatewayRevocationIsImmediate(t *testing.T) {
	f := setup(t)
	up := &upstreamRecorder{}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()
	f.rewireUpstream(t, upstream.URL)

	sess := f.newSession(t, f.clientA, "AAL1", []string{"pwd"})
	token := f.userToken(t, f.clientA, sess)

	resp, _ := f.do(t, http.MethodGet, "/appA/resource", token, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.NoError(t, f.db.Exec("UPDATE sessions SET status = 'revoked', revoked_at = now(), revoke_reason = 'test' WHERE id = ?", sess.ID).Error)

	resp, body := f.do(t, http.MethodGet, "/appA/resource", token, "")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, "SESSION_REVOKED", errorCode(t, body))
	t.Logf("撤销实时生效: 同一 token 再请求 → %d %s", resp.StatusCode, errorCode(t, body))
}

func TestGatewayACRInsufficient(t *testing.T) {
	f := setup(t)
	up := &upstreamRecorder{}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()
	f.rewireUpstream(t, upstream.URL)

	sess := f.newSession(t, f.clientL, "AAL1", []string{"pwd"})
	token := f.userToken(t, f.clientL, sess)

	resp, body := f.do(t, http.MethodGet, "/appL3/secure", token, "")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, "ACR_INSUFFICIENT", errorCode(t, body))

	var envelope struct {
		Details map[string]json.RawMessage `json:"details"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Contains(t, envelope.Details, "flow")
	var flowDesc struct {
		TargetACR  string `json:"target_acr"`
		CurrentACR string `json:"current_acr"`
	}
	require.NoError(t, json.Unmarshal(envelope.Details["flow"], &flowDesc))
	require.Equal(t, "AAL2", flowDesc.TargetACR)
	require.Equal(t, "AAL1", flowDesc.CurrentACR)
	t.Logf("档位不足引导: %d %s flow.target_acr=%s current_acr=%s", resp.StatusCode, errorCode(t, body), flowDesc.TargetACR, flowDesc.CurrentACR)
}

func TestGatewayUnauthenticated(t *testing.T) {
	f := setup(t)
	up := &upstreamRecorder{}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()
	f.rewireUpstream(t, upstream.URL)

	resp, body := f.do(t, http.MethodGet, "/appA/orders", "", "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, "TOKEN_INVALID", errorCode(t, body))
	t.Logf("未认证: %d %s", resp.StatusCode, errorCode(t, body))
}

func TestGatewaySLOOnLogout(t *testing.T) {
	f := setup(t)
	up := &upstreamRecorder{}
	upstream := httptest.NewServer(up.handler())
	defer upstream.Close()
	f.rewireUpstream(t, upstream.URL)

	sessA := f.newSession(t, f.clientA, "AAL1", []string{"pwd"})
	tokenA := f.userToken(t, f.clientA, sessA)
	resp, _ := f.do(t, http.MethodGet, "/appA/x", tokenA, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	sessB := f.newSession(t, f.clientB, "AAL1", []string{"pwd"})
	tokenB := f.userToken(t, f.clientB, sessB)
	resp, _ = f.do(t, http.MethodGet, "/appB/y", tokenB, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	visited := f.visitCount(t, f.clientA) + f.visitCount(t, f.clientB)
	require.Equal(t, 2, visited)

	count, err := f.gw.OnLogout(context.Background(), f.userID, sessA.ID, "logout")
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Equal(t, 2, f.rec.Count())
	require.ElementsMatch(t, []string{f.clientA, f.clientB}, f.rec.ClientIDs())
	t.Logf("SLO 通知覆盖已访问业务系统: count=%d clients=%v", f.rec.Count(), f.rec.ClientIDs())
}

func TestGatewayReadyzFailsClosedWhenRedisDown(t *testing.T) {
	dbURL := testEnv(t, "GUANJIN_TEST_DATABASE_URL")
	require.NoError(t, store.MigrateUp(dbURL))
	db, err := store.Open(store.Options{DSN: dbURL})
	require.NoError(t, err)

	downRedis, err := store.NewRedis("redis://127.0.0.1:1/0", 500*time.Millisecond)
	require.NoError(t, err)
	defer func() { _ = downRedis.Close() }()

	masterKey := testMasterKey()
	probes := &httpx.Probes{Checks: map[string]httpx.CheckFunc{
		observability.DependencyDB:    func(ctx context.Context) error { return store.Ping(ctx, db) },
		observability.DependencyRedis: func(ctx context.Context) error { return downRedis.Ping(ctx) },
		observability.DependencyKeys: func(context.Context) error {
			if !crypto.KeysReady(masterKey) {
				return io.ErrUnexpectedEOF
			}
			return nil
		},
	}}
	srv := httptest.NewServer(probes.ReadyzHandler(observability.NewMetrics()))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/readyz")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Contains(t, string(body), `"redis":false`)
	t.Logf("Redis 不可用 readyz: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func (f *fixture) rewireUpstream(t *testing.T, upstreamURL string) {
	t.Helper()
	router := gateway.NewTableRouter([]gateway.Route{
		{MatchType: gateway.MatchPrefix, Match: "/appA", ClientID: f.clientA, Upstream: upstreamURL},
		{MatchType: gateway.MatchPrefix, Match: "/appB", ClientID: f.clientB, Upstream: upstreamURL},
		{MatchType: gateway.MatchPrefix, Match: "/appL3", ClientID: f.clientL, Upstream: upstreamURL},
	})
	f.gw.SetRouter(router)
}

func (f *fixture) visitCount(t *testing.T, clientID string) int {
	t.Helper()
	var count int64
	require.NoError(t, f.db.Model(&model.UserVisitedClient{}).
		Where("user_id = ? AND client_id = ?", f.userID, clientID).
		Count(&count).Error)
	if count == 0 {
		return 0
	}
	var visits int64
	require.NoError(t, f.db.Model(&model.UserVisitedClient{}).
		Where("user_id = ? AND client_id = ?", f.userID, clientID).
		Pluck("visit_count", &visits).Error)
	return int(visits)
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	return envelope.Code
}

func verifyAssertion(t *testing.T, keys *store.SigningKeyRepo, token string) jwt.MapClaims {
	t.Helper()
	parsed, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	require.NoError(t, err)
	kid, _ := parsed.Header["kid"].(string)
	require.True(t, strings.HasPrefix(kid, crypto.KIDPrefixGateway), "kid 应带 gw: 前缀, got %q", kid)

	rows, err := keys.ListPublishable(context.Background(), crypto.PurposeGateway)
	require.NoError(t, err)
	require.NotEmpty(t, rows)

	var pub *sm2.PublicKey
	for _, row := range rows {
		if row.Kid != kid {
			continue
		}
		pk, perr := store.PublicKeyFromText(row.PublicKey)
		require.NoError(t, perr)
		pub = pk
	}
	require.NotNil(t, pub, "JWKS 中应能按 kid 找到网关公钥")

	verified, err := jwt.Parse(token, func(*jwt.Token) (interface{}, error) { return pub, nil },
		jwt.WithValidMethods([]string{crypto.AlgoSM3WithSM2}))
	require.NoError(t, err)
	require.True(t, verified.Valid)
	return verified.Claims.(jwt.MapClaims)
}

func TestGatewayAssertionJWKSKey(t *testing.T) {
	f := setup(t)
	rows, err := f.keys.ListPublishable(context.Background(), crypto.PurposeGateway)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	pub, err := store.PublicKeyFromText(rows[0].PublicKey)
	require.NoError(t, err)
	require.NotNil(t, pub)
	jwk := crypto.NewJWK(rows[0].Kid, rows[0].Algorithm, pub)
	require.Equal(t, "EC", jwk.Kty)
	require.Equal(t, "SM2", jwk.Crv)
	require.NotEmpty(t, base64.RawURLEncoding.EncodeToString(mustBig(t, jwk.X)))
	t.Logf("gateway JWK: kid=%s alg=%s kty=%s crv=%s", jwk.Kid, jwk.Alg, jwk.Kty, jwk.Crv)
}

func mustBig(t *testing.T, b64 string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	require.NoError(t, err)
	require.Positive(t, new(big.Int).SetBytes(raw).Sign())
	return raw
}
