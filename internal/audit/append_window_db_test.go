//go:build integration && audit_serial

// **串行专用套件**（`-tags 'integration audit_serial' -p 1`）：本文件的三条用例
// （串行零分叉 / b1 / b2）验证的是"串行化与探针敏感性"，在 `-p 8` 的多包并行下
// 会与其它包共享全局审计链、结果不可解释；因此从默认并行套件里摘出，
// 由 `make test-audit-serial`（CI 单列一步）以 `-p 1 -count=3` 运行。

package audit

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 普通并发用例：**advisory 串行下的 20 并发零分叉**（不再声称"窗口强制张开"）。
//
// 事实澄清（Gate 实测）：`afterTailRead` 里加并发计数后，**hook 内同时最多只有 1 个
// goroutine**——`pg_advisory_xact_lock` 在读链尾**之前**取得，写者根本不会同时进入
// "读→插"窗口。所以本用例证明的是"串行 + 零分叉"，**不是**"窗口并发下仍零分叉"。
//
// 为什么需要这条：只跑 100×20 不撞上，只能说明"概率低"，不能说明"有防线"。
// 这里用生产代码里的注入点（`Chain.afterTailRead`，生产恒为 nil）把窗口必然打开，
// 若没有 advisory 锁 + 链尾行 `FOR UPDATE` 这两道防线，20 个写者会立刻分叉。
//
// 原 Gate 要求形态：**造可控延迟让"读链尾 → 插入"的窗口必然张开**，
// 仍必须 0 分叉。做法：并发 20 个写者，每个在读到链尾之后 `sleep 2ms` 再插入
// （`hookedAppend` 复刻 `Append` 的同一条 SQL 序列，只在中间插 sleep）。
func TestAppendConcurrentSerializedNoFork(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
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
	marker := "win-" + uuid.NewString()[:8]
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := chain.Append(context.Background(), db, Record{
				Action: "delay", Result: "success", TargetID: fmt.Sprintf("%s-%d", marker, i),
				OccurredAt: month.Add(time.Duration(i) * time.Millisecond),
			}); err != nil {
				t.Errorf("append: %v", err)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	all, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var records []Record
	for _, rec := range all {
		if strings.HasPrefix(rec.TargetID, marker) {
			records = append(records, rec)
		}
	}
	if len(records) != workers {
		t.Fatalf("本次写入应有 %d 条，实际 %d（本月总 %d 条，含其它标记）", workers, len(records), len(all))
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

// **b1（Gate 口径）**：只摘掉 advisory 锁、保留链尾行 `FOR UPDATE`。
//
// 行锁可能顶上来把写者串行在同一条链尾上，因此这一段**不判红**，只记录事实：
// 零分叉 → "行锁足以串行"；有分叉 → "行锁不足以串行"。两段都先断言条数，防跨轮污染失真。
func TestAppendB1AdvisoryRemovedRowLockHolds(t *testing.T) {
	forks, records := runConcurrentAppends(t, func(c *Chain) { c.disableChainLock = true })
	if len(records) != bWorkers {
		t.Fatalf("写入条数 %d ≠ 期望 %d（跨轮污染时统计不可信）", len(records), bWorkers)
	}
	t.Logf("b1（只摘 advisory，保留 FOR UPDATE）：条数=%d forks=%v", len(records), forks)
}

// **b2（Gate 判据）**：两道串行机制**一起摘掉** → 必须检出分叉。
//
// 这一条才证明"探针能检出分叉"；若 b2 仍零分叉，说明测量手段无效，
// `AC-AUD-04` 的所有零分叉结论都不成立（应记"未稳定通过"）。
func TestAppendB2BothLocksRemovedMustFork(t *testing.T) {
	forks, records := runConcurrentAppends(t, func(c *Chain) {
		c.disableChainLock = true
		c.disableRowLock = true
	})
	if len(records) != bWorkers {
		t.Fatalf("写入条数 %d ≠ 期望 %d（跨轮污染时统计不可信）", len(records), bWorkers)
	}
	if !forks {
		t.Fatalf("两道锁都摘掉后仍零分叉（n=%d）：探针检测不到分叉，正向结论无效", len(records))
	}
	t.Logf("b2 成立：两道锁摘掉后检出分叉（n=%d）", len(records))
}

const bWorkers = 20

// runConcurrentAppends 跑 bWorkers 个并发写者，返回"是否检出分叉"与实际写入条数。
func runConcurrentAppends(t *testing.T, tweak func(*Chain)) (bool, []Record) {
	t.Helper()
	db := openAuditTestDB(t)
	base := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)

	c := *base
	tweak(&c)
	chain := &c

	marker := "bprobe-" + uuid.NewString()[:8]
	var wg sync.WaitGroup
	for i := 0; i < bWorkers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = chain.Append(context.Background(), db, Record{
				Action: "b-probe", Result: "success",
				TargetID:   fmt.Sprintf("%s-%d", marker, i),
				OccurredAt: month.Add(time.Duration(i) * time.Millisecond),
			})
		}(i)
	}
	wg.Wait()

	all, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var records []Record
	for _, rec := range all {
		if strings.HasPrefix(rec.TargetID, marker) {
			records = append(records, rec)
		}
	}
	if len(records) == 0 {
		t.Fatal("没有写入任何记录")
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
	t.Logf("dup_prev_tag=%d broken=%v n=%d", dup, broken != nil, len(records))
	return dup > 0 || broken != nil, records
}
