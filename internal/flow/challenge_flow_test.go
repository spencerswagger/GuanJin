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

// ---- 挑战仓储的测试桩 ----

type fakeChallenges struct {
	row         *model.Challenge
	consumeRows int64
	consumed    int
	attempts    int
	exhausted   bool
	err         error
}

func (f *fakeChallenges) GetByID(_ context.Context, id uuid.UUID) (*model.Challenge, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.row == nil || f.row.ID != id {
		return nil, ErrChallengeNotFound
	}
	return f.row, nil
}

func (f *fakeChallenges) Consume(_ context.Context, _ uuid.UUID, _ time.Time) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.consumed++
	return f.consumeRows, nil
}

func (f *fakeChallenges) RecordAttempt(_ context.Context, _ uuid.UUID, _ time.Time) (int32, bool, error) {
	if f.err != nil {
		return 0, false, f.err
	}
	f.attempts++
	if f.exhausted {
		return 0, true, nil
	}
	return 3, false, nil
}

func newChallengeRow(id uuid.UUID, maxAttempts int32) *model.Challenge {
	return &model.Challenge{
		BaseModel:   model.BaseModel{ID: id},
		Purpose:     string(factor.PurposeLogin),
		FactorType:  string(factor.FactorPassword),
		MaxAttempts: maxAttempts,
		ExpiresAt:   time.Unix(1_760_000_600, 0).UTC(),
	}
}

// 口令的无状态挑战：不给 `flow_id` 不行（CH-1 已追认），给了就只回 next_action。
func TestBeginChallengeRequiresPurposeAndFlowID(t *testing.T) {
	orc := newOrc(t, &fakeStore{}, &fakeVerifier{})

	if _, err := orc.BeginChallenge(context.Background(), BeginChallengeInput{
		Factor: factor.FactorPassword,
	}); !errors.Is(err, ErrPurposeRequired) {
		t.Fatalf("缺 purpose 必须报错，实际 %v", err)
	}
	// CH-1：`login`/`step_up` 的 `flow_id` 必填，缺省 → MISSING_PARAMETER（400），不是 FLOW_NOT_FOUND。
	_, err := orc.BeginChallenge(context.Background(), BeginChallengeInput{
		Factor:  factor.FactorPassword,
		Purpose: factor.PurposeLogin,
	})
	if !errors.Is(err, ErrFlowIDRequired) {
		t.Fatalf("login 缺 flow_id 必须报错，实际 %v", err)
	}
	if code, ok := CodeOf(err); !ok || code != httpx.CodeMissingParameter {
		t.Fatalf("缺 flow_id 应映射 MISSING_PARAMETER，实际 %q", code)
	}
	// 作废的 purpose（`bind`）不许静默当 login。
	if _, err := orc.BeginChallenge(context.Background(), BeginChallengeInput{
		Factor: factor.FactorPassword, Purpose: "bind",
	}); !errors.Is(err, factor.ErrUnknownPurpose) {
		t.Fatalf("作废 purpose 必须报错，实际 %v", err)
	}
}

// 口令不落挑战行：`challenge_id` 为空、`next_action=input_password`、有效期取该流的。
func TestBeginChallengePasswordNeedsNoRow(t *testing.T) {
	st := &fakeStore{}
	verifier := &fakeVerifier{ticket: factor.ChallengeTicket{NextAction: factor.NextActionInputPassword}}
	orc := newOrc(t, st, verifier)
	begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	got, err := orc.BeginChallenge(context.Background(), BeginChallengeInput{
		FlowID:  begun.FlowID,
		Factor:  factor.FactorPassword,
		Purpose: factor.PurposeLogin,
	})
	if err != nil {
		t.Fatalf("BeginChallenge: %v", err)
	}
	if got.ChallengeID != nil {
		t.Fatalf("口令是无状态挑战，不得有 challenge_id，实际 %v", *got.ChallengeID)
	}
	if got.NextAction != factor.NextActionInputPassword {
		t.Fatalf("next_action 应为 input_password，实际 %s", got.NextAction)
	}
	if !got.ExpiresAt.Equal(st.flow.ExpiresAt) {
		t.Fatalf("无状态挑战的有效期应等于该流的，实际 %v vs %v", got.ExpiresAt, st.flow.ExpiresAt)
	}
	if got.FlowID == nil || *got.FlowID != begun.FlowID {
		t.Fatal("应回显 flow_id")
	}
	if len(verifier.beginReqs) != 1 {
		t.Fatalf("应调用一次因子的 BeginChallenge，实际 %d", len(verifier.beginReqs))
	}
	if verifier.beginReqs[0].ClientID != "c-1" {
		t.Fatalf("应把业务系统上下文传给因子，实际 %q", verifier.beginReqs[0].ClientID)
	}
	if verifier.beginReqs[0].Purpose != factor.PurposeLogin {
		t.Fatalf("应把 purpose 原样传给因子，实际 %s", verifier.beginReqs[0].Purpose)
	}
}

