package crypto

import (
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func mustMasterKey(t *testing.T) *MasterKey {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	mk, err := NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	return mk
}

// 派生确定性：同一用途、同一主密钥 → 同一子密钥（否则重启即解不开历史密文）。
func TestDeriveIsDeterministic(t *testing.T) {
	mk := mustMasterKey(t)
	for _, p := range Purposes() {
		switch kind, _ := p.Kind(); kind {
		case KindSM4:
			a, err := mk.DeriveSM4(p)
			if err != nil {
				t.Fatalf("derive %s: %v", p, err)
			}
			b, _ := mk.DeriveSM4(p)
			if !a.Equal(b) {
				t.Fatalf("derive %s not deterministic", p)
			}
			if a.IsZero() {
				t.Fatalf("derive %s produced all-zero key", p)
			}
		case KindHMAC:
			a, err := mk.DeriveHMAC(p)
			if err != nil {
				t.Fatalf("derive %s: %v", p, err)
			}
			b, _ := mk.DeriveHMAC(p)
			if !a.Equal(b) {
				t.Fatalf("derive %s not deterministic", p)
			}
			if a.IsZero() {
				t.Fatalf("derive %s produced all-zero key", p)
			}
		}
	}
}

// 用途隔离：8 个用途域必须两两不同（同主密钥下不允许出现"两个用途同键"）。
func TestDerivePurposesAreIsolated(t *testing.T) {
	mk := mustMasterKey(t)
	seen := map[string]Purpose{}
	for _, p := range Purposes() {
		kind, err := p.Kind()
		if err != nil {
			t.Fatalf("kind(%s): %v", p, err)
		}
		var hexKey string
		switch kind {
		case KindSM4:
			k, err := mk.DeriveSM4(p)
			if err != nil {
				t.Fatalf("derive %s: %v", p, err)
			}
			hexKey = hex.EncodeToString(k.material[:])
		case KindHMAC:
			k, err := mk.DeriveHMAC(p)
			if err != nil {
				t.Fatalf("derive %s: %v", p, err)
			}
			hexKey = hex.EncodeToString(k.material[:])
		}
		if prev, dup := seen[hexKey]; dup {
			t.Fatalf("purposes %q and %q derived the same key (isolation broken)", prev, p)
		}
		seen[hexKey] = p
	}
}

// D14②：`hmac/register`（注册窗口令牌）必须与其余用途**派生出不同密钥**。
//
// 单看 TestDerivePurposesAreIsolated 已经覆盖两两隔离，但这条独立钉住"新用途不许
// 复用 hmac/identifier"这个具体裁定：如果将来有人把 register 的用途值改成 identifier，
// 下面第一条（用途名）与第二条（密钥不同）会分别报错，评审不必靠肉眼比对 hash。
func TestRegisterPurposeIsDistinctFromTheOtherFive(t *testing.T) {
	if PurposeHMACRegister != Purpose("guanjin/hmac/register") {
		t.Fatalf("用途域冻结为 guanjin/hmac/register，实际 %q", string(PurposeHMACRegister))
	}
	if PurposeHMACRegister == PurposeHMACIdentifier {
		t.Fatal("register_token 不得复用 hmac/identifier（一密钥一用途）")
	}
	kind, err := PurposeHMACRegister.Kind()
	if err != nil {
		t.Fatalf("kind: %v", err)
	}
	if kind != KindHMAC {
		t.Fatalf("hmac/register kind = %v, want KindHMAC", kind)
	}
	if length, err := PurposeHMACRegister.Length(); err != nil || length != 32 {
		t.Fatalf("hmac/register length = %d, %v; want 32", length, err)
	}

	mk := mustMasterKey(t)
	registerKey, err := mk.DeriveHMAC(PurposeHMACRegister)
	if err != nil {
		t.Fatalf("derive hmac/register: %v", err)
	}
	if registerKey.IsZero() {
		t.Fatal("derived hmac/register key must not be zero")
	}
	for _, other := range Purposes() {
		if other == PurposeHMACRegister {
			continue
		}
		otherKind, err := other.Kind()
		if err != nil {
			t.Fatalf("kind(%s): %v", other, err)
		}
		// 同为 HMAC 的用途才能直接比字节；SM4 用途已由"密钥空间不同"天然隔开。
		if otherKind != KindHMAC {
			continue
		}
		otherKey, err := mk.DeriveHMAC(other)
		if err != nil {
			t.Fatalf("derive %s: %v", other, err)
		}
		if registerKey.Equal(otherKey) {
			t.Fatalf("hmac/register 与 %s 派生出同一把密钥（用途隔离被破坏）", other)
		}
	}
}

// 不同主密钥 → 不同子密钥（主密钥真的参与了派生）。
func TestDeriveDependsOnMasterKey(t *testing.T) {
	a := mustMasterKey(t)
	other := make([]byte, 32)
	for i := range other {
		other[i] = 0xff
	}
	b, err := NewMasterKey(other)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	ka, _ := a.DeriveHMAC(PurposeHMACAudit)
	kb, _ := b.DeriveHMAC(PurposeHMACAudit)
	if ka.Equal(kb) {
		t.Fatal("different master keys must derive different subkeys")
	}
}

// 长度与用途绑定：SM4 用途 16B、HMAC 用途 32B（不接受"随便给个长度"）。
func TestPurposeLengths(t *testing.T) {
	want := map[Purpose]struct {
		kind KeyKind
		len  int
	}{
		PurposeSM4Field:       {KindSM4, 16},
		PurposeSM4Keywrap:     {KindSM4, 16},
		PurposeHMACAudit:      {KindHMAC, 32},
		PurposeHMACIdentifier: {KindHMAC, 32},
		PurposeHMACAgent:      {KindHMAC, 32},
		PurposeHMACRegister:   {KindHMAC, 32},
		PurposeHMACField:      {KindHMAC, 32},
		PurposeHMACKeywrap:    {KindHMAC, 32},
	}
	// 逐用途的期望表必须覆盖**全部**冻结用途：漏写一条就等于给"新用途随便定长度"开门。
	if len(want) != len(Purposes()) {
		t.Fatalf("期望表 %d 条 ≠ 冻结用途 %d 条（新增用途要同时补长度断言）", len(want), len(Purposes()))
	}
	for p, exp := range want {
		kind, err := p.Kind()
		if err != nil {
			t.Fatalf("kind(%s): %v", p, err)
		}
		if kind != exp.kind {
			t.Errorf("kind(%s) = %v, want %v", p, kind, exp.kind)
		}
		length, err := p.Length()
		if err != nil || length != exp.len {
			t.Errorf("length(%s) = %d, %v; want %d", p, length, err, exp.len)
		}
	}
}

// ADR-005：用途表自检——每个 SM4 用途必须有配套的 32B HMAC 用途，HMAC 用途不许再挂 MAC。
//
// 少了这条，将来有人加一个 SM4 用途却忘了配 MAC 域，`DeriveSM4` 会在**运行期**报
// ErrNoIntegrityKey（启动即炸）；这里让它在**单测**就红，不必等到部署。
func TestPurposeSpecsAreConsistent(t *testing.T) {
	if err := validatePurposeSpecs(); err != nil {
		t.Fatalf("用途表不一致：%v", err)
	}
}

// ADR-005：SM4 信封的 MAC 材料**就是** `hmac/field` / `hmac/keywrap` 派生出来的那把钥匙，
// 而加密键与 MAC 键互不相同（同一用途域内两把钥匙也必须分开）。
func TestSM4EnvelopeMacKeyIsThePairedPurpose(t *testing.T) {
	mk := mustMasterKey(t)
	for _, pair := range []struct {
		enc, mac Purpose
	}{
		{PurposeSM4Field, PurposeHMACField},
		{PurposeSM4Keywrap, PurposeHMACKeywrap},
	} {
		envelopeKey, err := mk.DeriveSM4(pair.enc)
		if err != nil {
			t.Fatalf("DeriveSM4(%s): %v", pair.enc, err)
		}
		macKey, err := mk.DeriveHMAC(pair.mac)
		if err != nil {
			t.Fatalf("DeriveHMAC(%s): %v", pair.mac, err)
		}
		if string(envelopeKey.mac[:]) != string(macKey.material[:]) {
			t.Fatalf("%s 的 MAC 材料必须等于 DeriveHMAC(%s)（两把钥匙都直接来自主密钥）",
				pair.enc, pair.mac)
		}
		if string(envelopeKey.material[:]) == string(envelopeKey.mac[:SM4KeySize]) {
			t.Fatalf("%s 的加密键与 MAC 键不得相同（用途隔离）", pair.enc)
		}
	}
}

// 未知用途必须报错：不允许自造 domain separator。
func TestUnknownPurposeRejected(t *testing.T) {
	mk := mustMasterKey(t)
	if _, err := mk.DeriveHMAC(Purpose("guanjin/hmac/whatever")); err == nil {
		t.Fatal("unknown purpose must be rejected")
	}
	if _, err := mk.DeriveSM4(Purpose("guanjin/sm4/field/")); err == nil {
		t.Fatal("near-miss purpose must be rejected (no prefix matching)")
	}
}

// 种类不符必须报错，不许"派生出来截断凑合"。
func TestKeyKindMismatch(t *testing.T) {
	mk := mustMasterKey(t)
	if _, err := mk.DeriveSM4(PurposeHMACAudit); !errors.Is(err, ErrKeyKindMismatch) {
		t.Fatalf("DeriveSM4(hmac purpose) err = %v, want ErrKeyKindMismatch", err)
	}
	if _, err := mk.DeriveHMAC(PurposeSM4Field); !errors.Is(err, ErrKeyKindMismatch) {
		t.Fatalf("DeriveHMAC(sm4 purpose) err = %v, want ErrKeyKindMismatch", err)
	}
}

// 主密钥长度与空值：fail-closed，不补齐、不截断。
func TestNewMasterKeyValidation(t *testing.T) {
	if _, err := NewMasterKey(nil); !errors.Is(err, ErrMasterKeyEmpty) {
		t.Fatalf("nil key err = %v, want ErrMasterKeyEmpty", err)
	}
	if _, err := NewMasterKey([]byte{}); !errors.Is(err, ErrMasterKeyEmpty) {
		t.Fatalf("empty key err = %v, want ErrMasterKeyEmpty", err)
	}
	short := make([]byte, MinMasterKeyLen-1)
	if _, err := NewMasterKey(short); !errors.Is(err, ErrMasterKeyTooShort) {
		t.Fatalf("short key err = %v, want ErrMasterKeyTooShort", err)
	}
	if _, err := NewMasterKey(make([]byte, MinMasterKeyLen)); err != nil {
		t.Fatalf("exactly-min key must be accepted, got %v", err)
	}
}

// 脱敏：任何格式化输出都不得泄露密钥材料。
func TestKeyMaterialNeverPrinted(t *testing.T) {
	mk := mustMasterKey(t)
	masterHex := hex.EncodeToString(mk.material)
	sm4, _ := mk.DeriveSM4(PurposeSM4Field)
	hmacKey, _ := mk.DeriveHMAC(PurposeHMACAudit)
	sm4Hex := hex.EncodeToString(sm4.material[:])
	hmacHex := hex.EncodeToString(hmacKey.material[:])

	for _, rendered := range []string{
		fmt.Sprint(mk), fmt.Sprintf("%v", mk), fmt.Sprintf("%#v", mk), mk.String(),
		fmt.Sprint(sm4), fmt.Sprintf("%#v", sm4), sm4.String(),
		fmt.Sprint(hmacKey), fmt.Sprintf("%#v", hmacKey), hmacKey.String(),
	} {
		for _, secret := range []string{masterHex, sm4Hex, hmacHex, masterHex[:8], sm4Hex[:8]} {
			if strings.Contains(strings.ToLower(rendered), secret) {
				t.Fatalf("rendered key leaks material: %s", rendered)
			}
		}
	}
}

// 编译期约束的结构性验证：MasterKey / 子密钥**没有任何导出方法返回 []byte**。
//
// 这就是"主密钥不进业务算法"的可执行证明——如果哪天有人加了 `Bytes()`，
// 这个测试会先红，而不是等到评审时靠肉眼发现。
func TestNoExportedMethodReturnsRawBytes(t *testing.T) {
	byteSlice := reflect.TypeOf([]byte{})
	for _, typ := range []reflect.Type{
		reflect.TypeOf(MasterKey{}),
		reflect.TypeOf(SM4Key{}),
		reflect.TypeOf(HMACKey{}),
		reflect.TypeOf(&MasterKey{}),
	} {
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i)
			for j := 0; j < m.Type.NumOut(); j++ {
				if m.Type.Out(j) == byteSlice {
					t.Fatalf("%s.%s returns []byte — raw key material must never escape", typ, m.Name)
				}
			}
		}
	}
}

