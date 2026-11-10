package credential

import (
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// 基线冻结的参数（迭代 ≥100000、密钥 32B）。**测试里也不许降**：
// 一旦这里跟着降，AC-AUD-12④ 就变成"测试通过但实现降级"。
var testParams = crypto.PasswordParams{Iterations: crypto.MinPBKDF2Iterations, KeyLen: crypto.MinPasswordKeyLen}

// 信封缓存：PBKDF2-SM3 100k 迭代一次约 0.3s，测试里反复派生会把 CI 拖成分钟级。
// 信封是不可变值，同口令复用同一份不影响断言（"随机盐"那条自己直连 NewPasswordEnvelope 验）。
var (
	envelopeMu    sync.Mutex
	envelopeCache = map[string]PasswordEnvelope{}
)

func newTestEnvelope(t *testing.T, password string) PasswordEnvelope {
	t.Helper()
	envelopeMu.Lock()
	defer envelopeMu.Unlock()
	if e, ok := envelopeCache[password]; ok {
		return e
	}
	e, err := NewPasswordEnvelope(password, testParams, time.Now(), nil)
	if err != nil {
		t.Fatalf("NewPasswordEnvelope(%q): %v", password, err)
	}
	envelopeCache[password] = e
	return e
}

func TestNewPasswordEnvelopeAndVerify(t *testing.T) {
	e := newTestEnvelope(t, "Str0ng-Passw0rd!")
	if err := e.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if e.Algo != AlgoPBKDF2SM3 || e.Iterations < crypto.MinPBKDF2Iterations || e.KeyLen != crypto.MinPasswordKeyLen {
		t.Fatalf("参数没按基线冻结值写: %+v", e)
	}
	salt, err := base64.StdEncoding.DecodeString(e.Salt)
	if err != nil || len(salt) < MinPasswordSaltBytes {
		t.Fatalf("盐必须是 ≥16B 的 base64：len=%d err=%v", len(salt), err)
	}
	ok, err := e.Verify("Str0ng-Passw0rd!")
	if err != nil || !ok {
		t.Fatalf("正确口令应通过：ok=%v err=%v", ok, err)
	}
	ok, err = e.Verify("Str0ng-Passw0rd?")
	if err != nil || ok {
		t.Fatalf("错误口令必须失败：ok=%v err=%v", ok, err)
	}
	// 同一口令两次派生必须得到不同盐与不同派生值（随机盐生效）。
	// 注意：这里**直连** NewPasswordEnvelope，不走 newTestEnvelope 的缓存。
	other, err := NewPasswordEnvelope("Str0ng-Passw0rd!", testParams, time.Now(), nil)
	if err != nil {
		t.Fatalf("NewPasswordEnvelope(2): %v", err)
	}
	if other.Salt == e.Salt || other.DK == e.DK {
		t.Fatal("两次派生出现相同盐/派生值，随机盐失效")
	}
}

func TestNewPasswordEnvelopeRejectsBadInput(t *testing.T) {
	now := time.Now()
	if _, err := NewPasswordEnvelope("", testParams, now, nil); !errors.Is(err, ErrEmptyPassword) {
		t.Fatalf("空口令应报 ErrEmptyPassword，实际 %v", err)
	}
	long := strings.Repeat("a", MaxPasswordBytes+1)
	if _, err := NewPasswordEnvelope(long, testParams, now, nil); !errors.Is(err, ErrPasswordTooLong) {
		t.Fatalf("超长口令应报 ErrPasswordTooLong，实际 %v", err)
	}
	weak := crypto.PasswordParams{Iterations: 1000, KeyLen: 32}
	if _, err := NewPasswordEnvelope("abc12345678", weak, now, nil); !errors.Is(err, ErrWeakParams) {
		t.Fatalf("迭代低于下限必须报 ErrWeakParams，实际 %v", err)
	}
	shortKey := crypto.PasswordParams{Iterations: crypto.MinPBKDF2Iterations, KeyLen: 16}
	if _, err := NewPasswordEnvelope("abc12345678", shortKey, now, nil); !errors.Is(err, ErrWeakParams) {
		t.Fatalf("密钥长度低于下限必须报 ErrWeakParams，实际 %v", err)
	}
}

// 读信封必须挡住"被降级的参数"（这是场景 1：库被改）。
func TestPasswordEnvelopeValidateRejects(t *testing.T) {
	good := newTestEnvelope(t, "Str0ng-Passw0rd!")
	dk, _ := base64.StdEncoding.DecodeString(good.DK)

	cases := []struct {
		name string
		mut  func(*PasswordEnvelope)
		want error
	}{
		{"算法不是 PBKDF2-SM3", func(e *PasswordEnvelope) { e.Algo = "SHA256" }, ErrUnknownAlgo}, // NEGATIVE-SHA-TEST（负向用例：断言这些算法必须被拒；扫描器只在测试文件认这个标记）
		{"算法为空", func(e *PasswordEnvelope) { e.Algo = "" }, ErrUnknownAlgo},
		{"迭代被降到 1000", func(e *PasswordEnvelope) { e.Iterations = 1000 }, ErrWeakParams},
		{"key_len 低于下限", func(e *PasswordEnvelope) { e.KeyLen = 16 }, ErrWeakParams},
		{"盐不是 base64", func(e *PasswordEnvelope) { e.Salt = "!!!" }, ErrMalformedEnvelope},
		{"盐太短", func(e *PasswordEnvelope) {
			e.Salt = base64.StdEncoding.EncodeToString([]byte("short"))
		}, ErrWeakParams},
		{"派生值不是 base64", func(e *PasswordEnvelope) { e.DK = "!!!" }, ErrMalformedEnvelope},
		{"派生值长度与 key_len 不符", func(e *PasswordEnvelope) {
			e.DK = base64.StdEncoding.EncodeToString(dk[:8])
		}, ErrMalformedEnvelope},
		{"updated_at 不是 RFC3339", func(e *PasswordEnvelope) { e.UpdatedAt = "2026-11-02 10:00:00" }, ErrMalformedEnvelope},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := good
			tc.mut(&e)
			if err := e.Validate(); !errors.Is(err, tc.want) {
				t.Fatalf("期望 %v，实际 %v", tc.want, err)
			}
			// 坏信封不得进入校验路径（宁可报错，也不许用弱参数算出"通过"）。
			if _, err := e.Verify("Str0ng-Passw0rd!"); err == nil {
				t.Fatal("坏信封的 Verify 必须报错")
			}
		})
	}
}

