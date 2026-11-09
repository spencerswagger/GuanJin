package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/gateway"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/signing"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是 **S10 · `AC-DP-12` 送达半边的装配**：把 `session.RevokeNotifier`
// （Core 的挂点，`internal/session/notice.go`）接到 `gateway.HTTPNotifier`（R12 = 候选 A）。
//
// 三件事都在这一层做完，谁也不多知道一点：
//  1. **目标**：从路由表（`GUANJIN_GATEWAY_ROUTES`）取该 client 的 `upstream`——
//     与数据面同一份静态映射，不猜域名、不做服务发现；
//  2. **身份事实**：`session.RevokeNotice.Subject` 是 `*model.Session`（Core 不 import gateway），
//     这里补齐 `uname`（`users.username`）与 `roles`（`role_bindings`），再交给签发器；
//  3. **范围**：`login_policies.sso_logout_scope`：`current_client` → 只通知本会话的 client；
//     `all_clients` → 该用户在 `user_visited_clients` 里出现过的全部 client（这张表存在的理由）。
//
// 明确不做：不加开关（A 是默认口径）、不加 claim（十项就是全部）、不动表结构（CH-9 只登记）。

// sloRevokeNotifier 是 `session.RevokeNotifier` 的实现。
//
// 它**不返回错误**（接缝如此）：投递失败由 `SloBroadcaster` 计数与记日志，
// 撤销早已提交，通知是副作用（判据原文：失败不阻塞本地撤销）。
type sloRevokeNotifier struct {
	broadcaster *gateway.SloBroadcaster
	sessions    *store.SessionOpsRepo
	users       *store.UserRepo
	roles       *store.RoleBindingRepo
	visits      *store.VisitRepo
	logger      *slog.Logger
}

// newSLORevokeNotifier 装出投递链路。
//
// fail-fast：签名器缺 `gateway` 用途的密钥、路由表解析失败 → 直接让进程起不来
// （否则每条撤销都会静默"没有通知"，而判据要的是"送达"）。
func newSLORevokeNotifier(db *gorm.DB, cfg *config.Config, keys *signing.Set,
	logger *slog.Logger) (session.RevokeNotifier, error) {

	signer, err := gateway.NewAssertionSigner(keys)
	if err != nil {
		return nil, fmt.Errorf("startup: slo notifier signer: %w", err)
	}
	routes, err := gateway.ParseRoutes(os.Getenv("GUANJIN_GATEWAY_ROUTES"))
	if err != nil {
		return nil, fmt.Errorf("startup: slo notifier routes: %w", err)
	}
	router, err := gateway.NewRouter(routes)
	if err != nil {
		return nil, fmt.Errorf("startup: slo notifier router: %w", err)
	}
	notifier, err := gateway.NewHTTPNotifier(signer, gateway.UpstreamsFromRouter(router),
		gateway.HTTPNotifierConfig{
			// 候选 A：默认 `/internal/guanjin/v1/logout`，部署方可用 env 改路径（不是开关）。
			Path:   strings.TrimSpace(os.Getenv("GUANJIN_SLO_NOTIFY_PATH")),
			Logger: logger,
		})
	if err != nil {
		return nil, fmt.Errorf("startup: slo http notifier: %w", err)
	}

	broadcaster := gateway.NewSloBroadcaster(notifier, gateway.DefaultSloTimeout, logger)
	if len(router.Routes()) == 0 {
		// 空路由表是合法的（还没接业务系统）：此时通知一律 `ErrSloNoUpstream` 计失败。
		// 打一条日志让部署方知道"登出通知暂时发不出去"，而不是以为已经送达。
		logger.Warn("slo: route table is empty; logout notifications will not be delivered")
	}
	return &sloRevokeNotifier{
		broadcaster: broadcaster,
		sessions:    store.NewSessionOpsRepo(db),
		users:       store.NewUserRepo(db),
		roles:       store.NewRoleBindingRepo(db),
		visits:      store.NewVisitRepo(db),
		logger:      logger,
	}, nil
}

