package protocol

import (
	"context"
	"encoding/json"
	"net/http"
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
	totpClientID    = "totp-client"
	totpRedirectURI = "http://localhost:9998/cb"
	totpUsername    = "totp-carol"
	totpPassword    = "GuanJin-TOTP-Pass-01"
	totpVerifier    = "totp-code-verifier-0123456789-abcdefg"
)

var totpSharedSecret = []byte("guanjin-totp-e2e-shared-secret-01")

func setupTOTPE2E(t *testing.T) *e2eFixture {
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

	seedTOTPE2E(t, db, masterKey)

	authenticators := store.NewAuthenticatorRepo(db)
	challenges := store.NewChallengeRepo(db)
	registry := factor.NewRegistry()
	require.NoError(t, registry.Register(holding.NewPasswordProvider(authenticators, challenges, holding.DefaultFailThreshold, holding.DefaultLockDuration)))
	require.NoError(t, registry.Register(holding.NewTotpProvider(authenticators, challenges, masterKey, holding.DefaultFailThreshold, holding.DefaultLockDuration)))
	require.Equal(t, []factor.FactorType{"password", "totp"}, registry.EnabledTypes())

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

func seedTOTPE2E(t *testing.T, db *gorm.DB, masterKey []byte) {
	t.Helper()
	ctx := context.Background()

	users := store.NewUserRepo(db)
	auths := store.NewAuthenticatorRepo(db)

	var existing model.User
	if err := db.Where("username = ?", totpUsername).First(&existing).Error; err == nil {
		require.NoError(t, db.Exec("DELETE FROM sessions WHERE user_id = ?", existing.ID).Error)
		require.NoError(t, db.Exec("DELETE FROM challenges WHERE user_id = ?", existing.ID).Error)
		require.NoError(t, db.Exec("DELETE FROM authenticators WHERE user_id = ?", existing.ID).Error)
		require.NoError(t, db.Exec("DELETE FROM identifiers WHERE user_id = ?", existing.ID).Error)
		require.NoError(t, db.Exec("DELETE FROM users WHERE id = ?", existing.ID).Error)
	}
	require.NoError(t, db.Exec("DELETE FROM challenges WHERE client_id = ?", totpClientID).Error)
	require.NoError(t, db.Exec("DELETE FROM sessions WHERE client_id = ?", totpClientID).Error)
	require.NoError(t, db.Exec("DELETE FROM clients WHERE client_id = ?", totpClientID).Error)

	client := &model.Client{
		ClientID:               totpClientID,
		Name:                   "TOTP L3 公共客户端",
		Tier:                   "L3",
		Status:                 "active",
		RedirectURIs:           pq.StringArray{totpRedirectURI},
		PostLogoutRedirectURIs: pq.StringArray{},
	}
	require.NoError(t, db.Create(client).Error)

	user := &model.User{Username: totpUsername, Status: "active"}
	require.NoError(t, users.Create(ctx, user))

	cd, err := credential.Hash(totpPassword, credential.DefaultPolicy())
	require.NoError(t, err)
	require.NoError(t, auths.Create(ctx, &model.Authenticator{
		UserID:         user.ID,
		Type:           string(holding.PasswordType),
		CredentialData: cd,
		Status:         "active",
	}))

	totpCD, err := holding.NewTOTPCredential(masterKey, totpSharedSecret)
	require.NoError(t, err)
	require.NoError(t, auths.Create(ctx, &model.Authenticator{
		UserID:         user.ID,
		Type:           string(holding.TOTPType),
		CredentialData: totpCD,
		Status:         "active",
	}))
}

func totpAuthorizeQuery(state string) url.Values {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", totpClientID)
	v.Set("redirect_uri", totpRedirectURI)
	v.Set("scope", "openid profile")
	v.Set("state", state)
	v.Set("nonce", "totp-nonce-0S6_WzA2Mj")
	v.Set("code_challenge", pkceChallenge(totpVerifier))
	v.Set("code_challenge_method", "S256")
	return v
}

func TestE2EL3PasswordThenTotpSecondFactor(t *testing.T) {
	fx := setupTOTPE2E(t)
	srv := httptest.NewServer(fx.engine)
	defer srv.Close()

	client := noRedirectClient(t)
	ctx := context.Background()

	params := totpAuthorizeQuery("state-totp")

	resp, err := client.Get(srv.URL + "/authorize?" + params.Encode())
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

	resp, err = client.Get(srv.URL + "/authorize/flow?flow_id=" + url.QueryEscape(flowID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var desc map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &desc))
	require.Equal(t, "AAL2", desc["target_acr"])
	require.Equal(t, "AAL0", desc["current_acr"])
	require.Equal(t, false, desc["satisfied"])
	clientRef, ok := desc["client"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, totpClientID, clientRef["id"])
	require.Equal(t, "L3", clientRef["tier"])
	steps, ok := desc["steps"].([]any)
	require.True(t, ok)
	require.Len(t, steps, 1)
	firstStep, ok := steps[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "password", firstStep["factor"])
	require.Equal(t, "first", firstStep["role"])
	require.Equal(t, "pending", firstStep["status"])
	available, ok := desc["available_factors"].([]any)
	require.True(t, ok)
	require.Contains(t, available, "password")
	require.Contains(t, available, "totp")
	t.Logf("[1] GET /authorize/flow → target_acr=%v current_acr=%v steps=%v available=%v",
		desc["target_acr"], desc["current_acr"], dumpSteps(steps), available)

	resp = postJSON(t, client, srv.URL+"/auth/challenge/password", map[string]any{
		"flow_id": flowID, "purpose": "login", "user_hint": totpUsername,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var ch map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &ch))
	pwdChallenge, _ := ch["challenge_id"].(string)
	require.NotEmpty(t, pwdChallenge)
	require.Equal(t, "input_password", ch["next_action"])

	resp = postJSON(t, client, srv.URL+"/auth/verify/password", map[string]any{
		"challenge_id": pwdChallenge, "response": totpPassword, "flow_id": flowID,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var pwdVerified map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &pwdVerified))
	require.Equal(t, true, pwdVerified["verified"])
	require.Equal(t, "AAL1", pwdVerified["acr"])
	pwdAMR, ok := pwdVerified["amr"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{"pwd"}, pwdAMR)
	t.Logf("[2] 完成 password(first) → acr=%v amr=%v", pwdVerified["acr"], pwdAMR)

	resp, err = client.Get(srv.URL + "/authorize/flow?flow_id=" + url.QueryEscape(flowID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, json.Unmarshal(readBody(t, resp), &desc))
	require.Equal(t, "AAL2", desc["target_acr"])
	require.Equal(t, "AAL1", desc["current_acr"])
	require.Equal(t, false, desc["satisfied"])
	steps, ok = desc["steps"].([]any)
	require.True(t, ok)
	require.Len(t, steps, 2)
	doneStep := steps[0].(map[string]any)
	pendingStep := steps[1].(map[string]any)
	require.Equal(t, "password", doneStep["factor"])
	require.Equal(t, "first", doneStep["role"])
	require.Equal(t, "done", doneStep["status"])
	require.Equal(t, "totp", pendingStep["factor"])
	require.Equal(t, "second", pendingStep["role"])
	require.Equal(t, "pending", pendingStep["status"])
	hint, ok := pendingStep["challenge_hint"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "input_totp", hint["next_action"])
	t.Logf("[3] 编排层自动纳入第二因子 → steps=%v current_acr=%v", dumpSteps(steps), desc["current_acr"])

	resp = postJSON(t, client, srv.URL+"/auth/challenge/totp", map[string]any{
		"flow_id": flowID, "purpose": "login",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var totpCh map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &totpCh))
	totpChallenge, _ := totpCh["challenge_id"].(string)
	require.NotEmpty(t, totpChallenge)
	require.Equal(t, "input_totp", totpCh["next_action"])

	code := holding.TOTPCode(totpSharedSecret, time.Now().UTC(), 6, 30)
	require.Len(t, code, 6)
	resp = postJSON(t, client, srv.URL+"/auth/verify/totp", map[string]any{
		"challenge_id": totpChallenge, "response": code, "flow_id": flowID,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var totpVerified map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &totpVerified))
	require.Equal(t, true, totpVerified["verified"])
	require.Equal(t, "AAL2", totpVerified["acr"])
	totpAMR, ok := totpVerified["amr"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{"pwd", "totp"}, totpAMR)
	t.Logf("[4] 完成 totp(second) → acr=%v amr=%v (code=%s)", totpVerified["acr"], totpAMR, code)

	resp = postJSON(t, client, srv.URL+"/authorize/submit", map[string]any{
		"flow_id": flowID, "answers": []any{},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotEmpty(t, resp.Header.Values("Set-Cookie"))
	var submit map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &submit))
	require.Equal(t, "completed", submit["status"])
	codeAuth, _ := submit["code"].(string)
	require.NotEmpty(t, codeAuth)
	require.Equal(t, totpRedirectURI, submit["redirect_uri"])
	require.Equal(t, "state-totp", submit["state"])

	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("code", codeAuth)
	tokenForm.Set("redirect_uri", totpRedirectURI)
	tokenForm.Set("client_id", totpClientID)
	tokenForm.Set("code_verifier", totpVerifier)
	resp = postForm(t, client, srv.URL+"/oauth/token", tokenForm)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var tokens map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &tokens))
	idToken, _ := tokens["id_token"].(string)
	require.NotEmpty(t, idToken)

	jwksResp, err := client.Get(srv.URL + "/.well-known/jwks.json")
	require.NoError(t, err)
	var doc crypto.JWKSDocument
	require.NoError(t, json.Unmarshal(readBody(t, jwksResp), &doc))
	require.NotEmpty(t, doc.Keys)

	pub := publicKeyForIDToken(t, idToken, doc)
	parsed, err := jwt.Parse(idToken, func(*jwt.Token) (interface{}, error) { return pub, nil },
		jwt.WithValidMethods([]string{crypto.AlgoSM3WithSM2}))
	require.NoError(t, err)
	require.True(t, parsed.Valid, "id_token 应通过 JWKS 公钥验签")
	idClaims := parsed.Claims.(jwt.MapClaims)
	require.Equal(t, "AAL2", idClaims["acr"])
	idAMR, ok := idClaims["amr"].([]interface{})
	require.True(t, ok)
	require.Equal(t, []interface{}{"pwd", "totp"}, idAMR)
	t.Logf("[5] /oauth/token → id_token 验签成功 kid=%v acr=%v amr=%v", parsed.Header["kid"], idClaims["acr"], idAMR)

	var user model.User
	require.NoError(t, fx.db.Where("username = ?", totpUsername).First(&user).Error)
	var totpAuth model.Authenticator
	require.NoError(t, fx.db.Where("user_id = ? AND type = ?", user.ID, "totp").First(&totpAuth).Error)
	require.EqualValues(t, 0, totpAuth.FailCount)
	require.NotNil(t, totpAuth.LastUsedAt)
}

