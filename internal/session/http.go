package session

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是**会话自助面**的 HTTP 入口（端点名照抄接口清单 §2.6 / §3.6 的冻结名）：
//
//	GET    /api/v1/me/sessions         查看我在各业务系统的在线会话
//	DELETE /api/v1/me/sessions/{id}    踢掉指定会话
//	DELETE /api/v1/me/sessions         变体 `?scope=all`：一次踢掉我的全部会话
//
// 与 `internal/flow/http.go` 同一套写法：handler 只解码 → 调 Manager → 按码表写出，
// **不做业务判断**（归属校验、幂等、级联全在 Manager）。
//
// 身份从 `httpx.CallerResolver` 来（S6 接线后由令牌解析实现；未接线时一律 401，fail-closed）。

// HTTPHandler 是自助面三个端点的处理器。
type HTTPHandler struct {
	mgr    *Manager
	caller httpx.CallerResolver
	logger *slog.Logger
}

// NewHTTPHandler 构造处理器（缺依赖即装配期失败）。
func NewHTTPHandler(mgr *Manager, caller httpx.CallerResolver, logger *slog.Logger) (*HTTPHandler, error) {
	switch {
	case mgr == nil:
		return nil, ErrNilDependency
	case caller == nil:
		// 显式要求一个 resolver（哪怕是否决式实现 `httpx.DenyAllResolver{}`）：
		// nil 会在运行期变成 panic，而装配期报错能让人看见"忘了接线"。
		return nil, ErrNilDependency
	case logger == nil:
		return nil, ErrNilDependency
	}
	return &HTTPHandler{mgr: mgr, caller: caller, logger: logger}, nil
}

// Routes 挂上三个冻结路径（`DELETE /api/v1/me/sessions` 是 `{id}` 之外的变体）。
func (h *HTTPHandler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/me/sessions", h.list)
	mux.HandleFunc("DELETE /api/v1/me/sessions/{id}", h.revokeOne)
	mux.HandleFunc("DELETE /api/v1/me/sessions", h.revokeAll)
}

// sessionView 是列表项（接口 §3.6.1 的字段）。
//
// `client` 嵌成对象（`{id,name}`）而不是平铺两个字段：页面要按业务系统分组展示，
// 平铺会让前端从"两个字段拼一个对象"变成唯一做法。
type sessionView struct {
	ID     string     `json:"id"`
	Client clientView `json:"client"`
	// CreatedAt 用 `sessions.created_at`（DB 时间），**不是** `issued_at`：
	// 两条列在同一事务里写入，差异只在时钟来源；契约字段名是 created_at，就取那一列。
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	IP         *string    `json:"ip"`
	Device     *string    `json:"device"`
	ACR        string     `json:"acr"`
	AMR        []string   `json:"amr"`
	Current    bool       `json:"current"`
}

