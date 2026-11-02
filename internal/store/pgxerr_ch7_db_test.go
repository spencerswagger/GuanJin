package store

import (
	"context"
	"errors"
	"testing"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 真库用例：CH-7 ③④ 两行（§5.4 **本来就有**专用码，不许退化成 INVALID_PARAMETER）。
//
// 为什么必须真库跑：单测只能构造 `*pgconn.PgError`，证明的是"表里怎么写"；
// 真库才能证明"约束名真的是这个、错误真的是 23505、映射真的接在这条路径上"。
// 两条都用 `MapError(...)` 显式映射 —— GORM 的 Exec 原样返回 pgconn 错误（不自动映射），
// 这正是仓储层每个写操作的义务（技术栈 §5.3）。
func TestDBCH7UniqueRowsMapTo409(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)

	// 清理顺序：先删引用方再删被引用方（FK RESTRICT）。
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM policy_snapshots WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM role_bindings WHERE client_id = ?`, f.clientID).Error
	})

	t.Run("CH-7③ role_bindings_uniq → BINDING_ALREADY_EXISTS 409", func(t *testing.T) {
		insert := func(role string) error {
			id := mustV7(t)
			return MapError(db.Exec(
				`INSERT INTO role_bindings (id, user_id, client_id, role) VALUES (?, ?, ?, ?)`,
				id, f.userID, f.clientID, role).Error)
		}

		if err := insert("admin"); err != nil {
			t.Fatalf("首次绑定必须成功: %v", err)
		}
		err := insert("admin") // 同 (user_id, client_id, role) 再来一次
		if err == nil {
			t.Fatal("重复绑定必须被 role_bindings_uniq 拒绝")
		}
		assertMapped(t, err, sqlStateUniqueViolation, "role_bindings_uniq",
			codeBindingAlreadyExists, 409)

		// 反方向：换个角色是可插的（证明拒绝的原因是唯一约束，不是别的）。
		if err := insert("viewer"); err != nil {
			t.Fatalf("不同角色必须能插入: %v", err)
		}
	})

	t.Run("CH-7④ policy_snapshots_uniq → POLICY_SNAPSHOT_CONFLICT 409", func(t *testing.T) {
		insert := func(version int) error {
			id := mustV7(t)
			return MapError(db.Exec(
				`INSERT INTO policy_snapshots (id, client_id, version, payload, payload_hash, integrity_tag)
				 VALUES (?, ?, ?, '{}'::jsonb, ?, ?)`,
				id, f.clientID, version, []byte{0x01}, []byte{0x02}).Error)
		}

		if err := insert(1); err != nil {
			t.Fatalf("首次快照必须成功: %v", err)
		}
		err := insert(1) // 同 (client_id, version) 再来一次
		if err == nil {
			t.Fatal("重复版本必须被 policy_snapshots_uniq 拒绝")
		}
		assertMapped(t, err, sqlStateUniqueViolation, "policy_snapshots_uniq",
			codePolicySnapshotConflict, 409)

		// 反方向：版本递增是可插的（`since_version` 单调语义的前提）。
		if err := insert(2); err != nil {
			t.Fatalf("更高版本必须能插入: %v", err)
		}
	})
}

// 真库用例：flows 的活跃流唯一冲突 → COOLDOWN_ACTIVE 409（CH-7⑤）。
// 已由 TestDBFlowActiveUniqueMapsTo409 覆盖"先查后建 + 竞态兜底"，
// 这里只把"码与状态在真库路径上"再钉一次（因为它是最容易被误读的语义）。
func TestDBCH7FlowUniqueIsCooldown(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	repo := NewFlowRepo(db)
	ctx := context.Background()

	first := f.newFlow(t)
	first.UserID = &f.userID
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("create first flow: %v", err)
	}
	second := f.newFlow(t)
	second.UserID = &f.userID

	assertMapped(t, repo.Create(ctx, second), sqlStateUniqueViolation,
		"flows_one_active_per_user_client_uniq", codeCooldownActive, 409)
}

func assertMapped(t *testing.T, err error, wantState, wantConstraint string,
	wantCode httpx.Code, wantStatus int) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a mapped db error, got nil")
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *store.Error", err, err)
	}
	if e.SQLState != wantState {
		t.Errorf("sqlstate = %q, want %q", e.SQLState, wantState)
	}
	if e.Constraint != wantConstraint {
		t.Errorf("constraint = %q, want %q", e.Constraint, wantConstraint)
	}
	if e.Code != wantCode {
		t.Errorf("code = %q, want %q", e.Code, wantCode)
	}
	if e.Status != wantStatus {
		t.Errorf("status = %d, want %d", e.Status, wantStatus)
	}
	if e.Reason == "" {
		t.Error("映射必须带 reason")
	}
}
