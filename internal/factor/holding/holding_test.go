package holding

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
)

// ---- 测试桩：只实现窄接口，证明"因子不需要数据库也能验" ----

type fakePasswordStore struct {
	envs []credential.PasswordEnvelope
	err  error
}

func (f *fakePasswordStore) ListPasswordEnvelopes(_ context.Context, _ string) ([]credential.PasswordEnvelope, error) {
	return f.envs, f.err
}

type fakeTOTPStore struct {
	recs    []TOTPRecord
	consume bool
	err     error
}

func (f *fakeTOTPStore) ListTOTPRecords(_ context.Context, _ string) ([]TOTPRecord, error) {
	return f.recs, f.err
}

func (f *fakeTOTPStore) ConsumeStep(_ context.Context, _ string, _ int64) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.consume, nil
}

func testKeys(t *testing.T) crypto.SM4Key {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 3)
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

func testPolicy() credential.Policy {
	return credential.Policy{MinLength: 12, CharClasses: 3, HistoryCount: 5, MaxAgeDays: 90}
}

// 能力矩阵（基线 §4.1）在**真实 provider** 上再钉一次（S4a 用测试桩钉过一次）。
func TestProvidersMatchCapabilityMatrix(t *testing.T) {
	pwd, err := NewPassword(&fakePasswordStore{}, testPolicy())
	if err != nil {
		t.Fatalf("NewPassword: %v", err)
	}
	totp, err := NewTOTP(&fakeTOTPStore{}, testKeys(t), time.Now)
	if err != nil {
		t.Fatalf("NewTOTP: %v", err)
	}

	d, err := SelfCheck(pwd)
	if err != nil {
		t.Fatalf("password 自检失败：%v", err)
	}
	if d.Kind != factor.KindHolding {
		t.Fatalf("password 必须是 holding，实际 %s", d.Kind)
	}
	for _, c := range []factor.Capability{factor.CapAuthenticatable, factor.CapACRContributing,
		factor.CapEnrollable, factor.CapRegistrable, factor.CapRevocable, factor.CapRateLimited, factor.CapRiskReporting} {
		if !factor.Has(pwd, c) {
			t.Errorf("password 缺少能力 %s", c)
		}
	}
	// 基线两格硬约束：密码不能找回自己；holding 型不验标识符。
	for _, c := range []factor.Capability{factor.CapRecoverable, factor.CapIdentifierVerifying, factor.CapResettable} {
		if factor.Has(pwd, c) {
			t.Errorf("password 不应具备能力 %s（基线 §4.1）", c)
		}
	}

	d, err = SelfCheck(totp)
	if err != nil {
		t.Fatalf("totp 自检失败：%v", err)
	}
	if d.Authenticatable == nil || d.Authenticatable.AsFirst || !d.Authenticatable.AsSecond {
		t.Fatalf("totp 必须只作第二因子：%+v", d.Authenticatable)
	}
	if !factor.Has(totp, factor.CapRecoverable) {
		t.Fatal("totp 应可参与找回（信任 2）")
	}
	// 基线：TOTP Registrable = ❌（要先绑定，注册里塞二维码很别扭）。
	for _, c := range []factor.Capability{factor.CapRegistrable, factor.CapIdentifierVerifying, factor.CapResettable} {
		if factor.Has(totp, c) {
			t.Errorf("totp 不应具备能力 %s（基线 §4.1）", c)
		}
	}
}

