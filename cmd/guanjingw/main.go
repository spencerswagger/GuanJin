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

	"github.com/yourorg/guanjin-next/internal/config"
	"github.com/yourorg/guanjin-next/internal/crypto"
	"github.com/yourorg/guanjin-next/internal/httpx"
	"github.com/yourorg/guanjin-next/internal/observability"
	"github.com/yourorg/guanjin-next/internal/store"
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

	mux := http.NewServeMux()
	mux.Handle("/healthz", probes.HealthzHandler())
	mux.Handle("/readyz", probes.ReadyzHandler(metrics))

	handler := httpx.NetHTTPTrace(
		httpx.NetHTTPRecovery(logger,
			httpx.NetHTTPAccessLog(logger, gatewayPlaceholder(mux)),
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

func gatewayPlaceholder(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		httpx.WriteError(w, r, httpx.New("NOT_FOUND", "gateway proxy not implemented yet (phase 6)"))
	})
}
