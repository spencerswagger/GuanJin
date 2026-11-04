package model

import (
	"time"

	"github.com/google/uuid"
)

// Challenge 是 `challenges` 表（数据模型 §3.4）的读写映射：一次挑战的**权威状态**。
//
// 权威在库、不在缓存（索引 §2 / 接口 §5.9）：Redis 只承载限流计数与热缓存。
// `attempts` 是**单挑战**计数（`max_attempts` 封顶），与账号级失败计数
// （Redis 的 `login:fail:{user_id}`，D3-D）是两个对象，不能互相替代。
type Challenge struct {
	BaseModel
	// UserID 注册场景可能尚无用户 → 可空。
	UserID *uuid.UUID `gorm:"column:user_id;type:uuid"`
	// IdentifierID / AuthenticatorID 挑战的目标对象（可空）。
	IdentifierID    *uuid.UUID `gorm:"column:identifier_id;type:uuid"`
	AuthenticatorID *uuid.UUID `gorm:"column:authenticator_id;type:uuid"`
	// ClientID 业务系统上下文（可空：某些自助动作用户没有 client 上下文）。
	ClientID *string `gorm:"column:client_id"`
	// Purpose 六值词表（D2，DB CHECK `challenges_purpose_valid`）。
	Purpose string `gorm:"column:purpose;not null"`
	// FactorType 开放集合（不加 CHECK：新增因子只写数据）。
	FactorType string `gorm:"column:factor_type;not null"`
	// Channel sms / email；holding 型为空。
	Channel *string `gorm:"column:channel"`
	// TargetMasked 脱敏投递目标。
	TargetMasked *string `gorm:"column:target_masked"`
	// ResponseHash 期望应答的摘要（SM3/HMAC-SM3）：**明文码永不落库**。
	ResponseHash []byte `gorm:"column:response_hash;type:bytea;not null"`
	// Attempts / MaxAttempts 单挑战尝试计数（DB CHECK：attempts <= max_attempts）。
	Attempts    int32 `gorm:"column:attempts;not null;default:0"`
	MaxAttempts int32 `gorm:"column:max_attempts;not null;default:5"`
	// ConsumedAt 一次性消费标记。
	ConsumedAt *time.Time `gorm:"column:consumed_at"`
	// ExpiresAt 过期时刻（DB CHECK：expires_at > created_at）。
	ExpiresAt time.Time `gorm:"column:expires_at;not null"`
}

// TableName 冻结表名。
func (Challenge) TableName() string { return "challenges" }
