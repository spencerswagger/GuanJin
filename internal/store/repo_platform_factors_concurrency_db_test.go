//go:build integration

package store

// 平台级因子启停的**并发正确性**（AC-FLOW-05 判据 4 的存储层那一半）。
//
// 缺陷形态（2026-11-11，Core 的 #92 在 HTTP 层抓到）：`runtime_config['factors.disabled']`
// 这一行**还不存在**时，`SELECT … FOR UPDATE` 一行都锁不到 → 20 个并发首次 PATCH 各自
// 读到"空集" → 同一次真实变更被记成**两条审计**（两条 `before=true`），enable/disable
// 交叉时还会丢更新。修法是先取 `pg_advisory_xact_lock`（见 `platformFactorLockKey`）。
//
// 本用例与 #92 的分工：**#92 在 HTTP 层验**"审计条数 = 真实变更数、链自洽"；
// 这里在**存储层**直接压 `SetEnabled`，用"逻辑状态折叠"把不变量钉死——
// 好处是不经过 handler，锁被摘掉时红得更直接（不必等 HTTP 层某条断言才暴露）。
//
// 不变量（每一条都必须成立，缺一条就是丢写/重复记）：
//  1. 相邻两条审计 `Before.enabled` 必须等于上一条 `After.enabled`（逻辑状态连续）；
//  2. 单条审计 `Before != After`（同值不该写审计）；
//  3. 折叠终态必须等于库里的真值；
//  4. 交替 20 次后，审计条数必须远小于 20（真串行化时每次"变"才记一条）。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// recordAuditWriter 把审计事实**按调用顺序**记在内存里（调用发生在调用方事务内，
// 所以"谁真的认为自己改了状态"= 谁调了它）。
type recordAuditWriter struct {
	mu   sync.Mutex
	recs []FactorAuditRecord
}

func (w *recordAuditWriter) AppendInTx(_ context.Context, _ *gorm.DB, rec FactorAuditRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recs = append(w.recs, rec)
	return nil
}

func (w *recordAuditWriter) snapshot() []FactorAuditRecord {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]FactorAuditRecord, len(w.recs))
	copy(out, w.recs)
	return out
}

func TestDBPlatformFactorConcurrentSetEnabledIsSerialized(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// 前置：**这一行必须不存在**（缺陷只在"行不存在"时出现）。
	// 快照 + 还原：平台级开关是全局状态，别把后续用例的"可用因子"改掉。
	var prev string
	snapErr := db.Raw(`SELECT value::text FROM runtime_config WHERE "key" = ?`,
		KeyFactorsDisabled).Row().Scan(&prev)
	switch {
	case snapErr == nil:
	case snapErr == gorm.ErrRecordNotFound || snapErr.Error() == "sql: no rows in result set":
		prev = ""
	default:
		t.Fatalf("读快照：%v", snapErr)
	}
	restore := func() {
		if prev == "" {
			_ = db.Exec(`DELETE FROM runtime_config WHERE "key" = ?`, KeyFactorsDisabled).Error
			return
		}
		_ = db.Exec(`UPDATE runtime_config SET value = ?::jsonb WHERE "key" = ?`,
			prev, KeyFactorsDisabled).Error
	}
	restore()
	t.Cleanup(restore)

	writer := &recordAuditWriter{}
	reader, err := NewPlatformFactorReader(db)
	if err != nil {
		t.Fatalf("NewPlatformFactorReader: %v", err)
	}
	repo, err := NewPlatformFactorRepo(reader, writer)
	if err != nil {
		t.Fatalf("NewPlatformFactorRepo: %v", err)
	}

	// 20 个并发交替 enable/disable 同一个因子。
	const n = 20

	// 把"读到当前集合 → 写回"的窗口张开：20 个 goroutine 都到齐（或等 300ms 超时）再往下走。
	// 为什么需要它：不加这个闸，20 个 goroutine 里先到的那个几十微秒就把行建好了，
	// 后面的人读到的是"已有行"，**缺陷复现不出来**（实测：不带闸时还原实现也 3/3 绿 = 假证据）。
	// 带锁的实现下只有 1 个能进临界区，其余在 advisory 锁上排队 → 闸只等 1 个就超时放行。
	var (
		arrived sync.WaitGroup
		mu      sync.Mutex
		opened  bool
	)
	arrived.Add(n)
	gate := make(chan struct{})
	open := func() {
		mu.Lock()
		if !opened {
			opened = true
			close(gate)
		}
		mu.Unlock()
	}
	repo.afterRead = func() {
		mu.Lock()
		done := opened // 闸已开 → 后面的调用立刻放行（带锁实现下只有第一个需要等）
		mu.Unlock()
		if done {
			return
		}
		arrived.Done()
		go func() { arrived.Wait(); open() }()
		select {
		case <-gate:
		case <-time.After(200 * time.Millisecond):
			open() // 带锁实现下其余请求在 advisory 锁上排队，不会有人再"到齐"
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- repo.SetEnabled(ctx, SetFactorEnabledInput{
				Type:        "password",
				Enabled:     i%2 == 0,
				Reason:      "concurrency",
				ActorUserID: uuid.Nil, // 存储层不校验 actor；HTTP 层才要求登录
				ActorRole:   "system_admin",
				TraceID:     "trace-" + uuid.NewString()[:8],
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发 SetEnabled 不该报错（锁要挡住竞态，不是挡请求）：%v", err)
		}
	}

	// ① + ② 逻辑状态连续、且每条都是真变更。
	recs := writer.snapshot()
	if len(recs) == 0 {
		t.Fatal("20 次交替至少要有 1 条审计")
	}
	cur, ok := recs[0].Before["enabled"].(bool)
	if !ok {
		t.Fatalf("审计 before 缺 enabled 字段：%v", recs[0].Before)
	}
	for i, rec := range recs {
		b, ok1 := rec.Before["enabled"].(bool)
		a, ok2 := rec.After["enabled"].(bool)
		if !ok1 || !ok2 {
			t.Fatalf("第 %d 条审计缺 enabled：before=%v after=%v", i+1, rec.Before, rec.After)
		}
		if b != cur {
			t.Fatalf("审计不自洽：第 %d 条 before=%v，但上一条 after=%v（丢写或重复记）", i+1, b, cur)
		}
		if a == b {
			t.Fatalf("第 %d 条审计记录了一次无变化（before==after=%v）", i+1, a)
		}
		cur = a
	}

	// ③ 折叠终态 == 库里的真值。
	disabled, err := reader.Disabled(ctx)
	if err != nil {
		t.Fatalf("Disabled: %v", err)
	}
	inStore := false
	for _, typ := range disabled {
		if typ == "password" {
			inStore = true
		}
	}
	// `cur` 是"最后一条审计之后 enabled 的状态"，库里是"当前是否被禁用"。
	if wantDisabled := !cur; wantDisabled != inStore {
		t.Fatalf("审计折叠终态（enabled=%v → disabled=%v）与库里的真值（disabled=%v）不一致：%v",
			cur, wantDisabled, inStore, disabled)
	}

	// ④ 真串行化：交替 20 次里，"变更"次数必然远小于 20（同值不写审计）。
	//     这条是防止"把锁换成'每次都写'"式的假修复（那样条数会等于 20）。
	if len(recs) >= n {
		t.Fatalf("审计条数 %d 等于并发数 %d：说明每次请求都认为自己在改（锁没生效或幂等判断丢了）",
			len(recs), n)
	}
}
