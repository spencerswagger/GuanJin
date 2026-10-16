package protocol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/tjfoc/gmsm/sm2"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/credential"
	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/factor/holding"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/identifier"
	"github.com/spencerswagger/GuanJin/internal/session"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	e2eClientID    = "test-client"
	e2eRedirectURI = "http://localhost:9999/cb"
	e2eUsername    = "e2e-alice"
	e2ePassword    = "GuanJin-E2E-Pass-01"
	e2eVerifier    = "e2e-code-verifier-0123456789-abcdefg"
)

func e2eMasterKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	return key
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type e2eFixture struct {
	engine *gin.Engine
	db     *gorm.DB
	server *Server
	rdb    *store.Redis
}

func setupE2E(t *testing.T) *e2eFixture {
	t.Helper()

	dbURL := os.Getenv("GUANJIN_TEST_DATABASE_URL")
	redisURL := os.Getenv("GUANJIN_TEST_REDIS_URL")
	if dbURL == "" || redisURL == "" {
		t.Skip("set GUANJIN_TEST_DATABASE_URL and GUANJIN_TEST_REDIS_URL to run E2E")
	}

	require.NoError(t, store.MigrateUp(dbURL))

	db, err := store.Open(store.Options{DSN: dbURL})
	require.NoError(t, err)

	rdb, err := store.NewRedis(redisURL, 2*time.Second)
	require.NoError(t, err)
	require.NoError(t, rdb.Ping(context.Background()))

	masterKey := e2eMasterKey()
	ctx := context.Background()

	keysRepo := store.NewSigningKeyRepo(db, masterKey)
	require.NoError(t, db.Exec("DELETE FROM signing_keys").Error)
	require.NoError(t, func() error {
		if _, err := keysRepo.EnsureKeypair(ctx, crypto.PurposeOIDC); err != nil {
			return err
		}
		_, err := keysRepo.EnsureKeypair(ctx, crypto.PurposeGateway)
		return err
	}())

	seedE2E(t, db, keysRepo, masterKey)

	authenticators := store.NewAuthenticatorRepo(db)
	challenges := store.NewChallengeRepo(db)
	registry := factor.NewRegistry()
	require.NoError(t, registry.Register(holding.NewPasswordProvider(authenticators, challenges, holding.DefaultFailThreshold, holding.DefaultLockDuration)))

	protoServer := New(Deps{
		Config:         Config{Issuer: "http://localhost:8080", PublicBaseURL: "http://localhost:8080", SecureCookie: false},
		Redis:          rdb,
		Users:          store.NewUserRepo(db),
		Clients:        store.NewClientRepo(db, masterKey),
		Identifiers:    store.NewIdentifierRepo(db),
		Authenticators: authenticators,
		Challenges:     challenges,
		Sessions:       store.NewSessionRepo(db),
		Keys:           keysRepo,
		Policies:       store.NewLoginPolicyRepo(db),
		Roles:          store.NewRoleBindingRepo(db),
		SessionMgr:     session.NewManager(store.NewSessionRepo(db)),
		Blind:          identifier.NewBlindIndexer(masterKey),
		Registry:       registry,
	})

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(httpx.Trace())
	protoServer.RegisterRoutes(engine)

	return &e2eFixture{engine: engine, db: db, server: protoServer, rdb: rdb}
}

