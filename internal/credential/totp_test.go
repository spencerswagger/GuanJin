package credential

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/httpx"
)

// testSM4Key 按 D4-B 派生一把 SM4 键（测试也不许自造用途域）。
func testSM4Key(t *testing.T) crypto.SM4Key {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	mk, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	key, err := mk.DeriveSM4(crypto.PurposeSM4Field)
	if err != nil {
		t.Fatalf("DeriveSM4: %v", err)
	}
	return key
}

func TestNewTOTPEnvelopeAndOpen(t *testing.T) {
	key := testSM4Key(t)
	now := time.Now()
	env, secret, err := NewTOTPEnvelope(key, nil, now)
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	if len(secret) != MinTOTPSecretBytes {
		t.Fatalf("共享密钥应为 %d 字节，实际 %d", MinTOTPSecretBytes, len(secret))
	}
	if env.Algo != TOTPAlgo || env.Hash != TOTPHashSM3 || env.Digits != 6 || env.Period != 30 {
		t.Fatalf("信封默认形态不对: %+v", env)
	}
	if env.SecretEnc == "" || strings.Contains(env.SecretEnc, string(secret)) {
		t.Fatalf("secret_enc 必须是密文（不得出现明文密钥）")
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	opened, err := env.Open(key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if string(opened) != string(secret) {
		t.Fatal("解封后应与原密钥一致")
	}
	// 两次生成必须不同（随机密钥）。
	env2, secret2, err := NewTOTPEnvelope(key, nil, now)
	if err != nil {
		t.Fatalf("NewTOTPEnvelope(2): %v", err)
	}
	if string(secret2) == string(secret) || env2.SecretEnc == env.SecretEnc {
		t.Fatal("两次绑定的共享密钥必须不同")
	}
}

func TestTOTPMissingKeyFailsClosed(t *testing.T) {
	var zero crypto.SM4Key
	if _, _, err := NewTOTPEnvelope(zero, nil, time.Now()); !errors.Is(err, ErrMissingKey) {
		t.Fatalf("零值键必须报 ErrMissingKey，实际 %v", err)
	}
	env, _, err := NewTOTPEnvelope(testSM4Key(t), nil, time.Now())
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	if _, err := env.Open(zero); !errors.Is(err, ErrMissingKey) {
		t.Fatalf("零值键解封必须报 ErrMissingKey，实际 %v", err)
	}
}

// 密钥不对时解封必须报错（KEY_UNAVAILABLE），**绝不**返回半截明文或"跳过"。
func TestTOTPOpenWithWrongKey(t *testing.T) {
	env, _, err := NewTOTPEnvelope(testSM4Key(t), nil, time.Now())
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(200 - i)
	}
	mk, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	other, err := mk.DeriveSM4(crypto.PurposeSM4Field)
	if err != nil {
		t.Fatalf("DeriveSM4: %v", err)
	}
	// D15④：键是给了的、解开失败 → **完整性失败**（数据/密钥不匹配的缺陷，500），
	// 不是"密钥没装配"（503）。两者混在一个码里会让运维一直等一个不会好的故障。
	if _, err := env.Open(other); !errors.Is(err, ErrIntegrityMismatch) {
		t.Fatalf("错误密钥解封应报 ErrIntegrityMismatch，实际 %v", err)
	}
	if code, ok := CodeOf(ErrIntegrityMismatch); !ok || code != httpx.CodeInternalError {
		t.Fatalf("完整性失败应映射 INTERNAL_ERROR，实际 %q", code)
	}
	// 键缺失仍是 503 语义。
	if _, err := env.Open(crypto.SM4Key{}); !errors.Is(err, ErrMissingKey) {
		t.Fatalf("零值键应报 ErrMissingKey，实际 %v", err)
	}
	if code, ok := CodeOf(ErrMissingKey); !ok || code != httpx.CodeKeyUnavailable {
		t.Fatalf("键缺失应映射 KEY_UNAVAILABLE，实际 %q", code)
	}
	// 篡改任一字节也必须被拒（ADR-005 的反向断言，与 D15④ 同一条链路）。
	sealed, _ := base64.StdEncoding.DecodeString(env.SecretEnc)
	sealed[len(sealed)-1] ^= 0x01
	env.SecretEnc = base64.StdEncoding.EncodeToString(sealed)
	if _, err := env.Open(testSM4Key(t)); !errors.Is(err, ErrIntegrityMismatch) {
		t.Fatalf("篡改密文应报 ErrIntegrityMismatch，实际 %v", err)
	}
}

func TestTOTPEnvelopeValidateRejects(t *testing.T) {
	env, _, err := NewTOTPEnvelope(testSM4Key(t), nil, time.Now())
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*TOTPEnvelope)
		want error
	}{
		// 合规硬约束：SHA 系 HMAC 一律不许（索引 §2）。
		{"hash 用 SHA1", func(e *TOTPEnvelope) { e.Hash = "SHA1" }, ErrUnknownAlgo},
		{"hash 为空", func(e *TOTPEnvelope) { e.Hash = "" }, ErrUnknownAlgo},
		{"algo 不对", func(e *TOTPEnvelope) { e.Algo = "HOTP" }, ErrUnknownAlgo},
		{"位数太少", func(e *TOTPEnvelope) { e.Digits = 5 }, ErrInvalidOptions},
		{"位数太多", func(e *TOTPEnvelope) { e.Digits = 9 }, ErrInvalidOptions},
		{"周期太短", func(e *TOTPEnvelope) { e.Period = 5 }, ErrInvalidOptions},
		{"周期太长", func(e *TOTPEnvelope) { e.Period = 600 }, ErrInvalidOptions},
		{"密文为空", func(e *TOTPEnvelope) { e.SecretEnc = "" }, ErrMalformedEnvelope},
		{"密文不是 base64", func(e *TOTPEnvelope) { e.SecretEnc = "!!!" }, ErrMalformedEnvelope},
		{"bound_at 不是 RFC3339", func(e *TOTPEnvelope) { e.BoundAt = "昨天" }, ErrMalformedEnvelope},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := env
			tc.mut(&e)
			if err := e.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("期望 %v，实际 %v", tc.want, err)
			}
		})
	}
	// 解封前的坏信封也必须被拦。
	bad := env
	bad.Hash = "SHA256"
	if _, err := bad.Open(testSM4Key(t)); !errors.Is(err, ErrUnknownAlgo) {
		t.Fatalf("坏信封不得进入解封，实际 %v", err)
	}
}

