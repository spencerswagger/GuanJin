//go:build integration && audit_serial

package audit

// 本文件是 **Owl II P0（锚点键语义相反）的回归用例**，由测试侧先写"红用例"，
// 产品代码（`verify.go`）由实现方在同一分支上修。
//
// ## 缺陷形态（main `d1ef135`）
//
//   - `RunRange`：`anchor := anchors.Get(monthOf(from))` → `chain.Verify(records, anchor)`，
//     即把锚点当作**本段首条记录应有的 `prev_tag`**；
//   - `AnchorCurrentMonth`：`anchors.Put(from, last.IntegrityTag)`，
//     即写入的是**本月链尾记录自己的 `integrity_tag`**。
//
// 两者语义相反：封锚之后，同一月再跑一次复核，第 0 条的 `prev_tag`（= 上段链尾 tag 或 nil）
// 必然 ≠ 本月链尾 tag → 恒报 `prev_tag_mismatch`，`audit_chain_broken_total` 每天 +1，
// `anchor_age` 停在旧值 → **真断链被噪声淹没，校验 Job 等于失效**。
//
// ## 断言口径
//
// "锚点键的语义是唯一的"：不管实现选哪种语义（首条 prev_tag 还是段尾 integrity_tag），
// **写锚点的一方与读锚点复核的一方必须一致**。本用例只断言外部可观察行为：
//   ① 封锚后同一月再复核 → 不报断链；
//   ② `audit_chain_broken_total` 不递增；
//   ③ `anchor_age` 的基准（`lastVerifiedUnixSec`）被刷新。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestAnchorContinuitySameMonth 封锚后同一月再次复核必须通过（P0 回归）。
func TestAnchorContinuitySameMonth(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	month = requirePartition(t, db, month)
	cleanupMonth(t, db, month)

	ctx := context.Background()
	marker := "anchor-continuity-" + uuid.NewString()[:8]
	for i := 0; i < 3; i++ {
		if _, err := chain.Append(ctx, db, Record{
			Action:     "anchor-continuity",
			Result:     "success",
			TargetID:   fmt.Sprintf("%s-%d", marker, i),
			OccurredAt: month.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	anchors := NewAnchorStore(db)
	verifier, err := NewVerifier(chain, db, anchors, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	// 复核"当月"= 刚写入的那一月。
	verifier.now = func() time.Time { return month.Add(30 * time.Minute) }

	// ① 封锚（Job 在整链通过后调用，等价于"当天第一次 RunOnce 成功"）。
	//
	// 这里不先跑一次 RunOnce：main 上"该月无锚点"时会拿 nil 当"本段首条 prev_tag 应为空"，
	// 对非首月**同样**报 prev_tag_mismatch —— 那是同一根因的另一个面，
	// 但会让本用例在真正要断言的第二步之前就红，信息不聚焦。断言从这里开始。
	if err := verifier.AnchorCurrentMonth(ctx); err != nil {
		t.Fatalf("AnchorCurrentMonth: %v", err)
	}

	// ② 封锚后同一月复核（Job 下一次 tick 的等价场景）。

	// ③ 同一月第二次复核：锚点已存在，语义必须自洽 → 必须通过。
	ResetMetricsForTest()
	res2, err := verifier.RunOnce(ctx)
	if err != nil {
		t.Fatalf("第二次 RunOnce: %v", err)
	}
	if res2.Broken != nil {
		t.Fatalf("封锚后同一月再复核报了断链：index=%d reason=%s —— "+
			"锚点键语义不一致（`AnchorCurrentMonth` 写的是本月**链尾**的 integrity_tag，"+
			"而 `RunRange` 把它当成**本段首条**的 prev_tag）",
			res2.Broken.Index, res2.Broken.Reason)
	}
	if got := ChainBrokenTotal(); got != 0 {
		t.Fatalf("audit_chain_broken_total 不应递增，实际 %d（真断链会被噪声淹没）", got)
	}
	if lastVerifiedUnixSec.Load() == 0 {
		t.Fatalf("anchor_age 基准（lastVerifiedUnixSec）未被刷新：说明这次复核没有走到整链通过分支")
	}
}

// TestCanonicalEncodingUnified_TODO —— Owl II P2 ②：`canonical` 数字编码三套口径不统一。
//
// 现状：哈希用的 canonical 序列化在多处各自实现（审计链 / 描述符 / 签名侧），
// 对数字（int / int64 / float）的编码规则不一致；一旦首个生产写入方接入，
// 同一语义的记录会算出不同 `integrity_tag` → 链**自断**。
//
// TODO(实现方)：抽出唯一的 canonical 编码函数（或被 Spec 指定的单一实现），
// 三处调用点全部改为调用它；本用例断言"同一逻辑值的多种数字类型编码结果相同"。
func TestCanonicalEncodingUnified(t *testing.T) {
	// 同一逻辑数值的不同 Go 类型必须编出**同一个** canonical 字节串，
	// 否则"同值不同写法"会算出不同 integrity_tag → 接首个生产写入方时链自断（P2 ②）。
	c := testChain(t)
	base := Record{Action: "canon", Result: "success", OccurredAt: time.Unix(0, 0).UTC()}
	variants := []any{1, int64(1), float64(1), json.Number("1"), json.Number("1.0"), json.Number("1.00")}
	var first []byte
	for i, v := range variants {
		rec := base
		rec.Before = map[string]any{"n": v}
		got, err := c.Tag(nil, rec)
		if err != nil {
			t.Fatalf("variant %d: %v", i, err)
		}
		if i == 0 {
			first = got
			continue
		}
		if !bytesEqual(first, got) {
			t.Fatalf("数字 %#v 与 1 的 canonical 不一致（同值不同写法，链会自断）", v)
		}
	}
	// 不同数值必须编出不同结果（不能把 1 和 2 归一成同一个）。
	other := base
	other.Before = map[string]any{"n": 2}
	got2, err := c.Tag(nil, other)
	if err != nil {
		t.Fatalf("tag n=2: %v", err)
	}
	if bytesEqual(first, got2) {
		t.Fatal("1 与 2 的 canonical 相同 —— 归一化过头，链失去区分力")
	}
}

// TestAnchorContinuityAfterSameMonthAppend —— Owl II P0 复审抓到的**新假告警**回归。
//
// 场景（生产上必然发生）：Job 每一拍是「先 RunOnce 复核当月 → 再 AnchorCurrentMonth 写当月链尾」。
// 所以封锚之后同月**还会继续写入**审计事实。若段尾校验写成"段尾 tag == anchor[M]"（等值），
// 第二天复核就必然报 `integrity_tag_mismatch` —— 症状与本次要修的 P0 一模一样（只是 index 从 0 挪到末尾）。
//
// 正确口径：**存在性**判定 —— 段内必须找得到 `anchor[M]` 对应的那条记录；正常追加不影响，
// 而"整月尾部被删"照样能发现（见下一条用例）。
func TestAnchorContinuityAfterSameMonthAppend(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)

	ctx := context.Background()
	if _, marker := appendN(t, chain, db, month, 3); marker == "" {
		t.Fatal("appendN 未返回标记")
	}

	anchors := NewAnchorStore(db)
	verifier, err := NewVerifier(chain, db, anchors, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	verifier.now = func() time.Time { return month.Add(10 * time.Minute) }
	if err := verifier.AnchorCurrentMonth(ctx); err != nil {
		t.Fatalf("AnchorCurrentMonth: %v", err)
	}

	// 封锚之后同月继续写入（Job 下一拍之前，业务还在产生审计事实）。
	if _, marker := appendN(t, chain, db, month, 2); marker == "" {
		t.Fatal("appendN 未返回标记")
	}

	ResetMetricsForTest()
	verifier.now = func() time.Time { return month.Add(20 * time.Minute) }
	res, err := verifier.RunOnce(ctx)
	if err != nil {
		t.Fatalf("封锚后追加再复核：%v", err)
	}
	if res.Broken != nil {
		t.Fatalf("封锚后同月追加一条就报断链：index=%d reason=%s（broken_total=%d）—— "+
			"段尾校验用了等值判定，正常追加被当成篡改",
			res.Broken.Index, res.Broken.Reason, ChainBrokenTotal())
	}
	if got := ChainBrokenTotal(); got != 0 {
		t.Fatalf("audit_chain_broken_total 不应递增，实际 %d", got)
	}
	if lastVerifiedUnixSec.Load() == 0 {
		t.Fatal("anchor_age 基准未被刷新：复核没走到整链通过分支")
	}
}

// TestSealedMonthTailDeletedIsDetected —— 存在性判定的另一半：封条对应的记录被删，必须检出。
//
// 整月尾部被删时，段内**每一对 prev/tag 仍然自洽**（链看不出断），只有锚点能发现。
// 这也是段尾校验存在的唯一理由——所以它必须能红。
func TestSealedMonthTailDeletedIsDetected(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)

	ctx := context.Background()
	appendN(t, chain, db, month, 3)

	anchors := NewAnchorStore(db)
	verifier, err := NewVerifier(chain, db, anchors, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	verifier.now = func() time.Time { return month.Add(10 * time.Minute) }
	if err := verifier.AnchorCurrentMonth(ctx); err != nil {
		t.Fatalf("AnchorCurrentMonth: %v", err)
	}

	// 删掉封条对应的那条（本月最后一条）：链内 prev/tag 仍自洽，只有锚点对不上。
	records, err := chain.Load(ctx, db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) < 3 {
		t.Fatalf("本月记录不足（期望 ≥3，实际 %d）", len(records))
	}
	victim := records[len(records)-1]
	if err := db.Exec(fmt.Sprintf(`DELETE FROM audit_logs WHERE id = '%s'`, victim.ID)).Error; err != nil {
		t.Fatalf("delete tail: %v", err)
	}

	ResetMetricsForTest()
	res, err := verifier.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Broken == nil {
		t.Fatal("封条对应的记录被删却判绿 —— 尾部截断检测失效（AC-AUD-06）")
	}
	if res.Broken.Reason != ReasonAnchorMismatch {
		t.Fatalf("应报 anchor_mismatch（不是 integrity_tag_mismatch：剩下的记录内容没被改），实际 %s",
			res.Broken.Reason)
	}
	if got := ChainBrokenTotal(); got == 0 {
		t.Fatal("检出断链必须让 audit_chain_broken_total 递增")
	}
	if lastVerifiedUnixSec.Load() != 0 {
		t.Fatal("断链时不许刷新 anchor_age 基准（否则指标会把'没验过'显示成'刚验过'）")
	}
}
