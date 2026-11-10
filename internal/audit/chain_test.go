package audit

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// 本文件是哈希链**纯计算**的单测（AC-AUD-03/05 的"能检出"那一半）：
// 断链、篡改、乱序、删中间一条，全部用假记录构造；真库用例只验"SQL/并发按预期跑"。

func testChain(t *testing.T) *Chain {
	t.Helper()
	master, err := crypto.NewMasterKey(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 7)
	}
	master, err = crypto.NewMasterKey(raw)
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

// buildChain 造一段链（n 条，内容可被 mutate 改）。
func buildChain(t *testing.T, c *Chain, n int, mutate func(i int, r *Record)) ([]Record, error) {
	t.Helper()
	out := make([]Record, 0, n)
	var prev []byte
	for i := 0; i < n; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		rec := Record{
			ID: id, Action: "user.disable", Result: "success",
			ActorRole: "sec_admin", TargetType: "user", TargetID: id.String(),
			ClientID: "billing", TraceID: "trace-" + id.String()[:8],
			Before:     map[string]any{"status": "active", "n": json.Number("1")},
			After:      map[string]any{"status": "disabled", "n": json.Number("2")},
			OccurredAt: time.Unix(1700000000+int64(i), 0).UTC(),
		}
		if mutate != nil {
			mutate(i, &rec)
		}
		tag, err := c.Tag(prev, rec)
		if err != nil {
			return nil, err
		}
		rec.PrevTag = prev
		rec.IntegrityTag = tag
		out = append(out, rec)
		prev = tag
	}
	return out, nil
}

// 干净链必须整体通过（否则链会在"没有任何篡改"时自己断）。
func TestVerifyCleanChain(t *testing.T) {
	c := testChain(t)
	records, err := buildChain(t, c, 10, nil)
	if err != nil {
		t.Fatalf("buildChain: %v", err)
	}
	if broken, err := c.Verify(records, nil); err != nil || broken != nil {
		t.Fatalf("干净链应通过，实际 broken=%v err=%v", broken, err)
	}
}

