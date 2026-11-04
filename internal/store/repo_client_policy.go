package store

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// ClientRegistrationPolicy 是"某业务系统允许怎样注册"的**只读快照**。
//
// 它把两张表的门槛读成一份结构：`clients.tier`（档位）+ `login_policies`（A/D/E 组的门槛项）。
// 为什么不返回 `*model.Client`：注册只需要这几个字段，返回值越窄，调用方越不可能顺手
// 拿去改（注册流程不该有写 clients 的能力）。
type ClientRegistrationPolicy struct {
	ClientID string
	Tier     string
	// SelfRegistrationAllowed 来自 login_policies（客户端覆盖优先，其次档位默认）。
	SelfRegistrationAllowed bool
	// PasswordMinLength / PasswordCharClasses / PasswordHistoryCount / PasswordMaxAgeDays
	// 是 `credential.Policy` 的输入（口令策略按**目标业务系统的档位**算）。
	PasswordMinLength    int
	PasswordCharClasses  int
	PasswordHistoryCount int
	PasswordMaxAgeDays   int
	// DisabledFactors 该业务系统禁用的因子（注册可用因子 = 全量 − 该集合）。
	DisabledFactors []string
}

var (
	// ErrClientNotFound 表示业务系统不存在（`INVALID_CLIENT`，E1/400）。
	ErrClientNotFound = errors.New("store: client not found")
	// ErrClientDisabled 表示业务系统已停用（不可注册/登录）。
	ErrClientDisabled = errors.New("store: client is disabled")
	// ErrPolicyNotConfigured 表示既没有 client_override 也没有该档位的 tier_default 策略行。
	//
	// **不返回"默认值"**：那会把 12/3/5/90 抄进代码，变成第二套口径（D5 对 flow.ttl_seconds
	// 的处理同一条原则）。这是配置缺陷，必须显式报出来 —— 三档 tier_default 由
	// `migrations/000017` seed（D13），启动期再由 `VerifyTierDefaults` 兜一道（缺失即不启动）。
	ErrPolicyNotConfigured = errors.New("store: login policy not configured for client")
	// ErrUnknownTier 表示 `clients.tier` 不在 L1/L2/L3（DB CHECK 本应拦住，属 schema 漂移）。
	ErrUnknownTier = errors.New("store: unknown client tier")
)

// ClientPolicyRepo 读 `clients` + `login_policies`（只读；注册准入与口令策略的唯一出处）。
type ClientPolicyRepo struct{ db *gorm.DB }

// NewClientPolicyRepo 绑定连接。
func NewClientPolicyRepo(db *gorm.DB) *ClientPolicyRepo { return &ClientPolicyRepo{db: db} }

// clientRow / policyRow 是查询的中间形态（只在包内可见）。
//
// `tier_default` 可能是 NULL（用指针表示）：scope=client_override 的行 `tier` 必须为空。
type clientRow struct {
	ClientID string `gorm:"column:client_id"`
	Tier     string `gorm:"column:tier"`
	Status   string `gorm:"column:status"`
}

type policyRow struct {
	SelfRegistrationAllowed bool `gorm:"column:self_registration_allowed"`
	PasswordMinLength       int  `gorm:"column:password_min_length"`
	PasswordCharClasses     int  `gorm:"column:password_char_classes"`
	PasswordHistoryCount    int  `gorm:"column:password_history_count"`
	PasswordMaxAgeDays      int  `gorm:"column:password_max_age_days"`
	// DisabledFactors 读成**逗号串**（`array_to_string`）：`database/sql` 的 Scan 不认 PG 的
	// `text[]` → `*[]string`（会报 "unsupported Scan, storing driver.Value type string"），
	// 而这里只需要"有哪些禁因子"，不需要保留数组类型。
	DisabledFactors string `gorm:"column:disabled_factors"`
}

// LoadRegistrationPolicy 按 `client_id` 读出注册所需的门槛。
//
// 解析顺序（数据模型 §3.9）：**client_override 优先，其次同档位的 tier_default**；
// 两者都没有 → `ErrPolicyNotConfigured`。**不做"代码里再垫一套默认值"**。
//
// 结构上刻意拆成「三条查询 + 一个纯函数」：查询无法在没有数据库时验证，
// 而**解析顺序**（覆盖优先、回落、缺失即错）是真正容易写错的地方 —— 它是纯函数，
// 单测直接钉住（见 `TestResolveRegistrationPolicy`）。
func (r *ClientPolicyRepo) LoadRegistrationPolicy(ctx context.Context, tx *gorm.DB, clientID string) (*ClientRegistrationPolicy, error) {
	if strings.TrimSpace(clientID) == "" {
		return nil, errors.New("store: empty client id")
	}
	db := r.db
	if tx != nil {
		db = tx
	}

	var c clientRow
	if res := db.WithContext(ctx).Raw(
		`SELECT client_id, tier, status FROM clients WHERE client_id = ?`, clientID).Scan(&c); res.Error != nil {
		return nil, MapError(res.Error)
	}
	if c.ClientID == "" {
		return nil, ErrClientNotFound
	}

	var override *policyRow
	var row policyRow
	res := db.WithContext(ctx).Raw(
		`SELECT self_registration_allowed, password_min_length, password_char_classes,
		        password_history_count, password_max_age_days,
		        array_to_string(disabled_factors, ',') AS disabled_factors
		   FROM login_policies
		  WHERE scope = 'client_override' AND client_id = ?`, clientID).Scan(&row)
	if res.Error != nil {
		return nil, MapError(res.Error)
	}
	if res.RowsAffected > 0 {
		override = &row
	}

	var tierDefault *policyRow
	if override == nil {
		var defRow policyRow
		res = db.WithContext(ctx).Raw(
			`SELECT self_registration_allowed, password_min_length, password_char_classes,
			        password_history_count, password_max_age_days,
			        array_to_string(disabled_factors, ',') AS disabled_factors
			   FROM login_policies
			  WHERE scope = 'tier_default' AND tier = ?`, c.Tier).Scan(&defRow)
		if res.Error != nil {
			return nil, MapError(res.Error)
		}
		if res.RowsAffected > 0 {
			tierDefault = &defRow
		}
	}

	return resolveRegistrationPolicy(c, override, tierDefault)
}

