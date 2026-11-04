package store

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// SessionRepo 是 `sessions` 的仓储（S7）。状态机判定在 `internal/session`，这里只做存。
//
// 三条刻意的设计：
//  1. **撤销永远是一次条件 UPDATE**（`WHERE id = ? AND status = 'active'`）：先读再写会在
//     并发下被"两个请求都读到 active"绕过，条件更新由行锁保证原子；返回 RowsAffected
//     让调用方区分"真的撤销了"与"早就不在 active"。
//  2. **会话一律不做 `FOR UPDATE`**（D5 I-F8 的全站锁序 `sessions → flows`）：本仓储不提供
//     任何加锁读接口，`EvictOldestActive` 用的是单语句 CTE（`FOR UPDATE SKIP LOCKED` 只锁
//     被挤掉的那一行，且与 UPDATE 同语句，不存在"拿着会话锁去等流锁"的路径）。
//  3. **会话永不物理删除**：撤销只改 `status/revoked_at/revoke_reason`，过期同理；
//     `flows.session_id` 的 `ON DELETE SET NULL` 因此没有触发路径（别把它当归属保障）。
type SessionRepo struct{ db *gorm.DB }

// NewSessionRepo 绑定连接。
func NewSessionRepo(db *gorm.DB) *SessionRepo { return &SessionRepo{db: db} }

// WithTx 返回绑定事务的副本。
func (r *SessionRepo) WithTx(tx *gorm.DB) *SessionRepo { return &SessionRepo{db: tx} }

// ErrSessionNotFound 表示按 id 没查到会话（对外 `SESSION_NOT_FOUND`，E3/404）。
var ErrSessionNotFound = errors.New("store: session not found")

// SessionQuery 是管理面列表的查询条件（接口 §3.8 sessions 段：`?user_id=&client_id=&active=true`）。
//
// `Active` 用指针：`nil` = 不过滤状态，`true` = 只看 active。列表默认**不隐式过滤**——
// 审计员要看的是"全部在线会话"还是"含已撤销"，必须由调用方说清。
type SessionQuery struct {
	UserID   *uuid.UUID
	ClientID *string
	Active   *bool
	// Limit 必填且 <= MaxSessionPageLimit（D3-A′：`limit` > 200 → `PAGE_SIZE_EXCEEDED`）。
	Limit int
	// Cursor 是不透明游标（本仓储自己编解码，调用方不得解析/拼装）。
	Cursor string
}

// MaxSessionPageLimit 是列表页大小的上限（D3-A′：`limit` > 200 → `PAGE_SIZE_EXCEEDED`）。
const MaxSessionPageLimit = 200

// SessionPage 是列表的一页（D3-A′：`{items, next_cursor}`，**不返回 total**）。
type SessionPage struct {
	Items      []model.Session
	NextCursor string
}

// Create 落一条会话——**调用方是 S6 的协议面**（spencer 裁定 2026-11-04：会话创建归 S6）。
//
// S7 只提供这个数据原语，不在本包再实现一套"建会话"（双超时取值、并发上限、`sid` 落点
// 都在 S6）。状态、时间、ACR 一律由调用方显式给（模型侧没有 `default:active` tag）：
// 漏传要当场失败，而不是由 DB 兜一个值出来。
func (r *SessionRepo) Create(ctx context.Context, s *model.Session) error {
	switch {
	case s == nil:
		return errors.New("store: nil session")
	case s.UserID == uuid.Nil:
		return errors.New("store: session requires user_id")
	case strings.TrimSpace(s.ClientID) == "":
		return errors.New("store: session requires client_id")
	case s.ACR == "":
		return errors.New("store: session requires acr")
	case s.Status == "":
		return errors.New("store: session requires status")
	case s.IssuedAt.IsZero():
		return errors.New("store: session requires issued_at")
	case s.ExpiresAt.IsZero():
		return errors.New("store: session requires expires_at")
	case s.IdleExpiresAt.IsZero():
		return errors.New("store: session requires idle_expires_at")
	case s.IdleExpiresAt.After(s.ExpiresAt):
		// DB CHECK `sessions_time_order` 会拒，这里先拒一次：错误信息能指明是哪两个字段，
		// 而不是一句 23514。
		return errors.New("store: session idle_expires_at must not exceed expires_at")
	case s.AMR == nil:
		s.AMR = model.StringArray{}
	}
	return MapError(r.db.WithContext(ctx).Create(s).Error)
}

