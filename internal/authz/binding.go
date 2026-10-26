package authz

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/audit"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/observability"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	roleChangeReason = "role_change"
	defaultActorRole = "sec_admin"
)

type RoleEntry struct {
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}

type catalog struct {
	Roles []RoleEntry `json:"roles"`
}

type Deps struct {
	DB        *gorm.DB
	Users     *store.UserRepo
	Clients   *store.ClientRepo
	Snapshots *store.PolicySnapshotRepo
	Audit     *audit.Chain
}

type Service struct {
	db        *gorm.DB
	users     *store.UserRepo
	clients   *store.ClientRepo
	snapshots *store.PolicySnapshotRepo
	audit     *audit.Chain
	now       func() time.Time
}

func New(deps Deps) *Service {
	return &Service{
		db:        deps.DB,
		users:     deps.Users,
		clients:   deps.Clients,
		snapshots: deps.Snapshots,
		audit:     deps.Audit,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func RolesFromPayload(payload []byte) ([]RoleEntry, error) {
	var doc catalog
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, err
	}
	if doc.Roles == nil {
		return []RoleEntry{}, nil
	}
	return doc.Roles, nil
}

func (s *Service) Bind(ctx context.Context, userID uuid.UUID, clientID, role string, grantedBy uuid.UUID, reason string) (uuid.UUID, int64, error) {
	if _, err := s.users.GetByID(ctx, userID); err != nil {
		return uuid.Nil, 0, userError(err)
	}
	if _, err := s.clients.GetByID(ctx, clientID); err != nil {
		return uuid.Nil, 0, clientError(err)
	}
	known, err := s.roleKnown(ctx, clientID, role)
	if err != nil {
		return uuid.Nil, 0, err
	}
	if !known {
		return uuid.Nil, 0, httpx.New("ROLE_UNKNOWN", "角色不在该业务系统的最新策略快照中")
	}

	grantedAt := s.now()
	var bindingID uuid.UUID
	var revoked int64
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.RoleBinding
		findErr := tx.Where("user_id = ? AND client_id = ? AND role = ?", userID, clientID, role).First(&existing).Error
		switch {
		case findErr == nil:
			if existing.Status == "active" {
				return store.ErrBindingAlreadyExists
			}
			updates := map[string]any{
				"status":        "active",
				"granted_by":    nullableUUID(grantedBy),
				"granted_at":    grantedAt,
				"revoked_at":    nil,
				"revoke_reason": nil,
			}
			if err := tx.Model(&model.RoleBinding{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
				return err
			}
			bindingID = existing.ID
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			row := &model.RoleBinding{
				UserID:    userID,
				ClientID:  clientID,
				Role:      role,
				Status:    "active",
				GrantedAt: grantedAt,
			}
			if grantedBy != uuid.Nil {
				row.GrantedBy = &grantedBy
			}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
			bindingID = row.ID
		default:
			return findErr
		}

		res := tx.Model(&model.Session{}).
			Where("user_id = ? AND status = ?", userID, "active").
			Updates(map[string]any{
				"status":        "revoked",
				"revoked_at":    grantedAt,
				"revoke_reason": roleChangeReason,
			})
		if res.Error != nil {
			return res.Error
		}
		revoked = res.RowsAffected
		return nil
	})
	if err != nil {
		return uuid.Nil, 0, bindError(err)
	}

	clientRef := clientID
	targetType := "role_binding"
	targetID := bindingID.String()
	actorRole := defaultActorRole
	after := map[string]any{
		"user_id":   userID.String(),
		"client_id": clientID,
		"role":      role,
		"reason":    reason,
	}
	if _, err := s.audit.Append(ctx, audit.Entry{
		ActorUserID: uuidPtr(grantedBy),
		ActorRole:   &actorRole,
		Action:      "role.bind",
		TargetType:  &targetType,
		TargetID:    &targetID,
		ClientID:    &clientRef,
		After:       after,
		Result:      "success",
		TraceID:     tracePtr(ctx),
	}); err != nil {
		return uuid.Nil, 0, err
	}
	return bindingID, revoked, nil
}

func (s *Service) Unbind(ctx context.Context, bindingID, actor uuid.UUID) (int64, error) {
	var binding model.RoleBinding
	if err := s.db.WithContext(ctx).Where("id = ?", bindingID).First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, httpx.New("BINDING_NOT_FOUND", "绑定不存在")
		}
		return 0, err
	}
	if binding.Status != "active" {
		return 0, httpx.New("BINDING_NOT_FOUND", "绑定不存在或已解绑")
	}

	now := s.now()
	reason := roleChangeReason
	var revoked int64
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.RoleBinding{}).
			Where("id = ? AND status = ?", bindingID, "active").
			Updates(map[string]any{
				"status":        "revoked",
				"revoked_at":    now,
				"revoke_reason": reason,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return store.ErrBindingNotFound
		}
		sessionRes := tx.Model(&model.Session{}).
			Where("user_id = ? AND status = ?", binding.UserID, "active").
			Updates(map[string]any{
				"status":        "revoked",
				"revoked_at":    now,
				"revoke_reason": roleChangeReason,
			})
		if sessionRes.Error != nil {
			return sessionRes.Error
		}
		revoked = sessionRes.RowsAffected
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrBindingNotFound) {
			return 0, httpx.New("BINDING_NOT_FOUND", "绑定不存在或已解绑")
		}
		return 0, err
	}

	clientRef := binding.ClientID
	targetType := "role_binding"
	targetID := binding.ID.String()
	actorRole := defaultActorRole
	before := map[string]any{
		"user_id":   binding.UserID.String(),
		"client_id": binding.ClientID,
		"role":      binding.Role,
		"status":    "active",
	}
	after := map[string]any{
		"user_id":   binding.UserID.String(),
		"client_id": binding.ClientID,
		"role":      binding.Role,
		"status":    "revoked",
	}
	if _, err := s.audit.Append(ctx, audit.Entry{
		ActorUserID: uuidPtr(actor),
		ActorRole:   &actorRole,
		Action:      "role.unbind",
		TargetType:  &targetType,
		TargetID:    &targetID,
		ClientID:    &clientRef,
		Before:      before,
		After:       after,
		Result:      "success",
		TraceID:     tracePtr(ctx),
	}); err != nil {
		return 0, err
	}
	return revoked, nil
}

func (s *Service) roleKnown(ctx context.Context, clientID, role string) (bool, error) {
	snapshot, err := s.snapshots.LatestByClient(ctx, clientID)
	if err != nil {
		if errors.Is(err, store.ErrSnapshotNotFound) {
			return false, nil
		}
		return false, err
	}
	roles, err := RolesFromPayload(snapshot.Payload)
	if err != nil {
		return false, err
	}
	for _, item := range roles {
		if item.Role == role {
			return true, nil
		}
	}
	return false, nil
}

func userError(err error) error {
	if errors.Is(err, store.ErrUserNotFound) {
		return httpx.New("USER_NOT_FOUND", "用户不存在")
	}
	return err
}

func clientError(err error) error {
	if errors.Is(err, store.ErrClientNotFound) {
		return httpx.New("CLIENT_NOT_FOUND", "业务系统不存在")
	}
	return err
}

func bindError(err error) error {
	if errors.Is(err, store.ErrBindingAlreadyExists) {
		return httpx.New("BINDING_ALREADY_EXISTS", "该用户已拥有此角色")
	}
	return err
}

func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func uuidPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func tracePtr(ctx context.Context) *string {
	traceID := observability.FromContext(ctx)
	if traceID == "" {
		return nil
	}
	return &traceID
}