func TestTOTPMarshalParse(t *testing.T) {
	env, _, err := NewTOTPEnvelope(testSM4Key(t), nil, time.Now())
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	raw, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := ParseTOTPEnvelope(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if back.SecretEnc != env.SecretEnc {
		t.Fatal("往返后密文应一致")
	}
	bad := env
	bad.Digits = 3
	if _, err := bad.Marshal(); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("非法信封不得序列化，实际 %v", err)
	}
	if _, err := ParseTOTPEnvelope([]byte("nope")); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("坏 JSON 应报 ErrMalformedEnvelope，实际 %v", err)
	}
	if _, err := ParseTOTPEnvelope([]byte(`{"algo":"TOTP","hash":"SHA256","digits":6,"period":30,"secret_enc":"AAAA"}`)); !errors.Is(err, ErrUnknownAlgo) {
		t.Fatalf("解析即校验：SHA256 必须被拒，实际 %v", err)
	}
}

// RFC 6238 的行为断言：码必须是 6 位数字、同一时间步稳定、跨步变化。
func TestGenerateTOTP(t *testing.T) {
	secret := []byte("12345678901234567890") // RFC 4226 附录 D 的经典种子
	at := time.Unix(1_760_000_000, 0).UTC()
	opt := CodeOptions{Digits: 6, Period: 30 * time.Second}

	code, err := GenerateTOTP(secret, at, opt)
	if err != nil {
		t.Fatalf("GenerateTOTP: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("码长度应为 6，实际 %q", code)
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			t.Fatalf("码必须是十进制数字，实际 %q", code)
		}
	}
	// 同一时间步内稳定（差 1 秒仍在同一 30s 窗口）。
	again, err := GenerateTOTP(secret, at.Add(time.Second), opt)
	if err != nil || again != code {
		t.Fatalf("同一时间步内应稳定：%q vs %q (%v)", again, code, err)
	}
	// 下一时间步必须变（否则等于码可长期复用）。
	next, err := GenerateTOTP(secret, at.Add(30*time.Second), opt)
	if err != nil {
		t.Fatalf("GenerateTOTP(next): %v", err)
	}
	if next == code {
		t.Fatalf("跨时间步必须换码（%q）", next)
	}
	// 8 位也要支持（零填充）。
	long, err := GenerateTOTP(secret, at, CodeOptions{Digits: 8, Period: 30 * time.Second})
	if err != nil || len(long) != 8 {
		t.Fatalf("8 位码应可用：%q %v", long, err)
	}
}

