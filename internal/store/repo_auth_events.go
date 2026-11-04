package store

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// AuthEventRepo 是 `auth_events` 的**只追加**仓储。
//
// 没有 Update/Delete 方法，而且**故意不提供**：表上还有 `forbid_mutation()` 触发器兜底
// （I5），但"仓储层面没有这个方法"是第一道闸——写不出来比写错被拒更好。
type AuthEventRepo struct{ db *gorm.DB }

// NewAuthEventRepo 绑定连接（db 可为连接或事务句柄）。
func NewAuthEventRepo(db *gorm.DB) *AuthEventRepo { return &AuthEventRepo{db: db} }

// WithTx 返回绑定事务的副本。
func (r *AuthEventRepo) WithTx(tx *gorm.DB) *AuthEventRepo { return &AuthEventRepo{db: tx} }

// Append 追加一条认证事件。
//
// 要求 `Result` 非空：把"结果未知"写进流水等于伪造事实。
// 高频路径请用批量写（`CreateInBatches` / COPY，技术栈 §5 第五条约束）——
// 本方法只服务"一次请求写一两条"的常规场景。
func (r *AuthEventRepo) Append(ctx context.Context, e *model.AuthEvent) error {
	if e == nil {
		return errors.New("store: nil auth event")
	}
	if e.EventType == "" {
		return errors.New("store: auth event requires event_type")
	}
	if e.Result == "" {
		return errors.New("store: auth event requires result")
	}
	if e.AMR == nil {
		// NOT NULL 列：nil 写成空数组，避免一插入就撞约束。
		e.AMR = model.StringArray{}
	}
	return MapError(r.db.WithContext(ctx).Create(e).Error)
}