// 装配期自检：两个因子都能进注册表；nil 注册表/nil 因子必须报错。
func TestRegisterAssembly(t *testing.T) {
	pwd, err := NewPassword(&fakePasswordStore{}, testPolicy())
	if err != nil {
		t.Fatalf("NewPassword: %v", err)
	}
	totp, err := NewTOTP(&fakeTOTPStore{}, testKeys(t), time.Now)
	if err != nil {
		t.Fatalf("NewTOTP: %v", err)
	}
	reg := factor.NewRegistry()
	ds, err := Register(reg, pwd, totp)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(ds) != 2 {
		t.Fatalf("应返回 2 份描述符，实际 %d", len(ds))
	}
	if _, err := reg.Lookup(factor.FactorPassword); err != nil {
		t.Fatalf("注册表里应有 password：%v", err)
	}
	// 编排层选因子的两条断言（第一因子 / 第二因子）。
	if got := reg.Authenticatable(factor.RoleFirst); len(got) != 1 || got[0].Type() != factor.FactorPassword {
		t.Fatalf("第一因子应只有 password，实际 %v", got)
	}
	if got := reg.Authenticatable(factor.RoleSecond); len(got) != 1 || got[0].Type() != factor.FactorTOTP {
		t.Fatalf("第二因子应只有 totp，实际 %v", got)
	}

	if _, err := Register(nil, pwd); !errors.Is(err, ErrNilRegistry) {
		t.Fatalf("nil 注册表应报 ErrNilRegistry，实际 %v", err)
	}
	if _, err := Register(factor.NewRegistry(), nil); !errors.Is(err, ErrNilProvider) {
		t.Fatalf("nil 因子应报 ErrNilProvider，实际 %v", err)
	}
}

func TestConstructorsFailFast(t *testing.T) {
	if _, err := NewPassword(nil, testPolicy()); !errors.Is(err, ErrNilStore) {
		t.Fatalf("缺 store 应报 ErrNilStore，实际 %v", err)
	}
	if _, err := NewPassword(&fakePasswordStore{}, credential.Policy{MinLength: 4, CharClasses: 2}); err == nil {
		t.Fatal("非法策略应在构造期报错")
	}
	if _, err := NewTOTP(nil, testKeys(t), time.Now); !errors.Is(err, ErrNilStore) {
		t.Fatalf("缺 store 应报 ErrNilStore，实际 %v", err)
	}
	var zero crypto.SM4Key
	if _, err := NewTOTP(&fakeTOTPStore{}, zero, time.Now); !errors.Is(err, ErrNilSecretKey) {
		t.Fatalf("零值密钥应报 ErrNilSecretKey，实际 %v", err)
	}
	if _, err := NewTOTP(&fakeTOTPStore{}, testKeys(t), nil); !errors.Is(err, ErrNilClock) {
		t.Fatalf("缺时钟应报 ErrNilClock，实际 %v", err)
	}
}

func TestBeginChallengeAndRecovery(t *testing.T) {
	pwd, _ := NewPassword(&fakePasswordStore{}, testPolicy())
	ticket, err := pwd.BeginChallenge(context.Background(), factor.ChallengeRequest{Purpose: factor.PurposeLogin})
	if err != nil {
		t.Fatalf("BeginChallenge: %v", err)
	}
	if ticket.NextAction != factor.NextActionInputPassword {
		t.Fatalf("口令的 next_action 应为 input_password，实际 %s", ticket.NextAction)
	}
	if ticket.ChallengeID != "" {
		t.Fatal("口令是无状态挑战：不该产生 challenge_id（落库由编排层按 next_action 决定）")
	}
	// purpose 必须是 D2 六值之一，空值/作废值一律报错（禁隐性回退）。
	if _, err := pwd.BeginChallenge(context.Background(), factor.ChallengeRequest{}); !errors.Is(err, factor.ErrUnknownPurpose) {
		t.Fatalf("空 purpose 应报 ErrUnknownPurpose，实际 %v", err)
	}
	if _, err := pwd.BeginChallenge(context.Background(), factor.ChallengeRequest{Purpose: factor.Purpose("bind")}); !errors.Is(err, factor.ErrUnknownPurpose) {
		t.Fatalf("作废值 bind 应报 ErrUnknownPurpose，实际 %v", err)
	}

	totp, _ := NewTOTP(&fakeTOTPStore{}, testKeys(t), time.Now)
	ticket, err = totp.BeginRecovery(context.Background(), factor.ChallengeRequest{Purpose: factor.PurposeLogin})
	if err != nil {
		t.Fatalf("BeginRecovery: %v", err)
	}
	if ticket.NextAction != factor.NextActionInputTOTP {
		t.Fatalf("TOTP 的 next_action 应为 input_totp，实际 %s", ticket.NextAction)
	}
}