func TestGenerateTOTPRejectsBadParams(t *testing.T) {
	secret := []byte("12345678901234567890")
	at := time.Unix(1_760_000_000, 0).UTC()
	if _, err := GenerateTOTP(secret, at, CodeOptions{Digits: 4, Period: 30 * time.Second}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("位数越界应报 ErrInvalidOptions，实际 %v", err)
	}
	if _, err := GenerateTOTP(secret, at, CodeOptions{Digits: 6, Period: time.Second}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("周期越界应报 ErrInvalidOptions，实际 %v", err)
	}
	if _, err := GenerateTOTP([]byte("short"), at, CodeOptions{Digits: 6, Period: 30 * time.Second}); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("短密钥应报 ErrInvalidSecret，实际 %v", err)
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret := []byte("12345678901234567890")
	at := time.Unix(1_760_000_000, 0).UTC()
	codeOpt := CodeOptions{Digits: 6, Period: 30 * time.Second}
	code, err := GenerateTOTP(secret, at, codeOpt)
	if err != nil {
		t.Fatalf("GenerateTOTP: %v", err)
	}
	verifyOpt := TOTPOptions{Digits: 6, Period: 30 * time.Second, Skew: 1}

	res, err := VerifyTOTP(secret, code, at, verifyOpt)
	if err != nil {
		t.Fatalf("当前步应校验通过：%v", err)
	}
	if res.Drift != 0 {
		t.Fatalf("当前步 Drift 应为 0，实际 %d", res.Drift)
	}
	if res.MatchedStep != at.Unix()/30 {
		t.Fatalf("MatchedStep 应为 %d，实际 %d", at.Unix()/30, res.MatchedStep)
	}

	// 上一窗口的码：skew=1 应通过且 Drift=-1（容忍客户端时钟慢）。
	prevCode, err := GenerateTOTP(secret, at.Add(-30*time.Second), codeOpt)
	if err != nil {
		t.Fatalf("GenerateTOTP(prev): %v", err)
	}
	res, err = VerifyTOTP(secret, prevCode, at, verifyOpt)
	if err != nil {
		t.Fatalf("上一窗口应通过（skew=1）：%v", err)
	}
	if res.Drift != -1 {
		t.Fatalf("Drift 应为 -1，实际 %d", res.Drift)
	}

	// skew=0 时上一窗口必须失败（窗口不放宽就等于没放开）。
	if _, err := VerifyTOTP(secret, prevCode, at, TOTPOptions{Digits: 6, Period: 30 * time.Second, Skew: 0}); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("skew=0 时窗口外的码应报 ErrInvalidCredential，实际 %v", err)
	}

	// 错的码。
	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	if _, err := VerifyTOTP(secret, wrong, at, verifyOpt); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("错误码应报 ErrInvalidCredential，实际 %v", err)
	}
	// 形态不对。
	if _, err := VerifyTOTP(secret, "12345", at, verifyOpt); !errors.Is(err, ErrMalformedCode) {
		t.Fatalf("长度不符应报 ErrMalformedCode，实际 %v", err)
	}
	if _, err := VerifyTOTP(secret, "12345a", at, verifyOpt); !errors.Is(err, ErrMalformedCode) {
		t.Fatalf("非数字应报 ErrMalformedCode，实际 %v", err)
	}
	// 参数越界。
	if _, err := VerifyTOTP(secret, code, at, TOTPOptions{Digits: 6, Period: 30 * time.Second, Skew: MaxTOTPSkewSteps + 1}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("skew 越界应报 ErrInvalidOptions，实际 %v", err)
	}
	if _, err := VerifyTOTP([]byte("short"), code, at, verifyOpt); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("短密钥应报 ErrInvalidSecret，实际 %v", err)
	}
}

