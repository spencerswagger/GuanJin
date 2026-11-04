package flow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// ---- 编排器测试桩：只实现编排要用的那几个方法，能精确记录调用顺序 ----

type fakeStore struct {
	flow *model.Flow

	started    int
	advanced   int
	consumed   int
	getN       int
	advanceErr error
	readAfter  *model.Flow // Advance 之后 Get 到的行（模拟"写回后重读"）
}

func (f *fakeStore) Start(_ context.Context, in StartInput) (*model.Flow, error) {
	f.started++
	f.flow = &model.Flow{
		ClientID: in.ClientID, TargetACR: in.TargetACR, Status: "active",
		AchievedAMR: model.StringArray{}, ExpiresAt: time.Now().Add(time.Minute),
	}
	f.flow.ID = uuid.New()
	return f.flow, nil
}

func (f *fakeStore) Get(_ context.Context, id uuid.UUID) (*model.Flow, error) {
	f.getN++
	if f.readAfter != nil {
		return f.readAfter, nil
	}
	if f.flow == nil || f.flow.ID != id {
		return nil, ErrFlowNotFound
	}
	return f.flow, nil
}

func (f *fakeStore) Advance(_ context.Context, in AdvanceInput) (*model.Flow, error) {
	f.advanced++
	if f.advanceErr != nil {
		return nil, f.advanceErr
	}
	amr, err := AddAMR(append([]string(nil), f.flow.AchievedAMR...), in.AMR)
	if err != nil {
		return nil, err
	}
	next := *f.flow
	next.AchievedAMR = model.StringArray(amr)
	// 模拟"写回后重读"：Get 拿到的是推进后的行。
	f.readAfter = &next
	return &next, nil
}

func (f *fakeStore) Consume(_ context.Context, _ ConsumeInput) (*model.Flow, error) {
	f.consumed++
	return f.flow, nil
}

type fakeVerifier struct {
	calls   int
	resp    factor.VerifyResult
	err     error
	lastReq factor.VerifyRequest
	// order 用于断言"先校验、后推进"（I-F8）。
	order *[]string
	// ticket / beginErr / beginReqs 供挑战下发用例（S5）使用：
	// 口令回 `input_password` 且不落行；`type_code` 类必须带 `challenge_id`。
	ticket    factor.ChallengeTicket
	beginErr  error
	beginReqs []factor.ChallengeRequest
}

func (v *fakeVerifier) AuthenticatableSpec() factor.AuthenticatableSpec {
	return factor.AuthenticatableSpec{AsFirst: true}
}
func (v *fakeVerifier) BeginChallenge(_ context.Context, req factor.ChallengeRequest) (factor.ChallengeTicket, error) {
	v.beginReqs = append(v.beginReqs, req)
	if v.beginErr != nil {
		return factor.ChallengeTicket{}, v.beginErr
	}
	return v.ticket, nil
}
func (v *fakeVerifier) VerifyResponse(_ context.Context, req factor.VerifyRequest) (factor.VerifyResult, error) {
	v.calls++
	v.lastReq = req
	if v.order != nil {
		*v.order = append(*v.order, "verify")
	}
	return v.resp, v.err
}

func newOrc(t *testing.T, st *fakeStore, verifier *fakeVerifier, opts ...func(*Deps)) *Orchestrator {
	t.Helper()
	deps := Deps{
		Reader:   st,
		Starter:  st,
		Advancer: st,
		Consumer: st,
		Verifiers: map[factor.FactorType]factor.Authenticatable{
			factor.FactorPassword: verifier,
		},
		Factors:         []FactorInfo{pwdInfo(), totpInfo()},
		DisabledFactors: func(context.Context, string) ([]factor.FactorType, error) { return nil, nil },
		TierOf:          func(context.Context, string) (string, error) { return "L2", nil },
		ClientOf:        func(context.Context, string) (Client, error) { return Client{ID: "c-1"}, nil },
		// 默认给一个"永不锁定、阈值 3"的桩：个别用例需要真实计数时自行替换（见 risk_test.go）。
		Lockout: func(context.Context, string) (LockoutPolicy, error) {
			return LockoutPolicy{FailThreshold: 3, LockDuration: 10 * time.Minute}, nil
		},
		Guard: &fakeGuard{threshold: 3},
		Now:   func() time.Time { return time.Unix(1_760_000_000, 0).UTC() },
	}
	for _, o := range opts {
		o(&deps)
	}
	orc, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return orc
}

