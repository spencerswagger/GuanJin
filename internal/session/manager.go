package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是 S7 的**会话生命周期 + 实时撤销**（m1-slices §4-S7）。
//
// 职责边界（与 Core 的 S6 协议面、Edge 的 S8 数据面严格分工）：
//   - 本包**不签发令牌、不解析令牌**（S6 的事），也不做转发与断言注入（S8 的事）；
//   - 本包只回答两个问题：**这条会话现在还能不能用**（`Validate`）、**撤销了没有**（`Revoke*`）；
//   - **会话创建归 S6**（spencer 裁定 2026-11-04）：建会话、双超时取值、并发上限与 `sid` 落点
//     都在协议面（S6）。本包只做 S7 的"创建之后"——续期（`Touch`）、过期（`ExpireStale`）、
//     撤销/踢出（`Revoke*`/`Logout`）与管理面查询，**不实现第二套创建逻辑**；
//   - 会话的权威状态在 `sessions` 表（索引 §2「挑战落库、Redis 只承载限流计数与热缓存」）。
//     **撤销不走 Redis**：Redis 在本仓既有约定里只放限流计数与热缓存（D3-D / 索引 §2），
//     撤销事实必须落库，否则"Redis 一抖就把已撤销的会话放回来"就是降级放行。
//
// 实时性（AC-SESS-01 的"无缓存窗口"）由结构保证：`Validate` 每次都读库，
// 本包**不缓存会话状态**——要加缓存必须先有一条判据允许缓存窗口，目前没有。

// SessionStore 是本包需要的会话存储能力面（实现是 `*store.SessionRepo`）。
//
// 用窄接口而不是直接吃 `*store.SessionRepo`：撤销级联、并发上限这些判定要在**不建库**
// 的前提下被测到（单测用内存假实现），真库只用来验 SQL 形状与约束。
type SessionStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Session, error)
	ListByUser(ctx context.Context, userID uuid.UUID, limit int) ([]model.Session, error)
	ActiveIDsByUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
	List(ctx context.Context, q store.SessionQuery) (*store.SessionPage, error)
	RevokeByID(ctx context.Context, id uuid.UUID, reason string, at time.Time) (int64, error)
	RevokeAllByUser(ctx context.Context, userID uuid.UUID, reason string, at time.Time) (int64, error)
	RevokeOthers(ctx context.Context, userID, keepID uuid.UUID, reason string, at time.Time) (int64, error)
	TouchLastSeen(ctx context.Context, id uuid.UUID, at, idleExpiresAt time.Time) (int64, error)
	LoadSessionPolicy(ctx context.Context, clientID string) (*store.SessionPolicy, error)
	ClientNames(ctx context.Context, clientIDs []string) (map[string]string, error)
	UserExists(ctx context.Context, id uuid.UUID) (bool, error)
}

// FlowAbandoner 是 D5 的**窄接口**：会话撤销时把该会话的活跃流置 `abandoned`。
//
// 为什么用接口而不是直接调 `*store.FlowRepo`：`session ↔ flow` 直接互相 import 会成环，
// D5 的裁定就是"用窄接口把环切断"。实现由装配点注入（`store.NewFlowRepo(db).AbandonBySession`）。
type FlowAbandoner interface {
	AbandonBySession(ctx context.Context, sessionID uuid.UUID) (int64, error)
}

// EventWriter 是 `auth_events` 的只追加写入面（实现是 `*store.AuthEventRepo`）。
type EventWriter interface {
	Append(ctx context.Context, e *model.AuthEvent) error
}

// RevokeReason 是 `sessions.revoke_reason` 的取值集合。
//
// 前六个来自数据模型 §3.5 的列注释（logout / logout_all / admin / risk / role_change /
// password_change）；`concurrent_limit` 是并发上限"挤掉最旧"产生的撤销——**列上没有 CHECK**，
// 但它是新取值，已在 S7 交付说明里作为待追认项列出（不隐式混进 `risk`：混进去会让
// "被挤下线"看起来像风控命中，排障时会把两条完全不同的原因看成一件事）。
type RevokeReason string

