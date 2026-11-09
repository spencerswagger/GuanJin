//go:build integration

package audit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 并发防线的**受控证据**：强制把"读链尾 → 插入"的窗口张开 2ms，仍必须 0 分叉。
//
// 为什么需要这条：只跑 100×20 不撞上，只能说明"概率低"，不能说明"有防线"。
// 这里用生产代码里的注入点（`Chain.afterTailRead`，生产恒为 nil）把窗口必然打开，
// 若没有 advisory 锁 + 链尾行 `FOR UPDATE` 这两道防线，20 个写者会立刻分叉。
//
// 原 Gate 要求形态：**造可控延迟让"读链尾 → 插入"的窗口必然张开**，
// 仍必须 0 分叉。做法：并发 20 个写者，每个在读到链尾之后 `sleep 2ms` 再插入
// （`hookedAppend` 复刻 `Append` 的同一条 SQL 序列，只在中间插 sleep）。
func TestAppendWindowForcedOpenStillKeepsChainIntact(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := cleanMonth(t)
	month = requirePartition(t, db, month)
	cleanupMonth(t, db, month)

	// **本用例自己建分区**：不依赖 `TestMain` 预建窗口（Gate 实测该用例 3/3 红在
	// `23514 no partition of relation "audit_logs"`——分区没建就直接断言"零分叉"，
	// 结论无效）。建完再断言确实存在，避免"以为建了"。
	if err := EnsurePartitions(context.Background(), db, []time.Time{month}); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	var ready bool
	if err := db.Raw(`SELECT to_regclass(?) IS NOT NULL`,
		fmt.Sprintf("audit_logs_p%s", month.Format("200601"))).Scan(&ready).Error; err != nil {
		t.Fatalf("check partition: %v", err)
	}
	if !ready {
		t.Fatalf("分区 audit_logs_p%s 未建好，无法验证防线", month.Format("200601"))
	}

	// 让"读链尾 → 插入"的窗口**必然张开** 2ms（生产代码里的注入点，恒为 nil），
	// 并**证明窗口真的张开**：统计同时在 hook 内的 goroutine 数，峰值必须 ≥2
	// （否则"零分叉"可能只是并发根本没重叠）。
	var (
		inside   int32
		peak     int32
		mu       sync.Mutex
		observed int
	)
	delayed := *chain
	delayed.afterTailRead = func() {
		cur := atomic.AddInt32(&inside, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
		}
		mu.Lock()
		observed++
		mu.Unlock()
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&inside, -1)
	}
	chain = &delayed

	const workers = 20
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := chain.Append(context.Background(), db, Record{
				Action: "delay", Result: "success",
				OccurredAt: month.Add(time.Duration(i) * time.Millisecond),
			}); err != nil {
				t.Errorf("append: %v", err)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != workers {
		t.Fatalf("期望 %d 条，实际 %d", workers, len(records))
	}
	// **串行化被证明**：advisory 锁在 hook 之前取得，因此 hook 内并发峰值必须恒为 1。
	// 峰值 ≥2 就说明锁没起作用（并发真的重叠了），那才是防线失效。
	if p := atomic.LoadInt32(&peak); p != 1 {
		t.Fatalf("hook 内并发峰值 = %d（期望 1）：说明写者在该窗口内重叠，串行化失效", p)
	}
	if observed < workers {
		t.Fatalf("hook 被进入 %d 次（期望 ≥%d）", observed, workers)
	}
	broken, err := chain.Verify(records, records[0].PrevTag)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if broken != nil {
		t.Fatalf("可控延迟下出现分叉：%+v（elapsed=%s）", broken, elapsed)
	}
	// 顺带统计 prev_tag 重复（分叉的显式度量）。
	seen := map[string]int{}
	for _, r := range records {
		seen[string(r.PrevTag)]++
	}
	dup := 0
	for _, n := range seen {
		if n > 1 {
			dup++
		}
	}
	fmt.Printf("SUMMARY workers=%d elapsed=%s dup_prev_tag=%d broken=%v\n",
		workers, elapsed.Round(time.Millisecond), dup, broken != nil)
}

// **反证**：把 advisory 锁关掉（`disableChainLock`，仅测试用），窗口内并发峰值必然 >1，
// 且探针必须能检测到分叉/断链——否则正向那条"零分叉"就只是探针失灵。
//
// 这条是 Gate 要求的"反证必须红"：它证明本文件的检测手段有效。
func TestAppendWithoutChainLockForks(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := cleanMonth(t)
	month = requirePartition(t, db, month)
	cleanupMonth(t, db, month)
	if err := EnsurePartitions(context.Background(), db, []time.Time{month}); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}

	var inside, peak int32
	unlocked := *chain
	unlocked.disableChainLock = true
	unlocked.afterTailRead = func() {
		cur := atomic.AddInt32(&inside, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		atomic.AddInt32(&inside, -1)
	}
	chain = &unlocked

	const workers = 20
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = chain.Append(context.Background(), db, Record{
				Action: "unlocked", Result: "success",
				OccurredAt: month.Add(time.Duration(i) * time.Millisecond),
			})
		}(i)
	}
	wg.Wait()

	if p := atomic.LoadInt32(&peak); p < 2 {
		t.Fatalf("关掉 advisory 锁后 hook 内并发峰值 = %d（期望 ≥2）：说明这个探针制造不出并发窗口", p)
	}
	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	seen := map[string]int{}
	for _, r := range records {
		seen[string(r.PrevTag)]++
	}
	dup := 0
	for _, n := range seen {
		if n > 1 {
			dup++
		}
	}
	broken, err := chain.Verify(records, records[0].PrevTag)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if dup == 0 && broken == nil {
		t.Fatalf("关掉锁后仍无分叉（dup=0, broken=nil, n=%d）：说明探针检测不到分叉，正向结论无效", len(records))
	}
	t.Logf("反证成立：关锁后 dup_prev_tag=%d broken=%v peak=%d", dup, broken, atomic.LoadInt32(&peak))
}
