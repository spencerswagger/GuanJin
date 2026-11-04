package protocol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/signing"
)

// SessionStateReader 读会话状态（`active|revoked|expired`）。实现在装配层（`store.SessionRepo`）。
type SessionStateReader interface {
	SessionStatus(ctx context.Context, sid string) (string, error)
}

// ErrSessionGone 表示会话不存在（对外按"已撤销/已失效"处理，不区分，避免枚举）。
var ErrSessionGone = errors.New("protocol: session not found")

// CallerResolver 是 `httpx.CallerResolver` 的实现：验签 → 校验 exp/nbf → 查会话状态。
//
// 顺序有含义：**先验签再查库**——让伪造令牌在花掉一次数据库查询之前就被拒。
type CallerResolver struct {
	Keys     *signing.Set
	Sessions SessionStateReader
	Now      func() time.Time
	// Skew 允许的时钟偏移（`nbf` 提前量；0 表示不宽容）。
	Skew time.Duration
}

// Resolve 见 `httpx.CallerResolver`。
func (r *CallerResolver) Resolve(ctx context.Context, token string) (httpx.Caller, error) {
	if token == "" {
		return httpx.Caller{}, httpx.ErrCallerUnauthenticated
	}
	key, err := r.Keys.For(signing.PurposeOIDC)
	if err != nil {
		return httpx.Caller{}, fmt.Errorf("%w: %v", httpx.ErrCallerUnauthenticated, err)
	}
	claims, err := signing.VerifyJWT(key.Public, token)
	if err != nil {
		return httpx.Caller{}, fmt.Errorf("%w: %v", httpx.ErrCallerUnauthenticated, err)
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	if !withinWindow(claims, now().UTC(), r.Skew) {
		return httpx.Caller{}, fmt.Errorf("%w: token is outside its validity window", httpx.ErrCallerUnauthenticated)
	}
	sub, _ := claims["sub"].(string)
	sid, _ := claims["sid"].(string)
	if sub == "" {
		return httpx.Caller{}, fmt.Errorf("%w: missing sub", httpx.ErrCallerUnauthenticated)
	}
	if sid != "" && r.Sessions != nil {
		status, err := r.Sessions.SessionStatus(ctx, sid)
		if err != nil {
			if errors.Is(err, ErrSessionGone) {
				return httpx.Caller{}, httpx.ErrCallerSessionRevoked
			}
			return httpx.Caller{}, err
		}
		if status != "active" {
			return httpx.Caller{}, httpx.ErrCallerSessionRevoked
		}
	}
	caller := httpx.Caller{Sub: sub, SID: sid, Claims: claims}
	if roles, ok := claims["roles"].([]any); ok {
		for _, v := range roles {
			if s, ok := v.(string); ok {
				caller.Roles = append(caller.Roles, s)
			}
		}
	}
	return caller, nil
}

// ResolveCaller 是数据面/S8 的唯一入口：解析 Bearer 令牌并把失败写成对外码。
//
// **状态码按 D3-E 分工**（不是一律 401）：令牌不可信（缺/坏/过期）→ `TOKEN_INVALID` 401；
// 令牌有效但会话已撤销/过期 → `SESSION_REVOKED` 403。二者是"凭证状态"与"准入状态"的区别。
// 失败响应只回统一四字段（`code/message/trace_id`），**不带内部原因**。
func (s *Server) ResolveCaller(w http.ResponseWriter, r *http.Request) (httpx.Caller, bool) {
	if s.d.Caller == nil {
		s.fail(w, r, httpx.CodeInternalError, "身份解析未装配")
		return httpx.Caller{}, false
	}
	token := bearer(r)
	if token == "" {
		s.fail(w, r, httpx.CodeTokenInvalid, "缺少或无法解析 Bearer 令牌")
		return httpx.Caller{}, false
	}
	caller, err := s.d.Caller.Resolve(r.Context(), token)
	switch {
	case err == nil:
		return caller, true
	case errors.Is(err, httpx.ErrCallerSessionRevoked):
		s.fail(w, r, httpx.CodeSessionRevoked, "会话已失效，请重新登录")
		return httpx.Caller{}, false
	default:
		// 细节只进日志（按 trace_id 可查），对外统一"令牌无效"。
		s.d.Logger.WarnContext(r.Context(), "protocol: caller resolution failed", "trace_id", traceID(r), "error", err)
		s.fail(w, r, httpx.CodeTokenInvalid, "令牌无效或已过期")
		return httpx.Caller{}, false
	}
}

// withinWindow 校验 exp/nbf（iat 不单独拒：它由签发侧写入，时钟偏移由 Skew 吸收）。
func withinWindow(claims map[string]any, now time.Time, skew time.Duration) bool {
	if exp, ok := claims["exp"].(float64); ok {
		if now.Unix() >= int64(exp) {
			return false
		}
	}
	if nbf, ok := claims["nbf"].(float64); ok {
		if now.Add(skew).Unix() < int64(nbf) {
			return false
		}
	}
	return true
}
