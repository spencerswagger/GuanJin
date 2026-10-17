package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/factor/channel"
	"github.com/spencerswagger/GuanJin/internal/factor/holding"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/identifier"
	"github.com/spencerswagger/GuanJin/internal/notify"
	"github.com/spencerswagger/GuanJin/internal/session"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	registerClientID    = "register-client"
	registerRedirectURI = "http://localhost:9997/cb"
	registerPhone       = "13812348888"
	registerEmail       = "reg-bob@example.com"
	registerPassword    = "GuanJin-Reg-Pass-01"
)

type registerFixture struct {
	engine      *gin.Engine
	db          *gorm.DB
	server      *Server
	rdb         *store.Redis
	sender      *channel.RecordingSender
	notifier    *notify.RecordingNotifier
	phoneNorm   string
	phoneMasked string
	emailNorm   string
}

func setupRegisterE2E(t *testing.T) *registerFixture {
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

	cleanupRegisterState(t, db, rdb)

	keysRepo := store.NewSigningKeyRepo(db, masterKey)
	require.NoError(t, db.Exec("DELETE FROM signing_keys").Error)
	require.NoError(t, func() error {
		if _, err := keysRepo.EnsureKeypair(ctx, crypto.PurposeOIDC); err != nil {
			return err
		}
		_, err := keysRepo.EnsureKeypair(ctx, crypto.PurposeGateway)
		return err
	}())

	require.NoError(t, db.Create(&model.Client{
		ClientID:               registerClientID,
		Name:                   "注册 E2E 客户端",
		Tier:                   "L2",
		Status:                 "active",
		RedirectURIs:           pq.StringArray{registerRedirectURI},
		PostLogoutRedirectURIs: pq.StringArray{},
	}).Error)

	sender := channel.NewRecordingSender()
	senders := map[string]channel.Sender{"sms": sender, "email": sender}
	challenges := store.NewChallengeRepo(db)
	channelDeps := channel.Deps{Challenges: challenges, Redis: rdb, ChannelSenders: senders}

	registry := factor.NewRegistry()
	require.NoError(t, registry.Register(holding.NewPasswordProvider(store.NewAuthenticatorRepo(db), challenges, holding.DefaultFailThreshold, holding.DefaultLockDuration)))
	require.NoError(t, registry.Register(channel.NewSmsOtpProvider(channelDeps)))
	require.NoError(t, registry.Register(channel.NewEmailOtpProvider(channelDeps)))

	notifier := notify.NewRecordingNotifier()
	protoServer := New(Deps{
		Config:         Config{Issuer: "http://localhost:8080", PublicBaseURL: "http://localhost:8080", SecureCookie: false},
		Redis:          rdb,
		DB:             db,
		Users:          store.NewUserRepo(db),
		Clients:        store.NewClientRepo(db, masterKey),
		Identifiers:    store.NewIdentifierRepo(db),
		Authenticators: store.NewAuthenticatorRepo(db),
		Challenges:     challenges,
		Sessions:       store.NewSessionRepo(db),
		Keys:           keysRepo,
		Policies:       store.NewLoginPolicyRepo(db),
		Roles:          store.NewRoleBindingRepo(db),
		Visited:        store.NewUserVisitedClientRepo(db),
		AuthEvents:     store.NewAuthEventRepo(db),
		SessionMgr:     session.NewManager(store.NewSessionRepo(db)),
		Blind:          identifier.NewBlindIndexer(masterKey),
		Registry:       registry,
		Notifier:       notifier,
	})

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(httpx.Trace())
	protoServer.RegisterRoutes(engine)

	phoneNorm, err := identifier.Normalize(identifier.TypePhone, registerPhone)
	require.NoError(t, err)
	emailNorm, err := identifier.Normalize(identifier.TypeEmail, registerEmail)
	require.NoError(t, err)

	fx := &registerFixture{
		engine:      engine,
		db:          db,
		server:      protoServer,
		rdb:         rdb,
		sender:      sender,
		notifier:    notifier,
		phoneNorm:   phoneNorm,
		phoneMasked: identifier.Mask(identifier.TypePhone, phoneNorm),
		emailNorm:   emailNorm,
	}
	t.Cleanup(func() { cleanupRegisterState(t, db, rdb) })
	return fx
}

