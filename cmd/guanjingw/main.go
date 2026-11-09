// Command guanjingw 是数据面进程入口（薄数据面：只做 7 件事）。
//
// 口径（decisions.md P2② / D10 / m1-slices.md S8）：
//   - 只做：验签、会话校验与实时撤销、身份注入 X-GuanJin-Assertion、按 client 档位判 ACR、
//     认证风控、认证审计事件、单点登出通知；
//   - **不做** TLS 终结 / WAF / 负载均衡 / 灰度 / 熔断（前置 nginx / Kong 承担，不属本平台范围）；
//     本文件不 import `crypto/tls`、不设"跳过校验"的暗门（`internal/gateway` 有静态判据守着）；
//   - 剥离的对象是**客户端伪造的同名头**（先弃后签）；SDK 侧的"验签后剥离"是另一件事（D10）。
//
// 当前进度（S8·2 完成）：剥离 → 路由 → 准入（验签 + 会话 + 实时撤销 + 档位）→
// 逐请求签发 `X-GuanJin-Assertion` → 转发，全部经 `gateway.Pipeline` 串起来；
// 单点登出通知的**失败隔离**已在 `gateway.SloBroadcaster`（wire 契约 R12 待裁，见 D16④）。
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

	"github.com/redis/go-redis/v9"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/gateway"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 注意：`config.Load` 是三个进程共享的（要求 `GUANJIN_DB_DSN` / `GUANJIN_REDIS_ADDR` /
	// `GUANJIN_MASTER_KEY` 全给）。数据面用 Redis 存**断言判重**（AC-DP-08：同 `jti` 二次 → 401），
	// 故地址是本进程的必需项，不是沿用别人的配置。
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

	skew, err := durationEnv("GUANJIN_GATEWAY_CLOCK_SKEW", 0)
	if err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(2)
	}

	// 数据库：准入判定（会话/角色/门槛）与访问记录都直读直写库——
	// 启动期必须 ping 通，不带着"连不上库的数据面"对外服务（那是每条请求 503）。
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	db, err := store.Open(ctx, store.Options{DSN: cfg.DatabaseDSN})
	if err != nil {
		logger.Error("startup failed: open postgres", "error", err)
		os.Exit(2)
	}
	defer func() { _ = store.Close(db) }()
	if err := store.Ping(ctx, db); err != nil {
		logger.Error("startup failed: postgres unreachable", "error", err)
		os.Exit(2)
	}

	// 判重存储（AC-DP-08）用 Redis：**启动期不 ping**（Redis 挂了进程要活着、业务全拒，
	// 与 D5-₂ 同一口径）；连接在进程退出时统一关闭。
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	defer func() { _ = rdb.Close() }()

	dataPlane, err := newDataPlane(ctx, db, rdb, cfg, router, gateway.Options{
		UpstreamTimeout: upstreamTimeout,
		DialTimeout:     dialTimeout,
	}, skew, logger)
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
