//go:build integration

package audit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是**哈希链的真库证据**（AC-AUD-03/04/05/11），真 PG17 `guanjin_test`：
//
//	03 删中间一条 → 断链可检出；
//	04 **并发 100 写** → 不断链、整链复核通过（且逐条 hash 接得上、无重复 tag）；
//	05 篡改一行 → `broken`（`integrity_tag_mismatch`）；
//	11 两个指标的语义（`audit_chain_broken_total` 每次断链 +1、`audit_anchor_age_seconds`
//	   以成功复核为基准）+ 整链复核器可在 `cmd/guanjind` 进程内跑（装配见 `cmd/guanjind/audit_job.go`）。

func openAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		// 不许 Skip（Gate 口径：SKIP = 未验证 = 红）。
		t.Fatal("必须设置 GUANJIN_TEST_DSN（integration job 已注入）")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Ping(ctx, db); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if !db.Migrator().HasTable("audit_logs") {
		t.Fatalf("缺 audit_logs 表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN")
	}
	t.Cleanup(func() { _ = store.Close(db) })
	return db
}

func auditTestChain(t *testing.T) *Chain {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 0x31)
	}
	master, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	key, err := master.DeriveHMAC(crypto.PurposeHMACAudit)
	if err != nil {
		t.Fatalf("DeriveHMAC: %v", err)
	}
	chain, err := NewChain(key)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	return chain
}

// cleanMonth 选一个"干净月份"（远离真实数据的月份），让用例的链段只含自己写的记录。
//
// 为什么不能直接用当月：`guanjin_test` 里可能已有别人写的审计行，混进来会让
// "删一条/改一条"的断言互相干扰。用未来月份的日期写入是合法的（`occurred_at` 无上限约束）。
func cleanMonth(t *testing.T) time.Time {
	t.Helper()
	// **每个用例一个唯一月份**：早先用随机月份（24 个候选）会撞月——两条用例共用一个月分区，
	// 后一条的链尾接着前一条，`Verify(nil anchor)` 就会在 index 0 报 prev_tag_mismatch（假红）。
	// 用进程内计数器 + 纳秒低位做偏移，保证同一次运行内不重复、跨运行也几乎不重复。
	n := monthSeq.Add(1)
	base := time.Date(2060, 1, 1, 0, 0, 0, 0, time.UTC)
	// 抖动取纳秒的**高位**（每次运行几乎必然不同），避免跨运行复用到同一个月：
	// 复用会让"空月份不写锚""干净链不该断"这类断言读到上一轮的残留（实测踩过两次）。
	jitter := int(time.Now().UnixNano() % 100000)
	return base.AddDate(0, int(n)+jitter, 0)
}

// monthSeq 给每个用例分配唯一的"未来月份"，避免撞月（见 `cleanMonth`）。
var monthSeq atomic.Int64

// ensurePartition 给测试月份建一个月分区（生产由 `cmd/guanjind` 的月度任务做，见 000011 注释）。
//
// 为什么测试要自己建：`audit_logs` 是 RANGE 分区表，迁移只 seed 了「上月/当月/下月」，
// 用"未来月份"隔离数据就必须自己补一个分区；用完 `DROP TABLE` 掉，不留垃圾。
func ensurePartition(t *testing.T, db *gorm.DB, month time.Time) {
	t.Helper()
	name := fmt.Sprintf("audit_logs_p%s", month.Format("200601"))
	from := month.Format("2006-01-02")
	to := month.AddDate(0, 1, 0).Format("2006-01-02")
	if err := db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF audit_logs
		FOR VALUES FROM ('%s') TO ('%s')`, name, from, to)).Error; err != nil {
		t.Fatalf("create partition %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, name)).Error
	})
}

func appendN(t *testing.T, chain *Chain, db *gorm.DB, month time.Time, n int) []Record {
	t.Helper()
	out := make([]Record, 0, n)
	for i := 0; i < n; i++ {
		rec := Record{
			ActorRole: "sec_admin", Action: "policy.update", Result: "success",
			TargetType: "client", TargetID: fmt.Sprintf("c-%d", i), ClientID: "s10-audit",
			Before:     map[string]any{"n": fmt.Sprintf("%d", i)},
			After:      map[string]any{"n": fmt.Sprintf("%d", i+1)},
			TraceID:    "trace-" + uuid.NewString()[:8],
			OccurredAt: month.Add(time.Duration(i) * time.Second),
		}
		if _, err := chain.Append(context.Background(), db, rec); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
		out = append(out, rec)
	}
	return out
}

func cleanupMonth(t *testing.T, db *gorm.DB, month time.Time) {
	t.Helper()
	to := month.AddDate(0, 1, 0)
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM audit_logs WHERE occurred_at >= ? AND occurred_at < ?`, month, to).Error
		// 锚点也要清：跨运行复用同一"测试月份"时，旧锚会让"空月份不写锚"的断言假红
		// （实测踩到：上一轮跑过的月份留下了 anchor，这一轮读到它）。
		_ = db.Exec(`DELETE FROM runtime_config WHERE key = ?`, AnchorKey(month)).Error
	})
}

