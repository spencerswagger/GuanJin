package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// trace 基础设施（decisions.md D4-C / AC-AUD-01）。
//
// 口径：
//   - `trace_id` 贯穿**日志、`auth_events.trace_id`、`audit_logs.trace_id`、错误响应**；
//   - `flows.trace_id` 只写"创建该流的那一次请求"，后续补步**不覆盖**（D4-C(4)）——
//     因此同一登录里 `flows.trace_id` 与后续 `auth_events.trace_id` 不同是**正常**的；
//     要串一次登录请用 `flows.id`（`flow_id`）作关联键；
//   - 入站 `trace_id` 只**有限信任**：格式不符就重新生成（防日志注入与超长值）。

// traceIDKey 是 context 里的私有键（不导出，避免被别的包覆盖）。
type traceIDKey struct{}

// MaxTraceIDLen 是允许的最大长度（入站值超长一律弃用并重新生成）。
const MaxTraceIDLen = 128

// NewTraceID 生成 UUID v7 形式的 trace_id（时间有序，方便按时间检索日志）。
func NewTraceID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// SanitizeTraceID 校验入站 trace_id：只接受 1..MaxTraceIDLen 个
// `[A-Za-z0-9._-]` 字符；不合法返回空串（调用方应重新生成）。
//
// 为什么要白名单：trace_id 会被原样写进日志行与响应头，未过滤的换行/控制字符
// 可以伪造日志行（日志注入）；超长值可以直接打满日志。
func SanitizeTraceID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxTraceIDLen {
		return ""
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
		default:
			return ""
		}
	}
	return raw
}

// WithTraceID 把 trace_id 放进 context。
func WithTraceID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceIDKey{}, id)
}

// TraceIDFromContext 取 context 里的 trace_id；没有则返回空串。
//
// **不自动生成**：静默生成会让"本该带 trace 的路径漏传"变得看不出来。
// 需要兜底的调用方显式用 NewTraceID。
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(traceIDKey{}).(string); ok {
		return id
	}
	return ""
}

// TraceMiddleware 建立 trace 上下文：
//  1. 取入站头（经 SanitizeTraceID 过滤），不合法则生成新 UUID v7；
//  2. 写回响应头，放进 request context；
//  3. 请求结束后打一行结构化访问日志（含 trace_id、方法、路径、状态码）。
//
// 生成失败（随机源不可用）时**不静默**：用空 trace_id 并把该异常记进日志——宁可
// 少一个字段，也不伪造一个假 trace。
func TraceMiddleware(next http.Handler, header string, logger *slog.Logger) http.Handler {
	if header == "" {
		header = "X-Trace-Id"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := SanitizeTraceID(r.Header.Get(header))
		if id == "" {
			generated, err := NewTraceID()
			if err != nil && logger != nil {
				logger.Error("trace id generation failed", "error", err)
			}
			id = generated
		}
		if id != "" {
			w.Header().Set(header, id)
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(WithTraceID(r.Context(), id)))
		if logger != nil {
			logger.Info("request",
				"trace_id", id,
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
			)
		}
	})
}

// statusRecorder 记录实际写出的状态码（默认 200，与 net/http 行为一致）。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wroteHeader {
		return // 与 net/http 一致：重复 WriteHeader 只记一次，且不重复下发
	}
	s.status = code
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}
