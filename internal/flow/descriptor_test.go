package flow

import (
	"errors"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

func pwdInfo() FactorInfo {
	return FactorInfo{Type: factor.FactorPassword, AMR: "pwd", AsFirst: true, Sole: factor.ACRAAL1, Combined: factor.ACRAAL2}
}

func totpInfo() FactorInfo {
	return FactorInfo{Type: factor.FactorTOTP, AMR: "totp", AsSecond: true, Sole: factor.ACRAAL1, Combined: factor.ACRAAL2}
}

func baseState() State {
	return State{
		FlowID:    "f-1",
		Client:    Client{ID: "c-1", Name: "订单", Tier: "L2"},
		TargetACR: factor.ACRAAL1,
		Factors:   []FactorInfo{pwdInfo(), totpInfo()},
		Now:       time.Unix(1_760_000_000, 0).UTC(),
		ExpiresAt: time.Unix(1_760_000_600, 0).UTC(),
	}
}

// AC-FLOW-01：Descriptor 字段齐全，已达标时 terminal_state=satisfied。
func TestBuildDescriptorSatisfied(t *testing.T) {
	st := baseState()
	st.AchievedAMR = []string{"pwd"}
	d, err := BuildDescriptor(st)
	if err != nil {
		t.Fatalf("BuildDescriptor: %v", err)
	}
	if d.TargetACR != factor.ACRAAL1 || d.CurrentACR != factor.ACRAAL1 {
		t.Fatalf("acr 不对：target=%s current=%s", d.TargetACR, d.CurrentACR)
	}
	if !d.Satisfied || d.TerminalState != TerminalSatisfied {
		t.Fatalf("已达标应为 satisfied，实际 %+v", d)
	}
	if d.FlowID != "f-1" || d.Client.ID != "c-1" {
		t.Fatalf("流/业务系统标识丢失：%+v", d)
	}
	if len(d.Steps) != 2 || d.Steps[0].Status != StepDone || d.Steps[1].Status != StepPending {
		t.Fatalf("步骤应为「done 在前、pending 在后」，实际 %+v", d.Steps)
	}
	if d.Steps[0].Factor != factor.FactorPassword || d.Steps[1].Factor != factor.FactorTOTP {
		t.Fatalf("步骤因子不对：%+v", d.Steps)
	}
	if d.Steps[1].Role != factor.RoleSecond {
		t.Fatalf("TOTP 只能作第二因子，实际 %s", d.Steps[1].Role)
	}
	if d.Steps[1].Purpose != factor.PurposeLogin {
		t.Fatalf("步骤 purpose 应为 login，实际 %s", d.Steps[1].Purpose)
	}
	if d.Steps[1].ContributesACR != factor.ACRAAL1 {
		t.Fatalf("contributes_acr 应来自因子的 solo 级别，实际 %s", d.Steps[1].ContributesACR)
	}
}

// AC-FLOW-02 的数据侧：目标 AAL2、只有口令达成 → need_more（**不是错误**）。
func TestBuildDescriptorNeedMore(t *testing.T) {
	st := baseState()
	st.TargetACR = factor.ACRAAL2
	st.AchievedAMR = []string{"pwd"}
	d, err := BuildDescriptor(st)
	if err != nil {
		t.Fatalf("BuildDescriptor: %v", err)
	}
	if d.Satisfied || d.TerminalState != TerminalNeedMore {
		t.Fatalf("应为 need_more，实际 %+v", d)
	}
	if d.CurrentACR != factor.ACRAAL1 {
		t.Fatalf("单因子 current 应为其 sole(AAL1)，实际 %s", d.CurrentACR)
	}
}

// AC-FLOW-04：**被禁用因子不显示、也不计入 current_acr**。
func TestBuildDescriptorDisabledNotCounted(t *testing.T) {
	st := baseState()
	st.AchievedAMR = []string{"pwd"}
	st.DisabledFactors = []factor.FactorType{factor.FactorPassword}

	d, err := BuildDescriptor(st)
	if err != nil {
		t.Fatalf("BuildDescriptor: %v", err)
	}
	if d.CurrentACR != "" {
		t.Fatalf("已达成但被禁用的因子不得计入 current_acr，实际 %q", d.CurrentACR)
	}
	if d.Satisfied {
		t.Fatal("禁用后不应算达标")
	}
	for _, f := range d.AvailableFactors {
		if f == factor.FactorPassword {
			t.Fatal("被禁因子不得出现在 available_factors")
		}
	}
	if len(d.DisabledFactors) != 1 || d.DisabledFactors[0] != factor.FactorPassword {
		t.Fatalf("disabled_factors 应回显，实际 %+v", d.DisabledFactors)
	}
	for _, s := range d.Steps {
		if s.Factor == factor.FactorPassword {
			t.Fatal("被禁因子不得出现在 steps 里（前端渲染前就不用过滤）")
		}
	}
}

// AC-FLOW-07：不可达必须是**显式终态**（M1 的构造：client 禁用 password + 目标 AAL2）。
func TestBuildDescriptorUnreachable(t *testing.T) {
	st := baseState()
	st.TargetACR = factor.ACRAAL2
	st.DisabledFactors = []factor.FactorType{factor.FactorPassword}
	// 平台上只有口令这一个因子：禁掉之后无论如何到不了 AAL2。
	st.Factors = []FactorInfo{pwdInfo()}

	d, err := BuildDescriptor(st)
	if err != nil {
		t.Fatalf("BuildDescriptor: %v", err)
	}
	if d.TerminalState != TerminalUnreachable {
		t.Fatalf("应判不可达，实际 %+v", d)
	}
	if d.UnreachableReason == nil || d.UnreachableReason.Code != ReasonFactorsDisabled {
		t.Fatalf("原因应为 FACTORS_DISABLED，实际 %+v", d.UnreachableReason)
	}
	if len(d.UnreachableReason.MissingFactors) == 0 || d.UnreachableReason.MissingFactors[0] != factor.FactorPassword {
		t.Fatalf("missing_factors 应指出被禁的那个，实际 %+v", d.UnreachableReason.MissingFactors)
	}
	if d.Satisfied {
		t.Fatal("不可达不可能是 satisfied")
	}
}

// 两个可用因子能组合 → 不算不可达（这是"禁用不是永远不可达"的反向断言）。
func TestBuildDescriptorReachableByCombination(t *testing.T) {
	st := baseState()
	st.TargetACR = factor.ACRAAL2
	st.AchievedAMR = []string{"pwd"}
	d, err := BuildDescriptor(st)
	if err != nil {
		t.Fatalf("BuildDescriptor: %v", err)
	}
	if d.TerminalState != TerminalNeedMore {
		t.Fatalf("口令+TOTP 组合可达 AAL2，应 need_more，实际 %s", d.TerminalState)
	}
}

// 两个因子都达成 → 取 combined（AAL2）。
func TestBuildDescriptorCombinedACR(t *testing.T) {
	st := baseState()
	st.TargetACR = factor.ACRAAL2
	st.AchievedAMR = []string{"pwd", "totp"}
	d, err := BuildDescriptor(st)
	if err != nil {
		t.Fatalf("BuildDescriptor: %v", err)
	}
	if d.CurrentACR != factor.ACRAAL2 || !d.Satisfied {
		t.Fatalf("两因子达成应到 combined AAL2，实际 %s satisfied=%v", d.CurrentACR, d.Satisfied)
	}
}

// D12：AAL3 不得作为目标（预留值），AAL0/空也不是合法目标。
func TestBuildDescriptorRejectsBadTarget(t *testing.T) {
	for _, bad := range []factor.ACR{"AAL3", "AAL0", "AAL9", ""} {
		st := baseState()
		st.TargetACR = bad
		if _, err := BuildDescriptor(st); err == nil {
			t.Errorf("target_acr=%q 必须被拒（D12/AAL0 不落库）", string(bad))
		}
	}
}

// DescriptorOfFlow 的入口校验：空目标不是"默认 AAL1"，是数据缺陷。
func TestDescriptorOfFlow(t *testing.T) {
	now := time.Unix(1_760_000_000, 0).UTC()
	f := &model.Flow{
		TargetACR:   "AAL1",
		AchievedAMR: model.StringArray{"pwd"},
		ExpiresAt:   now.Add(10 * time.Minute),
	}
	d, err := DescriptorOfFlow(f, Client{ID: "c-1"}, nil, []FactorInfo{pwdInfo()}, now)
	if err != nil {
		t.Fatalf("DescriptorOfFlow: %v", err)
	}
	if d.FlowID != f.ID.String() || !d.Satisfied {
		t.Fatalf("折算不对：%+v", d)
	}

	if _, err := DescriptorOfFlow(nil, Client{}, nil, nil, now); err == nil {
		t.Fatal("nil flow 必须报错")
	}
	empty := &model.Flow{TargetACR: "", ExpiresAt: now}
	if _, err := DescriptorOfFlow(empty, Client{}, nil, nil, now); err == nil {
		t.Fatal("空 target_acr 必须报错（不许默认成 AAL1）")
	}
}

// FactorInfoOf 只认能力断言 + AMR 登记表；不参与编排的因子必须被过滤掉。
func TestFactorInfoOf(t *testing.T) {
	pwd, err := factor.Describe(nil)
	_ = pwd
	if err == nil {
		t.Fatal("nil provider 应报错（前置断言）")
	}

	// 用一个只声明能力的 Descriptor 直接验折算逻辑（不需要构造 provider）。
	d := factor.Descriptor{
		Type:            factor.FactorPassword,
		Kind:            factor.KindHolding,
		Capabilities:    []factor.Capability{factor.CapAuthenticatable, factor.CapACRContributing},
		Authenticatable: &factor.AuthenticatableSpec{AsFirst: true},
		ACR:             &factor.ACRSpec{Sole: factor.ACRAAL1, Combined: factor.ACRAAL2},
	}
	fi, ok := FactorInfoOf(d)
	if !ok || fi.AMR != "pwd" || !fi.AsFirst {
		t.Fatalf("应折算出口令信息，实际 %+v ok=%v", fi, ok)
	}

	// 不能认证的因子（恢复码）不参与编排。
	noAuth := d
	noAuth.Capabilities = []factor.Capability{factor.CapRecoverable}
	if _, ok := FactorInfoOf(noAuth); ok {
		t.Fatal("不能认证的因子不得进入编排")
	}

	// 没登记 AMR 的新因子：不参与编排（不拿类型名兜底）。
	unknown := d
	unknown.Type = factor.FactorType("wecom_otp")
	if _, ok := FactorInfoOf(unknown); ok {
		t.Fatal("AMR 未登记的因子不得进入编排")
	}

	// 缺 ACR / 缺 authenticatable spec 的一律不参与。
	noACR := d
	noACR.ACR = nil
	if _, ok := FactorInfoOf(noACR); ok {
		t.Fatal("缺 acr 的因子不得进入编排")
	}
	noSpec := d
	noSpec.Authenticatable = nil
	if _, ok := FactorInfoOf(noSpec); ok {
		t.Fatal("缺 authenticatable spec 的因子不得进入编排")
	}

	infos := FactorInfosOf([]factor.Descriptor{d, noAuth})
	if len(infos) != 1 {
		t.Fatalf("批量折回应只保留可编排的，实际 %d", len(infos))
	}
}

func TestSortedFactors(t *testing.T) {
	in := []FactorInfo{totpInfo(), pwdInfo()}
	out := SortedFactors(in)
	if len(out) != 2 || out[0].Type != factor.FactorPassword || out[1].Type != factor.FactorTOTP {
		t.Fatalf("应按类型字典序，实际 %+v", out)
	}
	// 不改原切片（调用方可能复用）。
	if in[0].Type != factor.FactorTOTP {
		t.Fatal("SortedFactors 不得原地排序入参")
	}
}

func TestHTTPCodeFor(t *testing.T) {
	if code, ok := HTTPCodeFor(Descriptor{Satisfied: true}); ok {
		t.Fatalf("达标不该有错误码，实际 %s", code)
	}
	if code, ok := HTTPCodeFor(Descriptor{TerminalState: TerminalUnreachable}); ok {
		t.Fatalf("不可达是 200 + terminal_state，不该有错误码，实际 %s", code)
	}
	code, ok := HTTPCodeFor(Descriptor{TerminalState: TerminalNeedMore})
	if !ok || code != "ACR_INSUFFICIENT" {
		t.Fatalf("未达标应给 ACR_INSUFFICIENT，实际 %q", code)
	}
}

var _ = errors.Is
