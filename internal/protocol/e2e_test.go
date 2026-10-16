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
	require.NoError(t, func() error {
		if _, err := keysRepo.EnsureKeypair(ctx, crypto.PurposeOIDC); err != nil {
			return err
		}
		_, err := keysRepo.EnsureKeypair(ctx, crypto.PurposeGateway)
		return err
	}())

	seedE2E(t, db, keysRepo, masterKey)

	protoServer := New(Deps{
		Config:         Config{Issuer: "http://localhost:8080", PublicBaseURL: "http://localhost:8080", SecureCookie: false},
		Redis:          rdb,
		Users:          store.NewUserRepo(db),
		Clients:        store.NewClientRepo(db, masterKey),
		Identifiers:    store.NewIdentifierRepo(db),
		Authenticators: store.NewAuthenticatorRepo(db),
		Sessions:       store.NewSessionRepo(db),
		Keys:           keysRepo,
		Policies:       store.NewLoginPolicyRepo(db),
		Roles:          store.NewRoleBindingRepo(db),
		SessionMgr:     session.NewManager(store.NewSessionRepo(db)),
		Blind:          identifier.NewBlindIndexer(masterKey),
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
		require.NoError(t, db.Exec("DELETE FROM authenticators WHERE user_id = ?", existing.ID).Error)
		require.NoError(t, db.Exec("DELETE FROM identifiers WHERE user_id = ?", existing.ID).Error)
		require.NoError(t, db.Exec("DELETE FROM users WHERE id = ?", existing.ID).Error)
	}
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

	// 1) /authorize 未登录 → 302 /login
	resp, err := client.Get(srv.URL + "/authorize?" + authParams.Encode())
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	location := resp.Header.Get("Location")
	require.True(t, strings.HasPrefix(location, "/login?"), "location=%s", location)
	resp.Body.Close()

	// 2) POST /login 正确口令 → 302 /authorize 且建立会话与 cookie
	form := url.Values{}
	form.Set("username", e2eUsername)
	form.Set("password", e2ePassword)
	for k, v := range authParams {
		form.Set(k, v[0])
	}
	resp = postForm(t, client, srv.URL+"/login", form)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.True(t, strings.HasPrefix(resp.Header.Get("Location"), "/authorize?"), "location=%s", resp.Header.Get("Location"))
	require.NotEmpty(t, resp.Header.Values("Set-Cookie"))
	resp.Body.Close()

	var user model.User
	require.NoError(t, fx.db.Where("username = ?", e2eUsername).First(&user).Error)
	var sessionCount int64
	require.NoError(t, fx.db.Model(&model.Session{}).Where("user_id = ? AND status = ?", user.ID, "active").Count(&sessionCount).Error)
	require.EqualValues(t, 1, sessionCount, "登录后应建立会话")

	// 3) 再 /authorize → 302 带 code
	resp, err = client.Get(srv.URL + "/authorize?" + authParams.Encode())
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	callback, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, e2eRedirectURI, callback.Scheme+"://"+callback.Host+callback.Path)
	code := callback.Query().Get("code")
	require.NotEmpty(t, code)
	require.Equal(t, "state-abc", callback.Query().Get("state"))
	resp.Body.Close()

	// 4) /oauth/token 授权码换令牌
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

	// 5) 断言 id_token 含 acr 且 amr 非空，且用 jwks 公钥验签成功
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
	amr, ok := idClaims["amr"].([]interface{})
	require.True(t, ok)
	require.NotEmpty(t, amr)
	t.Logf("id_token 验签成功: kid=%s acr=%v amr=%v", parsed.Header["kid"], idClaims["acr"], amr)

	// 6) /userinfo → 返回 sub
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

	// 7) refresh_token 轮换一次成功
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

	// 8) 重复使用旧 refresh_token → REFRESH_TOKEN_REUSED 且会话被撤销
	resp = postForm(t, client, srv.URL+"/oauth/token", refreshForm)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	var errBody map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &errBody))
	require.Equal(t, "REFRESH_TOKEN_REUSED", errBody["code"])

	sessions, err := store.NewSessionRepo(fx.db).ListActiveByUser(ctx, user.ID)
	require.NoError(t, err)
	require.Empty(t, sessions, "重放后会话应被撤销")
	t.Logf("refresh 重放被拒: code=%v 活动会话数=%d", errBody["code"], len(sessions))
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
