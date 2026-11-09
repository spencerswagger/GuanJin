package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// 本文件是 S8 的 ⑦ **单点登出通知（SLO）的传输无关骨架**。
//
// ## 现在能保证什么（AC-DP-12 可判的那一半）
//
// 「**通知失败不阻塞本地撤销**」：撤销是本地事实（`sessions.status='revoked'` 已落库），
// 通知是尽力而为的副作用。故本类型保证：
//   - 失败/超时/`panic` 都不改变、不延迟调用方的撤销结果（超时有上限，`panic` 被 recover）；
//   - 每次失败都留痕（结构化日志 + `Failures()` 计数），不"静默吞掉"；
//   - 调用方**不需要**看返回值（方法没有返回值，从签名上就没有"失败路径"可依赖）。
//
// ## 现在不能保证什么（R12，**待裁**）
//
// 通知的 wire 契约——目标地址从哪来、报文长什么样、事件名是什么——基线里**没有**。
// 自造端点名/事件名/JSON 字段属新增对外契约面（F0 禁自造 wire 名），故本文件
// **只定义接缝**（`Notifier`），不实现任何一种具体报文；`NoNotifier` 是默认装配值，
// 也就是"不通知"，而不是"发一个假报文"。裁定后由 `Notifier` 的实现补上，调用点不用改。
// 记账口径：`AC-DP-12` 的"送达并被验签"半边记 **PENDING·R12**，不得写成 PASS（D16④）。

// RevocationNotice 是一次撤销事实的**最小**描述（不含任何 wire 格式）。
type RevocationNotice struct {
	// UserID 被撤销的用户（`users.id`）。
	UserID string
	// SessionID 被撤销的会话（`sessions.id`）。
	SessionID string
	// ClientIDs 需要通知的业务系统集合。
	//
	// 取值范围由撤销侧按 `login_policies.sso_logout_scope` 决定：
	// `current_client` → 只通知本会话的 client；`all_clients` → 该用户在
	// `user_visited_clients` 里出现过的全部 client（这就是那张表存在的理由）。
	ClientIDs []string
	// Reason 撤销原因（S7 的 `revoke_reason` 取值，如 `logout`/`admin_revoke`/`concurrent_limit`）。
	Reason string
	// At 撤销生效时刻。
	At time.Time
	// Subject 是签通知断言所需的**身份事实**（`uname`/`roles`/`acr`/`amr`/`cid`）；
	// `aud` 由投递方按目标 client 覆盖（D16①：`aud` 永远是目标业务系统）。
	//
	// 为什么放在这里而不让 notifier 回库查：撤销路径已经握着这些事实（就在被撤销的会话上），
	// 让投递端再查一次库，等于把"通知失败"的半径扩大到"查库也可能失败"。
	Subject *AssertionInput
}

// Notifier 是**唯一**的投递接缝（实现属 R12 裁定之后；测试用假实现）。
//
// 约定：返回 error 只表示"这次没送到"，不代表撤销失败；实现必须是幂等的
// （同一条通知可能因重试被投递两次，业务方按 `session_id` 去重）。
type Notifier interface {
	Notify(ctx context.Context, notice RevocationNotice) error
}

// NoNotifier 是默认装配值：**什么都不发**。
//
// 为什么默认是"不发"而不是"发个占位报文"：占位报文一旦上线，就成了事实契约
// （业务方会照它对接），而 R12 还没裁。宁可不发，也不签发一个没人认的对外格式。
type NoNotifier struct{}

// Notify 实现 `Notifier`：不发送、不报错。
func (NoNotifier) Notify(context.Context, RevocationNotice) error { return nil }

// DefaultSloTimeout 是单条通知的投递上限。
//
// 撤销请求不能被通知拖住（AC-DP-12），故上限取"数据面一次上游超时"同量级（5s）已足够宽：
// 超时后只记一次失败，撤销照常返回。
const DefaultSloTimeout = 5 * time.Second

// SloBroadcaster 把"撤销事实"投递给各业务系统，并吞掉一切失败。
type SloBroadcaster struct {
	notifier Notifier
	timeout  time.Duration
	logger   *slog.Logger
	failures atomic.Int64
	sent     atomic.Int64
}

// NewSloBroadcaster 构造投递器；`notifier` 为 nil 时用 `NoNotifier`（= 不通知）。
func NewSloBroadcaster(notifier Notifier, timeout time.Duration, logger *slog.Logger) *SloBroadcaster {
	if notifier == nil {
		notifier = NoNotifier{}
	}
	if timeout <= 0 {
		timeout = DefaultSloTimeout
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SloBroadcaster{notifier: notifier, timeout: timeout, logger: logger}
}

// NotifyRevoked 投递一次撤销通知。**永不返回错误、永不阻塞超过 timeout**：
// 撤销已经生效，通知只是副作用（AC-DP-12）。
//
// 每个 client 单独投递：一个业务系统挂掉不该连累其他系统收到通知。
func (b *SloBroadcaster) NotifyRevoked(ctx context.Context, notice RevocationNotice) {
	if b == nil {
		return
	}
	for _, clientID := range notice.ClientIDs {
		if clientID == "" {
			continue
		}
		one := notice
		one.ClientIDs = []string{clientID}
		b.deliver(ctx, clientID, one)
	}
}

// deliver 投递一条，并把任何形式的失败收敛成"记一次 + 继续"。
func (b *SloBroadcaster) deliver(ctx context.Context, clientID string, notice RevocationNotice) {
	if ctx == nil {
		ctx = context.Background()
	}
	// 每次投递自己带截止时间：调用方的 ctx 可能没有 deadline（HTTP 请求上下文就是这样）。
	callCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	if err := b.safeNotify(callCtx, notice); err != nil {
		b.failures.Add(1)
		b.logger.Warn("slo: notify failed (本地撤销不受影响)",
			"error", err, "client_id", clientID,
			"session_id", notice.SessionID, "reason", notice.Reason)
		return
	}
	b.sent.Add(1)
}

// safeNotify 把实现里的 panic 也归成失败：一个写错的 `Notifier` 不该让撤销路径崩掉。
func (b *SloBroadcaster) safeNotify(ctx context.Context, notice RevocationNotice) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("notifier panicked: %v", r)
		}
	}()
	if b.notifier == nil {
		return nil
	}
	if err := b.notifier.Notify(ctx, notice); err != nil {
		// `context.DeadlineExceeded` 是常态（对端慢），仍按失败计数并留痕，但不升级。
		return fmt.Errorf("notify: %w", err)
	}
	return nil
}

// Failures / Sent 是给 S10 的指标接缝（本切片只提供计数，不建指标后端）。
func (b *SloBroadcaster) Failures() int64 {
	if b == nil {
		return 0
	}
	return b.failures.Load()
}

// Sent 已成功投递的条数。
func (b *SloBroadcaster) Sent() int64 {
	if b == nil {
		return 0
	}
	return b.sent.Load()
}

// ErrSloWireContractPending 是"R12 未裁"的可执行痕迹：任何具体 Notifier 实现
// 在裁定前都应返回它，而不是自己发明报文。
var ErrSloWireContractPending = errors.New("gateway: SLO notify wire contract pending (R12)")
