package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/logger"
)

// 本文件补的是 P0 修复（锚点语义）与 P2 ②（数字 canonical 统一）**新引入分支**的单元证据。
// 真库语义（封锚后同月复核）在 `anchor_continuity_serial_db_test.go`；这里只钉纯计算分支，
// 目的是让"新增分支没被任何用例走过"这件事在 unit profile 里也暴露出来。

// 段首 `prev_tag` 未知（上月没有锚点）时：不判 prev，但仍判内容（改内容照样要发现）。
func TestVerifyFromUnknownPrevStillChecksContent(t *testing.T) {
	c := testChain(t)
	base := time.Unix(1_700_000_000, 0).UTC()

	// 段首 PrevTag 故意写一个"上月链尾"值（本段看不到它，属于未知）；后续照常接龙。
	unknownPrev := []byte("last-month-seal")
	var prev []byte = unknownPrev
	recs := make([]Record, 3)
	for i := range recs {
		rec := Record{
			ID:         uuid.New(),
			Action:     "verify-from",
			Result:     "success",
			OccurredAt: base.Add(time.Duration(i) * time.Second),
			PrevTag:    prev,
		}
		tag, err := c.Tag(prev, rec)
		if err != nil {
			t.Fatalf("Tag: %v", err)
		}
		rec.IntegrityTag = tag
		recs[i] = rec
		prev = tag
	}

	// ① 判首条 prev（checkFirst=true）→ 必断（这正是 P0 的旧行为）。
	if bp, err := c.VerifyFrom(recs, nil, true); err != nil || bp == nil || bp.Index != 0 ||
		bp.Reason != ReasonPrevMismatch {
		t.Fatalf("checkFirst=true 时应在首条报 prev_tag_mismatch，实际 bp=%+v err=%v", bp, err)
	}
	// ② 不判首条 prev（checkFirst=false）→ 通过（段首 prev 属"未知"，不是"应为 nil"）。
	if bp, err := c.VerifyFrom(recs, nil, false); err != nil || bp != nil {
		t.Fatalf("checkFirst=false 时不应报断链，实际 bp=%+v err=%v", bp, err)
	}

	// ③ 但 checkFirst=false 仍必须按内容重算 tag：只改内容不改 tag → 必断。
	tampered := make([]Record, len(recs))
	copy(tampered, recs)
	tampered[0].Before = map[string]any{"amount": 1}
	bp, err := c.VerifyFrom(tampered, nil, false)
	if err != nil || bp == nil || bp.Index != 0 || bp.Reason != ReasonTagMismatch {
		t.Fatalf("段首内容被改必须报 integrity_tag_mismatch，实际 bp=%+v err=%v", bp, err)
	}

	// ④ 内容无法规范化（chan 不可 JSON 序列化）→ 报错，不许当成"通过"。
	bad := make([]Record, len(recs))
	copy(bad, recs)
	bad[0].Before = map[string]any{"ch": make(chan int)}
	if _, err := c.VerifyFrom(bad, nil, false); err == nil ||
		!strings.Contains(err.Error(), "canonical") {
		t.Fatalf("不可序列化内容必须报 canonical 错误，实际 %v", err)
	}
}

// canonical 的数字口径：超出 float64 范围的 `json.Number` 退回原文（仍然确定）。
func TestCanonicalHugeNumberIsDeterministic(t *testing.T) {
	c := testChain(t)
	base := Record{ID: uuid.New(), Action: "canon", Result: "success", OccurredAt: time.Unix(0, 0).UTC()}

	rec := base
	rec.Before = map[string]any{"n": json.Number("1e400")} // Float64() 会失败 → 走原文分支
	first, err := c.Tag(nil, rec)
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	again, err := c.Tag(nil, rec)
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if !bytesEqual(first, again) {
		t.Fatal("同一超范围数字两次编码必须一致（确定性是链的前提）")
	}
	// 与普通可解析值必须不同（不能全都兜成同一个串）。
	other := base
	other.Before = map[string]any{"n": json.Number("1")}
	otherTag, err := c.Tag(nil, other)
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if bytesEqual(first, otherTag) {
		t.Fatal("超范围数字与 1 编出了同一个 tag —— 兜底分支把值抹平了")
	}

	// Before 不可序列化时 canonical 必须报错（不许静默产出一个空串去算 tag）。
	bad := base
	bad.Before = map[string]any{"ch": make(chan int)}
	if _, err := c.Tag(nil, bad); err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("不可序列化的 Before 必须报 canonical 错误，实际 %v", err)
	}
}

