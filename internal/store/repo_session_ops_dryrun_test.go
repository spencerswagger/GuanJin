package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 本文件用 **DryRun 句柄**（永不出网）覆盖 `repo_sessions.go` 的每一条语句路径：
// 单测不建库，但要保证"代码被执行到了"（否则覆盖率门禁会红，且真库用例只在 CI 跑）。
// SQL 形态的断言放在这里；行级行为在 `repo_sessions_db_test.go`（integration）。

func sessionFixtureRow(t *testing.T) *SessionRecord {
	t.Helper()
	now := time.Now().UTC()
	return &SessionRecord{
		UserID:        uuid.New(),
		ClientID:      "web",
		ACR:           "AAL1",
		Status:        SessionStatusActive,
		IssuedAt:      now,
		ExpiresAt:     now.Add(time.Hour),
		IdleExpiresAt: now.Add(30 * time.Minute),
	}
}

func TestSessionRepoDryRunPaths(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewSessionOpsRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()

	// GetByID
	if _, err := repo.GetByID(ctx, uuid.New()); err != nil {
		t.Fatalf("GetByID (dry run): %v", err)
	}

	// ListByUser / List（两种 cursor 分支 + active 过滤分支）
	if _, err := repo.ListByUser(ctx, uuid.New(), 10); err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if _, err := repo.List(ctx, SessionQuery{Limit: 10}); err != nil {
		t.Fatalf("List plain: %v", err)
	}
	userID := uuid.New()
	clientID := "web"
	active := true
	cursor := encodeSessionCursor(now, uuid.New())
	page, err := repo.List(ctx, SessionQuery{
		UserID: &userID, ClientID: &clientID, Active: &active, Limit: 10, Cursor: cursor,
	})
	if err != nil {
		t.Fatalf("List filtered: %v", err)
	}
	if page == nil || page.Items == nil {
		t.Fatal("List must return an empty (non-nil) slice")
	}
	// 行值比较是翻页正确性的关键：`(issued_at, id) < (?, ?)`。
	if sql := capture.last(t); !strings.Contains(sql, "(issued_at, id) <") {
		t.Fatalf("cursor predicate missing: %s", sql)
	}

	// Revoke* 三条 + 计数 + 挤最旧 + 续期 + 惰性过期
	if _, err := repo.RevokeByID(ctx, uuid.New(), "admin", now); err != nil {
		t.Fatalf("RevokeByID: %v", err)
	}
	if sql := capture.last(t); !strings.Contains(sql, "status") || !strings.Contains(sql, "active") {
		t.Fatalf("revoke must be a conditional update: %s", sql)
	}
	if _, err := repo.RevokeAllByUser(ctx, uuid.New(), "admin", now); err != nil {
		t.Fatalf("RevokeAllByUser: %v", err)
	}
	if _, err := repo.RevokeOthers(ctx, uuid.New(), uuid.New(), "password_change", now); err != nil {
		t.Fatalf("RevokeOthers: %v", err)
	}
	if _, err := repo.CountActiveByUserClient(ctx, uuid.New(), "web"); err != nil {
		t.Fatalf("CountActiveByUserClient: %v", err)
	}
	// Raw + Scan 在 DryRun 下会直接返回 "dry run mode unsupported"（GORM 的限制，不是本仓储的行为）；
	// 这里只要求"代码走到那条分支"，CTE 的 SQL 形态由下面的 integration 用例在真库上验。
	if _, err := repo.EvictOldestActive(ctx, uuid.New(), "web", "concurrent_limit", now); err != nil {
		allowDryRunUnsupported(t, err)
	}
	if _, err := repo.TouchLastSeen(ctx, uuid.New(), now, now.Add(time.Minute)); err != nil {
		t.Fatalf("TouchLastSeen: %v", err)
	}
	if _, err := repo.ExpireStale(ctx, now); err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if _, err := repo.ActiveIDsByUser(ctx, uuid.New()); err != nil {
		allowDryRunUnsupported(t, err)
	}

	// ClientNames / UserExists / Roles
	if _, err := repo.ClientNames(ctx, []string{"web"}); err != nil {
		allowDryRunUnsupported(t, err)
	}
	if _, err := repo.UserExists(ctx, uuid.New()); err != nil {
		allowDryRunUnsupported(t, err)
	}
	roles := NewRoleBindingRepo(db)
	if _, err := roles.RolesForUserClient(ctx, uuid.New(), "web"); err != nil {
		allowDryRunUnsupported(t, err)
	}
	if _, err := repo.LoadSessionPolicy(ctx, "web"); err != nil {
		allowDryRunUnsupported(t, err)
	}
}

// allowDryRunUnsupported 接受 GORM DryRun 对 `Raw(...).Scan(...)` 的显式拒绝。
//
// 只接受这一种错误：把"我们自己写错了"当成 DryRun 限制吞掉，会让本文件变成假绿。
func allowDryRunUnsupported(t *testing.T, err error) {
	t.Helper()
	if !strings.Contains(err.Error(), "dry run mode unsupported") {
		t.Fatalf("unexpected error under dry run: %v", err)
	}
}

func TestSessionRepoEmptyClientNamesDoesNotQuery(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewSessionOpsRepo(db)
	before := len(capture.queries)
	if _, err := repo.ClientNames(context.Background(), []string{}); err != nil {
		t.Fatalf("ClientNames: %v", err)
	}
	if len(capture.queries) != before {
		t.Fatal("empty input must not hit the database")
	}
}

func TestSessionListWithoutCursorHasNoKeysetPredicate(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewSessionOpsRepo(db)
	if _, err := repo.List(context.Background(), SessionQuery{Limit: 5}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if sql := capture.last(t); strings.Contains(sql, "(issued_at, id) <") {
		t.Fatalf("first page must not carry a keyset predicate: %s", sql)
	}
}

func TestSessionListActiveFalseDoesNotFilterStatus(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewSessionOpsRepo(db)
	no := false
	if _, err := repo.List(context.Background(), SessionQuery{Limit: 5, Active: &no}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if sql := capture.last(t); strings.Contains(sql, "status =") {
		t.Fatalf("active=false must not filter by status: %s", sql)
	}
}