// AC-AUD-03：删中间一条 → 断链可检出（`prev_tag_mismatch`），且**整链复核器**能报出来。
func TestAC_AUD_03_DeletedMiddleRecordIsDetected(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := cleanMonth(t)
	ensurePartition(t, db, month)
	cleanupMonth(t, db, month)
	appendN(t, chain, db, month, 5)

	// 直接删中间一条（模拟"有人删了证据"）。
	// 生产环境 `audit_logs` 有 append-only 触发器（UPDATE/DELETE 一律 23001），
	// 这里必须**临时关掉**才能构造出"已被篡改的库"——这正是本用例要检测的对象；
	// 用完立刻恢复（t.Cleanup 里）。
	allowMutationForTest(t, db, month)
	if err := db.Exec(`DELETE FROM audit_logs WHERE occurred_at = ?`, month.Add(2*time.Second)).Error; err != nil {
		t.Fatalf("delete middle: %v", err)
	}

	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("删一条后应有 4 条，实际 %d", len(records))
	}
	// 测试月份是全球链里的一段：锚点取**现存首条的 prev_tag**（它指向段外的链尾）。
	broken, err := chain.Verify(records, records[0].PrevTag)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if broken == nil || broken.Reason != ReasonPrevMismatch {
		t.Fatalf("删中间一条必须以 %s 断链，实际 %+v", ReasonPrevMismatch, broken)
	}
}

