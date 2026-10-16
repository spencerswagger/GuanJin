package httpx

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/spencerswagger/GuanJin/internal/observability"
)

func Trace() gin.HandlerFunc {
	return func(c *gin.Context) {
		traceID := observability.ResolveTraceID(c.GetHeader("traceparent"))
		c.Request = c.Request.WithContext(observability.WithTraceID(c.Request.Context(), traceID))
		c.Header("X-Trace-Id", traceID)
		c.Next()
	}
}

func NetHTTPTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := observability.ResolveTraceID(r.Header.Get("traceparent"))
		w.Header().Set("X-Trace-Id", traceID)
		next.ServeHTTP(w, r.WithContext(observability.WithTraceID(r.Context(), traceID)))
	})
}

func AccessLog(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Info("access",
			observability.TraceIDField(c.Request.Context()),
			zap.String(observability.FieldMethod, c.Request.Method),
			zap.String(observability.FieldPath, c.Request.URL.Path),
			zap.Int(observability.FieldStatus, c.Writer.Status()),
			zap.Int64(observability.FieldLatencyMS, time.Since(start).Milliseconds()),
		)
	}
}

func NetHTTPAccessLog(logger *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		logger.Info("access",
			observability.TraceIDField(r.Context()),
			zap.String(observability.FieldMethod, r.Method),
			zap.String(observability.FieldPath, r.URL.Path),
			zap.Int(observability.FieldStatus, sw.status),
			zap.Int64(observability.FieldLatencyMS, time.Since(start).Milliseconds()),
		)
	})
}

func Recovery(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				logger.Error("panic recovered",
					observability.TraceIDField(c.Request.Context()),
					zap.String(observability.FieldErrCode, "INTERNAL_ERROR"),
				)
				Error(c, &AppError{
					Code:    "INTERNAL_ERROR",
					HTTP:    http.StatusInternalServerError,
					Message: MessageFor("INTERNAL_ERROR"),
				})
			}
		}()
		c.Next()
	}
}

func NetHTTPRecovery(logger *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func(ctx context.Context) {
			if recover() != nil {
				logger.Error("panic recovered",
					observability.TraceIDField(ctx),
					zap.String(observability.FieldErrCode, "INTERNAL_ERROR"),
				)
				WriteError(w, r, &AppError{
					Code:    "INTERNAL_ERROR",
					HTTP:    http.StatusInternalServerError,
					Message: MessageFor("INTERNAL_ERROR"),
				})
			}
		}(r.Context())
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
