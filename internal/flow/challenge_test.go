package flow

import (
	"errors"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
)

func TestNewChallenge(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()
	c, err := NewChallenge("ch-1", factor.PurposeLogin, factor.FactorSMSOTP, 5, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("NewChallenge: %v", err)
	}
	if c.Attempts != 0 || c.MaxAttempts != 5 || c.ConsumedAt != nil {
		t.Fatalf("初始状态不对：%+v", c)
	}

	// 空 id / 作废 purpose / 空因子 / 非法 max_attempts 一律拒绝。
	for name, fn := range map[string]func() error{
		"空 id": func() error {
			_, err := NewChallenge("", factor.PurposeLogin, factor.FactorPassword, 5, now)
			return err
		},
		"作废 purpose bind": func() error {
			_, err := NewChallenge("ch", factor.Purpose("bind"), factor.FactorPassword, 5, now)
			return err
		},
		"空 purpose": func() error {
			_, err := NewChallenge("ch", factor.Purpose(""), factor.FactorPassword, 5, now)
			return err
		},
		"空因子": func() error {
			_, err := NewChallenge("ch", factor.PurposeLogin, "", 5, now)
			return err
		},
		"max_attempts=0": func() error {
			_, err := NewChallenge("ch", factor.PurposeLogin, factor.FactorPassword, 0, now)
			return err
		},
	} {
		if err := fn(); err == nil {
			t.Errorf("%s 必须被拒", name)
		}
	}
}

// 判定顺序：过期 → 已消费 → 次数用尽（顺序决定用户看到哪条提示）。
func TestChallengeCheckOrder(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()
	base, err := NewChallenge("ch-1", factor.PurposeLogin, factor.FactorSMSOTP, 3, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("NewChallenge: %v", err)
	}

	if err := base.Check(now); err != nil {
		t.Fatalf("刚创建的挑战应可用：%v", err)
	}
	if err := (Challenge{}).Check(now); !errors.Is(err, ErrChallengeNotFound) {
		t.Fatalf("空挑战应报 ErrChallengeNotFound，实际 %v", err)
	}
	// 过期：now == expires_at 也算过期（边界，`now.Before` 的另一侧）。
	if err := base.Check(now.Add(time.Minute)); !errors.Is(err, ErrChallengeExpired) {
		t.Fatalf("到期时刻应判过期，实际 %v", err)
	}

	consumed := base
	ts := now
	consumed.ConsumedAt = &ts
	if err := consumed.Check(now); !errors.Is(err, ErrChallengeConsumed) {
		t.Fatalf("已消费应报 ErrChallengeConsumed，实际 %v", err)
	}

	exhausted := base
	exhausted.Attempts = 3
	if err := exhausted.Check(now); !errors.Is(err, ErrAttemptsExceeded) {
		t.Fatalf("次数用尽应报 ErrAttemptsExceeded，实际 %v", err)
	}

	// 过期优先于已消费：同时成立时给"过期"（对用户更可解释）。
	both := consumed
	both.ExpiresAt = now.Add(-time.Second)
	if err := both.Check(now); !errors.Is(err, ErrChallengeExpired) {
		t.Fatalf("过期应优先于已消费，实际 %v", err)
	}
}