// GetByID 读一条会话（不加锁；会话不加锁读，锁序见包注释）。
func (r *SessionRepo) GetByID(ctx context.Context, id uuid.UUID) (*model.Session, error) {
	if id == uuid.Nil {
		return nil, ErrSessionNotFound
	}
	var s model.Session
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, MapError(err)
	}
	return &s, nil
}

// ListByUser 取某用户的全部会话（自助面 `GET /api/v1/me/sessions`，按签发时间倒序）。
//
// 自助面**不隐藏已撤销/已过期**的会话：页面要展示的是"登录记录"，撤销过的记录同样要看得到
// （否则用户看不到"谁在什么时候被踢下线"）。是否只显示 active 由页面决定，不由仓储替它裁。
func (r *SessionRepo) ListByUser(ctx context.Context, userID uuid.UUID, limit int) ([]model.Session, error) {
	if userID == uuid.Nil {
		return nil, errors.New("store: empty user id")
	}
	if limit <= 0 || limit > MaxSessionPageLimit {
		return nil, fmt.Errorf("store: session list limit out of range: %d", limit)
	}
	var out []model.Session
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("issued_at DESC, id DESC").
		Limit(limit).
		Find(&out).Error
	if err != nil {
		return nil, MapError(err)
	}
	return out, nil
}

// ActiveIDsByUser 取某用户全部活跃会话的 id（撤销级联用：逐个会话作废其活跃流）。
//
// 不复用 `ListByUser`：那条有页大小上限（200），而"撤销该用户全部会话"不能因为会话多
// 就漏掉一部分——漏掉的那部分恰恰是"没被踢下线"的安全事故。
func (r *SessionRepo) ActiveIDsByUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	if userID == uuid.Nil {
		return nil, errors.New("store: empty user id")
	}
	var ids []uuid.UUID
	err := r.db.WithContext(ctx).Model(&model.Session{}).
		Where("user_id = ? AND status = ?", userID, model.SessionStatusActive).
		Pluck("id", &ids).Error
	if err != nil {
		return nil, MapError(err)
	}
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return ids, nil
}

// UserExists 判断用户是否存在（管理面 `DELETE /api/v1/sessions?user_id=` 要回 `USER_NOT_FOUND`）。
//
// 用 `SELECT 1 ... LIMIT 1` 而不是 `COUNT(*)`：只要存在性，不要行数。
func (r *SessionRepo) UserExists(ctx context.Context, id uuid.UUID) (bool, error) {
	if id == uuid.Nil {
		return false, nil
	}
	var n int
	if err := r.db.WithContext(ctx).Raw(`SELECT 1 FROM users WHERE id = ? LIMIT 1`, id).Scan(&n).Error; err != nil {
		return false, MapError(err)
	}
	return n == 1, nil
}

// ClientNames 按 client_id 批量取展示名（列表响应里的 `client.name`，接口 §3.6.1）。
//
// 单独一次查询而不是 join 进列表 SQL：`sessions` 的列表已经按游标分页，
// 再 join `clients` 会把 keyset 计划变复杂；一页最多 200 个 client_id，一次 IN 查询足够。
// 查不到的 client 不出现在 map 里（调用方回落到空串，不编造名字）。
func (r *SessionRepo) ClientNames(ctx context.Context, clientIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(clientIDs))
	if len(clientIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		ClientID string `gorm:"column:client_id"`
		Name     string `gorm:"column:name"`
	}
	if err := r.db.WithContext(ctx).Raw(
		`SELECT client_id, name FROM clients WHERE client_id IN ?`, clientIDs).Scan(&rows).Error; err != nil {
		return nil, MapError(err)
	}
	for _, row := range rows {
		out[row.ClientID] = row.Name
	}
	return out, nil
}

