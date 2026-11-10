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
	// 每个用例从干净键开始：**保证这一行存在且是空集 `[]`**（只动这一个键）。
	// 不能删行了（2026-11-11 定稿口径）：缺行现在是 fail-closed 错误，不再是"空集"。
	if err := resetPlatformFactors(t, db); err != nil {
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
	typ := "probe-a"
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
	typ := "probe-b"

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

// 注意（测试卫生）：本文件的用例**统一用探针因子类型**（`probe-a/b/c`），不碰 `password`/`totp`。
// 原因：`factors.disabled` 是**全局行**，`-p 8` 下 `cmd/guanjind` 的 E2E 正在算 ACR——
// 用例期间把 `password` 停掉，那边会红成 `422 FACTOR_DISABLED`（实测偶发）。
// 仓库逻辑（同事务审计、幂等、排序）与"类型是不是真实因子"无关，用探针类型照样证得到。
//
// 审计写失败 → **整体回滚**：报错，且 `factors.disabled` 一个字节都没变。
func TestDBPlatformFactorAuditFailureRollsBackConfig(t *testing.T) {
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	ctx := context.Background()
	db, err := Open(ctx, Options{DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = Close(db) })
	if err := resetPlatformFactors(t, db); err != nil {
		t.Fatalf("reset: %v", err)
	}
	t.Cleanup(func() { _ = resetPlatformFactors(t, db) })

	reader, err := NewPlatformFactorReader(db)
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	repo, err := NewPlatformFactorRepo(reader, failingAppender{})
	if err != nil {
		t.Fatalf("repo: %v", err)
	}

	err = repo.SetEnabled(ctx, SetFactorEnabledInput{Type: "probe-a", Enabled: false, Reason: "x"})
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
	// 行必须还在且仍是空集（D22：行由迁移 seed，写入路径**不再**建行/删行）。
	var n int64
	if err := db.Raw(`SELECT count(*) FROM runtime_config WHERE "key" = ?`, KeyFactorsDisabled).
		Scan(&n).Error; err != nil {
		t.Fatalf("count runtime_config: %v", err)
	}
	if n != 1 {
		t.Fatalf("审计失败时 runtime_config 行必须保持 1 行（D22 seed），实际 %d 行", n)
	}
}

// 启用 → 从集合里移除（并写 `factor.enable`）。
func TestDBPlatformFactorEnableRemovesFromSet(t *testing.T) {
	repo, reader, db, actor := openPlatformFactorRepo(t)
	ctx := context.Background()
	typ := "probe-a"

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
	for _, typ := range []string{"probe-b", "probe-a", "probe-c"} {
		if err := repo.SetEnabled(ctx, SetFactorEnabledInput{Type: typ, Enabled: false, Reason: "r", ActorUserID: actor}); err != nil {
			t.Fatalf("disable %s: %v", typ, err)
		}
	}
	got, err := reader.Disabled(ctx)
	if err != nil {
		t.Fatalf("Disabled: %v", err)
	}
	want := []string{"probe-a", "probe-b", "probe-c"}
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

// resetPlatformFactors 把 `factors.disabled` 重置成 **存在且为空集 `[]`**（D22）。
//
// 为什么不再 DELETE：缺行现在是 fail-closed 错误（`ErrFactorDisabledMissing`），
// 而"空集"的唯一表达是行存在 + 值为 `[]`（D22 原文）。用例基线必须与生产同形。
func resetPlatformFactors(t *testing.T, db *gorm.DB) error {
	t.Helper()
	return db.Exec(`INSERT INTO runtime_config ("key", value, value_type, description, is_secret)
		VALUES (?, '[]'::jsonb, 'json', '测试重置：平台级禁用集', false)
		ON CONFLICT ("key") DO UPDATE SET value = '[]'::jsonb, value_type = 'json'`,
		KeyFactorsDisabled).Error
}

// **D22 定稿口径（2026-11-11）**：缺行 / `null` / 非数组都是 fail-closed 错误，不许静默当空集。
//
// 反向证据：这条用例在"改前"会绿（`Disabled()` 返回 nil,nil），改后必须红——所以它同时也是
// `raw=="" → 空集` 这个安全降级被消灭的证据。
//
// **每个阶段都在"必然回滚"的事务里做**：`factors.disabled` 是**全局行**，`-p 8` 下别的包
// 正在读它（`cmd/guanjind` 的 ACR 用例、`ValidateStartup`）。第一版直接 DELETE 真行，
// 结果并行跑的 `cmd/guanjind` 当场红成 `runtime_config[factors.disabled] row is missing`
// ——测试自己的"造缺行"污染了别人。事务隔离后，缺行只在本事务内可见。
func TestDBPlatformFactorMissingOrBadValueIsFailClosed(t *testing.T) {
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		t.Fatal("必须设置 GUANJIN_TEST_DSN（真 PostgreSQL）")
	}
	ctx := context.Background()
	db, err := Open(ctx, Options{DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = Close(db) })
	if err := resetPlatformFactors(t, db); err != nil {
		t.Fatalf("reset: %v", err)
	}

	// inTx 在**必然回滚**的事务里跑 fn（fn 里的 reader/repo 都绑在这条事务上）。
	// 返回 fn 里的错误（若有），事务本身永远回滚。
	inTx := func(fn func(tx *gorm.DB)) {
		t.Helper()
		_ = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			fn(tx)
			return errFactorRollback
		})
	}

	// ① 缺行：读 → ErrFactorDisabledMissing；写 → 同样（**不许**顺手 insert 建行）。
	inTx(func(tx *gorm.DB) {
		if err := tx.Exec(`DELETE FROM runtime_config WHERE "key" = ?`, KeyFactorsDisabled).Error; err != nil {
			t.Fatalf("delete in tx: %v", err)
		}
		reader, err := NewPlatformFactorReader(tx)
		if err != nil {
			t.Fatalf("reader: %v", err)
		}
		repo, err := NewPlatformFactorRepo(reader, &recordAuditWriter{})
		if err != nil {
			t.Fatalf("repo: %v", err)
		}
		if got, err := reader.Disabled(ctx); !errors.Is(err, ErrFactorDisabledMissing) {
			t.Fatalf("缺行时 Disabled 必须返回 ErrFactorDisabledMissing，实际 got=%v err=%v", got, err)
		}
		if err := repo.SetEnabled(ctx, SetFactorEnabledInput{Type: "probe-a", Enabled: false, Reason: "x"}); !errors.Is(err, ErrFactorDisabledMissing) {
			t.Fatalf("缺行时 SetEnabled 必须返回 ErrFactorDisabledMissing（不建行），实际 %v", err)
		}
		var n int64
		if err := tx.Raw(`SELECT count(*) FROM runtime_config WHERE "key" = ?`, KeyFactorsDisabled).
			Scan(&n).Error; err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 0 {
			t.Fatalf("写入路径不得自己建行（D22：行由迁移 seed），实际 %d 行", n)
		}
	})

	// ② `null` 与"空集"不可区分（D22 原文）→ 必须当坏值，不许当空集。
	inTx(func(tx *gorm.DB) {
		reader, err := NewPlatformFactorReader(tx)
		if err != nil {
			t.Fatalf("reader: %v", err)
		}
		for _, bad := range []string{`null`, `{}`, `"probe-a"`, `[1,2]`, `[null]`} {
			if err := tx.Exec(`UPDATE runtime_config SET value = ?::jsonb, value_type = 'json'
				WHERE "key" = ?`, bad, KeyFactorsDisabled).Error; err != nil {
				t.Fatalf("seed bad value %s: %v", bad, err)
			}
			if got, err := reader.Disabled(ctx); !errors.Is(err, ErrFactorDisabledStore) {
				t.Fatalf("值 %s 必须返回 ErrFactorDisabledStore（不许当空集），实际 got=%v err=%v", bad, got, err)
			}
		}
	})

	// ③ 未知因子类型：**读保留原文**、读-改-写不丢（否则滚动升级时会意外变为可用）。
	inTx(func(tx *gorm.DB) {
		if err := tx.Exec(`UPDATE runtime_config SET value = '["probe-c","from-newer-node"]'::jsonb,
			value_type = 'json' WHERE "key" = ?`, KeyFactorsDisabled).Error; err != nil {
			t.Fatalf("seed unknown types: %v", err)
		}
		reader, err := NewPlatformFactorReader(tx)
		if err != nil {
			t.Fatalf("reader: %v", err)
		}
		repo, err := NewPlatformFactorRepo(reader, &recordAuditWriter{})
		if err != nil {
			t.Fatalf("repo: %v", err)
		}
		if got, err := reader.Disabled(ctx); err != nil || len(got) != 2 {
			t.Fatalf("读侧必须保留未知类型，实际 got=%v err=%v", got, err)
		}
		if err := repo.SetEnabled(ctx, SetFactorEnabledInput{Type: "probe-a", Enabled: false, Reason: "关"}); err != nil {
			t.Fatalf("disable password: %v", err)
		}
		got, err := reader.Disabled(ctx)
		if err != nil {
			t.Fatalf("Disabled: %v", err)
		}
		want := []string{"from-newer-node", "probe-a", "probe-c"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("读-改-写不得丢掉未知类型：got %v, want %v", got, want)
		}
	})
}

// errFactorRollback 只用来让上面的事务必然回滚（本身不参与断言）。
var errFactorRollback = errors.New("test: rollback on purpose")
