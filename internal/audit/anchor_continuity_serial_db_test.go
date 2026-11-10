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
	"errors"
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

// TestOccurredAtUpperBoundGuard_TODO —— Owl II P2 ①：`occurred_at` 缺**上界守卫**。
//
// 现状：`Append` 只拒"早于链尾所在月"的写入（下界），不拒"晚于当月"的写入（上界）。
// 后果：往**未来月**写入会让当月封锚的段出现"空洞"（跨月分段时该段不连续），
// 且锚点对不上——与 P0 叠加时更难定位。
//
// TODO(实现方)：补上界守卫（`occurred_at` 不得晚于当前自然月 + 允许的时钟偏移），
// 并在此用例里断言"未来月写入被拒 + 错误可识别（错误码/前缀稳定）"。
func TestOccurredAtUpperBoundGuard(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	month = requirePartition(t, db, month)
	cleanupMonth(t, db, month)
	ctx := context.Background()

	// 正常写入（链尾月 + 1 之内）→ 通过。
	if _, err := chain.Append(ctx, db, Record{
		Action: "upper-bound", Result: "success",
		TargetID: "ub-" + uuid.NewString()[:8], OccurredAt: month.Add(time.Minute),
	}); err != nil {
		t.Fatalf("链尾月内写入应通过：%v", err)
	}
	// 往"链尾月 + 2 个月"写 → 必须被上界守卫拒（否则未来月会在封锚前掺入，锚点对不上）。
	_, err := chain.Append(ctx, db, Record{
		Action: "upper-bound", Result: "success",
		TargetID: "ub-" + uuid.NewString()[:8], OccurredAt: month.AddDate(0, 2, 0),
	})
	if err == nil {
		t.Fatal("未来两个月写入必须被上界守卫拒绝（P2 ①）")
	}
	if !errors.Is(err, ErrOccurredAtTooNew) {
		t.Fatalf("错误必须是 ErrOccurredAtTooNew（可识别），实际 %v", err)
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
