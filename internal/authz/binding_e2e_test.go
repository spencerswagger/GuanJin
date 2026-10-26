package authz_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/tjfoc/gmsm/sm2"
	"gorm.io/gorm"

	adminapi "github.com/spencerswagger/GuanJin/internal/admin"
	"github.com/spencerswagger/GuanJin/internal/audit"
	"github.com/spencerswagger/GuanJin/internal/authz"
	"github.com/spencerswagger/GuanJin/internal/credential"
	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/factor/holding"
	"github.com/spencerswagger/GuanJin/internal/gateway"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/identifier"
	"github.com/spencerswagger/GuanJin/internal/protocol"
	"github.com/spencerswagger/GuanJin/internal/session"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	issuer      = "http://localhost:8080"
	redirectURI = "http://localhost:9999/cb"
	password    = "GuanJin-Phase8-Pass-01"
	verifier    = "phase8-code-verifier-0123456789-abcdefg"
)

type fixture struct {
	db       *gorm.DB
	keys     *store.SigningKeyRepo
	server   *httptest.Server
	engine   *gin.Engine
	chain    *audit.Chain
	clientID string
	admin    *model.User
	target   *model.User
	def      *model.User
	binding  uuid.UUID
	adminTok string
	target1  string
	target2  string
}

func masterKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 21)
	}
	return key
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dbURL := os.Getenv("GUANJIN_TEST_DATABASE_URL")
	redisURL := os.Getenv("GUANJIN_TEST_REDIS_URL")
	if dbURL == "" || redisURL == "" {
		t.Skip("set GUANJIN_TEST_DATABASE_URL and GUANJIN_TEST_REDIS_URL to run authz E2E")
	}
	require.NoError(t, store.MigrateUp(dbURL))
	db, err := store.Open(store.Options{DSN: dbURL})
	require.NoError(t, err)
	rdb, err := store.NewRedis(redisURL, 2*time.Second)
	require.NoError(t, err)
	require.NoError(t, rdb.Ping(context.Background()))

	mk := masterKey()
	ctx := context.Background()
	keys := store.NewSigningKeyRepo(db, mk)
	require.NoError(t, db.Exec("DELETE FROM signing_keys").Error)
	_, err = keys.EnsureKeypair(ctx, crypto.PurposeOIDC)
	require.NoError(t, err)
	_, err = keys.EnsureKeypair(ctx, crypto.PurposeGateway)
	require.NoError(t, err)

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	clientID := "order-" + suffix
	require.NoError(t, db.Exec("DELETE FROM policy_snapshots WHERE client_id = ?", clientID).Error)
	require.NoError(t, db.Exec("DELETE FROM role_bindings WHERE client_id = ?", clientID).Error)
	require.NoError(t, db.Exec("DELETE FROM clients WHERE client_id = ?", clientID).Error)

	defaultRole := "user"
	client := &model.Client{
		ClientID:               clientID,
		Name:                   "订单系统",
		Tier:                   "L2",
		Status:                 "active",
		RedirectURIs:           pq.StringArray{redirectURI},
		PostLogoutRedirectURIs: pq.StringArray{},
		DefaultRole:            &defaultRole,
	}
	require.NoError(t, db.Create(client).Error)

	users := store.NewUserRepo(db)
	auths := store.NewAuthenticatorRepo(db)
	adminName := "sec-admin-" + suffix
	require.NoError(t, adminapi.Bootstrap(ctx, adminapi.BootstrapDeps{
		DB: db, Users: users, Authenticators: auths,
		Username: adminName, Password: password,
	}))
	admin, err := users.GetByUsername(ctx, adminName)
	require.NoError(t, err)
	target := seedUser(t, users, auths, "order-user-"+suffix)
	def := seedUser(t, users, auths, "default-user-"+suffix)

	snapshots := store.NewPolicySnapshotRepo(db)
	payload := map[string]any{
		"client_id":         clientID,
		"sdk_version":       "0.7.0",
		"version":           1,
		"model_conf_digest": "sm3:orders",
		"permissions":       []string{"orders:read", "orders:write"},
		"roles": []any{
			map[string]any{"role": "order_admin", "permissions": []string{"orders:read", "orders:write"}},
			map[string]any{"role": "order_viewer", "permissions": []string{"orders:read"}},
		},
		"interface_map": []any{
			map[string]any{"path": "/orders", "method": "GET", "resource": "orders", "action": "read"},
		},
		"default_role": map[string]any{"role": "user", "permissions": []string{"profile:read"}},
	}
	rawPayload, err := json.Marshal(payload)
	require.NoError(t, err)
	hash := crypto.Sum(rawPayload)
	require.NoError(t, snapshots.Create(ctx, &model.PolicySnapshot{
		ClientID: clientID, Version: 1, Payload: rawPayload, PayloadHash: hash,
		IntegrityTag: crypto.HMACSM3(crypto.Derive(mk, "guanjin/hmac/policy-snapshot", 32), hash),
		ReportedAt:   time.Now().UTC(),
	}))

	rolesRepo := store.NewRoleBindingRepo(db)
	chain := audit.NewChain(db, mk)
	authzService := authz.New(authz.Deps{
		DB: db, Users: users, Clients: store.NewClientRepo(db, mk), Snapshots: snapshots, Audit: chain,
	})
	adminServer := adminapi.New(adminapi.Deps{
		Keys: keys, Users: users, Clients: store.NewClientRepo(db, mk),
		Snapshots: snapshots, Bindings: rolesRepo, Authz: authzService,
	})

	registry := factor.NewRegistry()
	require.NoError(t, registry.Register(holding.NewPasswordProvider(auths, store.NewChallengeRepo(db),
		holding.DefaultFailThreshold, holding.DefaultLockDuration)))

	proto := protocol.New(protocol.Deps{
		Config:         protocol.Config{Issuer: issuer, PublicBaseURL: issuer, SecureCookie: false},
		Redis:          rdb,
		DB:             db,
		Users:          users,
		Clients:        store.NewClientRepo(db, mk),
		Identifiers:    store.NewIdentifierRepo(db),
		Authenticators: auths,
		Challenges:     store.NewChallengeRepo(db),
		Sessions:       store.NewSessionRepo(db),
		Keys:           keys,
		Policies:       store.NewLoginPolicyRepo(db),
		Roles:          rolesRepo,
		Visited:        store.NewUserVisitedClientRepo(db),
		AuthEvents:     store.NewAuthEventRepo(db),
		SessionMgr:     session.NewManager(store.NewSessionRepo(db)),
		Blind:          identifier.NewBlindIndexer(mk),
		Registry:       registry,
	})

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(httpx.Trace())
	proto.RegisterRoutes(engine)
	adminServer.RegisterRoutes(engine)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM role_bindings WHERE client_id = ?", clientID).Error
		_ = db.Exec("DELETE FROM policy_snapshots WHERE client_id = ?", clientID).Error
		_ = rdb.Close()
	})

	return &fixture{
		db: db, keys: keys, server: server, engine: engine, chain: chain,
		clientID: clientID, admin: admin, target: target, def: def,
	}
}

