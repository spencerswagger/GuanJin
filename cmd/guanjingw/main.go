package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/config"
	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/factor/holding"
	"github.com/spencerswagger/GuanJin/internal/gateway"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/observability"
	"github.com/spencerswagger/GuanJin/internal/store"
)

const serviceName = "guanjingw"

func main() {
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, serviceName+":", err)
		os.Exit(1)
	}
}

func serve() error {
	env, err := config.Load()
	if err != nil {
		return err
	}

	logger, err := observability.NewLogger(env.Env, env.LogLevel, env.LogFormat, serviceName)
	if err != nil {
		return err
	}
	defer func() { _ = logger.Sync() }()

	masterKey, err := env.MasterKeyBytes()
	if err != nil {
		return err
	}

	db, err := store.Open(store.Options{
		DSN:             env.DatabaseURL,
		MaxOpenConns:    env.DBMaxOpenConns,
		MaxIdleConns:    env.DBMaxIdleConns,
		ConnMaxLifetime: env.DBConnMaxLifetime,
	})
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		defer func() { _ = sqlDB.Close() }()
	}

	rdb, err := store.NewRedis(env.RedisURL, env.RedisTimeout)
	if err != nil {
		return fmt.Errorf("init redis: %w", err)
	}
	defer func() { _ = rdb.Close() }()

	metrics := observability.NewMetrics()

	probes := &httpx.Probes{Checks: map[string]httpx.CheckFunc{
		observability.DependencyDB: func(ctx context.Context) error {
			return store.Ping(ctx, db)
		},
		observability.DependencyRedis: func(ctx context.Context) error {
			return rdb.Ping(ctx)
		},
		observability.DependencyKeys: func(context.Context) error {
			if !crypto.KeysReady(masterKey) {
				return errors.New("master key is not usable")
			}
			return nil
		},
	}}

	keys := store.NewSigningKeyRepo(db, masterKey)
	if _, err := keys.EnsureKeypair(context.Background(), crypto.PurposeGateway); err != nil {
		return fmt.Errorf("ensure gateway signing keypair: %w", err)
	}

	router, err := buildRouter(context.Background(), db)
	if err != nil {
		return fmt.Errorf("load gateway routes: %w", err)
	}

	gw := gateway.New(gateway.Deps{
		Logger:          logger,
		Redis:           rdb,
		Keys:            keys,
		Clients:         store.NewClientRepo(db, masterKey),
		Sessions:        store.NewSessionRepo(db),
		Policies:        store.NewLoginPolicyRepo(db),
		Roles:           store.NewRoleBindingRepo(db),
		Users:           store.NewUserRepo(db),
		Visited:         store.NewUserVisitedClientRepo(db),
		AuthEvents:      store.NewAuthEventRepo(db),
		Router:          router,
		Notifier:        buildNotifier(keys),
		Registry:        buildRegistry(db),
		Issuer:          env.Issuer,
		AssertionTTL:    gateway.AssertionTTL,
		UpstreamTimeout: env.GatewayUpstreamTimeout,
		DialTimeout:     env.GatewayDialTimeout,
		RequireRedis:    true,
	})

	mux := http.NewServeMux()
	mux.Handle("/healthz", probes.HealthzHandler())
	mux.Handle("/readyz", probes.ReadyzHandler(metrics))
	mux.Handle("/", gw)

	handler := httpx.NetHTTPTrace(
		httpx.NetHTTPRecovery(logger,
			httpx.NetHTTPAccessLog(logger, mux),
		),
	)

	httpServer := &http.Server{
		Addr:              env.GatewayAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       env.GatewayUpstreamTimeout,
		WriteTimeout:      env.GatewayUpstreamTimeout,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("gateway server listening", zap.String("addr", env.GatewayAddr))
		if serveErr := httpServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case <-signalCtx.Done():
		logger.Info("shutdown signal received")
	case serveErr := <-errCh:
		return serveErr
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	logger.Info("server stopped")
	return nil
}

func buildRouter(ctx context.Context, db *gorm.DB) (gateway.Router, error) {
	if raw := os.Getenv("GUANJIN_GATEWAY_ROUTES"); raw != "" {
		routes, err := gateway.ParseRoutes([]byte(raw))
		if err != nil {
			return nil, err
		}
		return gateway.NewTableRouter(routes), nil
	}
	return gateway.RuntimeRoutes(ctx, db)
}

func buildNotifier(keys *store.SigningKeyRepo) gateway.LogoutNotifier {
	base := os.Getenv("GUANJIN_GATEWAY_SLO_BASE_URL")
	if base == "" {
		return nil
	}
	return gateway.NewSM2HTTPNotifier(keys, base, nil)
}

func buildRegistry(db *gorm.DB) *factor.Registry {
	registry := factor.NewRegistry()
	provider := holding.NewPasswordProvider(
		store.NewAuthenticatorRepo(db),
		store.NewChallengeRepo(db),
		holding.DefaultFailThreshold,
		holding.DefaultLockDuration,
	)
	_ = registry.Register(provider)
	return registry
}
