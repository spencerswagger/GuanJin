//go:build integration

package audit

import (
	"context"
	"fmt"
	"sync"
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
	requirePartition(t, db, month)
	cleanupMonth(t, db, month)

	// 让"读链尾 → 插入"的窗口**必然张开** 2ms（生产代码里的注入点，恒为 nil）。
	delayed := *chain
	delayed.afterTailRead = func() { time.Sleep(2 * time.Millisecond) }
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
