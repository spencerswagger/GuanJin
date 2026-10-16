package model

import (
	"time"

	"github.com/google/uuid"
)

// AuthEvent 映射 auth_events 表（只增表）。
// ★ 使用 AppendOnlyModel：无 updated_at；由 forbid_mutation 触发器禁止 UPDATE/DELETE。
// ★ 绝不添加软删除字段。
type AuthEvent struct {
	AppendOnlyModel
	UserID       *uuid.UUID `gorm:"column:user_id;type:uuid"`
	IdentifierID *uuid.UUID `gorm:"column:identifier_id;type:uuid"`
	SessionID    *uuid.UUID `gorm:"column:session_id;type:uuid"`
	ClientID     *string    `gorm:"column:client_id"`
	EventType    string     `gorm:"column:event_type;not null"`
	FactorType   *string    `gorm:"column:factor_type"`
	Result       string     `gorm:"column:result;not null"`
	Reason       *string    `gorm:"column:reason"`
	RiskScore    *int16     `gorm:"column:risk_score;type:smallint"`
	ACR          *string    `gorm:"column:acr"`
	AMR          []string   `gorm:"column:amr;type:text[];not null"`
	IP           *string    `gorm:"column:ip;type:inet"`
	UserAgent    *string    `gorm:"column:user_agent"`
	DeviceID     *string    `gorm:"column:device_id"`
	TraceID      *string    `gorm:"column:trace_id"`
	OccurredAt   time.Time  `gorm:"column:occurred_at;not null"`
}

func (AuthEvent) TableName() string { return "auth_events" }
