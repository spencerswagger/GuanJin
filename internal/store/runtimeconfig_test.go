package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// fakeConfig 是 ConfigReader 的假实现（启动期校验不需要真库）。
type fakeConfig struct {
	rows map[string]*model.RuntimeConfig
	err  error
}

func (f *fakeConfig) Lookup(_ context.Context, key string) (*model.RuntimeConfig, error) {
	if f.err != nil {
		return nil, f.err
	}
	row, ok := f.rows[key]
	if !ok {
		// 与 RuntimeConfigRepo.Lookup 的行为保持一致：错误里带上键名。
		return nil, fmt.Errorf("%w: %q", ErrConfigKeyMissing, key)
	}
	return row, nil
}

func intRow(key, raw string) *model.RuntimeConfig {
	return &model.RuntimeConfig{Key: key, Value: model.JSONValue(raw), ValueType: "int"}
}

// jsonArrayRow 是 `factors.disabled` 这类 jsonb 数组键的测试行（D22）。
func jsonArrayRow(key, raw string) *model.RuntimeConfig {
	return &model.RuntimeConfig{Key: key, Value: model.JSONValue(raw), ValueType: "json"}
}

// 正常路径：600 秒 → 10 分钟。
func TestLoadFlowTTLHappyPath(t *testing.T) {
	reader := &fakeConfig{rows: map[string]*model.RuntimeConfig{
		KeyFlowTTLSeconds: intRow(KeyFlowTTLSeconds, "600"),
	}}
	ttl, err := LoadFlowTTL(context.Background(), reader)
	if err != nil {
		t.Fatalf("LoadFlowTTL: %v", err)
	}
	if ttl != 10*time.Minute {
		t.Fatalf("ttl = %v, want 10m", ttl)
	}
}

// 缺键 = fail-fast（D5）。**不许**回退到 600 之类的默认值。
func TestLoadFlowTTLMissingKeyFailsFast(t *testing.T) {
	reader := &fakeConfig{rows: map[string]*model.RuntimeConfig{}}
	_, err := LoadFlowTTL(context.Background(), reader)
	if !errors.Is(err, ErrConfigKeyMissing) {
		t.Fatalf("err = %v, want ErrConfigKeyMissing", err)
	}
}

