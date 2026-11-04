package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/schema"
)

// GORM 模型 ↔ 迁移列的漂移测试（S2 退出条件里的"漂移测试骨架"，这里先落 flows）。
//
// 为什么必须有：模型写错列名 GORM 不会报错，只会在运行期拼出一条
// `column "x" does not exist"`——而那条错误在 MapError 里是未映射（500），
// 排查成本极高。这里是编译期就能发现的一层。
func TestFlowModelColumnsMatchMigration(t *testing.T) {
	migrationCols := flowColumnsFromMigration(t)
	modelCols := dbColumnsOf(t, &Flow{})

	sort.Strings(migrationCols)
	sort.Strings(modelCols)
	if strings.Join(migrationCols, ",") != strings.Join(modelCols, ",") {
		t.Fatalf("flows 列漂移：\n migration = %v\n model     = %v", migrationCols, modelCols)
	}
	if len(migrationCols) != 13 {
		t.Fatalf("flows 必须 13 列（D5），got %d: %v", len(migrationCols), migrationCols)
	}
	// 冻结的列名清单也要对（防止有人改了 FlowColumns 却没人发现）。
	sort.Strings(flowColumnList)
	if strings.Join(migrationCols, ",") != strings.Join(flowColumnList, ",") {
		t.Fatalf("FlowColumns() 与迁移不一致：%v vs %v", flowColumnList, migrationCols)
	}
}

// 模型里不得出现软删除字段（CI 有静态扫描，这里是运行时断言的双保险）。
func TestNoSoftDeleteField(t *testing.T) {
	for _, v := range []any{&Flow{}, &RuntimeConfig{}} {
		cols := dbColumnsOf(t, v)
		for _, c := range cols {
			if c == "deleted_at" {
				t.Fatalf("%T must not have a soft-delete column", v)
			}
		}
	}
	s := parseSchema(t, &Flow{})
	if f := s.LookUpField("DeletedAt"); f != nil {
		t.Fatal("Flow must not embed gorm's soft delete field")
	}
}

// 匿名/可空列的映射要对：这些是最容易"以为是 NULL 其实写成了零值"的地方。
func TestFlowFieldNullability(t *testing.T) {
	s := parseSchema(t, &Flow{})
	for field, wantNullable := range map[string]bool{
		"UserID":         true,
		"SessionID":      true,
		"ConsumedAt":     true,
		"TraceID":        true,
		"ClientID":       false,
		"TargetACR":      false,
		"AchievedAMR":    false,
		"CompletedSteps": false,
		"Status":         false,
		"ExpiresAt":      false,
	} {
		f := s.LookUpField(field)
		if f == nil {
			t.Fatalf("field %s not found", field)
		}
		if f.NotNull != !wantNullable {
			t.Errorf("%s: NotNull = %v, want %v", field, f.NotNull, !wantNullable)
		}
	}
}

// UUID v7 由应用层生成（D4-A）：版本号必须是 7，variant 必须是 RFC4122。
func TestBaseModelGeneratesUUIDv7(t *testing.T) {
	var b BaseModel
	if err := b.BeforeCreate(nil); err != nil { //nolint:staticcheck // 钩子不依赖 *gorm.DB
		t.Fatalf("BeforeCreate: %v", err)
	}
	if b.ID == uuid.Nil {
		t.Fatal("id must be generated")
	}
	if got := b.ID.Version(); got != 7 {
		t.Fatalf("uuid version = %d, want 7", got)
	}
	if b.ID.Variant() != uuid.RFC4122 {
		t.Fatalf("uuid variant = %v, want RFC4122", b.ID.Variant())
	}

	// v7 是时间有序的：连续两个 id 的字典序必须随时间递增（取证实证依赖这一点）。
	var later BaseModel
	if err := later.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if later.ID.String() <= b.ID.String() {
		t.Fatalf("v7 ids must be time-ordered: %s <= %s", later.ID, b.ID)
	}
}

