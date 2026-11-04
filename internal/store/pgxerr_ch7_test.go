package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// CH-7（`decisions.md` D4-D₂）八行映射表的**金本位**：逐行钉死，改一行必须同时改文档。
//
// 为什么单独一条测试而不是只靠 TestMapErrorDoubleKey：那张表是"行为抽样"，
// 这条是"账目对账"——它保证代码里的行数、约束名、码、状态与 CH-7 完全一致，
// 不会出现"文档 8 行、代码 9 行"这种评审时看不出的漂移。
func TestCH7EightRowMapping(t *testing.T) {
	rows := []struct {
		row        string
		constraint string
		wantCode   httpx.Code
		wantStatus int
	}{
		{"CH-7①", "identifiers_type_value_uniq", httpx.CodeIdentifierAlreadyBound, 409},
		{"CH-7②", "users_username_lower_uidx", httpx.CodeIdentifierAlreadyBound, 409},
		{"CH-7③", "role_bindings_uniq", httpx.CodeBindingAlreadyExists, 409},
		{"CH-7④", "policy_snapshots_uniq", httpx.CodePolicySnapshotConflict, 409},
		{"CH-7⑤", "flows_one_active_per_user_client_uniq", httpx.CodeCooldownActive, 409},
		{"CH-7⑥", "clients_pkey", httpx.CodeInvalidClient, 400},
		{"CH-7⑦", "signing_keys_kid_uniq", httpx.CodeInvalidParam, 400},
		{"CH-7⑧a", "login_policies_tier_default_uidx", httpx.CodeInvalidParam, 400},
		{"CH-7⑧b", "login_policies_client_override_uidx", httpx.CodeInvalidParam, 400},
	}

	for _, row := range rows {
		t.Run(row.row, func(t *testing.T) {
			mapped := MapError(pgErr(sqlStateUniqueViolation, row.constraint))
			var e *Error
			if !errors.As(mapped, &e) {
				t.Fatalf("%s: want *store.Error, got %T", row.row, mapped)
			}
			if e.Code != row.wantCode || e.Status != row.wantStatus {
				t.Fatalf("%s %s: got %s/%d, want %s/%d",
					row.row, row.constraint, e.Code, e.Status, row.wantCode, row.wantStatus)
			}
			if e.Reason == "" {
				t.Errorf("%s: 映射必须带 reason（评审要能直接读到「为什么」）", row.row)
			}
			// 码表的唯一来源是 httpx（§5.4 冻结值），状态也必须由它推导。
			if !httpx.IsKnownCode(e.Code) {
				t.Errorf("%s: %q 不在 §5.4 冻结码表", row.row, e.Code)
			}
			if e.Status != httpx.HTTPStatus(e.Code) {
				t.Errorf("%s: status %d 与 HTTPStatus(%s) 不一致", row.row, e.Status, e.Code)
			}
		})
	}

	// 映射表里不许有 CH-7 之外的新增约束（新增必须先进文档再进代码）。
	// 注意 CH-7 是「八行表」但落成 9 个约束名：⑧ 一行覆盖 tier_default / client_override 两条索引。
	if got, want := len(uniqueRules), len(rows); got != want {
		t.Fatalf("uniqueRules 有 %d 条约束，CH-7 要求 %d 条（八行，⑧ 含两条索引）—— "+
			"新增约束请先改 decisions.md 的 D4-D₂", got, want)
	}
}

// CH-7 明确「不映射」的那两条必须保持缺陷（500），不许为了"看起来完整"硬塞一个码。
func TestCH7UnmappedAreStillDefects(t *testing.T) {
	for _, constraint := range []string{
		"identifiers_one_primary_per_user",
		"user_visited_clients_uniq",
	} {
		mapped := MapError(pgErr(sqlStateUniqueViolation, constraint))
		var e *Error
		if !errors.As(mapped, &e) {
			t.Fatalf("%s: want *store.Error", constraint)
		}
		if e.Mapped() {
			t.Fatalf("%s 必须不映射（D3-B I6 无对外码 / upsert 前提），got %s", constraint, e.Code)
		}
		if unmappedUniqueConstraints[constraint] == "" {
			t.Fatalf("%s 必须在 unmappedUniqueConstraints 里写明原因", constraint)
		}
	}
}

// CH-7⑤ 的前置条件：正常路径「先查后建」，所以仓储必须提供这条查询。
// 这里验 SQL 形态（真的查库由 repo_flows_db_test.go 覆盖）。
func TestFindActiveByUserClientSQL(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewFlowRepo(db)
	userID := uuid.Must(uuid.NewV7())

	// DryRun 不真正执行，所以这里只验 SQL；"没有活跃流 → ErrFlowNotFound" 与
	// "已有活跃流 → 拿到同一条" 由 repo_flows_db_test.go 的
	// TestDBFlowActiveUniqueMapsTo409（真库）覆盖。
	if _, err := repo.FindActiveByUserClient(context.Background(), userID, "client-a"); err != nil {
		t.Fatalf("DryRun lookup must not fail: %v", err)
	}
	sql := capture.last(t)
	for _, want := range []string{"user_id = ", "client_id = ", "status = 'active'"} {
		if !strings.Contains(sql, want) {
			t.Errorf("先查后建的查询必须带 %q，got: %s", want, sql)
		}
	}
	if !strings.Contains(strings.ToUpper(sql), "ORDER BY") {
		t.Errorf("必须按创建时间取最新一条（多副本下可能残留历史行），got: %s", sql)
	}

	// 匿名流不适用（user_id 为空直接当"没有"），避免悄悄走全表扫描。
	if _, err := repo.FindActiveByUserClient(context.Background(), uuid.Nil, "client-a"); !errors.Is(err, ErrFlowNotFound) {
		t.Fatalf("nil user err = %v, want ErrFlowNotFound", err)
	}
	if _, err := repo.FindActiveByUserClient(context.Background(), userID, ""); !errors.Is(err, ErrFlowNotFound) {
		t.Fatalf("empty client err = %v, want ErrFlowNotFound", err)
	}
}
