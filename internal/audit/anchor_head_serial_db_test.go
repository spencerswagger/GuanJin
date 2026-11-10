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
