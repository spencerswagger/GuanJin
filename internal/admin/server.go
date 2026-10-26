package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/authz"
	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	ctxKeyActorID   = "guanjin_admin_actor_id"
	ctxKeyActorRole = "guanjin_admin_actor_role"
	maxPageSize     = 200
)

type Deps struct {
	Keys      *store.SigningKeyRepo
	Users     *store.UserRepo
	Clients   *store.ClientRepo
	Snapshots *store.PolicySnapshotRepo
	Bindings  *store.RoleBindingRepo
	Authz     *authz.Service
}

type Server struct {
	keys      *store.SigningKeyRepo
	users     *store.UserRepo
	clients   *store.ClientRepo
	snapshots *store.PolicySnapshotRepo
	bindings  *store.RoleBindingRepo
	authz     *authz.Service
}

func New(deps Deps) *Server {
	return &Server{
		keys:      deps.Keys,
		users:     deps.Users,
		clients:   deps.Clients,
		snapshots: deps.Snapshots,
		bindings:  deps.Bindings,
		authz:     deps.Authz,
	}
}

func (s *Server) RegisterRoutes(engine *gin.Engine) {
	read := s.RequireRole("sys_admin", "sec_admin", "auditor")
	write := s.RequireRole("sec_admin")

	group := engine.Group("/api/v1")
	group.GET("/policies", read, s.listSnapshots)
	group.GET("/policies/snapshots/:id", read, s.getSnapshot)
	group.GET("/policies/roles", read, s.listRoles)
	group.GET("/policies/bindings", read, s.listBindings)
	group.POST("/policies/bindings", write, s.createBinding)
	group.DELETE("/policies/bindings/:id", write, s.deleteBinding)
	group.GET("/users/:id/role-bindings", read, s.listUserBindings)
}

func (s *Server) RequireRole(required ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := trimBearer(c.GetHeader("Authorization"))
		if raw == "" {
			httpx.Error(c, httpx.New("TOKEN_INVALID", "缺少 Bearer 令牌"))
			c.Abort()
			return
		}
		_, priv, err := s.keys.ActiveSigner(c.Request.Context(), crypto.PurposeOIDC)
		if err != nil {
			httpx.Error(c, err)
			c.Abort()
			return
		}
		parsed, err := jwt.Parse(raw, func(*jwt.Token) (interface{}, error) {
			return &priv.PublicKey, nil
		}, jwt.WithValidMethods([]string{crypto.AlgoSM3WithSM2}))
		if err != nil {
			if errors.Is(err, jwt.ErrTokenExpired) {
				httpx.Error(c, httpx.New("TOKEN_EXPIRED", "令牌已过期"))
			} else {
				httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌无效"))
			}
			c.Abort()
			return
		}
		claims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌声明无效"))
			c.Abort()
			return
		}
		roles := claimStrings(claims["roles"])
		if len(roles) == 0 {
			httpx.Error(c, httpx.New("INSUFFICIENT_ROLE", "缺少管理角色"))
			c.Abort()
			return
		}
		matched := ""
		for _, role := range required {
			for _, have := range roles {
				if role == have {
					matched = role
					break
				}
			}
			if matched != "" {
				break
			}
		}
		if matched == "" {
			httpx.Error(c, httpx.New("INSUFFICIENT_ROLE", "权限不足"))
			c.Abort()
			return
		}
		sub, _ := claims["sub"].(string)
		c.Set(ctxKeyActorID, sub)
		c.Set(ctxKeyActorRole, matched)
		c.Next()
	}
}

func (s *Server) listSnapshots(c *gin.Context) {
	page, pageSize, err := pagination(c)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	rows, total, err := s.snapshots.List(c.Request.Context(), page, pageSize)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for i := range rows {
		items = append(items, gin.H{
			"snapshot_id": rows[i].ID.String(),
			"client_id":   rows[i].ClientID,
			"version":     rows[i].Version,
			"reported_at": rows[i].ReportedAt.UTC().Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"items":     items,
		"page":      page,
		"page_size": pageSize,
		"total":     total,
	})
}

func (s *Server) getSnapshot(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, httpx.New("SNAPSHOT_NOT_FOUND", "快照不存在"))
		return
	}
	snapshot, err := s.snapshots.GetByID(c.Request.Context(), id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"snapshot_id": snapshot.ID.String(),
		"client_id":   snapshot.ClientID,
		"version":     snapshot.Version,
		"reported_at": snapshot.ReportedAt.UTC().Format(time.RFC3339),
		"payload":     json.RawMessage(snapshot.Payload),
	})
}

