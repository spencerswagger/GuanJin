package session

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是**令牌 → 调用方身份**的唯一入口（S7 的两组端点共用）。
//
// 验签由 S6 提供（`httpx.CallerResolver`：只接受 `SM3withSM2`、校验 `exp`/`nbf`、
// 并查会话状态），本包**只做两件事**：
//  1. 从 `Authorization: Bearer <token>` 取出令牌串（缺失/畸形 → 401 `TOKEN_INVALID`）；
//  2. 把 `httpx.Caller`（`Sub`/`SID`/`Roles` 都是字符串）折成本包要的 uuid 形态。
//
// 为什么不自己验签：签发与验签必须同源（否则"签出来的自己验不过"或"验得太松"都是长期隐患），
// 这也是 spencer 把 `CallerResolver` 判给 S6 的原因。

// TokenResolver 是本包需要的最小验签能力面（实现是 S6 的 `protocol.CallerResolver`）。
type TokenResolver interface {
	Resolve(ctx context.Context, token string) (httpx.Caller, error)
}

// Caller 是解析成功后的调用方（uuid 形态，供 handler 直接使用）。
//
// 导出的原因：管理面（`internal/admin`）要复用同一套解析与错误分工，
// 否则两处各写一份"Bearer 怎么取、撤销了回什么码"，迟早不一致。
type Caller struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
	Roles     []string
}

// HasRole 报告调用方是否持有某角色（大小写敏感：角色名是数据，不是枚举常量）。
func (c *Caller) HasRole(role string) bool {
	if c == nil {
		return false
	}
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// resolveCaller 解析请求的调用方；失败时**已写出错误响应**，返回 false。
//
// 错误分工（D3-E）：
//   - 令牌不可信（缺失/验签失败/过期）→ 401 `TOKEN_INVALID`；
//   - 令牌有效但会话已撤销/过期 → 403 `SESSION_REVOKED`（S6 侧用 `ErrCallerSessionRevoked` 表达）。
func ResolveCaller(w http.ResponseWriter, r *http.Request, resolver TokenResolver) (*Caller, bool) {
	if resolver == nil {
		httpx.WriteError(w, traceID(r), httpx.CodeTokenInvalid, "缺少有效的访问令牌", nil)
		return nil, false
	}
	token := bearerToken(r)
	if token == "" {
		httpx.WriteError(w, traceID(r), httpx.CodeTokenInvalid, "缺少有效的访问令牌", nil)
		return nil, false
	}
	caller, err := resolver.Resolve(r.Context(), token)
	if err != nil {
		if errors.Is(err, httpx.ErrCallerSessionRevoked) {
			httpx.WriteError(w, traceID(r), httpx.CodeSessionRevoked, "该会话已被撤销，请重新登录", nil)
			return nil, false
		}
		httpx.WriteError(w, traceID(r), httpx.CodeTokenInvalid, "缺少有效的访问令牌", nil)
		return nil, false
	}
	userID, err := uuid.Parse(strings.TrimSpace(caller.Sub))
	if err != nil || userID == uuid.Nil {
		// 验签通过但 `sub` 不是 uuid：属令牌内容与平台契约不符，按不可信处理。
		httpx.WriteError(w, traceID(r), httpx.CodeTokenInvalid, "缺少有效的访问令牌", nil)
		return nil, false
	}
	var sid uuid.UUID
	if raw := strings.TrimSpace(caller.SID); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.WriteError(w, traceID(r), httpx.CodeTokenInvalid, "缺少有效的访问令牌", nil)
			return nil, false
		}
		sid = parsed
	}
	return &Caller{UserID: userID, SessionID: sid, Roles: caller.Roles}, true
}

// bearerToken 取 `Authorization: Bearer <token>` 里的令牌串；没有就返回空串。
//
// 只认 `Bearer`（大小写不敏感）：其它认证方案不被静默当作令牌，否则"Basic xxx"会被送去验签。
func bearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if raw == "" {
		return ""
	}
	const prefix = "bearer "
	if len(raw) <= len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(raw[len(prefix):])
}