// AC-FLOW-03①：已完成的步再开挑战 → 409，且**不产生挑战行**（判定在调用因子之前）。
func TestBeginChallengeAlreadySatisfiedDoesNotReachFactor(t *testing.T) {
	st := &fakeStore{}
	verifier := &fakeVerifier{ticket: factor.ChallengeTicket{NextAction: factor.NextActionInputPassword}}
	orc := newOrc(t, st, verifier)
	begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	st.flow.AchievedAMR = model.StringArray{"pwd"}

	_, err = orc.BeginChallenge(context.Background(), BeginChallengeInput{
		FlowID:  begun.FlowID,
		Factor:  factor.FactorPassword,
		Purpose: factor.PurposeLogin,
	})
	if !errors.Is(err, ErrStepAlreadySatisfied) {
		t.Fatalf("应报 STEP_ALREADY_SATISFIED，实际 %v", err)
	}
	if status, ok := StatusFor(err); !ok || status != 409 {
		t.Fatalf("应映射 409，实际 %d", status)
	}
	if len(verifier.beginReqs) != 0 {
		t.Fatal("已完成的步不得调用因子（否则会先落一行挑战再回 409）")
	}
}

// 禁用 / 未装配：两种原因分开报（前端该"换因子"还是"找管理员"不同）。
func TestBeginChallengeDisabledAndUnsupported(t *testing.T) {
	st := &fakeStore{}
	orc := newOrc(t, st, &fakeVerifier{}, func(d *Deps) {
		d.DisabledFactors = func(context.Context, string) ([]factor.FactorType, error) {
			return []factor.FactorType{factor.FactorPassword}, nil
		}
	})
	begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := orc.BeginChallenge(context.Background(), BeginChallengeInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Purpose: factor.PurposeLogin,
	}); !errors.Is(err, ErrFactorDisabled) {
		t.Fatalf("应报 FACTOR_DISABLED，实际 %v", err)
	}

	// 平台没装配的因子：`sms_otp` 不在 Verifiers 里。
	orc2 := newOrc(t, &fakeStore{}, &fakeVerifier{})
	begun2, err := orc2.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := orc2.BeginChallenge(context.Background(), BeginChallengeInput{
		FlowID: begun2.FlowID, Factor: factor.FactorSMSOTP, Purpose: factor.PurposeLogin,
	}); !errors.Is(err, ErrFactorNotSupported) {
		t.Fatalf("应报 FACTOR_NOT_SUPPORTED，实际 %v", err)
	}
}

// 过期 → FLOW_EXPIRED(400)；已完成/已放弃 → FLOW_NOT_FOUND（AC-FLOW-10①②）。
func TestBeginChallengeFlowStateGuards(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()
	cases := []struct {
		name   string
		mutate func(*model.Flow)
		want   error
		code   httpx.Code
	}{
		{"expires_at 已过", func(f *model.Flow) { f.ExpiresAt = now.Add(-time.Second) }, ErrFlowExpired, httpx.CodeFlowExpired},
		{"status=expired", func(f *model.Flow) { f.Status = string(StatusExpired) }, ErrFlowExpired, httpx.CodeFlowExpired},
		{"status=completed", func(f *model.Flow) { f.Status = string(StatusCompleted) }, ErrFlowNotFound, httpx.CodeFlowNotFound},
		{"status=abandoned", func(f *model.Flow) { f.Status = string(StatusAbandoned) }, ErrFlowNotFound, httpx.CodeFlowNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{}
			orc := newOrc(t, st, &fakeVerifier{ticket: factor.ChallengeTicket{NextAction: factor.NextActionInputPassword}})
			begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			tc.mutate(st.flow)

			_, err = orc.BeginChallenge(context.Background(), BeginChallengeInput{
				FlowID: begun.FlowID, Factor: factor.FactorPassword, Purpose: factor.PurposeLogin,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("应报 %v，实际 %v", tc.want, err)
			}
			if code, ok := CodeOf(err); !ok || code != tc.code {
				t.Fatalf("应映射 %s，实际 %q", tc.code, code)
			}
		})
	}
}

