package model

import (
	"time"

	"github.com/google/uuid"
)

// Identifier 映射 identifiers 表（可变表）。
type Identifier struct {
	BaseModel
	UserID       uuid.UUID  `gorm:"column:user_id;type:uuid;not null"`
	Type         string     `gorm:"column:type;not null"`
	Value        []byte     `gorm:"column:value;type:bytea;not null"`
	ValueEnc     []byte     `gorm:"column:value_enc;type:bytea;not null"`
	ValueMasked  string     `gorm:"column:value_masked;not null"`
	Verified     bool       `gorm:"column:verified;not null;default:false"`
	TrustLevel   int16      `gorm:"column:trust_level;type:smallint;not null;default:0"`
	IsPrimary    bool       `gorm:"column:is_primary;not null;default:false"`
	VerifiedAt   *time.Time `gorm:"column:verified_at"`
	VerifyMethod *string    `gorm:"column:verify_method"`
	LastUsedAt   *time.Time `gorm:"column:last_used_at"`
	Status       string     `gorm:"column:status;not null;default:active"`
}

func (Identifier) TableName() string { return "identifiers" }
