//go:build integration

package main

// AC-FLOW-05 判据 4（并发）的**红证据**：本用例现在**必然失败**，因为在
// `runtime_config['factors.disabled']` 行尚不存在时，`SELECT ... FOR UPDATE` 锁不到行，
// 并发的首次 PATCH 会各自读到"空集"→ 一个真实变更被记成两条审计（before 都是 true），
// 违反判据 4 的「审计条数 = 真实变更数」。
//
// 为什么单独放一个文件/PR 而不是在本 PR 里放宽断言：放宽等于把缺陷藏起来。
// 修好实现（migration seed 该键，或 `pg_advisory_xact_lock` / 先
// `INSERT ... ON CONFLICT DO NOTHING` 再 `FOR UPDATE`）后，本文件应当**不改一字**地转绿。
//
// 复现（CI integration job，真 PG17）：
//
//	--- FAIL: TestACFLOW05_04_ConcurrentPatches
//	    acflow05_db_test.go: 审计链不自洽：第 2 条的 before="true"，但上一条 after="false"
//
// 依赖：夹具与助手都在 `acflow05_db_test.go`（同包），本文件只多一个用例。

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ── 判据 4 ────────────────────────────────────────────────────────
func TestACFLOW05_04_ConcurrentPatches(t *testing.T) {
	db := openTestDB(t)
	e := newACFLOW05Env(t, db)

	// ① 20 并发交替 PATCH 同一 type。
	const n = 20
	type result struct {
		i       int
		enabled bool
		done    time.Time
	}
	results := make([]result, 0, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	before := e.disabledSet(t)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			enabled := i%2 == 0 // 交替
			body := `{"enabled":false,"reason":"并发停用"}`
			if enabled {
				body = `{"enabled":true,"reason":"并发启用"}`
			}
			status, raw, env := e.patch(t, "password", body,
				"trace-acflow05-04-"+uuid.NewString()[:8])
			if status != http.StatusOK {
				t.Errorf("第 %d 次并发 PATCH 应 2xx，实际 %d / %q（body=%s）", i, status, env.Code, raw)
				return
			}
			mu.Lock()
			results = append(results, result{i: i, enabled: enabled, done: time.Now()})
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}

	// ② 数组无重复（同一 type 只出现一次或零次）。
	final := e.disabledSet(t)
	seen := map[string]int{}
	for _, typ := range final {
		seen[typ]++
	}
	for typ, c := range seen {
		if c > 1 {
			t.Fatalf("factors.disabled 出现重复项 %q（%d 次）：%v", typ, c, final)
		}
	}

	// ③ 终值 = 最后一次成功变更 —— 判定放在 ④ 的链式折叠里做（确定性），
	//    不按"哪个 HTTP 响应最后返回"来推：提交顺序与响应返回顺序不保证一致，那样会 flaky。
	//
	// ④ 审计条数 = 真实变更数：按 **id（UUID v7）** 折叠，每条 before 必须等于上一条 after。
	//    为什么不用 occurred_at：它在**取行锁之前**就取了时间，并发下可能早于实际提交顺序；
	//    id 是拿到行锁之后才生成的 v7，与提交顺序一致（链本身也按插入顺序串 prev_tag）。
	rows, err := db.Raw(`SELECT before->>'enabled', after->>'enabled'
		FROM audit_logs WHERE actor_user_id = ? AND target_type = 'factor' AND target_id = 'password'
		ORDER BY id`, e.actorID).Rows()
	if err != nil {
		t.Fatalf("读审计序列：%v", err)
	}
	defer func() { _ = rows.Close() }()
	cur := "true"
	if containsStr(before, "password") {
		cur = "false"
	}
	changes := 0
	for rows.Next() {
		var b, a string
		if err := rows.Scan(&b, &a); err != nil {
			t.Fatalf("scan 审计序列：%v", err)
		}
		if b != cur {
			t.Fatalf("审计链不自洽：第 %d 条的 before=%q，但上一条 after=%q（丢写或重复记）",
				changes+1, b, cur)
		}
		if a == b {
			t.Fatalf("审计条目记录了一次无变化（before==after=%q），同值重复不该写审计", a)
		}
		cur = a
		changes++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("审计序列 rows：%v", err)
	}
	if changes == 0 {
		t.Fatal("20 次交替 PATCH 至少要有 1 条审计")
	}
	if got := containsStr(final, "password"); (cur == "false") != got {
		t.Fatalf("审计链终态（enabled=%v）与库里的终值（disabled=%v）不一致", cur, got)
	}

	// ⑤ 并发两个不同 type → 两条都生效（不丢写）。
	var wg2 sync.WaitGroup
	wg2.Add(2)
	for _, typ := range []string{"password", "totp"} {
		go func(typ string) {
			defer wg2.Done()
			status, raw, env := e.patch(t, typ,
				`{"enabled":false,"reason":"并发不同 type"}`,
				"trace-acflow05-04b-"+uuid.NewString()[:8])
			if status != http.StatusOK {
				t.Errorf("并发停用 %s 应 200，实际 %d / %q（body=%s）", typ, status, env.Code, raw)
			}
		}(typ)
	}
	wg2.Wait()
	if t.Failed() {
		t.FailNow()
	}
	finalTwo := e.disabledSet(t)
	if !containsStr(finalTwo, "password") || !containsStr(finalTwo, "totp") {
		t.Fatalf("两个不同 type 并发都必须生效（不丢写），实际 %v", finalTwo)
	}
}