func TestChallengeFailAndConsume(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()
	c, err := NewChallenge("ch-1", factor.PurposeLogin, factor.FactorPassword, 2, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("NewChallenge: %v", err)
	}

	next, exhausted := c.Fail(now)
	if next.Attempts != 1 || exhausted {
		t.Fatalf("第一次失败应 attempts=1 且未用尽，实际 %+v exhausted=%v", next, exhausted)
	}
	next2, exhausted := next.Fail(now)
	if next2.Attempts != 2 || !exhausted {
		t.Fatalf("第二次失败应到上限并标记用尽，实际 %+v exhausted=%v", next2, exhausted)
	}
	if err := next2.Check(now); !errors.Is(err, ErrAttemptsExceeded) {
		t.Fatalf("用尽后 Check 应报 ErrAttemptsExceeded，实际 %v", err)
	}

	// 消费：成功后 consumed_at 非空；再消费报"已消费"。
	done, err := c.Consume(now)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if done.ConsumedAt == nil || !done.ConsumedAt.Equal(now) {
		t.Fatalf("消费后 consumed_at 应等于 now，实际 %+v", done.ConsumedAt)
	}
	if _, err := done.Consume(now); !errors.Is(err, ErrChallengeConsumed) {
		t.Fatalf("重复消费应报 ErrChallengeConsumed，实际 %v", err)
	}
	// 过期挑战不能消费。
	stale := c
	stale.ExpiresAt = now.Add(-time.Second)
	if _, err := stale.Consume(now); !errors.Is(err, ErrChallengeExpired) {
		t.Fatalf("过期挑战消费应报 ErrChallengeExpired，实际 %v", err)
	}
}

// 哪些 purpose 必须挂流（CH-1 的落点；S5 只做内部能力）。
func TestRequiresFlow(t *testing.T) {
	for _, p := range []factor.Purpose{factor.PurposeLogin, factor.PurposeStepUp} {
		if !RequiresFlow(p) {
			t.Errorf("%s 必须挂流", p)
		}
	}
	for _, p := range []factor.Purpose{factor.PurposeRegister, factor.PurposeEnroll,
		factor.PurposeRecover, factor.PurposeVerifyIdentifier} {
		if RequiresFlow(p) {
			t.Errorf("%s 是无流挑战（一次性动作），不该要求 flow_id", p)
		}
	}
}

// purpose 词表只有一处（factor），编排层不许再抄；作废值必须报错。
func TestParsePurpose(t *testing.T) {
	for _, ok := range []string{"login", "register", "step_up", "enroll", "recover", "verify_identifier"} {
		if _, err := ParsePurpose(ok); err != nil {
			t.Errorf("%q 应合法：%v", ok, err)
		}
	}
	for _, dead := range []string{"bind", "stepup", "", "LOGIN"} {
		if _, err := ParsePurpose(dead); !errors.Is(err, factor.ErrUnknownPurpose) {
			t.Errorf("%q 应报 ErrUnknownPurpose，实际 %v", dead, err)
		}
	}
}

// 码映射：挑战四种失败 + 因子三种不可用 + ACR 不足 + STEP_ALREADY_SATISFIED。
func TestCodeOf(t *testing.T) {
	cases := []struct {
		err  error
		want httpx.Code
	}{
		{nil, ""},
		{&ACRInsufficientError{}, httpx.CodeACRInsufficient},
		{ErrStepAlreadySatisfied, CodeStepAlreadySatisfied},
		{ErrChallengeExpired, httpx.CodeChallengeExpired},
		{ErrChallengeConsumed, httpx.CodeChallengeConsumed},
		{ErrChallengeNotFound, httpx.CodeChallengeInvalid},
		{ErrAttemptsExceeded, httpx.CodeAttemptsExceeded},
		{ErrFactorDisabled, httpx.CodeFactorDisabled},
		{ErrFactorNotSupported, httpx.CodeFactorNotSupported},
		{ErrNoEligibleFactor, httpx.CodeNoEligibleFactor},
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
		if tc.want != CodeStepAlreadySatisfied && !httpx.IsKnownCode(got) {
			t.Errorf("%q 不在冻结码表里", got)
		}
	}
	if _, ok := CodeOf(errors.New("boom")); ok {
		t.Fatal("未知错误不该被映射")
	}
}

func TestACRInsufficientErrorMessage(t *testing.T) {
	e := &ACRInsufficientError{Flow: Descriptor{
		CurrentACR: factor.ACRAAL1, TargetACR: factor.ACRAAL2,
		Steps: []Step{{Status: StepDone}, {Status: StepPending}},
	}}
	if e.Error() == "" {
		t.Fatal("Error() 不得为空（日志要看）")
	}
}
