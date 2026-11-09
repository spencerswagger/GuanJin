package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 本文件是**数据面（S8）专用的三条窄读取/写入**，口径见 decisions.md **D16②**：
//
//	① `LoadAdmissionPolicy`：按 client 读准入门槛（`login_policies.min_acr`）与档位/禁用因子 ——
//	   `client_override` 优先 → `tier_default` 回落 → 两侧都没有 = `ErrPolicyNotConfigured`。
//	② `RecordVisit`：`user_visited_clients` 的幂等 upsert（**只在通过准入之后**写，D16②）。
//	③ 用户名（断言 `uname`）复用既有 `UserRepo.GetByID`，本文件不重复实现。
//
// 为什么单开文件而不是加进 `repo_client_policy.go`：D16⑤ 明确这三条属 S8 自己的窄读取，
// 与注册面/口令策略那条路径（`LoadRegistrationPolicy`）语义不同——前者服务**数据面判准入**，
// 后者服务**注册与口令强度**。混在一个文件里，后来者改一处很容易顺手改到另一处。
//
// 共同纪律：**不垫任何默认值**。`min_acr` 是存储态真值（运维可改），代码里再写一份
// `AAL1` 兜底就是第二套口径（与 D5 对 `flow.ttl_seconds` 的处理同一条原则）。

// AdmissionPolicy 是数据面判准入所需的**全部事实**（一次读齐，避免边判边查）。
type AdmissionPolicy struct {
	ClientID string
	// ClientName 只用于 `details.flow.client.name`（渲染补齐步骤时的展示名）。
	ClientName string
	// Tier 是该业务系统的档位（`clients.tier`），写 `user_visited_clients.tier_at_visit` 用。
	Tier string
	// MinACR 是**唯一门槛真值**：`login_policies.min_acr`（client_override 优先，其次 tier_default）。
	MinACR string
	// DisabledFactors 该业务系统禁用的因子（`login_policies.disabled_factors`）；
	// 禁用因子既不进 `available_factors`，也不计入 `current_acr`（AC-FLOW-04）。
	DisabledFactors []string
}

// LoadAdmissionPolicy 读准入门槛（D16② 的唯一取法）。
//
// 返回的错误语义与 `LoadRegistrationPolicy` 保持一致（同一个 `ErrClientNotFound` /
// `ErrClientDisabled` / `ErrPolicyNotConfigured`），装配层与数据面只认这三种：
//   - 业务系统不存在/已停用 → 配置缺陷（对外 500，不是 4xx：客户端的请求没错）；
//   - 两侧策略行都没有 → 同样是配置缺陷（`ErrPolicyNotConfigured`），**不回退默认 AAL1**。
func (r *ClientPolicyRepo) LoadAdmissionPolicy(ctx context.Context, clientID string) (*AdmissionPolicy, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("store: nil client policy repo")
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil, ErrClientNotFound
	}

	// 只取四列：数据面要的是"是谁、哪档、能不能用"，不把 `secret_hash` 之类的列带出来
	// （数据面是全量 QPS 路径，D16③）。
	var c admissionClient
	err := r.db.WithContext(ctx).Raw(
		`SELECT client_id, name, tier, status FROM clients WHERE client_id = ?`, clientID).Scan(&c).Error
	if err != nil {
		return nil, MapError(err)
	}

	override, err := r.admissionRow(ctx, `scope = 'client_override' AND client_id = ?`, clientID)
	if err != nil {
		return nil, err
	}
	var tierDefault *admissionRow
	if override == nil && c.ClientID != "" && c.Status == "active" {
		// 只有 override 缺失时才查 tier_default：多一次查询换不来正确性。
		tierDefault, err = r.admissionRow(ctx, `scope = 'tier_default' AND tier = ?`, c.Tier)
		if err != nil {
			return nil, err
		}
	}
	return resolveAdmissionPolicy(c, override, tierDefault)
}

// admissionClient 是 `clients` 行的最小投影。
type admissionClient struct {
	ClientID string `gorm:"column:client_id"`
	Name     string `gorm:"column:name"`
	Tier     string `gorm:"column:tier"`
	Status   string `gorm:"column:status"`
}

