package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BaseModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (m *BaseModel) BeforeCreate(*gorm.DB) error {
	if m.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		m.ID = id
	}
	return nil
}

type AppendOnlyModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at;not null;autoCreateTime"`
}

func (m *AppendOnlyModel) BeforeCreate(*gorm.DB) error {
	if m.ID == uuid.Nil {
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		m.ID = id
	}
	return nil
}
