package model

import (
	"time"

	"github.com/google/uuid"
)

// RuntimeConfig 映射 runtime_config 表（可变表）。
// ★ 主键为 text key（非 uuid），因此不复用 BaseModel，单独声明 created_at/updated_at。
type RuntimeConfig struct {
	Key         string     `gorm:"column:key;primaryKey"`
	Value       []byte     `gorm:"column:value;type:jsonb;not null"`
	ValueType   string     `gorm:"column:value_type;not null;default:json"`
	Description *string    `gorm:"column:description"`
	IsSecret    bool       `gorm:"column:is_secret;not null;default:false"`
	UpdatedBy   *uuid.UUID `gorm:"column:updated_by;type:uuid"`
	CreatedAt   time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (RuntimeConfig) TableName() string { return "runtime_config" }