// 值形态错误也要 fail-fast，而不是"当成默认值继续跑"。
func TestRequiredIntSecondsRejectsBadValues(t *testing.T) {
	cases := []struct {
		name    string
		row     *model.RuntimeConfig
		wantErr error
	}{
		{"wrong type", &model.RuntimeConfig{
			Key: KeyFlowTTLSeconds, Value: model.JSONValue("600"), ValueType: "long"}, ErrConfigTypeMismatch},
		{"string value", intRow(KeyFlowTTLSeconds, `"600"`), ErrConfigValueInvalid},
		{"zero", intRow(KeyFlowTTLSeconds, "0"), ErrConfigValueInvalid},
		{"negative", intRow(KeyFlowTTLSeconds, "-1"), ErrConfigValueInvalid},
		{"fraction", intRow(KeyFlowTTLSeconds, "1.5"), ErrConfigValueInvalid},
		{"bool", intRow(KeyFlowTTLSeconds, "true"), ErrConfigValueInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeConfig{rows: map[string]*model.RuntimeConfig{KeyFlowTTLSeconds: tc.row}}
			_, err := RequiredIntSeconds(context.Background(), reader, KeyFlowTTLSeconds)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// reader 自身报错（如库不可用）必须原样上抛，不能被当成"缺键"处理。
func TestRequiredIntSecondsPropagatesReaderError(t *testing.T) {
	boom := errors.New("db down")
	_, err := RequiredIntSeconds(context.Background(), &fakeConfig{err: boom}, KeyFlowTTLSeconds)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the reader error", err)
	}
	if errors.Is(err, ErrConfigKeyMissing) {
		t.Fatal("reader failure must not be reported as a missing key")
	}
	if _, err := RequiredIntSeconds(context.Background(), nil, KeyFlowTTLSeconds); err == nil {
		t.Fatal("nil reader must be rejected")
	}
}

// ValidateStartup 一次报**所有**缺失键（避免重启多次逐个找）。
func TestValidateStartupReportsAllMissingKeys(t *testing.T) {
	keys := StartupKeys()
	if len(keys) == 0 {
		t.Fatal("StartupKeys must not be empty")
	}
	reader := &fakeConfig{rows: map[string]*model.RuntimeConfig{}}
	_, err := ValidateStartup(context.Background(), reader)
	if err == nil {
		t.Fatal("expected failure when startup keys are missing")
	}
	for _, key := range keys {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("validation error must name the missing key %q, got: %v", key, err)
		}
	}

	ok := &fakeConfig{rows: map[string]*model.RuntimeConfig{
		KeyFlowTTLSeconds:       intRow(KeyFlowTTLSeconds, "600"),
		KeyChallengeTTLSeconds:  intRow(KeyChallengeTTLSeconds, "300"),
		KeyChallengeMaxAttempts: intRow(KeyChallengeMaxAttempts, "5"),
		// D22：平台级禁用集载体也是启动必备键（缺行 = fail-closed）。
		KeyFactorsDisabled: jsonArrayRow(KeyFactorsDisabled, `[]`),
	}}
	got, err := ValidateStartup(context.Background(), ok)
	if err != nil {
		t.Fatalf("ValidateStartup: %v", err)
	}
	if got.FlowTTL != 10*time.Minute {
		t.Fatalf("flow ttl = %v, want 10m", got.FlowTTL)
	}
	if got.ChallengeTTL != 5*time.Minute {
		t.Fatalf("challenge ttl = %v, want 5m（300 秒）", got.ChallengeTTL)
	}
	// 关键：5 必须原样是 **5 次**，不能被当成 5ns（D14① 点名的语义错）。
	if got.ChallengeMaxAttempts != 5 {
		t.Fatalf("challenge max_attempts = %d, want 5", got.ChallengeMaxAttempts)
	}
}

// 只缺一个键时也要把**那一个**报出来（而不是笼统的 "validation failed"）。
func TestValidateStartupNamesTheSingleMissingKey(t *testing.T) {
	reader := &fakeConfig{rows: map[string]*model.RuntimeConfig{
		KeyFlowTTLSeconds:      intRow(KeyFlowTTLSeconds, "600"),
		KeyChallengeTTLSeconds: intRow(KeyChallengeTTLSeconds, "300"),
		// challenge.max_attempts 缺失
	}}
	_, err := ValidateStartup(context.Background(), reader)
	if !errors.Is(err, ErrConfigKeyMissing) {
		t.Fatalf("err = %v, want ErrConfigKeyMissing", err)
	}
	if !strings.Contains(err.Error(), KeyChallengeMaxAttempts) {
		t.Fatalf("缺键报错必须点名 %q，实际：%v", KeyChallengeMaxAttempts, err)
	}
	if strings.Contains(err.Error(), KeyFlowTTLSeconds) {
		t.Fatalf("已存在的键不该出现在缺键报错里：%v", err)
	}
}

// 次数键的 value_type 写成别的类型必须报 ErrConfigTypeMismatch（不是静默取默认值）。
func TestValidateStartupRejectsNonIntValueType(t *testing.T) {
	reader := &fakeConfig{rows: map[string]*model.RuntimeConfig{
		KeyFlowTTLSeconds:       intRow(KeyFlowTTLSeconds, "600"),
		KeyChallengeTTLSeconds:  intRow(KeyChallengeTTLSeconds, "300"),
		KeyChallengeMaxAttempts: {Key: KeyChallengeMaxAttempts, Value: model.JSONValue(`"5"`), ValueType: "string"},
	}}
	_, err := ValidateStartup(context.Background(), reader)
	if !errors.Is(err, ErrConfigTypeMismatch) {
		t.Fatalf("err = %v, want ErrConfigTypeMismatch", err)
	}
}

// StartupKeys 必须逐字含 D5/D14 冻结的三个键名（拼错就是启动必挂）。
func TestStartupKeysContainsFrozenNames(t *testing.T) {
	want := []string{"flow.ttl_seconds", "challenge.ttl_seconds", "challenge.max_attempts",
		"factors.disabled"} // D22：平台级禁用集载体（jsonb 字符串数组）
	got := StartupKeys()
	if len(got) != len(want) {
		t.Fatalf("StartupKeys = %v, want %v", got, want)
	}
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("StartupKeys[%d] = %q, want %q（全量 %v）", i, got[i], k, got)
		}
	}
}

