package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/admin"
	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/gateway"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
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
	*session.HTTPHandler, *admin.SessionsHTTP, error) {
	keys, err := loadSigningKeys(ctx, db, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: S7 requires signing keys: %w", err)
	}
	resolver, err := newCallerResolver(keys, db)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: S7 caller resolver: %w", err)
	}
	if resolver == nil {
		return nil, nil, errors.New("startup: S7 caller resolver is nil")
	}

	// 撤销级联走 D5 的窄接口 `AbandonBySession`（不直接 import flowstore，避免成环）。
	ops := store.NewSessionOpsRepo(db)
	flowRepo := store.NewFlowRepo(db)
	events := store.NewAuthEventRepo(db)

	// S10：撤销后的单点登出通知（`AC-DP-12`）。
	//
	// 装配口径（如实登记，别当成"已送达"）：
	//   - 广播器默认用 `gateway.NoNotifier`（**什么都不发**）——R12 的 wire 实现
	//     （`gateway.HTTPNotifier`，见 S10 的 `internal/gateway/slo_http.go`）落地后，
	//     只需把这里的 `gateway.NoNotifier{}` 换掉，页面与撤销路径都不用动；
	//   - 撤销路径**不因通知失败而回滚**（判据原文）：接缝无返回值、投递异步，
	//     失败/panic/超时都只留日志与计数（`SloBroadcaster.Failures()`）。
	slo := gateway.NewSloBroadcaster(gateway.NoNotifier{}, gateway.DefaultSloTimeout, logger)

	mgr, err := session.NewManager(ops, flowRepo, events, session.Options{
		Logger:   logger,
		Notifier: sloNotifier{broadcaster: slo},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("startup: session manager: %w", err)
	}
	sessionHandler, err := session.NewHTTPHandler(mgr, resolver, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: session http: %w", err)
	}
	adminSessions, err := admin.NewSessionsHTTP(mgr, resolver, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: admin sessions http: %w", err)
	}
	return sessionHandler, adminSessions, nil
}

// sloNotifier 是 `session.RevokeNotifier` → `gateway.SloBroadcaster` 的**唯一**适配点。
//
// 为什么适配放在装配层（`cmd/guanjind`）而不是 `internal/session`：
//   - 会话生命周期不该 import 数据面的 wire 类型（S7 反向依赖 S8，R12 再改一次就渗进来）；
//   - 适配只做"字段搬运"：`session.RevokeNotice.Subject` 是被撤销的会话行，
//     `gateway.RevocationNotice` 要的是 `client_id` 列表（投递端按路由表查 `upstream`）。
//
// 会话行的 `client_id` 就是"这台设备登出前在用的业务系统"；`sso_logout_scope=all_clients`
// 时级联会**逐会话**各发一条，客户端集合由投递端按 `session_id` 去重（幂等是 `Notifier` 的契约）。
type sloNotifier struct{ broadcaster *gateway.SloBroadcaster }

func (n sloNotifier) NotifyRevoked(ctx context.Context, notice session.RevokeNotice) {
	clients := revokeNoticeClients(notice.Subject)
	if len(clients) == 0 {
		// 拿不到 client 就没法查路由：**明确不发**，而不是发到"默认上游"（那等于猜目标）。
		return
	}
	n.broadcaster.NotifyRevoked(ctx, gateway.RevocationNotice{
		UserID:    notice.UserID,
		SessionID: notice.SessionID,
		ClientIDs: clients,
		Reason:    notice.Reason,
		At:        notice.At,
	})
}

// revokeNoticeClients 从"被撤销的那一行"里取业务系统 id（当前只可能是本会话的 client）。
func revokeNoticeClients(subject any) []string {
	s, ok := subject.(*model.Session)
	if !ok || s == nil || s.ClientID == "" {
		return nil
	}
	return []string{s.ClientID}
}
