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
		KeyFlowTTLSeconds: intRow(KeyFlowTTLSeconds, "600"),
	}}
	got, err := ValidateStartup(context.Background(), ok)
	if err != nil {
		t.Fatalf("ValidateStartup: %v", err)
	}
	if got[KeyFlowTTLSeconds] != 10*time.Minute {
		t.Fatalf("flow ttl = %v, want 10m", got[KeyFlowTTLSeconds])
	}
}

// StartupKeys 必须含 D5 冻结的那个键名（拼错就是启动必挂）。
func TestStartupKeysContainsFlowTTL(t *testing.T) {
	found := false
	for _, k := range StartupKeys() {
		if k == "flow.ttl_seconds" {
			found = true
		}
	}
	if !found {
		t.Fatalf("StartupKeys must contain flow.ttl_seconds, got %v", StartupKeys())
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
