package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是**管理面 sessions 段**的 HTTP 入口（端点名照抄接口 §3.8 sessions 段）：
//
//	GET    /api/v1/sessions              查看全部在线会话（三员皆可读）
//	DELETE /api/v1/sessions/{id}         强制下线指定会话（系统管理员/安全管理员）
//	DELETE /api/v1/sessions?user_id=…    撤销某用户全部会话（系统管理员/安全管理员）
//
// 三件事刻意不在 handler 里做：
//   - **鉴权判定**：全部走 `AuthorizeSessions`（矩阵只有一处，否则"某个端点忘了拦审计员"）；
//   - **撤销副作用**：级联作废流 + 写 `auth_events` 都在 `session.Manager`；
//   - **"用户不存在"** 的判定走仓储存在性查询，不用"撤销 0 条"来推断——
//     0 条既可能是用户不存在，也可能是用户存在但没有活跃会话，两者对外码不同。

// ErrNilDependency 表示装配期缺依赖。
var ErrNilDependency = errors.New("admin: nil dependency")

// SessionAdmin 是 sessions 管理面需要的能力面（实现是 `*session.Manager`）。
type SessionAdmin interface {
	ListAll(ctx context.Context, q store.SessionQuery) (*store.SessionPage, error)
	ClientNames(ctx context.Context, clientIDs []string) (map[string]string, error)
	Revoke(ctx context.Context, in session.RevokeInput) error
	RevokeAllByUser(ctx context.Context, userID uuid.UUID, reason session.RevokeReason,
		eventType string, traceID *string) (int64, error)
	UserExists(ctx context.Context, id uuid.UUID) (bool, error)
}

// SessionsHTTP 是 sessions 段三个端点的处理器。
type SessionsHTTP struct {
	svc    SessionAdmin
	caller httpx.CallerResolver
	logger *slog.Logger
}

// NewSessionsHTTP 构造处理器（缺依赖即装配期失败）。
func NewSessionsHTTP(svc SessionAdmin, caller httpx.CallerResolver, logger *slog.Logger) (*SessionsHTTP, error) {
	switch {
	case svc == nil, caller == nil, logger == nil:
		return nil, ErrNilDependency
	}
	return &SessionsHTTP{svc: svc, caller: caller, logger: logger}, nil
}

// Routes 挂上三个冻结路径。
func (h *SessionsHTTP) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/sessions", h.list)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", h.revokeOne)
	mux.HandleFunc("DELETE /api/v1/sessions", h.revokeByUser)
}

