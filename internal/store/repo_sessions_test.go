package store

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/store/model"
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

func TestSessionCreateValidations(t *testing.T) {
	repo := &SessionRepo{} // 校验都在落库之前，不需要 db
	now := time.Now().UTC()
	base := func() *model.Session {
		return &model.Session{
			UserID:        uuid.New(),
			ClientID:      "web",
			ACR:           "AAL1",
			Status:        model.SessionStatusActive,
			IssuedAt:      now,
			ExpiresAt:     now.Add(time.Hour),
			IdleExpiresAt: now.Add(time.Minute),
		}
	}

	cases := []struct {
		name string
		mod  func(*model.Session) *model.Session
	}{
		{"nil", func(*model.Session) *model.Session { return nil }},
		{"no user", func(s *model.Session) *model.Session { s.UserID = uuid.Nil; return s }},
		{"no client", func(s *model.Session) *model.Session { s.ClientID = "  "; return s }},
		{"no acr", func(s *model.Session) *model.Session { s.ACR = ""; return s }},
		{"no status", func(s *model.Session) *model.Session { s.Status = ""; return s }},
		{"no issued", func(s *model.Session) *model.Session { s.IssuedAt = time.Time{}; return s }},
		{"no expires", func(s *model.Session) *model.Session { s.ExpiresAt = time.Time{}; return s }},
		{"no idle", func(s *model.Session) *model.Session { s.IdleExpiresAt = time.Time{}; return s }},
		{"idle after absolute", func(s *model.Session) *model.Session {
			s.IdleExpiresAt = s.ExpiresAt.Add(time.Second)
			return s
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := repo.Create(context.Background(), tc.mod(base())); err == nil {
				t.Fatal("want validation error")
			}
		})
	}
}

func TestSessionListLimitBounds(t *testing.T) {
	repo := &SessionRepo{}
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
	repo := &SessionRepo{}
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
	repo := &SessionRepo{}
	names, err := repo.ClientNames(context.Background(), nil)
	if err != nil {
		t.Fatalf("ClientNames: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("want empty map, got %v", names)
	}
}

func TestUserExistsNilID(t *testing.T) {
	repo := &SessionRepo{}
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
