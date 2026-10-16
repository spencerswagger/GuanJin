package model

import "time"

// SigningKey 映射 signing_keys 表（可变表）。
type SigningKey struct {
	BaseModel
	Kid           string     `gorm:"column:kid;not null"`
	Purpose       string     `gorm:"column:purpose;not null"`
	Algorithm     string     `gorm:"column:algorithm;not null"`
	Status        string     `gorm:"column:status;not null;default:pending"`
	PublicKey     string     `gorm:"column:public_key;not null"`
	PrivateKeyEnc []byte     `gorm:"column:private_key_enc;type:bytea;not null"`
	ActivatesAt   *time.Time `gorm:"column:activates_at"`
	RetiresAt     *time.Time `gorm:"column:retires_at"`
}

func (SigningKey) TableName() string { return "signing_keys" }
