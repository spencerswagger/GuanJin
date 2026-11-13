package guanjin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Principal 是**语言无关的身份视图**（契约 §1.2）：字段与 JWT claim **逐字符相同**
// （序列化一律 snake_case）。
type Principal struct {
	Sub      string   `json:"sub"`
	Uname    string   `json:"uname"`
	Roles    []string `json:"roles"`
	Acr      string   `json:"acr"`
	Amr      []string `json:"amr"`
	Cid      string   `json:"cid"`
	ClientID string   `json:"client_id"`
	Iss      string   `json:"iss"`
	Aud      string   `json:"aud"`
	Iat      int64    `json:"iat"`
	Exp      int64    `json:"exp"`
	Jti      string   `json:"jti"`
}

// HasRole 判断身份是否含某角色（大小写敏感，角色名由业务方定义）。
func (p *Principal) HasRole(role string) bool {
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// HasAcr 判断 acr 是否达到给定等级（`acr` 只认 AAL1/AAL2/AAL3 三个取值，D12）。
func (p *Principal) HasAcr(want string) bool {
	rank := map[string]int{"AAL1": 1, "AAL2": 2, "AAL3": 3}
	return rank[p.Acr] >= rank[want] && rank[want] > 0
}

// Claims 是断言原始 claim（验签通过后的解码结果）。
type Claims struct {
	Iss   string          `json:"iss"`
	Sub   string          `json:"sub"`
	Aud   json.RawMessage `json:"aud"` // 可能是字符串或字符串数组（OIDC 允许两种）
	Cid   string          `json:"cid"`
	Uname string          `json:"uname"`
	Roles []string        `json:"roles"`
	Acr   string          `json:"acr"`
	Amr   []string        `json:"amr"`
	Iat   int64           `json:"iat"`
	Exp   int64           `json:"exp"`
	Jti   string          `json:"jti"`
}

// AudValue 把 aud 归一成字符串：单值原样返回，数组取**最后一个**（本平台只签单值，
// 数组容错只为不被外部实现绊倒）。
func (c *Claims) AudValue() (string, error) {
	if len(c.Aud) == 0 {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(c.Aud, &s); err == nil {
		return s, nil
	}
	var arr []string
	if err := json.Unmarshal(c.Aud, &arr); err == nil {
		if len(arr) == 0 {
			return "", nil
		}
		return arr[len(arr)-1], nil
	}
	return "", fmt.Errorf("aud 既不是字符串也不是字符串数组: %s", string(c.Aud))
}

// Principal 把 claim 转成身份视图。
func (c *Claims) Principal() *Principal {
	aud, _ := c.AudValue()
	return &Principal{
		Sub:      c.Sub,
		Uname:    c.Uname,
		Roles:    append([]string(nil), c.Roles...),
		Acr:      c.Acr,
		Amr:      append([]string(nil), c.Amr...),
		Cid:      c.Cid,
		ClientID: aud,
		Iss:      c.Iss,
		Aud:      aud,
		Iat:      c.Iat,
		Exp:      c.Exp,
		Jti:      c.Jti,
	}
}

// ExpiresAt 返回断言过期时刻。
func (c *Claims) ExpiresAt() time.Time { return time.Unix(c.Exp, 0) }

type principalKey struct{}

// WithPrincipal 把身份写进 context（能力 GJ-CAP-10 的 Go 形态，命名规则 R-3）。
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom 取身份；`ok=false` 表示该路由未受保护（**不注入 nil 身份**，
// 避免业务方误判成"已登录但没角色"）。
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok && p != nil
}
