//go:build integration

package store

import (
	"sort"
	"strings"
	"testing"
)

// flowsNamedIndexes 是 `flows` 上**全部**具名索引（含主键）——D5 的索引清单：
// 5 条业务索引 + 1 条主键。与 `.github/workflows/ci.yml` 的 `flows_named_indexes`
// 断言同一口径（CI 在 `public` 上数 5 条），这里额外做**集合并校验**：
// 多一条、少一条、改名都红，避免"迁移加了索引但台账没记"或"迁移漏了索引却没人发现"。
var flowsNamedIndexes = []string{
	"flows_pkey",
	"flows_one_active_per_user_client_uniq",
	"flows_session_idx",
	"flows_expires_idx",
	"flows_client_created_idx",
	"flows_expired_cleanup_idx", // 000016 引入，D5「第二段清理索引裁定」
}

// flowsBusinessIndexCount 是 D5 明文写死的业务索引条数（不含主键）。
const flowsBusinessIndexCount = 5

// TestDBFlowsNamedIndexesMatchLedger 证明库里的 `flows` 索引集合与 D5 台账逐字一致。
//
// 用 `current_schema()` 而不是写死 `public`：CI 的迁移落在 public，本地隔离验证
// 可能落在专用 schema，两种环境测的是**同一份迁移**产出的集合。
func TestDBFlowsNamedIndexesMatchLedger(t *testing.T) {
	db := openTestDB(t)

	rows, err := db.Raw(
		`SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename = 'flows'`).Rows()
	if err != nil {
		t.Fatalf("查询 pg_indexes 失败：%v", err)
	}
	defer func() { _ = rows.Close() }()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("读取 indexname 失败：%v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 pg_indexes 结果失败：%v", err)
	}

	want := append([]string(nil), flowsNamedIndexes...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("flows 索引集合与 D5 台账不一致\n期望（%d 条）：%s\n实际（%d 条）：%s",
			len(want), strings.Join(want, ", "), len(got), strings.Join(got, ", "))
	}

	if n := len(got) - 1; n != flowsBusinessIndexCount {
		t.Fatalf("flows 业务索引（不含主键）应为 %d 条（D5），实际 %d 条：%s",
			flowsBusinessIndexCount, n, strings.Join(got, ", "))
	}
}

// TestDBFlowsExpiredCleanupIdxShape 钉住 000016 的**索引形状**：
// 列 = `updated_at`、部分谓词 = `status = 'expired'`。
//
// 只断言"名字存在"不够：把索引改成 `(created_at)` 或去掉 WHERE（变成全表索引）
// 名字仍然对，但第二段的计划语义已经完全变了。名字 + 形状一起钉，才叫"台账一致"。
func TestDBFlowsExpiredCleanupIdxShape(t *testing.T) {
	db := openTestDB(t)

	var def string
	if err := db.Raw(
		`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND tablename = 'flows' AND indexname = 'flows_expired_cleanup_idx'`).
		Scan(&def).Error; err != nil {
		t.Fatalf("查询 flows_expired_cleanup_idx 定义失败：%v", err)
	}
	if def == "" {
		t.Fatalf("库中没有 flows_expired_cleanup_idx —— 000016 未应用（先跑 migrate up）")
	}

	// 归一化后逐项断言，避免空格/大小写差异造成假红，同时保留"哪一项不对"的定位信息。
	norm := strings.ToLower(strings.Join(strings.Fields(def), " "))
	for _, want := range []string{"(updated_at)", "where (status = 'expired'::text)"} {
		if !strings.Contains(norm, want) {
			t.Fatalf("flows_expired_cleanup_idx 形状不符 D5：期望包含 %q\n实际定义：%s", want, def)
		}
	}
}