// 重放防护：同一个时间步只能用一次（同窗重放与陈旧码都拒）。
func TestReplayStateAccept(t *testing.T) {
	var st ReplayState // 从未使用
	next, err := st.Accept(100)
	if err != nil {
		t.Fatalf("首次使用应通过：%v", err)
	}
	if next.LastUsedStep != 100 {
		t.Fatalf("状态应推进到 100，实际 %d", next.LastUsedStep)
	}
	if _, err := next.Accept(100); !errors.Is(err, ErrTemporalCodeReused) {
		t.Fatalf("同一窗口二次使用必须报 ErrTemporalCodeReused，实际 %v", err)
	}
	if _, err := next.Accept(99); !errors.Is(err, ErrTemporalCodeReused) {
		t.Fatalf("更早的步（陈旧码）必须报 ErrTemporalCodeReused，实际 %v", err)
	}
	if _, err := next.Accept(101); err != nil {
		t.Fatalf("更晚的步应通过：%v", err)
	}
}

func TestDriftOrder(t *testing.T) {
	got := driftOrder(2)
	want := []int{0, -1, 1, -2, 2}
	if len(got) != len(want) {
		t.Fatalf("driftOrder(2) = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("driftOrder(2) = %v，期望 %v", got, want)
		}
	}
	if d := driftOrder(0); len(d) != 1 || d[0] != 0 {
		t.Fatalf("driftOrder(0) 应只有 0，实际 %v", d)
	}
}

func TestHOTPDeterminism(t *testing.T) {
	secret := []byte("12345678901234567890")
	a, err := hotp(secret, 42, 6)
	if err != nil {
		t.Fatalf("hotp: %v", err)
	}
	b, err := hotp(secret, 42, 6)
	if err != nil || a != b {
		t.Fatalf("同一输入必须得到同一码：%q vs %q (%v)", a, b, err)
	}
	if _, err := hotp(nil, 42, 6); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("空密钥应报 ErrInvalidSecret，实际 %v", err)
	}
}

func TestTOTPEnvelopeRedaction(t *testing.T) {
	env, _, err := NewTOTPEnvelope(testSM4Key(t), nil, time.Now())
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	for _, out := range []string{env.String(), env.GoString()} {
		if strings.Contains(out, env.SecretEnc) {
			t.Fatalf("脱敏失败：%s", out)
		}
	}
}

// Open 路径上"密文解出来太短"也要拦（防止有人塞一段短密文冒充密钥）。
func TestTOTPOpenShortSecret(t *testing.T) {
	key := testSM4Key(t)
	env, _, err := NewTOTPEnvelope(key, nil, time.Now())
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	sealed, err := crypto.EncryptSM4(key, []byte("short"))
	if err != nil {
		t.Fatalf("EncryptSM4: %v", err)
	}
	env.SecretEnc = base64.StdEncoding.EncodeToString(sealed)
	if _, err := env.Open(key); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("解出短密钥应报 ErrInvalidSecret，实际 %v", err)
	}
}