func seedUser(t *testing.T, users *store.UserRepo, auths *store.AuthenticatorRepo, username string) *model.User {
	t.Helper()
	data, err := credential.Hash(password, credential.DefaultPolicy())
	require.NoError(t, err)
	user := &model.User{Username: username, Status: "active"}
	require.NoError(t, users.Create(context.Background(), user))
	require.NoError(t, auths.Create(context.Background(), &model.Authenticator{
		UserID: user.ID, Type: "password", CredentialData: data, Status: "active",
	}))
	return user
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func login(t *testing.T, baseURL, clientID, username, userPassword string) string {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	httpClient := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", "openid profile")
	query.Set("state", "state-"+uuid.NewString()[:8])
	query.Set("nonce", "nonce-"+uuid.NewString()[:8])
	query.Set("code_challenge", pkceChallenge(verifier))
	query.Set("code_challenge_method", "S256")

	resp, err := httpClient.Get(baseURL + "/authorize?" + query.Encode())
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, resp.StatusCode)
	location := resp.Header.Get("Location")
	_ = resp.Body.Close()
	require.True(t, strings.HasPrefix(location, "/login?"), "location=%s", location)
	loginURL, err := url.Parse(location)
	require.NoError(t, err)
	flowID := loginURL.Query().Get("flow_id")
	require.NotEmpty(t, flowID)

	challenge := postJSON(t, httpClient, baseURL+"/auth/challenge/password", map[string]any{
		"flow_id": flowID, "purpose": "login", "user_hint": username,
	})
	require.Equal(t, http.StatusOK, challenge.status, challenge.body)
	var challengeBody map[string]any
	require.NoError(t, json.Unmarshal(challenge.body, &challengeBody))
	challengeID, _ := challengeBody["challenge_id"].(string)
	require.NotEmpty(t, challengeID)

	verified := postJSON(t, httpClient, baseURL+"/auth/verify/password", map[string]any{
		"challenge_id": challengeID, "response": userPassword, "flow_id": flowID,
	})
	require.Equal(t, http.StatusOK, verified.status, verified.body)

	submitted := postJSON(t, httpClient, baseURL+"/authorize/submit", map[string]any{
		"flow_id": flowID, "answers": []any{},
	})
	require.Equal(t, http.StatusOK, submitted.status, submitted.body)
	var submitBody map[string]any
	require.NoError(t, json.Unmarshal(submitted.body, &submitBody))
	code, _ := submitBody["code"].(string)
	require.NotEmpty(t, code)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	tokenResp := postForm(t, httpClient, baseURL+"/oauth/token", form)
	require.Equal(t, http.StatusOK, tokenResp.status, tokenResp.body)
	var tokens map[string]any
	require.NoError(t, json.Unmarshal(tokenResp.body, &tokens))
	access, _ := tokens["access_token"].(string)
	require.NotEmpty(t, access)
	return access
}

