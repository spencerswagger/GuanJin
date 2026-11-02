package store

import (
	"context"
	"errors"
	"testing"
)

// 真库用例（**需要 GUANJIN_TEST_DSN**）：收口 PR #4 的 P2-3 ——
// `sessions_acr_check` 违反必须是 **400 `INVALID_PARAMETER`**，不能是 500。
//
// 为什么这一条归 Edge：约束名由迁移给出（S2），但"23514 → 400 而不是 500"发生在
// `pgxerr` 的双键映射（S3）。#4 里 `internal/store` 只有 doc.go，证不到这一环。
func TestDBSessionsAcrCheckMapsTo400(t *testing.T) {
	db := openTestDB(t)
	if !db.Migrator().HasTable("sessions") {
		t.Fatalf("测试库缺少 sessions 表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN")
	}
	f := newFixture(t, db)
	ctx := context.Background()

	insert := func(acr string) error {
		id := mustV7(t)
		t.Cleanup(func() {
			_ = db.Exec(`DELETE FROM sessions WHERE id = ?`, id).Error
		})
		// 必须显式过 MapError：GORM 的 Exec 原样返回 *pgconn.PgError，
		// 映射是仓储层每个写操作的义务（技术栈 §5.3「不得把原始 DB 错误抛到 HTTP 层」）。
		return MapError(db.Exec(`INSERT INTO sessions (id, user_id, client_id, acr, expires_at, idle_expires_at)
			VALUES (?, ?, ?, ?, now() + interval '1 hour', now() + interval '30 min')`,
			id, f.userID, f.clientID, acr).Error)
	}

	// 1) 越界取值 → 23514，必须映射成 400（D12：acr 取值域；不得静默降级、不得当 500）。
	err := insert("AAL9")
	if err == nil {
		t.Fatal("acr='AAL9' 必须被 CHECK 拒绝")
	}
	var se *Error
	if !errors.As(err, &se) {
		t.Fatalf("err = %T (%v), want *store.Error", err, err)
	}
	if se.SQLState != sqlStateCheckViolation {
		t.Fatalf("sqlstate = %q, want 23514", se.SQLState)
	}
	if se.Constraint != "sessions_acr_check" {
		t.Fatalf("constraint = %q, want sessions_acr_check（Nova 实测名一致才说明没被改名）", se.Constraint)
	}
	if se.Code != codeInvalidParameter || se.Status != 400 {
		t.Fatalf("code/status = %s/%d, want INVALID_PARAMETER/400（P2-3 要的就是这一行）",
			se.Code, se.Status)
	}

	// 2) 合法值必须正常插入（证明上一条的失败是 CHECK 造成的，不是别的原因）。
	if err := insert("AAL2"); err != nil {
		t.Fatalf("acr='AAL2' 必须能插入: %v", err)
	}

	// 3) AAL3 是预留值（D12）：DDL 允许落库，但 V1 业务层不得产出 —— 这条只在
	//    store 层确认"DB 不拦"，业务白名单断言属 Core（AC-FLOW-13）。
	if err := insert("AAL3"); err != nil {
		t.Fatalf("acr='AAL3' 在 DDL 上仍合法（预留），store 层不应拦住: %v", err)
	}

	_ = ctx
}
