package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/agent"
	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const agentSecret = "order-service-agent-secret"

func masterKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 9)
	}
	return key
}

type fixture struct {
	engine   *gin.Engine
	server   *httptest.Server
	db       *gorm.DB
	clientID string
	version  atomic.Int32
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dbURL := os.Getenv("GUANJIN_TEST_DATABASE_URL")
	redisURL := os.Getenv("GUANJIN_TEST_REDIS_URL")
	if dbURL == "" || redisURL == "" {
		t.Skip("set GUANJIN_TEST_DATABASE_URL and GUANJIN_TEST_REDIS_URL to run agent E2E")
	}
	require.NoError(t, store.MigrateUp(dbURL))
	db, err := store.Open(store.Options{DSN: dbURL})
	require.NoError(t, err)
	rdb, err := store.NewRedis(redisURL, 2*time.Second)
	require.NoError(t, err)
	require.NoError(t, rdb.Ping(context.Background()))

	mk := masterKey()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	clientID := "agent-" + suffix
	require.NoError(t, db.Exec("DELETE FROM policy_snapshots WHERE client_id = ?", clientID).Error)
	require.NoError(t, db.Exec("DELETE FROM role_bindings WHERE client_id = ?", clientID).Error)
	require.NoError(t, db.Exec("DELETE FROM clients WHERE client_id = ?", clientID).Error)

	secretEnc, err := store.EncryptClientSecret(mk, agentSecret)
	require.NoError(t, err)
	client := &model.Client{
		ClientID:               clientID,
		Name:                   "Agent E2E",
		Tier:                   "L2",
		Status:                 "active",
		RedirectURIs:           pq.StringArray{},
		PostLogoutRedirectURIs: pq.StringArray{},
		SecretEnc:              secretEnc,
	}
	require.NoError(t, db.Create(client).Error)

	service := agent.New(agent.Deps{
		DB:        db,
		Clients:   store.NewClientRepo(db, mk),
		Redis:     rdb,
		Snapshots: store.NewPolicySnapshotRepo(db),
		Bindings:  store.NewRoleBindingRepo(db),
		MasterKey: mk,
	})

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(httpx.Trace())
	service.RegisterRoutes(engine)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM policy_snapshots WHERE client_id = ?", clientID).Error
		_ = db.Exec("DELETE FROM role_bindings WHERE client_id = ?", clientID).Error
		_ = db.Exec("DELETE FROM clients WHERE client_id = ?", clientID).Error
		_ = rdb.Close()
	})

	return &fixture{engine: engine, server: server, db: db, clientID: clientID}
}

func (f *fixture) nextVersion() int32 {
	return f.version.Add(1)
}

func (f *fixture) reportBody(version int32) []byte {
	body := map[string]any{
		"client_id":         f.clientID,
		"sdk_version":       "0.7.0",
		"version":           version,
		"model_conf_digest": "sm3:abc",
		"permissions":       []string{"orders:read", "orders:write"},
		"roles": []any{
			map[string]any{"role": "order_admin", "permissions": []string{"orders:read", "orders:write"}},
			map[string]any{"role": "order_viewer", "permissions": []string{"orders:read"}},
		},
		"interface_map": []any{
			map[string]any{"path": "/orders", "method": "GET", "resource": "orders", "action": "read"},
		},
		"default_role": map[string]any{"role": "user", "permissions": []string{"profile:read"}},
		"label_defs": []any{
			map[string]any{"resource": "orders", "level": 1},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return raw
}

func (f *fixture) signed(method, path, query string, body []byte, timestamp int64, nonce string) map[string]string {
	canonical := agent.Canonical(method, path, query, agent.SHA256Hex(body), timestamp, nonce)
	return map[string]string{
		agent.HeaderClient:    f.clientID,
		agent.HeaderTimestamp: strconv.FormatInt(timestamp, 10),
		agent.HeaderNonce:     nonce,
		agent.HeaderSignature: agent.Signature([]byte(agentSecret), canonical),
		"Content-Type":        "application/json",
	}
}

func (f *fixture) send(t *testing.T, method, url string, body []byte, headers map[string]string) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, f.server.URL+url, reader)
	require.NoError(t, err)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var parsed map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &parsed)
	}
	return resp, parsed
}

