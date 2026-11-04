package flow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 账号级锁定（D3-D / AC-LOGIN-03）：已锁定 → `ACCOUNT_LOCKED`(401)，且**不再算一次 PBKDF2**。
func TestVerifyStepLockedAccountRejectsBeforeVerify(t *testing.T) {
	st := &fakeStore{}
	verifier := &fakeVerifier{resp: factor.VerifyResult{Factor: factor.FactorPassword, AMR: "pwd", ACR: factor.ACRAAL1}}
	guard := &fakeGuard{threshold: 3, locked: map[string]bool{}}
	orc := newOrc(t, st, verifier, func(d *Deps) { d.Guard = guard })

	begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	uid := uuid.New()
	st.flow.UserID = &uid
	guard.locked[uid.String()] = true

	_, err = orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "pw",
	})
	if !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("已锁定应报 ErrAccountLocked，实际 %v", err)
	}
	if code, ok := CodeOf(err); !ok || code != httpx.CodeAccountLocked {
		t.Fatalf("应映射 ACCOUNT_LOCKED，实际 %q", code)
	}
	if verifier.calls != 0 {
		t.Fatal("已锁定时不得调用因子（否则等于让攻击者继续烧 CPU）")
	}
}

// 凭据错误计数：达阈值的那一次回 ACCOUNT_LOCKED（而不是继续回 INVALID_CREDENTIAL）。
func TestVerifyStepCountsCredentialFailures(t *testing.T) {
	st := &fakeStore{}
	guard := &fakeGuard{threshold: 2}
	orc := newOrc(t, st, &fakeVerifier{err: credential.ErrInvalidCredential}, func(d *Deps) {
		d.Guard = guard
		// 阈值来自策略（`login_policies.fail_threshold`）：这里显式给 2，不靠代码里的数字。
		d.Lockout = func(context.Context, string) (LockoutPolicy, error) {
			return LockoutPolicy{FailThreshold: 2, LockDuration: 15 * time.Minute}, nil
		}
	})
	begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	uid := uuid.New()
	st.flow.UserID = &uid

	// 第一次：计数 +1，未锁定 → 仍是 INVALID_CREDENTIAL。
	_, err = orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "bad",
	})
	if !errors.Is(err, credential.ErrInvalidCredential) {
		t.Fatalf("第一次失败应回 INVALID_CREDENTIAL，实际 %v", err)
	}
	if guard.recorded != 1 {
		t.Fatalf("应记一次失败，实际 %d", guard.recorded)
	}

	// 第二次：达阈值 → ACCOUNT_LOCKED。
	_, err = orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "bad",
	})
	if !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("达阈值应回 ACCOUNT_LOCKED，实际 %v", err)
	}
	if guard.recorded != 2 {
		t.Fatalf("应记两次失败，实际 %d", guard.recorded)
	}
}

// 业务性拒绝不计数（"该步已完成"是点错按钮，不是攻击信号）。
func TestVerifyStepBusinessRejectionDoesNotCount(t *testing.T) {
	st := &fakeStore{}
	guard := &fakeGuard{threshold: 3}
	orc := newOrc(t, st, &fakeVerifier{}, func(d *Deps) { d.Guard = guard })
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	uid := uuid.New()
	st.flow.UserID = &uid
	st.flow.AchievedAMR = model.StringArray{"pwd"}

	if _, err := orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "pw",
	}); !errors.Is(err, ErrStepAlreadySatisfied) {
		t.Fatalf("应报 STEP_ALREADY_SATISFIED，实际 %v", err)
	}
	if guard.recorded != 0 {
		t.Fatalf("业务性拒绝不得计入失败数，实际 %d", guard.recorded)
	}
}

// 成功即清零（接口 §3.2.2：成功时全局清零）。
func TestVerifyStepSuccessResetsCounters(t *testing.T) {
	st := &fakeStore{}
	guard := &fakeGuard{threshold: 3}
	orc := newOrc(t, st, &fakeVerifier{resp: factor.VerifyResult{Factor: factor.FactorPassword, AMR: "pwd", ACR: factor.ACRAAL1}},
		func(d *Deps) { d.Guard = guard })
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	uid := uuid.New()
	st.flow.UserID = &uid

	if _, err := orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "pw",
	}); err != nil {
		t.Fatalf("VerifyStep: %v", err)
	}
	if guard.resets != 1 {
		t.Fatalf("成功应清零一次，实际 %d", guard.resets)
	}
}