// 子密钥结构体不得有导出字段（防止字面量构造"未派生的键"）。
func TestKeyStructsHaveNoExportedFields(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(SM4Key{}), reflect.TypeOf(HMACKey{})} {
		for i := 0; i < typ.NumField(); i++ {
			if f := typ.Field(i); f.IsExported() {
				t.Fatalf("%s.%s is exported — keys must only come from MasterKey.Derive*", typ, f.Name)
			}
		}
	}
}

// Zero 真的清空（用于"用完即弃"）。
func TestZeroClearsMaterial(t *testing.T) {
	mk := mustMasterKey(t)
	sm4, _ := mk.DeriveSM4(PurposeSM4Field)
	hmacKey, _ := mk.DeriveHMAC(PurposeHMACAudit)
	if sm4.IsZero() || hmacKey.IsZero() {
		t.Fatal("fresh keys must not be zero")
	}
	sm4.Zero()
	hmacKey.Zero()
	if !sm4.IsZero() || !hmacKey.IsZero() {
		t.Fatal("Zero must clear material")
	}
	mk.Zero()
	if _, err := mk.DeriveHMAC(PurposeHMACAudit); err == nil {
		t.Fatal("zeroed master key must be unusable")
	}
	var nilKey *MasterKey
	nilKey.Zero() // 不得 panic
	if _, err := nilKey.DeriveHMAC(PurposeHMACAudit); !errors.Is(err, ErrMasterKeyEmpty) {
		t.Fatalf("nil master key err = %v, want ErrMasterKeyEmpty", err)
	}
}

