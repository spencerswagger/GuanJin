//go:build integration

package audit

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/testdb"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是**哈希链的真库证据**（AC-AUD-03/04/05/11），真 PG17 `guanjin_test`：
//
//	03 删中间一条 → 断链可检出；
//	04 **并发 100 写** → 不断链、整链复核通过（且逐条 hash 接得上、无重复 tag）；
//	05 篡改一行 → `broken`（`integrity_tag_mismatch`）；
//	11 两个指标的语义（`audit_chain_broken_total` 每次断链 +1、`audit_anchor_age_seconds`
//	   以成功复核为基准）+ 整链复核器可在 `cmd/guanjind` 进程内跑（装配见 `cmd/guanjind/audit_job.go`）。

func openAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		// 不许 Skip（Gate 口径：SKIP = 未验证 = 红）。
		t.Fatal("必须设置 GUANJIN_TEST_DSN（integration job 已注入）")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{DSN: dsn})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Ping(ctx, db); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if !db.Migrator().HasTable("audit_logs") {
		t.Fatalf("缺 audit_logs 表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN")
	}
	// **限连接占用**：`AC-AUD-04` 要 100 个并发写，而多包并行 + `max_connections=100`
	// 会打满服务端（实测 `FATAL: sorry, too many clients already (53300)`）。
	// 100 个 goroutine 在 16 条连接上排队，仍是 100 个并发**逻辑**写——
	// 不靠降并发来"修"，判据强度不变。
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(16)
		sqlDB.SetMaxIdleConns(16)
	}
	t.Cleanup(func() { _ = store.Close(db) })
	return db
}

func auditTestChain(t *testing.T) *Chain {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 0x31)
	}
	master, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	key, err := master.DeriveHMAC(crypto.PurposeHMACAudit)
	if err != nil {
		t.Fatalf("DeriveHMAC: %v", err)
	}
	chain, err := NewChain(key)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	return chain
}

// 测试月的"分区池"：由 `TestMain` **一次性**建好，运行期**不做任何 DDL**。
//
// 为什么改成预建：早先每个用例自己 `CREATE TABLE ... PARTITION OF`，在 `-p 8`（多包并行）
// 下与并发写入撞出 **40P01 死锁**（Gate 复现：`create partition audit_logs_pXXXXXX`）。
// 运行期 DDL 在并发测试里必然不稳；生产也不这么做（分区由月度任务建，见 000011 注释）。
const (
	partitionStartYear  = 2040
	partitionMonthCount = 120 // 10 年：运行期**零 DDL**，全部由 TestMain 预建
)

