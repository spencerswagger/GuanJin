package credential

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

func TestPolicyValidate(t *testing.T) {
	good := Policy{MinLength: 12, CharClasses: 3, HistoryCount: 5, MaxAgeDays: 90}
	if err := good.Validate(); err != nil {
		t.Fatalf("合规策略应通过：%v", err)
	}
	cases := map[string]Policy{
		"min_length 太小":       {MinLength: 7, CharClasses: 2},
		"min_length 超过硬上限":    {MinLength: MaxPasswordBytes + 1, CharClasses: 2},
		"max_length 小于 min":   {MinLength: 12, MaxLength: 8, CharClasses: 2},
		"max_length 超过硬上限":    {MinLength: 12, MaxLength: MaxPasswordBytes + 1, CharClasses: 2},
		"char_classes 太少":     {MinLength: 12, CharClasses: 1},
		"char_classes 太多":     {MinLength: 12, CharClasses: 5},
		"history_count 为负":    {MinLength: 12, CharClasses: 2, HistoryCount: -1},
		"max_age_days 为负":     {MinLength: 12, CharClasses: 2, MaxAgeDays: -1},
		"char_classes 为 0":    {MinLength: 12},
		"min_length 刚好 8 可接受": {MinLength: 8, CharClasses: 4},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			err := p.Validate()
			wantErr := name != "min_length 刚好 8 可接受"
			if wantErr != (err != nil) {
				t.Fatalf("wantErr=%v，实际 %v", wantErr, err)
			}
			if wantErr && !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("应包 ErrInvalidPolicy，实际 %v", err)
			}
		})
	}
}

// 合规口令必须放行（反向断言：别把闸修成"什么都拦"）。
func TestEvaluateAcceptsCompliantPassword(t *testing.T) {
	p := Policy{MinLength: 12, CharClasses: 3, HistoryCount: 5}
	res, err := p.Evaluate(EvaluateInput{Password: "GuanJin#2026Strong"})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !res.OK() || res.Code != "" {
		t.Fatalf("合规口令应 OK，实际 %+v", res)
	}
}

// AC-REG-10 的核心：三码同时命中时 `code` 只回首个（WEAK_PASSWORD），
// 全量命中项进 violations[]，顺序固定 weak → reused → policy。
func TestAC_REG_10_PasswordCodeOrder(t *testing.T) {
	weakAndShort := "abc123" // 6 字符、内置弱口令、2 类字符（小写+数字）
	history := []PasswordEnvelope{newTestEnvelope(t, weakAndShort)}

	p := Policy{MinLength: 12, CharClasses: 2, HistoryCount: 5}
	res, err := p.Evaluate(EvaluateInput{Password: weakAndShort, History: history})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Code != httpx.CodeWeakPassword {
		t.Fatalf("首个码必须是 WEAK_PASSWORD（AC-REG-10 ①），实际 %s", res.Code)
	}
	if len(res.Violations) != 3 {
		t.Fatalf("三码同时命中应有 3 条 violations，实际 %d: %+v", len(res.Violations), res.Violations)
	}
	wantOrder := []httpx.Code{
		httpx.CodeWeakPassword,            // weak
		httpx.CodePasswordReused,          // reused
		httpx.CodePasswordPolicyViolation, // policy.min_length
	}
	for i, want := range wantOrder {
		if res.Violations[i].Code != want {
			t.Fatalf("第 %d 条 violations 应为 %s，实际 %s（顺序不得调换）", i, want, res.Violations[i].Code)
		}
		if res.Violations[i].Rule == "" {
			t.Fatalf("第 %d 条 violations 必须带可定位的 rule", i)
		}
	}
	if res.Violations[0].Kind != ViolationWeak || res.Violations[1].Kind != ViolationReused ||
		res.Violations[2].Kind != ViolationPolicy {
		t.Fatalf("kind 顺序不对: %+v", res.Violations)
	}
	if !strings.HasPrefix(res.Violations[2].Rule, "policy.") {
		t.Fatalf("第三条应是策略规则，实际 %s", res.Violations[2].Rule)
	}
}

// 复用检测只看最近 HistoryCount 条（不许多查，也不许少查）。
func TestEvaluateHistoryWindow(t *testing.T) {
	oldest := newTestEnvelope(t, "Oldest#Passw0rd")
	newer := newTestEnvelope(t, "Newer#Passw0rd1")

	t.Run("命中在窗口内", func(t *testing.T) {
		p := Policy{MinLength: 8, CharClasses: 2, HistoryCount: 2}
		res, err := p.Evaluate(EvaluateInput{Password: "Newer#Passw0rd1", History: []PasswordEnvelope{newer, oldest}})
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if res.Code != httpx.CodePasswordReused {
			t.Fatalf("应命中 PASSWORD_REUSED，实际 %+v", res)
		}
		if res.Violations[0].Rule != "reused.history[0]" {
			t.Fatalf("rule 应定位到具体历史位，实际 %s", res.Violations[0].Rule)
		}
	})

	t.Run("命中在窗口外", func(t *testing.T) {
		// HistoryCount=1 → 只查 newer，仍用旧口令（oldest）应放行。
		p := Policy{MinLength: 8, CharClasses: 2, HistoryCount: 1}
		res, err := p.Evaluate(EvaluateInput{Password: "Oldest#Passw0rd", History: []PasswordEnvelope{newer, oldest}})
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if !res.OK() {
			t.Fatalf("窗口外不应命中，实际 %+v", res)
		}
	})

	t.Run("HistoryCount=0 不查历史", func(t *testing.T) {
		p := Policy{MinLength: 8, CharClasses: 2, HistoryCount: 0}
		res, err := p.Evaluate(EvaluateInput{Password: "Newer#Passw0rd1", History: []PasswordEnvelope{newer}})
		if err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
		if !res.OK() {
			t.Fatalf("不查历史时不应命中复用，实际 %+v", res)
		}
	})
}