// `type_code` 必须有权威挑战行：拿不到 `challenge_id` 就是装配缺陷（宁可 500 也不"假发码"）。
func TestBeginChallengeStatefulTicketRequiresRow(t *testing.T) {
	st := &fakeStore{}
	verifier := &fakeVerifier{ticket: factor.ChallengeTicket{NextAction: factor.NextActionTypeCode}}
	orc := newOrc(t, st, verifier)
	begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	_, err = orc.BeginChallenge(context.Background(), BeginChallengeInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Purpose: factor.PurposeLogin,
	})
	if !errors.Is(err, ErrChallengeNotPersisted) {
		t.Fatalf("应报 ErrChallengeNotPersisted，实际 %v", err)
	}
}

// 因子回了非法的 next_action（不在封闭三值里）→ 不猜，按"没有可用因子"报错。
func TestBeginChallengeRejectsInvalidNextAction(t *testing.T) {
	st := &fakeStore{}
	verifier := &fakeVerifier{ticket: factor.ChallengeTicket{NextAction: "do_something"}}
	orc := newOrc(t, st, verifier)
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if _, err := orc.BeginChallenge(context.Background(), BeginChallengeInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Purpose: factor.PurposeLogin,
	}); !errors.Is(err, ErrNoEligibleFactor) {
		t.Fatalf("应报 ErrNoEligibleFactor，实际 %v", err)
	}
}

// 无流挑战（register 等）：不读流、只按业务系统上下文判禁用集。
func TestBeginChallengeWithoutFlow(t *testing.T) {
	verifier := &fakeVerifier{ticket: factor.ChallengeTicket{NextAction: factor.NextActionInputPassword}}
	orc := newOrc(t, &fakeStore{}, verifier)
	got, err := orc.BeginChallenge(context.Background(), BeginChallengeInput{
		Factor:   factor.FactorPassword,
		Purpose:  factor.PurposeRegister,
		ClientID: "c-9",
	})
	if err != nil {
		t.Fatalf("无流挑战不该失败：%v", err)
	}
	if got.FlowID != nil {
		t.Fatal("无流挑战不得回显 flow_id")
	}
	if len(verifier.beginReqs) != 1 || verifier.beginReqs[0].FlowID != "" {
		t.Fatal("无流挑战不应把 flow_id 传给因子")
	}
	if verifier.beginReqs[0].ClientID != "c-9" {
		t.Fatalf("应传业务系统上下文，实际 %q", verifier.beginReqs[0].ClientID)
	}
}

// 无 `challenge_id`（口令）→ 直接走 VerifyStep，不碰挑战仓储。
func TestVerifyChallengeStatelessDelegates(t *testing.T) {
	st := &fakeStore{}
	orc := newOrc(t, st, &fakeVerifier{resp: factor.VerifyResult{Factor: factor.FactorPassword, AMR: "pwd", ACR: factor.ACRAAL1}})
	begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	res, err := orc.VerifyChallenge(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "pw",
	})
	if err != nil {
		t.Fatalf("VerifyChallenge: %v", err)
	}
	if !res.Satisfied {
		t.Fatalf("AAL1 目标下口令通过后应达标，实际 %+v", res.Flow)
	}
	if st.advanced != 1 {
		t.Fatalf("应推进一步，实际 %d", st.advanced)
	}
}

