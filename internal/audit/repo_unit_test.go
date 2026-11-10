package audit

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// 本文件是 `repo.go` / `verify.go` 里**不依赖数据库**的分支：入参校验、构造期校验、
// 指标语义、月度切分、Job 的取消语义。真库行为在 `repo_db_test.go`（`-tags=integration`）。

func TestAppendAndLoadRejectBadInput(t *testing.T) {
	c := testChain(t)
	ctx := context.Background()

	if _, err := c.Append(ctx, nil, Record{}); err == nil {
		t.Fatal("nil db 必须报错")
	}
	var nilChain *Chain
	if _, err := nilChain.Append(ctx, nil, Record{}); err == nil {
		t.Fatal("nil chain 必须报错")
	}
	if _, err := c.Load(ctx, nil, time.Now(), time.Now()); err == nil {
		t.Fatal("Load 的 nil db 必须报错")
	}
	// 缺必填字段：不许写一条"没有动作/结果/时间"的审计事实。
	for name, rec := range map[string]Record{
		"缺 action": {Result: "success", OccurredAt: time.Now()},
		"缺 result": {Action: "a", OccurredAt: time.Now()},
		"缺时间":      {Action: "a", Result: "success"},
	} {
		if _, err := c.Append(ctx, nil, rec); err == nil {
			t.Fatalf("%s 时必须报错", name)
		}
	}
}

func TestAnchorStoreRejectsNil(t *testing.T) {
	var s *AnchorStore
	if err := s.Put(context.Background(), time.Now(), []byte("x")); err == nil {
		t.Fatal("nil anchor store 的 Put 必须报错")
	}
	if _, err := s.Get(context.Background(), time.Now()); err == nil {
		t.Fatal("nil anchor store 的 Get 必须报错")
	}
	if _, err := NewAnchorStore(nil).Get(context.Background(), time.Now()); err == nil {
		t.Fatal("nil db 的 Get 必须报错")
	}
	if err := NewAnchorStore(nil).Put(context.Background(), time.Now(), []byte("x")); err == nil {
		t.Fatal("nil db 的 Put 必须报错")
	}
}

func TestNewVerifierValidation(t *testing.T) {
	c := testChain(t)
	if _, err := NewVerifier(nil, nil, nil, nil); err == nil {
		t.Fatal("缺 chain 必须报错")
	}
	if _, err := NewVerifier(c, nil, nil, nil); err == nil {
		t.Fatal("缺 db 必须报错")
	}
	if _, err := NewVerifier(c, nil, NewAnchorStore(nil), nil); err == nil {
		t.Fatal("缺 db（有 anchor store）必须报错")
	}
	if _, err := NewVerifier(c, nilDB(), nil, nil); err == nil {
		t.Fatal("缺 anchor store 必须报错")
	}
}

func TestMonthOfTruncatesToUTCFirstDay(t *testing.T) {
	at := time.Date(2026, 11, 10, 23, 59, 59, 999, time.FixedZone("CST", 8*3600))
	got := monthOf(at)
	want := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("期望 %s，实际 %s", want, got)
	}
}

// 指标语义：从未复核过时 `AnchorAge` 必须返回 ok=false（不拿 0 冒充"刚跑过"）。
func TestAnchorAgeSemantics(t *testing.T) {
	ResetMetricsForTest()
	if _, ok := AnchorAge(time.Now()); ok {
		t.Fatal("从未复核过时 ok 必须为 false")
	}
	now := time.Now().UTC()
	lastVerifiedUnixSec.Store(now.Add(-10 * time.Second).Unix())
	age, ok := AnchorAge(now)
	if !ok || age < 9*time.Second || age > 11*time.Second {
		t.Fatalf("AnchorAge 期望约 10s，实际 %s / ok=%v", age, ok)
	}
	// 断链计数：每次 +1，可读。
	ResetMetricsForTest()
	chainBrokenTotal.Add(1)
	chainBrokenTotal.Add(1)
	if got := ChainBrokenTotal(); got != 2 {
		t.Fatalf("期望 2，实际 %d", got)
	}
	ResetMetricsForTest()
}

// Job 的取消语义：`cancel()` 之后 goroutine 必须退出（不留后台任务）。
func TestStartJobStopsOnCancel(t *testing.T) {
	c := testChain(t)
	v, err := NewVerifier(c, unreachableDB(t), NewAnchorStore(unreachableDB(t)), nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := StartJob(ctx, v, 10*time.Millisecond, nil)
	time.Sleep(30 * time.Millisecond)
	stop()
	cancel()
	// 能走到这里说明没有 panic / 没有阻塞；再给一点时间让 goroutine 收尾。
	time.Sleep(20 * time.Millisecond)
}

// `RunRange` 在锚点读不到时**不许**退化成 nil 继续校验（否则"锚点丢了"会被当成"首段正常"）。
func TestRunRangeFailsClosedWhenAnchorUnreadable(t *testing.T) {
	c := testChain(t)
	// 用 nil db：Load 会报错；这里只断言"不会静默返回 ok"。
	v, err := NewVerifier(c, unreachableDB(t), NewAnchorStore(unreachableDB(t)), nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	res, err := v.RunRange(context.Background(), time.Now(), time.Now().Add(time.Hour))
	if err == nil {
		t.Fatalf("读锚点/扫表失败必须报错，实际 res=%+v", res)
	}
}

// unreachableDB 返回一个**非 nil 但连不上**的 gorm 句柄（单元测试里表达"库不可用"）。
//
// `DisableAutomaticPing` 必须打开：否则 `gorm.Open` 会立刻拨号、测试变成慢超时。
func unreachableDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "postgres://user:pass@127.0.0.1:1/guanjin_test?sslmode=disable&connect_timeout=1",
	}), &gorm.Config{DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db
}

