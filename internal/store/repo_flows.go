package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// ExpiredRetention 是「已过期流的行」在库里继续保留的时长（D5 I-F6 两段式清理）。
//
// 为什么不是"过期即删"：行一旦删掉，过期流只能返回 `FLOW_NOT_FOUND`，
// `FLOW_EXPIRED` 会时隐时现（`AC-FLOW-10` 因此 flaky）。所以：
// 第一段只置 `status='expired'`（**行保留，7 天内稳定返回 `FLOW_EXPIRED`**），
// 第二段（7 天后）才 `DELETE`。
const ExpiredRetention = 7 * 24 * time.Hour

// FlowRepo 是 `flows` 表的仓储（D5 第 15 张表）。
//
// 职责边界（D5）：
//   - 本仓储只做**存储操作**：读、带锁读、创建、列更新、两段式清理、按会话作废；
//   - 不变式校验（amr 单调 I-F2、一次性回填 I-F7、锁序与锁内禁令 I-F8）在 Core 的
//     `internal/flow` 里做，因为那需要"锁 → 校验 → 消费 challenge → 累加 amr → 写回"
//     的一整个事务语义，散在仓储里会变成隐性规则。
//
// **锁读只经 LockByID**：仓储不提供"无锁读 + 后写回"的便捷方法，
// 避免有人在事务外读到旧状态再覆盖（多副本下就是丢更新）。
type FlowRepo struct {
	db *gorm.DB
}

// NewFlowRepo 绑定连接（db 可为 *gorm.DB 或事务句柄）。
func NewFlowRepo(db *gorm.DB) *FlowRepo { return &FlowRepo{db: db} }

// ErrNilTx 表示需要事务的接口拿到了 nil 事务句柄。
var ErrNilTx = errors.New("store: a transaction handle is required for this operation")

// ErrFlowNotFound 表示按 id 没查到流（对应 `FLOW_NOT_FOUND`，E1/404）。
var ErrFlowNotFound = errors.New("store: flow not found")

// WithTx 返回绑定到事务的仓储副本（便于编排层组合）。
func (r *FlowRepo) WithTx(tx *gorm.DB) *FlowRepo { return &FlowRepo{db: tx} }

// Create 插入一条流。
//
// 用 `RETURNING *` 取回 DB 生成的时间戳（`created_at` 默认 now()、`updated_at` 由触发器维护）：
// 模型里时间戳是只读字段（`->`），应用不自己写时间，所以只能从库里读回来。
// 冲突（如同 (user_id, client_id) 已有活跃流）由 MapError 映射成 §5.4 码。
func (r *FlowRepo) Create(ctx context.Context, f *model.Flow) error {
	if f == nil {
		return errors.New("store: nil flow")
	}
	err := r.db.WithContext(ctx).Clauses(clause.Returning{}).Create(f).Error
	return MapError(err)
}

// GetByID 读一条流（**不加锁**，只用于查询/展示）。
//
// 要"读 → 改 → 写回"的路径必须用 LockByID：无锁读出来的状态在并发下随时作废。
func (r *FlowRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Flow, error) {
	if id == uuid.Nil {
		return nil, ErrFlowNotFound
	}
	var f model.Flow
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrFlowNotFound
	}
	if err != nil {
		return nil, MapError(err)
	}
	return &f, nil
}

// LockByID 在**调用方的事务里**对 `flows` 行做 `SELECT … FOR UPDATE`（D5 I-F5/I-F8）。
//
// 为什么必须传 tx：锁只在事务内有效，事务一提交锁就没了。如果允许这里自己开事务，
// 出了函数锁就丢了，调用方拿到的"锁住的流"是个假象——比不锁更危险。
//
// 锁内禁令（I-F8）：拿到这把锁之后**只允许**读该行、校验 status/expires_at/achieved_amr、
// 同一事务内消费 challenge、写回 amr/completed_steps/status；
// PBKDF2-SM3（≥10 万迭代）、短信邮件投递、外部 HTTP、审计导出**必须**在锁外完成
// （做法：先算 → 再锁 → 短事务）。
func (r *FlowRepo) LockByID(ctx context.Context, tx *gorm.DB, id uuid.UUID) (*model.Flow, error) {
	if tx == nil {
		return nil, ErrNilTx
	}
	if id == uuid.Nil {
		return nil, ErrFlowNotFound
	}
	var f model.Flow
	err := flowLockQuery(tx.WithContext(ctx), id).Take(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrFlowNotFound
	}
	if err != nil {
		return nil, MapError(err)
	}
	return &f, nil
}

