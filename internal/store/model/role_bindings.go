package model

import (
	"time"

	"github.com/google/uuid"
)

// RoleBinding 映射 role_bindings 表（可变表）。
type RoleBinding struct {
	BaseModel
	UserID       uuid.UUID  `gorm:"column:user_id;type:uuid;not null"`
	ClientID     string     `gorm:"column:client_id;not null"`
	Role         string     `gorm:"column:role;not null"`
	Status       string     `gorm:"column:status;not null;default:active"`
	GrantedBy    *uuid.UUID `gorm:"column:granted_by;type:uuid"`
	GrantedAt    time.Time  `gorm:"column:granted_at;not null"`
	RevokedAt    *time.Time `gorm:"column:revoked_at"`
	RevokeReason *string    `gorm:"column:revoke_reason"`
}

func (RoleBinding) TableName() string { return "role_bindings" }
