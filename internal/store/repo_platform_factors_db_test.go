//go:build integration

package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/audit"
	"github.com/spencerswagger/guanjin/internal/crypto"
)

// 本文件是 `AC-FLOW-05` 的**真库证据**（`-tags=integration` + `GUANJIN_TEST_DSN`）：
//
//  1. 平台级启停落在 `runtime_config.factors.disabled`（jsonb），读回一致；
//  2. **同一事务**内追加 `audit_logs`：动作/目标/before-after/reason 都对得上；
//  3. **审计写失败 → 配置不变**（判据要求"失败整体回滚"）；
//  4. 幂等：重复提交同一条启停不产生第二条审计事实；
//  5. 枚举：启用后从集合里移除。

// auditChainForTest 用测试主密钥派生出审计链（键来源与生产一致：`PurposeHMACAudit`）。
func auditChainForTest(t *testing.T) *audit.Chain {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 7)
	}
	master, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	key, err := master.DeriveHMAC(crypto.PurposeHMACAudit)
	if err != nil {
		t.Fatalf("DeriveHMAC: %v", err)
	}
	chain, err := audit.NewChain(key)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	return chain
}

// chainAppender 把 store 的记录交给审计链（与生产适配器同形态：**接受外部事务**）。
type chainAppender struct{ chain *audit.Chain }

func (a chainAppender) AppendInTx(ctx context.Context, tx *gorm.DB, rec FactorAuditRecord) error {
	actor := rec.ActorUserID
	_, err := a.chain.Append(ctx, tx, audit.Record{
		ID:          rec.ID,
		ActorUserID: &actor,
		ActorRole:   rec.ActorRole,
		Action:      rec.Action,
		TargetType:  rec.TargetType,
		TargetID:    rec.TargetID,
		Before:      rec.Before,
		After:       rec.After,
		Result:      rec.Result,
		IP:          rec.IP,
		TraceID:     rec.TraceID,
		OccurredAt:  rec.OccurredAt,
	})
	return err
}

// failingAppender 让审计写入必然失败（用来验"配置不变"）。
type failingAppender struct{}

func (failingAppender) AppendInTx(context.Context, *gorm.DB, FactorAuditRecord) error {
	return errors.New("injected audit failure")
}

