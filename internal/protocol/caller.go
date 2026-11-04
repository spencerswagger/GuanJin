package protocol

import (
	"context"
	"errors"
	"fmt"
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
