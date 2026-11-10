//go:build integration && audit_serial

package audit

// 本文件钉住 **"审计误报：封锚之后再写一条，复核就报断链"** 的两个形态。
//
// ## 缺陷形态（main `3464010` 及之前）
//
// `RunRange` 把**上月锚点** `anchor[M-1]` 当作本段首条 `prev_tag` 的期望值，而
// `AnchorCurrentMonth` 是**每一拍**拍下的快照（= 当拍链尾的 `integrity_tag`）。审计事实是
// 持续写入的：
//
//   - **同月形态**（PR #76 / #80 已修）：封锚 → 同月再写一条 → 复核。已由
//     `TestAnchorContinuitySameMonth` 钉住（段尾改存在性判定）。
//   - **跨月形态**（本文件，仍红到本次修复）：月末最后一拍封锚 → 当月又写一条
//     → 次月首条记录 `prev_tag` 指向"封锚之后那条" → 与上月锚点不等
//     → 整个次月**每一拍**都报 `prev_tag_mismatch`（`audit_chain_broken_total` 每天 +1、
//     `audit_anchor_age_seconds` 停住 = 校验 Job 沦为噪声源）。
//
// ## 修法与断言口径
//
// 段首期望值改为**链上真实前驱**的 `integrity_tag`（`Chain.TailTagBefore`），锚点退回
// "封条 + 断点兜底"。本文件因此断言三件事，**缺一条就是修歪了**：
//   ① 封锚之后正常追加 → 同月/次月复核都**不报**断链（误报消失）；
//   ② 指标不递增、`anchor_age` 基准被刷新（Job 真跑到了"通过"分支）；
//   ③ **反向守线**：真把上月最后一条删掉 → 次月复核**必须**报 `prev_tag_mismatch`
//      （不许把判据放宽成"恒绿"——那等于关掉检测）。

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newTestVerifier(t *testing.T, chain *Chain, db *gorm.DB) (*Verifier, *AnchorStore) {
	t.Helper()
	anchors := NewAnchorStore(db)
	v, err := NewVerifier(chain, db, anchors, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v, anchors
}

func appendAt(t *testing.T, chain *Chain, db *gorm.DB, at time.Time, tag string) {
	t.Helper()
	if _, err := chain.Append(context.Background(), db, Record{
		Action: "anchor-head", Result: "success",
		TargetID:   tag + "-" + uuid.NewString()[:8],
		OccurredAt: at,
	}); err != nil {
		t.Fatalf("append %s @%s: %v", tag, at.UTC().Format(time.RFC3339), err)
	}
}

// TestAnchorHeadUsesRealPredecessor：段首判据必须用"真实前驱"，不许用"上月锚点快照"。
func TestAnchorHeadUsesRealPredecessor(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := requirePartition(t, db, freshMonth(t, db))
	cleanupMonth(t, db, month)
	next := requirePartition(t, db, month.AddDate(0, 1, 0))
	cleanupMonth(t, db, next)

	v, anchors := newTestVerifier(t, chain, db)
	ctx := context.Background()

	// ① 当月写两条 → 复核通过 → 封锚（等价于月末最后一拍）。
	appendAt(t, chain, db, month.Add(0*time.Minute), "m0")
	appendAt(t, chain, db, month.Add(1*time.Minute), "m1")
	v.now = func() time.Time { return month.Add(5 * time.Minute) }
	if res, err := v.RunOnce(ctx); err != nil || res.Broken != nil {
		t.Fatalf("前置：当月第一次复核应通过，实际 broken=%+v err=%v", res.Broken, err)
	}
	if err := v.AnchorCurrentMonth(ctx); err != nil {
		t.Fatalf("AnchorCurrentMonth: %v", err)
	}
	sealed, err := anchors.Get(ctx, month)
	if err != nil || len(sealed) == 0 {
		t.Fatalf("封锚失败：anchor=%x err=%v", sealed, err)
	}

	// ② 封锚**之后**当月又写一条（正常业务），次月再写一条（链接着它）。
	appendAt(t, chain, db, month.Add(50*time.Minute), "m2-after-seal")
	appendAt(t, chain, db, next.Add(1*time.Minute), "next0")

	// ③ 次月复核：段首应取"真实前驱"（= m2-after-seal 的 tag），不许拿 sealed 比对。
	ResetMetricsForTest()
	v.now = func() time.Time { return next.Add(10 * time.Minute) }
	res, err := v.RunOnce(ctx)
	if err != nil {
		t.Fatalf("次月 RunOnce: %v", err)
	}
	if res.Broken != nil {
		t.Fatalf("封锚后正常追加被误报成断链（index=%d reason=%s）：段首期望值仍在用上月锚点快照",
			res.Broken.Index, res.Broken.Reason)
	}
	if res.Checked != 1 {
		t.Fatalf("次月应复核 1 条，实际 %d", res.Checked)
	}
	if got := ChainBrokenTotal(); got != 0 {
		t.Fatalf("audit_chain_broken_total 不应递增，实际 %d（真断链会被噪声淹没）", got)
	}
	if lastVerifiedUnixSec.Load() == 0 {
		t.Fatal("anchor_age 基准未刷新：这次复核没走到「通过」分支")
	}
	// 段首期望值必须是"真实前驱"（m2-after-seal 的 tag），且**不等于** early 封条 sealed。
	if string(res.AnchorUsed) == string(sealed) {
		t.Fatal("AnchorUsed 仍是封锚快照：段首判据没有改到真实前驱")
	}
	lastOfMonth, err := chain.Load(ctx, db, month.Add(49*time.Minute), month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load(last of month): %v", err)
	}
	if len(lastOfMonth) != 1 || string(res.AnchorUsed) != string(lastOfMonth[0].IntegrityTag) {
		t.Fatalf("AnchorUsed 必须等于上月最后一条的 integrity_tag（期望 %x，实际 %x）",
			lastOfMonth[len(lastOfMonth)-1].IntegrityTag, res.AnchorUsed)
	}

	// ④ **反向守线**：真把上月最后一条删掉 → 次月复核必须报 prev_tag_mismatch。
	// 否则就是"把判据放宽成恒绿"（误报没了、检测也没了）。
	allowMutationForTest(t, db, month)
	if err := db.Exec(`DELETE FROM audit_logs WHERE id = ?`, lastOfMonth[0].ID).Error; err != nil {
		t.Fatalf("delete last of month: %v", err)
	}
	ResetMetricsForTest()
	res, err = v.RunOnce(ctx)
	if err != nil {
		t.Fatalf("删尾后 RunOnce: %v", err)
	}
	if res.Broken == nil || res.Broken.Reason != ReasonPrevMismatch {
		t.Fatalf("上月尾部被删必须以 %s 断链（次月首条 prev_tag 指向已不存在的 tag），实际 %+v",
			ReasonPrevMismatch, res.Broken)
	}
	if got := ChainBrokenTotal(); got != 1 {
		t.Fatalf("删尾后 audit_chain_broken_total 期望 1，实际 %d", got)
	}
}

// TestSameMonthRepeatVerifyNoFalseAlarm：同月"封锚 → 追加 → 复核"反复多次都必须通过。
//
// 与 `TestAnchorContinuitySameMonth` 互补：那条只跑**一次**第二次复核（而当时恰恰是
// "封锚后没有追加"的最宽松场景）；这里每一轮都**在封锚之后追加**，覆盖
// "段尾变了但封条还在段内"的真实运行形态。
func TestSameMonthRepeatVerifyNoFalseAlarm(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := requirePartition(t, db, freshMonth(t, db))
	cleanupMonth(t, db, month)

	v, _ := newTestVerifier(t, chain, db)
	ctx := context.Background()
	v.now = func() time.Time { return month.Add(30 * time.Minute) }

	appendAt(t, chain, db, month.Add(0*time.Minute), "s0")
	if res, err := v.RunOnce(ctx); err != nil || res.Broken != nil {
		t.Fatalf("第一次复核应通过，实际 broken=%+v err=%v", res.Broken, err)
	}
	if err := v.AnchorCurrentMonth(ctx); err != nil {
		t.Fatalf("AnchorCurrentMonth: %v", err)
	}

	ResetMetricsForTest()
	for round := 1; round <= 3; round++ {
		appendAt(t, chain, db, month.Add(time.Duration(10*round)*time.Minute), fmt.Sprintf("r%d", round))
		res, err := v.RunOnce(ctx)
		if err != nil {
			t.Fatalf("第 %d 轮 RunOnce: %v", round, err)
		}
		if res.Broken != nil {
			t.Fatalf("第 %d 轮（封锚后追加再复核）误报断链：index=%d reason=%s",
				round, res.Broken.Index, res.Broken.Reason)
		}
		if err := v.AnchorCurrentMonth(ctx); err != nil {
			t.Fatalf("第 %d 轮 AnchorCurrentMonth: %v", round, err)
		}
	}
	if got := ChainBrokenTotal(); got != 0 {
		t.Fatalf("三轮追加+复核后 audit_chain_broken_total 应为 0，实际 %d", got)
	}
}

// TestAnchorHeadDetectsSealedTailDeletion 是 **Owl 事后审 P1-1 的守线**：
// 补的是"新口径相对旧口径的**增益**"，而不是"不恒绿"。
//
// 形态（Owl A/B 里的那一种）：
//  1. 上月写 2 条 → 复核通过 → 封锚，于是 **`anchor[M-1]` 就是上月最后一条的 tag**；
//  2. 封锚之后**不再追加**任何上月记录（这正是旧口径的盲区条件）；
//  3. 次月写 1 条（它的 `prev_tag` 指向上月最后一条）；
//  4. **删掉**上月最后一条。
//
// 两种口径的行为分叉：
//   - 旧口径（段首期望值取 `anchor[M-1]`）：期望值仍是那条被删记录的 tag，而次月首条的
//     `prev_tag` 也仍指向它 → 两者**相等** → `broken=nil` → **漏检**（篡改被放过）；
//   - 新口径（段首期望值取**链上真实前驱**）：前驱已经退化成被删记录的前一条 → 不等 →
//     报 `prev_tag_mismatch`。
//
// 因此这条用例**在旧树上必红**（旧树 `RunOnce` 返回 `broken=nil`，本用例断言"必须报断链"失败）。
// 写进 PR 是为了让后来人能复算这个增益，不必再猜"为什么段首要用真实前驱"。
func TestAnchorHeadDetectsSealedTailDeletion(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := requirePartition(t, db, freshMonth(t, db))
	cleanupMonth(t, db, month)
	next := requirePartition(t, db, month.AddDate(0, 1, 0))
	cleanupMonth(t, db, next)

	v, anchors := newTestVerifier(t, chain, db)
	ctx := context.Background()

	// ① 上月两条 → 复核通过 → 封锚（封的就是最后一条 = `m1`）。
	appendAt(t, chain, db, month.Add(0*time.Minute), "m0")
	appendAt(t, chain, db, month.Add(1*time.Minute), "m1")
	v.now = func() time.Time { return month.Add(5 * time.Minute) }
	if res, err := v.RunOnce(ctx); err != nil || res.Broken != nil {
		t.Fatalf("前置：上月复核应通过，实际 broken=%+v err=%v", res.Broken, err)
	}
	if err := v.AnchorCurrentMonth(ctx); err != nil {
		t.Fatalf("AnchorCurrentMonth: %v", err)
	}
	tailOfMonth, err := chain.Load(ctx, db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load(month): %v", err)
	}
	if len(tailOfMonth) != 2 {
		t.Fatalf("前置：上月应 2 条，实际 %d", len(tailOfMonth))
	}
	sealed := tailOfMonth[len(tailOfMonth)-1]
	anchor, err := anchors.Get(ctx, month)
	if err != nil || len(anchor) == 0 {
		t.Fatalf("封锚失败：anchor=%x err=%v", anchor, err)
	}
	if string(anchor) != string(sealed.IntegrityTag) {
		t.Fatal("前置：锚点必须等于上月最后一条的 integrity_tag（旧口径盲区的前提）")
	}

	// ② 封锚后**不追加**上月记录；次月写 1 条，`prev_tag` 指向那条被封的尾巴。
	appendAt(t, chain, db, next.Add(1*time.Minute), "next0")

	// ③ 删掉"被封的那条"（= 上月最后一条）。
	allowMutationForTest(t, db, month)
	if err := db.Exec(`DELETE FROM audit_logs WHERE id = ?`, sealed.ID).Error; err != nil {
		t.Fatalf("delete sealed tail: %v", err)
	}

	// ④ 次月复核：必须报断链（旧口径这里会 `broken=nil` 放过）。
	ResetMetricsForTest()
	v.now = func() time.Time { return next.Add(10 * time.Minute) }
	res, err := v.RunOnce(ctx)
	if err != nil {
		t.Fatalf("次月 RunOnce: %v", err)
	}
	if res.Broken == nil {
		t.Fatal("封锚尾巴被删后次月复核仍报通过 —— 段首判据没走真实前驱（旧口径的漏检形态）")
	}
	if res.Broken.Reason != ReasonPrevMismatch {
		t.Fatalf("应以 %s 断链（次月首条 prev_tag 指向已不存在的 tag），实际 %s",
			ReasonPrevMismatch, res.Broken.Reason)
	}
	if got := ChainBrokenTotal(); got != 1 {
		t.Fatalf("audit_chain_broken_total 期望 1，实际 %d", got)
	}
}

// TestAnchorHeadHandlesCreatedAtTie 是 **Owl 事后审 P1-2 的回归用例**：链序是 `(created_at, id)`
// 两列，而 `created_at` 没有唯一约束——只要出现**并列**，按单列 `created_at < ?` 取前驱就会
// 把"同 created_at、id 更小"的那条漏掉，期望值落到更早的一条 → `prev_tag_mismatch` **假告警**。
//
// 并列从哪来：`Append` 自己永远抬 `created_at`（链尾+1µs），所以只有**非 `Append` 写入**
// （回灌 / 归档恢复 / 批量导入）才可能造出并列。这里用直接 INSERT 精确复刻那个形态：
//   - R1 属上月（`occurred_at` 在上月）；
//   - R2 属次月，`created_at` **与 R1 完全相同**，且 `id` 更大（链序上排在 R1 之后）；
//   - R2.`prev_tag` = R1 的 tag（这就是真实链序）。
//
// 单列口径：`created_at < R2.created_at` 把 R1 排除掉 → 前驱退到更早的记录 → 报断链（假告警）；
// 行值口径：`(created_at, id) < (R1.created_at, R1.id)` → 命中 R1 → 通过。
func TestAnchorHeadHandlesCreatedAtTie(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := requirePartition(t, db, freshMonth(t, db))
	cleanupMonth(t, db, month)
	next := requirePartition(t, db, month.AddDate(0, 1, 0))
	cleanupMonth(t, db, next)

	v, _ := newTestVerifier(t, chain, db)
	ctx := context.Background()
	tieAt := month.Add(10 * time.Hour) // 两条记录共用的 created_at

	// id 固定且**可比较**：R1.id < R2.id（不依赖 uuid v7 的随机位）。
	idA := uuid.MustParse("018f0000-0000-7000-8000-00000000000a")
	idB := uuid.MustParse("018f0000-0000-7000-8000-00000000000b")

	r1 := Record{ID: idA, Action: "tie.import", Result: "success", TargetID: "tie-a",
		OccurredAt: month.Add(30 * time.Minute)}
	tag1, err := chain.Tag(nil, r1)
	if err != nil {
		t.Fatalf("Tag(r1): %v", err)
	}
	// 直接 INSERT：绕开 `Append`（它就是要把 created_at 抬成不并列，见文件头）。
	if err := db.Exec(`INSERT INTO audit_logs
			(id, action, result, target_id, integrity_tag, prev_tag, occurred_at, created_at)
			VALUES (?, 'tie.import', 'success', 'tie-a', ?, NULL, ?, ?)`,
		idA, tag1, r1.OccurredAt.UTC(), tieAt.UTC()).Error; err != nil {
		t.Fatalf("插入 R1（上月，回灌形态）：%v", err)
	}

	r2 := Record{ID: idB, Action: "tie.import", Result: "success", TargetID: "tie-b",
		OccurredAt: next.Add(5 * time.Minute), PrevTag: tag1}
	tag2, err := chain.Tag(tag1, r2)
	if err != nil {
		t.Fatalf("Tag(r2): %v", err)
	}
	if err := db.Exec(`INSERT INTO audit_logs
			(id, action, result, target_id, integrity_tag, prev_tag, occurred_at, created_at)
			VALUES (?, 'tie.import', 'success', 'tie-b', ?, ?, ?, ?)`,
		idB, tag2, tag1, r2.OccurredAt.UTC(), tieAt.UTC()).Error; err != nil {
		t.Fatalf("插入 R2（次月，与 R1 同 created_at）：%v", err)
	}

	ResetMetricsForTest()
	v.now = func() time.Time { return next.Add(30 * time.Minute) }
	res, err := v.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Broken != nil {
		t.Fatalf("created_at 并列时把正常链报成断链（index=%d reason=%s）：段首前驱必须按 (created_at, id) 取",
			res.Broken.Index, res.Broken.Reason)
	}
	if got := ChainBrokenTotal(); got != 0 {
		t.Fatalf("audit_chain_broken_total 应为 0，实际 %d", got)
	}
	if len(res.AnchorUsed) == 0 {
		t.Fatal("段首期望值应取到 R1 的 tag（而非退化成 nil/未知）")
	}
	if string(res.AnchorUsed) != string(tag1) {
		t.Fatalf("段首期望值必须是 R1 的 tag（并列里的前一条）：期望 %x 实际 %x", tag1, res.AnchorUsed)
	}
}
