//go:build integration

package store

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/testdb"
)

// 本文件是 `AC-FLOW-09` 读取面的**真库证据**：
//
//	max tier = user_visited_clients ⨝ clients(status='active') 的最大档位
//	门槛     = login_policies(scope='tier_default', tier=max tier).min_acr
//
// 四条边界各有一条用例：active 参与、**停用不参与**、无记录 → found=false（调用方回退 L2）、
// 档位默认门槛按档位取（L3 → AAL2）。

func openEffectiveACRRepo(t *testing.T) (*EffectiveACRRepo, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		t.Fatal("必须设置 GUANJIN_TEST_DSN（真 PostgreSQL）")
	}
	ctx := context.Background()
	db, err := Open(ctx, Options{DSN: dsn})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := Ping(ctx, db); err != nil {
		t.Fatalf("ping test db: %v", err)
	}
	// 本文件只读 + 插入自建夹具；删除走 testdb 约定（#74）。
	testdb.RequireTestDB(t, db)
	t.Cleanup(func() { _ = Close(db) })
	repo, err := NewEffectiveACRRepo(db)
	if err != nil {
		t.Fatalf("NewEffectiveACRRepo: %v", err)
	}
	return repo, db
}

// acrFixture 建一个隔离用户 + N 个业务系统（各自档位/状态）+ 访问记录。
type acrFixture struct {
	userID  uuid.UUID
	clients []string
}

func newACRFixture(t *testing.T, db *gorm.DB) *acrFixture {
	t.Helper()
	suffix := uuid.NewString()
	f := &acrFixture{userID: mustV7(t)}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		f.userID, "flow09-"+suffix[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM user_visited_clients WHERE user_id = ?`, f.userID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, f.userID).Error
		for _, c := range f.clients {
			_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, c).Error
		}
	})
	return f
}

// addClient 建业务系统并返回 client_id。
func (f *acrFixture) addClient(t *testing.T, db *gorm.DB, tier, status string) string {
	t.Helper()
	cid := "flow09-" + uuid.NewString()
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier, status) VALUES (?, ?, ?, ?)`,
		cid, "flow09 client", tier, status).Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	f.clients = append(f.clients, cid)
	return cid
}

// visit 写一条访问记录（`tier_at_visit` 只是追溯用，不参与门槛计算）。
func (f *acrFixture) visit(t *testing.T, db *gorm.DB, clientID, tierAtVisit string) {
	t.Helper()
	err := db.Exec(`INSERT INTO user_visited_clients (id, user_id, client_id, tier_at_visit, visit_count, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, 1, now(), now())
		ON CONFLICT (user_id, client_id) DO NOTHING`,
		mustV7(t), f.userID, clientID, tierAtVisit).Error
	if err != nil {
		t.Fatalf("insert visit: %v", err)
	}
}

// 访问过 L1/L3/L2（全 active）→ 取 L3；同时读 L3 的默认门槛 = AAL2。
func TestDBMaxVisitedTierPicksHighestActive(t *testing.T) {
	repo, db := openEffectiveACRRepo(t)
	f := newACRFixture(t, db)
	for _, tier := range []string{"L1", "L3", "L2"} {
		cid := f.addClient(t, db, tier, "active")
		f.visit(t, db, cid, tier)
	}

	tier, found, err := repo.MaxVisitedTier(context.Background(), f.userID)
	if err != nil {
		t.Fatalf("MaxVisitedTier: %v", err)
	}
	if !found || tier != "L3" {
		t.Fatalf("want L3/found，实际 tier=%q found=%v", tier, found)
	}
	minACR, err := repo.TierDefaultMinACR(context.Background(), tier)
	if err != nil {
		t.Fatalf("TierDefaultMinACR: %v", err)
	}
	if minACR != "AAL2" {
		t.Fatalf("L3 的 tier_default.min_acr 应为 AAL2，实际 %q", minACR)
	}
}

// 最高档位的业务系统被**停用** → 不参与计算，回落到它下面那个档位。
func TestDBMaxVisitedTierSkipsDisabledClient(t *testing.T) {
	repo, db := openEffectiveACRRepo(t)
	f := newACRFixture(t, db)
	l2 := f.addClient(t, db, "L2", "active")
	f.visit(t, db, l2, "L2")
	l3 := f.addClient(t, db, "L3", "disabled") // 停用：按 join 过滤
	f.visit(t, db, l3, "L3")

	tier, found, err := repo.MaxVisitedTier(context.Background(), f.userID)
	if err != nil {
		t.Fatalf("MaxVisitedTier: %v", err)
	}
	if !found || tier != "L2" {
		t.Fatalf("停用的 L3 不得参与，want L2/found，实际 tier=%q found=%v", tier, found)
	}
}

// 没有访问记录 → found=false（调用方回退平台默认 L2 → AAL1）。
func TestDBMaxVisitedTierNoVisits(t *testing.T) {
	repo, db := openEffectiveACRRepo(t)
	f := newACRFixture(t, db)

	tier, found, err := repo.MaxVisitedTier(context.Background(), f.userID)
	if err != nil {
		t.Fatalf("MaxVisitedTier: %v", err)
	}
	if found || tier != "" {
		t.Fatalf("无访问记录应 found=false，实际 tier=%q found=%v", tier, found)
	}
	// 回退档位（L2）的门槛必须读得出来，否则"回退"变成"无门槛"。
	if minACR, err := repo.TierDefaultMinACR(context.Background(), PlatformDefaultTier); err != nil || minACR != "AAL1" {
		t.Fatalf("平台默认 L2 的门槛应为 AAL1，实际 %q err=%v", minACR, err)
	}
}

// 未知档位 → 报错（不静默当成最低档）。
func TestDBTierDefaultMinACRRejectsUnknownTier(t *testing.T) {
	repo, _ := openEffectiveACRRepo(t)
	if _, err := repo.TierDefaultMinACR(context.Background(), "L9"); err == nil {
		t.Fatal("未知档位必须报错")
	}
}
