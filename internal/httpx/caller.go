package httpx

import (
	"context"
	"errors"
)

// Caller 是一次请求解析出来的调用者身份（数据面/SDK 侧的身份入口）。
//
// 只放**验签后可信**的字段：`sub`（用户）、`sid`（会话）、`roles`（角色列表）与原始 claims。
// 权限判定不在这里（平台不做授权，D11/索引 §2）；这里只回答"你是谁"。
type Caller struct {
	Sub    string
	SID    string
	Roles  []string
	Claims map[string]any
}

var (
	// ErrCallerUnauthenticated 表示令牌不可信（签名/算法/格式/过期）。
	ErrCallerUnauthenticated = errors.New("httpx: caller token is not authentic")
	// ErrCallerSessionRevoked 表示令牌本身有效，但对应会话已撤销/过期（E3 语义）。
	ErrCallerSessionRevoked = errors.New("httpx: caller session is revoked or expired")
)

// CallerResolver 把"一个令牌"解成 `Caller`：**验签（SM3withSM2）+ 校验 exp/nbf + 查会话状态**。
//
// 为什么要有这个接口：签发（S6）与验签必须同源——否则"签出来的令牌自己验不过"或
// "验签宽松到能接受伪造"都会成为长期隐患。S8 数据面与 SDK 侧都只消费这个结果。
type CallerResolver interface {
	Resolve(ctx context.Context, token string) (Caller, error)
}