// 源码级检查：`flow.ttl_seconds` 的默认值（600）**不得**以常量形式出现在 store 包里。
//
// D5 的原话是"代码里不得出现 600 常量"。这条测试只扫**非注释行**，
// 避免把文档说明也算成违规；单测里出现的 600 是测试数据，因此排除 _test.go。
func TestNoHardcodedFlowTTLDefault(t *testing.T) {
	re := regexp.MustCompile(`\b600\b`)
	for _, name := range []string{"repo_flows.go", "runtimeconfig.go", "db.go", "pgxerr.go", "doc.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") ||
				strings.HasPrefix(trimmed, "*") {
				continue
			}
			if re.MatchString(line) {
				t.Errorf("%s:%d 出现 600 字面量（D5：flow.ttl_seconds 只能来自 runtime_config）: %s",
					name, i+1, trimmed)
			}
		}
	}
}

// 源码级检查：`challenge.ttl_seconds` 的初值（300）**不得**以常量形式出现在 store 包里。
//
// 与 D5 的 600 同款（D14①："代码里不得出现 300 / 5 常量"）：
// 一旦代码里留了 300，`AC-REG-01` 的"改键值必须跟着变"就变成"改键值可能没跟着变"。
func TestNoHardcodedChallengeTTLDefault(t *testing.T) {
	re := regexp.MustCompile(`\b300\b`)
	for _, name := range []string{"repo_challenges.go", "runtimeconfig.go", "db.go", "pgxerr.go", "doc.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") ||
				strings.HasPrefix(trimmed, "*") {
				continue
			}
			if re.MatchString(line) {
				t.Errorf("%s:%d 出现 300 字面量（D14①：challenge.ttl_seconds 只能来自 runtime_config）: %s",
					name, i+1, trimmed)
			}
		}
	}
}

// 正常路径：300 秒 → 5 分钟；`max_attempts` = 5 → **5 次**（不是 5ns）。
func TestLoadChallengeConfigHappyPath(t *testing.T) {
	reader := &fakeConfig{rows: map[string]*model.RuntimeConfig{
		KeyChallengeTTLSeconds:  intRow(KeyChallengeTTLSeconds, "300"),
		KeyChallengeMaxAttempts: intRow(KeyChallengeMaxAttempts, "5"),
	}}
	ctx := context.Background()

	ttl, err := LoadChallengeTTL(ctx, reader)
	if err != nil {
		t.Fatalf("LoadChallengeTTL: %v", err)
	}
	if ttl != 5*time.Minute {
		t.Fatalf("challenge ttl = %v, want 5m", ttl)
	}

	attempts, err := LoadChallengeMaxAttempts(ctx, reader)
	if err != nil {
		t.Fatalf("LoadChallengeMaxAttempts: %v", err)
	}
	if attempts != 5 {
		t.Fatalf("max_attempts = %d, want 5", attempts)
	}
}

// 缺键 = fail-fast：两个键都不得回退到 300 / 5。
func TestLoadChallengeConfigMissingKeyFailsFast(t *testing.T) {
	ctx := context.Background()
	empty := &fakeConfig{rows: map[string]*model.RuntimeConfig{}}
	if _, err := LoadChallengeTTL(ctx, empty); !errors.Is(err, ErrConfigKeyMissing) {
		t.Fatalf("LoadChallengeTTL err = %v, want ErrConfigKeyMissing", err)
	}
	if _, err := LoadChallengeMaxAttempts(ctx, empty); !errors.Is(err, ErrConfigKeyMissing) {
		t.Fatalf("LoadChallengeMaxAttempts err = %v, want ErrConfigKeyMissing", err)
	}
}