func (s *Server) listRoles(c *gin.Context) {
	clientID := strings.TrimSpace(c.Query("client_id"))
	if clientID == "" {
		httpx.Error(c, httpx.New("INVALID_PARAMETER", "缺少 client_id"))
		return
	}
	snapshot, err := s.snapshots.LatestByClient(c.Request.Context(), clientID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	roles, err := authz.RolesFromPayload(snapshot.Payload)
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_PARAMETER", "策略快照不可解析"))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"client_id": clientID,
		"version":   snapshot.Version,
		"roles":     roles,
	})
}

func (s *Server) listBindings(c *gin.Context) {
	var userID *uuid.UUID
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(c, httpx.New("INVALID_PARAMETER", "user_id 无效"))
			return
		}
		userID = &id
	}
	rows, err := s.bindings.List(c.Request.Context(), userID,
		strings.TrimSpace(c.Query("client_id")), strings.TrimSpace(c.Query("role")))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": bindingViews(rows)})
}

func (s *Server) createBinding(c *gin.Context) {
	var req struct {
		UserID   string `json:"user_id"`
		ClientID string `json:"client_id"`
		Role     string `json:"role"`
		Reason   string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, httpx.New("INVALID_REQUEST", "请求参数无效"))
		return
	}
	userID, err := uuid.Parse(strings.TrimSpace(req.UserID))
	if err != nil {
		httpx.Error(c, httpx.New("INVALID_PARAMETER", "user_id 无效"))
		return
	}
	actor, ok := actorID(c)
	if !ok {
		httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌主体无效"))
		return
	}
	bindingID, revoked, err := s.authz.Bind(c.Request.Context(), userID,
		strings.TrimSpace(req.ClientID), strings.TrimSpace(req.Role), actor, req.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"binding_id": bindingID.String(), "revoked_sessions": revoked})
}

func (s *Server) deleteBinding(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, httpx.New("BINDING_NOT_FOUND", "绑定不存在"))
		return
	}
	actor, ok := actorID(c)
	if !ok {
		httpx.Error(c, httpx.New("TOKEN_INVALID", "令牌主体无效"))
		return
	}
	revoked, err := s.authz.Unbind(c.Request.Context(), id, actor)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"binding_id": id.String(), "revoked_sessions": revoked})
}

func (s *Server) listUserBindings(c *gin.Context) {
	userID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, httpx.New("USER_NOT_FOUND", "用户不存在"))
		return
	}
	if _, err := s.users.GetByID(c.Request.Context(), userID); err != nil {
		httpx.Error(c, err)
		return
	}
	rows, err := s.bindings.ListByUser(c.Request.Context(), userID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": bindingViews(rows)})
}

type bindingView struct {
	BindingID    string     `json:"binding_id"`
	UserID       string     `json:"user_id"`
	ClientID     string     `json:"client_id"`
	Role         string     `json:"role"`
	Status       string     `json:"status"`
	GrantedBy    *string    `json:"granted_by,omitempty"`
	GrantedAt    time.Time  `json:"granted_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	RevokeReason *string    `json:"revoke_reason,omitempty"`
}

func bindingViews(rows []model.RoleBinding) []bindingView {
	views := make([]bindingView, 0, len(rows))
	for i := range rows {
		var grantedBy *string
		if rows[i].GrantedBy != nil {
			value := rows[i].GrantedBy.String()
			grantedBy = &value
		}
		views = append(views, bindingView{
			BindingID:    rows[i].ID.String(),
			UserID:       rows[i].UserID.String(),
			ClientID:     rows[i].ClientID,
			Role:         rows[i].Role,
			Status:       rows[i].Status,
			GrantedBy:    grantedBy,
			GrantedAt:    rows[i].GrantedAt.UTC(),
			RevokedAt:    rows[i].RevokedAt,
			RevokeReason: rows[i].RevokeReason,
		})
	}
	return views
}

func pagination(c *gin.Context) (int, int, error) {
	page := 1
	pageSize := 50
	if raw := strings.TrimSpace(c.Query("page")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return 0, 0, httpx.New("INVALID_PARAMETER", "page 无效")
		}
		page = value
	}
	if raw := strings.TrimSpace(c.Query("page_size")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return 0, 0, httpx.New("INVALID_PARAMETER", "page_size 无效")
		}
		if value > maxPageSize {
			return 0, 0, httpx.New("PAGE_SIZE_EXCEEDED", "page_size 超过上限")
		}
		pageSize = value
	}
	return page, pageSize, nil
}

func actorID(c *gin.Context) (uuid.UUID, bool) {
	raw := c.GetString(ctxKeyActorID)
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func trimBearer(header string) string {
	if len(header) > 7 && strings.EqualFold(header[:7], "Bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func claimStrings(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
