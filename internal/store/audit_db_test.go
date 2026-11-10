//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/testdb"
)

// 本文件补 `AC-AUD-02` 的真库行为证据：`audit_logs` 的 `forbid_mutation()` 触发器**官有**，
// 且 UPDATE / DELETE 真的被 PostgreSQL 拒（`restrict_violation` / 23001）。
//
// 为什么非要真库：触发器是 DB 对象，桩掉它再断言等于断言自己写的桩。
// 另外这里顺带证明 D4-C 的一个前提——**分区上也真的有触发器**：
// PG≥11 在 `CREATE TABLE … PARTITION OF` 时会复制父表触发器，但那是"应该发生"，
// 不是"已经发生"；锚点与链式完整性全建立在这条前提上。

// TestDBAuditLogsAppendOnlyTriggerExists 父表与当月分区各有一把。
func TestDBAuditLogsAppendOnlyTriggerExists(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var onParent int64
	err := db.WithContext(ctx).Raw(
		`SELECT count(*) FROM pg_trigger
		  WHERE tgrelid = 'audit_logs'::regclass
		    AND tgname = 'audit_logs_append_only'
		    AND NOT tgisinternal`).Scan(&onParent).Error
	if err != nil {
		t.Fatalf("查询父表触发器失败：%v", err)
	}
	if onParent != 1 {
		t.Fatalf("audit_logs 父表应有且仅有 1 个 audit_logs_append_only 触发器，实际 %d（AC-AUD-02）", onParent)
	}

	var partition string
	if err := db.WithContext(ctx).Raw(
		`SELECT c.relname::text
		   FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		  WHERE i.inhparent = 'audit_logs'::regclass
		  ORDER BY 1 DESC LIMIT 1`).Scan(&partition).Error; err != nil {
		t.Fatalf("查询 audit_logs 分区失败：%v", err)
	}
	if partition == "" {
		t.Fatalf("audit_logs 没有任何分区：000011 的初始化分区没建出来（D4-C）")
	}

	var onPartition int64
	if err := db.WithContext(ctx).Raw(
		`SELECT count(*) FROM pg_trigger
		  WHERE tgrelid = to_regclass(?)
		    AND tgname = 'audit_logs_append_only'
		    AND NOT tgisinternal`, partition).Scan(&onPartition).Error; err != nil {
		t.Fatalf("查询分区触发器失败：%v", err)
	}
	if onPartition != 1 {
		t.Fatalf("分区 %s 上应复制到 audit_logs_append_only，实际 %d —— 插入该分区的行将失去防改防删保护",
			partition, onPartition)
	}
}

// TestDBAuditLogsRejectsUpdateAndDelete 写入一行后，UPDATE 与 DELETE 必须被拒且行仍在。
func TestDBAuditLogsRejectsUpdateAndDelete(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	id := mustV7(t)
	action := "test.append_only." + id.String()

	err := db.WithContext(ctx).Exec(
		`INSERT INTO audit_logs (id, action, result, integrity_tag, occurred_at)
		 VALUES (?, ?, 'success', ?, now())`, id, action, []byte("chain-tag")).Error
	if err != nil {
		t.Fatalf("写入审计行失败：%v", err)
	}
	t.Cleanup(func() { cleanupAuditRow(t, db, id) })

	updErr := db.WithContext(ctx).Exec(`UPDATE audit_logs SET action = 'tampered' WHERE id = ?`, id).Error
	assertRestrictViolation(t, updErr, "UPDATE")

	// 注意：这里的 UPDATE/DELETE 是**故意要被数据库拒绝**的（验证 append-only 触发器），
	// 不是清理动作，所以**不加** testdb 护栏、也不加 schema 限定之外的处理——
	// 它失败才是判据 AC-AUD-02 的通过条件。
	delErr := db.WithContext(ctx).Exec(`DELETE FROM audit_logs WHERE id = ?`, id).Error
	assertRestrictViolation(t, delErr, "DELETE")

	var survived int64
	if err := db.WithContext(ctx).Raw(`SELECT count(*) FROM audit_logs WHERE id = ?`, id).Scan(&survived).Error; err != nil {
		t.Fatalf("复核行是否仍在失败：%v", err)
	}
	if survived != 1 {
		t.Fatalf("被拒后行必须原样保留，实际 %d 行", survived)
	}
}

// assertRestrictViolation 断言错误是 PostgreSQL 的 restrict_violation（23001），
// 而不是"应用层自己拦了一下"或别的错误。
func assertRestrictViolation(t *testing.T, err error, op string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s 竟然成功了：audit_logs 的只增保护没生效（AC-AUD-02）", op)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s 应返回 PostgreSQL 错误，实际 %T: %v", op, err, err)
	}
	if pgErr.Code != sqlStateRestrictViolation {
		t.Fatalf("%s 的 SQLSTATE 应为 %s（restrict_violation），实际 %s: %s",
			op, sqlStateRestrictViolation, pgErr.Code, pgErr.Message)
	}
	if pgErr.Message == "" {
		t.Fatalf("%s 的错误消息为空：排障时看不到是「只增表」拦的", op)
	}
}

// cleanupAuditRow 用最少的权限把测试行清掉：只对**该行落进去的那个分区**
// 临时禁触发器，删完立刻恢复（owner 即可，不需要超级用户）。
func cleanupAuditRow(t *testing.T, db *gorm.DB, id uuid.UUID) {
	t.Helper()
	// **安全护栏**：这段会临时关 append-only 触发器并删审计行，必须确认是测试库
	// （把 DSN 指到非测试库时直接失败，而不是删真数据）。
	testdb.RequireTestDB(t, db)
	var partition string
	if err := db.Raw(`SELECT tableoid::regclass::text FROM audit_logs WHERE id = ?`, id).Scan(&partition).Error; err != nil || partition == "" {
		return // 行没写进去（前面的用例已失败），无需清理
	}
	// 关触发器 → 删行 → 开触发器**必须在同一事务里**：PG 的触发器开关是持久化表属性，
	// 分开提交时若中途失败（panic/杀进程）会让 append-only 保护长期失效。
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s DISABLE TRIGGER audit_logs_append_only`, partition)).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM public.audit_logs WHERE id = ?`, id).Error; err != nil {
			return err
		}
		if err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s ENABLE TRIGGER audit_logs_append_only`, partition)).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Logf("cleanupAuditRow: %v（行可能已随用例失败残留）", err)
	}
}