func TestE2ETotpWrongCodeRejected(t *testing.T) {
	fx := setupTOTPE2E(t)
	srv := httptest.NewServer(fx.engine)
	defer srv.Close()

	client := noRedirectClient(t)
	ctx := context.Background()

	params := totpAuthorizeQuery("state-totp-bad")
	resp, err := client.Get(srv.URL + "/authorize?" + params.Encode())
	require.NoError(t, err)
	location := resp.Header.Get("Location")
	resp.Body.Close()
	loginURL, err := url.Parse(location)
	require.NoError(t, err)
	flowID := loginURL.Query().Get("flow_id")
	require.NotEmpty(t, flowID)
	defer func() { _ = fx.rdb.Client().Del(ctx, "guanjin:flow:"+flowID) }()

	resp = postJSON(t, client, srv.URL+"/auth/challenge/password", map[string]any{
		"flow_id": flowID, "purpose": "login", "user_hint": totpUsername,
	})
	var ch map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &ch))
	pwdChallenge, _ := ch["challenge_id"].(string)
	resp = postJSON(t, client, srv.URL+"/auth/verify/password", map[string]any{
		"challenge_id": pwdChallenge, "response": totpPassword, "flow_id": flowID,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	readBody(t, resp)

	resp = postJSON(t, client, srv.URL+"/auth/challenge/totp", map[string]any{
		"flow_id": flowID, "purpose": "login",
	})
	var totpCh map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &totpCh))
	totpChallenge, _ := totpCh["challenge_id"].(string)
	require.NotEmpty(t, totpChallenge)

	resp = postJSON(t, client, srv.URL+"/auth/verify/totp", map[string]any{
		"challenge_id": totpChallenge, "response": "000000", "flow_id": flowID,
	})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	var badErr map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &badErr))
	require.Equal(t, "INVALID_CREDENTIAL", badErr["code"])

	var user model.User
	require.NoError(t, fx.db.Where("username = ?", totpUsername).First(&user).Error)
	var totpAuth model.Authenticator
	require.NoError(t, fx.db.Where("user_id = ? AND type = ?", user.ID, "totp").First(&totpAuth).Error)
	require.EqualValues(t, 1, totpAuth.FailCount)
	t.Logf("[6] 错误 TOTP 码 → code=%v fail_count=%d", badErr["code"], totpAuth.FailCount)
}

func dumpSteps(steps []any) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, m["factor"].(string)+"/"+m["role"].(string)+"/"+m["status"].(string))
	}
	return out
}
