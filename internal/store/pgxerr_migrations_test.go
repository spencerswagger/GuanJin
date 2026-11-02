package store

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// unmappedUniqueConstraints 是**故意不映射**的唯一约束，每条都要写清原因。
//
// 为什么需要这份登记：AC-ENG-04 要求"23505 各类各触发一次"，而"各类"的权威来源是
// `migrations/`。新增唯一约束时：要么进 `uniqueRules`（有对外码），要么登记到这里
// （说明它到达 HTTP 层就是缺陷）。二者都不做 → 下面的测试红，评审时能被看见。
var unmappedUniqueConstraints = map[string]string{
	"identifiers_one_primary_per_user": "I6（最后一个主标识符不可降级）由 D3-B 定「无对外错误码」，" +
		"服务层校验；撞到 DB 索引即属缺陷 → 500",
	"user_visited_clients_uniq": "写入走 ON CONFLICT 幂等 upsert；撞上说明没走 upsert → 缺陷 → 500",
}

// 主键冲突（`<table>_pkey`）一律不映射：UUID v7 撞键是随机源或代码缺陷，
// 不是用户错误（除 clients_pkey 例外——client_id 是业务主键，见 uniqueRules）。
const primaryKeyCollisionReason = "主键冲突 = UUID 生成/赋值缺陷，不是用户错误 → 500"

var (
	reCreateTable   = regexp.MustCompile(`(?i)CREATE TABLE\s+(?:IF NOT EXISTS\s+)?([a-z_][a-z0-9_]*)`)
	reUniqueIdx     = regexp.MustCompile(`(?i)CREATE UNIQUE INDEX\s+([a-z_][a-z0-9_]*)`)
	reUniqueConstr  = regexp.MustCompile(`(?i)CONSTRAINT\s+([a-z_][a-z0-9_]*)\s+UNIQUE`)
	rePrimaryKeyDef = regexp.MustCompile(`(?i)PRIMARY KEY`)
)

// stripSQLComments 去掉 `--` 行注释。
//
// 必须先去注释再解析：`000015` 里有一句
// 「带 WHERE 的部分唯一索引只能 CREATE UNIQUE INDEX，不得写成 CONSTRAINT」，
// 注释里的 CONSTRAINT 紧跟着下一行的 CREATE UNIQUE INDEX，会把 "CREATE" 当成约束名
// 抽出来（本测试第一版就是这么红的）。顺带也避免把注释里的示例 DDL 当真。
func stripSQLComments(sql string) string {
	lines := strings.Split(sql, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// uniqueConstraintsInMigrations 从 migrations/*.up.sql 里抽取全部唯一性约束名。
//
// 只看 up.sql：down.sql 里是 DROP，抽出来的名字没有意义。
func uniqueConstraintsInMigrations(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}

	var (
		names []string
		cur   string
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		sql := stripSQLComments(string(raw))
		// 先按语句切块，才能把 PRIMARY KEY 归到正确的表上（`audit_logs` 的
		// 主键是 `(id, occurred_at)`，PG 自动命名为 `<table>_pkey`）。
		for _, stmt := range strings.Split(sql, ";") {
			if m := reCreateTable.FindStringSubmatch(stmt); m != nil {
				cur = strings.ToLower(m[1])
				if rePrimaryKeyDef.MatchString(stmt) {
					names = append(names, cur+"_pkey")
				}
			}
			for _, m := range reUniqueIdx.FindAllStringSubmatch(stmt, -1) {
				names = append(names, strings.ToLower(m[1]))
			}
			for _, m := range reUniqueConstr.FindAllStringSubmatch(stmt, -1) {
				names = append(names, strings.ToLower(m[1]))
			}
		}
	}
	sort.Strings(names)
	return names
}

// 迁移里的**每一个**唯一性约束，要么在 uniqueRules（有对外码），要么在
// unmappedUniqueConstraints（显式认定为缺陷），否则本测试失败。
//
// 这条测试就是技术栈 §5.3 那句"新增唯一约束必须同步补映射"的可执行版本。
func TestEveryUniqueConstraintIsClassified(t *testing.T) {
	names := uniqueConstraintsInMigrations(t)
	if len(names) < 15 {
		t.Fatalf("expect at least 15 unique constraints from migrations, got %d (%v)", len(names), names)
	}

	seen := map[string]bool{}
	for _, name := range names {
		seen[name] = true
		switch {
		case uniqueRules[name].code != "":
			// 已映射，OK。
		case unmappedUniqueConstraints[name] != "":
			// 已登记为缺陷，OK。
		case strings.HasSuffix(name, "_pkey"):
			// 主键冲突统一按缺陷处理（clients_pkey 已在 uniqueRules 里单独映射）。
		default:
			t.Errorf("唯一约束 %q 既不在 uniqueRules 也没登记为不映射 —— "+
				"按 AC-ENG-04/D4-D，新增唯一约束必须同步补映射或显式登记原因", name)
		}
	}

	// 反向：映射表里不许有"迁移中已不存在"的僵尸规则（改名后最容易漂移的地方）。
	for name := range uniqueRules {
		if !seen[name] {
			t.Errorf("uniqueRules 里的 %q 在 migrations/ 中不存在（约束改名了？）", name)
		}
	}
	for name := range unmappedUniqueConstraints {
		if !seen[name] {
			t.Errorf("unmappedUniqueConstraints 里的 %q 在 migrations/ 中不存在", name)
		}
	}
}

// 外键映射表同样不许有僵尸条目：PG 的外键名由「表_列_fkey」自动生成，
// 迁移改名后映射会静默失效（变成 400 而不是 404），必须靠测试发现。
func TestForeignKeyRulesMatchMigrations(t *testing.T) {
	dir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	found := map[string]bool{}
	reRef := regexp.MustCompile(`(?i)([a-z_][a-z0-9_]*)\s+[a-z0-9_\[\]]+(?:\s+NOT NULL)?\s+REFERENCES`)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, stmt := range strings.Split(stripSQLComments(string(raw)), ";") {
			m := reCreateTable.FindStringSubmatch(stmt)
			if m == nil {
				continue
			}
			table := strings.ToLower(m[1])
			for _, col := range reRef.FindAllStringSubmatch(stmt, -1) {
				found[table+"_"+strings.ToLower(col[1])+"_fkey"] = true
			}
		}
	}
	if len(found) == 0 {
		t.Fatal("failed to extract any foreign keys from migrations")
	}
	for name := range fkRules {
		if !found[name] {
			t.Errorf("fkRules 里的 %q 在 migrations/ 中不存在（外键改名了？）", name)
		}
	}
	// 抽查几条必须存在的：flows 的三个外键是 D5 的表结构规格。
	for _, must := range []string{
		"flows_user_id_fkey", "flows_session_id_fkey", "flows_client_id_fkey",
	} {
		if !found[must] {
			t.Errorf("migrations 里没有 %q（D5 表结构规格被改动？）", must)
		}
	}
}