type response struct {
	status int
	body   []byte
}

func postJSON(t *testing.T, client *http.Client, target string, payload any) response {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return response{status: resp.StatusCode, body: body}
}

func postForm(t *testing.T, client *http.Client, target string, form url.Values) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return response{status: resp.StatusCode, body: body}
}

func (f *fixture) authed(t *testing.T, method, path, token string, payload any) response {
	t.Helper()
	var reader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.server.URL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return response{status: resp.StatusCode, body: body}
}

func (f *fixture) claims(t *testing.T, token string) jwt.MapClaims {
	t.Helper()
	_, priv, err := f.keys.ActiveSigner(context.Background(), crypto.PurposeOIDC)
	require.NoError(t, err)
	parsed, err := jwt.Parse(token, func(*jwt.Token) (interface{}, error) {
		return &priv.PublicKey, nil
	}, jwt.WithValidMethods([]string{crypto.AlgoSM3WithSM2}))
	require.NoError(t, err)
	claims, ok := parsed.Claims.(jwt.MapClaims)
	require.True(t, ok)
	return claims
}

func rolesOf(t *testing.T, claims jwt.MapClaims) []string {
	t.Helper()
	raw, ok := claims["roles"].([]any)
	require.True(t, ok)
	roles := make([]string, 0, len(raw))
	for _, item := range raw {
		value, ok := item.(string)
		require.True(t, ok)
		roles = append(roles, value)
	}
	return roles
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	return envelope.Code
}