// 已显式指定 ID 时不覆盖（导入/测试固定值场景）。
func TestBaseModelKeepsExplicitID(t *testing.T) {
	fixed := uuid.MustParse("018f0000-0000-7000-8000-000000000001")
	b := BaseModel{ID: fixed}
	if err := b.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if b.ID != fixed {
		t.Fatalf("explicit id must not be overwritten, got %s", b.ID)
	}
}

func TestAppendOnlyModelGeneratesUUIDv7(t *testing.T) {
	var a AppendOnlyModel
	if err := a.BeforeCreate(nil); err != nil {
		t.Fatalf("BeforeCreate: %v", err)
	}
	if a.ID.Version() != 7 {
		t.Fatalf("uuid version = %d, want 7", a.ID.Version())
	}
	// 只增表没有 updated_at。
	if f := parseSchema(t, &AppendOnlyModel{}).LookUpField("UpdatedAt"); f != nil {
		t.Fatal("append-only model must not carry updated_at")
	}
}

// text[] 读写：写出的字面量 PG 能吃，读回来逐项一致（含引号/反斜杠/逗号/花括号）。
func TestStringArrayRoundTrip(t *testing.T) {
	cases := [][]string{
		{},
		{"pwd"},
		{"pwd", "totp"},
		{`a,b`},
		{`quote"inside`},
		{`back\slash`},
		{`{braces}`},
		{"空", "中文"},
		{""},
	}
	for _, items := range cases {
		arr := StringArray(items)
		v, err := arr.Value()
		if err != nil {
			t.Fatalf("Value(%v): %v", items, err)
		}
		literal, ok := v.(string)
		if !ok {
			t.Fatalf("Value must return string literal, got %T", v)
		}
		var back StringArray
		if err := back.Scan(literal); err != nil {
			t.Fatalf("Scan(%q): %v", literal, err)
		}
		if len(back) != len(items) {
			t.Fatalf("round-trip len %d != %d (%q -> %v)", len(back), len(items), literal, back)
		}
		for i := range items {
			if back[i] != items[i] {
				t.Fatalf("round-trip item %d: %q != %q (literal %q)", i, back[i], items[i], literal)
			}
		}
	}

	// nil 必须写成 `{}`（NOT NULL 列不允许 NULL）。
	var nilArr StringArray
	if v, _ := nilArr.Value(); v.(string) != "{}" {
		t.Fatalf("nil array must encode as {}, got %v", v)
	}
	// 裸元素（PG 也可能返回不加引号的形态）。
	var bare StringArray
	if err := bare.Scan("{a,b}"); err != nil {
		t.Fatalf("Scan bare: %v", err)
	}
	if len(bare) != 2 || bare[0] != "a" || bare[1] != "b" {
		t.Fatalf("bare array parse = %v", bare)
	}
	// 非法形态不 panic，返回空。
	var bad StringArray
	if err := bad.Scan("not-an-array"); err != nil || len(bad) != 0 {
		t.Fatalf("invalid literal must yield empty array, got %v %v", bad, err)
	}
	var src any = 42
	if err := bad.Scan(src); err == nil {
		t.Fatal("unsupported source type must error")
	}
}

