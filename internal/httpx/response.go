package httpx

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/yourorg/guanjin-next/internal/observability"
)

const CodeOK = "OK"

type Envelope struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	TraceID string         `json:"trace_id"`
	Details map[string]any `json:"details,omitempty"`
	Data    any            `json:"data,omitempty"`
}

func toAppError(err error) *AppError {
	if err == nil {
		return &AppError{Code: "INTERNAL_ERROR", HTTP: http.StatusInternalServerError, Message: MessageFor("INTERNAL_ERROR")}
	}
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr
	}
	code := err.Error()
	if knownCode(code) {
		return &AppError{Code: code, HTTP: HTTPStatus(code), Message: MessageFor(code)}
	}
	return &AppError{Code: "INTERNAL_ERROR", HTTP: http.StatusInternalServerError, Message: MessageFor("INTERNAL_ERROR")}
}

func WriteJSON(w http.ResponseWriter, status int, body Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	appErr := toAppError(err)
	var traceID string
	if r != nil {
		traceID = observability.FromContext(r.Context())
	}
	WriteJSON(w, appErr.HTTP, Envelope{
		Code:    appErr.Code,
		Message: appErr.Message,
		TraceID: traceID,
		Details: appErr.Details,
	})
}

func Error(c *gin.Context, err error) {
	appErr := toAppError(err)
	traceID := observability.FromContext(c.Request.Context())
	c.Header("X-Trace-Id", traceID)
	c.AbortWithStatusJSON(appErr.HTTP, Envelope{
		Code:    appErr.Code,
		Message: appErr.Message,
		TraceID: traceID,
		Details: appErr.Details,
	})
}

func Success(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{
		Code:    CodeOK,
		Message: "ok",
		TraceID: observability.FromContext(c.Request.Context()),
		Data:    data,
	})
}