func cleanupRegisterState(t *testing.T, db *gorm.DB, rdb *store.Redis) {
	t.Helper()
	ctx := context.Background()

	require.NoError(t, db.Exec("ALTER TABLE auth_events DISABLE TRIGGER auth_events_append_only").Error)
	defer func() {
		require.NoError(t, db.Exec("ALTER TABLE auth_events ENABLE TRIGGER auth_events_append_only").Error)
	}()

	phoneNorm, err := identifier.Normalize(identifier.TypePhone, registerPhone)
	require.NoError(t, err)
	emailNorm, err := identifier.Normalize(identifier.TypeEmail, registerEmail)
	require.NoError(t, err)
	phoneMasked := identifier.Mask(identifier.TypePhone, phoneNorm)
	emailMasked := identifier.Mask(identifier.TypeEmail, emailNorm)

	require.NoError(t, db.Exec("DELETE FROM challenges WHERE target_masked IN (?, ?)", phoneMasked, emailMasked).Error)
	require.NoError(t, db.Exec("DELETE FROM auth_events WHERE client_id = ?", registerClientID).Error)

	var uids []uuid.UUID
	require.NoError(t, db.Raw("SELECT user_id FROM user_visited_clients WHERE client_id = ?", registerClientID).Scan(&uids).Error)
	for _, uid := range uids {
		require.NoError(t, db.Exec("DELETE FROM auth_events WHERE user_id = ?", uid).Error)
		require.NoError(t, db.Exec("DELETE FROM challenges WHERE user_id = ?", uid).Error)
		require.NoError(t, db.Exec("DELETE FROM sessions WHERE user_id = ?", uid).Error)
		require.NoError(t, db.Exec("DELETE FROM authenticators WHERE user_id = ?", uid).Error)
		require.NoError(t, db.Exec("DELETE FROM identifiers WHERE user_id = ?", uid).Error)
		require.NoError(t, db.Exec("DELETE FROM user_visited_clients WHERE user_id = ?", uid).Error)
		require.NoError(t, db.Exec("DELETE FROM users WHERE id = ?", uid).Error)
	}

	require.NoError(t, db.Exec("DELETE FROM challenges WHERE client_id = ?", registerClientID).Error)
	require.NoError(t, db.Exec("DELETE FROM sessions WHERE client_id = ?", registerClientID).Error)
	require.NoError(t, db.Exec("DELETE FROM user_visited_clients WHERE client_id = ?", registerClientID).Error)
	require.NoError(t, db.Exec("DELETE FROM clients WHERE client_id = ?", registerClientID).Error)

	for _, pattern := range []string{
		"guanjin:register:*",
		"guanjin:ratelimit:*",
		"guanjin:ident:cooldown:*",
		"guanjin:refresh:*",
		"guanjin:sess-refresh:*",
		"guanjin:flow:*",
	} {
		clearRedisPattern(ctx, rdb, pattern)
	}
}

func clearRedisPattern(ctx context.Context, rdb *store.Redis, pattern string) {
	client := rdb.Client()
	if client == nil {
		return
	}
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return
		}
		if len(keys) > 0 {
			_ = client.Del(ctx, keys...).Err()
		}
		cursor = next
		if cursor == 0 {
			return
		}
	}
}

func (fx *registerFixture) codeFor(t *testing.T, target string) string {
	t.Helper()
	for _, sc := range fx.sender.All() {
		if sc.Target == target {
			return sc.Code
		}
	}
	t.Fatalf("内存 Sender 未记录发往 %s 的验证码", target)
	return ""
}

