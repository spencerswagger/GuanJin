package identity

import (
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// `register_token` 的签发/校验（D14②）：自包含 + 签名 + 版本 + 过期判定。
func newTokenKey(t *testing.T) crypto.HMACKey {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 3)
	}
	mk, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	key, err := mk.DeriveHMAC(crypto.PurposeHMACRegister)
	if err != nil {
		t.Fatalf("DeriveHMAC: %v", err)
	}
	return key
}

func testService(t *testing.T) *Service {
	t.Helper()
	return &Service{cfg: testConfig(t)}
}

func sampleClaims() RegisterClaims {
	return RegisterClaims{
		V:           1,
		ClientID:    "c-1",
		Type:        "email",
		Value:       "a@example.com",
		ChallengeID: "0192f0a0-0000-7000-8000-0000000000aa",
		ExpiresAt:   1_760_000_300,
	}
}

// 往返：内容一字不差（`val` 必须是**规范化后**的值，调用方负责传进来）。
func TestRegisterTokenRoundTrip(t *testing.T) {
	svc := testService(t)
	token, err := svc.sign(sampleClaims())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, err := parseRegisterToken(svc.cfg.RegisterTokenKey, token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.ClientID != "c-1" || got.Type != "email" || got.Value != "a@example.com" {
		t.Fatalf("claims 不一致：%+v", got)
	}
	if got.ChallengeID != "0192f0a0-0000-7000-8000-0000000000aa" {
		t.Fatalf("chid 未绑定：%+v", got)
	}
	// 令牌与口令同级脱敏：String() 不得回原文。
	if got.String() == token || got.String() == "" {
		t.Fatalf("String() 必须是脱敏占位，实际 %q", got.String())
	}
}

// 篡改 payload / 换密钥 / 版本不符 / 结构畸形 → 一律 `ErrInvalidRegisterToken`。
func TestRegisterTokenRejectsTampering(t *testing.T) {
	svc := testService(t)
	token, err := svc.sign(sampleClaims())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parts := splitToken(t, token)

	cases := map[string]string{
		"改 payload":    parts[0] + ".eyJ2IjoxfQ." + parts[2],
		"改 mac":        parts[0] + "." + parts[1] + "." + flipLast(parts[2]),
		"版本不符":         "v2." + parts[1] + "." + parts[2],
		"少一段":          parts[0] + "." + parts[1],
		"mac 非 base64": parts[0] + "." + parts[1] + ".!!!",
		"空串":           "",
	}
	for name, bad := range cases {
		if _, err := parseRegisterToken(svc.cfg.RegisterTokenKey, bad); err == nil {
			t.Fatalf("%s 必须被判无效", name)
		}
	}

	// 换一把密钥（另一个用途域）也必须无效。
	other := newTokenKeyOther(t)
	if _, err := parseRegisterToken(other, token); err == nil {
		t.Fatal("换密钥必须判无效（令牌与外键不同用途域）")
	}
}

// 内容缺字段（cid/typ/val/exp）也算无效：不允许"空 client_id 的令牌"。
func TestRegisterTokenRequiresFields(t *testing.T) {
	svc := testService(t)
	bad := sampleClaims()
	bad.ClientID = ""
	if _, err := svc.sign(bad); err != nil {
		t.Fatalf("sign 不应因内容缺失失败（校验在解析侧）：%v", err)
	}
}

// 过期判定用调用方给的**库时间**（不在解析里判，避免两套钟）。
func TestRegisterClaimsExpired(t *testing.T) {
	c := sampleClaims()
	at := time.Unix(c.ExpiresAt, 0).UTC()
	if c.expired(at.Add(-time.Second)) {
		t.Fatal("到期前不应判过期")
	}
	if !c.expired(at) {
		t.Fatal("到期时刻应判过期（与 challenges.expires_at 同口径：!now.Before(expires)）")
	}
	if !c.expired(at.Add(time.Second)) {
		t.Fatal("到期后应判过期")
	}
}

// 版本号必须显式是 1：`v2` 内容套 `v1` 前缀也不行。
func TestSignRejectsUnknownVersion(t *testing.T) {
	svc := testService(t)
	c := sampleClaims()
	c.V = 2
	if _, err := svc.sign(c); err == nil {
		t.Fatal("未知版本必须拒绝签发")
	}
}

func splitToken(t *testing.T, token string) []string {
	t.Helper()
	parts := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	parts = append(parts, token[start:])
	if len(parts) != 3 {
		t.Fatalf("令牌段数应为 3，实际 %d（%q）", len(parts), token)
	}
	return parts
}

func flipLast(s string) string {
	if s == "" {
		return "A"
	}
	last := s[len(s)-1]
	if last == 'A' {
		return s[:len(s)-1] + "B"
	}
	return s[:len(s)-1] + "A"
}

// newTokenKeyOther 派生**另一个用途域**的键（盲索引键），用于"换密钥必须无效"。
func newTokenKeyOther(t *testing.T) crypto.HMACKey {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 3)
	}
	mk, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	key, err := mk.DeriveHMAC(crypto.PurposeHMACIdentifier)
	if err != nil {
		t.Fatalf("DeriveHMAC: %v", err)
	}
	return key
}
