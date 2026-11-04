package store

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 本文件只测**不需要数据库**的部分：游标编解码、入参校验、空集合行为。
// SQL 形状与 DB 约束（CHECK/部分索引/CTE 挤最旧）在 `repo_sessions_db_test.go`（integration）。

func TestSessionCursorRoundTrip(t *testing.T) {
	id := uuid.MustParse("018f2b0c-1111-7abc-8def-000000000001")
	at := time.Date(2026, 11, 4, 12, 0, 0, 123456789, time.UTC)

	cur := encodeSessionCursor(at, id)
	if cur == "" {
		t.Fatal("cursor must not be empty")
	}
	if strings.Contains(cur, "2026") || strings.Contains(cur, "018f2b0c") {
		// 不透明是 D3-A′③ 的硬要求：明文时间戳/主键会让调用方开始"自己拼游标"。
		t.Fatalf("cursor leaks plaintext: %q", cur)
	}
	decoded, err := decodeSessionCursor(cur)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded == nil {
		t.Fatal("decoded cursor is nil")
	}
	if !decoded.IssuedAt.Equal(at) {
		t.Fatalf("issued_at = %s, want %s", decoded.IssuedAt, at)
	}
	if decoded.ID != id {
		t.Fatalf("id = %s, want %s", decoded.ID, id)
	}
}

func TestDecodeSessionCursorEmptyIsFirstPage(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		cur, err := decodeSessionCursor(raw)
		if err != nil || cur != nil {
			t.Fatalf("%q: want (nil,nil), got (%v,%v)", raw, cur, err)
		}
	}
}

func TestDecodeSessionCursorRejectsGarbage(t *testing.T) {
	cases := []string{
		"!!!not-base64!!!",
		encodeRaw("no-pipe-here"),
		encodeRaw("notanumber|018f2b0c-1111-7abc-8def-000000000001"),
		encodeRaw("123|not-a-uuid"),
	}
	for _, raw := range cases {
		if _, err := decodeSessionCursor(raw); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("%q: want ErrInvalidCursor, got %v", raw, err)
		}
	}
}

