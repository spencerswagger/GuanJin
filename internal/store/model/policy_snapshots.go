package model

import "time"

// PolicySnapshot 映射 policy_snapshots 表（按 version 追加，只有 created_at，无 updated_at）。
// ★ 使用 AppendOnlyModel：迁移中该表无 updated_at 列，仅追加新版本、不原地更新。
type PolicySnapshot struct {
	AppendOnlyModel
	ClientID     string    `gorm:"column:client_id;not null"`
	Version      int32     `gorm:"column:version;type:integer;not null"`
	Payload      []byte    `gorm:"column:payload;type:jsonb;not null"`
	PayloadHash  []byte    `gorm:"column:payload_hash;type:bytea;not null"`
	IntegrityTag []byte    `gorm:"column:integrity_tag;type:bytea;not null"`
	ReportedAt   time.Time `gorm:"column:reported_at;not null"`
}

func (PolicySnapshot) TableName() string { return "policy_snapshots" }
