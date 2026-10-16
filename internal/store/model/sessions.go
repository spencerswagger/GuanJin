package model

import (
	"time"

	"github.com/google/uuid"
)

// Session 映射 sessions 表（可变表）。
type Session struct {
	BaseModel
	UserID        uuid.UUID  `gorm:"column:user_id;type:uuid;not null"`
	ClientID      string     `gorm:"column:client_id;not null"`
	ACR           string     `gorm:"column:acr;not null"`
	AMR           []string   `gorm:"column:amr;type:text[];not null"`
	Status        string     `gorm:"column:status;not null;default:active"`
	IssuedAt      time.Time  `gorm:"column:issued_at;not null"`
	LastSeenAt    *time.Time `gorm:"column:last_seen_at"`
	ExpiresAt     time.Time  `gorm:"column:expires_at;not null"`
	IdleExpiresAt time.Time  `gorm:"column:idle_expires_at;not null"`
	RevokedAt     *time.Time `gorm:"column:revoked_at"`
	RevokeReason  *string    `gorm:"column:revoke_reason"`
	IP            *string    `gorm:"column:ip;type:inet"`
	UserAgent     *string    `gorm:"column:user_agent"`
	DeviceID      *string    `gorm:"column:device_id"`
}

func (Session) TableName() string { return "sessions" }