// 有 `challenge_id`：状态机先行（过期/已消费/次数用尽），成功才消费。
func TestVerifyChallengeDrivesStateMachine(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()

	t.Run("过期", func(t *testing.T) {
		st := &fakeStore{}
		ch := newChallengeRow(uuid.New(), 3)
		ch.ExpiresAt = now.Add(-time.Second)
		cs := &fakeChallenges{row: ch, consumeRows: 1}
		orc := newOrc(t, st, &fakeVerifier{}, func(d *Deps) { d.Challenges = cs })
		begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
		if _, err := orc.VerifyChallenge(context.Background(), VerifyInput{
			FlowID: begun.FlowID, Factor: factor.FactorPassword, ChallengeID: ch.ID.String(),
		}); !errors.Is(err, ErrChallengeExpired) {
			t.Fatalf("应报 CHALLENGE_EXPIRED，实际 %v", err)
		}
		if cs.consumed != 0 {
			t.Fatal("过期的挑战不得被消费")
		}
	})

	t.Run("已消费", func(t *testing.T) {
		st := &fakeStore{}
		ch := newChallengeRow(uuid.New(), 3)
		at := now.Add(-time.Minute)
		ch.ConsumedAt = &at
		cs := &fakeChallenges{row: ch, consumeRows: 1}
		orc := newOrc(t, st, &fakeVerifier{}, func(d *Deps) { d.Challenges = cs })
		begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
		if _, err := orc.VerifyChallenge(context.Background(), VerifyInput{
			FlowID: begun.FlowID, Factor: factor.FactorPassword, ChallengeID: ch.ID.String(),
		}); !errors.Is(err, ErrChallengeConsumed) {
			t.Fatalf("应报 CHALLENGE_CONSUMED，实际 %v", err)
		}
	})

	t.Run("次数用尽", func(t *testing.T) {
		st := &fakeStore{}
		ch := newChallengeRow(uuid.New(), 2)
		ch.Attempts = 2
		cs := &fakeChallenges{row: ch, consumeRows: 1}
		orc := newOrc(t, st, &fakeVerifier{}, func(d *Deps) { d.Challenges = cs })
		begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
		if _, err := orc.VerifyChallenge(context.Background(), VerifyInput{
			FlowID: begun.FlowID, Factor: factor.FactorPassword, ChallengeID: ch.ID.String(),
		}); !errors.Is(err, ErrAttemptsExceeded) {
			t.Fatalf("应报 ATTEMPTS_EXCEEDED，实际 %v", err)
		}
	})

	t.Run("成功即一次性消费", func(t *testing.T) {
		st := &fakeStore{}
		ch := newChallengeRow(uuid.New(), 3)
		cs := &fakeChallenges{row: ch, consumeRows: 1}
		orc := newOrc(t, st, &fakeVerifier{resp: factor.VerifyResult{Factor: factor.FactorPassword, AMR: "pwd", ACR: factor.ACRAAL1}},
			func(d *Deps) { d.Challenges = cs })
		begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
		if _, err := orc.VerifyChallenge(context.Background(), VerifyInput{
			FlowID: begun.FlowID, Factor: factor.FactorPassword, ChallengeID: ch.ID.String(), Response: "pw",
		}); err != nil {
			t.Fatalf("VerifyChallenge: %v", err)
		}
		if cs.consumed != 1 {
			t.Fatalf("通过后应消费一次，实际 %d", cs.consumed)
		}
		if cs.attempts != 0 {
			t.Fatal("成功不得扣尝试次数")
		}
	})

	t.Run("并发抢先（0 行）", func(t *testing.T) {
		st := &fakeStore{}
		ch := newChallengeRow(uuid.New(), 3)
		cs := &fakeChallenges{row: ch, consumeRows: 0}
		orc := newOrc(t, st, &fakeVerifier{resp: factor.VerifyResult{Factor: factor.FactorPassword, AMR: "pwd", ACR: factor.ACRAAL1}},
			func(d *Deps) { d.Challenges = cs })
		begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
		if _, err := orc.VerifyChallenge(context.Background(), VerifyInput{
			FlowID: begun.FlowID, Factor: factor.FactorPassword, ChallengeID: ch.ID.String(), Response: "pw",
		}); !errors.Is(err, ErrChallengeConsumed) {
			t.Fatalf("被别人先消费应回 CHALLENGE_CONSUMED，实际 %v", err)
		}
	})
}

// 凭据不对 → 扣一次尝试；扣到用尽回 ATTEMPTS_EXCEEDED（不是 INVALID_CREDENTIAL）。
func TestVerifyChallengeCountsAttemptsOnlyOnWrongCredential(t *testing.T) {
	st := &fakeStore{}
	ch := newChallengeRow(uuid.New(), 2)
	cs := &fakeChallenges{row: ch, exhausted: true}
	orc := newOrc(t, st, &fakeVerifier{err: credential.ErrInvalidCredential}, func(d *Deps) { d.Challenges = cs })
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	_, err := orc.VerifyChallenge(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, ChallengeID: ch.ID.String(), Response: "bad",
	})
	if !errors.Is(err, ErrAttemptsExceeded) {
		t.Fatalf("最后一次失败应回 ATTEMPTS_EXCEEDED，实际 %v", err)
	}
	if cs.attempts != 1 {
		t.Fatalf("应扣一次尝试，实际 %d", cs.attempts)
	}
	if cs.consumed != 0 {
		t.Fatal("失败的校验不得消费挑战")
	}
}