func authRequest(t *testing.T, client *http.Client, method, urlStr, token string, payload any) *http.Response {
	t.Helper()
	var body io.Reader
	if payload != nil {
		blob, err := json.Marshal(payload)
		require.NoError(t, err)
		body = bytes.NewReader(blob)
	}
	req, err := http.NewRequest(method, urlStr, body)
	require.NoError(t, err)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(readBody(t, resp), &out))
	return out
}

func TestE2ERegistrationAndIdentifierSelfService(t *testing.T) {
	fx := setupRegisterE2E(t)
	srv := httptest.NewServer(fx.engine)
	defer srv.Close()

	client := noRedirectClient(t)

	resp := postJSON(t, client, srv.URL+"/auth/register/start", map[string]any{
		"client_id": registerClientID,
		"type":      "phone",
		"value":     registerPhone,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	start := decodeJSON(t, resp)
	registerToken, _ := start["register_token"].(string)
	require.NotEmpty(t, registerToken)
	challengeID, _ := start["challenge_id"].(string)
	require.NotEmpty(t, challengeID)
	require.Equal(t, "type_code", start["next_action"])
	require.Equal(t, fx.phoneMasked, start["target_masked"])

	code := fx.codeFor(t, fx.phoneNorm)
	require.Len(t, code, 6)

	resp = postJSON(t, client, srv.URL+"/auth/register/complete", map[string]any{
		"register_token": registerToken,
		"code":           code,
		"password":       registerPassword,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	complete := decodeJSON(t, resp)
	require.Equal(t, true, complete["ok"])
	accessToken, _ := complete["access_token"].(string)
	idToken, _ := complete["id_token"].(string)
	refreshToken, _ := complete["refresh_token"].(string)
	require.NotEmpty(t, accessToken)
	require.NotEmpty(t, idToken)
	require.NotEmpty(t, refreshToken)
	require.Equal(t, "Bearer", complete["token_type"])
	require.EqualValues(t, 600, complete["expires_in"])
	t.Logf("[1] 注册即登录 三令牌: access=%s id=%s refresh=%s (code=%s)", accessToken, idToken, refreshToken, code)

	parsed, _, err := jwt.NewParser().ParseUnverified(accessToken, jwt.MapClaims{})
	require.NoError(t, err)
	claims, ok := parsed.Claims.(jwt.MapClaims)
	require.True(t, ok)
	sub, _ := claims["sub"].(string)
	require.NotEmpty(t, sub)
	uid, err := uuid.Parse(sub)
	require.NoError(t, err)
	require.Equal(t, "AAL1", claims["acr"])
	require.Equal(t, []any{"pwd", "sms_otp"}, claims["amr"])

	var user model.User
	require.NoError(t, fx.db.Where("id = ?", uid).First(&user).Error)
	var ident model.Identifier
	require.NoError(t, fx.db.Where("user_id = ? AND type = ?", uid, "phone").First(&ident).Error)
	require.True(t, ident.Verified)
	require.EqualValues(t, 2, ident.TrustLevel)
	require.True(t, ident.IsPrimary)
	require.NotNil(t, ident.VerifiedAt)
	var authCount int64
	require.NoError(t, fx.db.Model(&model.Authenticator{}).Where("user_id = ? AND type = ? AND status = ?", uid, "password", "active").Count(&authCount).Error)
	require.EqualValues(t, 1, authCount)
	var sessCount int64
	require.NoError(t, fx.db.Model(&model.Session{}).Where("user_id = ? AND status = ?", uid, "active").Count(&sessCount).Error)
	require.EqualValues(t, 1, sessCount)
	t.Logf("[2] 落库校验: user=%s identifier.verified=%v trust=%d authenticators=%d sessions=%d", uid, ident.Verified, ident.TrustLevel, authCount, sessCount)

	resp = authRequest(t, client, http.MethodGet, srv.URL+"/api/v1/me/identifiers", accessToken, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	list := decodeJSON(t, resp)
	items, ok := list["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	first, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "phone", first["type"])
	require.Equal(t, fx.phoneMasked, first["value_masked"])
	require.Equal(t, true, first["verified"])
	require.Equal(t, true, first["primary"])
	require.Equal(t, "sms_otp", first["bound_factor"])
	phoneID, _ := first["id"].(string)
	require.NotEmpty(t, phoneID)
	t.Logf("[3] GET /me/identifiers 脱敏: %v", items)

	resp = authRequest(t, client, http.MethodPost, srv.URL+"/api/v1/me/identifiers", accessToken, map[string]any{
		"type":  "email",
		"value": registerEmail,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	bind := decodeJSON(t, resp)
	emailID, _ := bind["id"].(string)
	bindChallenge, _ := bind["challenge_id"].(string)
	require.NotEmpty(t, emailID)
	require.NotEmpty(t, bindChallenge)
	require.Equal(t, false, bind["verified"])
	require.Equal(t, "r***@example.com", bind["target_masked"])

	emailCode := fx.codeFor(t, fx.emailNorm)
	resp = authRequest(t, client, http.MethodPost, srv.URL+"/api/v1/me/identifiers/"+emailID+"/verify", accessToken, map[string]any{
		"challenge_id": bindChallenge,
		"code":         emailCode,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	verified := decodeJSON(t, resp)
	require.Equal(t, true, verified["verified"])
	require.Equal(t, "r***@example.com", verified["value_masked"])
	var emailRow model.Identifier
	require.NoError(t, fx.db.Where("id = ?", emailID).First(&emailRow).Error)
	require.True(t, emailRow.Verified)
	require.EqualValues(t, 2, emailRow.TrustLevel)
	require.Equal(t, "email_otp", *emailRow.VerifyMethod)
	t.Logf("[4] 绑定并验证 email: id=%s verified=%v method=%s masked=%v", emailID, emailRow.Verified, *emailRow.VerifyMethod, verified["value_masked"])

	resp = authRequest(t, client, http.MethodDelete, srv.URL+"/api/v1/me/identifiers/"+emailID, accessToken, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	records := fx.notifier.Records()
	require.Len(t, records, 2)
	targets := map[string]int{}
	for _, rec := range records {
		targets[rec.Target]++
	}
	require.Equal(t, 1, targets[fx.phoneNorm])
	require.Equal(t, 1, targets[fx.emailNorm])
	t.Logf("[5] 解绑 email 且 Notifier 覆盖全部已绑标识符(含旧值): %v", targets)

	var disabledRow model.Identifier
	require.NoError(t, fx.db.Where("id = ?", emailID).First(&disabledRow).Error)
	require.Equal(t, "disabled", disabledRow.Status)

	resp = authRequest(t, client, http.MethodGet, srv.URL+"/api/v1/me/identifiers", accessToken, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	list = decodeJSON(t, resp)
	items, _ = list["items"].([]any)
	require.Len(t, items, 1)

	resp = authRequest(t, client, http.MethodDelete, srv.URL+"/api/v1/me/identifiers/"+phoneID, accessToken, nil)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	lastErr := decodeJSON(t, resp)
	require.Equal(t, "LAST_IDENTIFIER_CANNOT_UNBIND", lastErr["code"])
	t.Logf("[6] 解绑最后一个已验证标识符被拒: code=%v", lastErr["code"])

	resp = postJSON(t, client, srv.URL+"/auth/register/start", map[string]any{
		"client_id": registerClientID,
		"type":      "phone",
		"value":     registerPhone,
	})
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	dupErr := decodeJSON(t, resp)
	require.Equal(t, "IDENTIFIER_ALREADY_BOUND", dupErr["code"])
	t.Logf("[7] 标识符唯一性冲突: code=%v", dupErr["code"])
}