func TestBindingLifecycle(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	t.Run("bootstrap admin token carries three roles", func(t *testing.T) {
		var admin model.User
		require.NoError(t, f.db.Where("id = ?", f.admin.ID).First(&admin).Error)
		require.True(t, admin.IsAdmin)
		f.adminTok = login(t, f.server.URL, f.clientID, admin.Username, password)
		roles := rolesOf(t, f.claims(t, f.adminTok))
		require.ElementsMatch(t, []string{"sys_admin", "sec_admin", "auditor"}, roles)
	})

	t.Run("unbound user token has empty roles but client default role registered", func(t *testing.T) {
		f.target1 = login(t, f.server.URL, f.clientID, f.target.Username, password)
		require.Empty(t, rolesOf(t, f.claims(t, f.target1)))

		defToken := login(t, f.server.URL, f.clientID, f.def.Username, password)
		require.Empty(t, rolesOf(t, f.claims(t, defToken)))

		var client model.Client
		require.NoError(t, f.db.Where("client_id = ?", f.clientID).First(&client).Error)
		require.NotNil(t, client.DefaultRole)
		require.Equal(t, "user", *client.DefaultRole)
	})

	t.Run("bind revokes sessions and writes auditable chain", func(t *testing.T) {
		resp := f.authed(t, http.MethodPost, "/api/v1/policies/bindings", f.adminTok, map[string]any{
			"user_id": f.target.ID.String(), "client_id": f.clientID,
			"role": "order_admin", "reason": "phase8 e2e",
		})
		require.Equal(t, http.StatusOK, resp.status, string(resp.body))
		var body map[string]any
		require.NoError(t, json.Unmarshal(resp.body, &body))
		bindingID, _ := body["binding_id"].(string)
		require.NotEmpty(t, bindingID)
		f.binding = uuid.MustParse(bindingID)

		var active int64
		require.NoError(t, f.db.Model(&model.Session{}).
			Where("user_id = ? AND status = ?", f.target.ID, "active").Count(&active).Error)
		require.Zero(t, active, "绑定后该用户全部会话必须被撤销")

		var binding model.RoleBinding
		require.NoError(t, f.db.Where("id = ?", f.binding).First(&binding).Error)
		require.Equal(t, "active", binding.Status)

		var logs int64
		require.NoError(t, f.db.Model(&model.AuditLog{}).
			Where("action = ? AND target_id = ?", "role.bind", bindingID).Count(&logs).Error)
		require.EqualValues(t, 1, logs)
		require.NoError(t, f.chain.Verify(ctx))
	})

	t.Run("relogin injects role into token assertion and userinfo", func(t *testing.T) {
		f.target2 = login(t, f.server.URL, f.clientID, f.target.Username, password)
		claims := f.claims(t, f.target2)
		require.Contains(t, rolesOf(t, claims), "order_admin")

		resp := f.authed(t, http.MethodGet, "/userinfo", f.target2, nil)
		require.Equal(t, http.StatusOK, resp.status, string(resp.body))
		var info map[string]any
		require.NoError(t, json.Unmarshal(resp.body, &info))
		infoRoles, ok := info["roles"].([]any)
		require.True(t, ok)
		require.Contains(t, infoRoles, "order_admin")

		assertion := gatewayAssertion(t, f, f.target2)
		assertionRoles := rolesOf(t, assertion)
		require.Contains(t, assertionRoles, "order_admin")
		require.Equal(t, rolesOf(t, claims), assertionRoles)
	})

	t.Run("non sec_admin token is rejected", func(t *testing.T) {
		resp := f.authed(t, http.MethodPost, "/api/v1/policies/bindings", f.target2, map[string]any{
			"user_id": f.target.ID.String(), "client_id": f.clientID, "role": "order_viewer",
		})
		require.Equal(t, http.StatusForbidden, resp.status, string(resp.body))
		require.Equal(t, "INSUFFICIENT_ROLE", errorCode(t, resp.body))
	})

	t.Run("unknown role and duplicate binding", func(t *testing.T) {
		resp := f.authed(t, http.MethodPost, "/api/v1/policies/bindings", f.adminTok, map[string]any{
			"user_id": f.target.ID.String(), "client_id": f.clientID, "role": "ghost_role",
		})
		require.Equal(t, http.StatusUnprocessableEntity, resp.status, string(resp.body))
		require.Equal(t, "ROLE_UNKNOWN", errorCode(t, resp.body))

		resp = f.authed(t, http.MethodPost, "/api/v1/policies/bindings", f.adminTok, map[string]any{
			"user_id": f.target.ID.String(), "client_id": f.clientID, "role": "order_admin",
		})
		require.Equal(t, http.StatusConflict, resp.status, string(resp.body))
		require.Equal(t, "BINDING_ALREADY_EXISTS", errorCode(t, resp.body))
	})

	t.Run("unbind revokes sessions and appends audit", func(t *testing.T) {
		resp := f.authed(t, http.MethodDelete, "/api/v1/policies/bindings/"+f.binding.String(), f.adminTok, nil)
		require.Equal(t, http.StatusOK, resp.status, string(resp.body))

		var binding model.RoleBinding
		require.NoError(t, f.db.Where("id = ?", f.binding).First(&binding).Error)
		require.Equal(t, "revoked", binding.Status)
		require.NotNil(t, binding.RevokedAt)

		var active int64
		require.NoError(t, f.db.Model(&model.Session{}).
			Where("user_id = ? AND status = ?", f.target.ID, "active").Count(&active).Error)
		require.Zero(t, active)

		var logs int64
		require.NoError(t, f.db.Model(&model.AuditLog{}).
			Where("action = ? AND target_id = ?", "role.unbind", f.binding.String()).Count(&logs).Error)
		require.EqualValues(t, 1, logs)
		require.NoError(t, f.chain.Verify(ctx))
	})

	t.Run("audit chain detects tampering", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 12)
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				_, err := f.chain.Append(ctx, audit.Entry{
					Action: "phase8.concurrent",
					Result: "success",
					After:  map[string]any{"index": index},
				})
				errs <- err
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		require.NoError(t, f.chain.Verify(ctx))

		var total int64
		require.NoError(t, f.db.Model(&model.AuditLog{}).Count(&total).Error)
		require.Greater(t, total, int64(2))
		var row model.AuditLog
		require.NoError(t, f.db.Order("occurred_at ASC, id ASC").Offset(int(total/2)).First(&row).Error)
		require.NoError(t, f.db.Exec("ALTER TABLE audit_logs DISABLE TRIGGER audit_logs_append_only").Error)
		t.Cleanup(func() {
			_ = f.db.Exec("ALTER TABLE audit_logs ENABLE TRIGGER audit_logs_append_only").Error
		})
		original := row.Action
		require.NoError(t, f.db.Exec("UPDATE audit_logs SET action = ? WHERE id = ?", "tampered", row.ID).Error)
		require.ErrorIs(t, f.chain.Verify(ctx), audit.ErrChainBroken)

		require.NoError(t, f.db.Exec("UPDATE audit_logs SET action = ? WHERE id = ?", original, row.ID).Error)
		require.NoError(t, f.chain.Verify(ctx))
		require.NoError(t, f.db.Exec("ALTER TABLE audit_logs ENABLE TRIGGER audit_logs_append_only").Error)
	})
}

