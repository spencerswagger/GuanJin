package identifier

import (
	"errors"
	"strings"
	"testing"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
)

func testKeys(t *testing.T) (crypto.HMACKey, crypto.SM4Key) {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 7)
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
	return idx, field
}

// 类型词表必须与 factor 侧一致（两处各有一份常量，靠这条测试防漂移）。
func TestIdentifierTypesMatchFactor(t *testing.T) {
	pairs := map[string]factor.IdentifierType{
		TypePhone:      factor.IdentifierPhone,
		TypeEmail:      factor.IdentifierEmail,
		TypeUsername:   factor.IdentifierUsername,
		TypeEmployeeNo: factor.IdentifierEmployeeNo,
	}
	for mine, theirs := range pairs {
		if mine != string(theirs) {
			t.Errorf("identifier.%q != factor.%q", mine, theirs)
		}
	}
}

// 盲索引必须**确定性**（同值同索引，才能做唯一约束），密文必须**非确定性**（随机 IV）。
func TestEncodeDeterministicIndexRandomCipher(t *testing.T) {
	idx, field := testKeys(t)
	a, err := Encode(TypeEmail, "Alice.Wang@Example.COM ", idx, field)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	b, err := Encode(TypeEmail, "alice.wang@example.com", idx, field)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(a.BlindIndex) != string(b.BlindIndex) {
		t.Fatal("同一规范化值必须得到同一盲索引（唯一约束的前提）")
	}
	if string(a.Cipher) == string(b.Cipher) {
		t.Fatal("密文必须随机（随机 IV）：两次加密相同说明 IV 被固定了")
	}
	if a.Normalized != "alice.wang@example.com" {
		t.Fatalf("规范化结果不对：%q", a.Normalized)
	}
	if strings.Contains(a.Masked, "alice.wang") || strings.Contains(a.Masked, "example.com") == false {
		t.Fatalf("掩码形态不对：%q", a.Masked)
	}
}