// partitionMonths 是预建分区覆盖的月份窗口：**基准月起连续 `partitionMonthCount` 个月**。
//
// 基准月由 `TestMain` 按"链尾月份 + 1"设定（见 `setPoolBaseMonth`）；`TestMain` 里
// 先设基准再建分区，所以这里读得到。若基准未设（例如单跑某个用例），退化为
// `partitionStartYear` 起算——此时那些用例会因下界守卫而失败，属预期（不该单跑它们）。
func partitionMonths() []time.Time {
	base, ok := poolBaseMonth.Load().(time.Time)
	if !ok || base.IsZero() {
		base = time.Date(partitionStartYear, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	out := make([]time.Time, 0, partitionMonthCount)
	for i := 0; i < partitionMonthCount; i++ {
		out = append(out, base.AddDate(0, i, 0))
	}
	return out
}

// monthSeq 给每个用例分配**单调递增**的月份序号（不建表、不撞月、且一定晚于链尾）。
var monthSeq atomic.Int64

// poolBaseMonth 是分区池的**基准月**（`TestMain` 里按"链尾月份 + 1"设定）。
//
// 为什么必须以链尾为基：`Append` 有下界守卫——`occurred_at` 早于链尾所在分区月初就拒。
// 早先用 jitter 随机挑月份，后跑的用例可能拿到**更早**的月份 → 直接撞守卫
// （实测：单包 5 轮红 1 轮、全量 `-p 8` 3 轮红 2 轮，每轮 47 条）。
var poolBaseMonth atomic.Value // time.Time

// cleanMonth 按调用顺序分配 `poolBaseMonth + n`（严格单调、不撞月、零 DDL）。
func cleanMonth(t *testing.T) time.Time {
	t.Helper()
	base, ok := poolBaseMonth.Load().(time.Time)
	if !ok || base.IsZero() {
		t.Fatal("分区池基准月未初始化（应由 TestMain 设定）")
	}
	n := monthSeq.Add(1) - 1
	if int(n) >= partitionMonthCount {
		t.Fatalf("分区池不够用（用例数 %d > %d），调大 partitionMonthCount", n+1, partitionMonthCount)
	}
	return base.AddDate(0, int(n), 0)
}

// retriesTotal 记录"重锚重试"的累计次数（Gate 要看这个数：> 总写入 10% 说明结构性问题仍在）。
var retriesTotal atomic.Int64

// disableReanchor 在串行套件里置 true：串行套件用 `freshMonth` 的独占月份，
// 重锚会把行写到**别的月份**、绕过该用例的清理，造成"同月 70 条"这种跨轮污染。
// 因此串行套件里**不做重锚**：真出现 `predates` 就让它红出来（配额足够时不该发生）。
var disableReanchor atomic.Bool

// appendWithReanchor 写一条记录；命中 `ErrOccurredAtTooOld` / 23514 时**重锚**（重取链尾 → 新月份）
// 后重试，最多 3 次，并把次数累加到 `retriesTotal`。
//
// 为什么需要它：「选月 → INSERT」之间别的写者会把链尾推前（全局单链），这一步在 `-count>1`
// 与 `-p 8` 下必然偶发；重锚是唯一不依赖"赌没撞上"的做法（同事务内取尾 → INSERT 需要改产品签名，
// 已作为后续项记录）。
func appendWithReanchor(chain *Chain, db *gorm.DB, rec Record) ([]byte, error) {
	cur := rec
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		saved, err := chain.Append(context.Background(), db, cur)
		if err == nil {
			return saved, nil
		}
		lastErr = err
		if disableReanchor.Load() || (!errors.Is(err, ErrOccurredAtTooOld) && !strings.Contains(err.Error(), "23514")) {
			return nil, err
		}
		retriesTotal.Add(1)
		// 重锚**只按当前链尾**取 `tail+1`（不消耗"进程内独占月份"）：
		// 否则每条重试都推进一次月份，100 个 goroutine 会互相把月份推到 2048+。
		m := freshMonthNoTest(chain, db)
		cur.OccurredAt = m
	}
	return nil, lastErr
}

// freshMonthNoTest 给非 *testing.T 场景取一个"晚于当前链尾"的月份（供重锚使用）。
func freshMonthNoTest(_ *Chain, db *gorm.DB) time.Time {
	lo := time.Date(partitionStartYear, 1, 1, 0, 0, 0, 0, time.UTC)
	var tail sql.NullTime
	_ = db.Raw(`SELECT max(occurred_at) FROM audit_logs`).Scan(&tail).Error
	month := lo
	if tail.Valid && !tail.Time.IsZero() {
		tm := time.Date(tail.Time.UTC().Year(), tail.Time.UTC().Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
		if tm.After(month) {
			month = tm
		}
	}
	return month
}

// lastUsedMonth 记录本进程已分配过的最大月份（保证不重复）。
var lastUsedMonth atomic.Value // time.Time

// freshMonth 返回一个**保证可写**的月份：`max(链尾月份 + 1, 2040-01)`，并现场建好该月分区。
//
// 为什么不再用"预建池 + 顺序分配"：`-p 8` 下别的包/别的用例会把全局链尾推高，
// 池里分配的月份可能已经**早于链尾**（撞下界守卫）或分区被别人 `DROP`（撞 23514）。
// 每次现取链尾 → +1 个月 → `EnsurePartitions`（同事务取链锁，不与其他写者死锁）→
// **断言分区真的存在**，把这类错误在"拿到月份那一刻"就炸掉，而不是等 INSERT 抛 23514。
func freshMonth(t *testing.T, db *gorm.DB) time.Time {
	t.Helper()
	var tail sql.NullTime
	if err := db.Raw(`SELECT max(occurred_at) FROM audit_logs`).Scan(&tail).Error; err != nil {
		t.Fatalf("read tail: %v", err)
	}
	// 预建范围以 `TestMain` 里按 `max(occurred_at)+1` 算出的基准月为准（不是硬编码 2040-01）：
	// 多轮跑下来链尾会漂移到 2043+，硬编码下界会把"其实已预建"的月份判成越界。
	lo := time.Date(partitionStartYear, 1, 1, 0, 0, 0, 0, time.UTC)
	if base, ok := poolBaseMonth.Load().(time.Time); ok && !base.IsZero() {
		lo = base
	}
	hi := lo.AddDate(0, partitionMonthCount-1, 0) // 最后一个**已预建**月份
	month := lo
	if tail.Valid && !tail.Time.IsZero() {
		tm := time.Date(tail.Time.UTC().Year(), tail.Time.UTC().Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
		if tm.After(month) {
			month = tm
		}
	}
	// 还要**大于本进程已用过的最大月份**：`cleanupMonth` 删行会让链尾回退，
	// 只用 `tail+1` 会把同一个月份发给两个用例（实测"期望 3 实际 7"、锚点对不上）。
	if last, ok := lastUsedMonth.Load().(time.Time); ok && !last.IsZero() && !month.After(last) {
		month = last.AddDate(0, 1, 0)
	}
	lastUsedMonth.Store(month)
	// **运行期零 DDL**：不在这里建分区（`CREATE TABLE … PARTITION OF` 取 ACCESS EXCLUSIVE，
	// 与并发 INSERT 的 ROW EXCLUSIVE 相撞会 40P01）。分区全部由 `TestMain` 预建；
	// 越界就当场 Fatalf 并打出范围与链尾，不等到 INSERT 抛 23514。
	if month.Before(lo) || month.After(hi) {
		t.Fatalf("可用月份 %s 超出预建范围 [%s, %s]（链尾=%v）——调大 partitionMonthCount",
			month.Format("200601"), lo.Format("200601"), hi.Format("200601"), tail)
	}
	name := fmt.Sprintf("audit_logs_p%s", month.Format("200601"))
	var exists bool
	if err := db.Raw(`SELECT to_regclass(?) IS NOT NULL`, name).Scan(&exists).Error; err != nil {
		t.Fatalf("check %s: %v", name, err)
	}
	if !exists {
		t.Fatalf("分区 %s 不在预建集合里（TestMain 应预建 %d 个月）", name, partitionMonthCount)
	}
	return month
}

// requirePartition 保证"该月分区存在"且**该月晚于全局链尾月份**，返回实际可用的月份。
//
// 为什么返回月份：`Append` 有下界守卫（`occurred_at` 早于链尾所在分区月初即拒），
// 而全局链尾会被**其它用例**推高（同一 DB、同一张表）。用例拿到的月份若已被推高，
// 这里就把它抬到"链尾月份 + 1"——这样**用例执行顺序无关**，连跑多轮也不会红
// （实测两种红：`23514 no partition` 与 `occurred_at predates the tail partition month`）。
func requirePartition(t *testing.T, db *gorm.DB, month time.Time) time.Time {
	t.Helper()
	for i := 0; i < 5; i++ {
		// ① 抬高到链尾之后。
		var tail sql.NullTime
		if err := db.Raw(`SELECT max(occurred_at) FROM audit_logs`).Scan(&tail).Error; err != nil {
			t.Fatalf("read tail month: %v", err)
		}
		if tail.Valid && !tail.Time.IsZero() {
			tailMonth := time.Date(tail.Time.UTC().Year(), tail.Time.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
			if month.Before(tailMonth.AddDate(0, 1, 0)) {
				month = tailMonth.AddDate(0, 1, 0)
			}
		}
		// ② 保证分区存在（缺失时现场自愈；同事务先取链锁，不会与写入死锁）。
		name := fmt.Sprintf("audit_logs_p%s", month.Format("200601"))
		var exists bool
		if err := db.Raw(`SELECT to_regclass(?) IS NOT NULL`, name).Scan(&exists).Error; err != nil {
			t.Fatalf("check partition: %v", err)
		}
		if !exists {
			// **运行期零 DDL**：不再现场建分区；缺了就当场失败并打出范围与链尾，
			// 便于一眼看出是"预建窗口不够"还是"月份被推远"。
			var tr sql.NullTime
			_ = db.Raw(`SELECT max(occurred_at) FROM audit_logs`).Scan(&tr).Error
			t.Fatalf("分区 %s 不存在（预建窗口 %d 个月，链尾=%v）：调大 partitionMonthCount 或改用 freshMonth",
				name, partitionMonthCount, tr)
		}
		return month
	}
	t.Fatalf("分区/月份调整 5 轮后仍不可用（month=%s）", month.Format("200601"))
	return month
}

// seedSegmentAnchor 把"本段锚点"写成现存首条的 `prev_tag`。
//
// 测试月份只是**全局链**里的一段（`Append` 读的是全局链尾），所以复核该段时的锚点
// 必须是"段首那条的 `prev_tag`"，不是 nil。写进 `runtime_config` 是为了让 `Verifier`
// （它从锚点表读）能按生产路径校验。
func seedSegmentAnchor(t *testing.T, chain *Chain, db *gorm.DB, anchors *AnchorStore, month time.Time) {
	t.Helper()
	records, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) == 0 {
		return
	}
	if err := anchors.Put(context.Background(), month, records[0].PrevTag); err != nil {
		t.Fatalf("seed segment anchor: %v", err)
	}
}

func appendN(t *testing.T, chain *Chain, db *gorm.DB, month time.Time, n int) ([]Record, string) {
	t.Helper()
	out := make([]Record, 0, n)
	marker := "appendn-" + uuid.NewString()[:8]
	for i := 0; i < n; i++ {
		rec := Record{
			ActorRole: "sec_admin", Action: "policy.update", Result: "success",
			TargetType: "client", TargetID: fmt.Sprintf("%s-%d", marker, i), ClientID: "s10-audit",
			Before:     map[string]any{"n": fmt.Sprintf("%d", i)},
			After:      map[string]any{"n": fmt.Sprintf("%d", i+1)},
			TraceID:    "trace-" + uuid.NewString()[:8],
			OccurredAt: month.Add(time.Duration(i) * time.Second),
		}
		if _, err := appendWithReanchor(chain, db, rec); err != nil {
			t.Fatalf("Append #%d（月份=%s）: %v", i, month.Format("200601"), err)
		}
		out = append(out, rec)
	}
	return out, marker
}

// loadByMarker 只取"本轮写入"的行：`-count>1` 时同一月可能有上一轮的行，
// 按月份/时间范围统计会把别人算进来（Gate 实测"期望 3 实际 6/9"）。
func loadByMarker(t *testing.T, chain *Chain, db *gorm.DB, month time.Time, marker string) []Record {
	t.Helper()
	all, err := chain.Load(context.Background(), db, month, month.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	out := make([]Record, 0, len(all))
	for _, rec := range all {
		if strings.HasPrefix(rec.TargetID, marker) {
			out = append(out, rec)
		}
	}
	return out
}

func cleanupMonth(t *testing.T, db *gorm.DB, month time.Time) {
	t.Helper()
	to := month.AddDate(0, 1, 0)
	t.Cleanup(func() {
		testdb.RequireTestDB(t, db) // 删改类动作的入口护栏（库名必须 _test）
		// **schema 限定**，不依赖 search_path（隔离开启时是 audit_test）。
		_ = db.Exec(fmt.Sprintf(`DELETE FROM %s.audit_logs WHERE occurred_at >= ? AND occurred_at < ?`,
			auditSchemaForTests), month, to).Error
		// 锚点也要清：跨运行复用同一"测试月份"时，旧锚会让"空月份不写锚"的断言假红
		// （实测踩到：上一轮跑过的月份留下了 anchor，这一轮读到它）。
		_ = db.Exec(fmt.Sprintf(`DELETE FROM %s.runtime_config WHERE key = ?`, auditSchemaForTests),
			AnchorKey(month)).Error
	})
}

// AC-AUD-03：删中间一条 → 断链可检出（`prev_tag_mismatch`），且**整链复核器**能报出来。

// AC-AUD-05：篡改一行内容（不改 tag）→ `integrity_tag_mismatch`；同时验指标递增。

// AC-AUD-04：**并发 100 写** → 全部落库、链完整（逐条 hash 接上、tag 无重复），
// 且整链复核通过（这是"并发下不丢链不重号"的唯一证明）。
// AC-AUD-11：锚点写入/读取 + 复核通过后 `AnchorAge` 归零（指标基准）。

// 锚点错误 → 首条即断（"尾部被整段删除"只能靠锚点发现）。

// allowMutationForTest 临时关闭某月分区的 append-only 触发器（仅供构造"已被篡改"的场景）。
//
// 生产环境**不许**这么做（`audit_logs_append_only` 就是判据 AC-AUD-02 的实现）；
// 用例结束后立刻恢复，确保不留后门。
func allowMutationForTest(t *testing.T, db *gorm.DB, month time.Time) {
	t.Helper()
	// **安全护栏**：这里要关 append-only 触发器造"被篡改的库"，属于删改类动作。
	testdb.RequireTestDB(t, db)
	name := fmt.Sprintf("%s.audit_logs_p%s", auditSchemaForTests, month.Format("200601"))
	stmt := fmt.Sprintf(`ALTER TABLE %s DISABLE TRIGGER USER`, name)
	if err := db.Exec(stmt).Error; err != nil {
		t.Fatalf("disable trigger on %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = db.Exec(fmt.Sprintf(`ALTER TABLE %s ENABLE TRIGGER USER`, name)).Error
	})
}

// encodeAnchorForTest 把 tag 编成锚点列里的 JSON 字符串（与 AnchorStore.Put 同格式）。
func encodeAnchorForTest(tag []byte) string {
	out, _ := json.Marshal(base64.StdEncoding.EncodeToString(tag))
	return string(out)
}

// AC-AUD-11（Job 语义）：空月份不写锚点；真实 Job 在 ticker 上跑一遍（含锚点写入）。

// 记录的两种"可选字段形态"都要能落库并验过：有 actor_user_id + 有 before/after，以及全空。
// （覆盖 `nullUUID` / `jsonOrNil` 的两侧分支——它们写错会让"匿名操作"或"无变更快照"
// 的审计事实落不进去，而那两类恰恰是最需要留痕的。）

func mustID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return id
}

// Spec 裁定（2026-11-10）的两条都要钉住：
//  1. `occurred_at` **原样入库**（不许被抬高——它是取证时间、分区键，且不可改）；
//  2. `occurred_at` **参与 `integrity_tag`**（哈希覆盖原值，改它 tag 必变）。

// 下界守卫：`occurred_at` 早于链尾分区月初 → 拒绝（否则会插进上月分区、破跨分区链接）。

// 覆盖率兜底用的两条真用例（Gate 要求 audit ≥92%，贴线一次 abort 就会双红）：
//  1. `Load` 空区间返回空切片（不是 nil、不报错）；
//  2. `AnchorStore.Put` 幂等覆盖（同月写两次，读回后写的那个）。

// 链序真用例（Gate 要求：92% 必须来自真路径）：**同月内 `occurred_at` 微秒级回退**必须被接受，
// 且链仍按 `created_at` 串起来（这正是"抬 created_at 而不是 occurred_at"要证明的行为）。

// 两条**真路径**补口（Gate 要求 92% 来自真路径，不是灌水）：
//  1. `RunRange` 扫一个"分区存在但无行"的区间：0 条、通过、且不写锚（`AnchorCurrentMonth` 的早退分支）；
//  2. `Append` 在**同一事务内连续两条**（`prev` 从库里读到刚写的那条，链自接）。
