//go:build integration

package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 本文件钉**执行计划级**证据：两段式清理的各段真的走各自的具名索引
// （第一段 `flows_expires_idx`，第二段 `flows_expired_cleanup_idx`）。
//
// 为什么"net/http 层面的字符串包含"不算：这两条都是带 `WHERE` 的**部分索引**，
// 只有谓词与索引定义同形时才可能被选中；一旦有人把语句改成 `status <> 'expired'`、
// 给列套函数、或把 `status='expired'` 去掉，索引立刻失效，而功能用例仍然全绿。
// 所以这里断言 **EXPLAIN 的计划节点**（索引名 + 扫描方式），并且断言用的 SQL
// 就是仓储里的常量（`markExpiredSQL` / `deleteExpiredBeforeSQL`）——"测的就是跑的"。

// explainLines 收集 `EXPLAIN (COSTS OFF)` 的全部输出行（args 用于带占位符的语句）。
func explainLines(t *testing.T, db *gorm.DB, stmt string, args ...any) []string {
	t.Helper()
	rows, err := db.Raw("EXPLAIN (COSTS OFF) "+stmt, args...).Rows()
	if err != nil {
		t.Fatalf("EXPLAIN 失败：%v\nSQL: %s", err, stmt)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("读取 EXPLAIN 行失败：%v", err)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 EXPLAIN 结果失败：%v", err)
	}
	if len(out) == 0 {
		t.Fatalf("EXPLAIN 没有任何输出行：SQL 可能没被真正计划\nSQL: %s", stmt)
	}
	return out
}

// TestDBFlowCleanupStage1UsesExpiresIndex 证明第一段（惰性过期）走 `flows_expires_idx`，
// 并顺带证明它真的把到期的活跃流置成了 `expired`（计划对了但语义错了同样不能过）。
func TestDBFlowCleanupStage1UsesExpiresIndex(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db) // 复用既有 fixture：唯一 client_id + 结束清理
	ctx := context.Background()

	const seed = 3000 // 数据量够大，规划器才有理由选索引；太小会 Seq Scan（那是统计问题，不是设计问题）
	err := db.WithContext(ctx).Exec(`
		INSERT INTO flows (id, client_id, target_acr, achieved_amr, completed_steps, status, expires_at)
		SELECT gen_random_uuid(), ?, 'AAL1', '{}', '[]'::jsonb, 'active',
		       now() + (g || ' seconds')::interval
		  FROM generate_series(1, ?) g`, f.clientID, seed).Error
	if err != nil {
		t.Fatalf("灌测试数据失败：%v", err)
	}
	// 挑 3 条改成已到期
	err = db.WithContext(ctx).Exec(`
		UPDATE flows SET expires_at = now() - interval '1 second'
		 WHERE id IN (SELECT id FROM flows WHERE client_id = ? ORDER BY id LIMIT 3)`, f.clientID).Error
	if err != nil {
		t.Fatalf("构造到期行失败：%v", err)
	}
	// 统计必须新鲜，否则规划器按 0 行估，计划不可信（也就不能拿它当证据）。
	if err := db.WithContext(ctx).Exec(`ANALYZE flows`).Error; err != nil {
		t.Fatalf("ANALYZE flows 失败：%v", err)
	}

	plan := strings.Join(explainLines(t, db, markExpiredSQL), "\n")
	if !strings.Contains(plan, "flows_expires_idx") {
		t.Fatalf("第一段没有用 flows_expires_idx —— 部分索引失效（谓词必须逐字是 status='active' AND expires_at <= now()）\n实际计划：\n%s", plan)
	}
	if !strings.Contains(plan, "Index Scan") && !strings.Contains(plan, "Bitmap Index Scan") {
		t.Fatalf("计划里出现索引名但不是索引扫描（可能是仅作为过滤条件/位图构建的误读）\n实际计划：\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan on flows") {
		t.Fatalf("第一段退化成全表扫描，flows_expires_idx 没被采用\n实际计划：\n%s", plan)
	}

	// 语义同测：真的把 3 条到期行置为 expired（行保留，I-F6 第一段）。
	affected, err := NewFlowRepo(db).MarkExpired(ctx)
	if err != nil {
		t.Fatalf("MarkExpired: %v", err)
	}
	if affected < 3 {
		t.Fatalf("应至少把 3 条到期流置为 expired，实际 %d", affected)
	}
	var stillActive int64
	if err := db.WithContext(ctx).Raw(
		`SELECT count(*) FROM flows WHERE client_id = ? AND status = 'active' AND expires_at <= now()`,
		f.clientID).Scan(&stillActive).Error; err != nil {
		t.Fatalf("复核失败：%v", err)
	}
	if stillActive != 0 {
		t.Fatalf("第一段跑完仍有 %d 条到期但 active 的流", stillActive)
	}
	var expired int64
	if err := db.WithContext(ctx).Raw(
		`SELECT count(*) FROM flows WHERE client_id = ? AND status = 'expired'`, f.clientID).Scan(&expired).Error; err != nil {
		t.Fatalf("复核失败：%v", err)
	}
	if expired < 3 {
		t.Fatalf("行必须保留（第一段只置状态不删），实际 expired=%d", expired)
	}
}