// 不同类型/不同键 → 不同盲索引（防止"类型没参与哈希"导致跨类型撞唯一约束）。
func TestEncodeIndexSensitivity(t *testing.T) {
	idx, field := testKeys(t)
	a, err := Encode(TypeUsername, "alice", idx, field)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	b, err := Encode(TypeEmployeeNo, "alice", idx, field)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(a.BlindIndex) == string(b.BlindIndex) {
		t.Fatal("类型不同必须得到不同盲索引（type 必须参与哈希）")
	}

	otherRaw := make([]byte, 32)
	for i := range otherRaw {
		otherRaw[i] = byte(200 - i)
	}
	mk, _ := crypto.NewMasterKey(otherRaw)
	otherIdx, _ := mk.DeriveHMAC(crypto.PurposeHMACIdentifier)
	c, err := Encode(TypeUsername, "alice", otherIdx, field)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(a.BlindIndex) == string(c.BlindIndex) {
		t.Fatal("换键后盲索引必须变化（否则等于没加密钥）")
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	idx, field := testKeys(t)
	enc, err := Encode(TypePhone, "+8613800008888", idx, field)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := Decode(enc.Cipher, field)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if back != "+8613800008888" {
		t.Fatalf("解回的值不对：%q", back)
	}
	var zero crypto.SM4Key
	if _, err := Decode(enc.Cipher, zero); err == nil {
		t.Fatal("零值键必须报错（否则等于明文落库）")
	}
	if _, err := Encode(TypePhone, "+8613800008888", idx, zero); err == nil {
		t.Fatal("零值键的 Encode 必须报错")
	}
}

func TestEncodeRejectsBadInput(t *testing.T) {
	idx, field := testKeys(t)
	cases := []struct {
		itype string
		raw   string
		want  error
	}{
		{TypeEmail, "not-an-email", ErrInvalidIdentifier},
		{TypePhone, "12", ErrInvalidIdentifier},
		{TypeUsername, "ab", ErrInvalidIdentifier},
		{TypeEmployeeNo, "  ", ErrEmptyValue},
		{TypeEmployeeNo, strings.Repeat("x", 65), ErrInvalidIdentifier},
		{"wecom_userid", "abc", ErrUnsupportedType},
	}
	for _, tc := range cases {
		if _, err := Encode(tc.itype, tc.raw, idx, field); !errors.Is(err, tc.want) {
			t.Errorf("Encode(%q,%q) = %v，期望 %v", tc.itype, tc.raw, err, tc.want)
		}
	}
}

// 不变式：I1 / I2 / I3-I4 / I6 各自的边界（规则层）。
func TestInvariants(t *testing.T) {
	t.Run("I1 最后一个已验证标识符不可解绑", func(t *testing.T) {
		s := Snapshot{Total: 2, Verified: 1, UsableHolding: 1}
		if err := CheckUnbind(s, true, true); !errors.Is(err, ErrLastVerifiedIdentifier) {
			t.Fatalf("期望 ErrLastVerifiedIdentifier，实际 %v", err)
		}
		// 还有第二个已验证的 → 允许。
		s.Verified = 2
		if err := CheckUnbind(s, true, true); err != nil {
			t.Fatalf("应允许：%v", err)
		}
		// 解绑的是未验证的那条 → 不影响 I1。
		if err := CheckUnbind(Snapshot{Total: 2, Verified: 1}, false, true); err != nil {
			t.Fatalf("解绑未验证标识符不该触发 I1：%v", err)
		}
	})

	t.Run("I2 最后一个标识符不可解绑", func(t *testing.T) {
		if err := CheckUnbind(Snapshot{Total: 1, Verified: 0}, false, true); !errors.Is(err, ErrLastIdentifier) {
			t.Fatalf("期望 ErrLastIdentifier，实际 %v", err)
		}
		if err := CheckUnbind(Snapshot{Total: 2, Verified: 0}, false, true); err != nil {
			t.Fatalf("两个标识符时允许解绑：%v", err)
		}
		// 已经是 disabled 的那条再解绑一次：不参与"减一"（幂等路径）。
		if err := CheckUnbind(Snapshot{Total: 1, Verified: 1}, true, false); err != nil {
			t.Fatalf("对已停用标识符的重复解绑应放行（幂等）：%v", err)
		}
	})

	t.Run("I3/I4 最后一个可用认证能力不可移除", func(t *testing.T) {
		// 只有一个持有型认证器、没有已验证信道标识符 → 拒绝。
		if err := CheckRemoveAuthenticator(Snapshot{UsableHolding: 1, VerifiedAuthable: 0}); !errors.Is(err, ErrLastAuthenticator) {
			t.Fatalf("期望 ErrLastAuthenticator，实际 %v", err)
		}
		// 还有已验证手机号 → 允许（并集语义）。
		if err := CheckRemoveAuthenticator(Snapshot{UsableHolding: 1, VerifiedAuthable: 1}); err != nil {
			t.Fatalf("有已验证手机号时应允许移除口令：%v", err)
		}
		// 两个持有型认证器 → 允许。
		if err := CheckRemoveAuthenticator(Snapshot{UsableHolding: 2}); err != nil {
			t.Fatalf("两个认证器时应允许：%v", err)
		}
	})

	t.Run("I6 最后一个主标识符不可降级", func(t *testing.T) {
		if err := CheckDemotePrimary(Snapshot{Primary: 1}); !errors.Is(err, ErrLastPrimary) {
			t.Fatalf("期望 ErrLastPrimary，实际 %v", err)
		}
		if err := CheckDemotePrimary(Snapshot{Primary: 2}); err != nil {
			t.Fatalf("两个主标识符时应允许：%v", err)
		}
	})

	t.Run("并集口径", func(t *testing.T) {
		if (Snapshot{}).UsableAuthMethods() != 0 {
			t.Fatal("空快照应为 0")
		}
		if (Snapshot{VerifiedAuthable: 1}).UsableAuthMethods() == 0 {
			t.Fatal("只有已验证信道标识符也算有可用能力")
		}
		// 两者都有也只算 1（这条数的用途是"是否为 0"，不是"有几个"）。
		if (Snapshot{UsableHolding: 3, VerifiedAuthable: 2}).UsableAuthMethods() != 1 {
			t.Fatal("并集语义：非空即为 1")
		}
	})
}

func TestCheckUsableAsLoginMethod(t *testing.T) {
	if err := CheckUsableAsLoginMethod(true, true, true); err != nil {
		t.Fatalf("已验证的认证型标识符应可当登录方式：%v", err)
	}
	for _, tc := range []struct{ verified, active, authable bool }{
		{false, true, true}, // 未验证
		{true, false, true}, // 已停用
		{true, true, false}, // username 不能当登录方式
	} {
		if err := CheckUsableAsLoginMethod(tc.verified, tc.active, tc.authable); !errors.Is(err, ErrIdentifierNotVerified) {
			t.Errorf("%+v 应报 ErrIdentifierNotVerified，实际 %v", tc, err)
		}
	}
}

// 码映射：I1/I2 → LAST_IDENTIFIER_CANNOT_UNBIND，I3/I4 → LAST_AUTHENTICATOR_...，I6 **无码**。
func TestCodeOf(t *testing.T) {
	cases := []struct {
		err  error
		want httpx.Code
	}{
		{nil, ""},
		{ErrLastIdentifier, httpx.CodeLastIdentifierCannotUnbind},
		{ErrLastVerifiedIdentifier, httpx.CodeLastIdentifierCannotUnbind},
		{ErrLastAuthenticator, httpx.CodeLastAuthenticatorCannotRemove},
		{ErrIdentifierNotVerified, httpx.CodeIdentifierNotVerified},
		{ErrLastPrimary, ""}, // I6 不设对外码（D3-B）：撞到即内部缺陷
	}
	for _, tc := range cases {
		got, ok := CodeOf(tc.err)
		if tc.want == "" {
			if ok {
				t.Errorf("CodeOf(%v) 不该有对外码，实际 %q", tc.err, got)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("CodeOf(%v) = (%q,%v)，期望 (%q,true)", tc.err, got, ok, tc.want)
		}
		if !httpx.IsKnownCode(got) {
			t.Errorf("%v 映射出的码不在冻结码表里", tc.err)
		}
	}
}
