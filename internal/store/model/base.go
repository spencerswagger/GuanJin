package model

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BaseModel 是可变表（有 created_at / updated_at）的公共列。
//
// 三条硬约定：
//  1. 主键 UUID v7 **由应用层生成**（D4-A：迁移里不写 DB 默认值）；
//  2. 时间戳**只读**（`->`）：`created_at` 取 DB 的 DEFAULT now()、`updated_at` 由
//     `set_updated_at()` 触发器维护 —— 应用不自己写时间，避免两套时钟；
//  3. 不含软删除字段：业务表用 `status` 表达生命周期。
type BaseModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at;not null;->"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;->"`
}

// AppendOnlyModel 是只增表（auth_events / audit_logs）的公共列：
// 只有 `id` 与 `created_at`，**没有 updated_at**（DB 侧还有 forbid_mutation() 触发器兜底）。
type AppendOnlyModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at;not null;->"`
}

// BeforeCreate 生成 UUID v7 主键（D4-A）。
//
// 只在 ID 为空时生成：显式指定 ID 的场景（导入/测试固定值）不被覆盖。
// uuid.NewV7 失败（随机源不可用）→ 直接报错，**不退回 v4 或随机字符串**：
// 主键格式一旦不统一，任何按时间排序的取证都失真。
func (b *BaseModel) BeforeCreate(_ *gorm.DB) error {
	return ensureV7(&b.ID)
}

// BeforeCreate 见 BaseModel.BeforeCreate。
func (b *AppendOnlyModel) BeforeCreate(_ *gorm.DB) error {
	return ensureV7(&b.ID)
}

func ensureV7(id *uuid.UUID) error {
	if id == nil {
		return fmt.Errorf("model: nil primary key target")
	}
	if *id != uuid.Nil {
		return nil
	}
	generated, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("model: uuid v7 generation failed: %w", err)
	}
	*id = generated
	return nil
}