// Job 在依赖不可用时：RunOnce 报错 → 打 warn 后继续下一拍（不 panic、不退出循环）。
// 顺便钉住"锚点写失败也只 warn"这条分支：两个错误都不许把 ticker 打死。
func TestStartJobSurvivesDependencyFailure(t *testing.T) {
	c := testChain(t)
	dep := unreachableDB(t)
	dep.Logger = dep.Logger.LogMode(logger.Silent)
	v, err := NewVerifier(c, dep, NewAnchorStore(dep), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := StartJob(ctx, v, 5*time.Millisecond, nil)
	time.Sleep(40 * time.Millisecond)
	stop()
	cancel()
	time.Sleep(20 * time.Millisecond)
}

// AnchorCurrentMonth 在扫表失败时必须把错误抛出来（不许"当月没记录"与"库挂了"混为一谈）。
func TestAnchorCurrentMonthSurfacesDBError(t *testing.T) {
	c := testChain(t)
	dep := unreachableDB(t)
	dep.Logger = dep.Logger.LogMode(logger.Silent)
	v, err := NewVerifier(c, dep, NewAnchorStore(dep), nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if err := v.AnchorCurrentMonth(context.Background()); err == nil {
		t.Fatal("库不可用时 AnchorCurrentMonth 必须报错")
	}
}

// canonical 的大整数精度（Owl II P1）：>2^53 的不同整数**不许**被抹成同一个 tag。
//
// 旧实现一律 `float64` 中转：`9007199254740993` 与 `…992` 会编成同一串 →
// `before/after` 里出现雪花 ID 时，"改 ID 不改 tag"也能验过，防篡改强度被静默削弱。
func TestCanonicalBigIntegersKeepPrecision(t *testing.T) {
	c := testChain(t)
	base := Record{ID: uuid.New(), Action: "canon", Result: "success", OccurredAt: time.Unix(0, 0).UTC()}
	tagOf := func(v any) []byte {
		rec := base
		rec.Before = map[string]any{"id": v}
		tag, err := c.Tag(nil, rec)
		if err != nil {
			t.Fatalf("Tag(%#v): %v", v, err)
		}
		return tag
	}

	distinct := []struct {
		a, b any
	}{
		{int64(9007199254740993), int64(9007199254740992)},
		{int64(1) << 62, (int64(1) << 62) + 1},
		{json.Number("12345678901234567890"), json.Number("12345678901234567891")},
		{uint64(18446744073709551615), uint64(18446744073709551614)},
	}
	for _, p := range distinct {
		if bytesEqual(tagOf(p.a), tagOf(p.b)) {
			t.Fatalf("%#v 与 %#v 编出了同一个 tag —— 大整数丢精度，篡改验不出来", p.a, p.b)
		}
	}

	// 但"同值不同写法"必须仍然归一到同一个 tag（P2 ② 的原始目的）。
	//
	// 取值刻意**跨过 `1e6` 分界**（Owl II 复审指出：只测 1 会掩盖方向——
	// 1 在 `FormatInt` 与 `FormatFloat('g')` 下恰好同串，`1234567` 才是分水岭）。
	for _, v := range []float64{1, 1000000, 1234567, 1e8, 1762900000000} {
		i := int(v)
		i64 := int64(v)
		u64 := uint64(v)
		j := json.Number(strconv.FormatFloat(v, 'f', -1, 64)) // "1000000" / "1234567"
		ref := tagOf(i)
		for _, one := range []struct {
			name string
			v    any
		}{
			{"int64", i64}, {"uint", uint(v)}, {"uint64", u64}, {"float64", v},
			{"json.Number 定点", j},
		} {
			if !bytesEqual(ref, tagOf(one.v)) {
				t.Fatalf("同值不同写法 %s(%v) 与 int(%d) 的 canonical 不一致（写入/读回会自断）",
					one.name, one.v, i)
			}
		}
	}
	// 小值那几种也照旧（`1`、`1.0`、`1.00`）。
	for _, v := range []any{int(1), int64(1), uint(1), uint64(1), float64(1), json.Number("1"), json.Number("1.0"), json.Number("1.00")} {
		if !bytesEqual(tagOf(int(1)), tagOf(v)) {
			t.Fatalf("同值不同写法 %#v 与 1 的 canonical 不一致", v)
		}
	}
	// 非整数必须保持小数语义（不许被整型分支截断）。
	if bytesEqual(tagOf(1.5), tagOf(1)) {
		t.Fatal("1.5 与 1 编出了同一个 tag —— 小数被截断")
	}
}

// TestCanonicalWriteReadAgree —— 属性断言：**写侧的值**与**读回来的 JSON 值**必须算同一个 tag。
//
// 这条是前三轮来回的收口：单点用例总会漏掉某个量级。这里把"写侧 Go 值"与"读侧
// `json.Marshal` → `UseNumber` 解码"的结果直接比 canonical，覆盖边界值与随机值。
func TestCanonicalWriteReadAgree(t *testing.T) {
	c := testChain(t)
	base := Record{ID: uuid.New(), Action: "canon", Result: "success", OccurredAt: time.Unix(0, 0).UTC()}
	tagOf := func(v any) []byte {
		rec := base
		rec.Before = map[string]any{"n": v}
		tag, err := c.Tag(nil, rec)
		if err != nil {
			t.Fatalf("Tag(%#v): %v", v, err)
		}
		return tag
	}
	// 模拟"落库后再读回"：`json.Marshal` + `UseNumber` 解码（与 `decodeJSON` 同口径）。
	readBack := func(v any) any {
		raw, err := json.Marshal(map[string]any{"n": v})
		if err != nil {
			t.Fatalf("Marshal(%#v): %v", v, err)
		}
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("Decode(%#v): %v", v, err)
		}
		return m["n"]
	}

	values := []any{
		int(0), int(1), int(-1), int64(1), uint64(0), uint32(7), int32(-7),
		float64(1), float32(1), json.Number("1"), json.Number("1.0"), json.Number("1.00"),
		float64(1e6), float64(1234567), float64(-1234567),
		float64(1 << 53), float64(1<<53 + 2), float64(-(1 << 53)),
		float64(1e15), float64(1e16), float64(1e18), float64(-1e18), float64(1e21), float64(-1e21),
		float64(1e-6), float64(1e-7), float64(1.5), float64(-0.5),
		float32(0.1), float32(1e7), float32(-0.25),
		int64(9007199254740993), uint64(math.MaxUint64), json.Number("12345678901234567890"),
	}
	// 随机值：指数铺开，覆盖"整数值 / 非整数值 / 极大 / 极小"。
	rnd := rand.New(rand.NewSource(20261110))
	for i := 0; i < 200; i++ {
		exp := rnd.Intn(60) - 30
		values = append(values, rnd.Float64()*math.Pow10(exp))
	}

	for _, v := range values {
		want, got := tagOf(v), tagOf(readBack(v))
		if !bytesEqual(want, got) {
			t.Fatalf("写侧 %#v 与读回来的值 %#v 算出不同 tag（链自断）", v, readBack(v))
		}
	}
}
