package model

import "time"

// User 映射 users 表（可变表，含 created_at/updated_at）。
// ★ 绝不添加软删除字段（用 status 表达停用，见技术栈 §5.1）。
type User struct {
	BaseModel
	Username       string     `gorm:"column:username;not null"`
	DisplayName    *string    `gorm:"column:display_name"`
	Status         string     `gorm:"column:status;not null;default:active"`
	IsAdmin        bool       `gorm:"column:is_admin;not null;default:false"`
	DisabledAt     *time.Time `gorm:"column:disabled_at"`
	DisabledReason *string    `gorm:"column:disabled_reason"`
	EffectiveTier  *string    `gorm:"column:effective_tier"`
}

func (User) TableName() string { return "users" }
