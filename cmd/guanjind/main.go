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

	"github.com/redis/go-redis/v9"

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
	// stopAuditJob 停止进程内的审计链整链复核任务（S10；close 时调用）。
	stopAuditJob func()
	// stopPartitionJob 停止审计分区滚动任务（S10）。
	stopPartitionJob func()
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

	// 协议面（S6）：密钥解封与用途校验都在这里 fail-fast（坏密钥不允许对外服务）。
	protoSrv, err := newProtocolHandler(ctx, db, cfg, startupCfg, logger)
	if err != nil {
		_ = store.Close(db)
		return nil, err
	}
	// S7 的装配（会话自助面 + 管理面 sessions 段）在自己的文件里：`wiring.go` 归 S6。
	sessRoutes, adminSessRoutes, callerResolver, err := newSessionWiring(ctx, db, cfg, logger)
	if err != nil {
		_ = store.Close(db)
		return nil, err
	}

	mux := http.NewServeMux()
	w, err := newWiring(ctx, db, cfg, startupCfg, callerResolver, logger)
	if err != nil {
		_ = store.Close(db)
		return nil, err
	}
	// 因子与挑战面（S5）：`/auth/challenge/{factor}`、`/auth/verify/{factor}`、`/authorize/flow`。
	w.flowHandler.Routes(mux)
	// 注册面（S4）：`/auth/register/start|complete`。
	w.registerHandler.Routes(mux)
	// 自助面（AC-FLOW-09）：`POST /api/v1/me/identifiers`，ACR 前置按访问过的最高档位推。
	w.meIdentifiers.Routes(mux)
	// 协议面（S6）：discovery / jwks / token / revoke / introspect / userinfo。
	protoSrv.Routes(mux)
	// 会话自助面 + 管理面 sessions 段（S7）：`/api/v1/me/sessions*`、`/api/v1/sessions*`。
	sessRoutes.Routes(mux)
	adminSessRoutes.Routes(mux)
	// 因子管理面（AC-FLOW-05）：`/api/v1/factors` 读 + `/{type}` 详情 + PATCH 平台级启停（写审计）。
	factorRoutes, err := newFactorsAdminWiring(db, cfg, w.registry, callerResolver, logger)
	if err != nil {
		_ = store.Close(db)
		return nil, err
	}
	factorRoutes.Routes(mux)

	mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// /readyz 反映**当前**依赖状态（每次现打，不吃启动期的旧结论）：DB 不通 → 503 `DB_UNAVAILABLE`、
	// Redis 不通 → 503 `REDIS_UNAVAILABLE`（D8-C / AC-FC-01/02）。Redis 客户端与数据面/风控共用同一地址。
	mux.HandleFunc("/readyz", readyzHandler(readyzDeps{
		db:    db,
		redis: redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword}),
	}))
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
	// S10：审计链整链复核 Job（**本进程内**，非新二进制；指标由 internal/audit 暴露）。
	stopAuditJob, err := startAuditVerifyJob(ctx, db, cfg, logger)
	if err != nil {
		_ = store.Close(db)
		return nil, err
	}
	// S10：审计**分区滚动**任务（当月 + 未来 5 个月，幂等；启动即跑一次）。
	// 这条补的是 Gate 指出的生产缺口：migrations 只预建 -1/0/+1 三个月，若没有滚动任务，
	// 第四个月起审计写入会因"无分区"直接失败（23514）。
	stopPartitionJob, err := startAuditPartitionJob(ctx, db, logger)
	if err != nil {
		stopAuditJob()
		_ = store.Close(db)
		return nil, err
	}

	app := &app{srv: srv, db: db, stopAuditJob: stopAuditJob, stopPartitionJob: stopPartitionJob}
	return app, nil
}

// close 释放装配期建立的资源（数据库连接池 + 审计校验 Job）。
func (a *app) close() error {
	if a == nil {
		return nil
	}
	if a.stopPartitionJob != nil {
		a.stopPartitionJob()
	}
	if a.stopAuditJob != nil {
		a.stopAuditJob()
	}
	if a.db == nil {
		return nil
	}
	return store.Close(a.db)
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// `guanjind keygen`：离线生成签名密钥（幂等）。生成只走 CLI，启动期只解封。
	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runKeygen(ctx, os.Getenv, os.Stdout); err != nil {
			logger.Error("keygen failed", "error", err)
			os.Exit(2)
		}
		return
	}

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
