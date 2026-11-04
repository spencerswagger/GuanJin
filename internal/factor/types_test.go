package factor

import (
	"errors"
	"strings"
	"testing"
)

func TestFactorTypeValidate(t *testing.T) {
	cases := []struct {
		in      FactorType
		wantErr bool
	}{
		{FactorPassword, false},
		{FactorSMSOTP, false},
		{FactorType("webauthn_v2"), false},
		{FactorType("f2"), false},
		{FactorType(""), true},
		{FactorType("Password"), true},
		{FactorType("sms-otp"), true},
		{FactorType("_sms"), true},
		{FactorType("2fa"), true},
		{FactorType(strings.Repeat("a", 65)), true},
	}
	for _, tc := range cases {
		err := tc.in.Validate()
		if tc.wantErr != (err != nil) {
			t.Errorf("Validate(%q) = %v, wantErr=%v", string(tc.in), err, tc.wantErr)
		}
		if tc.wantErr && !errors.Is(err, ErrInvalidFactorType) {
			t.Errorf("Validate(%q) 应包 ErrInvalidFactorType，实际 %v", string(tc.in), err)
		}
	}
}

// D2 的六值必须恰好是这六个，且**老值必须报错**（不许别名回退）。
func TestPurposeVocabulary(t *testing.T) {
	want := []string{"login", "register", "step_up", "enroll", "recover", "verify_identifier"}
	got := Purposes()
	if len(got) != len(want) {
		t.Fatalf("purpose 应为 6 值，实际 %d: %v", len(got), got)
	}
	for i, p := range got {
		if string(p) != want[i] {
			t.Fatalf("第 %d 个 purpose = %q，期望 %q（顺序也冻结）", i, p, want[i])
		}
		if !p.Valid() {
			t.Fatalf("%q 应合法", p)
		}
		if _, err := ParsePurpose(string(p)); err != nil {
			t.Fatalf("ParsePurpose(%q): %v", p, err)
		}
	}
	// 作废值：DDL 原写法 bind/stepup。
	for _, dead := range []string{"bind", "stepup", "step-up", "LOGIN", ""} {
		if Purpose(dead).Valid() {
			t.Errorf("%q 不得合法（D2 已作废 bind/stepup）", dead)
		}
		if _, err := ParsePurpose(dead); !errors.Is(err, ErrUnknownPurpose) {
			t.Errorf("ParsePurpose(%q) 必须报 ErrUnknownPurpose，实际 %v", dead, err)
		}
	}
}

// D12：AAL3 合法但不可产出；AAL0 根本不存在于本类型。
func TestACRDomain(t *testing.T) {
	if !ACRAAL1.Valid() || !ACRAAL2.Valid() || !ACRAAL3Reserved.Valid() {
		t.Fatal("AAL1/AAL2/AAL3 都应 Valid（DB CHECK 词表）")
	}
	if ACR("AAL0").Valid() {
		t.Fatal("AAL0 只在前端展示，不得作为落库取值（D12）")
	}
	if ACRAAL3Reserved.Issuable() {
		t.Fatal("AAL3 预留，V1 任何路径不得产出（D12）")
	}
	if !ACRAAL1.Issuable() || !ACRAAL2.Issuable() {
		t.Fatal("AAL1/AAL2 是唯一可产出的两个值")
	}
	if !ACRAAL2.AtLeast(ACRAAL1) || ACRAAL1.AtLeast(ACRAAL2) {
		t.Fatal("AtLeast 的强弱方向不对")
	}
	if ACR("AAL9").AtLeast(ACRAAL1) || ACRAAL1.AtLeast(ACR("AAL9")) {
		t.Fatal("未知 ACR 一律 false")
	}
	if ACR("AAL1").String() != "AAL1" {
		t.Fatal("String 应返回原值")
	}
}

func TestRoleAndIdentifierType(t *testing.T) {
	if !RoleFirst.Valid() || !RoleSecond.Valid() || Role("third").Valid() {
		t.Fatal("Role 封闭两值")
	}
	if RoleFirst.String() != "first" {
		t.Fatal("Role.String 不对")
	}
	for _, ok := range []IdentifierType{IdentifierPhone, IdentifierEmail, IdentifierUsername, IdentifierEmployeeNo, IdentifierType("wecom_userid")} {
		if err := ok.Validate(); err != nil {
			t.Errorf("%q 应合法：%v", ok, err)
		}
	}
	for _, bad := range []IdentifierType{"", "Phone", "wecom-userid", "_x"} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%q 应非法", bad)
		}
	}
	if IdentifierPhone.String() != "phone" {
		t.Fatal("IdentifierType.String 不对")
	}
}

func TestNextActionAndKind(t *testing.T) {
	for _, n := range []NextAction{NextActionTypeCode, NextActionInputTOTP, NextActionInputPassword} {
		if !n.Valid() {
			t.Errorf("%q 应合法", n)
		}
	}
	if NextAction("input_sms").Valid() {
		t.Fatal("next_action 是封闭三值（前端原子映射的输入）")
	}
	if NextActionTypeCode.String() != "type_code" {
		t.Fatal("NextAction.String 不对")
	}
	for _, k := range []Kind{KindHolding, KindChannel, KindProofing} {
		if !k.Valid() {
			t.Errorf("%q 应合法", k)
		}
	}
	if Kind("something").Valid() {
		t.Fatal("kind 是封闭三值")
	}
	if KindChannel.String() != "channel" {
		t.Fatal("Kind.String 不对")
	}
	for _, u := range []UnbindBehavior{UnbindNone, UnbindImmediate, UnbindDeferred} {
		if !u.Valid() {
			t.Errorf("%q 应合法", u)
		}
	}
	if UnbindBehavior("later").Valid() || UnbindImmediate.String() != "immediate" {
		t.Fatal("UnbindBehavior 封闭三值 + String")
	}
}

// AMR 表只覆盖 V1 种子值；未登记的因子必须返回 false，让调用方自己决定（禁隐性兜底）。
func TestAMRFor(t *testing.T) {
	cases := map[FactorType]string{
		FactorPassword:     "pwd",
		FactorTOTP:         "totp",
		FactorSMSOTP:       "sms_otp",
		FactorEmailOTP:     "email_otp",
		FactorRecoveryCode: "recovery_code",
		FactorWebAuthn:     "webauthn",
	}
	for f, want := range cases {
		got, ok := AMRFor(f)
		if !ok || got != want {
			t.Errorf("AMRFor(%s) = (%q,%v)，期望 (%q,true)", f, got, ok, want)
		}
	}
	got, ok := AMRFor(FactorType("wecom_otp"))
	if ok || got != "wecom_otp" {
		t.Errorf("未登记因子应返回 (type,false)，实际 (%q,%v)", got, ok)
	}
}
