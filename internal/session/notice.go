package session

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是 **S10 的单点登出通知（SLO）挂点**：撤销成功后把"撤销事实"交给投递方
// （`AC-DP-12` 的"送达"半边由 S10 落地，R12 = 候选 A）。
//
// 为什么挂在这里：撤销的唯一收口是 `cascade`（`Revoke` / `RevokeAllByUser` / `RevokeOthers` /
// `Logout` 全经过它），挂在收口处就覆盖了全部级联路径，不必在四个调用点各写一遍——
// 漏一个调用点，就等于"某些撤销不通知"，而这种漏是静默的。
//
// 边界（明确不做什么）：
//   - 本包**不实现投递**：报文、超时、重试、目标地址全在 `internal/gateway`（S8/S10）；
//     本包只把"撤销事实 + 签断言需要的身份事实"交出去；
//   - 本包**不 import gateway**：否则 S7 反向依赖 S8，gateway 的 wire 类型会渗进会话生命周期；
//   - 通知**永不**影响撤销结果（`AC-DP-12`）：接口无返回值、调用异步、失败与 panic 都吞掉；
//   - `Notifier` 为 nil 时**完全不通知、不打日志**（默认行为与加挂点之前逐字节一致）。

// RevokeNotice 是一次**已提交**的会话撤销事实（每个被撤销的会话一条）。
type RevokeNotice struct {
	// UserID 被撤销的用户（`users.id`）。
	UserID string
	// SessionID 被撤销的会话（`sessions.id`）；级联撤销时逐个会话一条。
	SessionID string
	// Reason 撤销原因（`sessions.revoke_reason` 的取值，见 `RevokeReason`）。
	Reason string
	// At 撤销生效时刻（与 `sessions.revoked_at` / `auth_events.occurred_at` 同一次取时）。
	At time.Time
	// Subject 是签通知断言所需的**身份事实**的载体。
	//
	// 本包填 `*model.Session`（被撤销的那一行：`user_id`/`client_id`/`acr`/`amr` 都在上面），
	// 由装配层断言后补齐 `uname`/`roles`（那两项要查 `users` / `role_bindings`，不属本包）。
	//
	// 为什么不让投递方按 `sid` 回库查：撤销路径手里已经握着这些事实，让投递端再查一次库，
	// 等于把"通知失败"的半径扩大到"查库也可能失败"。用 `any` 而不是具体类型，是为了让本包
	// 不依赖投递方的类型（见文件头"不 import gateway"）。
	Subject any
}

// RevokeNotifier 是**唯一**的投递接缝（实现由装配层提供，最终交给 `gateway.SloBroadcaster`）。
//
// 契约：**没有返回值**。签名上就不存在"失败路径"可供调用方依赖——通知失败不得影响撤销。
// 实现必须自己兜住超时/重试/失败计数，并且必须是幂等的（重试会让同一条通知被投递两次，
// 业务方按 `session_id` 去重）。
type RevokeNotifier interface {
	NotifyRevoked(ctx context.Context, n RevokeNotice)
}

// noticeOf 把会话行折成一条通知（`Subject` 直接指向该行，投递方按需断言）。
func noticeOf(s *model.Session, reason RevokeReason, at time.Time) RevokeNotice {
	return RevokeNotice{
		UserID:    s.UserID.String(),
		SessionID: s.ID.String(),
		Reason:    string(reason),
		At:        at,
		Subject:   s,
	}
}

// fireRevokeNotice 异步投递一条通知；`notifier` 为 nil 时**什么都不做**（含不打日志）。
//
// 为什么异步：`SloBroadcaster` 是同步的，单条通知最多 `1000ms × 2 次 × client 数`；
// 一次 `logout_all` 级联十几条会话会把这些延迟线性叠在 HTTP 响应上——而撤销早已提交，
// 让调用方等投递没有任何收益。撤销是低频动作，goroutine 数量天然受撤销频率约束，
// 因此不引入池化（池会带来生命周期、关闭与排空语义，代价大于收益）。
//
// 已知限制（写下来，不藏着）：进程退出时在途通知可能丢失；没有背压。`AC-DP-12` 只要求
// "通知失败不阻塞本地撤销"，这两条不影响判据；若将来要"送达率"指标，得先在基线里定口径。
//
// `context.WithoutCancel`：保留 `traceID` 等值、去掉请求的取消信号与超时——
// HTTP 响应返回（请求 ctx 结束）之后仍要送达。
func (m *Manager) fireRevokeNotice(ctx context.Context, n RevokeNotice) {
	if m == nil || m.notifier == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithoutCancel(ctx)
	notifier := m.notifier
	go func() {
		defer func() {
			// 投递方自己会 recover，但接缝不能假设实现守规矩：一次 panic 不该带走进程。
			if r := recover(); r != nil {
				m.logger.Warn("session: revoke notifier panicked",
					"session_id", n.SessionID, "panic", r)
			}
		}()
		notifier.NotifyRevoked(ctx, n)
	}()
}

// NoticeRevoked 对一条**已经撤销**的会话补发通知。
//
// 存在的理由：并发上限"挤掉最旧"（`login_policies.concurrent_overflow_action ∈ evict_oldest*`）
// 走的是 `store.SessionOpsRepo.EvictOldestActive` 的单语句 CTE，不经过本包的 `cascade`——
// 那条路径的调用方（S6 建会话）拿到被挤掉的 `sid` 后调本方法一行即可，不必自己拼通知、
// 也不必为了签断言再查一次会话。
//
// 失败只留日志：会话撤销已经生效，通知是副作用；这里返回 error 只会诱导调用方
// 用"通知失败"去回滚撤销（方向反了）。
func (m *Manager) NoticeRevoked(ctx context.Context, sessionID uuid.UUID, reason RevokeReason) {
	if m == nil || m.notifier == nil || sessionID == uuid.Nil {
		return
	}
	s, err := m.store.GetByID(ctx, sessionID)
	if err != nil {
		m.logger.Warn("session: load evicted session for notice failed",
			"session_id", sessionID, "error", err)
		return
	}
	m.fireRevokeNotice(ctx, noticeOf(s, reason, m.now().UTC()))
}