func seedE2E(t *testing.T, db *gorm.DB, keysRepo *store.SigningKeyRepo, masterKey []byte) {
	t.Helper()
	ctx := context.Background()

	users := store.NewUserRepo(db)
	auths := store.NewAuthenticatorRepo(db)

	var existing model.User
	if err := db.Where("username = ?", e2eUsername).First(&existing).Error; err == nil {
		require.NoError(t, db.Exec("DELETE FROM sessions WHERE user_id = ?", existing.ID).Error)
		require.NoError(t, db.Exec("DELETE FROM challenges WHERE user_id = ?", existing.ID).Error)
		require.NoError(t, db.Exec("DELETE FROM authenticators WHERE user_id = ?", existing.ID).Error)
		require.NoError(t, db.Exec("DELETE FROM identifiers WHERE user_id = ?", existing.ID).Error)
		require.NoError(t, db.Exec("DELETE FROM users WHERE id = ?", existing.ID).Error)
	}
	require.NoError(t, db.Exec("DELETE FROM challenges WHERE client_id = ?", e2eClientID).Error)
	require.NoError(t, db.Exec("DELETE FROM sessions WHERE client_id = ?", e2eClientID).Error)
	require.NoError(t, db.Exec("DELETE FROM clients WHERE client_id = ?", e2eClientID).Error)

	client := &model.Client{
		ClientID:               e2eClientID,
		Name:                   "E2E 公共客户端",
		Tier:                   "L2",
		Status:                 "active",
		RedirectURIs:           pq.StringArray{e2eRedirectURI},
		PostLogoutRedirectURIs: pq.StringArray{},
	}
	require.NoError(t, db.Create(client).Error)

	user := &model.User{Username: e2eUsername, Status: "active"}
	require.NoError(t, users.Create(ctx, user))

	cd, err := credential.Hash(e2ePassword, credential.DefaultPolicy())
	require.NoError(t, err)
	require.NoError(t, auths.Create(ctx, &model.Authenticator{
		UserID:         user.ID,
		Type:           "password",
		CredentialData: cd,
		Status:         "active",
	}))
}

func noRedirectClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func authorizeQuery(state string) url.Values {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", e2eClientID)
	v.Set("redirect_uri", e2eRedirectURI)
	v.Set("scope", "openid profile")
	v.Set("state", state)
	v.Set("nonce", "n-0S6_WzA2Mj")
	v.Set("code_challenge", pkceChallenge(e2eVerifier))
	v.Set("code_challenge_method", "S256")
	return v
}