// resolveAdmissionPolicy 是**纯函数**：给定 client 行与两条候选策略行，算出准入门槛。
//
// 抽成纯函数的理由与 `resolveRegistrationPolicy` 一样：查询无法在没有数据库时验证，
// 而"覆盖优先 → 同档位默认 → 都没有即配置缺陷"这条顺序才是真正容易写错的地方。
func resolveAdmissionPolicy(c admissionClient, override, tierDefault *admissionRow) (*AdmissionPolicy, error) {
	if c.ClientID == "" {
		return nil, ErrClientNotFound
	}
	if c.Status != "active" {
		return nil, ErrClientDisabled
	}
	policy := override
	if policy == nil {
		policy = tierDefault
	}
	if policy == nil {
		// 配置缺陷：既没有 client_override，也没有该档位的 tier_default（000017 应 seed 三档）。
		return nil, ErrPolicyNotConfigured
	}
	return &AdmissionPolicy{
		ClientID:        c.ClientID,
		ClientName:      c.Name,
		Tier:            c.Tier,
		MinACR:          policy.MinACR,
		DisabledFactors: splitFactors(policy.DisabledFactors),
	}, nil
}

// admissionRow 是准入读取的最小行（**只取两列**：门槛与禁用因子）。
//
// 不复用 `policyRow`：那个结构带注册/口令/会话/失败处理十几列，数据面每请求都要读它
// 会白白搬一堆用不上的列（数据面是全量 QPS 路径，D16③ 的容量口径第一）。
type admissionRow struct {
	MinACR          string
	DisabledFactors string
}

func (r *ClientPolicyRepo) admissionRow(ctx context.Context, where string, arg any) (*admissionRow, error) {
	var row admissionRow
	res := r.db.WithContext(ctx).Raw(
		`SELECT min_acr, array_to_string(disabled_factors, ',') AS disabled_factors
		   FROM login_policies WHERE `+where+` LIMIT 1`, arg).Scan(&row)
	if res.Error != nil {
		return nil, MapError(res.Error)
	}
	if res.RowsAffected == 0 || row.MinACR == "" {
		return nil, nil
	}
	return &row, nil
}

// VisitRepo 写 `user_visited_clients`（"该用户用过哪些业务系统"的唯一落点）。
//
// 为什么数据面要写它：单点登出级联范围（`sso_logout_scope=all_clients`）与
// `users.effective_tier` 的物化都依赖这张表；不写就等于"撤销时不知道该通知谁"。
type VisitRepo struct{ db *gorm.DB }

// NewVisitRepo 绑定连接。
func NewVisitRepo(db *gorm.DB) *VisitRepo { return &VisitRepo{db: db} }

// RecordVisit 幂等 upsert：首次插入 `visit_count=1`，再次命中同 `(user_id, client_id)` 则
// `visit_count += 1` 并刷新 `last_seen_at` / `tier_at_visit`。
//
// 用 `ON CONFLICT (user_id, client_id)`（即 `user_visited_clients_uniq`）：
// 这张表的写入在**每请求路径**上，读-改-写会撞并发，upsert 是唯一的正确写法。
// 主键 `id` 由应用层给（D4-A：不写 `DEFAULT uuidv7()`），故冲突时不动 `id`。
func (r *VisitRepo) RecordVisit(ctx context.Context, userID uuid.UUID, clientID, tier string, at time.Time) error {
	if r == nil || r.db == nil {
		return errors.New("store: nil visit repo")
	}
	if userID == uuid.Nil {
		return errors.New("store: visit requires user_id")
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return errors.New("store: visit requires client_id")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("store: generate visit id: %w", err)
	}
	// tier 为空时写 NULL（列 CHECK 允许 NULL，但不允许空串）。
	var tierArg any
	if strings.TrimSpace(tier) != "" {
		tierArg = tier
	}
	return MapError(r.db.WithContext(ctx).Exec(
		`INSERT INTO user_visited_clients (id, user_id, client_id, tier_at_visit, visit_count, first_seen_at, last_seen_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?)
		 ON CONFLICT (user_id, client_id) DO UPDATE
		    SET visit_count   = user_visited_clients.visit_count + 1,
		        last_seen_at  = EXCLUDED.last_seen_at,
		        tier_at_visit = EXCLUDED.tier_at_visit`,
		id, userID, clientID, tierArg, at.UTC(), at.UTC()).Error)
}
