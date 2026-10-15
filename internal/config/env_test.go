package config

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validKey(n int) string {
	return base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", n)))
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	require.NoError(t, os.Unsetenv(key))
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func setRequiredValid(t *testing.T) {
	t.Helper()
	t.Setenv("GUANJIN_PUBLIC_BASE_URL", "https://id.example.com")
	t.Setenv("GUANJIN_DATABASE_URL", "postgres://guanjin:guanjin@127.0.0.1:5432/guanjin?sslmode=disable")
	t.Setenv("GUANJIN_REDIS_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("GUANJIN_MASTER_KEY", validKey(32))
}

func TestLoadMissingRequiredFails(t *testing.T) {
	setRequiredValid(t)
	unsetEnv(t, "GUANJIN_DATABASE_URL")

	_, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "GUANJIN_DATABASE_URL")
}

func TestLoadPublicBaseURLMissingFails(t *testing.T) {
	setRequiredValid(t)
	unsetEnv(t, "GUANJIN_PUBLIC_BASE_URL")

	_, err := Load()
	require.Error(t, err)
}

func TestLoadMasterKeyNotBase64Fails(t *testing.T) {
	setRequiredValid(t)
	t.Setenv("GUANJIN_MASTER_KEY", "!!!not-base64!!!")

	_, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "base64")
}

func TestLoadMasterKeyTooShortFails(t *testing.T) {
	setRequiredValid(t)
	t.Setenv("GUANJIN_MASTER_KEY", validKey(16))

	_, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "32")
}

func TestLoadAppliesDefaults(t *testing.T) {
	setRequiredValid(t)
	for _, k := range []string{
		"GUANJIN_ENV", "GUANJIN_LOG_LEVEL", "GUANJIN_LOG_FORMAT",
		"GUANJIN_HTTP_ADDR", "GUANJIN_GATEWAY_ADDR", "GUANJIN_ISSUER",
		"GUANJIN_DB_MAX_OPEN_CONNS", "GUANJIN_DB_MAX_IDLE_CONNS", "GUANJIN_DB_CONN_MAX_LIFETIME",
		"GUANJIN_REDIS_TIMEOUT", "GUANJIN_GATEWAY_UPSTREAM_TIMEOUT", "GUANJIN_GATEWAY_DIAL_TIMEOUT",
		"GUANJIN_METRICS_ADDR", "GUANJIN_OTEL_ENDPOINT",
	} {
		unsetEnv(t, k)
	}

	e, err := Load()
	require.NoError(t, err)

	require.Equal(t, EnvProd, e.Env)
	require.Equal(t, "info", e.LogLevel)
	require.Equal(t, "json", e.LogFormat)
	require.Equal(t, ":8080", e.HTTPAddr)
	require.Equal(t, ":8081", e.GatewayAddr)
	require.Equal(t, 50, e.DBMaxOpenConns)
	require.Equal(t, 10, e.DBMaxIdleConns)
	require.Equal(t, 30*time.Minute, e.DBConnMaxLifetime)
	require.Equal(t, 2*time.Second, e.RedisTimeout)
	require.Equal(t, 5*time.Second, e.GatewayUpstreamTimeout)
	require.Equal(t, 2*time.Second, e.GatewayDialTimeout)
	require.Equal(t, "127.0.0.1:9090", e.MetricsAddr)
	require.False(t, e.IsDev())
}

func TestLoadIssuerFallsBackToPublicBaseURL(t *testing.T) {
	setRequiredValid(t)
	unsetEnv(t, "GUANJIN_ISSUER")

	e, err := Load()
	require.NoError(t, err)
	require.Equal(t, "https://id.example.com", e.Issuer)
}

func TestLoadIssuerExplicitWins(t *testing.T) {
	setRequiredValid(t)
	t.Setenv("GUANJIN_ISSUER", "https://issuer.example.com")

	e, err := Load()
	require.NoError(t, err)
	require.Equal(t, "https://issuer.example.com", e.Issuer)
}

func TestLoadDevEnv(t *testing.T) {
	setRequiredValid(t)
	t.Setenv("GUANJIN_ENV", "dev")

	e, err := Load()
	require.NoError(t, err)
	require.True(t, e.IsDev())
}

func TestMasterKeyBytesRoundTrip(t *testing.T) {
	setRequiredValid(t)

	e, err := Load()
	require.NoError(t, err)

	raw, err := e.MasterKeyBytes()
	require.NoError(t, err)
	require.Len(t, raw, 32)
}
