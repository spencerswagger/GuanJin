package model

import (
	"time"

	"github.com/google/uuid"
)

// Session 是 `sessions` 表（000005）的模型：**会话**是"登录已经发生"的权威事实。
//
// 边界（S6/S7 分工）：S6 只写**最小 active 会话**（`sid` 必须随 id_token 一起签发，
// 回填会破坏 AC-LOGIN-01 的举证）；续期、撤销、踢出、管理面列表全部属 S7，
// 因此本文件**只声明列**，写路径只有 `SessionRepo.Create`（`repo_session.go`）。
//
// 不含软删除：会话用 `status` 表达生命周期（`active|revoked|expired`），
// 与 `sessions_revoked_consistency` CHECK 一致。
type Session struct {
	BaseModel
	UserID   uuid.UUID `gorm:"column:user_id;type:uuid;not null"`
	ClientID string    `gorm:"column:client_id;not null"`
	// ACR 该会话达成的保证级别（DB CHECK 只允许 AAL1/AAL2/AAL3；V1 不产出 AAL3，D12）。
	ACR string `gorm:"column:acr;not null"`
	// AMR 达成该级别用到的因子组合（RFC 8176 取值，如 `{pwd}`）。
	AMR    StringArray `gorm:"column:amr;type:text[];not null"`
	Status string      `gorm:"column:status;not null;default:active"`
	// IssuedAt 签发时刻（DDL 有 DEFAULT now()；应用层显式给，便于测试与审计对齐）。
	IssuedAt time.Time `gorm:"column:issued_at;not null"`
	// ExpiresAt / IdleExpiresAt 绝对与空闲超时（来自 `login_policies`，DB 有顺序 CHECK）。
	ExpiresAt     time.Time  `gorm:"column:expires_at;not null"`
	IdleExpiresAt time.Time  `gorm:"column:idle_expires_at;not null"`
	LastSeenAt    *time.Time `gorm:"column:last_seen_at"`
	RevokedAt     *time.Time `gorm:"column:revoked_at"`
	RevokeReason  *string    `gorm:"column:revoke_reason"`
}

// TableName 冻结表名（F0：表名不得改名）。
func (Session) TableName() string { return "sessions" }

// SessionColumns 是 `sessions` 表的列名（顺序同迁移），供漂移测试与 SELECT 使用。
func SessionColumns() []string {
	return []string{
		"id", "user_id", "client_id", "acr", "amr", "status", "issued_at",
		"last_seen_at", "expires_at", "idle_expires_at", "revoked_at", "revoke_reason",
		"created_at", "updated_at",
	}
}
