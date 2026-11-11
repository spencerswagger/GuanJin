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

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
)

// ── 判据 4 ────────────────────────────────────────────────────────
// TestACFLOW05_04_ConcurrentPatches：20 并发交替 PATCH 同一 type（D22 seed 之后，行**存在**）。
//
// 与 `_04b` 唯一区别是前置：这里不删行（走正常库），`_04b` 显式注入"缺行"条件。
// 两条同时在库，才能区分「锁真的修好了」与「只是 seed 让缺行路径不常走到」。
func TestACFLOW05_04_ConcurrentPatches(t *testing.T) {
	e := newACFLOW05Env(t, openTestDB(t))
	concurrentPatchesBody(t, e)
}

// TestACFLOW05_04b_MissingRowIsFailClosed：**缺行必须 500，不得静默当空集放行**。
//
// 这一条替代原来「删行再并发」的版本 —— 后者在新口径下前提已不成立：
//   - `factors.disabled` 现在是 **required 启动键**（`ValidateStartup` 缺它就报
//     `required runtime_config key is missing`），删行会让**同包后续所有夹具**起不来；
//   - 运行期缺行按定稿口径就是 **500 `INTERNAL_ERROR`**（不再静默空集）。
//
// 所以这里断言的是**fail-closed 行为本身**，并且删行窗口只覆盖**一次请求**，随即还原。
func TestACFLOW05_04b_MissingRowIsFailClosed(t *testing.T) {
	e := newACFLOW05Env(t, openTestDB(t))

	// 正控①：删之前行在（否则"删掉"这一步毫无意义）。
	var before int64
	if err := e.db.Raw(`SELECT count(*) FROM runtime_config WHERE "key" = ?`,
		store.KeyFactorsDisabled).Scan(&before).Error; err != nil {
		t.Fatalf("数行：%v", err)
	}
	if before != 1 {
		t.Fatalf("正控①失败：factors.disabled 应恰好 1 行，实际 %d", before)
	}

	if err := e.db.Exec(`DELETE FROM runtime_config WHERE "key" = ?`,
		store.KeyFactorsDisabled).Error; err != nil {
		t.Fatalf("删行：%v", err)
	}
	trace := "trace-acflow05-04b-" + uuid.NewString()[:8]
	defer func() {
		// 立刻还原（upsert：UPDATE 对"行不存在"是 0 行，静默不还原 —— 首版就栽在这）。
		if err := e.db.Exec(`INSERT INTO runtime_config ("key", value, value_type)
			VALUES (?, '[]'::jsonb, 'json')
			ON CONFLICT ("key") DO UPDATE SET value = '[]'::jsonb`, store.KeyFactorsDisabled).Error; err != nil {
			t.Errorf("还原 factors.disabled 失败（后续用例会读不到启动键）：%v", err)
		}
	}()

	// 正控②：确实缺行。
	var gone int64
	if err := e.db.Raw(`SELECT count(*) FROM runtime_config WHERE "key" = ?`,
		store.KeyFactorsDisabled).Scan(&gone).Error; err != nil {
		t.Fatalf("确认缺行：%v", err)
	}
	if gone != 0 {
		t.Fatalf("正控②失败：行应已删除，实际仍有 %d 行", gone)
	}

	// 缺行 → 5xx `INTERNAL_ERROR`（fail-closed）；**不得**2xx（那等于静默按空集放行）。
	status, raw, env := e.patch(t, "totp", `{"enabled":false,"reason":"缺行 fail-closed"}`,
		trace)
	if status < 500 {
		t.Fatalf("缺行时必须 5xx（fail-closed），实际 %d / %q（body=%s）—— 2xx 意味着「平台禁用集」被静默当成空集",
			status, env.Code, raw)
	}
	if env.Code != httpx.CodeInternalError {
		t.Fatalf("缺行时对外码应为 %s，实际 %q（body=%s）", httpx.CodeInternalError, env.Code, raw)
	}

	// 且不得留下"半条审计"：缺行时事务应当整体失败。
	var audits int64
	if err := e.db.Raw(`SELECT count(*) FROM audit_logs WHERE actor_user_id = ? AND trace_id = ?`,
		e.actorID, trace).Scan(&audits).Error; err != nil {
		t.Fatalf("数审计：%v", err)
	}
	if audits != 0 {
		t.Fatalf("缺行失败路径不得写审计，实际 %d 条", audits)
	}
}