func gatewayAssertion(t *testing.T, f *fixture, accessToken string) jwt.MapClaims {
	t.Helper()
	assertions := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertions <- r.Header.Get(gateway.AssertionHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	mk := masterKey()
	gw := gateway.New(gateway.Deps{
		Keys:       f.keys,
		Clients:    store.NewClientRepo(f.db, mk),
		Sessions:   store.NewSessionRepo(f.db),
		Policies:   store.NewLoginPolicyRepo(f.db),
		Roles:      store.NewRoleBindingRepo(f.db),
		Users:      store.NewUserRepo(f.db),
		Visited:    store.NewUserVisitedClientRepo(f.db),
		AuthEvents: store.NewAuthEventRepo(f.db),
		Router: gateway.NewTableRouter([]gateway.Route{
			{MatchType: gateway.MatchPrefix, Match: "/app", ClientID: f.clientID, Upstream: upstream.URL},
		}),
		Notifier:     gateway.NewRecordingSLO(),
		Issuer:       issuer,
		AssertionTTL: gateway.AssertionTTL,
	})
	gwServer := httptest.NewServer(gw)
	t.Cleanup(gwServer.Close)

	req, err := http.NewRequest(http.MethodGet, gwServer.URL+"/app/orders", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	select {
	case assertion := <-assertions:
		require.NotEmpty(t, assertion)
		return verifyAssertion(t, f.keys, assertion)
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not receive assertion")
		return nil
	}
}

func verifyAssertion(t *testing.T, keys *store.SigningKeyRepo, token string) jwt.MapClaims {
	t.Helper()
	parsed, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	require.NoError(t, err)
	kid, _ := parsed.Header["kid"].(string)
	rows, err := keys.ListPublishable(context.Background(), crypto.PurposeGateway)
	require.NoError(t, err)
	var pub *sm2.PublicKey
	for _, row := range rows {
		if row.Kid != kid {
			continue
		}
		pk, perr := store.PublicKeyFromText(row.PublicKey)
		require.NoError(t, perr)
		pub = pk
	}
	require.NotNil(t, pub)
	verified, err := jwt.Parse(token, func(*jwt.Token) (interface{}, error) { return pub, nil },
		jwt.WithValidMethods([]string{crypto.AlgoSM3WithSM2}))
	require.NoError(t, err)
	require.True(t, verified.Valid)
	return verified.Claims.(jwt.MapClaims)
}
