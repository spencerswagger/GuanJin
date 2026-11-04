package identity

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/identifier"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是**标识符生命周期 + 凭据撤销**（数据模型 §4.1 的 I1~I6 落地点）。
//
// 每个写操作都遵循同一套骨架，顺序不能换：
//
//	① `store.Tx` 开事务
//	② `LockUser`（先锁用户行，串行化该账号的一切凭据变更）
//	③ 读 `Snapshot`（**在锁内读**，否则读数立刻作废）
//	④ `identifier.Check*` 判不变式
//	⑤ 写回 + 追加 auth_events
//
// ②③④ 的顺序是本文档最重要的三行：把 ③ 放到 ② 之前，两个并发解绑会各自看到
// "还有 2 个标识符"，然后各删一个 —— I1/I2 就成了纸面约束。

// BindRequest 是"绑定新标识符"的入参。
type BindRequest struct {
	UserID       uuid.UUID
	Type         string
	Value        string
	Verified     bool
	VerifyMethod string
	TrustLevel   int16
}

// Snapshot 读该账号的不变式读数（**必须在事务内、且通常在 LockUser 之后**调用）。
func (s *Service) Snapshot(ctx context.Context, tx *gorm.DB, userID uuid.UUID) (identifier.Snapshot, error) {
	now := s.cfg.Now().UTC()
	var snap identifier.Snapshot
	var err error
	if snap.Total, err = s.identifiers.CountByUser(ctx, tx, userID); err != nil {
		return snap, err
	}
	if snap.Verified, err = s.identifiers.CountVerified(ctx, tx, userID); err != nil {
		return snap, err
	}
	if snap.VerifiedAuthable, err = s.identifiers.CountVerifiedAuthable(ctx, tx, userID); err != nil {
		return snap, err
	}
	if snap.UsableHolding, err = s.authenticators.CountUsableHolding(ctx, tx, userID, now); err != nil {
		return snap, err
	}
	if snap.Primary, err = s.identifiers.CountPrimary(ctx, tx, userID); err != nil {
		return snap, err
	}
	return snap, nil
}

// BindIdentifier 绑定一个新标识符。
//
// 首条标识符自动成为主标识符（每用户至多一个，DB 有 partial unique 索引）；
// 占用外部稀缺资源的类型**必须验证**才算成立（`Verified=false` 时行落在 pending 语义里，
// 由 S5 的挑战流程把它置为已验证）。
func (s *Service) BindIdentifier(ctx context.Context, req BindRequest) (*model.Identifier, error) {
	if req.UserID == uuid.Nil {
		return nil, store.ErrUserNotFound
	}
	encoded, err := identifier.Encode(req.Type, req.Value, s.cfg.IndexKey, s.cfg.FieldKey)
	if err != nil {
		return nil, err
	}
	now := s.cfg.Now().UTC()

	var out *model.Identifier
	err = store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if _, err := s.users.LockUser(ctx, tx, req.UserID); err != nil {
			return err
		}
		snap, err := s.Snapshot(ctx, tx, req.UserID)
		if err != nil {
			return err
		}
		row := &model.Identifier{
			UserID:      req.UserID,
			Type:        encoded.Type,
			Value:       encoded.BlindIndex,
			ValueEnc:    encoded.Cipher,
			ValueMasked: encoded.Masked,
			Verified:    req.Verified,
			TrustLevel:  req.TrustLevel,
			IsPrimary:   snap.Primary == 0, // 首条即主；之后由显式操作调整（I6 保护最后一个）
			Status:      "active",
		}
		if req.Verified {
			row.VerifiedAt = &now
			if req.VerifyMethod != "" {
				row.VerifyMethod = &req.VerifyMethod
			}
		}
		if err := s.identifiers.WithTx(tx).Create(ctx, row); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, eventOf(req.UserID, "bind", "success", now)); err != nil {
			return err
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UnbindIdentifier 解绑（= 置 `disabled`，**不物理删**：唯一索引与审计都要留痕）。
//
// 先判归属再判不变式：别人的标识符一律回 `ErrIdentifierNotFound`（防枚举，
// 不给"这个 id 存在但不属于你"这种区分性信息）。
func (s *Service) UnbindIdentifier(ctx context.Context, userID, identifierID uuid.UUID) error {
	now := s.cfg.Now().UTC()
	return store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if _, err := s.users.LockUser(ctx, tx, userID); err != nil {
			return err
		}
		row, err := s.identifiers.WithTx(tx).GetByID(ctx, identifierID)
		if err != nil {
			return err
		}
		if row.UserID != userID {
			return ErrIdentifierNotFound
		}
		snap, err := s.Snapshot(ctx, tx, userID)
		if err != nil {
			return err
		}
		if err := identifier.CheckUnbind(snap, row.Verified, row.Status == "active"); err != nil {
			return err
		}
		if _, err := s.identifiers.SetStatus(ctx, tx, identifierID, "disabled"); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, eventOf(userID, "unbind", "success", now))
	})
}