// encodeRaw 造一个"能解开 base64 但内容非法"的游标，用来验形状校验。
func encodeRaw(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestSessionListLimitBounds(t *testing.T) {
	repo := &SessionOpsRepo{}
	if _, err := repo.ListByUser(context.Background(), uuid.New(), 0); err == nil {
		t.Fatal("limit 0 must be rejected")
	}
	if _, err := repo.ListByUser(context.Background(), uuid.New(), MaxSessionPageLimit+1); err == nil {
		t.Fatal("limit > max must be rejected")
	}
	if _, err := repo.ListByUser(context.Background(), uuid.Nil, 10); err == nil {
		t.Fatal("nil user must be rejected")
	}
	if _, err := repo.List(context.Background(), SessionQuery{Limit: MaxSessionPageLimit + 1}); err == nil {
		t.Fatal("List must reject limit > max")
	}
	if _, err := repo.List(context.Background(), SessionQuery{Limit: 10, Cursor: "!!!"}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("want ErrInvalidCursor, got %v", err)
	}
}

func TestRevokeRequiresReason(t *testing.T) {
	repo := &SessionOpsRepo{}
	now := time.Now()
	if _, err := repo.RevokeByID(context.Background(), uuid.New(), "", now); err == nil {
		t.Fatal("empty reason must be rejected")
	}
	if _, err := repo.RevokeByID(context.Background(), uuid.Nil, "admin", now); err == nil {
		t.Fatal("nil id must be rejected")
	}
	if _, err := repo.RevokeAllByUser(context.Background(), uuid.New(), "", now); err == nil {
		t.Fatal("empty reason must be rejected (bulk)")
	}
	if _, err := repo.RevokeOthers(context.Background(), uuid.Nil, uuid.New(), "admin", now); err == nil {
		t.Fatal("nil user must be rejected (others)")
	}
	if _, err := repo.EvictOldestActive(context.Background(), uuid.New(), "", "admin", now); err == nil {
		t.Fatal("empty client must be rejected (evict)")
	}
}

func TestClientNamesEmptyInput(t *testing.T) {
	repo := &SessionOpsRepo{}
	names, err := repo.ClientNames(context.Background(), nil)
	if err != nil {
		t.Fatalf("ClientNames: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("want empty map, got %v", names)
	}
}

func TestUserExistsNilID(t *testing.T) {
	repo := &SessionOpsRepo{}
	ok, err := repo.UserExists(context.Background(), uuid.Nil)
	if err != nil || ok {
		t.Fatalf("nil id: ok=%v err=%v", ok, err)
	}
}

func TestRolesLookupRequiresIDs(t *testing.T) {
	repo := &RoleBindingRepo{}
	if _, err := repo.RolesForUserClient(context.Background(), uuid.Nil, "web"); err == nil {
		t.Fatal("nil user must be rejected")
	}
	if _, err := repo.RolesForUserClient(context.Background(), uuid.New(), ""); err == nil {
		t.Fatal("empty client must be rejected")
	}
}

// 下面几条是**守卫与分支**覆盖：不建库，只确保每个错误分支真的被走到
// （覆盖率门禁 store ≥75 是 CI 的硬闸；真库行为另见 `repo_session_ops_db_test.go`）。
func TestSessionOpsGuards(t *testing.T) {
	repo := &SessionOpsRepo{}
	ctx := context.Background()
	now := time.Now()

	if _, err := repo.GetByID(ctx, uuid.Nil); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("GetByID(nil) = %v", err)
	}
	if _, err := repo.CountActiveByUserClient(ctx, uuid.Nil, "web"); err == nil {
		t.Fatal("count with nil user must fail")
	}
	if _, err := repo.CountActiveByUserClient(ctx, uuid.New(), " "); err == nil {
		t.Fatal("count with empty client must fail")
	}
	if _, err := repo.EvictOldestActive(ctx, uuid.New(), "web", "", now); err == nil {
		t.Fatal("evict without reason must fail")
	}
	if _, err := repo.EvictOldestActive(ctx, uuid.Nil, "web", "concurrent_limit", now); err == nil {
		t.Fatal("evict with nil user must fail")
	}
	if _, err := repo.TouchLastSeen(ctx, uuid.Nil, now, now); err == nil {
		t.Fatal("touch with nil id must fail")
	}
	if _, err := repo.ActiveIDsByUser(ctx, uuid.Nil); err == nil {
		t.Fatal("active ids with nil user must fail")
	}
	// 撤销的真实查询分支由 DryRun 句柄覆盖（nil db 只能走守卫分支：空 id / 空原因）。
	if _, err := repo.RevokeByID(ctx, uuid.New(), " ", now); err == nil {
		t.Fatal("revoke with blank reason must fail")
	}
	if _, err := repo.RevokeAllByUser(ctx, uuid.New(), " ", now); err == nil {
		t.Fatal("bulk revoke with blank reason must fail")
	}
	if _, err := repo.RevokeOthers(ctx, uuid.New(), uuid.New(), " ", now); err == nil {
		t.Fatal("revoke others with blank reason must fail")
	}
}

func TestSessionOpsWithTxBindsHandle(t *testing.T) {
	db, _ := dryRunDB(t)
	repo := NewSessionOpsRepo(db)
	if repo.WithTx(db) == nil {
		t.Fatal("WithTx must return a repo bound to the given handle")
	}
}

func TestSessionOpsDryRunFilterBranches(t *testing.T) {
	db, _ := dryRunDB(t)
	repo := NewSessionOpsRepo(db)
	ctx := context.Background()
	userID := uuid.New()
	clientID := "web"
	yes, no := true, false

	cases := []SessionQuery{
		{Limit: 5, UserID: &userID},
		{Limit: 5, ClientID: &clientID},
		{Limit: 5, Active: &yes},
		{Limit: 5, Active: &no},
		{Limit: 5, Cursor: encodeSessionCursor(time.Now().UTC(), uuid.New())},
	}
	for i, q := range cases {
		if _, err := repo.List(ctx, q); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	if _, err := repo.ExpireStale(ctx, time.Now()); err != nil {
		allowDryRunUnsupported(t, err)
	}
}
