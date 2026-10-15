package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func okCheck(context.Context) error  { return nil }
func badCheck(context.Context) error { return errors.New("down") }

func TestHealthzAlwaysOK(t *testing.T) {
	probes := &Probes{Checks: map[string]CheckFunc{"db": badCheck, "redis": badCheck, "keys": badCheck}}

	rec := httptest.NewRecorder()
	probes.HealthzHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestReadyzAllUp(t *testing.T) {
	probes := &Probes{Checks: map[string]CheckFunc{"db": okCheck, "redis": okCheck, "keys": okCheck}}

	rec := httptest.NewRecorder()
	probes.ReadyzHandler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Checks map[string]bool `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, map[string]bool{"db": true, "redis": true, "keys": true}, body.Checks)
}

func TestReadyzFailClosedWhenAnyDependencyDown(t *testing.T) {
	probes := &Probes{Checks: map[string]CheckFunc{"db": okCheck, "redis": badCheck, "keys": okCheck}}

	rec := httptest.NewRecorder()
	probes.ReadyzHandler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body struct {
		Checks map[string]bool `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.False(t, body.Checks["redis"])
	require.True(t, body.Checks["db"])
}
