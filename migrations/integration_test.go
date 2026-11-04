//go:build integration

package migrations_test

import (
	"database/sql"
	"os"
	"sort"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestIntegrationMigrationsApplied 是第一条**真连 Postgres** 的集成用例：
// 它证明「migrate up 之后 schema 真的长成 decisions 要求的样子」，
// 而不是只靠单测里假装的仓储。（Gate 口径：SKIP = 未验证 = 红，故缺 DSN 直接 Fatal。）
func TestIntegrationMigrationsApplied(t *testing.T) {
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("GUANJIN_DATABASE_URL")
	}
	if dsn == "" {
		t.Fatalf("缺少 GUANJIN_TEST_DSN：集成用例不得跳过（SKIP = 未验证 = 红）")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("连接数据库失败：%v", err)
	}

	// ① 迁移版本：golang-migrate 的 schema_migrations 是**单行**表（version + dirty），
	//    因此断言 version = 15（000000–000015 共 16 个版本、最大版本号 15）且非 dirty。
	var version int64
	var dirty bool
	if err := db.QueryRow(`SELECT version, dirty FROM schema_migrations`).Scan(&version, &dirty); err != nil {
		t.Fatalf("读 schema_migrations 失败：%v", err)
	}
	if version != 15 || dirty {
		t.Fatalf("迁移状态期望 version=15 dirty=false，实际 version=%d dirty=%v", version, dirty)
	}

	// ② 15 张逻辑表（audit_logs 的月分区是 BASE TABLE，需排除）
	rows, err := db.Query(`SELECT table_name FROM information_schema.tables
		WHERE table_schema='public' AND table_type='BASE TABLE' AND table_name <> 'schema_migrations'`)
	if err != nil {
		t.Fatalf("读表清单失败：%v", err)
	}
	defer rows.Close()
	var logical []string
	partitions := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("扫描表名失败：%v", err)
		}
		if len(name) > len("audit_logs_p") && name[:len("audit_logs_p")] == "audit_logs_p" && len(name) == len("audit_logs_p")+6 {
			partitions++
			continue
		}
		logical = append(logical, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表清单失败：%v", err)
	}
	sort.Strings(logical)
	want := []string{"audit_logs", "auth_events", "authenticators", "challenges", "clients", "flows",
		"identifiers", "login_policies", "policy_snapshots", "role_bindings", "runtime_config",
		"sessions", "signing_keys", "user_visited_clients", "users"}
	if len(logical) != len(want) {
		t.Fatalf("逻辑表期望 %d 张，实际 %d：%v", len(want), len(logical), logical)
	}
	for i := range want {
		if logical[i] != want[i] {
			t.Fatalf("表清单不符：期望[%d]=%s 实际[%d]=%s（全量 %v）", i, want[i], i, logical[i], logical)
		}
	}
	if partitions < 1 {
		t.Fatalf("audit_logs 无月分区（D4-C 要求按月分区），实际 %d 个", partitions)
	}

	// ③ flows 13 列 + 4 个具名索引 + I-F1 CHECK
	var cols int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='flows'`).Scan(&cols); err != nil {
		t.Fatalf("读 flows 列数失败：%v", err)
	}
	if cols != 13 {
		t.Fatalf("flows 列数期望 13，实际 %d", cols)
	}
	var idx int
	if err := db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND tablename='flows'
		AND indexname IN ('flows_one_active_per_user_client_uniq','flows_session_idx','flows_expires_idx','flows_client_created_idx')`).Scan(&idx); err != nil {
		t.Fatalf("读 flows 索引失败：%v", err)
	}
	if idx != 4 {
		t.Fatalf("flows 具名索引期望 4，实际 %d", idx)
	}
	var chk int
	if err := db.QueryRow(`SELECT count(*) FROM pg_constraint WHERE conname='flows_completed_requires_user'`).Scan(&chk); err != nil {
		t.Fatalf("读 I-F1 CHECK 失败：%v", err)
	}
	if chk != 1 {
		t.Fatalf("缺 I-F1 CHECK（flows_completed_requires_user）")
	}

	// ④ flow.ttl_seconds 已 seed = 600（D5 落地口径）
	var ttl string
	if err := db.QueryRow(`SELECT value::text FROM runtime_config WHERE key='flow.ttl_seconds'`).Scan(&ttl); err != nil {
		t.Fatalf("读 flow.ttl_seconds 失败：%v", err)
	}
	if ttl != "600" {
		t.Fatalf("flow.ttl_seconds 期望 600，实际 %q", ttl)
	}
}