// NotifyRevoked 见 `session.RevokeNotifier`（无返回值：失败不影响撤销）。
func (n *sloRevokeNotifier) NotifyRevoked(ctx context.Context, notice session.RevokeNotice) {
	if n == nil || n.broadcaster == nil {
		return
	}
	subject, clients := n.subjectAndClients(ctx, notice)
	if len(clients) == 0 || subject == nil {
		// 拿不到身份事实或没有目标：**明确不发**（不猜目标、不发"没有 sub"的通知）。
		n.logger.Warn("slo: skip notify (no subject or no target client)",
			"session_id", notice.SessionID, "reason", notice.Reason)
		return
	}
	n.broadcaster.NotifyRevoked(ctx, gateway.RevocationNotice{
		UserID:    notice.UserID,
		SessionID: notice.SessionID,
		ClientIDs: clients,
		Reason:    notice.Reason,
		At:        notice.At,
		Subject:   subject,
	})
}

// subjectAndClients 把"被撤销的那一行"折成签发输入与目标 client 列表。
func (n *sloRevokeNotifier) subjectAndClients(ctx context.Context,
	notice session.RevokeNotice) (*gateway.AssertionInput, []string) {

	s, ok := notice.Subject.(*model.Session)
	if !ok || s == nil || s.ClientID == "" {
		return nil, nil
	}
	// `uname`：断言取 `users.username`（规范化存储值），不取 display_name。
	user, err := n.users.GetByID(ctx, s.UserID)
	if err != nil {
		n.logger.Warn("slo: load user for assertion failed",
			"session_id", notice.SessionID, "error", err)
		return nil, nil
	}
	subject := &gateway.AssertionInput{
		Sub:   s.UserID.String(),
		CID:   s.ClientID,
		ACR:   s.ACR,
		AMR:   append([]string(nil), s.AMR...),
		Uname: user.Username,
	}
	// 目标列表：先定范围，再逐 client 取角色（角色是"该 client 内"的，不能跨系统复用）。
	targets := n.targetClients(ctx, s)
	if len(targets) == 0 {
		return nil, nil
	}
	// `roles` 取**发起 client**（会话所属系统）的角色：广播时每个目标的断言 `aud` 会被
	// `HTTPNotifier` 覆盖成目标 client，但 `roles` 没有"按目标重算"的依据（撤销时不应再回库
	// 逐个 client 查角色——那会把一次撤销放大成 N 次查询）。这是**明确的取舍**，
	// 已写进 PR 风险栏：跨系统代持的角色在登出通知里不重新求解。
	roles, err := n.roles.RolesForUserClient(ctx, s.UserID, s.ClientID)
	if err != nil {
		n.logger.Warn("slo: load roles for assertion failed",
			"session_id", notice.SessionID, "error", err)
		return nil, nil
	}
	subject.Roles = roles
	return subject, targets
}

// targetClients 按 `sso_logout_scope` 定级联范围。
//
// 策略读不出来时**退化为只通知本会话的 client**并留 WARN：撤销已经生效，
// 因为"读不到策略"就把整条通知丢掉，等于让业务方一直以为用户还登着。
func (n *sloRevokeNotifier) targetClients(ctx context.Context, s *model.Session) []string {
	policy, err := n.sessions.LoadSessionPolicy(ctx, s.ClientID)
	if err != nil {
		if !errors.Is(err, store.ErrPolicyNotConfigured) {
			n.logger.Warn("slo: load session policy failed; fallback to current client",
				"client_id", s.ClientID, "error", err)
		}
		return []string{s.ClientID}
	}
	if policy.SSOLogoutScope != "all_clients" {
		return []string{s.ClientID}
	}
	visited, err := n.visits.ListClientIDs(ctx, s.UserID)
	if err != nil {
		n.logger.Warn("slo: load visited clients failed; fallback to current client",
			"user_id", s.UserID.String(), "error", err)
		return []string{s.ClientID}
	}
	// 把本会话的 client 也带上（用户可能从没在 `user_visited_clients` 里出现过，例如刚建号）。
	out := make([]string, 0, len(visited)+1)
	seen := map[string]bool{}
	for _, c := range append([]string{s.ClientID}, visited...) {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// sloUpstreamOf 是一个小工具：按 client 取上游（诊断日志与测试用）。
func sloUpstreamOf(router *gateway.Router, clientID string) (*url.URL, bool) {
	return gateway.UpstreamsFromRouter(router)(clientID)
}
