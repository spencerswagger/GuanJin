package guanjin

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fullConfigForClockSkew 给出一个能过 Validate(EnvProd) 的最小配置（除 clock-skew 外都填齐）。
func fullConfigForClockSkew() map[string]string {
	return map[string]string{
		KeyPlatformBaseURL:     "https://gj.example.com",
		KeyClientID:            "orders",
		KeyClientHMACSecret:    strings.Repeat("a", 32),
		KeyGatewayJwksURI:      "https://gj.example.com/.well-known/jwks.json",
		KeyAuthzReplayStore:    "redis",
		KeyGatewayClockSkew:    "60s",
		KeyGatewayReplayWindow: "60s",
	}
}

// 契约 v16 §8.8 E42：`gateway.clock-skew = 0s` **合法**（= 零容忍），不得以"必须为正"拒绝；
// 且**显式 0 与缺省必须能区分**：缺省 = 默认 60s，显式 "0s" = 0（零容忍），不得回落。
func TestClockSkewZeroIsLegalZeroTolerance(t *testing.T) {
	in := fullConfigForClockSkew()
	in[KeyGatewayClockSkew] = "0s"
	cfg, err := LoadFromMap(in)
	if err != nil {
		t.Fatalf("0s 必须能装载（契约 v16 E42）：%v", err)
	}
	if cfg.Gateway.ClockSkew != 0 {
		t.Fatalf("显式 0s 必须如实生效为 0（零容忍），实得 %v —— 不得回落到默认 %v",
			cfg.Gateway.ClockSkew, DefaultClockSkew)
	}
	if err := cfg.Validate(EnvProd); err != nil {
		t.Fatalf("Validate 不得以\"必须为正\"拒绝 0s（那是实现自造的下界）：%v", err)
	}

	// 缺省 → 默认 60s（与显式 0 区分）
	absent := fullConfigForClockSkew()
	delete(absent, KeyGatewayClockSkew)
	def, err := LoadFromMap(absent)
	if err != nil {
		t.Fatalf("缺省装载失败：%v", err)
	}
	if def.Gateway.ClockSkew != DefaultClockSkew {
		t.Fatalf("缺省必须走默认 %v，实得 %v", DefaultClockSkew, def.Gateway.ClockSkew)
	}

	// 负值仍拒（编程式赋值可绕过解析；负偏斜会放宽 exp 判定 = 安全洞）
	neg := DefaultConfig()
	neg.Platform.BaseURL = "https://gj.example.com"
	neg.Client.ID = "orders"
	neg.Client.HMACSecret = strings.Repeat("a", 32)
	neg.Gateway.JwksURI = "https://gj.example.com/.well-known/jwks.json"
	neg.Authz.ReplayStore = ReplayStoreRedis
	neg.Gateway.ClockSkew = -1 * time.Second
	err = neg.Validate(EnvProd)
	if err == nil {
		t.Fatal("负的 clock-skew 必须失败（负偏斜会放宽 exp 判定）")
	}
	if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), KeyGatewayClockSkew) {
		t.Fatalf("错误应为 ErrConfig 且含键名，实得 %v", err)
	}
}

// 零容忍语义：skew=0 时 exp/iat 按**严格边界**判——exp == now 仍有效、exp < now 即过期；
// iat == now 仍有效、iat > now 即未生效（不做偏斜补偿）。
func TestClockSkewZeroMeansStrictBoundaries(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Platform.BaseURL = "https://gj.example.com"
	cfg.Client.ID = "orders"
	cfg.Gateway.ClockSkew = 0
	v := NewVerifier(cfg, nil)
	now := time.Unix(1_800_000_000, 0)
	v.now = func() time.Time { return now }

	aud, _ := json.Marshal(cfg.Aud())
	claimsAt := func(exp, iat int64) *Claims {
		return &Claims{Iss: cfg.Gateway.Issuer, Aud: aud, Exp: exp, Iat: iat, Jti: "jti-1"}
	}

	// exp == now（+0 补偿）→ 仍有效；exp = now-1 → 过期
	if err := v.ValidateClaims(claimsAt(now.Unix(), now.Unix())); err != nil {
		t.Fatalf("skew=0 时 exp == now 仍应有效（严格边界 exp+skew < now 才过期）：%v", err)
	}
	if err := v.ValidateClaims(claimsAt(now.Unix()-1, now.Unix())); err == nil {
		t.Fatal("skew=0 时 exp = now-1 必须判过期（零容忍不做偏斜补偿）")
	}
	// iat == now → 有效；iat = now+1 → 未生效
	if err := v.ValidateClaims(claimsAt(now.Unix()+600, now.Unix())); err != nil {
		t.Fatalf("skew=0 时 iat == now 仍应有效：%v", err)
	}
	if err := v.ValidateClaims(claimsAt(now.Unix()+600, now.Unix()+1)); err == nil {
		t.Fatal("skew=0 时 iat = now+1 必须判未生效")
	}
	// 反证：默认 60s 时同样两条（now-1 过期、now+1 未生效）都**不**成立，证明上面对照的是 skew=0 的效果
	withSkew := cfg
	withSkew.Gateway.ClockSkew = DefaultClockSkew
	v2 := NewVerifier(withSkew, nil)
	v2.now = func() time.Time { return now }
	if err := v2.ValidateClaims(claimsAt(now.Unix()-1, now.Unix())); err != nil {
		t.Fatalf("默认 60s 时 exp = now-1 应在容许内：%v", err)
	}
	if err := v2.ValidateClaims(claimsAt(now.Unix()+600, now.Unix()+1)); err != nil {
		t.Fatalf("默认 60s 时 iat = now+1 应在容许内：%v", err)
	}
}
