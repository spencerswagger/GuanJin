package identity

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// 本文件是 `register_token` 的**签发与校验**（D14② 冻结的形态）。
//
// 为什么不是一个 `challenge_id`：§3.3.2 的请求体只有 `{register_token, code?, password}`，
// **没有** `client_id`/`type`/`value`，而建号两样都要；`challenges` 也没有任何列能装
// "尚未存在的标识符值"（`identifier_id` 指向还没建的行、`target_masked` 是脱敏值）。
// 所以令牌必须**自包含**，并且把 `cid + typ + val + chid` 一起签进去 ——
// 否则可以拿自己手机上收到的验证码，把别人的号码标成 `verified=true`。
//
// 形态：`v1.<b64url(payload)>.<b64url(mac)>`
//   - payload = `{v,cid,typ,val,chid,exp}`（`exp` 为 unix 秒，**由库时间算出**）；
//   - mac = `HMAC-SM3(K, "guanjin/register-token/v1\n" + b64payload)`；
//   - K 是**独立用途域** `guanjin/hmac/register`（D4-B 一密钥一用途，不复用盲索引键）。
//
// 令牌与口令同级脱敏：**不得进访问日志**（`String()` 只返回 `register_token(<redacted>)`）。

const (
	// registerTokenVersion 是令牌前缀（形态变更必须换版本，禁止"就地改语义"）。
	registerTokenVersion = "v1"
	// registerTokenDomain 是 MAC 的域分隔（防跨用途重放）。
	registerTokenDomain = "guanjin/register-token/v1\n"
)

var (
	// ErrInvalidRegisterToken 表示令牌格式/签名不合规（对外 `INVALID_PARAMETER`，400）。
	ErrInvalidRegisterToken = errors.New("identity: register token is invalid")
	// ErrRegisterTokenExpired 表示注册窗口已过期（对外 `CHALLENGE_EXPIRED`，401）。
	ErrRegisterTokenExpired = errors.New("identity: register token expired")
)

// RegisterClaims 是 `register_token` 的自包含内容。
type RegisterClaims struct {
	// V 版本号（与令牌前缀同源；解码后仍要校验，防止前缀与内容不一致）。
	V int `json:"v"`
	// ClientID 目标业务系统（建号必需）。
	ClientID string `json:"cid"`
	// Type / Value 标识符类型与**规范化后**的值（与 `identifiers` 落库同一套归一化）。
	Type  string `json:"typ"`
	Value string `json:"val"`
	// ChallengeID 该次注册的挑战（用户名免验证路径为空）。
	ChallengeID string `json:"chid,omitempty"`
	// ExpiresAt 过期时刻（unix 秒）。
	ExpiresAt int64 `json:"exp"`
}

// String 只回脱敏占位：令牌一旦进日志就等于把注册窗口交出去。
func (c RegisterClaims) String() string { return "register_token(<redacted>)" }

// sign 生成令牌串。
func (s *Service) sign(c RegisterClaims) (string, error) {
	if c.V != 1 {
		return "", fmt.Errorf("%w: unsupported version %d", ErrInvalidRegisterToken, c.V)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("identity: marshal register claims: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := crypto.HMACSM3(s.cfg.RegisterTokenKey, []byte(registerTokenDomain+payload))
	return registerTokenVersion + "." + payload + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// parseRegisterToken 校验并解出内容。
//
// 顺序：结构 → 版本 → **先验签** → 再解释内容。先验签是为了"畸形输入也走同一条慢路径的
// 常量时间比较"，不让攻击者用畸形 tokend 探测解析细节。过期判定**不在这里**：
// 它要用库时间（D14②），由调用方在拿到 claims 后判。
func parseRegisterToken(key crypto.HMACKey, token string) (*RegisterClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != registerTokenVersion {
		return nil, ErrInvalidRegisterToken
	}
	payloadB64, macB64 := parts[1], parts[2]
	want := crypto.HMACSM3(key, []byte(registerTokenDomain+payloadB64))
	got, err := base64.RawURLEncoding.DecodeString(macB64)
	if err != nil {
		return nil, ErrInvalidRegisterToken
	}
	if !hmac.Equal(want, got) {
		return nil, ErrInvalidRegisterToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, ErrInvalidRegisterToken
	}
	var claims RegisterClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, ErrInvalidRegisterToken
	}
	// 前缀与内容版本必须一致（否则 `v1.` 可以套一个 `v2` 内容，版本号形同虚设）。
	if claims.V != 1 {
		return nil, ErrInvalidRegisterToken
	}
	if claims.ClientID == "" || claims.Type == "" || claims.Value == "" || claims.ExpiresAt <= 0 {
		return nil, ErrInvalidRegisterToken
	}
	return &claims, nil
}

// expired 报告令牌是否已过期（`now` 必须是**库时间**：注册窗口的权威钟在库里）。
func (c RegisterClaims) expired(now time.Time) bool {
	return !now.Before(time.Unix(c.ExpiresAt, 0).UTC())
}
