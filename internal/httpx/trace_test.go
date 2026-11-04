package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewTraceIDIsUUIDv7(t *testing.T) {
	id, err := NewTraceID()
	if err != nil {
		t.Fatalf("NewTraceID: %v", err)
	}
	if len(id) != 36 {
		t.Fatalf("trace id = %q, want 36-char uuid", id)
	}
	if id[14] != '7' {
		t.Fatalf("trace id %q must be UUID v7 (version nibble at 14)", id)
	}
	other, _ := NewTraceID()
	if id == other {
		t.Fatal("two trace ids must differ")
	}
}

// 入站 trace_id 只做有限信任：白名单字符 + 长度上限，其余一律弃用。
func TestSanitizeTraceID(t *testing.T) {
	good := []string{"abc", "a-b_c.d", "0123456789", strings.Repeat("a", MaxTraceIDLen)}
	for _, s := range good {
		if got := SanitizeTraceID(s); got != s {
			t.Errorf("SanitizeTraceID(%q) = %q, want unchanged", s, got)
		}
	}
	bad := []string{
		"",
		"   ",
		strings.Repeat("a", MaxTraceIDLen+1),
		"line\nbreak",     // 日志注入
		"tab\tchar",       // 控制字符
		"<script>",        // 响应头/日志里的可疑输出
		"a b",             // 空格
		"trace\r\nHeader", // 响应头注入
		"中文",              // 非 ASCII
	}
	for _, s := range bad {
		if got := SanitizeTraceID(s); got != "" {
			t.Errorf("SanitizeTraceID(%q) = %q, want empty (rejected)", s, got)
		}
	}
}

func TestTraceIDContextRoundTrip(t *testing.T) {
	if got := TraceIDFromContext(context.Background()); got != "" {
		t.Fatalf("empty context must return empty trace id, got %q", got)
	}
	if got := TraceIDFromContext(nil); got != "" { //nolint:staticcheck // 明确验证 nil 安全性
		t.Fatalf("nil context must return empty trace id, got %q", got)
	}
	ctx := WithTraceID(context.Background(), "t-1")
	if got := TraceIDFromContext(ctx); got != "t-1" {
		t.Fatalf("trace id = %q, want t-1", got)
	}
	// nil ctx 也要能挂值（不得 panic）。
	if got := TraceIDFromContext(WithTraceID(nil, "t-2")); got != "t-2" { //nolint:staticcheck
		t.Fatalf("trace id = %q, want t-2", got)
	}
}

func TestTraceMiddleware(t *testing.T) {
	const header = "X-Trace-Id"
	var seen string
	var seenStatus int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = TraceIDFromContext(r.Context())
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("x"))
	})

	t.Run("inbound id is reused", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/anything", nil)
		r.Header.Set(header, "client-provided")
		TraceMiddleware(next, header, nil).ServeHTTP(w, r)

		if seen != "client-provided" {
			t.Fatalf("context trace id = %q, want client-provided", seen)
		}
		if got := w.Header().Get(header); got != "client-provided" {
			t.Fatalf("response header = %q", got)
		}
	})

	t.Run("invalid inbound id is replaced", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/anything", nil)
		r.Header.Set(header, "bad\nvalue")
		TraceMiddleware(next, header, nil).ServeHTTP(w, r)

		if seen == "" || seen == "bad\nvalue" {
			t.Fatalf("invalid inbound trace id must be replaced, got %q", seen)
		}
		if got := w.Header().Get(header); got != seen {
			t.Fatalf("response header %q != context %q", got, seen)
		}
	})

	t.Run("default header name", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/anything", nil)
		TraceMiddleware(next, "", nil).ServeHTTP(w, r)
		if w.Header().Get("X-Trace-Id") == "" {
			t.Fatal("empty header name must fall back to X-Trace-Id")
		}
	})

	t.Run("logs status code", func(t *testing.T) {
		var buf strings.Builder
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/p", nil)
		TraceMiddleware(next, header, logger).ServeHTTP(w, r)
		if !strings.Contains(buf.String(), "status=418") {
			t.Fatalf("access log must carry the real status, got %s", buf.String())
		}
		seenStatus = w.Code
	})

	if seenStatus != http.StatusTeapot {
		t.Fatalf("handler status = %d, want 418", seenStatus)
	}
}

func TestStatusRecorder(t *testing.T) {
	// 只 Write 不 WriteHeader → 200（与 net/http 的隐式 200 一致）。
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	if _, err := rec.Write([]byte("body")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if rec.status != http.StatusOK {
		t.Fatalf("implicit write must record 200, got %d", rec.status)
	}

	// 先 WriteHeader 再 Write：状态保持，且重复 WriteHeader 不覆盖（net/http 的真实行为）。
	rec = &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	rec.WriteHeader(http.StatusNotFound)
	if _, err := rec.Write([]byte("body")); err != nil {
		t.Fatalf("write: %v", err)
	}
	rec.WriteHeader(http.StatusInternalServerError)
	if rec.status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (first WriteHeader wins)", rec.status)
	}

	// Write 之后再来 WriteHeader 也不得覆盖（此时 200 已发出）。
	rec = &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	if _, err := rec.Write([]byte("body")); err != nil {
		t.Fatalf("write: %v", err)
	}
	rec.WriteHeader(http.StatusInternalServerError)
	if rec.status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (write already committed)", rec.status)
	}
}