// 撤销原因取值。
const (
	ReasonLogout         RevokeReason = "logout"
	ReasonLogoutAll      RevokeReason = "logout_all"
	ReasonAdmin          RevokeReason = "admin"
	ReasonRisk           RevokeReason = "risk"
	ReasonRoleChange     RevokeReason = "role_change"
	ReasonPasswordChange RevokeReason = "password_change"
	// ReasonConcurrentLimit 由 S6 的建会话路径使用（并发上限"挤掉最旧"）：
	// 按 S6/S7 边界，**创建会话归 S6**，本包只承接"撤销之后"的生命周期。
	ReasonConcurrentLimit RevokeReason = "concurrent_limit"
)

// EventType 是写入 `auth_events.event_type` 的取值（开放集合，DB 无 CHECK）。
//
// 与撤销原因分开：原因回答"为什么踢"，事件类型回答"这是一次什么动作"（登出 / 强制下线）。
const (
	EventLogout = "logout"
	EventRevoke = "revoke"
)

// Manager 是会话生命周期的唯一入口（无本地状态，多副本行为一致）。
type Manager struct {
	store  SessionStore
	flows  FlowAbandoner
	events EventWriter
	logger *slog.Logger
	// now 可注入：超时判定与"挤掉最旧"的排序都依赖时钟，测试必须能钉住时间。
	now func() time.Time
}

// Options 是 Manager 的可选装配项。
type Options struct {
	// Now 为空时用 time.Now（生产路径）；测试注入固定时钟。
	Now func() time.Time
	// Logger 为空时用 slog.Default。
	Logger *slog.Logger
}

// NewManager 装配会话管理器；缺依赖即返回错误（装配期 fail-fast，不留 nil 分支）。
func NewManager(st SessionStore, flows FlowAbandoner, events EventWriter, opt Options) (*Manager, error) {
	switch {
	case st == nil:
		return nil, fmt.Errorf("%w: session store", ErrNilDependency)
	case flows == nil:
		return nil, fmt.Errorf("%w: flow abandoner", ErrNilDependency)
	case events == nil:
		return nil, fmt.Errorf("%w: auth event writer", ErrNilDependency)
	}
	m := &Manager{store: st, flows: flows, events: events, now: opt.Now, logger: opt.Logger}
	if m.now == nil {
		m.now = time.Now
	}
	if m.logger == nil {
		m.logger = slog.Default()
	}
	return m, nil
}

// Validate 判定一条会话现在能不能用（数据面 S8 的准入依据）。
//
// 返回的哨兵直接对应 D3-E 的双码分工：已撤销 → `SESSION_REVOKED`(403)；
// 过期（空闲/绝对）→ `TOKEN_EXPIRED`(401)。**只读、不写**：把"顺手置过期"塞进读路径，
// 会让一次查询变成写事务，也会在只读副本上失败——过期落库由 `ExpireStale` 负责。
func (m *Manager) Validate(ctx context.Context, id uuid.UUID) (*model.Session, error) {
	if id == uuid.Nil {
		return nil, ErrSessionNotFound
	}
	s, err := m.store.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	switch s.Status {
	case model.SessionStatusRevoked:
		return nil, ErrSessionRevoked
	case model.SessionStatusExpired:
		return nil, ErrSessionExpired
	}
	now := m.now().UTC()
	if !now.Before(s.ExpiresAt) || !now.Before(s.IdleExpiresAt) {
		return nil, ErrSessionExpired
	}
	return s, nil
}

// Touch 续期（每次成功访问后调用）：只滚空闲超时，**不滚绝对超时**。
//
// 滚动值被绝对超时截断（DB CHECK `sessions_time_order` 要求 `idle <= expires`）——
// 这是表结构约束的直接结果，不是"实现自己定的策略"。
func (m *Manager) Touch(ctx context.Context, id uuid.UUID) error {
	s, err := m.store.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return ErrSessionNotFound
		}
		return err
	}
	policy, err := m.store.LoadSessionPolicy(ctx, s.ClientID)
	if err != nil {
		return err
	}
	now := m.now().UTC()
	idle := now.Add(policy.IdleTimeout)
	if idle.After(s.ExpiresAt) {
		idle = s.ExpiresAt
	}
	_, err = m.store.TouchLastSeen(ctx, id, now, idle)
	return err
}

// RevokeInput 是一次撤销动作的入参。
type RevokeInput struct {
	SessionID uuid.UUID
	// Reason 为什么撤销（写 `sessions.revoke_reason` 与 `auth_events.reason`）。
	Reason RevokeReason
	// EventType 动作类型（`logout` / `revoke`）。
	EventType string
	TraceID   *string
}