// Begin：档位 → 目标级别（客户端不能自己声明），并返回完整 Descriptor。
func TestBeginCreatesFlowWithTargetFromTier(t *testing.T) {
	st := &fakeStore{}
	orc := newOrc(t, st, &fakeVerifier{})

	res, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1", TraceID: "tr-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if st.started != 1 {
		t.Fatalf("应开一条流，实际 %d", st.started)
	}
	if st.flow.TargetACR != string(factor.ACRAAL1) {
		t.Fatalf("L2 档位应为 AAL1，实际 %s", st.flow.TargetACR)
	}
	if res.Satisfied {
		t.Fatal("刚开的流不可能达标")
	}
	if res.Flow.TerminalState != TerminalNeedMore {
		t.Fatalf("应为 need_more，实际 %s", res.Flow.TerminalState)
	}
	if len(res.Flow.Steps) != 2 {
		t.Fatalf("两个可用因子应给两步，实际 %+v", res.Flow.Steps)
	}
}

// L3 → AAL2（D12：不是 AAL3）。
func TestBeginL3TargetsAAL2(t *testing.T) {
	st := &fakeStore{}
	orc := newOrc(t, st, &fakeVerifier{}, func(d *Deps) {
		d.TierOf = func(context.Context, string) (string, error) { return "L3", nil }
	})
	if _, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if st.flow.TargetACR != string(factor.ACRAAL2) {
		t.Fatalf("L3 应为 AAL2（D12 不产出 AAL3），实际 %s", st.flow.TargetACR)
	}
}

// 带 flow_id 时续流（不再开新流）。
func TestBeginResumesExistingFlow(t *testing.T) {
	st := &fakeStore{}
	orc := newOrc(t, st, &fakeVerifier{})
	first, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	st.getN = 0
	res, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1", FlowID: &first.FlowID})
	if err != nil {
		t.Fatalf("续流: %v", err)
	}
	if st.started != 1 {
		t.Fatalf("续流不得再开新流，实际 started=%d", st.started)
	}
	if res.FlowID != first.FlowID {
		t.Fatalf("续流应返回同一条流，实际 %s vs %s", res.FlowID, first.FlowID)
	}
}

// 缺 client_id 直接拒（注册/登录必带上下文）。
func TestBeginRequiresClient(t *testing.T) {
	orc := newOrc(t, &fakeStore{}, &fakeVerifier{})
	if _, err := orc.Begin(context.Background(), BeginInput{}); err == nil {
		t.Fatal("缺 client_id 必须报错")
	}
}

// AC-FLOW-03：已完成的步再提交 → STEP_ALREADY_SATISFIED（409），且**不调 provider、不推进**。
func TestVerifyStepAlreadySatisfied(t *testing.T) {
	st := &fakeStore{}
	verifier := &fakeVerifier{}
	orc := newOrc(t, st, verifier)
	begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// 把口令标记为已达成。
	st.flow.AchievedAMR = model.StringArray{"pwd"}
	st.readAfter = nil

	_, err = orc.VerifyStep(context.Background(), VerifyInput{FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "x"})
	if !errors.Is(err, ErrStepAlreadySatisfied) {
		t.Fatalf("应报 ErrStepAlreadySatisfied，实际 %v", err)
	}
	status, okStatus := StatusFor(err)
	if code, ok := CodeOf(err); !ok || code != CodeStepAlreadySatisfied || !okStatus || status != 409 {
		t.Fatalf("应映射 %s/409，实际 %q", CodeStepAlreadySatisfied, code)
	}
	if verifier.calls != 0 {
		t.Fatal("已完成的步不得再调 provider（否则等于白算一次 PBKDF2）")
	}
	if st.advanced != 0 {
		t.Fatal("已完成的步不得推进")
	}
}

