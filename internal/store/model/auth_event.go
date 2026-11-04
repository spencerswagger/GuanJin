package model

import (
	"time"

	"github.com/google/uuid"
)

// AuthEvent 是 `auth_events` 表（数据模型 §3.6）的**只写**映射：认证动作的不可变流水。
//
// 与 `audit_logs` 的分工（D4-C / 我 S4a 报告里的口径）：
//   - `auth_events`：每一次认证动作（发送、校验、登录成功/失败、锁定…），高频、**不入哈希链**；
//   - `audit_logs`：管理性副作用（三员动作、角色绑定、密钥轮换、注销/导出），低频、**入链**。
//
// 登录/补步路径**只写本表**——这也是"补步事务不跨审计链咨询锁"（I-F8）能成立的结构原因。
type AuthEvent struct {
	AppendOnlyModel
	UserID       *uuid.UUID `gorm:"column:user_id;type:uuid"`
	IdentifierID *uuid.UUID `gorm:"column:identifier_id;type:uuid"`
	SessionID    *uuid.UUID `gorm:"column:session_id;type:uuid"`
	ClientID     *string    `gorm:"column:client_id"`
	// EventType 开放集合（login / register / otp_send / bind / unbind / lock …），**不加 CHECK**。
	EventType string `gorm:"column:event_type;not null"`
	// FactorType 参与因子（开放，可空）。
	FactorType *string `gorm:"column:factor_type"`
	// Result 封闭五值（DB CHECK）：success | failure | locked | denied | error。
	Result string `gorm:"column:result;not null"`
	// Reason 失败分类（**不含敏感信息**）。
	Reason *string `gorm:"column:reason"`
	// RiskScore 0..100（可空）。
	RiskScore *int16 `gorm:"column:risk_score"`
	// ACR / AMR 本次动作达成的级别与因子组合。
	ACR *string     `gorm:"column:acr"`
	AMR StringArray `gorm:"column:amr;type:text[];not null"`
	// IP / UserAgent / DeviceID 设备上下文（可空；不得写入明文令牌）。
	IP        *string `gorm:"column:ip"`
	UserAgent *string `gorm:"column:user_agent"`
	DeviceID  *string `gorm:"column:device_id"`
	// TraceID 贯穿日志/事件/审计（D4-C(4)）。
	TraceID *string `gorm:"column:trace_id"`
	// OccurredAt 动辄与写库分离：动作发生时刻由调用方给（便于补录与复盘）。
	OccurredAt time.Time `gorm:"column:occurred_at;not null;default:now()"`
}

// TableName 冻结表名。
func (AuthEvent) TableName() string { return "auth_events" }
