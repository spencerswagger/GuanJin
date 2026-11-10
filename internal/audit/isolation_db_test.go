//go:build integration

package audit

// 审计集成用例的**schema 隔离**（S10 后的测试基建修复）。
//
// 背景（实测）：审计用例必须往"链尾之后的月份"写，才能验证链序与下界守卫；但共享库
// `guanjin_test` 的 `audit_logs` 是**全局单链**，一旦被推到 2048-xx，任何用真实 `now()`
// 写审计的路径（例如 `internal/store` 的平台级因子用例）都会撞下界守卫
// `occurred_at predates the tail partition month` —— 这不是产品缺陷（守卫本身是对的），
// 而是**测试往共享库写未来时间**造成的顺序依赖。
//
// 做法：TestMain 建独立 schema `audit_test`，本包所有连接用
// `options=-csearch_path=audit_test` 指过去；分区预建/链尾/锚点全部隔离在 audit_test 内，
// `public` 的链尾保持在真实月份。

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gorm.io/gorm"
)

// isolatedSchema 是审计集成用例独占的 schema。
const isolatedSchema = "audit_test"

// isolatedDSN 给 DSN 追加 `search_path=audit_test`（pgx/libpq 的 `options` 参数）。
func isolatedDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	if strings.Contains(dsn, "options=") {
		return dsn // 已显式指定 options，尊重调用方
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "options=-csearch_path%3D" + isolatedSchema
}

// ensureIsolatedSchema 建 schema 与两张被审计用例用到的表（`audit_logs` 分区父表、
// `runtime_config`），并把**已污染的 public 存量清掉**（幂等）。
//
// 表结构照抄 `migrations/000011`（audit_logs）与 `migrations/000014`（runtime_config）；
// 这里只建被测代码真正访问的列，足够验证哈希链、锚点与分区行为。
func ensureIsolatedSchema(db *gorm.DB) error {
	stmts := []string{
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, isolatedSchema),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.audit_logs (
			id            uuid        NOT NULL,
			actor_user_id uuid,
			actor_role    text,
			action        text        NOT NULL,
			target_type   text,
			target_id     text,
			client_id     text,
			before        jsonb,
			after         jsonb,
			result        text        NOT NULL CHECK (result IN ('success','failure')),
			integrity_tag bytea       NOT NULL,
			prev_tag      bytea,
			signature     bytea,
			ip            inet,
			trace_id      text,
			occurred_at   timestamptz NOT NULL DEFAULT now(),
			created_at    timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (id, occurred_at)
		) PARTITION BY RANGE (occurred_at)`, isolatedSchema),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.runtime_config (
			key         text        PRIMARY KEY,
			value       jsonb       NOT NULL,
			value_type  text        NOT NULL DEFAULT 'json',
			description text,
			is_secret   boolean     NOT NULL DEFAULT false,
			updated_by  uuid,
			created_at  timestamptz NOT NULL DEFAULT now(),
			updated_at  timestamptz NOT NULL DEFAULT now()
		)`, isolatedSchema),
	}
	for _, stmt := range stmts {
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("ensure isolated schema: %w", err)
		}
	}
	return nil
}

// purgeSharedFutureRows 幂等清理**共享库里被历史运行写进去的未来行与未来分区**。
//
// 判据：`occurred_at >= 当月 + 2 个月` 视为"测试写进去的未来数据"（生产不会预写未来）。
// 返回清理前后的 `max(occurred_at)`，供 PR 与日志核对（要求：清完回到当月）。
func purgeSharedFutureRows(db *gorm.DB) (before, after string, err error) {
	ctx := context.Background()
	var b, a struct{ Max *string }
	_ = db.Raw(`SELECT to_char(max(occurred_at), 'YYYY-MM-DD"T"HH24:MI:SSOF') AS max FROM audit_logs`).Scan(&b).Error
	if b.Max != nil {
		before = *b.Max
	}
	// ① 未来分区**整表 drop**（顺带把里面的行带走；`audit_logs` 有 append-only 触发器，
	// DELETE 会被 `forbid_mutation` 拒，所以先 drop 分区、再对残留行临时关触发器删）。
	futureMonths := []string{}
	if err := db.WithContext(ctx).Raw(`
		SELECT to_char(d, 'YYYYMM') FROM generate_series(
			date_trunc('month', now()) + interval '2 months',
			date_trunc('month', now()) + interval '120 months',
			interval '1 month') AS d`).Scan(&futureMonths).Error; err != nil {
		return before, "", fmt.Errorf("list future months: %w", err)
	}
	for _, m := range futureMonths {
		if err := db.WithContext(ctx).Exec(fmt.Sprintf(`DROP TABLE IF EXISTS audit_logs_p%s`, m)).Error; err != nil {
			return before, "", fmt.Errorf("drop partition %s: %w", m, err)
		}
	}
	// ② 其余残留未来行：临时关 append-only 触发器再删（与测试里 `allowMutationForTest` 同一手法）。
	if err := db.WithContext(ctx).Exec(`ALTER TABLE audit_logs DISABLE TRIGGER USER`).Error; err == nil {
		_ = db.WithContext(ctx).Exec(`
			DELETE FROM audit_logs WHERE occurred_at >= date_trunc('month', now()) + interval '2 months'`).Error
		_ = db.WithContext(ctx).Exec(`ALTER TABLE audit_logs ENABLE TRIGGER USER`).Error
	}
	// ② drop 未来分区（>= 当月 +2 个月）；只删 `audit_logs_pYYYYMM` 形式的子分区。
	var names []string
	if err := db.WithContext(ctx).Raw(`
		SELECT c.relname FROM pg_class c
		JOIN pg_inherits i ON i.inhrelid = c.oid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = 'audit_logs'
		  AND c.relname ~ '^audit_logs_p[0-9]{6}$'
		  AND to_date(substring(c.relname from 14), 'YYYYMM') >= date_trunc('month', now()) + interval '2 months'
		ORDER BY 1`).Scan(&names).Error; err != nil {
		return before, "", fmt.Errorf("list future partitions: %w", err)
	}
	for _, n := range names {
		if err := db.WithContext(ctx).Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, n)).Error; err != nil {
			return before, "", fmt.Errorf("drop %s: %w", n, err)
		}
	}
	_ = db.Raw(`SELECT to_char(max(occurred_at), 'YYYY-MM-DD"T"HH24:MI:SSOF') AS max FROM audit_logs`).Scan(&a).Error
	if a.Max != nil {
		after = *a.Max
	}
	return before, after, nil
}

// auditIsolationEnabled 是否启用隔离（默认启用；`GUANJIN_AUDIT_NO_ISOLATION=1` 可关，便于排查）。
func auditIsolationEnabled() bool { return os.Getenv("GUANJIN_AUDIT_NO_ISOLATION") != "1" }
