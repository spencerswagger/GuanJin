package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// SessionRepo 是 `sessions` 的**最小写路径**（S6 与 S7 的分工点）。
//
// 为什么只有一个方法：`id_token.sid` 必须在签发那一刻就存在（回填会破坏 AC-LOGIN-01 的举证），
// 所以 S6 需要 `Create`；而续期、撤销、踢出、列表属 S7，写在**另一个文件**
// （`repo_session_ops.go`）里，避免两单改同一批文件。
type SessionRepo struct{ db *gorm.DB }

// NewSessionRepo 绑定连接。
func NewSessionRepo(db *gorm.DB) *SessionRepo { return &SessionRepo{db: db} }

// Create 落一条会话。主键由应用层给（D4-A），`status` 缺省为 `active`。
//
// 校验放在应用层而不是等 DB CHECK：`sessions_time_order`（idle ≤ absolute）与
// `sessions_acr_check` 撞库只会给一个 23514，说不清是哪一项不合法。
func (r *SessionRepo) Create(ctx context.Context, s *model.Session) error {
	if s == nil {
		return errors.New("store: nil session")
	}
	if s.UserID == uuid.Nil || s.ClientID == "" {
		return errors.New("store: session requires user_id and client_id")
	}
	if s.ACR == "" {
		return errors.New("store: session requires acr")
	}
	if s.ExpiresAt.IsZero() || s.IdleExpiresAt.IsZero() {
		return errors.New("store: session requires absolute and idle expiry")
	}
	if s.IdleExpiresAt.After(s.ExpiresAt) {
		// `sessions_time_order` 的应用层同口径：空闲超时不得晚于绝对超时。
		return errors.New("store: idle expiry must not be after absolute expiry")
	}
	if s.Status == "" {
		s.Status = "active"
	}
	if s.IssuedAt.IsZero() {
		s.IssuedAt = time.Now().UTC()
	}
	return MapError(r.db.WithContext(ctx).Clauses(clause.Returning{}).Create(s).Error)
}

// 编译期断言：模型与表名/列名一一对应（改名即构建失败，防止漂移）。
var _ = model.SessionColumns
