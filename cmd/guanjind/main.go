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

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/config"
	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/factor/holding"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/identifier"
	"github.com/spencerswagger/GuanJin/internal/observability"
	"github.com/spencerswagger/GuanJin/internal/protocol"
	"github.com/spencerswagger/GuanJin/internal/session"
	"github.com/spencerswagger/GuanJin/internal/store"
)

const serviceName = "guanjind"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, serviceName+":", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "migrate" {
		return runMigrate(args[1:])
	}
	return serve()
}

func runMigrate(args []string) error {
	if len(args) == 0 || args[0] != "up" {
		return errors.New("usage: guanjind migrate up")
	}
	env, err := config.Load()
	if err != nil {
		return err
	}
	if err := store.MigrateUp(env.DatabaseURL); err != nil {
		return err
	}
	fmt.Println("migrations applied")
	return nil
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

	if err := bootstrapSigningKeys(context.Background(), db, masterKey); err != nil {
		return fmt.Errorf("bootstrap signing keys: %w", err)
	}

	protoServer := newProtocolServer(&env, logger, db, rdb, masterKey)

	httpServer := &http.Server{
		Addr:              env.HTTPAddr,
		Handler:           newEngine(&env, logger, probes, metrics, protoServer),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	metricsServer := &http.Server{
		Addr:              env.MetricsAddr,
		Handler:           metrics.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("http server listening", zap.String("addr", env.HTTPAddr), zap.String("issuer", env.Issuer))
		if serveErr := httpServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()
	go func() {
		logger.Info("metrics server listening", zap.String("addr", env.MetricsAddr))
		if serveErr := metricsServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
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
	_ = metricsServer.Shutdown(shutdownCtx)
	logger.Info("server stopped")
	return nil
}

func newEngine(env *config.Env, logger *zap.Logger, probes *httpx.Probes, metrics *observability.Metrics, protoServer *protocol.Server) *gin.Engine {
	if !env.IsDev() {
		gin.SetMode(gin.ReleaseMode)
	}
	engine := gin.New()
	engine.Use(httpx.Trace(), httpx.AccessLog(logger), httpx.Recovery(logger))
	engine.GET("/healthz", gin.WrapH(probes.HealthzHandler()))
	engine.GET("/readyz", gin.WrapH(probes.ReadyzHandler(metrics)))
	if protoServer != nil {
		protoServer.RegisterRoutes(engine)
	}
	engine.NoRoute(func(c *gin.Context) {
		httpx.Error(c, httpx.New("NOT_FOUND", "route not found"))
	})
	return engine
}

func bootstrapSigningKeys(ctx context.Context, db *gorm.DB, masterKey []byte) error {
	repo := store.NewSigningKeyRepo(db, masterKey)
	for _, purpose := range []string{crypto.PurposeOIDC, crypto.PurposeGateway} {
		if _, err := repo.EnsureKeypair(ctx, purpose); err != nil {
			return err
		}
	}
	return nil
}

func newProtocolServer(env *config.Env, logger *zap.Logger, db *gorm.DB, rdb *store.Redis, masterKey []byte) *protocol.Server {
	authenticators := store.NewAuthenticatorRepo(db)
	challenges := store.NewChallengeRepo(db)
	registry := factor.NewRegistry()
	if err := registry.Register(holding.NewPasswordProvider(authenticators, challenges, holding.DefaultFailThreshold, holding.DefaultLockDuration)); err != nil {
		panic(err)
	}
	return protocol.New(protocol.Deps{
		Config: protocol.Config{
			Issuer:        env.Issuer,
			PublicBaseURL: env.PublicBaseURL,
			SecureCookie:  !env.IsDev(),
		},
		Logger:         logger,
		Redis:          rdb,
		Users:          store.NewUserRepo(db),
		Clients:        store.NewClientRepo(db, masterKey),
		Identifiers:    store.NewIdentifierRepo(db),
		Authenticators: authenticators,
		Challenges:     challenges,
		Sessions:       store.NewSessionRepo(db),
		Keys:           store.NewSigningKeyRepo(db, masterKey),
		Policies:       store.NewLoginPolicyRepo(db),
		Roles:          store.NewRoleBindingRepo(db),
		SessionMgr:     session.NewManager(store.NewSessionRepo(db)),
		Blind:          identifier.NewBlindIndexer(masterKey),
		Registry:       registry,
	})
}