// completed_steps 的 jsonb 读写。
func TestStepListRoundTrip(t *testing.T) {
	at := time.Date(2026, time.November, 2, 10, 0, 0, 0, time.UTC)
	steps := StepList{{Factor: "pwd", ChallengeID: "c-1", At: at}}

	v, err := steps.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	var back StepList
	if err := back.Scan(v); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(back) != 1 || back[0].Factor != "pwd" || back[0].ChallengeID != "c-1" ||
		!back[0].At.Equal(at) {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
	if !json.Valid([]byte(v.(string))) {
		t.Fatalf("Value must emit valid json: %v", v)
	}

	var nilList StepList
	if v, _ := nilList.Value(); v.(string) != "[]" {
		t.Fatalf("nil step list must encode as [], got %v", v)
	}
	var fromNil StepList
	if err := fromNil.Scan(nil); err != nil || len(fromNil) != 0 {
		t.Fatalf("nil source must yield empty list, got %v %v", fromNil, err)
	}
	var bad StepList
	if err := bad.Scan("{"); err == nil {
		t.Fatal("invalid json must error")
	}
	var src any = 3.14
	if err := bad.Scan(src); err == nil {
		t.Fatal("unsupported source type must error")
	}
}

// runtime_config 的 jsonb 标量解释：type 说 int，就不能是字符串。
func TestJSONValueAsInt(t *testing.T) {
	cases := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{"600", 600, false},
		{"0", 0, false},
		{"-1", -1, false},
		{`"600"`, 0, true}, // 带引号的字符串不是数字
		{"600.5", 0, true}, // 小数不是整数
		{"true", 0, true},
		{"null", 0, true},
		{"not-json", 0, true},
	}
	for _, tc := range cases {
		got, err := JSONValue(tc.raw).AsInt()
		if tc.wantErr != (err != nil) {
			t.Errorf("AsInt(%q) err = %v, wantErr %v", tc.raw, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("AsInt(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestJSONValueAsStringAndBool(t *testing.T) {
	if s, err := (JSONValue(`"AAL1"`)).AsString(); err != nil || s != "AAL1" {
		t.Fatalf("AsString = %q, %v", s, err)
	}
	if _, err := (JSONValue(`1`)).AsString(); err == nil {
		t.Fatal("AsString on number must error")
	}
	if b, err := (JSONValue(`true`)).AsBool(); err != nil || !b {
		t.Fatalf("AsBool = %v, %v", b, err)
	}
	if _, err := (JSONValue(`"true"`)).AsBool(); err == nil {
		t.Fatal("AsBool on string must error")
	}
}

func TestJSONValueValuer(t *testing.T) {
	if _, err := (JSONValue(nil)).Value(); err == nil {
		t.Fatal("empty value must error (runtime_config.value is NOT NULL)")
	}
	if _, err := (JSONValue(`{bad`)).Value(); err == nil {
		t.Fatal("invalid json must error before hitting the DB")
	}
	v, err := (JSONValue(`{"a":1}`)).Value()
	if err != nil || v.(string) != `{"a":1}` {
		t.Fatalf("Value = %v, %v", v, err)
	}
	var j JSONValue
	if err := j.Scan(nil); err != nil || j != nil {
		t.Fatalf("Scan(nil) = %v, %v", j, err)
	}
	if err := j.Scan([]byte(`7`)); err != nil || string(j) != "7" {
		t.Fatalf("Scan(bytes) = %v, %v", j, err)
	}
	if err := j.Scan("8"); err != nil || string(j) != "8" {
		t.Fatalf("Scan(string) = %v, %v", j, err)
	}
	var src any = 9
	if err := j.Scan(src); err == nil {
		t.Fatal("unsupported source type must error")
	}
	if (JSONValue("600")).String() != "600" {
		t.Fatal("String must return raw json")
	}
}

// ---- helpers ----

var flowColumnList = FlowColumns()

func flowColumnsFromMigration(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "000015_create_flows.up.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(raw)
	start := strings.Index(sql, "CREATE TABLE flows")
	if start < 0 {
		t.Fatal("CREATE TABLE flows not found")
	}
	block := sql[start:]
	if i := strings.Index(block, "CONSTRAINT"); i > 0 {
		block = block[:i]
	}

	// 列定义行：4 空格缩进 + 列名 + 至少一个空白 + 小写类型。
	// 用「后面必须跟小写类型」把 `PRIMARY KEY` / `CHECK (` 这类行排除掉。
	re := regexp.MustCompile(`(?m)^\s{4}([a-z_][a-z0-9_]*)[ \t]+[a-z]`)
	var cols []string
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		cols = append(cols, m[1])
	}
	if len(cols) == 0 {
		t.Fatalf("failed to parse columns from flows migration")
	}
	return cols
}

func parseSchema(t *testing.T, v any) *schema.Schema {
	t.Helper()
	s, err := schema.Parse(v, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("schema.Parse(%T): %v", v, err)
	}
	return s
}

func dbColumnsOf(t *testing.T, v any) []string {
	t.Helper()
	s := parseSchema(t, v)
	cols := make([]string, 0, len(s.Fields))
	for _, f := range s.Fields {
		if f.DBName == "" {
			continue
		}
		cols = append(cols, f.DBName)
	}
	return cols
}
