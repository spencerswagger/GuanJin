package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/admin"
	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是 **S7 的装配点**（会话自助面 + 管理面 sessions 段）。
//
// 为什么单独一个文件而不是加进 `wiring.go`：`wiring.go` 与 `httpx/errors.go` 归 S6 所有
// （spencer 裁定 2026-11-04，避免两单同文件冲突）。S7 的接线放在自己的文件里，
// 只依赖 S6 已导出的两件事：`loadSigningKeys`（`keygen.go`）与 `newCallerResolver`（`wiring.go`）。
//
// 令牌验签**不在本包**：`httpx.CallerResolver` 由 S6 的 `protocol.CallerResolver` 实现
// （只接受 `SM3withSM2`、校验 `exp`/`nbf`、查会话状态），本文件只把它注入到 S7 的 handler。

// newSessionWiring 装出 S7 的两组路由。
//
// fail-fast：拿不到签名密钥/解析器就直接让进程起不来——否则端点会静默回 401，
// 看起来像"用户没登录"，实际是"装配没接上"。
func newSessionWiring(ctx context.Context, db *gorm.DB, cfg *config.Config, logger *slog.Logger) (
	*session.HTTPHandler, *admin.SessionsHTTP, session.TokenResolver, error) {
	keys, err := loadSigningKeys(ctx, db, cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: S7 requires signing keys: %w", err)
	}
	resolver, err := newCallerResolver(keys, db)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: S7 caller resolver: %w", err)
	}
	if resolver == nil {
		return nil, nil, nil, errors.New("startup: S7 caller resolver is nil")
	}

	// 撤销级联走 D5 的窄接口 `AbandonBySession`（不直接 import flowstore，避免成环）。
	ops := store.NewSessionOpsRepo(db)
	flowRepo := store.NewFlowRepo(db)
	events := store.NewAuthEventRepo(db)

	// S10：撤销后的单点登出通知（`AC-DP-12` 的送达半边，R12 = 候选 A）。
	// 装配在 `slo_wiring.go`：目标取路由表该 client 的 `upstream`，报文复用断言的十项 claim。
	notifier, err := newSLORevokeNotifier(db, cfg, keys, logger)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: %w", err)
	}

	mgr, err := session.NewManager(ops, flowRepo, events, session.Options{
		Logger:   logger,
		Notifier: notifier,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: session manager: %w", err)
	}
	sessionHandler, err := session.NewHTTPHandler(mgr, resolver, logger)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: session http: %w", err)
	}
	adminSessions, err := admin.NewSessionsHTTP(mgr, resolver, logger)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: admin sessions http: %w", err)
	}
	return sessionHandler, adminSessions, resolver, nil
}
