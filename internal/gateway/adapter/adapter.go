// Package adapter 把 `internal/store` 的仓储折成 `internal/gateway` 要的接口。
//
// 为什么单独一层（而不是把 SQL 写进 `gateway`）：网关包只回答"这条请求该不该过、以什么身份过"，
// 它不该知道 gorm、表名和列名；同时装配层（`cmd/guanjingw`）也不该成为唯一能被测试的适配点——
// 适配里最容易抄错的是**哨兵翻译**（`store.ErrSessionNotFound` → `gateway.ErrSessionNotFound`
// 等），翻错一个就会让"配置缺陷"变成"依赖挂了"（500 → 503），运维会去查库而问题在配置。
//
// 所以：适配器只持**窄接口**（每个仓储只暴露它真正用到的那一个方法），
// 于是翻译逻辑可以用假仓储做纯单测，SQL 形状由 `repo_*_db_test.go` 的真库用例保证。
package adapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/gateway"
	"github.com/spencerswagger/guanjin/internal/protocol"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 仓储窄接口（每个只声明本包用到的方法）。
type (
	// SessionRepo 读一条会话（S7 的 `SessionOpsRepo`）。
	SessionRepo interface {
		GetByID(ctx context.Context, id uuid.UUID) (*model.Session, error)
	}
	// PolicyRepo 读准入门槛（S8 的 `ClientPolicyRepo.LoadAdmissionPolicy`，D16⑤）。
	PolicyRepo interface {
		LoadAdmissionPolicy(ctx context.Context, clientID string) (*store.AdmissionPolicy, error)
	}
	// RoleRepo 读有效角色。
	RoleRepo interface {
		RolesForUserClient(ctx context.Context, userID uuid.UUID, clientID string) ([]string, error)
	}
	// UserRepo 读用户行（断言 `uname` 取 `username`）。
	UserRepo interface {
		GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
	}
	// VisitRepo 写 `user_visited_clients`（幂等 upsert）。
	VisitRepo interface {
		RecordVisit(ctx context.Context, userID uuid.UUID, clientID, tier string, at time.Time) error
	}
	// FlowRepo 读活跃流（`details.flow` 用，只读）。
	FlowRepo interface {
		FindActiveByUserClient(ctx context.Context, userID uuid.UUID, clientID string) (*model.Flow, error)
	}
	// SessionStatusRepo 读会话状态（协议面 `CallerResolver` 用）。
	SessionStatusRepo interface {
		SessionStatus(ctx context.Context, sid string) (string, error)
	}
)

// Sessions 实现 `gateway.SessionReader`（每请求现读，零缓存窗口）。
type Sessions struct{ repo SessionRepo }

// NewSessions 用真仓储构造（装配层用）。
func NewSessions(db *gorm.DB) *Sessions { return &Sessions{repo: store.NewSessionOpsRepo(db)} }

// NewSessionsWithRepo 用任意窄接口构造（单测用）。
func NewSessionsWithRepo(repo SessionRepo) *Sessions { return &Sessions{repo: repo} }

// LoadSession 读会话快照；不存在时返回 `gateway.ErrSessionNotFound`（对外与"已撤销"同形）。
func (s *Sessions) LoadSession(ctx context.Context, sid string) (gateway.SessionSnapshot, error) {
	id, err := uuid.Parse(sid)
	if err != nil {
		// 非法 `sid` 只能是伪造/数据缺陷：按"会话不存在"处理，不给枚举留口子。
		return gateway.SessionSnapshot{}, gateway.ErrSessionNotFound
	}
	row, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return gateway.SessionSnapshot{}, gateway.ErrSessionNotFound
		}
		return gateway.SessionSnapshot{}, err
	}
	return gateway.SessionSnapshot{
		ID:            row.ID.String(),
		UserID:        row.UserID.String(),
		ClientID:      row.ClientID,
		ACR:           row.ACR,
		AMR:           append([]string(nil), row.AMR...),
		Status:        row.Status,
		ExpiresAt:     row.ExpiresAt,
		IdleExpiresAt: row.IdleExpiresAt,
	}, nil
}

// Policies 实现 `gateway.AdmissionPolicyReader`（D16② 的唯一取法）。
type Policies struct{ repo PolicyRepo }

// NewPolicies 用真仓储构造。
func NewPolicies(db *gorm.DB) *Policies { return &Policies{repo: store.NewClientPolicyRepo(db)} }

// NewPoliciesWithRepo 用任意窄接口构造（单测用）。
func NewPoliciesWithRepo(repo PolicyRepo) *Policies { return &Policies{repo: repo} }

// LoadAdmissionPolicy 读门槛，并**把 store 的哨兵翻成 gateway 的哨兵**。
func (p *Policies) LoadAdmissionPolicy(ctx context.Context, clientID string) (gateway.AdmissionPolicy, error) {
	policy, err := p.repo.LoadAdmissionPolicy(ctx, clientID)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrClientNotFound):
		return gateway.AdmissionPolicy{}, gateway.ErrClientNotFound
	case errors.Is(err, store.ErrClientDisabled):
		return gateway.AdmissionPolicy{}, gateway.ErrClientDisabled
	case errors.Is(err, store.ErrPolicyNotConfigured):
		return gateway.AdmissionPolicy{}, gateway.ErrPolicyNotConfigured
	default:
		return gateway.AdmissionPolicy{}, err
	}
	return gateway.AdmissionPolicy{
		ClientID:        policy.ClientID,
		ClientName:      policy.ClientName,
		Tier:            policy.Tier,
		MinACR:          policy.MinACR,
		DisabledFactors: append([]string(nil), policy.DisabledFactors...),
	}, nil
}