// 禁用因子：既不显示也不计入，直接提交要回 FACTOR_DISABLED。
func TestVerifyStepDisabledAndUnsupported(t *testing.T) {
	st := &fakeStore{}
	verifier := &fakeVerifier{}
	orc := newOrc(t, st, verifier, func(d *Deps) {
		d.DisabledFactors = func(context.Context, string) ([]factor.FactorType, error) {
			return []factor.FactorType{factor.FactorPassword}, nil
		}
	})
	begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "x",
	}); !errors.Is(err, ErrFactorDisabled) {
		t.Fatalf("应报 ErrFactorDisabled，实际 %v", err)
	}
	if verifier.calls != 0 {
		t.Fatal("被禁因子不得进入校验")
	}

	// 未装配的因子。
	if _, err := orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorWebAuthn, Response: "x",
	}); !errors.Is(err, ErrFactorNotSupported) {
		t.Fatalf("未装配因子应报 ErrFactorNotSupported，实际 %v", err)
	}
}

// 校验失败：错误原样上抛，且**不推进**（顺序证明 I-F8）。
func TestVerifyStepFailureDoesNotAdvance(t *testing.T) {
	order := []string{}
	st := &fakeStore{}
	verifier := &fakeVerifier{err: credential.ErrInvalidCredential, order: &order}
	orc := newOrc(t, st, verifier)
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})

	_, err := orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "wrong",
	})
	if !errors.Is(err, credential.ErrInvalidCredential) {
		t.Fatalf("应透出凭据错误，实际 %v", err)
	}
	if st.advanced != 0 {
		t.Fatal("校验失败不得推进流")
	}
	if len(order) != 1 || order[0] != "verify" {
		t.Fatalf("应只调过一次校验，实际 %v", order)
	}
}

// AC-FLOW-02：一步通过但未达标 → ACR_INSUFFICIENT + 完整 Descriptor（不是错误页）。
func TestVerifyStepInsufficientCarriesDescriptor(t *testing.T) {
	order := []string{}
	st := &fakeStore{}
	verifier := &fakeVerifier{resp: factor.VerifyResult{Factor: factor.FactorPassword, AMR: "pwd", ACR: factor.ACRAAL1}, order: &order}
	orc := newOrc(t, st, verifier, func(d *Deps) {
		d.TierOf = func(context.Context, string) (string, error) { return "L3", nil } // 目标 AAL2
	})
	begun, err := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	res, err := orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "ok", UserID: "u-1",
	})
	if err == nil {
		t.Fatal("未达标应返回 ACR_INSUFFICIENT")
	}
	var insuff *ACRInsufficientError
	if !errors.As(err, &insuff) {
		t.Fatalf("应为 *ACRInsufficientError，实际 %T", err)
	}
	if insuff.Flow.TargetACR != factor.ACRAAL2 || insuff.Flow.CurrentACR != factor.ACRAAL1 {
		t.Fatalf("Descriptor 应反映 current<target，实际 %+v", insuff.Flow)
	}
	if insuff.Flow.TerminalState != TerminalNeedMore {
		t.Fatalf("应为 need_more，实际 %s", insuff.Flow.TerminalState)
	}
	if res == nil || res.Satisfied {
		t.Fatal("结果里也要带 Descriptor（便于调用方直接拼 details.flow）")
	}
	if st.advanced != 1 {
		t.Fatalf("通过的那一步必须推进（一次），实际 %d", st.advanced)
	}
	if verifier.lastReq.UserID != "u-1" || verifier.lastReq.FlowID != begun.FlowID.String() {
		t.Fatalf("校验入参没带上流/用户上下文：%+v", verifier.lastReq)
	}
}

// 一步通过且达标 → Satisfied=true，不再报错。
func TestVerifyStepSatisfied(t *testing.T) {
	st := &fakeStore{}
	verifier := &fakeVerifier{resp: factor.VerifyResult{Factor: factor.FactorPassword, AMR: "pwd", ACR: factor.ACRAAL1}}
	orc := newOrc(t, st, verifier) // L2 → 目标 AAL1
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})

	res, err := orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "ok",
	})
	if err != nil {
		t.Fatalf("达标不该报错：%v", err)
	}
	if !res.Satisfied || res.Flow.TerminalState != TerminalSatisfied {
		t.Fatalf("应为 satisfied，实际 %+v", res)
	}
}

