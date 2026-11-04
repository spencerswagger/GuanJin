package store

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// RoleBindingRepo 读 `role_bindings`（S7 的三员最小骨架 + S6 签发令牌时的角色来源）。
//
// 只提供**读**：绑定的授予/撤销是管理面（安全管理员）的写操作，属 M5 的 RBAC 面；
// 本切片只需要"这个用户在这个业务系统里有哪些有效角色"。
//
// 为什么角色按 `(user_id, client_id)` 查：`role_bindings` 的 `client_id` 是 NOT NULL
// （角色是业务系统定义的），平台侧没有"全局角色"这种行——三员的判定因此也带 client 维度。
type RoleBindingRepo struct{ db *gorm.DB }

// NewRoleBindingRepo 绑定连接。
func NewRoleBindingRepo(db *gorm.DB) *RoleBindingRepo { return &RoleBindingRepo{db: db} }

// RolesForUserClient 返回该用户在该业务系统内的**有效**角色（`status='active'`），去重后升序。
//
// 只读 `role` 列、不读 `granted_by`/`revoked_at`：调用方要的是"能做什么"，不是绑定流水。
func (r *RoleBindingRepo) RolesForUserClient(ctx context.Context, userID uuid.UUID, clientID string) ([]string, error) {
	if userID == uuid.Nil || strings.TrimSpace(clientID) == "" {
		return nil, errors.New("store: roles lookup requires user_id and client_id")
	}
	var roles []string
	err := r.db.WithContext(ctx).Raw(
		`SELECT DISTINCT role FROM role_bindings
		  WHERE user_id = ? AND client_id = ? AND status = 'active'
		  ORDER BY role`, userID, clientID).Scan(&roles).Error
	if err != nil {
		return nil, MapError(err)
	}
	if roles == nil {
		roles = []string{}
	}
	return roles, nil
}