// AC-AUD-05：篡改一行内容（不改 tag）→ `integrity_tag_mismatch`；同时验指标递增。
func TestAC_AUD_05_TamperedRecordIsDetectedAndMetricIncrements(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := cleanMonth(t)
	ensurePartition(t, db, month)
	cleanupMonth(t, db, month)
	appendN(t, chain, db, month, 4)

	ResetMetricsForTest()
	allowMutationForTest(t, db, month)
	if err := db.Exec(`UPDATE audit_logs SET after = '{"n":"tampered"}'::jsonb WHERE occurred_at = ?`,
		month.Add(1*time.Second)).Error; err != nil {
		t.Fatalf("tamper: %v", err)
	}

	verifier, err := NewVerifier(chain, db, NewAnchorStore(db), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	res, err := verifier.RunRange(context.Background(), month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("RunRange: %v", err)
	}
	if res.Broken == nil || res.Broken.Reason != ReasonTagMismatch {
		t.Fatalf("篡改必须以 %s 断链，实际 %+v", ReasonTagMismatch, res.Broken)
	}
	// 指标：每次 RunRange 发现断链 +1。
	if got := ChainBrokenTotal(); got != 1 {
		t.Fatalf("audit_chain_broken_total 期望 1，实际 %d", got)
	}
}

// AC-AUD-04：**并发 100 写** → 全部落库、链完整（逐条 hash 接上、tag 无重复），
// 且整链复核通过（这是"并发下不丢链不重号"的唯一证明）。
func TestAC_AUD_04_Concurrent100AppendsKeepChainIntact(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := cleanMonth(t)
	ensurePartition(t, db, month)
	cleanupMonth(t, db, month)

	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := chain.Append(context.Background(), db, Record{
				Action: "user.disable", Result: "success", TargetType: "user",
				TargetID: fmt.Sprintf("u-%d", i), ClientID: "s10-audit",
				After:      map[string]any{"i": fmt.Sprintf("%d", i)},
				OccurredAt: month.Add(time.Duration(i) * time.Millisecond),
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发写入失败：%v", err)
	}

	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != n {
		t.Fatalf("期望 %d 条，实际 %d（并发丢链/丢记录）", n, len(records))
	}
	// 逐条 hash 接得上 + tag 无重复（"不重号"）。
	seen := map[string]bool{}
	for i, rec := range records {
		if len(rec.IntegrityTag) == 0 {
			t.Fatalf("第 %d 条没有 integrity_tag", i)
		}
		key := string(rec.IntegrityTag)
		if seen[key] {
			t.Fatalf("第 %d 条 tag 与前面重复（链出现分叉）", i)
		}
		seen[key] = true
	}
	broken, err := chain.Verify(records, records[0].PrevTag)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if broken != nil {
		t.Fatalf("并发 100 写后整链必须通过，实际断在 %+v", broken)
	}
}

// AC-AUD-11：锚点写入/读取 + 复核通过后 `AnchorAge` 归零（指标基准）。
func TestAC_AUD_11_AnchorAndVerifyMetrics(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := cleanMonth(t)
	ensurePartition(t, db, month)
	cleanupMonth(t, db, month)
	appendN(t, chain, db, month, 3)

	anchors := NewAnchorStore(db)
	verifier, err := NewVerifier(chain, db, anchors, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	// 让 verifier 的"当月"= 本用例的干净月份。
	verifier.now = func() time.Time { return month }

	// 先写"本段锚点"= 现存首条的 prev_tag（真实分段语义：锚点来自上一段链尾）。
	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := anchors.Put(context.Background(), month, records[0].PrevTag); err != nil {
		t.Fatalf("seed segment anchor: %v", err)
	}

	ResetMetricsForTest()
	res, err := verifier.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Broken != nil {
		t.Fatalf("干净链不该断：%+v", res.Broken)
	}
	if res.Checked != 3 {
		t.Fatalf("复核条数期望 3，实际 %d", res.Checked)
	}
	// 指标：成功复核后 AnchorAge 可读且很小（不是 0 值哨兵）。
	age, ok := AnchorAge(time.Now().UTC())
	if !ok {
		t.Fatal("成功复核后 AnchorAge 应可用")
	}
	if age > time.Minute {
		t.Fatalf("刚复核完的 AnchorAge 应 <1min，实际 %s", age)
	}

	// 写当月锚点 → 读回来与链尾一致。
	if err := verifier.AnchorCurrentMonth(context.Background()); err != nil {
		t.Fatalf("AnchorCurrentMonth: %v", err)
	}
	got, err := anchors.Get(context.Background(), month)
	if err != nil {
		t.Fatalf("anchor Get: %v", err)
	}
	// `records` 已在上面（写锚点前）取过，这里复用即可：同一段、同一链尾。
	if len(got) == 0 || string(got) != string(records[len(records)-1].IntegrityTag) {
		t.Fatal("锚点必须等于链尾的 integrity_tag")
	}
}

// 锚点错误 → 首条即断（"尾部被整段删除"只能靠锚点发现）。
func TestAnchorMismatchBreaksAtFirstRecord(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := cleanMonth(t)
	ensurePartition(t, db, month)
	cleanupMonth(t, db, month)
	appendN(t, chain, db, month, 3)

	anchors := NewAnchorStore(db)
	bad := make([]byte, 32)
	for i := range bad {
		bad[i] = 0xEE // 一个长度合法、内容错的锚（模拟"上一段被封条改了"）
	}
	// 写一个错锚（模拟"上一段被封条改了"）：走 `AnchorStore.Put`，格式与生产一致。
	if err := anchors.Put(context.Background(), month, bad); err != nil {
		t.Fatalf("seed bad anchor: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM runtime_config WHERE key = ?`, AnchorKey(month)).Error })

	verifier, err := NewVerifier(chain, db, anchors, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	res, err := verifier.RunRange(context.Background(), month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("RunRange: %v", err)
	}
	if res.Broken == nil || res.Broken.Index != 0 {
		t.Fatalf("错锚点应在首条即断，实际 %+v", res.Broken)
	}
}

// allowMutationForTest 临时关闭某月分区的 append-only 触发器（仅供构造"已被篡改"的场景）。
//
// 生产环境**不许**这么做（`audit_logs_append_only` 就是判据 AC-AUD-02 的实现）；
// 用例结束后立刻恢复，确保不留后门。
func allowMutationForTest(t *testing.T, db *gorm.DB, month time.Time) {
	t.Helper()
	name := fmt.Sprintf("audit_logs_p%s", month.Format("200601"))
	stmt := fmt.Sprintf(`ALTER TABLE %s DISABLE TRIGGER USER`, name)
	if err := db.Exec(stmt).Error; err != nil {
		t.Fatalf("disable trigger on %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = db.Exec(fmt.Sprintf(`ALTER TABLE %s ENABLE TRIGGER USER`, name)).Error
	})
}

// encodeAnchorForTest 把 tag 编成锚点列里的 JSON 字符串（与 AnchorStore.Put 同格式）。
func encodeAnchorForTest(tag []byte) string {
	out, _ := json.Marshal(base64.StdEncoding.EncodeToString(tag))
	return string(out)
}

// AC-AUD-11（Job 语义）：空月份不写锚点；真实 Job 在 ticker 上跑一遍（含锚点写入）。
func TestAC_AUD_11_JobRunsOnTickerAndSkipsEmptyMonth(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := cleanMonth(t)
	ensurePartition(t, db, month)
	cleanupMonth(t, db, month)

	anchors := NewAnchorStore(db)
	verifier, err := NewVerifier(chain, db, anchors, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	verifier.now = func() time.Time { return month }

	// 前置：把这一段可能残留的锚清掉（跨运行复用月份时会有），再断言"空月不写锚"。
	if err := db.Exec(`DELETE FROM runtime_config WHERE key = ?`, AnchorKey(month)).Error; err != nil {
		t.Fatalf("clear anchor: %v", err)
	}
	// 空月份：复核 0 条、不报错，且**不写锚点**（空锚会让"空月"与"锚点丢失"混淆）。
	res, err := verifier.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(empty): %v", err)
	}
	if res.Checked != 0 || res.Broken != nil {
		t.Fatalf("空月份期望 0 条通过，实际 %+v", res)
	}
	if err := verifier.AnchorCurrentMonth(context.Background()); err != nil {
		t.Fatalf("AnchorCurrentMonth(empty): %v", err)
	}
	if tag, err := anchors.Get(context.Background(), month); err != nil || len(tag) != 0 {
		t.Fatalf("空月份不该写锚点，实际 tag=%v err=%v", tag, err)
	}

	// 有数据 + 真 Job 跑一拍：覆盖 ticker → RunOnce → AnchorCurrentMonth 的整条路径。
	appendN(t, chain, db, month, 2)
	ResetMetricsForTest()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := StartJob(ctx, verifier, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	deadline := time.Now().Add(2 * time.Second)
	for {
		if age, ok := AnchorAge(time.Now().UTC()); ok && age < time.Minute {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Job 没在 2s 内完成一次整链复核（ticker 未生效）")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	// 锚点应已被 Job 写入，且等于链尾。
	tag, err := anchors.Get(context.Background(), month)
	if err != nil {
		t.Fatalf("anchor Get: %v", err)
	}
	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(tag) == 0 || string(tag) != string(records[len(records)-1].IntegrityTag) {
		t.Fatal("Job 跑完后锚点应等于链尾")
	}
}

// 记录的两种"可选字段形态"都要能落库并验过：有 actor_user_id + 有 before/after，以及全空。
// （覆盖 `nullUUID` / `jsonOrNil` 的两侧分支——它们写错会让"匿名操作"或"无变更快照"
// 的审计事实落不进去，而那两类恰恰是最需要留痕的。）
func TestAppendHandlesOptionalColumns(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := cleanMonth(t)
	ensurePartition(t, db, month)
	cleanupMonth(t, db, month)

	actor, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		actor, "audit-"+actor.String()).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM users WHERE id = ?`, actor).Error })

	// ① 带 actor + before/after。
	if _, err := chain.Append(context.Background(), db, Record{
		ID: mustID(t), ActorUserID: &actor, ActorRole: "sec_admin", Action: "role.bind", Result: "success",
		TargetType: "role_binding", TargetID: "rb-1", ClientID: "s10-audit",
		Before: map[string]any{"role": "viewer"}, After: map[string]any{"role": "admin"},
		IP: "203.0.113.7", TraceID: "trace-opt-1",
		OccurredAt: month.Add(time.Second),
	}); err != nil {
		t.Fatalf("Append(with actor): %v", err)
	}
	// ② 全空的"匿名/无快照"记录。
	if _, err := chain.Append(context.Background(), db, Record{
		ID: mustID(t), Action: "system.cleanup", Result: "success",
		OccurredAt: month.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("Append(minimal): %v", err)
	}

	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(records))
	}
	if records[0].ActorUserID == nil || *records[0].ActorUserID != actor {
		t.Fatalf("actor_user_id 未落库：%+v", records[0])
	}
	if records[0].Before == nil || records[0].Before["role"] != "viewer" {
		t.Fatalf("before 未落库：%+v", records[0].Before)
	}
	if records[1].ActorUserID != nil || records[1].Before != nil {
		t.Fatalf("最小记录的 actor/before 应为空：%+v", records[1])
	}
	if broken, err := chain.Verify(records, nil); err != nil || broken != nil {
		t.Fatalf("两种形态都应验过，实际 broken=%+v err=%v", broken, err)
	}
}

func mustID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return id
}