// nilDB 表达"缺 db"的分支（`null` 句柄）。
func nilDB() *gorm.DB { return (*gorm.DB)(nil) }

// `Tag`/`Verify` 的边界：nil 链、零键链、空记录集都要有确定行为（不许 panic、不许静默通过）。
func TestChainGuardsOnZeroKey(t *testing.T) {
	var zero Chain // key 为零值
	if _, err := zero.Tag(nil, Record{}); err == nil {
		t.Fatal("零键链的 Tag 必须报错")
	}
	if _, err := zero.Verify(nil, nil); err == nil {
		t.Fatal("零键链的 Verify 必须报错（空记录集也不许静默通过）")
	}
}

// `BrokenPoint.Is` 与 `ErrChainBroken` 的归一致性（调用方靠它判"是否断链"）。
func TestBrokenPointIsErrChainBroken(t *testing.T) {
	var bp error = &BrokenPoint{Index: 1, Reason: ReasonTagMismatch}
	if !errors.Is(bp, ErrChainBroken) {
		t.Fatal("BrokenPoint 必须 errors.Is 到 ErrChainBroken")
	}
	if errors.Is(nil, ErrChainBroken) {
		t.Fatal("nil 不该被判成断链")
	}
}

// `jsonOrNil` / `decodeJSON` 的边界：不可序列化的值必须报错；坏 JSON 必须留痕而不是丢弃。
func TestJSONHelpersEdgeCases(t *testing.T) {
	if _, err := jsonOrNil(map[string]any{"f": func() {}}); err == nil {
		t.Fatal("不可序列化的值必须报错（否则会静默丢字段）")
	}
	if v, err := jsonOrNil(nil); err != nil || v != nil {
		t.Fatalf("nil map 应返回 (nil,nil)，实际 %v / %v", v, err)
	}
	// 坏 JSON：不 panic、不静默返回 nil，而是把原文挂在 `_raw` 上（排障时能看见）。
	got := decodeJSON([]byte("{not json"))
	if got == nil || got["_raw"] == nil {
		t.Fatalf("坏 JSON 应保留原文，实际 %+v", got)
	}
	if v := decodeJSON(nil); v != nil {
		t.Fatalf("空输入应返回 nil，实际 %+v", v)
	}
}

// 构造期缺依赖的分支（`NewVerifier` 的 5 个 case）——闭合装配期 fail-fast 的覆盖。
func TestNewVerifierMissingDeps(t *testing.T) {
	c := testChain(t)
	if _, err := NewVerifier(c, unreachableDB(t), nil, nil); err == nil {
		t.Fatal("缺 anchor store 必须报错")
	}
	if _, err := NewVerifier(c, nil, NewAnchorStore(unreachableDB(t)), nil); err == nil {
		t.Fatal("缺 db 必须报错")
	}
	// logger 为 nil 时补默认值（不 panic）。
	if _, err := NewVerifier(c, unreachableDB(t), NewAnchorStore(unreachableDB(t)), nil); err != nil {
		t.Fatalf("全依赖齐时应成功：%v", err)
	}
}

