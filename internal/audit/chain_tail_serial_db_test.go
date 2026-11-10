//go:build integration && audit_serial

package audit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var _ = context.Background
var _ = errors.Is
var _ = fmt.Sprintf
var _ = io.Discard
var _ = slog.New
var _ = strings.HasPrefix
var _ = time.Now
var _ = uuid.Nil
var _ = gorm.ErrRecordNotFound

// 本文件的用例**依赖"全局链尾"**：所有测试共享同一条全局审计链，其它包一写就会把
// 链尾推高，断言就飘（实测 `-p 8` 下必红）。因此与并发/探针用例一起放在**串行专用套件**：
// `go test -tags 'integration audit_serial' ./internal/audit -p 1 -count=3`
// （`make test-audit-serial`；CI 独立一步并断言关键 PASS 行）。
// 并行套件只保留**不依赖链尾语义**的用例（纯函数、错误分支、分区存在性）。

func TestAC_AUD_03_DeletedMiddleRecordIsDetected(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)
	_, _ = appendN(t, chain, db, month, 5)
	// `Append` 是按值传参，生成的 id 拿不回来；先读一遍拿到**真实 id**再按 id 删。
	beforeDelete, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load(before delete): %v", err)
	}
	if len(beforeDelete) != 5 {
		t.Fatalf("前置：期望 5 条，实际 %d", len(beforeDelete))
	}

	// 直接删中间一条（模拟"有人删了证据"）。
	// 生产环境 `audit_logs` 有 append-only 触发器（UPDATE/DELETE 一律 23001），
	// 这里必须**临时关掉**才能构造出"已被篡改的库"——这正是本用例要检测的对象；
	// 用完立刻恢复（t.Cleanup 里）。
	allowMutationForTest(t, db, month)
	// **按 id 删**（不是按 `occurred_at`）：后者会连带删掉同一时刻的另一条（如果存在），
	// 造成"应有 4 条、实际 3 条"的假红（已在 `-p 8` 多轮里出现过）。
	if err := db.Exec(`DELETE FROM audit_logs WHERE id = ?`, beforeDelete[2].ID).Error; err != nil {
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

func TestAC_AUD_05_TamperedRecordIsDetectedAndMetricIncrements(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)
	_, _ = appendN(t, chain, db, month, 4)

	ResetMetricsForTest()
	allowMutationForTest(t, db, month)
	if err := db.Exec(`UPDATE audit_logs SET after = '{"n":"tampered"}'::jsonb WHERE occurred_at = ?`,
		month.Add(1*time.Second)).Error; err != nil {
		t.Fatalf("tamper: %v", err)
	}

	anchors := NewAnchorStore(db)
	verifier, err := NewVerifier(chain, db, anchors, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	// 篡改前先把"本段锚点"写好（篡改的是内容，不是段首的 prev_tag）。
	seedAnchorForMonth(t, chain, db, anchors, month)
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

func TestAC_AUD_11_AnchorAndVerifyMetrics(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)
	_, _ = appendN(t, chain, db, month, 3)

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
	// 锚点语义：`anchor[M]` = 该月链尾的 integrity_tag（P0 修复后的唯一口径）。
	if err := anchors.Put(context.Background(), month, records[len(records)-1].IntegrityTag); err != nil {
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
	if len(records) == 0 {
		t.Fatalf("本段应有记录，实际 0 条（月份=%s）", month.Format("200601"))
	}
	if len(got) == 0 || string(got) != string(records[len(records)-1].IntegrityTag) {
		t.Fatal("锚点必须等于链尾的 integrity_tag")
	}
}

func TestAC_AUD_11_JobRunsOnTickerAndSkipsEmptyMonth(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
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
	_, _ = appendN(t, chain, db, month, 2)
	// **必须清零"最近成功复核时刻"**（包级指标，前面用例已设过），否则等待循环会读到旧值。
	ResetMetricsForTest()
	seedAnchorForMonth(t, chain, db, anchors, month)

	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) == 0 {
		t.Fatalf("本段应有 2 条记录，实际 0 条（月份=%s）", month.Format("200601"))
	}
	want := records[len(records)-1].IntegrityTag

	stop := StartJob(context.Background(), verifier, 10*time.Millisecond,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer stop()

	// **等"锚点变成链尾"这件事本身**，而不是等包级 `AnchorAge`：后者是进程级共享变量、
	// 受其它用例影响（CI 上就红在这里：读到旧值 → 循环立刻 break → Job 一次没跑就断言）。
	deadline := time.Now().Add(3 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, err = anchors.Get(context.Background(), month)
		if err != nil {
			t.Fatalf("anchor Get: %v", err)
		}
		if string(got) == string(want) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if string(got) != string(want) {
		t.Fatalf("Job 跑完后锚点应等于链尾：期望 %x，实际 %x（月份=%s）",
			want, got, month.Format("200601"))
	}
}

func TestChainRealPathsForCoverage(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)

	anchors := NewAnchorStore(db)
	verifier, err := NewVerifier(chain, db, anchors, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	verifier.now = func() time.Time { return month }

	// **清掉跨运行残留的锚**：不断言的话，"空月不写锚"会读到上一次运行留下的值 → 假红。
	if err := db.Exec(`DELETE FROM runtime_config WHERE key = ?`, AnchorKey(month)).Error; err != nil {
		t.Fatalf("clear anchor: %v", err)
	}

	// ① 空区间复核 + 空月份不写锚。
	res, err := verifier.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(empty): %v", err)
	}
	if res.Checked != 0 {
		t.Fatalf("空区间应 0 条，实际 %d", res.Checked)
	}
	if err := verifier.AnchorCurrentMonth(context.Background()); err != nil {
		t.Fatalf("AnchorCurrentMonth(empty): %v", err)
	}
	if tag, err := anchors.Get(context.Background(), month); err != nil || len(tag) != 0 {
		t.Fatalf("空月份不该写锚，实际 %v / %v", tag, err)
	}

	// ② 同一事务内连续两条：第二条的 `prev_tag` 必须是第一条的 `integrity_tag`。
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin: %v", tx.Error)
	}
	if _, err := chain.Append(context.Background(), tx, Record{
		Action: "tx-1", Result: "success", OccurredAt: month.Add(time.Second),
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Append(tx-1): %v", err)
	}
	if _, err := chain.Append(context.Background(), tx, Record{
		Action: "tx-2", Result: "success", OccurredAt: month.Add(2 * time.Second),
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("Append(tx-2): %v", err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit: %v", err)
	}
	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(records))
	}
	if string(records[1].PrevTag) != string(records[0].IntegrityTag) {
		t.Fatal("第二条的 prev_tag 应等于第一条的 integrity_tag（同事务内链自接）")
	}
	if broken, err := chain.Verify(records, records[0].PrevTag); err != nil || broken != nil {
		t.Fatalf("链应完整，实际 broken=%+v err=%v", broken, err)
	}
}

func TestLoadEmptyRangeAndAnchorOverwrite(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)

	// ① 空区间：已预建分区但没有行。
	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load(empty): %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("空区间应 0 条，实际 %d", len(records))
	}
	// 空段复核通过（没有记录就没有断点）。
	if broken, err := chain.Verify(records, nil); err != nil || broken != nil {
		t.Fatalf("空段应通过，实际 broken=%+v err=%v", broken, err)
	}

	// ② 锚点覆盖写：同月写两次，读回后写的。
	anchors := NewAnchorStore(db)
	first := make([]byte, 32)
	second := make([]byte, 32)
	for i := range first {
		first[i] = 0x11
		second[i] = 0x22
	}
	if err := anchors.Put(context.Background(), month, first); err != nil {
		t.Fatalf("Put(first): %v", err)
	}
	if err := anchors.Put(context.Background(), month, second); err != nil {
		t.Fatalf("Put(second): %v", err)
	}
	got, err := anchors.Get(context.Background(), month)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(second) {
		t.Fatalf("锚点应被覆盖成后写的值，实际 %x", got)
	}
}