func TestPasswordEnvelopeVerifyEdgeCases(t *testing.T) {
	e := newTestEnvelope(t, "Str0ng-Passw0rd!")
	// 空口令/超长口令直接判否，不做派生（省 CPU，也是防 DoS 的一环）。
	if ok, err := e.Verify(""); ok || err != nil {
		t.Fatalf("空口令应 (false,nil)，实际 (%v,%v)", ok, err)
	}
	if ok, err := e.Verify(strings.Repeat("a", MaxPasswordBytes+1)); ok || err != nil {
		t.Fatalf("超长口令应 (false,nil)，实际 (%v,%v)", ok, err)
	}
}

func TestPasswordEnvelopeNeedsRehash(t *testing.T) {
	e := newTestEnvelope(t, "Str0ng-Passw0rd!")
	if e.NeedsRehash(testParams) {
		t.Fatal("同参数不应要求重算")
	}
	higher := crypto.PasswordParams{Iterations: crypto.MinPBKDF2Iterations + 1000, KeyLen: crypto.MinPasswordKeyLen}
	if !e.NeedsRehash(higher) {
		t.Fatal("迭代被调高后应要求重算（登录成功时透明升级）")
	}
	broken := e
	broken.Iterations = 1000
	if broken.NeedsRehash(higher) {
		t.Fatal("坏信封不参与重算判定（Validate 会先报错）")
	}
}

func TestPasswordEnvelopeMarshalParse(t *testing.T) {
	e := newTestEnvelope(t, "Str0ng-Passw0rd!")
	raw, err := e.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "Str0ng-Passw0rd!") {
		t.Fatal("落库 JSON 里绝不能出现口令明文")
	}
	back, err := ParsePasswordEnvelope(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ok, err := back.Verify("Str0ng-Passw0rd!"); err != nil || !ok {
		t.Fatalf("往返后校验失败：%v %v", ok, err)
	}
	if _, err := ParsePasswordEnvelope([]byte("not json")); !errors.Is(err, ErrMalformedEnvelope) {
		t.Fatalf("坏 JSON 应报 ErrMalformedEnvelope，实际 %v", err)
	}
	weak := `{"algo":"PBKDF2-SM3","iterations":1000,"salt":"c2FsdHNhbHRzYWx0c2FsdA==","dk":"` +
		base64.StdEncoding.EncodeToString(make([]byte, 32)) + `","key_len":32}`
	if _, err := ParsePasswordEnvelope([]byte(weak)); !errors.Is(err, ErrWeakParams) {
		t.Fatalf("弱参数信封必须在解析时就被拒，实际 %v", err)
	}
	broken := e
	broken.Algo = "scrypt"
	if _, err := broken.Marshal(); !errors.Is(err, ErrUnknownAlgo) {
		t.Fatalf("非法信封不得序列化，实际 %v", err)
	}
}

func TestPasswordEnvelopeUpdatedAtTime(t *testing.T) {
	e := newTestEnvelope(t, "Str0ng-Passw0rd!")
	if _, ok, err := e.UpdatedAtTime(); !ok || err != nil {
		t.Fatalf("应有 updated_at：ok=%v err=%v", ok, err)
	}
	empty := e
	empty.UpdatedAt = ""
	if _, ok, err := empty.UpdatedAtTime(); ok || err != nil {
		t.Fatalf("空 updated_at 应 (false,nil)，实际 (%v,%v)", ok, err)
	}
	bad := e
	bad.UpdatedAt = "昨天"
	if _, _, err := bad.UpdatedAtTime(); err == nil {
		t.Fatal("坏 updated_at 必须报错")
	}
}

// 日志脱敏：信封的 %v/%#v 都不得带出盐与派生值。
func TestPasswordEnvelopeRedaction(t *testing.T) {
	e := newTestEnvelope(t, "Str0ng-Passw0rd!")
	for _, out := range []string{e.String(), e.GoString()} {
		if strings.Contains(out, e.Salt) || strings.Contains(out, e.DK) {
			t.Fatalf("脱敏失败：%s", out)
		}
	}
}