func TestMasterKeyLenAndNilSafety(t *testing.T) {
	mk := mustMasterKey(t)
	if got := mk.Len(); got != 32 {
		t.Fatalf("Len = %d, want 32", got)
	}
	var nilKey *MasterKey
	if got := nilKey.Len(); got != 0 {
		t.Fatalf("nil Len = %d, want 0", got)
	}
	// nil 子密钥的 Zero 不得 panic（防御性：调用方常写成 defer key.Zero()）。
	var nilSM4 *SM4Key
	nilSM4.Zero()
	var nilHMAC *HMACKey
	nilHMAC.Zero()
}

func TestKeyKindAndPurposeString(t *testing.T) {
	if KindSM4.String() != "SM4" || KindHMAC.String() != "HMAC-SM3" ||
		KeyKind(99).String() != "unknown" {
		t.Fatalf("unexpected KeyKind strings: %q %q %q",
			KindSM4.String(), KindHMAC.String(), KeyKind(99).String())
	}
	list := Purposes()
	if len(list) != 8 {
		t.Fatalf("Purposes() must return the 8 frozen purposes, got %d", len(list))
	}
	if _, err := Purpose("bogus").Kind(); err == nil {
		t.Fatal("unknown purpose Kind must fail")
	}
	if _, err := Purpose("bogus").Length(); err == nil {
		t.Fatal("unknown purpose Length must fail")
	}
	if !strings.Contains(errString(Purpose("bogus")), "guanjin/sm4/field") {
		t.Fatal("unknown purpose error must list the allowed purposes (self-documenting)")
	}
}

func errString(p Purpose) string {
	_, err := p.Length()
	if err == nil {
		return ""
	}
	return err.Error()
}