func TestPasswordVerifyResponse(t *testing.T) {
	env, err := credential.NewPasswordEnvelope("GuanJin#S4b-Strong", crypto.PasswordParams{
		Iterations: crypto.MinPBKDF2Iterations, KeyLen: crypto.MinPasswordKeyLen,
	}, time.Now(), nil)
	if err != nil {
		t.Fatalf("NewPasswordEnvelope: %v", err)
	}
	pwd, err := NewPassword(&fakePasswordStore{envs: []credential.PasswordEnvelope{env}}, testPolicy())
	if err != nil {
		t.Fatalf("NewPassword: %v", err)
	}
	ctx := context.Background()

	res, err := pwd.VerifyResponse(ctx, factor.VerifyRequest{UserID: "u-1", Response: "GuanJin#S4b-Strong"})
	if err != nil {
		t.Fatalf("正确口令应通过：%v", err)
	}
	if res.ACR != factor.ACRAAL1 || res.AMR != "pwd" || res.Factor != factor.FactorPassword {
		t.Fatalf("校验结果不对：%+v", res)
	}

	if _, err := pwd.VerifyResponse(ctx, factor.VerifyRequest{UserID: "u-1", Response: "wrong"}); !errors.Is(err, credential.ErrInvalidCredential) {
		t.Fatalf("错误口令应报 ErrInvalidCredential，实际 %v", err)
	}
	if _, err := pwd.VerifyResponse(ctx, factor.VerifyRequest{UserID: "", Response: "x"}); !errors.Is(err, credential.ErrInvalidCredential) {
		t.Fatalf("缺 userID 应报 ErrInvalidCredential，实际 %v", err)
	}

	// 坏信封（参数被降级）必须**报错**，不许跳过继续试。
	weak := env
	weak.Iterations = 1000
	broken, _ := NewPassword(&fakePasswordStore{envs: []credential.PasswordEnvelope{weak}}, testPolicy())
	if _, err := broken.VerifyResponse(ctx, factor.VerifyRequest{UserID: "u-1", Response: "whatever"}); !errors.Is(err, credential.ErrWeakParams) {
		t.Fatalf("坏信封应报 ErrWeakParams，实际 %v", err)
	}

	// 仓储报错要原样透出（不吞成"凭据错"）。
	boom := errors.New("db down")
	failing, _ := NewPassword(&fakePasswordStore{err: boom}, testPolicy())
	if _, err := failing.VerifyResponse(ctx, factor.VerifyRequest{UserID: "u-1", Response: "x"}); !errors.Is(err, boom) {
		t.Fatalf("仓储错误应透出，实际 %v", err)
	}
}