// Roles 实现 `gateway.RoleReader`。
type Roles struct{ repo RoleRepo }

// NewRoles 用真仓储构造。
func NewRoles(db *gorm.DB) *Roles { return &Roles{repo: store.NewRoleBindingRepo(db)} }

// NewRolesWithRepo 用任意窄接口构造（单测用）。
func NewRolesWithRepo(repo RoleRepo) *Roles { return &Roles{repo: repo} }

// RolesForUserClient 读有效角色（空集合 = 空切片）。
func (r *Roles) RolesForUserClient(ctx context.Context, userID, clientID string) ([]string, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("bad user id %q: %w", userID, err)
	}
	return r.repo.RolesForUserClient(ctx, uid, clientID)
}

// Users 实现 `gateway.UserReader`（断言 `uname` 取 `users.username`，不取 display_name）。
type Users struct{ repo UserRepo }

// NewUsers 用真仓储构造。
func NewUsers(db *gorm.DB) *Users { return &Users{repo: store.NewUserRepo(db)} }

// NewUsersWithRepo 用任意窄接口构造（单测用）。
func NewUsersWithRepo(repo UserRepo) *Users { return &Users{repo: repo} }

// Username 读规范化用户名。
func (u *Users) Username(ctx context.Context, userID string) (string, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return "", fmt.Errorf("bad user id %q: %w", userID, err)
	}
	user, err := u.repo.GetByID(ctx, uid)
	if err != nil {
		return "", err
	}
	return user.Username, nil
}

// Visits 实现 `gateway.VisitRecordReader`（`user_visited_clients` 幂等 upsert）。
type Visits struct{ repo VisitRepo }

// NewVisits 用真仓储构造。
func NewVisits(db *gorm.DB) *Visits { return &Visits{repo: store.NewVisitRepo(db)} }

// NewVisitsWithRepo 用任意窄接口构造（单测用）。
func NewVisitsWithRepo(repo VisitRepo) *Visits { return &Visits{repo: repo} }

// RecordVisit 记一次访问。
func (v *Visits) RecordVisit(ctx context.Context, userID, clientID, tier string) error {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return fmt.Errorf("bad user id %q: %w", userID, err)
	}
	return v.repo.RecordVisit(ctx, uid, clientID, tier, time.Now().UTC())
}

// Flows 实现 `gateway.ActiveFlowReader`（只读，`details.flow` 用）。
type Flows struct{ repo FlowRepo }

// NewFlows 用真仓储构造。
func NewFlows(db *gorm.DB) *Flows { return &Flows{repo: store.NewFlowRepo(db)} }

// NewFlowsWithRepo 用任意窄接口构造（单测用）。
func NewFlowsWithRepo(repo FlowRepo) *Flows { return &Flows{repo: repo} }

// FindActiveFlow 读该用户在该业务系统内的活跃流；没有时返回 `(nil, nil)`（不是错误）。
func (f *Flows) FindActiveFlow(ctx context.Context, userID, clientID string) (*gateway.FlowSnapshot, error) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return nil, fmt.Errorf("bad user id %q: %w", userID, err)
	}
	row, err := f.repo.FindActiveByUserClient(ctx, uid, clientID)
	if err != nil {
		if errors.Is(err, store.ErrFlowNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &gateway.FlowSnapshot{
		ID:          row.ID.String(),
		TargetACR:   row.TargetACR,
		AchievedAMR: append([]string(nil), row.AchievedAMR...),
		ExpiresAt:   row.ExpiresAt,
	}, nil
}

// SessionStatus 把 `store.SessionRepo.SessionStatus` 折成协议面要的形状
// （"没查到"翻成 `protocol.ErrSessionGone`，供 `protocol.CallerResolver` 用）。
type SessionStatus struct{ repo SessionStatusRepo }

// NewSessionStatus 用真仓储构造。
func NewSessionStatus(db *gorm.DB) *SessionStatus {
	return &SessionStatus{repo: store.NewSessionRepo(db)}
}

// NewSessionStatusWithRepo 用任意窄接口构造（单测用）。
func NewSessionStatusWithRepo(repo SessionStatusRepo) *SessionStatus {
	return &SessionStatus{repo: repo}
}

// SessionStatus 读会话状态。
func (s *SessionStatus) SessionStatus(ctx context.Context, sid string) (string, error) {
	st, err := s.repo.SessionStatus(ctx, sid)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return "", protocol.ErrSessionGone
		}
		return "", err
	}
	return st, nil
}

// 编译期断言：六个适配器必须同时满足 gateway 侧的接口（少一个就构建失败）。
var (
	_ gateway.SessionReader         = (*Sessions)(nil)
	_ gateway.AdmissionPolicyReader = (*Policies)(nil)
	_ gateway.RoleReader            = (*Roles)(nil)
	_ gateway.UserReader            = (*Users)(nil)
	_ gateway.VisitRecordReader     = (*Visits)(nil)
	_ gateway.ActiveFlowReader      = (*Flows)(nil)
	_ protocol.SessionStateReader   = (*SessionStatus)(nil)
)
