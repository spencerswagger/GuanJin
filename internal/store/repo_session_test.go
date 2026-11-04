package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// `SessionRepo.Create` 的应用层校验（不需要数据库）：漏一项就报错，而不是等 DB CHECK 给一个 23514。
func TestSessionCreateValidation(t *testing.T) {
	repo := NewSessionRepo(nil)
	now := time.Now().UTC()
	good := func() *model.Session {
		return &model.Session{
			UserID: uuid.New(), ClientID: "c-1", ACR: "AAL1",
			Status: "active", ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(30 * time.Minute),
		}
	}
	cases := map[string]func(*model.Session){
		"nil 会话":             nil,
		"缺 user_id":          func(s *model.Session) { s.UserID = uuid.Nil },
		"缺 client_id":        func(s *model.Session) { s.ClientID = "" },
		"缺 acr":              func(s *model.Session) { s.ACR = "" },
		"缺绝对超时":              func(s *model.Session) { s.ExpiresAt = time.Time{} },
		"缺空闲超时":              func(s *model.Session) { s.IdleExpiresAt = time.Time{} },
		"空闲晚于绝对（DB 有 CHECK）": func(s *model.Session) { s.IdleExpiresAt = s.ExpiresAt.Add(time.Minute) },
	}
	for name, mut := range cases {
		s := good()
		if mut == nil {
			if err := repo.Create(context.Background(), nil); err == nil {
				t.Fatalf("%s 必须报错", name)
			}
			continue
		}
		mut(s)
		if err := repo.Create(context.Background(), s); err == nil {
			t.Fatalf("%s 必须报错", name)
		}
	}
}

// DryRun 下只验 SQL 形态（真实落库由 integration 覆盖）。
func TestSessionCreateDryRun(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewSessionRepo(db)
	now := time.Now().UTC()
	s := &model.Session{
		UserID: uuid.New(), ClientID: "c-1", ACR: "AAL1", Status: "active",
		ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(30 * time.Minute),
	}
	if err := repo.Create(context.Background(), s); err != nil {
		t.Fatalf("DryRun 下 Create 不应报错：%v", err)
	}
	if sql := capture.last(t); sql == "" {
		t.Fatal("应发出一条 INSERT")
	}
}

// `SigningKeyRepo` 的两个方法：空仓库/缺列拒绝；DryRun 验 SQL 形态。
func TestSigningKeyRepoGuards(t *testing.T) {
	repo := NewSigningKeyRepo(nil)
	if _, err := repo.ListUsable(context.Background()); err == nil {
		t.Fatal("nil db 必须报错")
	}
	if _, err := repo.InsertIfAbsent(context.Background(), SigningKeyRow{}); err == nil {
		t.Fatal("缺 kid/purpose/algorithm 必须报错")
	}

	db, capture := dryRunDB(t)
	ok := NewSigningKeyRepo(db)
	// `Scan` 在 DryRun 下可能返回 nil（不真正执行），所以这里只验语句形态。
	if _, err := ok.ListUsable(context.Background()); err != nil && !errIsDryRun(err) {
		t.Fatalf("DryRun 下不该报其它错：%v", err)
	}
	if sql := capture.last(t); !strings.Contains(sql, "FROM signing_keys") {
		t.Fatalf("应查 signing_keys，实际 %q", sql)
	}
	if _, err := ok.InsertIfAbsent(context.Background(), SigningKeyRow{
		Kid: "k", Purpose: "oidc", Algorithm: "SM3withSM2", PublicKey: "p", PrivateEnc: []byte{1},
	}); err != nil && !errIsDryRun(err) {
		t.Fatalf("DryRun 下不该报其它错：%v", err)
	}
}

// `LoadTokenPolicy`：空 client_id 直接拒；DryRun 验首条语句查 clients。
func TestLoadTokenPolicySQLShape(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewClientPolicyRepo(db)
	if _, err := repo.LoadTokenPolicy(context.Background(), " "); err == nil {
		t.Fatal("空 client_id 必须报错")
	}
	if _, err := repo.LoadTokenPolicy(context.Background(), "c-1"); err != nil && !errIsDryRun(err) {
		t.Fatalf("DryRun 下不该报其它错：%v", err)
	}
	if sql := capture.last(t); !strings.Contains(sql, "FROM clients") && !strings.Contains(sql, "FROM login_policies") {
		t.Fatalf("应查 clients/login_policies，实际 %q", sql)
	}
}

func errIsDryRun(err error) bool { return err != nil && err == gorm.ErrDryRunModeUnsupported }

// `RedirectURIs`：空 client_id 拒绝；DryRun 下只验语句形态（逐字比对在授权端点用例里证）。
func TestRedirectURIsGuards(t *testing.T) {
	if _, err := NewClientPolicyRepo(nil).RedirectURIs(context.Background(), " "); err == nil {
		t.Fatal("空 client_id 必须报错")
	}
	db, capture := dryRunDB(t)
	if _, err := NewClientPolicyRepo(db).RedirectURIs(context.Background(), "c-1"); err != nil && !errIsDryRun(err) {
		t.Fatalf("DryRun 下不该报其它错：%v", err)
	}
	if sql := capture.last(t); !strings.Contains(sql, "redirect_uris") {
		t.Fatalf("应查 redirect_uris，实际 %q", sql)
	}
}

// `parseRedirectList`：纯函数，覆盖空串/单值/多值/空白。
func TestParseRedirectList(t *testing.T) {
	if got := parseRedirectList("  "); got != nil {
		t.Fatalf("空串应回 nil，实际 %v", got)
	}
	got := parseRedirectList("https://a/cb\n https://b/cb \n\n")
	if len(got) != 2 || got[0] != "https://a/cb" || got[1] != "https://b/cb" {
		t.Fatalf("拆分不符：%v", got)
	}
}