func TestTOTPVerifyResponseAndReplay(t *testing.T) {
	key := testKeys(t)
	at := time.Unix(1_760_000_000, 0).UTC()
	env, secret, err := credential.NewTOTPEnvelope(key, nil, at)
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	code, err := credential.GenerateTOTP(secret, at, credential.CodeOptions{
		Digits: env.Digits, Period: time.Duration(env.Period) * time.Second,
	})
	if err != nil {
		t.Fatalf("GenerateTOTP: %v", err)
	}

	newProvider := func(consume bool) *TOTPProvider {
		p, err := NewTOTP(&fakeTOTPStore{
			recs:    []TOTPRecord{{AuthenticatorID: "a-1", Envelope: env}},
			consume: consume,
		}, key, func() time.Time { return at })
		if err != nil {
			t.Fatalf("NewTOTP: %v", err)
		}
		return p
	}

	res, err := newProvider(true).VerifyResponse(context.Background(), factor.VerifyRequest{UserID: "u-1", Response: code})
	if err != nil {
		t.Fatalf("正确动态口令应通过：%v", err)
	}
	if res.AMR != "totp" || res.Factor != factor.FactorTOTP || res.ACR != factor.ACRAAL1 {
		t.Fatalf("校验结果不对：%+v", res)
	}

	// 码对但"时间步已被消费" → 必须报重放（这是防重放的唯一保证）。
	if _, err := newProvider(false).VerifyResponse(context.Background(), factor.VerifyRequest{UserID: "u-1", Response: code}); !errors.Is(err, credential.ErrTemporalCodeReused) {
		t.Fatalf("抢不到时间步应报 ErrTemporalCodeReused，实际 %v", err)
	}

	// 错误的码 → 凭据无效。
	if _, err := newProvider(true).VerifyResponse(context.Background(), factor.VerifyRequest{UserID: "u-1", Response: "000000"}); !errors.Is(err, credential.ErrInvalidCredential) {
		t.Fatalf("错误码应报 ErrInvalidCredential，实际 %v", err)
	}

	// 解封失败（密钥不对）→ **完整性失败**（D15④：键在、信封过不了校验 = 500），
	// 既不得当成"码错"（401），也不得伪装成"密钥没装配"（503）。
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(99 - i)
	}
	mk, _ := crypto.NewMasterKey(raw)
	otherKey, _ := mk.DeriveSM4(crypto.PurposeSM4Field)
	wrongKey, _ := NewTOTP(&fakeTOTPStore{recs: []TOTPRecord{{AuthenticatorID: "a-1", Envelope: env}}, consume: true}, otherKey, func() time.Time { return at })
	if _, err := wrongKey.VerifyResponse(context.Background(), factor.VerifyRequest{UserID: "u-1", Response: code}); !errors.Is(err, credential.ErrIntegrityMismatch) {
		t.Fatalf("错误密钥应报 ErrIntegrityMismatch，实际 %v", err)
	}

	// 仓储错误透出。
	boom := errors.New("db down")
	failing, _ := NewTOTP(&fakeTOTPStore{err: boom}, key, func() time.Time { return at })
	if _, err := failing.VerifyResponse(context.Background(), factor.VerifyRequest{UserID: "u-1", Response: code}); !errors.Is(err, boom) {
		t.Fatalf("仓储错误应透出，实际 %v", err)
	}
}

// 多个凭据时：坏码继续试下一条，全部失败才回 INVALID_CREDENTIAL（不能因第一条就放弃）。
func TestTOTPVerifyTriesAllRecords(t *testing.T) {
	key := testKeys(t)
	at := time.Unix(1_760_000_000, 0).UTC()
	envA, secretA, err := credential.NewTOTPEnvelope(key, nil, at)
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	envB, secretB, err := credential.NewTOTPEnvelope(key, nil, at)
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	codeB, err := credential.GenerateTOTP(secretB, at, credential.CodeOptions{
		Digits: envB.Digits, Period: time.Duration(envB.Period) * time.Second,
	})
	if err != nil {
		t.Fatalf("GenerateTOTP: %v", err)
	}
	_ = secretA
	p, err := NewTOTP(&fakeTOTPStore{
		recs: []TOTPRecord{
			{AuthenticatorID: "a-1", Envelope: envA},
			{AuthenticatorID: "b-1", Envelope: envB},
		},
		consume: true,
	}, key, func() time.Time { return at })
	if err != nil {
		t.Fatalf("NewTOTP: %v", err)
	}
	if _, err := p.VerifyResponse(context.Background(), factor.VerifyRequest{UserID: "u-1", Response: codeB}); err != nil {
		t.Fatalf("第二条凭据的码应能通过：%v", err)
	}
}

func TestParseUserID(t *testing.T) {
	if _, err := ParseUserID("not-a-uuid"); !errors.Is(err, credential.ErrInvalidCredential) {
		t.Fatalf("非法 UUID 应报 ErrInvalidCredential，实际 %v", err)
	}
}