// AC-AUD-03：**删中间一条** → 后续 `prev_tag` 对不上，必须能定位到第一个断点。
func TestVerifyDetectsDeletedMiddleRecord(t *testing.T) {
	c := testChain(t)
	records, err := buildChain(t, c, 5, nil)
	if err != nil {
		t.Fatalf("buildChain: %v", err)
	}
	tampered := append([]Record{}, records[:2]...)
	tampered = append(tampered, records[3:]...) // 删掉第 3 条（index 2）

	broken, err := c.Verify(tampered, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if broken == nil {
		t.Fatal("删中间一条必须断链")
	}
	if broken.Reason != ReasonPrevMismatch {
		t.Fatalf("原因期望 %s，实际 %s", ReasonPrevMismatch, broken.Reason)
	}
	if broken.RecordID != records[3].ID {
		t.Fatalf("断点应在被删后的第一条（%s），实际 %s", records[3].ID, broken.RecordID)
	}
}

// AC-AUD-05：**篡改一行内容**（不改 tag）→ tag 重算不符，必须断。
func TestVerifyDetectsTamperedContent(t *testing.T) {
	c := testChain(t)
	records, err := buildChain(t, c, 5, nil)
	if err != nil {
		t.Fatalf("buildChain: %v", err)
	}
	records[2].After = map[string]any{"status": "active"} // 把"已禁用"改回"启用"

	broken, err := c.Verify(records, nil)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if broken == nil || broken.Reason != ReasonTagMismatch {
		t.Fatalf("篡改内容必须以 %s 断链，实际 %+v", ReasonTagMismatch, broken)
	}
	if broken.Index != 2 {
		t.Fatalf("断点应在 index 2，实际 %d", broken.Index)
	}
}

// 乱序（把两条换位）也必须断：链的顺序就是时间顺序，换位等于改历史。
func TestVerifyDetectsReordering(t *testing.T) {
	c := testChain(t)
	records, err := buildChain(t, c, 4, nil)
	if err != nil {
		t.Fatalf("buildChain: %v", err)
	}
	records[1], records[2] = records[2], records[1]
	if broken, err := c.Verify(records, nil); err != nil || broken == nil {
		t.Fatalf("乱序必须断链，实际 broken=%v err=%v", broken, err)
	}
}

// 锚点接续：分段起点用锚点作为 `prev_tag`；锚点错则首条即断（这是"跨月分段"的正确性基础）。
func TestVerifyUsesAnchorForSegmentStart(t *testing.T) {
	c := testChain(t)
	records, err := buildChain(t, c, 3, nil)
	if err != nil {
		t.Fatalf("buildChain: %v", err)
	}
	// 正确锚点 = 上一段链尾（这里用 records[0].PrevTag 代表"上一段最后一条的 tag"）。
	anchor := records[0].PrevTag
	if broken, err := c.Verify(records, anchor); err != nil || broken != nil {
		t.Fatalf("锚点接续应通过，实际 broken=%v err=%v", broken, err)
	}
	// 错锚点（改 1 字节）→ 首条即断。
	bad := append([]byte{}, anchor...)
	if len(bad) == 0 {
		bad = []byte{0xFF}
	} else {
		bad[0] ^= 0xFF
	}
	broken, err := c.Verify(records, bad)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if broken == nil || broken.Index != 0 {
		t.Fatalf("错锚点应在首条即断，实际 %+v", broken)
	}
}

// 规范化必须**逐字节确定**：同一条记录算两次同一个 tag；map 键顺序不影响结果。
func TestCanonicalIsDeterministicAndOrderInsensitive(t *testing.T) {
	c := testChain(t)
	id := uuid.MustParse("0193f0aa-0000-7000-8000-000000000001")
	base := Record{
		ID: id, Action: "policy.update", Result: "success", OccurredAt: time.Unix(1700000000, 0).UTC(),
		Before: map[string]any{"b": 2, "a": 1},
		After:  map[string]any{"z": []any{"x", "y"}},
	}
	tag1, err := c.Tag(nil, base)
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	// 换一个 map 插入顺序（Go 的 map 本身无序，这里显式重建以证明与插入顺序无关）。
	base.Before = map[string]any{"a": 1, "b": 2}
	tag2, err := c.Tag(nil, base)
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if !bytesEqual(tag1, tag2) {
		t.Fatal("同一内容必须算出同一个 tag（map 顺序不应影响）")
	}

	// 有 `before` 与 `before=nil` 必须算出不同 tag（presence 标记的意义）。
	withNil := base
	withNil.Before = nil
	tag3, err := c.Tag(nil, withNil)
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if bytesEqual(tag1, tag3) {
		t.Fatal("`before=nil` 与 `before={...}` 不能算出同一个 tag")
	}
}

// 键校验：零键必须拒（否则链用一个"空密钥"签，等于没签）。
func TestNewChainRejectsZeroKey(t *testing.T) {
	if _, err := NewChain(crypto.HMACKey{}); err == nil {
		t.Fatal("零键必须报错")
	}
}

// 断链错误的文案要能定位（record id + index + reason），否则排障只能全表扫。
func TestBrokenPointErrorCarriesLocation(t *testing.T) {
	b := &BrokenPoint{Index: 7, RecordID: uuid.MustParse("0193f0aa-0000-7000-8000-0000000000ff"), Reason: ReasonTagMismatch}
	msg := b.Error()
	for _, want := range []string{"0193f0aa", "index 7", ReasonTagMismatch} {
		if !strings.Contains(msg, want) {
			t.Fatalf("断链文案缺 %q：%s", want, msg)
		}
	}
	if !errors.Is(b, ErrChainBroken) {
		t.Fatal("BrokenPoint 必须能用 errors.Is 归到 ErrChainBroken")
	}
}

// 锚/分段的键名必须按月（D4-C）：跨月换键，链不会跨月混写。
func TestAnchorAndSegmentKeys(t *testing.T) {
	at := time.Date(2026, 11, 10, 3, 4, 5, 0, time.UTC)
	if got := AnchorKey(at); got != "audit.chain.anchor.202611" {
		t.Fatalf("锚点键不符：%q", got)
	}
	if got := SegmentKey(at); got != "audit.chain.segment.202611" {
		t.Fatalf("分段键不符：%q", got)
	}
}

// canonical 的类型分支必须全覆盖：bool / nil / 数字两种形态 / 未知类型兜底。
// 漏一个分支的后果是"那种类型的字段被改"检不出来（tag 不变）。
func TestCanonicalCoversValueKinds(t *testing.T) {
	c := testChain(t)
	id := uuid.MustParse("0193f0aa-0000-7000-8000-000000000002")
	at := time.Unix(1700000000, 0).UTC()
	mk := func(v any) Record {
		return Record{ID: id, Action: "a", Result: "success", OccurredAt: at,
			After: map[string]any{"k": v}}
	}

	tags := map[string][]byte{}
	for name, rec := range map[string]Record{
		"bool-true":  mk(true),
		"bool-false": mk(false),
		"nil":        mk(nil),
		"jsonNumber": mk(json.Number("42")),
		"float64":    mk(float64(1.5)),
		"string":     mk("x"),
		"array":      mk([]any{"a", 1, true, nil}),
		"unknown":    mk(struct{ A int }{A: 1}),
	} {
		tag, err := c.Tag(nil, rec)
		if err != nil {
			t.Fatalf("%s: Tag: %v", name, err)
		}
		tags[name] = tag
	}
	// 两两不同：bool-true / bool-false / nil 绝不能被压成同一个 tag。
	seen := map[string]string{}
	for name, tag := range tags {
		key := string(tag)
		if prev, dup := seen[key]; dup {
			t.Fatalf("%s 与 %s 算出了同一个 tag（类型/取值没被区分）", name, prev)
		}
		seen[key] = name
	}
}

// nil chain / nil broken point 的边界必须有确定行为（不许 panic）。
func TestAuditNilSafety(t *testing.T) {
	var c *Chain
	if _, err := c.Tag(nil, Record{}); err == nil {
		t.Fatal("nil chain 必须报错")
	}
	if _, err := c.Verify(nil, nil); err == nil {
		t.Fatal("nil chain 的 Verify 必须报错")
	}
	var b *BrokenPoint
	if b.Error() != "" {
		t.Fatal("nil BrokenPoint 的 Error 应为空串")
	}
	if !errors.Is(&BrokenPoint{}, ErrChainBroken) {
		t.Fatal("BrokenPoint 必须归到 ErrChainBroken")
	}
}

// NewID 必须是 UUIDv7（与应用主键同一生成器），两次不同。
func TestNewIDIsV7AndUnique(t *testing.T) {
	a, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	b, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if a == b {
		t.Fatal("两次 NewID 不该相同")
	}
	if a.Version() != 7 {
		t.Fatalf("期望 UUIDv7，实际 version=%d", a.Version())
	}
}

// 空链（0 条）必须通过——月内还没有审计事实是正常状态，不是"断链"。
func TestVerifyEmptyChainPasses(t *testing.T) {
	c := testChain(t)
	if broken, err := c.Verify(nil, nil); err != nil || broken != nil {
		t.Fatalf("空链应通过，实际 broken=%v err=%v", broken, err)
	}
}