// 历史信封坏了必须如实报错（跳过去 = 复用检测静默少查一条）。
func TestEvaluateBrokenHistoryFailsClosed(t *testing.T) {
	broken := PasswordEnvelope{Algo: AlgoPBKDF2SM3, Iterations: 1000, Salt: "x", DK: "y", KeyLen: 32}
	p := Policy{MinLength: 8, CharClasses: 2, HistoryCount: 1}
	if _, err := p.Evaluate(EvaluateInput{Password: "GuanJin#2026Strong", History: []PasswordEnvelope{broken}}); !errors.Is(err, ErrWeakParams) {
		t.Fatalf("坏信封应报 ErrWeakParams，实际 %v", err)
	}
}

// 弱口令名单是"替换"而不是"追加"。
func TestEvaluateCustomWeakListReplacesBuiltin(t *testing.T) {
	p := Policy{MinLength: 8, CharClasses: 2, WeakPasswords: []string{"LetMeIn"}}
	// 内置表里的 password 在自定义名单下不再命中。
	res, err := p.Evaluate(EvaluateInput{Password: "Password1!"})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !res.OK() {
		t.Fatalf("自定义名单应替换内置表，实际 %+v", res)
	}
	// 自定义名单里的（大小写不敏感 + 去空白）必须命中。
	res, err = p.Evaluate(EvaluateInput{Password: "  letmein  "})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Code != httpx.CodeWeakPassword {
		t.Fatalf("自定义弱口令应命中 WEAK_PASSWORD，实际 %+v", res)
	}
}

// 空口令：min_length + char_classes 两条，且不触发任何派生（快）。
func TestEvaluateEmptyPassword(t *testing.T) {
	p := Policy{MinLength: 8, CharClasses: 4}
	res, err := p.Evaluate(EvaluateInput{Password: ""})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Code != httpx.CodeWeakPassword && res.Code != httpx.CodePasswordPolicyViolation {
		t.Fatalf("空口令必须被策略拒，实际 %+v", res)
	}
	if len(res.Violations) != 2 {
		t.Fatalf("空口令应命中 min_length + char_classes 两条，实际 %+v", res.Violations)
	}
	if res.Violations[0].Rule != "policy.min_length" || res.Violations[1].Rule != "policy.char_classes" {
		t.Fatalf("策略内规则顺序应为 min_length → char_classes，实际 %+v", res.Violations)
	}
}

// 超过上限：默认 128 字节（与 login_policies 的 CHECK 同源）。
func TestEvaluateMaxLength(t *testing.T) {
	p := Policy{MinLength: 8, CharClasses: 4}
	long := strings.Repeat("Aa1!", 40) // 160 字节
	res, err := p.Evaluate(EvaluateInput{Password: long})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.Violations) != 1 || res.Violations[0].Rule != "policy.max_length" {
		t.Fatalf("应只命中 max_length，实际 %+v", res.Violations)
	}

	// 显式 max_length 也要生效。
	p2 := Policy{MinLength: 8, MaxLength: 20, CharClasses: 4}
	res, err = p2.Evaluate(EvaluateInput{Password: strings.Repeat("Aa1!", 6)}) // 24 字节
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(res.Violations) != 1 || res.Violations[0].Rule != "policy.max_length" {
		t.Fatalf("显式 max_length 应生效，实际 %+v", res.Violations)
	}
}

func TestPolicyExpired(t *testing.T) {
	e := newTestEnvelope(t, "Str0ng-Passw0rd!")
	now := time.Now()

	expired, judged, err := (Policy{MinLength: 12, CharClasses: 3, MaxAgeDays: 90}).Expired(e, now)
	if err != nil || !judged || expired {
		t.Fatalf("刚设置的口令不应过期：expired=%v judged=%v err=%v", expired, judged, err)
	}

	old := e
	old.UpdatedAt = now.Add(-100 * 24 * time.Hour).UTC().Format(time.RFC3339)
	expired, judged, err = (Policy{MinLength: 12, CharClasses: 3, MaxAgeDays: 90}).Expired(old, now)
	if err != nil || !judged || !expired {
		t.Fatalf("超过 90 天应判过期：expired=%v judged=%v err=%v", expired, judged, err)
	}

	// 策略不判有效期 → judged=false（不许把"没判"当"没过期"或"已过期"）。
	if _, judged, _ := (Policy{MinLength: 12, CharClasses: 3}).Expired(old, now); judged {
		t.Fatal("MaxAgeDays=0 时不应判定")
	}

	// 信封没有 updated_at → judged=false（"没数据"不等于"已过期"）。
	noStamp := e
	noStamp.UpdatedAt = ""
	if _, judged, _ := (Policy{MinLength: 12, CharClasses: 3, MaxAgeDays: 90}).Expired(noStamp, now); judged {
		t.Fatal("缺 updated_at 时不应判定")
	}

	bad := e
	bad.UpdatedAt = "昨天"
	if _, _, err := (Policy{MinLength: 12, CharClasses: 3, MaxAgeDays: 90}).Expired(bad, now); err == nil {
		t.Fatal("坏 updated_at 必须报错")
	}
}

func TestCharClassCount(t *testing.T) {
	cases := map[string]int{
		"":          0,
		"abc":       1,
		"abc123":    2,
		"Abc123":    3,
		"Abc123!":   4,
		"中文口令":      1, // 非 ASCII 归入"符号"一类（不算小写/大写/数字）
		"中文Abc123!": 4,
	}
	for in, want := range cases {
		if got := charClassCount(in); got != want {
			t.Errorf("charClassCount(%q) = %d，期望 %d", in, got, want)
		}
	}
}