func TestOccurredAtBeforeTailMonthIsRejected(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)

	if _, err := appendWithReanchor(chain, db, Record{
		Action: "a", Result: "success", OccurredAt: month.Add(time.Second),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// 比链尾早一个月：必须拒绝并归到哨兵。
	_, err := chain.Append(context.Background(), db, Record{
		Action: "b", Result: "success", OccurredAt: month.AddDate(0, -1, 1),
	})
	if !errors.Is(err, ErrOccurredAtTooOld) {
		t.Fatalf("期望 ErrOccurredAtTooOld，实际 %v", err)
	}
}

func TestOccurredAtIsPreservedAndHashed(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)

	want := month.Add(3 * time.Second).Truncate(time.Microsecond)
	first := Record{Action: "user.disable", Result: "success", TargetID: "same", OccurredAt: want}
	if _, err := appendWithReanchor(chain, db, first); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// 同一秒内再来一条（**调用方时间不比链尾晚**）：`occurred_at` 必须原样保留，
	// 由 `created_at` 保证链序（这正是裁定的要点）。
	second := Record{Action: "user.disable", Result: "success", TargetID: "same", OccurredAt: want}
	if _, err := appendWithReanchor(chain, db, second); err != nil {
		t.Fatalf("Append(second): %v", err)
	}

	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(records))
	}
	for i, rec := range records {
		if !rec.OccurredAt.Equal(want) {
			t.Fatalf("第 %d 条 occurred_at 被改写了：期望 %s，实际 %s（取证时间不许动）",
				i+1, want, rec.OccurredAt)
		}
	}
	// 内容完全相同、只有 id 不同 → tag 必不同（否则"改内容不改 tag"就检不出来）。
	if string(records[0].IntegrityTag) == string(records[1].IntegrityTag) {
		t.Fatal("两条记录 tag 相同 → 说明 id/内容没进哈希")
	}
	// `occurred_at` 进哈希：同一条内容、只改 occurred_at → tag 必须不同。
	c := testChain(t)
	base := Record{Action: "a", Result: "success", OccurredAt: want}
	other := base
	other.OccurredAt = want.Add(time.Second)
	tagA, err := c.Tag(nil, base)
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	tagB, err := c.Tag(nil, other)
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if string(tagA) == string(tagB) {
		t.Fatal("改 occurred_at 后 tag 没变 → occurred_at 没进哈希（证据就不是原样了）")
	}
	// 链仍完整（created_at 排序把两条串起来）。
	if broken, err := chain.Verify(records, records[0].PrevTag); err != nil || broken != nil {
		t.Fatalf("链应完整，实际 broken=%+v err=%v", broken, err)
	}
}