// 匿名流（未绑定用户）不计数、不锁定：没有账号可锁，如实跳过而不是编造次数。
func TestVerifyStepAnonymousFlowSkipsCounters(t *testing.T) {
	st := &fakeStore{}
	guard := &fakeGuard{threshold: 3}
	orc := newOrc(t, st, &fakeVerifier{err: credential.ErrInvalidCredential}, func(d *Deps) { d.Guard = guard })
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})

	if _, err := orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "bad",
	}); !errors.Is(err, credential.ErrInvalidCredential) {
		t.Fatalf("应回 INVALID_CREDENTIAL，实际 %v", err)
	}
	if guard.recorded != 0 || guard.statuses != 0 {
		t.Fatalf("匿名流不该计数：recorded=%d statuses=%d", guard.recorded, guard.statuses)
	}
}

// 计数不可用（Redis 挂）→ 错误原样上抛，装配层翻成 `REDIS_UNAVAILABLE`（fail-closed）。
func TestVerifyStepRiskUnavailablePropagates(t *testing.T) {
	st := &fakeStore{}
	boom := errors.New("redis is down")
	guard := &fakeGuard{threshold: 3, err: boom}
	orc := newOrc(t, st, &fakeVerifier{}, func(d *Deps) { d.Guard = guard })
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	uid := uuid.New()
	st.flow.UserID = &uid

	if _, err := orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "pw",
	}); !errors.Is(err, boom) {
		t.Fatalf("应原样上抛计数错误，实际 %v", err)
	}
}

// 下发挑战：已锁定 → ACCOUNT_LOCKED；已绑定用户 → 回真实剩余次数；匿名 → 不适用（0）。
func TestBeginChallengeAttemptsLeftFromGuard(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()

	t.Run("已绑定用户回真实剩余次数", func(t *testing.T) {
		st := &fakeStore{}
		guard := &fakeGuard{threshold: 5, fails: map[string]int{}}
		verifier := &fakeVerifier{ticket: factor.ChallengeTicket{NextAction: factor.NextActionInputPassword}}
		orc := newOrc(t, st, verifier, func(d *Deps) {
			d.Guard = guard
			d.Lockout = func(context.Context, string) (LockoutPolicy, error) {
				return LockoutPolicy{FailThreshold: 5, LockDuration: 15 * time.Minute}, nil
			}
		})
		begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
		uid := uuid.New()
		st.flow.UserID = &uid
		st.flow.ExpiresAt = now.Add(10 * time.Minute)
		guard.fails[uid.String()] = 2

		got, err := orc.BeginChallenge(context.Background(), BeginChallengeInput{
			FlowID: begun.FlowID, Factor: factor.FactorPassword, Purpose: factor.PurposeLogin,
		})
		if err != nil {
			t.Fatalf("BeginChallenge: %v", err)
		}
		if got.AttemptsLeft != 3 {
			t.Fatalf("阈值 5、已错 2 次应回 3 次，实际 %d", got.AttemptsLeft)
		}
	})

	t.Run("已锁定不下发", func(t *testing.T) {
		st := &fakeStore{}
		guard := &fakeGuard{threshold: 3, locked: map[string]bool{}}
		orc := newOrc(t, st, &fakeVerifier{ticket: factor.ChallengeTicket{NextAction: factor.NextActionInputPassword}},
			func(d *Deps) { d.Guard = guard })
		begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
		uid := uuid.New()
		st.flow.UserID = &uid
		guard.locked[uid.String()] = true

		if _, err := orc.BeginChallenge(context.Background(), BeginChallengeInput{
			FlowID: begun.FlowID, Factor: factor.FactorPassword, Purpose: factor.PurposeLogin,
		}); !errors.Is(err, ErrAccountLocked) {
			t.Fatalf("已锁定应报 ACCOUNT_LOCKED，实际 %v", err)
		}
	})

	t.Run("匿名流不适用", func(t *testing.T) {
		st := &fakeStore{}
		guard := &fakeGuard{threshold: 3}
		orc := newOrc(t, st, &fakeVerifier{ticket: factor.ChallengeTicket{NextAction: factor.NextActionInputPassword}},
			func(d *Deps) { d.Guard = guard })
		begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})

		got, err := orc.BeginChallenge(context.Background(), BeginChallengeInput{
			FlowID: begun.FlowID, Factor: factor.FactorPassword, Purpose: factor.PurposeLogin,
		})
		if err != nil {
			t.Fatalf("BeginChallenge: %v", err)
		}
		if got.AttemptsLeft != 0 {
			t.Fatalf("匿名流没有账号级计数，应回 0（不适用），实际 %d", got.AttemptsLeft)
		}
	})
}
