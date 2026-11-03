// Command guanjingw 是数据面进程入口（薄数据面：只做 7 件事）。
//
// 口径（decisions.md P2② / D10 / m1-slices.md S8）：
//   - 只做：验签、会话校验与实时撤销、身份注入 X-GuanJin-Assertion、按 client 档位判 ACR、
//     认证风控、认证审计事件、单点登出通知；
//   - **不做** TLS 终结 / WAF / 负载均衡 / 灰度 / 熔断（前置 nginx / Kong 承担，不属本平台范围）；
//   - 剥离的对象是**客户端伪造的同名头**（先弃后签）；SDK 侧的"验签后剥离"是另一件事（D10）。
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/httpx"
)

// AssertionHeader 是内部令牌头名（冻结：D10 / 数据面设计 §4.6）。
const AssertionHeader = "X-GuanJin-Assertion"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(2)
	}

	// V1：route → client 按域名/路径前缀静态映射（索引 §4.2-18），映射关系留抽象。
	// 骨架阶段无路由表 → 一律 503 UPSTREAM_UNAVAILABLE（宁可拒服，也不裸转发）。
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 先弃客户端伪造的同名头：**绝不透传**（AC-DP-02）
		r.Header.Del(AssertionHeader)
		httpx.WriteError(w, r.Header.Get(cfg.TraceHeader), httpx.CodeUpstreamUnavailable,
			"no route configured for this request", nil)
	})

	upstream := os.Getenv("GUANJIN_UPSTREAM")
	if upstream != "" {
		if u, perr := url.Parse(upstream); perr == nil {
			proxy := httputil.NewSingleHostReverseProxy(u)
			proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
				logger.Error("upstream failed", "error", err, "path", r.URL.Path)
				// D8-C′④：上游失败 → 503 UPSTREAM_UNAVAILABLE（不是 500、不是 DEPENDENCY_UNAVAILABLE）
				httpx.WriteError(w, r.Header.Get(cfg.TraceHeader), httpx.CodeUpstreamUnavailable,
					"upstream unavailable", nil)
			}
			handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Header.Del(AssertionHeader) // 先弃后签：签发生在后续 wave
				proxy.ServeHTTP(w, r)
			})
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", handler)

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		logger.Info("guanjingw listening", "addr", cfg.HTTPAddr, "upstream", upstream)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	logger.Info("guanjingw stopped")
}