// flowLockQuery 是 LockByID 的唯一查询构造点。
//
// 单独抽出来是为了**能在没有数据库的情况下断言 SQL 里真的有 FOR UPDATE**
// （GORM DryRun + 捕获日志）：锁是并发正确性的前提，"以为加了锁"是最贵的错觉。
func flowLockQuery(db *gorm.DB, id uuid.UUID) *gorm.DB {
	return db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id)
}

// FlowPatch 是列更新的白名单：**只允许**改动这几列。
//
// 用白名单而不是 `Save(&flow)`：整行覆盖会把并发事务刚写的 `achieved_amr` 冲掉
// （丢更新），也会在无意中改写 `trace_id`（D4-C(4)：只有创建时能写）。
type FlowPatch struct {
	Status         *string
	AchievedAMR    *model.StringArray
	CompletedSteps *model.StepList
	SessionID      *uuid.UUID
	UserID         *uuid.UUID
	ConsumedAt     *time.Time
}

// Apply 在事务里按白名单更新一行（**调用方应先 LockByID**）。
//
// 返回值是受影响行数：0 表示这行不在了（被清理器删掉）或被条件挡住 —— 调用方必须处理，
// 不能当成功继续。
func (r *FlowRepo) Apply(ctx context.Context, tx *gorm.DB, id uuid.UUID, patch FlowPatch) (int64, error) {
	if tx == nil {
		return 0, ErrNilTx
	}
	updates := flowPatchUpdates(patch)
	if len(updates) == 0 {
		return 0, errors.New("store: empty flow patch (refusing a no-op UPDATE)")
	}

	res := tx.WithContext(ctx).Model(&model.Flow{}).Where("id = ?", id).Updates(updates)
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// flowPatchUpdates 把白名单 patch 翻成列 → 值（纯函数，便于单测直接断言"哪些列会被写"）。
func flowPatchUpdates(patch FlowPatch) map[string]any {
	updates := map[string]any{}
	if patch.Status != nil {
		updates["status"] = *patch.Status
	}
	if patch.AchievedAMR != nil {
		updates["achieved_amr"] = patch.AchievedAMR
	}
	if patch.CompletedSteps != nil {
		updates["completed_steps"] = patch.CompletedSteps
	}
	if patch.SessionID != nil {
		updates["session_id"] = *patch.SessionID
	}
	if patch.UserID != nil {
		updates["user_id"] = *patch.UserID
	}
	if patch.ConsumedAt != nil {
		updates["consumed_at"] = *patch.ConsumedAt
	}
	return updates
}

// 两段式清理的三条语句是**常量**，因为集成测试要用 `EXPLAIN` 证明它们走哪条索引：
// 如果测试自己抄一遍 SQL，就变成"测的是一条不存在的语句"。改这里必须同步看测试。
const (
	// markExpiredSQL 第一段：到期仍 active 的流 → expired（行保留）。走 `flows_expires_idx`。
	markExpiredSQL = `UPDATE flows SET status = 'expired' WHERE status = 'active' AND expires_at <= now()`
	// markExpiredIfActiveSQL 读路径上的单行懒过期（同样用库时间判过期）。
	markExpiredIfActiveSQL = `UPDATE flows SET status = 'expired' WHERE id = $1 AND status = 'active' AND expires_at <= now()`
	// deleteExpiredBeforeSQL 第二段：只删已过期且超过保留期的行。走 `flows_expired_cleanup_idx`。
	deleteExpiredBeforeSQL = `DELETE FROM flows WHERE status = 'expired' AND updated_at < $1`
)

// MarkExpired 是两段式清理的**第一段**：把到期但仍 `active` 的流置为 `expired`（行保留）。
//
// 返回受影响行数（本次惰性过期了几条）。`expires_at <= now` 的判定用**库时间**
// （`now()`），不用应用时钟：多副本下应用时钟可能不一致，而 TTL 是安全属性。
func (r *FlowRepo) MarkExpired(ctx context.Context) (int64, error) {
	res := r.db.WithContext(ctx).Exec(markExpiredSQL)
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// MarkExpiredIfActive 把单条流惰性置为 `expired`（读路径上的懒过期）。
//
// 条件里带 `status='active'`：并发下两个请求同时发现过期也只会有一次真正更新。
// 返回 0 行表示"这条流已经不 active 了"——由调用方决定是返回 `FLOW_EXPIRED`
// 还是 `FLOW_NOT_FOUND`（`completed` 后二次提交是 NOT_FOUND，D5 I-F4）。
func (r *FlowRepo) MarkExpiredIfActive(ctx context.Context, id uuid.UUID) (int64, error) {
	res := r.db.WithContext(ctx).Exec(markExpiredIfActiveSQL, id)
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// DeleteExpiredBefore 是两段式清理的**第二段**：只删 `expired` 且 `updated_at < cutoff` 的行。
//
// `cutoff` 由调用方显式给出（= now - ExpiredRetention），本函数不含"7 天"常量之外的隐式判断。
//
// 索引：谓词 `status='expired' AND updated_at < cutoff` 由**部分索引**
// `flows_expired_cleanup_idx (updated_at) WHERE status='expired'` 覆盖（`000016` 引入，
// 口径见 `decisions.md` D5「第二段清理索引裁定」）。它与第一段的
// `flows_expires_idx (expires_at) WHERE status='active'` 列不同、行集也不同，两段各走各的。
// `updated_at` 由 `flows_set_updated_at` 触发器维护，因此保留期语义 =「最后一次状态变更
// （置 expired）的时间」，不另加列、不改触发器（D5 I-F6）。
func (r *FlowRepo) DeleteExpiredBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if cutoff.IsZero() {
		return 0, errors.New("store: DeleteExpiredBefore requires an explicit cutoff")
	}
	res := r.db.WithContext(ctx).Exec(deleteExpiredBeforeSQL, cutoff)
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// AbandonBySession 把某会话下的 `active` 流置为 `abandoned`（会话撤销时级联，D5 I-F6⑤）。
//
// 这是给 `internal/session` 用的**窄接口**（Core 侧 `Revoke` 调用），
// 目的是避免 `session ↔ flow` 两个包互相 import 形成循环。
func (r *FlowRepo) AbandonBySession(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	if sessionID == uuid.Nil {
		return 0, fmt.Errorf("store: AbandonBySession requires a session id")
	}
	res := r.db.WithContext(ctx).Exec(
		`UPDATE flows SET status = 'abandoned' WHERE session_id = $1 AND status = 'active'`, sessionID)
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// FindActiveByUserClient 查某用户对某 client 的**活跃流**（不存在返回 ErrFlowNotFound）。
//
// 这是 CH-7⑤ 要求的「**先查后建**」的查询端：正常路径先看有没有活跃流，
// 有就直接复用同一条 `flow_id` 继续走（200 + 同一条流），**不要**先插再靠唯一索引挡。
// 理由：`flows_one_active_per_user_client_uniq` 的 23505 在 CH-7 里映射成
// `COOLDOWN_ACTIVE`(409)，那是「要等」的语义；而用户其实只是"接着上一次走"。
// 23505 只应作为**并发竞态兜底**（两个请求同时判定"没有活跃流"再同时插入），
// 出现即说明先查后建生效了但撞上了竞态，属可重试的极小概率路径。
//
// 匿名流（user_id 为 NULL）不适用本查询：匿名流按 `flow_id` 直接携带（CH-1）。
func (r *FlowRepo) FindActiveByUserClient(ctx context.Context, userID uuid.UUID, clientID string) (*model.Flow, error) {
	if userID == uuid.Nil || clientID == "" {
		return nil, ErrFlowNotFound
	}
	var f model.Flow
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND client_id = ? AND status = 'active'", userID, clientID).
		Order("created_at DESC").
		Take(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrFlowNotFound
	}
	if err != nil {
		return nil, MapError(err)
	}
	return &f, nil
}

// CountActiveByUserClient 统计某用户对某 client 的活跃流条数（用于诊断与用例断言）。
//
// 业务路径不需要它——唯一性由 `flows_one_active_per_user_client_uniq` 保证；
// 但排查"为什么插不进去"时，先看这条就看得到底。
func (r *FlowRepo) CountActiveByUserClient(ctx context.Context, userID uuid.UUID, clientID string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.Flow{}).
		Where("user_id = ? AND client_id = ? AND status = 'active'", userID, clientID).
		Count(&n).Error
	if err != nil {
		return 0, MapError(err)
	}
	return n, nil
}
