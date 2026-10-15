package httpx

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func newBufferLogger(t *testing.T) (*zap.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(buf),
		zapcore.DebugLevel,
	)
	return zap.New(core), buf
}

func newTestRouter(logger *zap.Logger) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(Trace(), AccessLog(logger), Recovery(logger))
	engine.GET("/ok", func(c *gin.Context) { Success(c, gin.H{"hello": "world"}) })
	engine.GET("/boom", func(_ *gin.Context) { panic("boom") })
	engine.NoRoute(func(c *gin.Context) { Error(c, New("NOT_FOUND", "route not found")) })
	return engine
}

func TestTraceMiddlewareSetsXTraceID(t *testing.T) {
	logger, _ := newBufferLogger(t)
	engine := newTestRouter(logger)

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, rec.Header().Get("X-Trace-Id"))
}

func TestTraceMiddlewareUsesIncomingTraceparent(t *testing.T) {
	logger, _ := newBufferLogger(t)
	engine := newTestRouter(logger)

	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", rec.Header().Get("X-Trace-Id"))
}

func TestAccessLogNeverContainsRawToken(t *testing.T) {
	const secret = "SUPERSECRET-TOKEN-abcdef"

	logger, buf := newBufferLogger(t)
	engine := newTestRouter(logger)

	req := httptest.NewRequest(http.MethodGet, "/ok?access_token="+secret, strings.NewReader(`{"refresh_token":"`+secret+`"}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Cookie", "sid="+secret)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	out := buf.String()
	require.NotContains(t, out, secret)
	require.Contains(t, out, "access")

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	require.Equal(t, "GET", entry["method"])
	require.Equal(t, "/ok", entry["path"])
}

func TestRecoveryReturnsUnifiedEnvelope(t *testing.T) {
	logger, _ := newBufferLogger(t)
	engine := newTestRouter(logger)

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)

	var body Envelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "INTERNAL_ERROR", body.Code)
}

func TestUnknownRouteReturnsUnifiedEnvelope(t *testing.T) {
	logger, _ := newBufferLogger(t)
	engine := newTestRouter(logger)

	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)

	var body Envelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "NOT_FOUND", body.Code)
	require.NotEmpty(t, body.TraceID)
}
