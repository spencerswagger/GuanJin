// Command guanjind 是控制面 + 认证服务 + 管理面 + Agent 面的进程入口。
//
// 边界（m1-slices.md S1 / 技术栈 §3）：
//   - 本二进制只做控制面与协议面；数据面（转发 + 身份注入）在 cmd/guanjingw；
//   - **禁止自动迁移**：schema 由 `golang-migrate` 手动执行（decisions.md P1）；
//   - fail-closed：配置缺失即非 0 退出，不存在"缺了就降级放行"的分支。
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/httpx"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		// fail-fast：绝不带着半截配置启动
		logger.Error("startup failed", "error", err)
		os.Exit(2)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// /readyz 反映依赖状态；依赖未接入前**只有配置就绪**才算就绪（不谎报 DB/Redis 可用）
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready","checks":{"config":"ok"},"pending":["db","redis"]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, traceID(r, cfg.TraceHeader), httpx.CodeInvalidRequest, "endpoint not implemented yet", nil)
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           withTrace(mux, cfg.TraceHeader, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("guanjind listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	logger.Info("guanjind stopped")
}

// withTrace 贯穿日志与响应头的 trace 标识（D4-C：trace_id 用于取证串联）。
func withTrace(next http.Handler, header string, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(header)
		if id == "" {
			id = newTraceID()
		}
		w.Header().Set(header, id)
		next.ServeHTTP(w, r)
		logger.Info("request", "trace_id", id, "method", r.Method, "path", r.URL.Path)
	})
}

func traceID(r *http.Request, header string) string {
	if id := r.Header.Get(header); id != "" {
		return id
	}
	return newTraceID()
}

func newTraceID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "trace-unavailable"
	}
	return hex.EncodeToString(b[:])
}