// resolveRegistrationPolicy 是**解析顺序**的唯一实现（纯函数，无 I/O）。
//
// 覆盖 > 档位默认 > 报错。顺序与"缺失即错"两条都很容易被写成"随便取一个"，
// 所以它必须能被单测直接调用。
func resolveRegistrationPolicy(c clientRow, override, tierDefault *policyRow) (*ClientRegistrationPolicy, error) {
	if c.Status != "active" {
		return nil, ErrClientDisabled
	}
	switch c.Tier {
	case "L1", "L2", "L3":
	default:
		return nil, ErrUnknownTier
	}
	chosen := override
	if chosen == nil {
		chosen = tierDefault
	}
	if chosen == nil {
		return nil, ErrPolicyNotConfigured
	}
	return &ClientRegistrationPolicy{
		ClientID:                c.ClientID,
		Tier:                    c.Tier,
		SelfRegistrationAllowed: chosen.SelfRegistrationAllowed,
		PasswordMinLength:       chosen.PasswordMinLength,
		PasswordCharClasses:     chosen.PasswordCharClasses,
		PasswordHistoryCount:    chosen.PasswordHistoryCount,
		PasswordMaxAgeDays:      chosen.PasswordMaxAgeDays,
		DisabledFactors:         splitFactors(chosen.DisabledFactors),
	}, nil
}

// splitFactors 把 `array_to_string(..., ',')` 的结果还原成切片。
//
// 空串 = 没有禁因子（PG 里空数组 `{}` 的 array_to_string 就是空串）；
// 逐项去空白，但**不丢弃空项以外的任何东西**——这里不做"猜测性清理"。
func splitFactors(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// LockoutPolicy 是失败处理参数（`login_policies` 的 C 组两列）。
//
// 单独一个读方法而不是并进 `ClientRegistrationPolicy`：
// 前者是**注册准入**快照，后者是**登录失败处理**参数，被两条不同的链路消费；
// 合成一个结构会让"注册要读 15 列"变成常态（也更容易被顺手改写出别的东西）。
type LockoutPolicy struct {
	// FailThreshold 连败多少次触发锁定（`fail_threshold`，DB CHECK > 0）。
	FailThreshold int
	// LockDurationMinutes 锁定时长（`lock_duration_min`，DB CHECK > 0）。
	LockDurationMinutes int
}

// LoadLockoutPolicy 读某业务系统的失败处理参数（client_override 优先，其次同档 tier_default）。
//
// **不返回默认值**：两者都没有 → `ErrPolicyNotConfigured`（配置缺陷，不是用户错）。
func (r *ClientPolicyRepo) LoadLockoutPolicy(ctx context.Context, clientID string) (*LockoutPolicy, error) {
	if strings.TrimSpace(clientID) == "" {
		return nil, errors.New("store: empty client id")
	}
	var c clientRow
	if res := r.db.WithContext(ctx).Raw(
		`SELECT client_id, tier, status FROM clients WHERE client_id = ?`, clientID).Scan(&c); res.Error != nil {
		return nil, MapError(res.Error)
	}
	if c.ClientID == "" {
		return nil, ErrClientNotFound
	}

	var over *lockoutRow
	if row, err := r.lockoutRow(ctx, `client_id = ?`, clientID); err != nil {
		return nil, err
	} else {
		over = row
	}
	var tierDefault *lockoutRow
	if c.Tier != "" {
		row, err := r.lockoutRow(ctx, `scope = 'tier_default' AND tier = ?`, c.Tier)
		if err != nil {
			return nil, err
		}
		tierDefault = row
	}
	chosen := over
	if chosen == nil {
		chosen = tierDefault
	}
	if chosen == nil {
		return nil, ErrPolicyNotConfigured
	}
	return &LockoutPolicy{FailThreshold: chosen.FailThreshold, LockDurationMinutes: chosen.LockDurationMin}, nil
}

type lockoutRow struct {
	FailThreshold   int `gorm:"column:fail_threshold"`
	LockDurationMin int `gorm:"column:lock_duration_min"`
}

// lockoutRow 按 where 条件读一条策略行（client_override 与 tier_default 共用）。
func (r *ClientPolicyRepo) lockoutRow(ctx context.Context, where string, arg any) (*lockoutRow, error) {
	var row lockoutRow
	res := r.db.WithContext(ctx).Raw(
		`SELECT fail_threshold, lock_duration_min FROM login_policies WHERE `+where+` LIMIT 1`, arg).Scan(&row)
	if res.Error != nil {
		return nil, MapError(res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, nil
	}
	return &row, nil
}