func concurrentPatchesBody(t *testing.T, e *acflow05Env) {
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
	// ④ 审计条数 = 真实变更数：**按 `prev_tag` 链遍历**（不是按 id、也不是按 occurred_at）。
	//    为什么修正：UUID v7 只有**毫秒**粒度的时序性，同一毫秒内是随机位序 —— 20 个并发请求
	//    很容易落在同一毫秒里，`ORDER BY id` 会把提交顺序排反，于是"链不自洽"可能是排序造成的假红。
	//    `occurred_at`/`created_at` 更早（取行锁之前/事务开始时就取了时间）。
	//    唯一与插入顺序严格一致的是链本身：每行 `prev_tag` 指向上一行的 `integrity_tag`。
	cur := "true"
	if containsStr(before, "password") {
		cur = "false"
	}
	changes, cur := assertAuditChain(t, e, "password", cur)

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

// assertAuditChain 按 `prev_tag` 链**从链尾往回走**，逐条校验本次 actor 的因子启停审计，
// 返回条目数；结束时把 `cur`（当前 enabled 的字符串形式）更新为链首之前的状态。
//
// 为什么必须用链而不是排序：见调用处的说明（UUID v7 同毫秒随机、时间戳早于行锁）。
// 只校验**本 actor** 的连续片段：链是全局的，相邻的其它 actor 记录会作为边界（遇到即停）。
func assertAuditChain(t *testing.T, e *acflow05Env, targetID, initialEnabled string) (int, string) {
	t.Helper()
	rows, err := e.db.Raw(`SELECT id::text, prev_tag, integrity_tag, before->>'enabled', after->>'enabled'
		FROM audit_logs
		WHERE actor_user_id = ? AND target_type = 'factor' AND target_id = ?
		ORDER BY occurred_at`, e.actorID, targetID).Rows()
	if err != nil {
		t.Fatalf("读审计行：%v", err)
	}
	defer func() { _ = rows.Close() }()

	type node struct {
		prev, tag, before, after string
	}
	byTag := map[string]node{}
	hasChild := map[string]bool{} // 作为别人 prev_tag 出现过的 tag
	for rows.Next() {
		var id, before, after string
		var prev, tag []byte
		if err := rows.Scan(&id, &prev, &tag, &before, &after); err != nil {
			t.Fatalf("scan 审计行：%v", err)
		}
		n := node{prev: string(prev), tag: string(tag), before: before, after: after}
		byTag[n.tag] = n
		if len(prev) > 0 {
			hasChild[string(prev)] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("审计行 rows：%v", err)
	}
	if len(byTag) == 0 {
		return 0, initialEnabled
	}

	// 链尾 = 自己的 tag 没有被任何行当作 prev_tag 的那一条。
	var tail *node
	for tag, n := range byTag {
		if !hasChild[tag] {
			cp := n
			tail = &cp
			break
		}
	}
	if tail == nil {
		t.Fatal("审计链里找不到链尾（本 actor 的片段自相成环 = 结构性缺陷）")
	}

	// 从链尾往回走，收集本 actor 的连续片段（遇到非本 actor 的前驱即停）。
	seq := make([]node, 0, len(byTag))
	for cur := tail; cur != nil; {
		seq = append(seq, *cur)
		next, ok := byTag[cur.prev]
		if !ok {
			break // 前驱不在本 actor 集合里：链的另一段（或全局链的更早记录）
		}
		cur = &next
	}
	// 反转成插入顺序。
	for i, j := 0, len(seq)-1; i < j; i, j = i+1, j-1 {
		seq[i], seq[j] = seq[j], seq[i]
	}

	prev := initialEnabled
	for i, n := range seq {
		if n.before != prev {
			t.Fatalf("审计链不自洽：第 %d 条 before=%q，但前一条 after=%q（丢写或重复记）",
				i+1, n.before, prev)
		}
		if n.after == n.before {
			t.Fatalf("第 %d 条记录了一次无变化（before==after=%q），同值重复不该写审计", i+1, n.after)
		}
		prev = n.after
	}
	return len(seq), prev
}