// 推进失败（如流已过期）要如实上抛，不能当成功。
func TestVerifyStepAdvanceErrorPropagates(t *testing.T) {
	st := &fakeStore{advanceErr: errors.New("flowstore: flow expired")}
	orc := newOrc(t, st, &fakeVerifier{})
	begun, _ := orc.Begin(context.Background(), BeginInput{ClientID: "c-1"})
	if _, err := orc.VerifyStep(context.Background(), VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "ok",
	}); err == nil {
		t.Fatal("推进失败必须报错")
	}
}

func TestVerifyStepRequiresFlow(t *testing.T) {
	orc := newOrc(t, &fakeStore{}, &fakeVerifier{})
	if _, err := orc.VerifyStep(context.Background(), VerifyInput{Factor: factor.FactorPassword}); !errors.Is(err, ErrFlowNotFound) {
		t.Fatalf("缺 flow_id 应报 ErrFlowNotFound，实际 %v", err)
	}
	if _, err := orc.VerifyStep(context.Background(), VerifyInput{FlowID: uuid.New()}); !errors.Is(err, ErrNoEligibleFactor) {
		t.Fatalf("缺因子应报 ErrNoEligibleFactor，实际 %v", err)
	}
}

// Finish 只做一次性消费（时机由业务决定，编排不替它做主）。
func TestFinishConsumes(t *testing.T) {
	st := &fakeStore{}
	orc := newOrc(t, st, &fakeVerifier{})
	if _, err := orc.Finish(context.Background(), uuid.New(), nil, nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if st.consumed != 1 {
		t.Fatalf("应消费一次，实际 %d", st.consumed)
	}
}

// 装配期校验：缺依赖 / 缺时钟一律报错（不等到第一个用户登录才炸）。
func TestNewValidatesDeps(t *testing.T) {
	full := func() Deps {
		st := &fakeStore{}
		return Deps{
			Reader: st, Starter: st, Advancer: st, Consumer: st,
			DisabledFactors: func(context.Context, string) ([]factor.FactorType, error) { return nil, nil },
			TierOf:          func(context.Context, string) (string, error) { return "L2", nil },
			Lockout: func(context.Context, string) (LockoutPolicy, error) {
				return LockoutPolicy{FailThreshold: 3, LockDuration: time.Minute}, nil
			},
			Guard: &fakeGuard{threshold: 3},
			Now:   time.Now,
		}
	}
	if _, err := New(full()); err != nil {
		t.Fatalf("完整依赖应通过：%v", err)
	}
	cases := map[string]func(*Deps){
		"缺 Reader":   func(d *Deps) { d.Reader = nil },
		"缺 Starter":  func(d *Deps) { d.Starter = nil },
		"缺 Advancer": func(d *Deps) { d.Advancer = nil },
		"缺 Consumer": func(d *Deps) { d.Consumer = nil },
		"缺禁用因子读取":    func(d *Deps) { d.DisabledFactors = nil },
		"缺档位读取":      func(d *Deps) { d.TierOf = nil },
		"缺时钟":        func(d *Deps) { d.Now = nil },
		"缺风控计数":      func(d *Deps) { d.Guard = nil },
		"缺失败处理策略":    func(d *Deps) { d.Lockout = nil },
	}
	for name, mut := range cases {
		d := full()
		mut(&d)
		err := error(nil)
		if _, err = New(d); err == nil {
			t.Errorf("%s 必须报错", name)
		}
	}
	// ClientOf 缺省时退回空 Client（只影响展示，不影响判定）。
	d := full()
	d.ClientOf = nil
	if _, err := New(d); err != nil {
		t.Fatalf("ClientOf 可缺省：%v", err)
	}
}

func TestTargetACRForTier(t *testing.T) {
	for tier, want := range map[string]factor.ACR{"L1": factor.ACRAAL1, "L2": factor.ACRAAL1, "L3": factor.ACRAAL2} {
		got, err := TargetACRForTier(tier)
		if err != nil || got != want {
			t.Errorf("TargetACRForTier(%s) = (%s,%v)，期望 %s", tier, got, err, want)
		}
	}
	if _, err := TargetACRForTier("L9"); err == nil {
		t.Fatal("未知档位必须报错")
	}
}