// 业务性拒绝（如"该步已完成"）不消耗尝试次数：用户的次数不该被自己的重复点击吃掉。
func TestVerifyChallengeBusinessErrorDoesNotCountAttempts(t *testing.T) {
	st := &fakeStore{}
	ch := newChallengeRow(uuid.New(), 3)
	cs := &fakeChallenges{row: ch, consumeRows: 1}
	orc := newOrc(t, st, &fakeVerifier{}, func(d *Deps) { d.Challenges = cs })
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	st.flow.AchievedAMR = model.StringArray{"pwd"}
	if _, err := orc.VerifyChallenge(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, ChallengeID: ch.ID.String(),
	}); !errors.Is(err, ErrStepAlreadySatisfied) {
		t.Fatalf("应报 STEP_ALREADY_SATISFIED，实际 %v", err)
	}
	if cs.attempts != 0 {
		t.Fatalf("业务性拒绝不该扣尝试次数，实际 %d", cs.attempts)
	}
}

// 没注入挑战仓储时必须 fail-closed（不存在"缺了也能验"的路径）。
func TestVerifyChallengeRequiresStore(t *testing.T) {
	st := &fakeStore{}
	orc := newOrc(t, st, &fakeVerifier{})
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	_, err := orc.VerifyChallenge(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, ChallengeID: uuid.New().String(),
	})
	if !errors.Is(err, ErrNilChallengeStore) {
		t.Fatalf("应报 ErrNilChallengeStore，实际 %v", err)
	}
}

// 非法 challenge_id 视为"不存在"（CHALLENGE_INVALID），不泄漏解析细节。
func TestVerifyChallengeRejectsMalformedID(t *testing.T) {
	st := &fakeStore{}
	orc := newOrc(t, st, &fakeVerifier{}, func(d *Deps) { d.Challenges = &fakeChallenges{} })
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	_, err := orc.VerifyChallenge(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, ChallengeID: "not-a-uuid",
	})
	if !errors.Is(err, ErrChallengeNotFound) {
		t.Fatalf("应报 ErrChallengeNotFound，实际 %v", err)
	}
}

// Describe：读路径同样要判过期（AC-FLOW-10①），否则前端会拿到"还能补步"的假契约。
func TestDescribeGuardsFlowState(t *testing.T) {
	st := &fakeStore{}
	orc := newOrc(t, st, &fakeVerifier{})
	begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	d, err := orc.Describe(context.Background(), begun.FlowID)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if d.FlowID != begun.FlowID.String() {
		t.Fatalf("应返回同一条流，实际 %s", d.FlowID)
	}

	st.flow.ExpiresAt = time.Unix(1_760_000_000, 0).UTC() // Now() == 该时刻 → 已过期
	if _, err := orc.Describe(context.Background(), begun.FlowID); !errors.Is(err, ErrFlowExpired) {
		t.Fatalf("过期流应报 FLOW_EXPIRED，实际 %v", err)
	}
	if _, err := orc.Describe(context.Background(), uuid.Nil); !errors.Is(err, ErrFlowNotFound) {
		t.Fatalf("空 id 应报 FLOW_NOT_FOUND，实际 %v", err)
	}
}

// 未知状态是数据缺陷：不猜"能不能用"，按"流不在"回（不能把缺陷伪装成可继续）。
func TestEnsureFlowUsableRejectsUnknownStatus(t *testing.T) {
	if err := ensureFlowUsable(nil, time.Now()); !errors.Is(err, ErrFlowNotFound) {
		t.Fatalf("nil 流应报 FLOW_NOT_FOUND，实际 %v", err)
	}
	f := &model.Flow{Status: "weird"}
	if err := ensureFlowUsable(f, time.Now()); err == nil {
		t.Fatal("未知状态必须报错")
	}
}

// 行 → 状态的换算只有一个出口：purpose 非法要报错，不能悄悄当成 login。
func TestChallengeOfRowValidatesPurpose(t *testing.T) {
	if _, err := ChallengeOfRow(nil); !errors.Is(err, ErrChallengeNotFound) {
		t.Fatalf("nil 行应报 ErrChallengeNotFound，实际 %v", err)
	}
	row := &model.Challenge{
		BaseModel:   model.BaseModel{ID: uuid.New()},
		Purpose:     "bind",
		FactorType:  "password",
		MaxAttempts: 3,
		ExpiresAt:   time.Now().Add(time.Minute),
	}
	if _, err := ChallengeOfRow(row); !errors.Is(err, factor.ErrUnknownPurpose) {
		t.Fatalf("作废的 purpose 必须报错，实际 %v", err)
	}
}