// Revoke 撤销一条会话：改状态 → 作废该会话的活跃流 → 写 `auth_events`。
//
// **幂等**（§5.6：DELETE 天然幂等）：会话早已不在 active 时按成功返回，
// 不报错、不重复写事件——重放一次"踢下线"不该产生第二条审计事实。
func (m *Manager) Revoke(ctx context.Context, in RevokeInput) error {
	if in.SessionID == uuid.Nil {
		return fmt.Errorf("%w: session_id is required", ErrInvalidInput)
	}
	if strings.TrimSpace(string(in.Reason)) == "" {
		return fmt.Errorf("%w: reason is required", ErrInvalidInput)
	}
	if in.EventType == "" {
		return fmt.Errorf("%w: event_type is required", ErrInvalidInput)
	}
	now := m.now().UTC()

	rows, err := m.store.RevokeByID(ctx, in.SessionID, string(in.Reason), now)
	if err != nil {
		return err
	}
	if rows == 0 {
		// 没改动：要么不存在（404），要么本来就不在 active（幂等成功）。
		if _, err := m.store.GetByID(ctx, in.SessionID); err != nil {
			if errors.Is(err, store.ErrSessionNotFound) {
				return ErrSessionNotFound
			}
			return err
		}
		return nil
	}
	m.cascade(ctx, []uuid.UUID{in.SessionID}, in.Reason, in.EventType, in.TraceID)
	return nil
}

// RevokeAllByUser 撤销某用户的全部活跃会话（管理面 `DELETE /api/v1/sessions?user_id=`）。
//
// 返回撤销条数；**不判断用户是否存在**（那是 handler/管理面的事，本包只做会话侧）。
func (m *Manager) RevokeAllByUser(ctx context.Context, userID uuid.UUID, reason RevokeReason,
	eventType string, traceID *string) (int64, error) {
	if userID == uuid.Nil {
		return 0, fmt.Errorf("%w: user_id is required", ErrInvalidInput)
	}
	ids, err := m.store.ActiveIDsByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	now := m.now().UTC()
	rows, err := m.store.RevokeAllByUser(ctx, userID, string(reason), now)
	if err != nil {
		return 0, err
	}
	if rows > 0 {
		m.cascade(ctx, ids, reason, eventType, traceID)
	}
	return rows, nil
}

// RevokeOthers 撤销某用户除 `keepID` 之外的活跃会话（改密/找回后的"踢掉其它设备"）。
func (m *Manager) RevokeOthers(ctx context.Context, userID, keepID uuid.UUID, reason RevokeReason,
	eventType string, traceID *string) (int64, error) {
	if userID == uuid.Nil {
		return 0, fmt.Errorf("%w: user_id is required", ErrInvalidInput)
	}
	ids, err := m.store.ActiveIDsByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	now := m.now().UTC()
	rows, err := m.store.RevokeOthers(ctx, userID, keepID, string(reason), now)
	if err != nil {
		return 0, err
	}
	if rows > 0 {
		victims := make([]uuid.UUID, 0, len(ids))
		for _, id := range ids {
			if id != keepID {
				victims = append(victims, id)
			}
		}
		m.cascade(ctx, victims, reason, eventType, traceID)
	}
	return rows, nil
}

// Logout 是单点登出（`POST /logout` 的服务侧）：按策略的 `sso_logout_scope` 决定级联范围。
//
// `current_client` → 只撤销当前会话；`all_clients` → 撤销该用户全部活跃会话（SLO）。
// HTTP 入口不在本切片：契约要求 `id_token_hint`，那是 S6 的令牌解析面（见交付说明）。
func (m *Manager) Logout(ctx context.Context, sessionID uuid.UUID, traceID *string) error {
	s, err := m.store.GetByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return ErrSessionNotFound
		}
		return err
	}
	policy, err := m.store.LoadSessionPolicy(ctx, s.ClientID)
	if err != nil {
		return err
	}
	if policy.SSOLogoutScope == "all_clients" {
		_, err = m.RevokeAllByUser(ctx, s.UserID, ReasonLogoutAll, EventLogout, traceID)
		return err
	}
	return m.Revoke(ctx, RevokeInput{
		SessionID: sessionID,
		Reason:    ReasonLogout,
		EventType: EventLogout,
		TraceID:   traceID,
	})
}

