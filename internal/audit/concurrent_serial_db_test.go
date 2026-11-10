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
