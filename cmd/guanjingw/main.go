// Command guanjingw 是数据面进程入口（薄数据面：只做 7 件事）。
//
// 口径（decisions.md P2② / D10 / m1-slices.md S8）：
//   - 只做：验签、会话校验与实时撤销、身份注入 X-GuanJin-Assertion、按 client 档位判 ACR、
//     认证风控、认证审计事件、单点登出通知；
//   - **不做** TLS 终结 / WAF / 负载均衡 / 灰度 / 熔断（前置 nginx / Kong 承担，不属本平台范围）；
//     本文件不 import `crypto/tls`、不设"跳过校验"的暗门（`internal/gateway` 有静态判据守着）；
//   - 剥离的对象是**客户端伪造的同名头**（先弃后签）；SDK 侧的"验签后剥离"是另一件事（D10）。
//
// 当前进度（S8·1）：转发内核 + 路由 + 先弃同名头 + 上游失败语义已装配；
// 认证/撤销/断言签发（S8·2）与单点登出通知（S8·3）在此文件按同一形态接入。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/gateway"
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

	// route → client 静态映射（索引 §4.2-18）：配置写坏就在启动期退出，不带着坏表跑。
	// 空表是**合法**的（尚未接入任何业务系统）：此时所有请求一律 503 UPSTREAM_UNAVAILABLE，
	// 宁可拒服也不裸转发。
	routes, err := gateway.ParseRoutes(os.Getenv("GUANJIN_GATEWAY_ROUTES"))
	if err != nil {
		logger.Error("startup failed: bad GUANJIN_GATEWAY_ROUTES", "error", err)
		os.Exit(2)
	}
	router, err := gateway.NewRouter(routes)
	if err != nil {
		logger.Error("startup failed: invalid route table", "error", err)
		os.Exit(2)
	}

	upstreamTimeout, err := durationEnv("GUANJIN_GATEWAY_UPSTREAM_TIMEOUT", gateway.DefaultUpstreamTimeout)
	if err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(2)
	}
	dialTimeout, err := durationEnv("GUANJIN_GATEWAY_DIAL_TIMEOUT", gateway.DefaultDialTimeout)
	if err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(2)
	}

	dataPlane, err := gateway.New(router, gateway.Options{
		TraceHeader:     cfg.TraceHeader,
		UpstreamTimeout: upstreamTimeout,
		DialTimeout:     dialTimeout,
		Logger:          logger,
	})
	if err != nil {
		logger.Error("startup failed: data plane", "error", err)
		os.Exit(2)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// 数据面通配：所有业务路径都由网关按路由表转发（找不到路由即 503）。
	mux.Handle("/", dataPlane)

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: httpx.TraceMiddleware(mux, cfg.TraceHeader, logger),
		// 只读超时防慢头攻击；读写体量交给上游与前置（本进程不缓冲请求体）。
		ReadHeaderTimeout: 5 * time.Second,
	}

	for _, rt := range router.Routes() {
		logger.Info("route configured", "prefix", rt.Prefix, "client_id", rt.ClientID, "upstream", rt.Upstream.String())
	}

	go func() {
		logger.Info("guanjingw listening", "addr", cfg.HTTPAddr, "routes", len(router.Routes()),
			"upstream_timeout", upstreamTimeout.String(), "dial_timeout", dialTimeout.String())
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

// durationEnv 读一个时间间隔型环境变量；未设置用**登记过的默认值**（技术栈 §7.2），
// 设了但解析不了 → 报错（fail-closed，不静默退回默认值）。
func durationEnv(key string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s 不是合法时长（如 5s / 2s）: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s 必须 > 0，got %s", key, raw)
	}
	return d, nil
}