type clientView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// listResponse 是 D3-A′ 的分页形态：`{items, next_cursor}`，**没有 total**。
type listResponse struct {
	Items      []sessionView `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request) {
	caller, ok := httpx.ResolveCaller(w, r, h.caller)
	if !ok {
		return
	}
	page, err := httpx.ParsePage(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	rows, err := h.mgr.ListByUser(r.Context(), caller.UserID, page.Limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	names, err := h.mgr.ClientNames(r.Context(), clientIDsOf(rows))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]sessionView, 0, len(rows))
	for i := range rows {
		items = append(items, viewOf(&rows[i], names, caller.SessionID))
	}
	// 自助面按用户全量取（一页上限 200），`next_cursor` 恒为 null：
	// 契约 §3.6.1 只说"分页参数"，而 D3-A′ 要求响应里必须**明确**给出"还有没有更多"。
	writeJSON(w, h.logger, http.StatusOK, listResponse{Items: items, NextCursor: nil})
}

func (h *HTTPHandler) revokeOne(w http.ResponseWriter, r *http.Request) {
	caller, ok := httpx.ResolveCaller(w, r, h.caller)
	if !ok {
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		// 畸形 id 与"不存在"同码：不向客户端泄漏解析细节。
		h.fail(w, r, ErrSessionNotFound)
		return
	}

	// 归属校验：只能撤销 `sub` 自己的会话（接口 §3.6.2）。
	target, err := h.mgr.Validate(r.Context(), id)
	if err != nil {
		// 已撤销/已过期也走这里：对调用方而言"这条会话已经不在线"，
		// 但他仍需要知道**这条会话不是他的**（否则 id 可被逐条探测）。
		if errors.Is(err, ErrSessionExpired) || errors.Is(err, ErrSessionRevoked) {
			owner, getErr := h.ownerOf(r, id)
			if getErr != nil {
				h.fail(w, r, getErr)
				return
			}
			if owner != caller.UserID {
				h.fail(w, r, ErrSessionNotOwned)
				return
			}
			writeNoContent(w)
			return
		}
		h.fail(w, r, err)
		return
	}
	if target.UserID != caller.UserID {
		h.fail(w, r, ErrSessionNotOwned)
		return
	}
	err = h.mgr.Revoke(r.Context(), RevokeInput{
		SessionID: id,
		Reason:    ReasonLogout,
		EventType: EventLogout,
		TraceID:   traceIDPtr(r),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeNoContent(w)
}

func (h *HTTPHandler) revokeAll(w http.ResponseWriter, r *http.Request) {
	caller, ok := httpx.ResolveCaller(w, r, h.caller)
	if !ok {
		return
	}
	// 契约只有一个变体 `?scope=all`；其它取值是请求参数问题，不静默忽略。
	if scope := strings.TrimSpace(r.URL.Query().Get("scope")); scope != "all" {
		httpx.WriteError(w, traceID(r), httpx.CodeInvalidParam,
			"scope 只支持 all（一次撤销本人全部会话）", nil)
		return
	}
	if _, err := h.mgr.RevokeAllByUser(r.Context(), caller.UserID, ReasonLogoutAll,
		EventLogout, traceIDPtr(r)); err != nil {
		h.fail(w, r, err)
		return
	}
	writeNoContent(w)
}

// ownerOf 读会话主体（供"已非 active"的归属判定用）。
func (h *HTTPHandler) ownerOf(r *http.Request, id uuid.UUID) (uuid.UUID, error) {
	s, err := h.mgr.Get(r.Context(), id)
	if err != nil {
		return uuid.Nil, err
	}
	return s.UserID, nil
}

// fail 按码表写出错误；无法映射的错误一律 500 并留日志（不猜"是不是依赖挂了"）。
func (h *HTTPHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	code := CodeOf(err)
	if code == "" {
		switch {
		case errors.Is(err, httpx.ErrInvalidLimit):
			code = httpx.CodeInvalidParam
		case errors.Is(err, httpx.ErrPageSizeExceeded):
			code = httpx.CodePageSizeExceeded
		default:
			h.logger.Error("session: unhandled error", "trace_id", traceID(r), "error", err)
			httpx.WriteError(w, traceID(r), httpx.CodeInternalError, "服务内部错误，请稍后重试", nil)
			return
		}
	}
	httpx.WriteError(w, traceID(r), code, messageOf(code), nil)
}

// messageOf 是面向用户的中文提示（按码给，**不含内部细节**）。
func messageOf(c httpx.Code) string {
	switch c {
	case httpx.CodeSessionNotFound:
		return "会话不存在或已结束"
	case httpx.CodeSessionNotOwned:
		return "该会话不属于当前账号"
	case httpx.CodeSessionRevoked:
		return "该会话已被撤销，请重新登录"
	case httpx.CodeTokenExpired:
		return "登录已超时，请重新登录"
	case httpx.CodeTokenInvalid:
		return "缺少有效的访问令牌"
	case httpx.CodePageSizeExceeded:
		return "每页条数超过上限（最多 200）"
	case httpx.CodeInvalidParam:
		return "请求参数不合法"
	default:
		return "请求无法完成"
	}
}

func viewOf(s *store.SessionRecord, names map[string]string, current uuid.UUID) sessionView {
	amr := []string(s.AMR)
	if amr == nil {
		amr = []string{}
	}
	return sessionView{
		ID:         s.ID.String(),
		Client:     clientView{ID: s.ClientID, Name: names[s.ClientID]},
		CreatedAt:  s.CreatedAt,
		LastSeenAt: s.LastSeenAt,
		IP:         maskIP(s.IP),
		Device:     s.DeviceID,
		ACR:        s.ACR,
		AMR:        amr,
		Current:    s.ID == current,
	}
}

// maskIP 按"合规展示"脱敏（接口 §3.6.1：IP/设备脱敏或按合规展示）。
//
// 规则：IPv4 只保留前两段（`203.0.***.***`）；IPv6 只保留前两组。
// 为什么不整条隐藏：用户要靠"哪个网段"认出是不是自己（"我不在那个城市"是判断被盗的关键线索）。
func maskIP(raw *string) *string {
	if raw == nil {
		return nil
	}
	ip := net.ParseIP(*raw)
	if ip == nil {
		// 非法值原样返回空串而不是原文：原文可能是任意字符串，直接回显等于回显注入面。
		empty := ""
		return &empty
	}
	var masked string
	if v4 := ip.To4(); v4 != nil {
		masked = fmt.Sprintf("%d.%d.***.***", v4[0], v4[1])
	} else {
		v6 := ip.To16()
		masked = fmt.Sprintf("%x:%x::/****", binary.BigEndian.Uint16(v6[0:2]), binary.BigEndian.Uint16(v6[2:4]))
	}
	return &masked
}

func clientIDsOf(rows []store.SessionRecord) []string {
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

func writeNoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

func writeJSON(w http.ResponseWriter, logger *slog.Logger, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Warn("session: write response failed", "error", err)
	}
}