// RequiredPositiveInt 的判据：`value_type` 必须 int、值必须 > 0；**不**做时间换算。
func TestRequiredPositiveInt(t *testing.T) {
	ctx := context.Background()

	ok := &fakeConfig{rows: map[string]*model.RuntimeConfig{
		KeyChallengeMaxAttempts: intRow(KeyChallengeMaxAttempts, "3"),
	}}
	got, err := RequiredPositiveInt(ctx, ok, KeyChallengeMaxAttempts)
	if err != nil || got != 3 {
		t.Fatalf("RequiredPositiveInt = %d, %v; want 3", got, err)
	}

	cases := []struct {
		name    string
		row     *model.RuntimeConfig
		wantErr error
	}{
		{"wrong type", &model.RuntimeConfig{
			Key: KeyChallengeMaxAttempts, Value: model.JSONValue("5"), ValueType: "long"}, ErrConfigTypeMismatch},
		{"string value", intRow(KeyChallengeMaxAttempts, `"5"`), ErrConfigValueInvalid},
		{"zero", intRow(KeyChallengeMaxAttempts, "0"), ErrConfigValueInvalid},
		{"negative", intRow(KeyChallengeMaxAttempts, "-1"), ErrConfigValueInvalid},
		{"fraction", intRow(KeyChallengeMaxAttempts, "1.5"), ErrConfigValueInvalid},
		{"bool", intRow(KeyChallengeMaxAttempts, "true"), ErrConfigValueInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeConfig{rows: map[string]*model.RuntimeConfig{KeyChallengeMaxAttempts: tc.row}}
			_, err := RequiredPositiveInt(ctx, reader, KeyChallengeMaxAttempts)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}

	if _, err := RequiredPositiveInt(ctx, nil, KeyChallengeMaxAttempts); err == nil {
		t.Fatal("nil reader must be rejected")
	}
	// reader 故障必须原样上抛（不能被当成缺键，否则"库挂了"会被误报成"配置漏了"）。
	boom := errors.New("db down")
	if _, err := RequiredPositiveInt(ctx, &fakeConfig{err: boom}, KeyChallengeMaxAttempts); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the reader error", err)
	}
}

// `factors.disabled` 的启动校验（D22）：`[]` 合法；`null` / `{}` / 非数组 / 含空串 → fail-fast。
func TestValidateStartupRejectsBadFactorsDisabled(t *testing.T) {
	base := func(raw string) *fakeConfig {
		return &fakeConfig{rows: map[string]*model.RuntimeConfig{
			KeyFlowTTLSeconds:       intRow(KeyFlowTTLSeconds, "600"),
			KeyChallengeTTLSeconds:  intRow(KeyChallengeTTLSeconds, "300"),
			KeyChallengeMaxAttempts: intRow(KeyChallengeMaxAttempts, "5"),
			KeyFactorsDisabled:      jsonArrayRow(KeyFactorsDisabled, raw),
		}}
	}
	cfg, err := ValidateStartup(context.Background(), base(`[]`))
	if err != nil {
		t.Fatalf("空集 [] 必须通过启动校验：%v", err)
	}
	if len(cfg.FactorsDisabled) != 0 {
		t.Fatalf("空集应解析成 0 项，实际 %v", cfg.FactorsDisabled)
	}
	cfg, err = ValidateStartup(context.Background(), base(`["sms","totp"]`))
	if err != nil || len(cfg.FactorsDisabled) != 2 {
		t.Fatalf("两项数组应通过并解析出 2 项：cfg=%v err=%v", cfg.FactorsDisabled, err)
	}
	for _, bad := range []string{`null`, `{}`, `""`, `[1]`, `[""]`} {
		if _, err := ValidateStartup(context.Background(), base(bad)); err == nil {
			t.Fatalf("%s 必须在启动期 fail-fast（不许当空集）", bad)
		}
	}
	// value_type 不是 json 也要拦（例如被写成 int）。
	wrongType := &fakeConfig{rows: map[string]*model.RuntimeConfig{
		KeyFlowTTLSeconds:       intRow(KeyFlowTTLSeconds, "600"),
		KeyChallengeTTLSeconds:  intRow(KeyChallengeTTLSeconds, "300"),
		KeyChallengeMaxAttempts: intRow(KeyChallengeMaxAttempts, "5"),
		KeyFactorsDisabled:      intRow(KeyFactorsDisabled, "0"),
	}}
	if _, err := ValidateStartup(context.Background(), wrongType); err == nil {
		t.Fatal("value_type 不是 json 时必须 fail-fast")
	}
}