// List 是管理面列表（游标分页，D3-A′）。
func (r *SessionRepo) List(ctx context.Context, q SessionQuery) (*SessionPage, error) {
	if q.Limit <= 0 || q.Limit > MaxSessionPageLimit {
		return nil, fmt.Errorf("store: session list limit out of range: %d", q.Limit)
	}
	cur, err := decodeSessionCursor(q.Cursor)
	if err != nil {
		return nil, err
	}

	tx := r.db.WithContext(ctx).Model(&model.Session{})
	if q.UserID != nil {
		tx = tx.Where("user_id = ?", *q.UserID)
	}
	if q.ClientID != nil {
		tx = tx.Where("client_id = ?", *q.ClientID)
	}
	if q.Active != nil && *q.Active {
		// 只有 `active=true` 才加过滤。`active=false` 的语义是"不要只看 active"
		// （含 revoked/expired，审计要全量视图），**不是**"只看非 active"——加反向过滤
		// 会把"在线会话"从审计员眼里抹掉。
		tx = tx.Where("status = ?", model.SessionStatusActive)
	}
	if cur != nil {
		// 行值比较走 `(issued_at, id)` 与排序键一致：翻页不会漏行也不会重复行
		// （只用 issued_at 会在同毫秒多行时抖）。
		tx = tx.Where("(issued_at, id) < (?, ?)", cur.IssuedAt, cur.ID)
	}

	var rows []model.Session
	// 多取一行用来判"还有更多"（不返回 total，D3-A′②）。
	if err := tx.Order("issued_at DESC, id DESC").Limit(q.Limit + 1).Find(&rows).Error; err != nil {
		return nil, MapError(err)
	}

	page := &SessionPage{Items: rows}
	if len(rows) > q.Limit {
		page.Items = rows[:q.Limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeSessionCursor(last.IssuedAt, last.ID)
	}
	if page.Items == nil {
		page.Items = []model.Session{}
	}
	return page, nil
}

// sessionCursor 是列表游标的解码结果（**不导出**：游标对调用方不透明）。
type sessionCursor struct {
	IssuedAt time.Time
	ID       uuid.UUID
}

// encodeSessionCursor 把 (issued_at, id) 编成不透明串。
//
// 用 base64url 而不是明文时间戳：D3-A′③ 要求游标不透明，且**列表游标与导出/Agent 游标
// 不得互喂**——不透明是那条约束唯一可执行的形态。
func encodeSessionCursor(issuedAt time.Time, id uuid.UUID) string {
	raw := fmt.Sprintf("%d|%s", issuedAt.UTC().UnixNano(), id.String())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeSessionCursor 解析游标；空串 = 第一页。
func decodeSessionCursor(cursor string) (*sessionCursor, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		// 游标非法是**请求参数问题**（E1/400），不是内部错误。
		return nil, fmt.Errorf("%w: session cursor is not valid base64url", ErrInvalidCursor)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: session cursor shape", ErrInvalidCursor)
	}
	var nanos int64
	if _, err := fmt.Sscanf(parts[0], "%d", &nanos); err != nil {
		return nil, fmt.Errorf("%w: session cursor timestamp", ErrInvalidCursor)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: session cursor id", ErrInvalidCursor)
	}
	return &sessionCursor{IssuedAt: time.Unix(0, nanos).UTC(), ID: id}, nil
}

// ErrInvalidCursor 表示游标解不开（对外 `INVALID_PARAMETER`，E1/400）。
var ErrInvalidCursor = errors.New("store: invalid cursor")

// RevokeByID 撤销一条会话（条件 UPDATE；`status` 与 `revoked_at` 同语句写，满足 DB CHECK）。
//
// 返回 0 = 该会话不在 active（已撤销/已过期/不存在），调用方按语义决定回哪个码。
func (r *SessionRepo) RevokeByID(ctx context.Context, id uuid.UUID, reason string, at time.Time) (int64, error) {
	if id == uuid.Nil {
		return 0, errors.New("store: empty session id")
	}
	if strings.TrimSpace(reason) == "" {
		// 撤销原因是审计事实的一部分：空原因等于"不知道为什么踢的"。
		return 0, errors.New("store: revoke requires reason")
	}
	res := r.db.WithContext(ctx).Model(&model.Session{}).
		Where("id = ? AND status = ?", id, model.SessionStatusActive).
		Updates(map[string]any{
			"status":        model.SessionStatusRevoked,
			"revoked_at":    at.UTC(),
			"revoke_reason": reason,
		})
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// RevokeAllByUser 撤销某用户的全部活跃会话（管理面 `DELETE /api/v1/sessions?user_id=`）。
func (r *SessionRepo) RevokeAllByUser(ctx context.Context, userID uuid.UUID, reason string, at time.Time) (int64, error) {
	if userID == uuid.Nil {
		return 0, errors.New("store: empty user id")
	}
	if strings.TrimSpace(reason) == "" {
		return 0, errors.New("store: revoke requires reason")
	}
	res := r.db.WithContext(ctx).Model(&model.Session{}).
		Where("user_id = ? AND status = ?", userID, model.SessionStatusActive).
		Updates(map[string]any{
			"status":        model.SessionStatusRevoked,
			"revoked_at":    at.UTC(),
			"revoke_reason": reason,
		})
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// RevokeOthers 撤销某用户除 `keepID` 之外的全部活跃会话（改密/找回后"踢掉其它设备"）。
func (r *SessionRepo) RevokeOthers(ctx context.Context, userID, keepID uuid.UUID, reason string, at time.Time) (int64, error) {
	if userID == uuid.Nil {
		return 0, errors.New("store: empty user id")
	}
	if strings.TrimSpace(reason) == "" {
		return 0, errors.New("store: revoke requires reason")
	}
	res := r.db.WithContext(ctx).Model(&model.Session{}).
		Where("user_id = ? AND status = ? AND id <> ?", userID, model.SessionStatusActive, keepID).
		Updates(map[string]any{
			"status":        model.SessionStatusRevoked,
			"revoked_at":    at.UTC(),
			"revoke_reason": reason,
		})
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// CountActiveByUserClient 数某用户在某业务系统的活跃会话数（并发上限判定用）。
//
// 命中 `sessions_user_client_active_idx`（`(user_id, client_id, issued_at DESC) WHERE status='active'`）。
func (r *SessionRepo) CountActiveByUserClient(ctx context.Context, userID uuid.UUID, clientID string) (int64, error) {
	if userID == uuid.Nil || strings.TrimSpace(clientID) == "" {
		return 0, errors.New("store: count requires user_id and client_id")
	}
	var n int64
	err := r.db.WithContext(ctx).Model(&model.Session{}).
		Where("user_id = ? AND client_id = ? AND status = ?", userID, clientID, model.SessionStatusActive).
		Count(&n).Error
	if err != nil {
		return 0, MapError(err)
	}
	return n, nil
}

// EvictOldestActive 挤掉最旧的一条活跃会话（`concurrent_overflow_action ∈ evict_oldest*`）。
//
// 单语句 CTE：`FOR UPDATE SKIP LOCKED` 只锁被挤掉的那一行，且与 UPDATE 同语句——
// 既避免"先查后改"的并发双踢，也不会出现"拿着会话锁去等流锁"（锁序 sessions → flows 仍成立）。
// 返回被挤掉的会话 id；0 行 = 没有可挤的会话（并发已自然回落）。
func (r *SessionRepo) EvictOldestActive(ctx context.Context, userID uuid.UUID, clientID, reason string, at time.Time) (uuid.UUID, error) {
	if userID == uuid.Nil || strings.TrimSpace(clientID) == "" {
		return uuid.Nil, errors.New("store: evict requires user_id and client_id")
	}
	if strings.TrimSpace(reason) == "" {
		return uuid.Nil, errors.New("store: revoke requires reason")
	}
	var ids []uuid.UUID
	err := r.db.WithContext(ctx).Raw(`
		WITH victim AS (
			SELECT id FROM sessions
			 WHERE user_id = ? AND client_id = ? AND status = ?
			 ORDER BY issued_at ASC, id ASC
			 LIMIT 1
			 FOR UPDATE SKIP LOCKED
		)
		UPDATE sessions s
		   SET status = ?, revoked_at = ?, revoke_reason = ?
		  FROM victim v
		 WHERE s.id = v.id
		RETURNING s.id`,
		userID, clientID, model.SessionStatusActive,
		model.SessionStatusRevoked, at.UTC(), reason).Scan(&ids).Error
	if err != nil {
		return uuid.Nil, MapError(err)
	}
	if len(ids) == 0 {
		return uuid.Nil, nil
	}
	return ids[0], nil
}

// TouchLastSeen 续期：写 `last_seen_at` 并按空闲超时滚动 `idle_expires_at`。
//
// 条件里带 `status='active'`：已撤销的会话不能被"顺手续活"。
// **不滚绝对超时**：`expires_at` 是重认证间隔，滚动它等于取消强制重认证。
func (r *SessionRepo) TouchLastSeen(ctx context.Context, id uuid.UUID, at, idleExpiresAt time.Time) (int64, error) {
	if id == uuid.Nil {
		return 0, errors.New("store: empty session id")
	}
	res := r.db.WithContext(ctx).Model(&model.Session{}).
		Where("id = ? AND status = ?", id, model.SessionStatusActive).
		Updates(map[string]any{
			"last_seen_at":    at.UTC(),
			"idle_expires_at": idleExpiresAt.UTC(),
		})
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// ExpireStale 把超过空闲超时或绝对超时的活跃会话置 `expired`（惰性过期，由调用方定时/按需触发）。
//
// 只置状态、不删行：会话记录是审计证据，撤销与过期都保留。
func (r *SessionRepo) ExpireStale(ctx context.Context, now time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.Session{}).
		Where("status = ? AND (idle_expires_at <= ? OR expires_at <= ?)",
			model.SessionStatusActive, now.UTC(), now.UTC()).
		Update("status", model.SessionStatusExpired)
	if res.Error != nil {
		return 0, MapError(res.Error)
	}
	return res.RowsAffected, nil
}

// SessionPolicy 是会话相关的**只读策略快照**（`login_policies` 的 B 组 + D 组）。
//
// 与 `ClientRegistrationPolicy` 同一条原则：返回值只窄到会话需要的那几项，
// 调用方拿不到"顺手改策略"的能力。**代码里不垫任何默认值**：没有策略行就是
// `ErrPolicyNotConfigured`（配置缺陷要显式暴露，不是拿 30/720 顶着）。
type SessionPolicy struct {
	ClientID string
	Tier     string
	// IdleTimeout / AbsoluteTimeout 由 `session_idle_timeout_min` / `session_absolute_timeout_min` 换算。
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	// MaxConcurrent 为 nil 表示"不限"（DB 列可空，NULL=不限，不是 0）。
	MaxConcurrent *int
	// OverflowAction deny_new | evict_oldest | evict_oldest_alert（DB CHECK 三值）。
	OverflowAction string
	// SSOLogoutScope current_client | all_clients（单点登出的级联范围）。
	SSOLogoutScope string
}

// LoadSessionPolicy 读某业务系统的会话策略：**client_override 优先，其次同档位 tier_default**。
//
// 两次查询而不是一次 UNION：覆盖行与默认行的列完全一致，分开写让"优先级"这件事在代码里
// 只有一处（先查覆盖、命中即返回），也让单测可以逐分支钉住。
func (r *SessionRepo) LoadSessionPolicy(ctx context.Context, clientID string) (*SessionPolicy, error) {
	if strings.TrimSpace(clientID) == "" {
		return nil, errors.New("store: empty client id")
	}

	var c struct {
		ClientID string `gorm:"column:client_id"`
		Tier     string `gorm:"column:tier"`
		Status   string `gorm:"column:status"`
	}
	if res := r.db.WithContext(ctx).Raw(
		`SELECT client_id, tier, status FROM clients WHERE client_id = ?`, clientID).Scan(&c); res.Error != nil {
		return nil, MapError(res.Error)
	}
	if c.ClientID == "" {
		return nil, ErrClientNotFound
	}
	if c.Status != "active" {
		return nil, ErrClientDisabled
	}
	if c.Tier != "L1" && c.Tier != "L2" && c.Tier != "L3" {
		return nil, ErrUnknownTier
	}

	type policyRow struct {
		IdleMin        int    `gorm:"column:session_idle_timeout_min"`
		AbsoluteMin    int    `gorm:"column:session_absolute_timeout_min"`
		MaxConcurrent  *int   `gorm:"column:max_concurrent_sessions"`
		OverflowAction string `gorm:"column:concurrent_overflow_action"`
		SSOLogoutScope string `gorm:"column:sso_logout_scope"`
	}
	const cols = `session_idle_timeout_min, session_absolute_timeout_min,
	              max_concurrent_sessions, concurrent_overflow_action, sso_logout_scope`

	var row policyRow
	res := r.db.WithContext(ctx).Raw(
		`SELECT `+cols+` FROM login_policies WHERE scope = 'client_override' AND client_id = ?`,
		clientID).Scan(&row)
	if res.Error != nil {
		return nil, MapError(res.Error)
	}
	if res.RowsAffected == 0 {
		res = r.db.WithContext(ctx).Raw(
			`SELECT `+cols+` FROM login_policies WHERE scope = 'tier_default' AND tier = ?`,
			c.Tier).Scan(&row)
		if res.Error != nil {
			return nil, MapError(res.Error)
		}
		if res.RowsAffected == 0 {
			return nil, ErrPolicyNotConfigured
		}
	}

	return &SessionPolicy{
		ClientID:        c.ClientID,
		Tier:            c.Tier,
		IdleTimeout:     time.Duration(row.IdleMin) * time.Minute,
		AbsoluteTimeout: time.Duration(row.AbsoluteMin) * time.Minute,
		MaxConcurrent:   row.MaxConcurrent,
		OverflowAction:  row.OverflowAction,
		SSOLogoutScope:  row.SSOLogoutScope,
	}, nil
}