func TestE2EPasswordLoginAuthorizationCodeFlow(t *testing.T) {
	fx := setupE2E(t)
	srv := httptest.NewServer(fx.engine)
	defer srv.Close()

	client := noRedirectClient(t)
	ctx := context.Background()

	authParams := authorizeQuery("state-abc")

	// 1) GET /authorize（无会话）→ 302 /login?flow_id
	resp, err := client.Get(srv.URL + "/authorize?" + authParams.Encode())
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	location := resp.Header.Get("Location")
	require.True(t, strings.HasPrefix(location, "/login?"), "location=%s", location)
	loginURL, err := url.Parse(location)
	require.NoError(t, err)
	flowID := loginURL.Query().Get("flow_id")
	require.NotEmpty(t, flowID)
	resp.Body.Close()
	defer func() { _ = fx.rdb.Client().Del(ctx, "guanjin:flow:"+flowID) }()

	// 2) GET /authorize/flow → Flow Descriptor（编排层驱动）
	resp, err = client.Get(srv.URL + "/authorize/flow?flow_id=" + url.QueryEscape(flowID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var desc map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &desc))
	require.Equal(t, flowID, desc["flow_id"])
	require.Equal(t, "AAL1", desc["target_acr"])
	require.Equal(t, "AAL0", desc["current_acr"])
	require.Equal(t, false, desc["satisfied"])
	clientRef, ok := desc["client"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, e2eClientID, clientRef["id"])
	require.Equal(t, "L2", clientRef["tier"])
	steps, ok := desc["steps"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, steps)
	firstStep, ok := steps[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "password", firstStep["factor"])
	require.Equal(t, "first", firstStep["role"])
	require.Equal(t, "pending", firstStep["status"])
	hint, ok := firstStep["challenge_hint"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "input_password", hint["next_action"])
	available, ok := desc["available_factors"].([]any)
	require.True(t, ok)
	require.Contains(t, available, "password")
	t.Logf("descriptor: flow_id=%s target=%v current=%v satisfied=%v steps=%d available=%v",
		desc["flow_id"], desc["target_acr"], desc["current_acr"], desc["satisfied"], len(steps), available)

	// 3) POST /auth/challenge/password → 开启挑战（编排行）
	resp = postJSON(t, client, srv.URL+"/auth/challenge/password", map[string]any{
		"flow_id": flowID, "purpose": "login", "user_hint": e2eUsername,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var ch map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &ch))
	challengeID, _ := ch["challenge_id"].(string)
	require.NotEmpty(t, challengeID)
	require.Equal(t, "input_password", ch["next_action"])

	// 4) POST /auth/verify/password → 校验并推进编排
	resp = postJSON(t, client, srv.URL+"/auth/verify/password", map[string]any{
		"challenge_id": challengeID, "response": e2ePassword, "flow_id": flowID,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var verified map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &verified))
	require.Equal(t, true, verified["verified"])
	require.Equal(t, "AAL1", verified["acr"])
	amr, ok := verified["amr"].([]any)
	require.True(t, ok)
	require.Contains(t, amr, "pwd")

	// 5) POST /authorize/submit（只补缺，已达标）→ completed 拿 code
	resp = postJSON(t, client, srv.URL+"/authorize/submit", map[string]any{
		"flow_id": flowID, "answers": []any{},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotEmpty(t, resp.Header.Values("Set-Cookie"))
	var submit map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &submit))
	require.Equal(t, "completed", submit["status"])
	code, _ := submit["code"].(string)
	require.NotEmpty(t, code)
	require.Equal(t, e2eRedirectURI, submit["redirect_uri"])
	require.Equal(t, "state-abc", submit["state"])

	var user model.User
	require.NoError(t, fx.db.Where("username = ?", e2eUsername).First(&user).Error)
	var sessionCount int64
	require.NoError(t, fx.db.Model(&model.Session{}).Where("user_id = ? AND status = ?", user.ID, "active").Count(&sessionCount).Error)
	require.EqualValues(t, 1, sessionCount, "完成编排后应建立会话")

	// 6) /oauth/token 授权码换令牌
	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("code", code)
	tokenForm.Set("redirect_uri", e2eRedirectURI)
	tokenForm.Set("client_id", e2eClientID)
	tokenForm.Set("code_verifier", e2eVerifier)
	resp = postForm(t, client, srv.URL+"/oauth/token", tokenForm)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	var tokens map[string]any
	require.NoError(t, json.Unmarshal(body, &tokens))
	accessToken, _ := tokens["access_token"].(string)
	idToken, _ := tokens["id_token"].(string)
	refreshToken, _ := tokens["refresh_token"].(string)
	require.NotEmpty(t, accessToken)
	require.NotEmpty(t, idToken)
	require.NotEmpty(t, refreshToken)
	require.Equal(t, "Bearer", tokens["token_type"])

	// 7) 断言 id_token 含 acr/amr，且用 jwks 公钥验签成功
	jwksResp, err := client.Get(srv.URL + "/.well-known/jwks.json")
	require.NoError(t, err)
	jwksBody := readBody(t, jwksResp)
	var doc crypto.JWKSDocument
	require.NoError(t, json.Unmarshal(jwksBody, &doc))
	require.NotEmpty(t, doc.Keys)

	pub := publicKeyForIDToken(t, idToken, doc)
	parsed, err := jwt.Parse(idToken, func(*jwt.Token) (interface{}, error) { return pub, nil },
		jwt.WithValidMethods([]string{crypto.AlgoSM3WithSM2}))
	require.NoError(t, err)
	require.True(t, parsed.Valid, "id_token 应通过 JWKS 公钥验签")
	idClaims := parsed.Claims.(jwt.MapClaims)
	require.Equal(t, "AAL1", idClaims["acr"])
	idAMR, ok := idClaims["amr"].([]interface{})
	require.True(t, ok)
	require.Equal(t, []interface{}{"pwd"}, idAMR)
	t.Logf("id_token 验签成功: kid=%s acr=%v amr=%v", parsed.Header["kid"], idClaims["acr"], idAMR)

	// 8) /userinfo → 返回 sub
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/userinfo", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	uiResp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, uiResp.StatusCode)
	var ui map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, uiResp), &ui))
	require.Equal(t, user.ID.String(), ui["sub"])
	require.Equal(t, e2eUsername, ui["preferred_username"])

	// 9) refresh_token 轮换一次成功
	refreshForm := url.Values{}
	refreshForm.Set("grant_type", "refresh_token")
	refreshForm.Set("refresh_token", refreshToken)
	refreshForm.Set("client_id", e2eClientID)
	resp = postForm(t, client, srv.URL+"/oauth/token", refreshForm)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var rotated map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &rotated))
	newRefresh, _ := rotated["refresh_token"].(string)
	require.NotEmpty(t, newRefresh)
	require.NotEqual(t, refreshToken, newRefresh, "refresh_token 应轮换")
	require.NotEmpty(t, rotated["access_token"])

	// 10) 重复使用旧 refresh_token → REFRESH_TOKEN_REUSED 且会话被撤销
	resp = postForm(t, client, srv.URL+"/oauth/token", refreshForm)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	var errBody map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &errBody))
	require.Equal(t, "REFRESH_TOKEN_REUSED", errBody["code"])

	sessions, err := store.NewSessionRepo(fx.db).ListActiveByUser(ctx, user.ID)
	require.NoError(t, err)
	require.Empty(t, sessions, "重放后会话应被撤销")
	t.Logf("refresh 重放被拒: code=%v 活动会话数=%d", errBody["code"], len(sessions))

	// 11) 错误口令 → INVALID_CREDENTIAL（编排层 challenge/verify 路径）
	badFlowID, err := createFlow(t, client, srv.URL, "state-bad")
	require.NoError(t, err)
	defer func() { _ = fx.rdb.Client().Del(ctx, "guanjin:flow:"+badFlowID) }()
	resp = postJSON(t, client, srv.URL+"/auth/challenge/password", map[string]any{
		"flow_id": badFlowID, "purpose": "login", "user_hint": e2eUsername,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var badCh map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &badCh))
	badChallenge, _ := badCh["challenge_id"].(string)
	resp = postJSON(t, client, srv.URL+"/auth/verify/password", map[string]any{
		"challenge_id": badChallenge, "response": "definitely-wrong-pass", "flow_id": badFlowID,
	})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	var badErr map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &badErr))
	require.Equal(t, "INVALID_CREDENTIAL", badErr["code"])
	t.Logf("错误口令被拒: code=%v", badErr["code"])
}