// adminSessionView 是管理面列表项：自助面的字段 + `user_id`（管理员要按人排查）。
type adminSessionView struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Client struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"client"`
	Status     string     `json:"status"`
	ACR        string     `json:"acr"`
	AMR        []string   `json:"amr"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	IssuedAt   time.Time  `json:"issued_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	// RevokeReason 对管理员可见：排障时"为什么被踢"比"被踢了"更重要。
	RevokeReason *string `json:"revoke_reason"`
	IP           *string `json:"ip"`
	Device       *string `json:"device"`
}

type adminListResponse struct {
	Items      []adminSessionView `json:"items"`
	NextCursor *string            `json:"next_cursor"`
}

func (h *SessionsHTTP) list(w http.ResponseWriter, r *http.Request) {
	caller, ok := httpx.ResolveCaller(w, r, h.caller)
	if !ok {
		return
	}
	if err := AuthorizeSessions(caller, false); err != nil {
		h.fail(w, r, err)
		return
	}
	page, err := httpx.ParsePage(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	q := store.SessionQuery{Limit: page.Limit, Cursor: page.Cursor}

	if raw := strings.TrimSpace(r.URL.Query().Get("user_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.WriteError(w, traceID(r), httpx.CodeInvalidParam, "user_id 不合法", nil)
			return
		}
		q.UserID = &id
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("client_id")); raw != "" {
		cid := raw
		q.ClientID = &cid
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("active")); raw != "" {
		// 只认 true/false 两个字面量：`active=1` 这种"看起来像"的取值静默当 false 会
		// 把"只看在线"变成"看全部"，审计场景下就是漏看。
		v, err := strconv.ParseBool(raw)
		if err != nil {
			httpx.WriteError(w, traceID(r), httpx.CodeInvalidParam, "active 只支持 true/false", nil)
			return
		}
		q.Active = &v
	}

	result, err := h.svc.ListAll(r.Context(), q)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	names, err := h.svc.ClientNames(r.Context(), clientIDs(result.Items))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	items := make([]adminSessionView, 0, len(result.Items))
	for i := range result.Items {
		items = append(items, adminViewOf(&result.Items[i], names))
	}
	resp := adminListResponse{Items: items}
	if result.NextCursor != "" {
		next := result.NextCursor
		resp.NextCursor = &next
	}
	writeJSON(w, h.logger, http.StatusOK, resp)
}

func (h *SessionsHTTP) revokeOne(w http.ResponseWriter, r *http.Request) {
	caller, ok := httpx.ResolveCaller(w, r, h.caller)
	if !ok {
		return
	}
	if err := AuthorizeSessions(caller, true); err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		httpx.WriteError(w, traceID(r), httpx.CodeSessionNotFound, "会话不存在或已结束", nil)
		return
	}
	err = h.svc.Revoke(r.Context(), session.RevokeInput{
		SessionID: id,
		Reason:    session.ReasonAdmin,
		EventType: session.EventRevoke,
		TraceID:   traceIDPtr(r),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SessionsHTTP) revokeByUser(w http.ResponseWriter, r *http.Request) {
	caller, ok := httpx.ResolveCaller(w, r, h.caller)
	if !ok {
		return
	}
	if err := AuthorizeSessions(caller, true); err != nil {
		h.fail(w, r, err)
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if raw == "" {
		httpx.WriteError(w, traceID(r), httpx.CodeMissingParameter,
			"缺少 user_id（撤销某用户全部会话）", nil)
		return
	}
	userID, err := uuid.Parse(raw)
	if err != nil {
		httpx.WriteError(w, traceID(r), httpx.CodeInvalidParam, "user_id 不合法", nil)
		return
	}
	exists, err := h.svc.UserExists(r.Context(), userID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !exists {
		httpx.WriteError(w, traceID(r), httpx.CodeUserNotFound, "用户不存在", nil)
		return
	}
	if _, err := h.svc.RevokeAllByUser(r.Context(), userID, session.ReasonAdmin,
		session.EventRevoke, traceIDPtr(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fail 按码表写出错误；未映射的一律 500 并留日志。
func (h *SessionsHTTP) fail(w http.ResponseWriter, r *http.Request, err error) {
	if code := CodeOf(err); code != "" {
		httpx.WriteError(w, traceID(r), code, adminMessageOf(code), nil)
		return
	}
	if code := session.CodeOf(err); code != "" {
		httpx.WriteError(w, traceID(r), code, adminMessageOf(code), nil)
		return
	}
	if code := httpx.CodeOfPageError(err); code != "" {
		httpx.WriteError(w, traceID(r), code, adminMessageOf(code), nil)
		return
	}
	h.logger.Error("admin: unhandled sessions error", "trace_id", traceID(r), "error", err)
	httpx.WriteError(w, traceID(r), httpx.CodeInternalError, "服务内部错误，请稍后重试", nil)
}

// adminMessageOf 是管理面的中文提示（按码给，面向运维人员）。
func adminMessageOf(c httpx.Code) string {
	switch c {
	case httpx.CodeInsufficientRole:
		return "当前账号没有该操作所需的角色"
	case httpx.CodeThreePowerViolation:
		return "该操作超出当前角色的职责范围（审计员不得执行写操作）"
	case httpx.CodeSessionNotFound:
		return "会话不存在或已结束"
	case httpx.CodeUserNotFound:
		return "用户不存在"
	case httpx.CodePageSizeExceeded:
		return "每页条数超过上限（最多 200）"
	case httpx.CodeInvalidParam:
		return "请求参数不合法"
	case httpx.CodeMissingParameter:
		return "缺少必需参数"
	default:
		return "请求无法完成"
	}
}

func adminViewOf(s *model.Session, names map[string]string) adminSessionView {
	amr := []string(s.AMR)
	if amr == nil {
		amr = []string{}
	}
	v := adminSessionView{
		ID:           s.ID.String(),
		UserID:       s.UserID.String(),
		Status:       s.Status,
		ACR:          s.ACR,
		AMR:          amr,
		CreatedAt:    s.CreatedAt,
		LastSeenAt:   s.LastSeenAt,
		IssuedAt:     s.IssuedAt,
		ExpiresAt:    s.ExpiresAt,
		RevokedAt:    s.RevokedAt,
		RevokeReason: s.RevokeReason,
		IP:           s.IP,
		Device:       s.DeviceID,
	}
	v.Client.ID = s.ClientID
	v.Client.Name = names[s.ClientID]
	return v
}

func clientIDs(rows []model.Session) []string {
	seen := make(map[string]struct{}, len(rows))
	out := make([]string, 0, len(rows))
	for i := range rows {
		if _, ok := seen[rows[i].ClientID]; ok {
			continue
		}
		seen[rows[i].ClientID] = struct{}{}
		out = append(out, rows[i].ClientID)
	}
	return out
}

func traceID(r *http.Request) string { return httpx.TraceIDFromContext(r.Context()) }

func traceIDPtr(r *http.Request) *string {
	id := traceID(r)
	if id == "" {
		return nil
	}
	return &id
}

func writeJSON(w http.ResponseWriter, logger *slog.Logger, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Warn("admin: write response failed", "error", err)
	}
}
