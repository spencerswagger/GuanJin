package httpx

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
)

// 本文件是**调用方身份**的唯一接缝（S7 起的自助面与管理面都走它）。
//
// 为什么放在 httpx 而不是各面自己解令牌：令牌解析与验签是 S6 协议面的职责
// （SM3withSM2 验签、`kid`、`exp/iat`、`jti` 重放）。S7 的两组端点只需要三个事实：
// **谁**（user_id）、**哪条会话**（session_id）、**有哪些角色**（roles）。
// 把这三件事定义成一个接口，S7 就能在不碰令牌的前提下被完整测试，
// S6 接线时只需实现 `Resolve`（从 `Authorization: Bearer` 解出这三个值）。
//
// 为什么默认实现是"一律拒绝"：本仓没有"开发模式放行"的暗门（技术栈 §8.3 / 索引 §2）。
// 在 S6 未接线前，端点**必须**以 401 `TOKEN_INVALID` 失败——这是 fail-closed，
// 不是"功能缺失"。

// Caller 是一次请求的调用方身份（由 `CallerResolver` 解析得到）。
type Caller struct {
	// UserID 令牌的 `sub`。
	UserID uuid.UUID
	// SessionID 令牌所属会话（`sid`）；自助面用它判"current"，撤销用它做归属校验。
	SessionID uuid.UUID
	// Roles 该调用方在**当前业务系统**内的有效角色（三员判定用；来源见 `role_bindings`）。
	Roles []string
}

// CallerResolver 把 HTTP 请求解析成调用方身份；解析失败返回错误（由调用方映射成 401）。
type CallerResolver interface {
	Resolve(ctx context.Context, r *http.Request) (*Caller, error)
}

// ErrNoCaller 表示请求没有可用的调用方身份（缺令牌、验签失败、会话不存在……）。
//
// 对外一律 `TOKEN_INVALID`（E2/401）：具体原因写日志，不写响应——
// 告诉客户端"你的签名错了"还是"这个会话不存在"会把探测面白送出去。
var ErrNoCaller = errors.New("httpx: caller cannot be resolved")

// DenyAllResolver 是 S6 接线前的默认实现：任何请求都解析不出身份。
//
// 用它而不是 `nil`：装配点必须显式选一个实现，`nil` 会让 handler 在运行期 panic，
// 而"显式拒绝"是一个可断言的行为（单测直接钉住 401）。
type DenyAllResolver struct{}

// Resolve 永远返回 `ErrNoCaller`。
func (DenyAllResolver) Resolve(context.Context, *http.Request) (*Caller, error) {
	return nil, ErrNoCaller
}

// ResolveCaller 是 handler 的统一入口：解析失败即写出 401 并返回 false。
func ResolveCaller(w http.ResponseWriter, r *http.Request, resolver CallerResolver) (*Caller, bool) {
	if resolver == nil {
		WriteError(w, TraceIDFromContext(r.Context()), CodeTokenInvalid, "缺少有效的访问令牌", nil)
		return nil, false
	}
	caller, err := resolver.Resolve(r.Context(), r)
	if err != nil || caller == nil || caller.UserID == uuid.Nil {
		WriteError(w, TraceIDFromContext(r.Context()), CodeTokenInvalid, "缺少有效的访问令牌", nil)
		return nil, false
	}
	return caller, true
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
