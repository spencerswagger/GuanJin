package model

import "time"

// Client 映射 clients 表（可变表）。
// ★ 主键为 text client_id（非 uuid），因此不复用 BaseModel，单独声明 created_at/updated_at。
type Client struct {
	ClientID               string     `gorm:"column:client_id;primaryKey"`
	Name                   string     `gorm:"column:name;not null"`
	Tier                   string     `gorm:"column:tier;not null;default:L2"`
	Status                 string     `gorm:"column:status;not null;default:active"`
	RedirectURIs           []string   `gorm:"column:redirect_uris;type:text[];not null"`
	PostLogoutRedirectURIs []string   `gorm:"column:post_logout_redirect_uris;type:text[];not null"`
	SecretHash             []byte     `gorm:"column:secret_hash;type:bytea"`
	SecretEnc              []byte     `gorm:"column:secret_enc;type:bytea"`
	PrevSecretHash         []byte     `gorm:"column:prev_secret_hash;type:bytea"`
	SecretRotatedAt        *time.Time `gorm:"column:secret_rotated_at"`
	DefaultRole            *string    `gorm:"column:default_role"`
	PentestRequired        bool       `gorm:"column:pentest_required;not null;default:false"`
	SOCRequired            bool       `gorm:"column:soc_required;not null;default:false"`
	CreatedAt              time.Time  `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt              time.Time  `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (Client) TableName() string { return "clients" }
