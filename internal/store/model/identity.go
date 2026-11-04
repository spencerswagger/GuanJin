package model

import (
	"time"

	"github.com/google/uuid"
)

// User 是 `users` 表（数据模型 §3.1）的读写映射。
//
// 只做映射：`users` 是**瘦身**的身份主体（基线 0.1 取舍 1），
// **不存任何凭据、不存手机/邮箱**——那些在 `identifiers` / `authenticators`。
// "这个字段是否随新增一种登录方式而变化？会变的，外置。"这条判据决定了本表的列。
type User struct {
	BaseModel
	// Username 规范化登录名（小写存储；DB 有 `users_username_lower_uidx` 唯一索引）。
	Username string `gorm:"column:username;not null"`
	// DisplayName 展示名，可空。
	DisplayName *string `gorm:"column:display_name"`
	// Status active | disabled（**不是**软删标记：停用保留主体与审计链）。
	Status string `gorm:"column:status;not null;default:active"`
	// IsAdmin 平台管理员标记（C 端用户恒 false）。
	IsAdmin bool `gorm:"column:is_admin;not null;default:false"`
	// DisabledAt / DisabledReason 与 status 同真同假（DB CHECK users_disabled_consistency）。
	DisabledAt     *time.Time `gorm:"column:disabled_at"`
	DisabledReason *string    `gorm:"column:disabled_reason"`
	// EffectiveTier 生效配置的物化缓存（L1/L2/L3），由应用层维护；权威计算依据是
	// `user_visited_clients`（数据模型 §3.14）。
	EffectiveTier *string `gorm:"column:effective_tier"`
}

// TableName 冻结表名（表名不得改名）。
func (User) TableName() string { return "users" }

// Identifier 是 `identifiers` 表（数据模型 §3.2）的读写映射：指向用户的"名字"。
//
// **三段式存储**（索引 §4.2-5）：同一列组同时满足"唯一约束 + 加密落库 + 脱敏展示"：
//   - `Value`     盲索引 HMAC-SM3(派生键, 规范化值)：承担 `(type,value)` 全局唯一与等值查询；
//   - `ValueEnc`  SM4 随机 IV 密文：承担还原（发短信/发邮件必须能拿到明文）；
//   - `ValueMasked` 脱敏展示：`138****8888`。
//
// 为什么不能只存密文：随机 IV 的密文无法建唯一索引（同一明文两次加密不同），
// 而这正是"一个手机号只能绑一个账号"的落地点。
type Identifier struct {
	BaseModel
	UserID uuid.UUID `gorm:"column:user_id;type:uuid;not null"`
	// Type 开放集合（phone/email/username/employee_no/未来新增），**不加 CHECK**。
	Type string `gorm:"column:type;not null"`
	// Value 盲索引（bytea）。
	Value []byte `gorm:"column:value;type:bytea;not null"`
	// ValueEnc SM4 密文（含 IV 前缀）。
	ValueEnc []byte `gorm:"column:value_enc;type:bytea;not null"`
	// ValueMasked 脱敏展示值。
	ValueMasked string `gorm:"column:value_masked;not null"`
	// Verified 是否已验证（只有已验证的 phone/email 才是"可认证能力"的一部分）。
	Verified bool `gorm:"column:verified;not null;default:false"`
	// TrustLevel 0=未验证 / 1=弱验证 / 2=信道验证 / 3=强证明。
	TrustLevel int16 `gorm:"column:trust_level;not null;default:0"`
	// IsPrimary 主标识符（DB 有 partial unique：每用户至多一个）。
	IsPrimary bool `gorm:"column:is_primary;not null;default:false"`
	// VerifiedAt 与 verified 一致（DB CHECK）。
	VerifiedAt *time.Time `gorm:"column:verified_at"`
	// VerifyMethod sms_otp / email_otp / admin …（可空）。
	VerifyMethod *string    `gorm:"column:verify_method"`
	LastUsedAt   *time.Time `gorm:"column:last_used_at"`
	// Status active | disabled。
	Status string `gorm:"column:status;not null;default:active"`
}

// TableName 冻结表名。
func (Identifier) TableName() string { return "identifiers" }

// Authenticator 是 `authenticators` 表（数据模型 §3.3）的读写映射：持有型因子的凭据实例。
//
// **信道型因子（短信/邮箱）不建本表行**：它们的"凭据"就是已验证的标识符
// （数据模型 §3.3 关键说明），强行建行会出现"标识符与认证器指向同一事实"的冗余。
type Authenticator struct {
	BaseModel
	UserID uuid.UUID `gorm:"column:user_id;type:uuid;not null"`
	// IdentifierID 可空（channel 型不建行；holding 型通常也无关联标识符）。
	IdentifierID *uuid.UUID `gorm:"column:identifier_id;type:uuid"`
	// Type 开放集合（password/totp/recovery_code/webauthn/未来新增），不加 CHECK。
	Type string `gorm:"column:type;not null"`
	// CredentialData 凭据信封（jsonb，结构由因子自描述，DB 不解释）。
	CredentialData JSONValue `gorm:"column:credential_data;type:jsonb;not null"`
	// Status active | disabled | revoked。
	Status string `gorm:"column:status;not null;default:active"`
	// Label 展示名，如"我的 iPhone"。
	Label *string `gorm:"column:label"`
	// FailCount 失败计数的**持久化镜像**：权威实时计数在 Redis（D3-D，按 user_id 全局共享）。
	// 这里可用于审计与"Redis 全丢后重建"的兜底，但**不得**作为判定依据。
	FailCount int32 `gorm:"column:fail_count;not null;default:0"`
	// LockedUntil 锁定截止（同上，镜像）。
	LockedUntil *time.Time `gorm:"column:locked_until"`
	LastUsedAt  *time.Time `gorm:"column:last_used_at"`
	// ExpiresAt 恢复码/临时凭据可设过期。
	ExpiresAt *time.Time `gorm:"column:expires_at"`
}

// TableName 冻结表名。
func (Authenticator) TableName() string { return "authenticators" }

// IdentifierColumns / AuthenticatorColumns 是列的**权威顺序**（与迁移定义一致），
// 供 drift 测试与显式 SELECT 使用。新增列必须同时改这里——否则漂移测试会红。
func IdentifierColumns() []string {
	return []string{
		"id", "user_id", "type", "value", "value_enc", "value_masked", "verified",
		"trust_level", "is_primary", "verified_at", "verify_method", "last_used_at",
		"status", "created_at", "updated_at",
	}
}

// AuthenticatorColumns 见 IdentifierColumns。
func AuthenticatorColumns() []string {
	return []string{
		"id", "user_id", "identifier_id", "type", "credential_data", "status", "label",
		"fail_count", "locked_until", "last_used_at", "expires_at", "created_at", "updated_at",
	}
}