func openPlatformFactorRepo(t *testing.T) (*PlatformFactorRepo, *PlatformFactorReader, *gorm.DB, uuid.UUID) {
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
	if !db.Migrator().HasTable("audit_logs") {
		t.Fatal("测试库缺少 audit_logs 表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN")
	}
	t.Cleanup(func() { _ = Close(db) })
	// 每个用例从干净键开始：清掉平台级禁用集合（只动这一个键）。
	if err := db.Exec(`DELETE FROM runtime_config WHERE "key" = ?`, KeyFactorsDisabled).Error; err != nil {
		t.Fatalf("reset %s: %v", KeyFactorsDisabled, err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM runtime_config WHERE "key" = ?`, KeyFactorsDisabled).Error
	})
	reader, err := NewPlatformFactorReader(db)
	if err != nil {
		t.Fatalf("NewPlatformFactorReader: %v", err)
	}
	repo, err := NewPlatformFactorRepo(reader, chainAppender{chain: auditChainForTest(t)})
	if err != nil {
		t.Fatalf("NewPlatformFactorRepo: %v", err)
	}
	// 审计与 runtime_config.updated_by 都有 FK 指向 users：必须用**真实**操作者。
	f := newFixture(t, db)
	return repo, reader, db, f.userID
}

func countAudit(t *testing.T, db *gorm.DB, action, targetID string) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM audit_logs WHERE action = ? AND target_id = ?`,
		action, targetID).Scan(&n).Error; err != nil {
		t.Fatalf("count audit_logs: %v", err)
	}
	return n
}

// 停用 → 集合里出现、审计一条、before/after 与 reason 都落库。
func TestDBPlatformFactorDisableWritesAuditInSameTx(t *testing.T) {
	repo, reader, db, actor := openPlatformFactorRepo(t)
	ctx := context.Background()
	typ := "password"
	trace := "trace-flow05-" + uuid.NewString()

	before := countAudit(t, db, "factor.disable", typ)
	if err := repo.SetEnabled(ctx, SetFactorEnabledInput{
		Type: typ, Enabled: false, Reason: "通道故障",
		ActorUserID: actor, ActorRole: "security_admin", TraceID: trace, IP: "10.0.0.9",
	}); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	got, err := reader.Disabled(ctx)
	if err != nil {
		t.Fatalf("Disabled: %v", err)
	}
	if len(got) != 1 || got[0] != typ {
		t.Fatalf("平台级禁用集合 = %v, want [%s]", got, typ)
	}
	if after := countAudit(t, db, "factor.disable", typ); after != before+1 {
		t.Fatalf("审计条数 = %d, want %d", after, before+1)
	}

	// 逐字段核对审计事实（页面/审计员就靠这些回答"谁在什么时候为什么关的"）。
	var row struct {
		ActorRole  string `gorm:"column:actor_role"`
		TargetType string `gorm:"column:target_type"`
		Before     string `gorm:"column:before"`
		After      string `gorm:"column:after"`
		Result     string `gorm:"column:result"`
		TraceID    string `gorm:"column:trace_id"`
		PrevTag    []byte `gorm:"column:prev_tag"`
		IntegTag   []byte `gorm:"column:integrity_tag"`
	}
	err = db.Raw(`SELECT actor_role, target_type, before::text, after::text, result, trace_id,
		prev_tag, integrity_tag FROM audit_logs
		WHERE action = 'factor.disable' AND target_id = ? ORDER BY occurred_at DESC LIMIT 1`, typ).
		Scan(&row).Error
	if err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	if row.ActorRole != "security_admin" || row.TargetType != "factor" || row.Result != "success" {
		t.Fatalf("审计主体/目标不对：%+v", row)
	}
	if row.TraceID != trace {
		t.Fatalf("trace_id = %q, want %q", row.TraceID, trace)
	}
	if !contains(row.Before, `"enabled": true`) || !contains(row.After, `"enabled": false`) {
		t.Fatalf("before/after 不对：before=%s after=%s", row.Before, row.After)
	}
	if !contains(row.After, "通道故障") {
		t.Fatalf("reason 必须进 after（审计要能回答为什么）：%s", row.After)
	}
	if len(row.IntegTag) == 0 {
		t.Fatal("integrity_tag 必须非空（链上事实）")
	}
}

// 幂等：重复停用不再写第二条审计、也不改配置。
func TestDBPlatformFactorIdempotentNoExtraAudit(t *testing.T) {
	repo, _, db, actor := openPlatformFactorRepo(t)
	ctx := context.Background()
	typ := "totp"

	in := SetFactorEnabledInput{Type: typ, Enabled: false, Reason: "r", ActorRole: "system_admin",
		ActorUserID: actor}
	if err := repo.SetEnabled(ctx, in); err != nil {
		t.Fatalf("first: %v", err)
	}
	n1 := countAudit(t, db, "factor.disable", typ)
	if err := repo.SetEnabled(ctx, in); err != nil {
		t.Fatalf("second: %v", err)
	}
	if n2 := countAudit(t, db, "factor.disable", typ); n2 != n1 {
		t.Fatalf("幂等重放不得新增审计：%d → %d", n1, n2)
	}
}

// 审计写失败 → **整体回滚**：报错，且 `factors.disabled` 一个字节都没变。
func TestDBPlatformFactorAuditFailureRollsBackConfig(t *testing.T) {
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	ctx := context.Background()
	db, err := Open(ctx, Options{DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = Close(db) })
	if err := db.Exec(`DELETE FROM runtime_config WHERE "key" = ?`, KeyFactorsDisabled).Error; err != nil {
		t.Fatalf("reset: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM runtime_config WHERE "key" = ?`, KeyFactorsDisabled).Error })

	reader, err := NewPlatformFactorReader(db)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	repo, err := NewPlatformFactorRepo(reader, failingAppender{})
	if err != nil {
		t.Fatalf("repo: %v", err)
	}

	err = repo.SetEnabled(ctx, SetFactorEnabledInput{Type: "password", Enabled: false, Reason: "x"})
	if err == nil {
		t.Fatal("审计写失败必须报错（不许假装成功）")
	}
	got, derr := reader.Disabled(ctx)
	if derr != nil {
		t.Fatalf("Disabled: %v", derr)
	}
	if len(got) != 0 {
		t.Fatalf("审计失败时配置必须回滚，实际集合 = %v", got)
	}
	// 键本身也不该被建出来（事务回滚到"没有这一行"的状态）。
	var n int64
	if err := db.Raw(`SELECT count(*) FROM runtime_config WHERE "key" = ?`, KeyFactorsDisabled).
		Scan(&n).Error; err != nil {
		t.Fatalf("count runtime_config: %v", err)
	}
	if n != 0 {
		t.Fatalf("审计失败时不得留下 runtime_config 行，实际 %d 行", n)
	}
}

// 启用 → 从集合里移除（并写 `factor.enable`）。
func TestDBPlatformFactorEnableRemovesFromSet(t *testing.T) {
	repo, reader, db, actor := openPlatformFactorRepo(t)
	ctx := context.Background()
	typ := "password"

	if err := repo.SetEnabled(ctx, SetFactorEnabledInput{Type: typ, Enabled: false, Reason: "关", ActorUserID: actor}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := repo.SetEnabled(ctx, SetFactorEnabledInput{Type: typ, Enabled: true, Reason: "恢复", ActorUserID: actor}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got, err := reader.Disabled(ctx)
	if err != nil {
		t.Fatalf("Disabled: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("启用后集合应为空，实际 %v", got)
	}
	if n := countAudit(t, db, "factor.enable", typ); n == 0 {
		t.Fatal("启用也必须写审计（factor.enable）")
	}
}

// 集合是排序后的稳定输出（页面/断言都用得上）。
func TestDBPlatformFactorDisabledSorted(t *testing.T) {
	repo, reader, _, actor := openPlatformFactorRepo(t)
	ctx := context.Background()
	for _, typ := range []string{"totp", "password", "sms"} {
		if err := repo.SetEnabled(ctx, SetFactorEnabledInput{Type: typ, Enabled: false, Reason: "r", ActorUserID: actor}); err != nil {
			t.Fatalf("disable %s: %v", typ, err)
		}
	}
	got, err := reader.Disabled(ctx)
	if err != nil {
		t.Fatalf("Disabled: %v", err)
	}
	want := []string{"password", "sms", "totp"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序不稳定：got %v, want %v", got, want)
		}
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
