package guanjin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/tjfoc/gmsm/sm2"
)

// SigningAlg 是唯一允许的签名算法（契约 §4.1 M2：`alg` **不做协商降级**）。
const SigningAlg = "SM3withSM2"

// sm2UID 是 GM/T 0003 的默认用户标识；平台与 SDK 必须一致，否则验签永远失败。
var sm2UID = []byte("1234567812345678")

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// Verifier 做**内部断言**（`X-GuanJin-Assertion`）的验签与 claim 校验
// （能力 GJ-CAP-01 / 05 / 06）。
type Verifier struct {
	cfg  Config
	jwks *JwksClient
	now  func() time.Time // 便于测试注入时钟
}

// NewVerifier 构造验签器。
func NewVerifier(cfg Config, jwks *JwksClient) *Verifier {
	return &Verifier{cfg: cfg, jwks: jwks, now: time.Now}
}

// Verify 校验签名并返回 claim（能力 GJ-CAP-01）。
//
// 只做「签名是否来自平台」：claim 的业务校验在 ValidateClaims 里（分开是为了
// 让「篡改令牌仍 401」这条反向断言能单独测到——验签必须在剥离之前发生）。
func (v *Verifier) Verify(ctx context.Context, raw string) (*Claims, error) {
	_ = ctx // 预留：超时/取消贯穿验签链路（M2 冻结签名 `Verify(ctx, raw)`；当前实现无外呼）
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, NewTokenInvalid("缺少内部断言", nil)
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, NewTokenInvalid("断言不是三段式 JWT", nil)
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, NewTokenInvalid("断言头不是 base64url", err)
	}
	var hdr jwtHeader
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil {
		return nil, NewTokenInvalid("断言头不是合法 JSON", err)
	}
	if hdr.Alg != SigningAlg {
		// 不做算法协商：alg 不是 SM3withSM2 一律拒（防 alg 混淆攻击）。
		return nil, NewTokenInvalid(fmt.Sprintf("alg 必须是 %s，得到 %q", SigningAlg, hdr.Alg), nil)
	}
	if hdr.Kid == "" {
		return nil, NewTokenInvalid("断言头缺 kid", nil)
	}
	pub, err := v.jwks.PublicKey(hdr.Kid)
	if err != nil {
		return nil, err // 已是 *Error（TOKEN_INVALID 或 KEY_UNAVAILABLE）
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, NewTokenInvalid("签名不是 base64url", err)
	}
	if len(sig) != 64 {
		return nil, NewTokenInvalid(fmt.Sprintf("SM2 签名必须是 r||s 共 64 字节，得到 %d", len(sig)), nil)
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	signed := []byte(parts[0] + "." + parts[1])
	if !sm2.Sm2Verify(pub, signed, sm2UID, r, s) {
		return nil, NewTokenInvalid("签名校验失败", nil)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, NewTokenInvalid("断言载荷不是 base64url", err)
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, NewTokenInvalid("断言载荷不是合法 JSON", err)
	}
	return &c, nil
}

// ValidateClaims 校验 `exp`/`iat`（含时钟偏移）与 `iss`/`aud`（能力 GJ-CAP-05 / 06）。
func (v *Verifier) ValidateClaims(c *Claims) error {
	now := v.now().Unix()
	skew := int64(v.cfg.Gateway.ClockSkew / time.Second)
	if c.Iss != v.cfg.Gateway.Issuer {
		return NewTokenInvalid(fmt.Sprintf("iss 不符（期望 %q）", v.cfg.Gateway.Issuer), nil)
	}
	aud, err := c.AudValue()
	if err != nil {
		return NewTokenInvalid("aud 非法", err)
	}
	if aud != v.cfg.Aud() {
		return NewTokenInvalid(fmt.Sprintf("aud 不符（期望 %q）", v.cfg.Aud()), nil)
	}
	if c.Exp == 0 || c.Exp+skew < now {
		return NewTokenInvalid("断言已过期", nil)
	}
	if c.Iat != 0 && c.Iat-skew > now {
		return NewTokenInvalid("断言尚未生效（iat 在未来）", nil)
	}
	if c.Jti == "" {
		return NewTokenInvalid("断言缺 jti（无法做重放检测）", nil)
	}
	return nil
}
