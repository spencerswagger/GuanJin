package model

import (
	"time"

	"github.com/google/uuid"
)

// UserVisitedClient 映射 user_visited_clients 表（可变表）。
type UserVisitedClient struct {
	BaseModel
	UserID      uuid.UUID `gorm:"column:user_id;type:uuid;not null"`
	ClientID    string    `gorm:"column:client_id;not null"`
	TierAtVisit *string   `gorm:"column:tier_at_visit"`
	VisitCount  int64     `gorm:"column:visit_count;type:bigint;not null;default:1"`
	FirstSeenAt time.Time `gorm:"column:first_seen_at;not null"`
	LastSeenAt  time.Time `gorm:"column:last_seen_at;not null"`
}

func (UserVisitedClient) TableName() string { return "user_visited_clients" }