// TestDBFlowCleanupStage2UsesExpiredCleanupIdx 证明第二段（物理删除）走
// `flows_expired_cleanup_idx`——与第一段的 `flows_expires_idx` 对称。
//
// 依据：`decisions.md` D5「第二段清理索引裁定」（2026-11-04）——第二段谓词
// `status='expired' AND updated_at < cutoff` 与第一段的 `(expires_at) WHERE status='active'`
// **列不同、行集也不同**，所以新增独立的部分索引（`000016` 引入），不改造既有 4 条。
// 部分索引只有谓词与索引定义同形才会被选中；这里断言的就是仓储常量
// `deleteExpiredBeforeSQL`，改语句/改索引定义任何一边，这个用例都会红。
func TestDBFlowCleanupStage2UsesExpiredCleanupIdx(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	ctx := context.Background()

	// 绝大多数行是 active（不在本索引的行集里），只留少量满期 expired 行：
	// 谓词有足够选择性，规划器才有理由选部分索引而不是全表扫。
	const activeRows = 20000
	if err := db.WithContext(ctx).Exec(`
		INSERT INTO flows (id, client_id, target_acr, achieved_amr, completed_steps, status, expires_at)
		SELECT gen_random_uuid(), ?, 'AAL1', '{}', '[]'::jsonb, 'active',
		       now() + (g || ' seconds')::interval
		  FROM generate_series(1, ?) g`, f.clientID, activeRows).Error; err != nil {
		t.Fatalf("灌 active 行失败：%v", err)
	}
	// 满保留期（8 天 > ExpiredRetention 7 天）的 expired 行；updated_at 由触发器语义
	// 视为"置 expired 的时间"，这里显式写死以便构造满期。
	if err := db.WithContext(ctx).Exec(`
		INSERT INTO flows (id, client_id, target_acr, achieved_amr, completed_steps, status, expires_at, created_at, updated_at)
		SELECT gen_random_uuid(), ?, 'AAL1', '{}', '[]'::jsonb, 'expired',
		       now() - interval '8 days 1 hour', now() - interval '8 days', now() - interval '8 days'
		  FROM generate_series(1, 40) g`, f.clientID).Error; err != nil {
		t.Fatalf("灌满期 expired 行失败：%v", err)
	}
	// 统计必须新鲜，否则规划器按 0 行估，计划不可信（也就不能拿它当证据）。
	if err := db.WithContext(ctx).Exec(`ANALYZE flows`).Error; err != nil {
		t.Fatalf("ANALYZE flows 失败：%v", err)
	}

	cutoff := time.Now().UTC().Add(-ExpiredRetention)
	plan := strings.Join(explainLines(t, db, deleteExpiredBeforeSQL, cutoff), "\n")
	if !strings.Contains(plan, "flows_expired_cleanup_idx") {
		t.Fatalf("第二段没有用 flows_expired_cleanup_idx —— 部分索引失效（谓词必须逐字是 status='expired' AND updated_at < cutoff）\n实际计划：\n%s", plan)
	}
	if !strings.Contains(plan, "Index Scan") && !strings.Contains(plan, "Bitmap Index Scan") {
		t.Fatalf("计划里出现索引名但不是索引扫描（可能是仅作为过滤条件/位图构建的误读）\n实际计划：\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan on flows") {
		t.Fatalf("第二段退化成全表扫描，flows_expired_cleanup_idx 没被采用\n实际计划：\n%s", plan)
	}
}

// TestDBFlowCleanupStage2KeepsRowsWithinRetention 证明第二段的边界：
// **未满保留期**的 expired 行不得被删，满期的才删；并且删掉的就是那些行。
//
// 执行计划由 TestDBFlowCleanupStage2UsesExpiredCleanupIdx 钉（D5 裁定新增
// `flows_expired_cleanup_idx`）；本用例只钉**行为边界**，两者互补：计划对了但
// cutoff 语义错了同样不能过。
func TestDBFlowCleanupStage2KeepsRowsWithinRetention(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	ctx := context.Background()
	repo := NewFlowRepo(db)

	fresh := insertExpiredFlow(t, db, f.clientID, "1 minute")
	old := insertExpiredFlow(t, db, f.clientID, "8 days")

	cutoff := time.Now().UTC().Add(-ExpiredRetention)
	affected, err := repo.DeleteExpiredBefore(ctx, cutoff)
	if err != nil {
		t.Fatalf("DeleteExpiredBefore: %v", err)
	}
	if affected < 1 {
		t.Fatalf("应删掉至少 1 条满期行，实际 %d", affected)
	}

	if n := countFlows(t, db, fresh); n != 1 {
		t.Fatalf("未满 7 天的 expired 行不得删（否则 FLOW_EXPIRED 会提前变成 NOT_FOUND）")
	}
	if n := countFlows(t, db, old); n != 0 {
		t.Fatalf("满 7 天的 expired 行应被删掉")
	}
}

func insertExpiredFlow(t *testing.T, db *gorm.DB, clientID, age string) uuid.UUID {
	t.Helper()
	id := mustV7(t)
	err := db.Exec(`
		INSERT INTO flows (id, client_id, target_acr, achieved_amr, completed_steps, status, expires_at, created_at, updated_at)
		VALUES (?, ?, 'AAL1', '{}', '[]'::jsonb, 'expired', now() - interval '1 hour',
		        now() - (?::interval), now() - (?::interval))`,
		id, clientID, age, age).Error
	if err != nil {
		t.Fatalf("插入 expired 行失败：%v", err)
	}
	return id
}

func countFlows(t *testing.T, db *gorm.DB, id uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM flows WHERE id = ?`, id).Scan(&n).Error; err != nil {
		t.Fatalf("计数失败：%v", err)
	}
	return n
}