// ListAll 是管理面列表（跨用户、游标分页，接口 §3.8 sessions 段）。
//
// 与自助面的 `ListByUser` 分开：管理面要按 `user_id`/`client_id`/`active` 过滤，
// 且必须走游标（D3-A′），两条查询的索引与形状都不同。
func (m *Manager) ListAll(ctx context.Context, q store.SessionQuery) (*store.SessionPage, error) {
	return m.store.List(ctx, q)
}

// UserExists 判断用户是否存在（管理面 `DELETE /api/v1/sessions?user_id=` 的 `USER_NOT_FOUND`）。
func (m *Manager) UserExists(ctx context.Context, id uuid.UUID) (bool, error) {
	if id == uuid.Nil {
		return false, nil
	}
	return m.store.UserExists(ctx, id)
}

// Get 读一条会话（**不做有效性判定**：给"归属校验/展示"这类只读场景用）。
//
// 与 `Validate` 分开：`Validate` 会按状态回撤销/过期的哨兵，而"这条会话是谁的"
// 在会话已撤销后仍然必须回答得出来（否则用户踢一个已过期的会话会拿到 404 而不是 204）。
func (m *Manager) Get(ctx context.Context, id uuid.UUID) (*model.Session, error) {
	if id == uuid.Nil {
		return nil, ErrSessionNotFound
	}
	s, err := m.store.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}
	return s, nil
}

// ClientNames 批量取业务系统展示名（列表响应 `client.name` 用）。
func (m *Manager) ClientNames(ctx context.Context, clientIDs []string) (map[string]string, error) {
	return m.store.ClientNames(ctx, clientIDs)
}

// ListByUser 取某用户的会话列表（自助面 `GET /api/v1/me/sessions`）。
func (m *Manager) ListByUser(ctx context.Context, userID uuid.UUID, limit int) ([]model.Session, error) {
	if userID == uuid.Nil {
		return nil, fmt.Errorf("%w: user_id is required", ErrInvalidInput)
	}
	return m.store.ListByUser(ctx, userID, limit)
}

// ExpireStale 把超时的活跃会话置 `expired`（惰性过期；由定时器或运维动作触发）。
func (m *Manager) ExpireStale(ctx context.Context) (int64, error) {
	expirer, ok := m.store.(interface {
		ExpireStale(ctx context.Context, now time.Time) (int64, error)
	})
	if !ok {
		return 0, fmt.Errorf("%w: session store does not support ExpireStale", ErrNilDependency)
	}
	return expirer.ExpireStale(ctx, m.now().UTC())
}

// cascade 是撤销后的**统一副作用**：作废活跃流 + 写认证事件。
//
// 流作废失败**不回滚会话撤销**：撤销是安全事实，必须先生效；流的 `abandoned` 置位失败
// 只影响"能不能继续补步"，而 `Validate` 已经会拒掉该会话。事件写失败同理（只留日志）——
// 让撤销因为审计写失败而回滚，等于用可用性换审计，方向反了。
func (m *Manager) cascade(ctx context.Context, ids []uuid.UUID, reason RevokeReason,
	eventType string, traceID *string) {
	for _, id := range ids {
		if _, err := m.flows.AbandonBySession(ctx, id); err != nil {
			m.logger.Warn("session: abandon flows failed", "session_id", id, "error", err)
		}
		s, err := m.store.GetByID(ctx, id)
		if err != nil {
			m.logger.Warn("session: load revoked session for event failed", "session_id", id, "error", err)
			continue
		}
		m.appendEvent(ctx, id, s.UserID, s.ClientID, eventType, string(reason), traceID, &s.ACR)
	}
}

// appendEvent 写一条 `auth_events`（失败只留日志，理由见 cascade）。
func (m *Manager) appendEvent(ctx context.Context, sessionID, userID uuid.UUID, clientID,
	eventType, reason string, traceID *string, acr *string) {
	reasonCopy := reason
	sid := sessionID
	uid := userID
	cid := clientID
	e := &model.AuthEvent{
		UserID:     &uid,
		SessionID:  &sid,
		ClientID:   &cid,
		EventType:  eventType,
		Result:     "success",
		Reason:     &reasonCopy,
		ACR:        acr,
		AMR:        model.StringArray{},
		TraceID:    traceID,
		OccurredAt: m.now().UTC(),
	}
	if err := m.events.Append(ctx, e); err != nil {
		m.logger.Warn("session: append auth event failed",
			"session_id", sessionID, "event_type", eventType, "error", err)
	}
}
