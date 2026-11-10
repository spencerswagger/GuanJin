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
	"time"

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
		// `users` 被 `TestAppendHandlesOptionalColumns`（带 actor 的落库形态）用到；
		// 隔离 schema 里给一个最小表即可（生产侧还有 FK/唯一索引，这里不影响被测逻辑）。
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.users (
			id       uuid PRIMARY KEY,
			username text NOT NULL
		)`, isolatedSchema),
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

// purgeSharedFutureRows 幂等清理**共享库里历史运行写进去的未来分区**。
//
// ## 三条安全护栏（删数据前必须全部满足，缺一不可）
//  1. **显式开关**：只有设置 `GUANJIN_ALLOW_TEST_CLEANUP=1` 才执行，否则 skip 并打印原因；
//  2. **库名必须像测试库**：`SELECT current_database()` 必须匹配 `_test$`，否则 skip；
//  3. **表名全部 schema 限定**（`public.audit_logs` / `public.audit_logs_pYYYYMM`），
//     不依赖 `search_path`——避免"以为在删测试库、实际删了别的 schema"。
//
// ## 只做 DROP 分区，**不做 DELETE**
// `DROP TABLE <partition>` 本身就带走该分区的行；为了 `DELETE` 而临时
// `ALTER TABLE … DISABLE TRIGGER USER` 的代价是：PG 的触发器开关是**持久化表属性、跨连接可见**，
// 一旦中途失败就会让 append-only 保护长期失效。少一条"能删真数据"的路径更安全。
//
// 判据：`occurred_at >= 当月 + 2 个月` 视为测试写进去的未来数据（生产不会预写未来）。
// 返回清理前后的 `max(occurred_at)`，供 PR 与日志核对（要求：清完回到当月）。
func purgeSharedFutureRows(db *gorm.DB) (before, after string, err error) {
	ctx := context.Background()

	// 护栏 ①：显式开关。
	if os.Getenv("GUANJIN_ALLOW_TEST_CLEANUP") != "1" {
		fmt.Fprintln(os.Stderr, "[audit.TestMain] skip future-partition cleanup: 未设置 GUANJIN_ALLOW_TEST_CLEANUP=1")
		return "", "", nil
	}

	// 护栏 ②：库名必须是测试库（`..._test$`）。
	var dbName string
	if err := db.WithContext(ctx).Raw(`SELECT current_database()`).Scan(&dbName).Error; err != nil {
		return "", "", fmt.Errorf("read current_database: %w", err)
	}
	if !strings.HasSuffix(dbName, "_test") {
		fmt.Fprintf(os.Stderr, "[audit.TestMain] skip future-partition cleanup: 库名 %q 不以 _test 结尾\n", dbName)
		return "", "", nil
	}

	var b, a struct{ Max *string }
	_ = db.Raw(`SELECT to_char(max(occurred_at), 'YYYY-MM-DD"T"HH24:MI:SSOF') AS max FROM public.audit_logs`).Scan(&b).Error
	if b.Max != nil {
		before = *b.Max
	}

	// 护栏 ③：分区名**schema 限定**，逐个月 DROP（幂等：`IF EXISTS`）。
	now := time.Now().UTC()
	for i := 2; i <= 120; i++ {
		m := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, i, 0)
		name := fmt.Sprintf("public.audit_logs_p%s", m.Format("200601"))
		if err := db.WithContext(ctx).Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, name)).Error; err != nil {
			return before, "", fmt.Errorf("drop %s: %w", name, err)
		}
	}

	_ = db.Raw(`SELECT to_char(max(occurred_at), 'YYYY-MM-DD"T"HH24:MI:SSOF') AS max FROM public.audit_logs`).Scan(&a).Error
	if a.Max != nil {
		after = *a.Max
	}
	return before, after, nil
}

// auditIsolationEnabled 是否启用隔离（默认启用；`GUANJIN_AUDIT_NO_ISOLATION=1` 可关，便于排查）。
func auditIsolationEnabled() bool { return os.Getenv("GUANJIN_AUDIT_NO_ISOLATION") != "1" }
