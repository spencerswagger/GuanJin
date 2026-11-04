// Command guanjind 是控制面 + 认证服务 + 管理面 + Agent 面的进程入口。
//
// 边界（m1-slices.md S1 / 技术栈 §3）：
//   - 本二进制只做控制面与协议面；数据面（转发 + 身份注入）在 cmd/guanjingw；
//   - **禁止自动迁移**：schema 由 `golang-migrate` 手动执行（decisions.md P1）；
//   - fail-closed：配置缺失即非 0 退出，不存在"缺了就降级放行"的分支。
//
// 装配期的三道 fail-fast（顺序固定，任何一道失败都非 0 退出）：
//  1. 配置（config.Load）；
//  2. 数据库连通（store.Ping）；
//  3. **D13**：`login_policies` 的三档 `tier_default` 各恰一行（store.VerifyTierDefaults）——
//     缺策略时**在这里**就退出，不允许拖到第一个请求上变成 500。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
)

// app 是装配完成后的进程句柄：HTTP 服务 + 已校验过的依赖连接。
//
// 把 db 挂在返回值上而不是包级变量：关闭路径能显式关掉连接池，
// 也不会让"某个 handler 顺手拿全局 db"变成隐式依赖。
type app struct {
	srv *http.Server
	db  *gorm.DB
}

// startup 完成装配期的全部 fail-fast，成功后返回可用的进程句柄。
//
// 拆成独立函数（而不是塞在 main 里）：它能在没有真库的情况下被单测直接调用，
// "配置缺了会失败""库不可达会失败"这两条就不必靠人肉跑二进制来证明。
func startup(ctx context.Context, getenv func(string) string, logger *slog.Logger) (*app, error) {
	cfg, err := config.Load(getenv)
	if err != nil {
		// fail-fast：绝不带着半截配置启动
		return nil, err
	}

	db, err := store.Open(ctx, store.Options{DSN: cfg.DatabaseDSN})
	if err != nil {
		return nil, err
	}

	// 连接是否可用**显式**探测（store.Open 刻意不自动 ping），失败即拒绝启动。
	if err := store.Ping(ctx, db); err != nil {
		_ = store.Close(db)
		return nil, fmt.Errorf("startup: database not usable: %w", err)
	}

	// D13：三档 tier_default 必须各恰一行；缺失/多行 → 拒绝启动
	// （fail-closed，绝不"先用默认值顶着"——那就是第二套口径）。
	if err := store.VerifyTierDefaults(ctx, db); err != nil {
		_ = store.Close(db)
		return nil, err
	}

	// D5 / D14：运行期参数**只能**来自 `runtime_config`（缺失/不可解释 → 拒绝启动）。
	// 代码里不许出现 600 / 300 / 5：一旦有第二套来源，`expires_at` 与 `attempts_left`
	// 就说不清是按谁算的（AC-REG-01 要求"改键值必须跟着变"）。
	startupCfg, err := store.ValidateStartup(ctx, store.NewRuntimeConfigRepo(db))
	if err != nil {
		_ = store.Close(db)
		return nil, fmt.Errorf("startup: runtime_config: %w", err)
	}

	mux := http.NewServeMux()
	w, err := newWiring(ctx, db, cfg, startupCfg, logger)
	if err != nil {
		_ = store.Close(db)
		return nil, err
	}
	// 因子与挑战面（S5）：`/auth/challenge/{factor}`、`/auth/verify/{factor}`、`/authorize/flow`。
	w.flowHandler.Routes(mux)
	// 注册面（S4）：`/auth/register/start|complete`。
	w.registerHandler.Routes(mux)

	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// /readyz 反映依赖状态：装配期已 ping 过 DB 并校验过策略 seed，
	// 所以这两项是 ok；Redis 尚未接入 → 如实留在 pending 里（不谎报）。
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready","checks":{"config":"ok","db":"ok","login_policies":"ok"},"pending":["redis"]}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, httpx.TraceIDFromContext(r.Context()), httpx.CodeInvalidRequest,
			"endpoint not implemented yet", nil)
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpx.TraceMiddleware(mux, cfg.TraceHeader, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("startup checks passed",
		"addr", cfg.HTTPAddr,
		"tier_defaults", store.TierDefaultTiers,
		"flow_ttl_seconds", int64(startupCfg.FlowTTL.Seconds()),
		"challenge_ttl_seconds", int64(startupCfg.ChallengeTTL.Seconds()),
		"challenge_max_attempts", startupCfg.ChallengeMaxAttempts,
	)
	return &app{srv: srv, db: db}, nil
}

// close 释放装配期建立的资源（目前只有数据库连接池）。
func (a *app) close() error {
	if a == nil || a.db == nil {
		return nil
	}
	return store.Close(a.db)
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := startup(ctx, os.Getenv, logger)
	if err != nil {
		// 非 0 退出：配置/依赖/策略 seed 缺失都属"不能提供服务"，不是 500。
		logger.Error("startup failed", "error", err)
		os.Exit(2)
	}
	defer func() { _ = a.close() }()

	go func() {
		logger.Info("guanjind listening", "addr", a.srv.Addr)
		if err := a.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = a.srv.Shutdown(shutdownCtx)
	logger.Info("guanjind stopped")
}
