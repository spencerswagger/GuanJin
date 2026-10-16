package model

import (
	"time"

	"github.com/google/uuid"
)

// Authenticator 映射 authenticators 表（可变表）。
// credential_data 为 jsonb 自描述信封，DB 不解释其语义。
type Authenticator struct {
	BaseModel
	UserID         uuid.UUID  `gorm:"column:user_id;type:uuid;not null"`
	IdentifierID   *uuid.UUID `gorm:"column:identifier_id;type:uuid"`
	Type           string     `gorm:"column:type;not null"`
	CredentialData []byte     `gorm:"column:credential_data;type:jsonb;not null"`
	Status         string     `gorm:"column:status;not null;default:active"`
	Label          *string    `gorm:"column:label"`
	FailCount      int32      `gorm:"column:fail_count;type:integer;not null;default:0"`
	LockedUntil    *time.Time `gorm:"column:locked_until"`
	LastUsedAt     *time.Time `gorm:"column:last_used_at"`
	ExpiresAt      *time.Time `gorm:"column:expires_at"`
}

func (Authenticator) TableName() string { return "authenticators" }