// VerifyIdentifier 把标识符置为已验证（幂等：已验过就什么也不做，不报错）。
//
// 为什么幂等而不是 409：验证码可能被重复提交（用户连点、网络重试），
// 报错只会让前端展示一个"其实已经成功"的失败。真正的防重放在 `challenges` 的一次性消费上。
func (s *Service) VerifyIdentifier(ctx context.Context, userID, identifierID uuid.UUID, method string, trust int16) error {
	now := s.cfg.Now().UTC()
	return store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if _, err := s.users.LockUser(ctx, tx, userID); err != nil {
			return err
		}
		row, err := s.identifiers.WithTx(tx).GetByID(ctx, identifierID)
		if err != nil {
			return err
		}
		if row.UserID != userID {
			return ErrIdentifierNotFound
		}
		if row.Verified {
			return nil
		}
		affected, err := s.identifiers.MarkVerified(ctx, identifierID, method, trust, now)
		if err != nil {
			return err
		}
		if affected == 0 {
			// 并发下别人刚验过：按成功处理（幂等）。
			return nil
		}
		return s.appendEvent(ctx, tx, eventOf(userID, "verify_identifier", "success", now))
	})
}

// RevokeAuthenticator 撤销一个持有型凭据（I3/I4：撤销后必须仍有可用认证能力）。
func (s *Service) RevokeAuthenticator(ctx context.Context, userID, authenticatorID uuid.UUID) error {
	now := s.cfg.Now().UTC()
	return store.Tx(ctx, s.db, func(tx *gorm.DB) error {
		if _, err := s.users.LockUser(ctx, tx, userID); err != nil {
			return err
		}
		row, err := s.authenticators.WithTx(tx).GetByID(ctx, authenticatorID)
		if err != nil {
			return err
		}
		if row.UserID != userID {
			return ErrAuthenticatorNotFound
		}
		snap, err := s.Snapshot(ctx, tx, userID)
		if err != nil {
			return err
		}
		if err := identifier.CheckRemoveAuthenticator(snap); err != nil {
			return err
		}
		if _, err := s.authenticators.SetStatus(ctx, tx, authenticatorID, "revoked"); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, eventOf(userID, "revoke_authenticator", "success", now))
	})
}

// appendEvent 追加一条认证事件（绑定/解绑/验证/撤销都留痕：取证靠它，不靠日志）。
func (s *Service) appendEvent(ctx context.Context, tx *gorm.DB, e *model.AuthEvent) error {
	return s.events.WithTx(tx).Append(ctx, e)
}

func eventOf(userID uuid.UUID, eventType, result string, at time.Time) *model.AuthEvent {
	uid := userID
	return &model.AuthEvent{
		UserID:     &uid,
		EventType:  eventType,
		Result:     result,
		AMR:        model.StringArray{},
		OccurredAt: at,
	}
}
