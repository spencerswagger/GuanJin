//go:build integration && audit_serial

package audit

// `AC-AUD-04`（并发 100 写不断链）放在**串行专用套件**里：它需要一个稳定的
// 全局链尾（月份 ≥ 链尾月）才有确定的语义，而 `-p 8` 下其它包会同时往同一张
// `audit_logs` 写（例如分区用例的建/删），月份会被推着走 → 偶发
// `occurred_at predates the tail partition month`。
//
// 运行方式：`make test-audit-serial`（`-tags 'integration audit_serial' -p 1 -count=3`）。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAC_AUD_04_Concurrent100AppendsKeepChainIntact(t *testing.T) {
	db := openAuditTestDB(t)
	chain := auditTestChain(t)
	month := freshMonth(t, db)
	cleanupMonth(t, db, month)

	const n = 100
	marker := "ac-aud-04-" + uuid.NewString()[:8]
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := chain.Append(context.Background(), db, Record{
				Action: "user.disable", Result: "success", TargetType: "user",
				TargetID: fmt.Sprintf("%s-%d", marker, i), ClientID: "s10-audit",
				After:      map[string]any{"i": fmt.Sprintf("%d", i)},
				OccurredAt: month.Add(time.Duration(i) * time.Millisecond),
			}); err != nil {
				errs <- fmt.Errorf("月份=%s i=%d: %w", month.Format("200601"), i, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发写入失败：%v", err)
	}

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
	if len(records) != n {
		t.Fatalf("本次写入应有 %d 条，实际 %d（本月总 %d 条）", n, len(records), len(all))
	}
	seen := map[string]bool{}
	for i, rec := range records {
		if len(rec.IntegrityTag) == 0 {
			t.Fatalf("第 %d 条没有 integrity_tag", i)
		}
		key := string(rec.IntegrityTag)
		if seen[key] {
			t.Fatalf("第 %d 条 tag 与前面重复（链出现分叉）", i)
		}
		seen[key] = true
	}
	broken, err := chain.Verify(records, records[0].PrevTag)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if broken != nil {
		t.Fatalf("并发 100 写后整链必须通过，实际断在 %+v", broken)
	}
}

// `EnsurePartitions` / `DropPartitions` 的**真库**路径（生产月度任务用的就是这对函数）：
// 建 → 存在 → 删 → 不存在；重复调用幂等；且与链锁共用同一把 advisory 锁（不与写入死锁）。
func TestEnsureAndDropPartitionsRoundTrip(t *testing.T) {
	db := openAuditTestDB(t)
	month := time.Date(2071, 3, 1, 0, 0, 0, 0, time.UTC) // 故意用池外的月份，独立验证
	ctx := context.Background()

	// 先确保干净（可能上次跑挂了）。
	_ = DropPartitions(ctx, db, []time.Time{month})
	t.Cleanup(func() { _ = DropPartitions(ctx, db, []time.Time{month}) })

	if err := EnsurePartitions(ctx, db, []time.Time{month}); err != nil {
		t.Fatalf("EnsurePartitions: %v", err)
	}
	// 幂等：再建一次不报错。
	if err := EnsurePartitions(ctx, db, []time.Time{month}); err != nil {
		t.Fatalf("EnsurePartitions(again): %v", err)
	}
	var exists bool
	if err := db.Raw(`SELECT to_regclass(?) IS NOT NULL`,
		fmt.Sprintf("audit_logs_p%s", month.Format("200601"))).Scan(&exists).Error; err != nil {
		t.Fatalf("check: %v", err)
	}
	if !exists {
		t.Fatal("分区应已创建")
	}
	if err := DropPartitions(ctx, db, []time.Time{month}); err != nil {
		t.Fatalf("DropPartitions: %v", err)
	}
	if err := db.Raw(`SELECT to_regclass(?) IS NOT NULL`,
		fmt.Sprintf("audit_logs_p%s", month.Format("200601"))).Scan(&exists).Error; err != nil {
		t.Fatalf("check after drop: %v", err)
	}
	if exists {
		t.Fatal("分区应已删除")
	}
}

// 注：本用例做的是**DDL**（建/删分区，对父表 `audit_logs` 取 ACCESS EXCLUSIVE），
// 与并行套件里其它用例的并发 INSERT（ROW EXCLUSIVE）会撞 40P01——实测 CI 上就红在这里。
// 因此放在串行套件里跑（`-tags 'integration audit_serial' -p 1`）。
