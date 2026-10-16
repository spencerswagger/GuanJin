package model

import (
	"time"

	"github.com/google/uuid"
)

// Challenge 映射 challenges 表（可变表）。
type Challenge struct {
	BaseModel
	UserID          *uuid.UUID `gorm:"column:user_id;type:uuid"`
	IdentifierID    *uuid.UUID `gorm:"column:identifier_id;type:uuid"`
	AuthenticatorID *uuid.UUID `gorm:"column:authenticator_id;type:uuid"`
	ClientID        *string    `gorm:"column:client_id"`
	Purpose         string     `gorm:"column:purpose;not null"`
	FactorType      string     `gorm:"column:factor_type;not null"`
	Channel         *string    `gorm:"column:channel"`
	TargetMasked    *string    `gorm:"column:target_masked"`
	ResponseHash    []byte     `gorm:"column:response_hash;type:bytea;not null"`
	Attempts        int32      `gorm:"column:attempts;type:integer;not null;default:0"`
	MaxAttempts     int32      `gorm:"column:max_attempts;type:integer;not null;default:5"`
	ConsumedAt      *time.Time `gorm:"column:consumed_at"`
	ExpiresAt       time.Time  `gorm:"column:expires_at;not null"`
}

func (Challenge) TableName() string { return "challenges" }