func TestAgentReportAndPolicies(t *testing.T) {
	f := setup(t)

	t.Run("report stores snapshot and writes default role", func(t *testing.T) {
		version := f.nextVersion()
		body := f.reportBody(version)
		nonce := "nonce-report-" + uuid.NewString()
		headers := f.signed(http.MethodPost, "/api/v1/agent/report", "", body, time.Now().Unix(), nonce)
		resp, parsed := f.send(t, http.MethodPost, "/api/v1/agent/report", body, headers)
		require.Equal(t, http.StatusOK, resp.StatusCode, parsed)
		require.NotEmpty(t, parsed["snapshot_id"])
		require.EqualValues(t, version, parsed["stored_version"])

		var stored model.PolicySnapshot
		require.NoError(t, f.db.Where("client_id = ? AND version = ?", f.clientID, version).First(&stored).Error)
		var generic any
		require.NoError(t, json.Unmarshal(body, &generic))
		canonical, err := json.Marshal(generic)
		require.NoError(t, err)
		var storedGeneric any
		require.NoError(t, json.Unmarshal(stored.Payload, &storedGeneric))
		storedCanonical, err := json.Marshal(storedGeneric)
		require.NoError(t, err)
		require.Equal(t, canonical, storedCanonical)
		require.Equal(t, crypto.Sum(storedCanonical), stored.PayloadHash)
		expectedTag := crypto.HMACSM3(crypto.Derive(masterKey(), "guanjin/hmac/policy-snapshot", 32), stored.PayloadHash)
		require.Equal(t, expectedTag, stored.IntegrityTag)

		var client model.Client
		require.NoError(t, f.db.Where("client_id = ?", f.clientID).First(&client).Error)
		require.NotNil(t, client.DefaultRole)
		require.Equal(t, "user", *client.DefaultRole)
	})

	t.Run("tampered body yields AGENT_SIGNATURE_INVALID", func(t *testing.T) {
		version := f.nextVersion()
		body := f.reportBody(version)
		nonce := "nonce-tamper-" + uuid.NewString()
		headers := f.signed(http.MethodPost, "/api/v1/agent/report", "", body, time.Now().Unix(), nonce)
		tampered := bytes.Replace(body, []byte(`"0.7.0"`), []byte(`"9.9.9"`), 1)
		resp, parsed := f.send(t, http.MethodPost, "/api/v1/agent/report", tampered, headers)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		require.Equal(t, "AGENT_SIGNATURE_INVALID", parsed["code"])
	})

	t.Run("timestamp skew yields AGENT_TIMESTAMP_SKEW", func(t *testing.T) {
		body := f.reportBody(f.nextVersion())
		ts := time.Now().Add(-600 * time.Second).Unix()
		nonce := "nonce-skew-" + uuid.NewString()
		headers := f.signed(http.MethodPost, "/api/v1/agent/report", "", body, ts, nonce)
		resp, parsed := f.send(t, http.MethodPost, "/api/v1/agent/report", body, headers)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		require.Equal(t, "AGENT_TIMESTAMP_SKEW", parsed["code"])
	})

	t.Run("nonce replay yields AGENT_NONCE_REPLAY", func(t *testing.T) {
		body := f.reportBody(f.nextVersion())
		nonce := "nonce-replay-" + uuid.NewString()
		headers := f.signed(http.MethodPost, "/api/v1/agent/report", "", body, time.Now().Unix(), nonce)
		resp, parsed := f.send(t, http.MethodPost, "/api/v1/agent/report", body, headers)
		require.Equal(t, http.StatusOK, resp.StatusCode, parsed)
		resp, parsed = f.send(t, http.MethodPost, "/api/v1/agent/report", body, headers)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		require.Equal(t, "AGENT_NONCE_REPLAY", parsed["code"])
	})

	t.Run("unknown client yields AGENT_UNKNOWN_CLIENT", func(t *testing.T) {
		body := f.reportBody(f.nextVersion())
		ts := time.Now().Unix()
		nonce := "nonce-unknown-" + uuid.NewString()
		canonical := agent.Canonical(http.MethodPost, "/api/v1/agent/report", "", agent.SHA256Hex(body), ts, nonce)
		headers := map[string]string{
			agent.HeaderClient:    "ghost-" + uuid.NewString(),
			agent.HeaderTimestamp: strconv.FormatInt(ts, 10),
			agent.HeaderNonce:     nonce,
			agent.HeaderSignature: agent.Signature([]byte(agentSecret), canonical),
		}
		resp, parsed := f.send(t, http.MethodPost, "/api/v1/agent/report", body, headers)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		require.Equal(t, "AGENT_UNKNOWN_CLIENT", parsed["code"])
	})

	t.Run("disabled secret yields AGENT_SECRET_DISABLED", func(t *testing.T) {
		disabledID := "agent-disabled-" + uuid.NewString()[:8]
		require.NoError(t, f.db.Exec("DELETE FROM clients WHERE client_id = ?", disabledID).Error)
		require.NoError(t, f.db.Create(&model.Client{
			ClientID: disabledID, Name: "Disabled Agent", Tier: "L2", Status: "active",
			RedirectURIs: pq.StringArray{}, PostLogoutRedirectURIs: pq.StringArray{},
		}).Error)
		t.Cleanup(func() { _ = f.db.Exec("DELETE FROM clients WHERE client_id = ?", disabledID).Error })

		body := f.reportBody(f.nextVersion())
		ts := time.Now().Unix()
		nonce := "nonce-disabled-" + uuid.NewString()
		canonical := agent.Canonical(http.MethodPost, "/api/v1/agent/report", "", agent.SHA256Hex(body), ts, nonce)
		headers := map[string]string{
			agent.HeaderClient:    disabledID,
			agent.HeaderTimestamp: strconv.FormatInt(ts, 10),
			agent.HeaderNonce:     nonce,
			agent.HeaderSignature: agent.Signature([]byte(agentSecret), canonical),
		}
		resp, parsed := f.send(t, http.MethodPost, "/api/v1/agent/report", body, headers)
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		require.Equal(t, "AGENT_SECRET_DISABLED", parsed["code"])
	})

	t.Run("policies returns role bindings only", func(t *testing.T) {
		user := &model.User{Username: "agent-user-" + uuid.NewString()[:8], Status: "active"}
		require.NoError(t, store.NewUserRepo(f.db).Create(context.Background(), user))
		t.Cleanup(func() { _ = f.db.Exec("DELETE FROM users WHERE id = ?", user.ID).Error })
		require.NoError(t, f.db.Create(&model.RoleBinding{
			UserID: user.ID, ClientID: f.clientID, Role: "order_admin",
			Status: "active", GrantedAt: time.Now().UTC(),
		}).Error)

		query := url.Values{}
		query.Set("since_version", "0")
		query.Add("user_ids[]", user.ID.String())
		encoded := query.Encode()
		nonce := "nonce-policies-" + uuid.NewString()
		headers := f.signed(http.MethodGet, "/api/v1/agent/policies", encoded, nil, time.Now().Unix(), nonce)
		resp, parsed := f.send(t, http.MethodGet, "/api/v1/agent/policies?"+encoded, nil, headers)
		require.Equal(t, http.StatusOK, resp.StatusCode, parsed)
		storedVersion, err := store.NewPolicySnapshotRepo(f.db).MaxVersionByClient(context.Background(), f.clientID)
		require.NoError(t, err)
		require.NotZero(t, storedVersion)
		require.EqualValues(t, storedVersion, parsed["version"])
		require.NotEmpty(t, parsed["issued_at"])
		bindings, ok := parsed["bindings"].([]any)
		require.True(t, ok)
		require.Len(t, bindings, 1)
		item, ok := bindings[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, user.ID.String(), item["user_id"])
		require.Equal(t, []any{"order_admin"}, item["roles"])
		_, hasPermissions := item["permissions"]
		require.False(t, hasPermissions)
	})
}

func TestCanonicalMatchesJavaSDKVector(t *testing.T) {
	canonical := agent.Canonical("POST", "/api/v1/agent/report", "", "deadbeef", 1730000000, "n1")
	require.Equal(t, "POST\n/api/v1/agent/report\n\ndeadbeef\n1730000000\nn1", canonical)
	require.Equal(t, "DVwGHNJV5YLz24jkk7dqwOoTjN6I1TwnPBkbAh15ETQ=",
		agent.Signature([]byte("order-service-agent-secret"), canonical))
	require.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", agent.SHA256Hex(nil))
}
