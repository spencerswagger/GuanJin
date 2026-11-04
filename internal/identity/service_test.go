package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

const testRegisterCode = "123456"

func testConfig(t *testing.T) Config {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 11)
	}
	mk, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	idx, err := mk.DeriveHMAC(crypto.PurposeHMACIdentifier)
	if err != nil {
		t.Fatalf("DeriveHMAC: %v", err)
	}
	field, err := mk.DeriveSM4(crypto.PurposeSM4Field)
	if err != nil {
		t.Fatalf("DeriveSM4: %v", err)
	}
	registerKey, err := mk.DeriveHMAC(crypto.PurposeHMACRegister)
	if err != nil {
		t.Fatalf("DeriveHMAC(register): %v", err)
	}
	return Config{
		IndexKey:         idx,
		FieldKey:         field,
		RegisterTokenKey: registerKey,
		PasswordParams:   crypto.PasswordParams{Iterations: crypto.MinPBKDF2Iterations, KeyLen: crypto.MinPasswordKeyLen},
		FlowTTL:          10 * 60 * 1e9, // 10 分钟（显式给，不从代码里取默认值）
		// 挑战参数显式给（真实来源是 runtime_config 的 challenge.* 两键）。
		ChallengeTTL:         5 * time.Minute,
		ChallengeMaxAttempts: 5,
		// 测试里固定验证码，才能走完"拿到码再校验"的链路。
		GenerateCode: func() (string, error) { return testRegisterCode, nil },
	}
}

// 构造期必须 fail-fast：任何一项缺失都不许"运行到一半才发现"。
func TestNewValidatesConfig(t *testing.T) {
	db := &gorm.DB{} // 构造只保存句柄，不查库
	cfg := testConfig(t)

	if _, err := New(nil, cfg); !errors.Is(err, ErrNilDB) {
		t.Fatalf("nil db 应报 ErrNilDB，实际 %v", err)
	}
	if _, err := New(db, cfg); err != nil {
		t.Fatalf("合规配置应通过：%v", err)
	}

	bad := cfg
	bad.IndexKey = crypto.HMACKey{}
	if _, err := New(db, bad); !errors.Is(err, ErrMissingKey) {
		t.Fatalf("缺盲索引键应报 ErrMissingKey，实际 %v", err)
	}
	bad = cfg
	bad.FieldKey = crypto.SM4Key{}
	if _, err := New(db, bad); !errors.Is(err, ErrMissingKey) {
		t.Fatalf("缺字段加密键应报 ErrMissingKey，实际 %v", err)
	}
	bad = cfg
	bad.PasswordParams = crypto.PasswordParams{Iterations: 1000, KeyLen: 32}
	if _, err := New(db, bad); !errors.Is(err, ErrWeakParams) {
		t.Fatalf("弱参数应报 ErrWeakParams，实际 %v", err)
	}
	bad = cfg
	bad.FlowTTL = 0
	if _, err := New(db, bad); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("TTL 为 0 应报 ErrInvalidTTL（不许退化成 600 常量），实际 %v", err)
	}
}

// "信道型不建 authenticators 行"的守卫必须真的挡得住（不是注释）。
func TestCreateAuthenticatorRejectsChannelKind(t *testing.T) {
	svc := &Service{}
	_, err := svc.createAuthenticator(context.Background(), nil, [16]byte{},
		string(factor.FactorSMSOTP), factor.KindChannel, model.JSONValue(`{"a":1}`))
	if !errors.Is(err, ErrChannelHasNoAuthenticator) {
		t.Fatalf("channel 型应被拒，实际 %v", err)
	}
}

func TestPasswordRejectedCarriesViolations(t *testing.T) {
	rej := &PasswordRejected{Result: credential.Result{
		Code: httpx.CodeWeakPassword,
		Violations: []credential.Violation{
			{Code: httpx.CodeWeakPassword, Rule: "weak.common_password"},
			{Code: httpx.CodePasswordPolicyViolation, Rule: "policy.min_length"},
		},
	}}
	if len(rej.Violations()) != 2 {
		t.Fatalf("必须带全量命中项，实际 %d", len(rej.Violations()))
	}
	if code, ok := CodeOf(rej); !ok || code != httpx.CodeWeakPassword {
		t.Fatalf("首个码应为 WEAK_PASSWORD，实际 %q", code)
	}
	if rej.Error() == "" {
		t.Fatal("Error() 不得为空")
	}
}

// 错误 → 对外码的单一出口：表驱动逐条钉住。
func TestCodeOfMapping(t *testing.T) {
	cases := []struct {
		err  error
		want httpx.Code
	}{
		{nil, ""},
		{ErrMissingClientID, httpx.CodeMissingParameter},
		{ErrRegistrationNotAllowed, httpx.CodeRegistrationNotAllowed},
		{ErrPasswordRequired, httpx.CodeWeakPassword},
		{ErrIdentifierNotFound, httpx.CodeIdentifierNotFound},
		{ErrAuthenticatorNotFound, httpx.CodeIdentifierNotFound},
		{store.ErrClientNotFound, httpx.CodeInvalidClient},
		{store.ErrClientDisabled, httpx.CodeInvalidClient},
		{store.ErrUserNotFound, httpx.CodeUserNotFound},
		{store.ErrIdentifierNotFound, httpx.CodeIdentifierNotFound},
		{credential.ErrInvalidCredential, httpx.CodeInvalidCredential},
		{credential.ErrMissingKey, httpx.CodeKeyUnavailable},
	}
	for _, tc := range cases {
		got, ok := CodeOf(tc.err)
		if tc.want == "" {
			if ok {
				t.Errorf("CodeOf(nil) 不该有码，实际 %q", got)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("CodeOf(%v) = (%q,%v)，期望 (%q,true)", tc.err, got, ok, tc.want)
		}
		if !httpx.IsKnownCode(got) {
			t.Errorf("%v 映射出的码 %q 不在冻结码表里", tc.err, got)
		}
	}
	// 非 PG 的未知错误：不猜码（交 HTTP 层 500）。
	if _, ok := CodeOf(errors.New("boom")); ok {
		t.Fatal("未知错误不该被映射")
	}
}

// 档位 → 目标级别映射（D12：L1/L2 → AAL1，L3 → AAL2；AAL3 永不产出）。
func TestTargetACRForTier(t *testing.T) {
	for tier, want := range map[string]string{"L1": "AAL1", "L2": "AAL1", "L3": "AAL2"} {
		got, err := targetACRForTier(tier)
		if err != nil || got != want {
			t.Errorf("targetACRForTier(%s) = (%q,%v)，期望 (%q,nil)", tier, got, err, want)
		}
	}
	if _, err := targetACRForTier("L9"); err == nil {
		t.Fatal("未知档位必须报错（不许默认 AAL1）")
	}
}