func createFlow(t *testing.T, client *http.Client, baseURL, state string) (string, error) {
	t.Helper()
	params := authorizeQuery(state)
	resp, err := client.Get(baseURL + "/authorize?" + params.Encode())
	require.NoError(t, err)
	defer resp.Body.Close()
	location := resp.Header.Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	return parsed.Query().Get("flow_id"), nil
}

func postJSON(t *testing.T, client *http.Client, urlStr string, payload any) *http.Response {
	t.Helper()
	blob, err := json.Marshal(payload)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, urlStr, bytes.NewReader(blob))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func publicKeyForIDToken(t *testing.T, idToken string, doc crypto.JWKSDocument) *sm2.PublicKey {
	t.Helper()
	parsed, _, err := jwt.NewParser().ParseUnverified(idToken, jwt.MapClaims{})
	require.NoError(t, err)
	kid, _ := parsed.Header["kid"].(string)
	require.NotEmpty(t, kid)
	for _, jwk := range doc.Keys {
		if jwk.Kid != kid {
			continue
		}
		x, err := base64.RawURLEncoding.DecodeString(jwk.X)
		require.NoError(t, err)
		y, err := base64.RawURLEncoding.DecodeString(jwk.Y)
		require.NoError(t, err)
		return &sm2.PublicKey{
			Curve: sm2.P256Sm2(),
			X:     new(big.Int).SetBytes(x),
			Y:     new(big.Int).SetBytes(y),
		}
	}
	t.Fatalf("jwks 中未找到 kid=%s", kid)
	return nil
}

func postForm(t *testing.T, client *http.Client, urlStr string, form url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, urlStr, strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, err := io.Copy(&buf, resp.Body)
	require.NoError(t, err)
	return buf.Bytes()
}