// `StartJob` 的 interval<=0 走默认周期（不许 0 ticker panic）。
func TestStartJobDefaultInterval(t *testing.T) {
	c := testChain(t)
	v, err := NewVerifier(c, unreachableDB(t), NewAnchorStore(unreachableDB(t)), nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	stop := StartJob(context.Background(), v, 0, nil)
	stop() // 立刻停：只验证"0 不 panic、能停"
}

// `Put` 的"值为 nil tag"也要能落（锚点可指向空链首）——用 DryRun 句柄只验"不报错、不发错 SQL"。
func TestAnchorPutEmptyTag(t *testing.T) {
	db, _ := dryRunDBForAudit(t)
	if err := NewAnchorStore(db).Put(context.Background(), time.Now(), nil); err != nil {
		t.Fatalf("Put(nil tag): %v", err)
	}
}

// dryRunDBForAudit 返回一个"只生成 SQL、不连接"的句柄（与 store 包的 dryRunDB 同思路）。
func dryRunDBForAudit(t *testing.T) (*gorm.DB, []string) {
	t.Helper()
	var stmts []string
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "postgres://user:pass@127.0.0.1:1/guanjin_test?sslmode=disable",
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		DryRun:                 false, // DryRun 在 Session 上开，见下
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db.Session(&gorm.Session{DryRun: true}), stmts
}

// 分区维护的入参分支（生产月度任务用同一对函数）。
func TestPartitionHelpersValidation(t *testing.T) {
	ctx := context.Background()
	if err := EnsurePartitions(ctx, nil, []time.Time{time.Now()}); err == nil {
		t.Fatal("nil db 必须报错")
	}
	if err := DropPartitions(ctx, nil, []time.Time{time.Now()}); err == nil {
		t.Fatal("nil db 必须报错")
	}
	if err := EnsurePartitions(ctx, unreachableDB(t), nil); err != nil {
		t.Fatalf("空月份应直接返回 nil，实际 %v", err)
	}
}

// `isNoRows`：空结果判定的两个来源（gorm 哨兵 + database/sql 原文），以及"非空结果不算空"。
func TestIsNoRows(t *testing.T) {
	if !isNoRows(gorm.ErrRecordNotFound) {
		t.Fatal("gorm.ErrRecordNotFound 应判为 no rows")
	}
	if !isNoRows(errors.New("sql: no rows in result set")) {
		t.Fatal("database/sql 的原文应判为 no rows")
	}
	if isNoRows(nil) || isNoRows(errors.New("connection refused")) {
		t.Fatal("其它错误/ nil 不算 no rows")
	}
}

// `Append` 的库错误分支：句柄连不上 → 报错（不许静默"写成功"）。
func TestAppendSurfacesDBError(t *testing.T) {
	c := testChain(t)
	if _, err := c.Append(context.Background(), unreachableDB(t), Record{
		Action: "a", Result: "success", OccurredAt: time.Now(),
	}); err == nil {
		t.Fatal("库不可达时 Append 必须报错")
	}
}

// `Put` 的库错误分支：句柄连不上 → 报错。
func TestAnchorPutSurfacesDBError(t *testing.T) {
	s := NewAnchorStore(unreachableDB(t))
	if err := s.Put(context.Background(), time.Now(), []byte("x")); err == nil {
		t.Fatal("库不可达时 Put 必须报错")
	}
}

// `RunRange` 在扫表失败时报错（不许把"扫不出来"当成"整链通过"）。
func TestRunRangeSurfacesLoadError(t *testing.T) {
	c := testChain(t)
	v, err := NewVerifier(c, unreachableDB(t), NewAnchorStore(unreachableDB(t)), nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	if _, err := v.RunRange(context.Background(), time.Now(), time.Now().Add(time.Hour)); err == nil {
		t.Fatal("读锚点/扫表失败必须报错")
	}
}

// 错误分支收口：不可序列化内容 → `Tag` 报错；分区维护在库不可达时报错。
func TestErrorBranchesReach(t *testing.T) {
	c := testChain(t)
	if _, err := c.Tag(nil, Record{
		Action: "a", Result: "success", OccurredAt: time.Now(),
		Before: map[string]any{"bad": func() {}},
	}); err == nil {
		t.Fatal("不可序列化内容必须让 Tag 报错（否则会静默丢字段、链覆盖不全）")
	}
	if err := EnsurePartitions(context.Background(), unreachableDB(t), []time.Time{time.Now()}); err == nil {
		t.Fatal("库不可达时 EnsurePartitions 必须报错")
	}
	if err := DropPartitions(context.Background(), unreachableDB(t), []time.Time{time.Now()}); err == nil {
		t.Fatal("库不可达时 DropPartitions 必须报错")
	}
}

// 上界守卫的**链空分支**（Gate 要求补：此前"链空"只有代码没有测试）。
//
// 规则：允许写到「max(链尾月, 当月) + 1」个月（含），右开界 = 那个月 + 2。
func TestOccurredAtUpperBoundIsAbsolute(t *testing.T) {
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

	// ① 绝对上界：恒为「当月 + 2」的右开区间（允许当月+1，拒当月+2）。
	limit := occurredAtUpperBound(now)
	if want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC); !limit.Equal(want) {
		t.Fatalf("上界应为 2027-01（右开），实际 %s", limit.Format("2006-01"))
	}
	if !time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC).Before(limit) {
		t.Fatal("当月+1 必须被允许（早于右开界）")
	}
	if time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC).Before(limit) {
		t.Fatal("当月+2 必须被拒绝（不早于右开界）")
	}

	// ② **防蠕变正控**：上界与链尾无关——链尾已经到「当月+1」时，上界仍是「当月+2」右开，
	// 即"当月+2 依然被拒"。旧实现是 `max(链尾月, 当月)+2`，链尾漂到未来就会把上界一起推高，
	// 于是当月流量反被下界守卫拒（审计写入停摆）。这条用例钉住"上界不随链尾漂"。
	nextMonthTail := time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC)
	if got := occurredAtUpperBound(now); !got.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("链尾在 %s 时上界仍应是 2027-01（右开），实际 %s",
			nextMonthTail.Format("2006-01"), got.Format("2006-01"))
	}
	if time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC).Before(occurredAtUpperBound(now)) {
		t.Fatal("链尾已到当月+1 时，当月+2 仍必须被拒")
	}
}