func TestAppendHandlesOptionalColumns(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
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
	if _, err := appendWithReanchor(chain, db, Record{
		ID: mustID(t), ActorUserID: &actor, ActorRole: "sec_admin", Action: "role.bind", Result: "success",
		TargetType: "role_binding", TargetID: "rb-1", ClientID: "s10-audit",
		Before: map[string]any{"role": "viewer"}, After: map[string]any{"role": "admin"},
		IP: "203.0.113.7", TraceID: "trace-opt-1",
		OccurredAt: month.Add(time.Second),
	}); err != nil {
		t.Fatalf("Append(with actor): %v", err)
	}
	// ② 全空的"匿名/无快照"记录。
	if _, err := appendWithReanchor(chain, db, Record{
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
	if broken, err := chain.Verify(records, records[0].PrevTag); err != nil || broken != nil {
		t.Fatalf("两种形态都应验过，实际 broken=%+v err=%v", broken, err)
	}
}

func TestSameMonthMicrosecondRollbackIsAccepted(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)

	first := month.Add(10 * time.Second)
	second := first.Add(-time.Microsecond) // 微秒级回退，仍在本月内
	if _, err := appendWithReanchor(chain, db, Record{
		Action: "a", Result: "success", OccurredAt: first,
	}); err != nil {
		t.Fatalf("Append(first): %v", err)
	}
	if _, err := appendWithReanchor(chain, db, Record{
		Action: "b", Result: "success", OccurredAt: second,
	}); err != nil {
		t.Fatalf("同月内微秒级回退应被接受，实际 %v", err)
	}

	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(records))
	}
	// 两条 `occurred_at` 都原样保留（第二条更早，没被改写）。
	if !records[0].OccurredAt.Equal(first) || !records[1].OccurredAt.Equal(second) {
		t.Fatalf("occurred_at 被改写：%s / %s（期望 %s / %s）",
			records[0].OccurredAt, records[1].OccurredAt, first, second)
	}
	// 链按 `created_at` 序仍然完整。
	if broken, err := chain.Verify(records, records[0].PrevTag); err != nil || broken != nil {
		t.Fatalf("链应完整，实际 broken=%+v err=%v", broken, err)
	}
}

func TestAnchorMismatchBreaksAtFirstRecord(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)
	_, _ = appendN(t, chain, db, month, 3)

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
	// 锚点语义（P0 修复后）：`anchor[M]` 是本月的**封条**（链尾 tag），所以错锚在**段尾**暴露，
	// 而不是首条——首条只看 `anchor[M-1]`（上月封条），与本用例无关。
	if res.Broken == nil || res.Broken.Index != 2 || res.Broken.Reason != ReasonTagMismatch {
		t.Fatalf("错锚点应在段尾（index=2, integrity_tag_mismatch）暴露，实际 %+v", res.Broken)
	}
}
