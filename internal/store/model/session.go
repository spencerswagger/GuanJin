package model

import (
	"time"

	"github.com/google/uuid"
)

// Session 是 `sessions` 表（数据模型 §3.5）的读写映射。
//
// 三条与表结构对齐的硬约定：
//   - **主键 UUID v7 由应用层给**（D4-A）；`created_at`/`updated_at` 由 DB 的 DEFAULT 与
//     `set_updated_at()` 触发器维护（`BaseModel` 里是只读列），应用不自己写时间；
//   - **`status` 与 `revoked_at` 同真同假**（DB CHECK `sessions_revoked_consistency`）：
//     只改一个必然被库拒，所以撤销路径永远是一次条件 UPDATE 同时写两列；
//   - **`idle_expires_at <= expires_at`**（DB CHECK `sessions_time_order`）：空闲超时
//     不得越过绝对超时，滚动续期只能在绝对超时之内滚。
//
// 为什么没有"会话版本号"列：基线里的版本号是**给业务系统增量拉取**用的旁路（架构说明 §5.7），
// 落点属 Agent 面（M2），不是本表的列；本表用 `status` + `revoked_at` 表达撤销事实。
type Session struct {
	BaseModel
	// UserID 会话主体（`ON DELETE RESTRICT`：会话永不物理删，撤销只改 status）。
	UserID uuid.UUID `gorm:"column:user_id;type:uuid;not null"`
	// ClientID 会话归属的业务系统（NOT NULL：登录与注册一定有客户端上下文）。
	ClientID string `gorm:"column:client_id;not null"`
	// ACR 本次会话达成的保证级别（AAL1/AAL2；AAL3 预留、V1 不产出，DB CHECK 三值）。
	ACR string `gorm:"column:acr;not null"`
	// AMR 达成该级别的因子组合（`pwd`/`totp`/…；空数组写成 `{}`，NOT NULL 列不许 NULL）。
	AMR StringArray `gorm:"column:amr;type:text[];not null"`
	// Status active | revoked | expired（DB CHECK）。
	//
	// **不写 `default:active` tag**：与 D14① 同一条原则——GORM 的 default tag 会让零值
	// 被静默省掉、由 DB 兜底，等于代码里留了第二套口径。状态一律由调用方显式给。
	Status string `gorm:"column:status;not null"`
	// IssuedAt 签发时刻（由应用给：撤销窗口、并发计数与"挤掉最旧"都按它排序）。
	IssuedAt time.Time `gorm:"column:issued_at;not null"`
	// LastSeenAt 最近活跃时刻（可空：刚签发还没动过就是 NULL）。
	LastSeenAt *time.Time `gorm:"column:last_seen_at"`
	// ExpiresAt 绝对超时（重认证间隔）；IdleExpiresAt 空闲超时（可滚动）。
	ExpiresAt     time.Time `gorm:"column:expires_at;not null"`
	IdleExpiresAt time.Time `gorm:"column:idle_expires_at;not null"`
	// RevokedAt / RevokeReason 与 `status='revoked'` 同真同假（DB CHECK）。
	// 原因取值见 `session.RevokeReason`（logout/logout_all/admin/risk/role_change/password_change）。
	RevokedAt    *time.Time `gorm:"column:revoked_at"`
	RevokeReason *string    `gorm:"column:revoke_reason"`
	// 设备上下文（可空；**不得写入明文令牌**）。
	IP        *string `gorm:"column:ip"`
	UserAgent *string `gorm:"column:user_agent"`
	DeviceID  *string `gorm:"column:device_id"`
}

// TableName 冻结表名（表名不得改名）。
func (Session) TableName() string { return "sessions" }

// 会话状态（封闭三值，与 DB CHECK `status IN ('active','revoked','expired')` 一致）。
const (
	SessionStatusActive  = "active"
	SessionStatusRevoked = "revoked"
	SessionStatusExpired = "expired"
)
