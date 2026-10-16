package model

import (
	"time"

	"github.com/google/uuid"
)

// AuditLog 映射 audit_logs 表（只增表）。
// ★ 使用 AppendOnlyModel：无 updated_at；由 forbid_mutation 触发器禁止 UPDATE/DELETE。
// ★ 绝不添加软删除字段。
type AuditLog struct {
	AppendOnlyModel
	ActorUserID  *uuid.UUID `gorm:"column:actor_user_id;type:uuid"`
	ActorRole    *string    `gorm:"column:actor_role"`
	Action       string     `gorm:"column:action;not null"`
	TargetType   *string    `gorm:"column:target_type"`
	TargetID     *string    `gorm:"column:target_id"`
	ClientID     *string    `gorm:"column:client_id"`
	Before       []byte     `gorm:"column:before;type:jsonb"`
	After        []byte     `gorm:"column:after;type:jsonb"`
	Result       string     `gorm:"column:result;not null"`
	IntegrityTag []byte     `gorm:"column:integrity_tag;type:bytea;not null"`
	PrevTag      []byte     `gorm:"column:prev_tag;type:bytea"`
	Signature    []byte     `gorm:"column:signature;type:bytea"`
	IP           *string    `gorm:"column:ip;type:inet"`
	TraceID      *string    `gorm:"column:trace_id"`
	OccurredAt   time.Time  `gorm:"column:occurred_at;not null"`
}

func (AuditLog) TableName() string { return "audit_logs" }
