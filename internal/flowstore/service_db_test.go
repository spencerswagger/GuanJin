//go:build integration

package flowstore

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 集成测试（**需要真 PG**）：没有 `GUANJIN_TEST_DSN` 就明确跳过。
//
// 为什么必须真库：本包的全部价值在"事务 + 行锁 + 库时间"上——用 mock 断言这些
// 等于断言自己写的 mock。I-F3（库时间）/ I-F4（一次性）/ I-F5（不丢 amr）三条
// **只能在真 PG 上被证伪**。
//
// 前置：目标库已按 migrations 手动迁移（P1：迁移不随应用启动、也不由测试代跑）。
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := firstNonEmpty(os.Getenv("GUANJIN_TEST_DSN"), os.Getenv("GUANJIN_DEV_DSN"))
	if dsn == "" {
		t.Skip("GUANJIN_TEST_DSN 未设置：跳过需要真 PostgreSQL 的集成测试")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{DSN: dsn})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Ping(ctx, db); err != nil {
		t.Fatalf("ping test db: %v", err)
	}
	if !db.Migrator().HasTable("flows") {
		t.Fatalf("测试库缺少 flows 表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN")
	}
	t.Cleanup(func() { _ = store.Close(db) })
	return db
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	return id
}

// fixture 建一组隔离数据（唯一 client_id / username），结束时按 id 清理。
type fixture struct {
	userID   uuid.UUID
	clientID string
}

func newFixture(t *testing.T, db *gorm.DB) *fixture {
	t.Helper()
	suffix := uuid.NewString()
	f := &fixture{userID: mustV7(t), clientID: "s3-fs-" + suffix}
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier) VALUES (?, ?, 'L2')`,
		f.clientID, "flowstore test client").Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		f.userID, "fs-"+suffix[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM flows WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM sessions WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, f.userID).Error
	})
	return f
}

// newSession 建一条真实会话：`flows.session_id` 有 FK，拿随机 uuid 冒充会撞外键。
func (f *fixture) newSession(t *testing.T, db *gorm.DB) uuid.UUID {
	t.Helper()
	id := mustV7(t)
	err := db.Exec(`INSERT INTO sessions (id, user_id, client_id, acr, expires_at, idle_expires_at)
		VALUES (?, ?, ?, 'AAL1', now() + interval '1 hour', now() + interval '30 min')`,
		id, f.userID, f.clientID).Error
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return id
}

// testTTL 刻意取 90s（不是 600）：万一实现里偷偷塞了 600 常量，这条断言立刻能发现。
const testTTL = 90 * time.Second

func newService(t *testing.T, db *gorm.DB) *Service {
	t.Helper()
	svc, err := New(db, store.NewFlowRepo(db), testTTL)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func (f *fixture) startFlow(t *testing.T, db *gorm.DB) *model.Flow {
	t.Helper()
	svc := newService(t, db)
	uid := f.userID
	fl, err := svc.Start(context.Background(), StartRequest{
		UserID:    &uid,
		ClientID:  f.clientID,
		TargetACR: "AAL2",
	})
	if err != nil {
		t.Fatalf("start flow: %v", err)
	}
	return fl
}

func reload(t *testing.T, db *gorm.DB, id uuid.UUID) *model.Flow {
	t.Helper()
	var f model.Flow
	if err := db.Where("id = ?", id).Take(&f).Error; err != nil {
		t.Fatalf("reload flow: %v", err)
	}
	return &f
}

// TestDBFlowstoreStartUsesDBClock：`expires_at` 必须等于「库时间 + TTL」。
// 用应用时钟凑值会让多副本的 TTL 忽长忽短（I-F3 的判定用的是库时间 now()）。
func TestDBFlowstoreStartUsesDBClock(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)

	fl := f.startFlow(t, db)
	if fl.TargetACR != "AAL2" {
		t.Fatalf("target_acr: want AAL2, got %q", fl.TargetACR)
	}

	var remaining float64
	if err := db.Raw(`SELECT extract(epoch FROM expires_at - now()) FROM flows WHERE id = ?`,
		fl.ID).Scan(&remaining).Error; err != nil {
		t.Fatalf("read remaining ttl: %v", err)
	}
	// 允许 5s 抖动（连接与扫描耗时），但 600s 常量会直接落在这里之外。
	if remaining < testTTL.Seconds()-5 || remaining > testTTL.Seconds()+5 {
		t.Fatalf("expires_at 应为 now()+%v，实测剩 %.1fs（怀疑用了应用时钟或 600 常量）",
			testTTL, remaining)
	}
	if fl.TraceID != nil {
		t.Fatalf("未给 trace_id 时不应凭空写一个：%v", *fl.TraceID)
	}
}

// TestDBFlowstoreStartReusesActiveFlow：CH-7⑤ 的「先查后建」——
// 同一用户对同一 client 再开流，返回同一条 `flow_id`（续流），而不是撞唯一索引报 409。
func TestDBFlowstoreStartReusesActiveFlow(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)

	first := f.startFlow(t, db)
	second := f.startFlow(t, db)
	if first.ID != second.ID {
		t.Fatalf("再开流应复用同一条：first=%s second=%s", first.ID, second.ID)
	}
}

// TestDBFlowstoreAdvanceAccumulates：推进两步 → amr 单调累加、步骤只追加。
func TestDBFlowstoreAdvanceAccumulates(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	fl := f.startFlow(t, db)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := svc.Advance(ctx, AdvanceRequest{FlowID: fl.ID, Factor: "pwd", At: now}); err != nil {
		t.Fatalf("advance pwd: %v", err)
	}
	got, err := svc.Advance(ctx, AdvanceRequest{FlowID: fl.ID, Factor: "totp", At: now.Add(time.Second)})
	if err != nil {
		t.Fatalf("advance totp: %v", err)
	}
	if len(got.AchievedAMR) != 2 || got.AchievedAMR[0] != "pwd" || got.AchievedAMR[1] != "totp" {
		t.Fatalf("achieved_amr 应为 [pwd totp]，实际 %v", []string(got.AchievedAMR))
	}
	if len(got.CompletedSteps) != 2 {
		t.Fatalf("completed_steps 应有 2 步，实际 %d", len(got.CompletedSteps))
	}

	db2 := reload(t, db, fl.ID)
	if len(db2.AchievedAMR) != 2 || len(db2.CompletedSteps) != 2 {
		t.Fatalf("库里应为 2 因子 2 步，实际 amr=%v steps=%d",
			[]string(db2.AchievedAMR), len(db2.CompletedSteps))
	}
}

// TestDBFlowstoreAdvanceRejectsDuplicateStep：D3-F 的 `STEP_ALREADY_SATISFIED` 落点。
// 重点不只是"报错"，而是**库里一步都没多**（重输不能把 amr 计数刷高）。
func TestDBFlowstoreAdvanceRejectsDuplicateStep(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	fl := f.startFlow(t, db)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := svc.Advance(ctx, AdvanceRequest{FlowID: fl.ID, Factor: "pwd", At: now}); err != nil {
		t.Fatalf("advance pwd: %v", err)
	}
	_, err := svc.Advance(ctx, AdvanceRequest{FlowID: fl.ID, Factor: "pwd", At: now.Add(time.Second)})
	if !errors.Is(err, flow.ErrStepAlreadySatisfied) {
		t.Fatalf("want flow.ErrStepAlreadySatisfied, got %v", err)
	}

	got := reload(t, db, fl.ID)
	if len(got.CompletedSteps) != 1 || len(got.AchievedAMR) != 1 {
		t.Fatalf("重输不得写入：steps=%d amr=%v", len(got.CompletedSteps), []string(got.AchievedAMR))
	}
}

// TestDBFlowstoreAdvanceExpiredKeepsRow：I-F3/I-F6——过期只是置 `expired`，**行不删**，
// 这样 `FLOW_EXPIRED` 才稳定（过期即删会让它和 `FLOW_NOT_FOUND` 时隐时现，AC-FLOW-10 flaky）。
func TestDBFlowstoreAdvanceExpiredKeepsRow(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	fl := f.startFlow(t, db)
	ctx := context.Background()

	if err := db.Exec(`UPDATE flows SET expires_at = now() - interval '1 second' WHERE id = ?`,
		fl.ID).Error; err != nil {
		t.Fatalf("force expire: %v", err)
	}

	_, err := svc.Advance(ctx, AdvanceRequest{FlowID: fl.ID, Factor: "pwd", At: time.Now().UTC()})
	if !errors.Is(err, ErrFlowExpired) {
		t.Fatalf("want ErrFlowExpired, got %v", err)
	}

	got := reload(t, db, fl.ID) // 行必须还在（能读出来就说明没被删）
	if got.Status != string(flow.StatusExpired) {
		t.Fatalf("status: want expired, got %q", got.Status)
	}
	if len(got.AchievedAMR) != 0 {
		t.Fatalf("过期流不得累加 amr：%v", []string(got.AchievedAMR))
	}
}

// TestDBFlowstoreConsumeOnce：I-F4——`completed` 与 `consumed_at` 同真同假，二次消费拒绝。
func TestDBFlowstoreConsumeOnce(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	fl := f.startFlow(t, db)
	session := f.newSession(t, db)
	ctx := context.Background()

	if _, err := svc.Advance(ctx, AdvanceRequest{FlowID: fl.ID, Factor: "pwd", At: time.Now().UTC()}); err != nil {
		t.Fatalf("advance: %v", err)
	}
	uid, sid := f.userID, session
	got, err := svc.Consume(ctx, ConsumeRequest{FlowID: fl.ID, UserID: &uid, SessionID: &sid})
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if got.Status != string(flow.StatusCompleted) || got.ConsumedAt == nil {
		t.Fatalf("completed 与 consumed_at 必须同时成立：status=%q consumed=%v", got.Status, got.ConsumedAt)
	}
	if got.SessionID == nil || *got.SessionID != session {
		t.Fatalf("session_id 应回填为 %s", session)
	}

	if _, err := svc.Consume(ctx, ConsumeRequest{FlowID: fl.ID, UserID: &uid, SessionID: &sid}); !errors.Is(err, ErrFlowConsumed) {
		t.Fatalf("二次消费 want ErrFlowConsumed, got %v", err)
	}
	// 消费后不得再推进（终态不可回退）。
	if _, err := svc.Advance(ctx, AdvanceRequest{FlowID: fl.ID, Factor: "totp", At: time.Now().UTC()}); !errors.Is(err, ErrFlowNotActive) {
		t.Fatalf("已完成的流 want ErrFlowNotActive, got %v", err)
	}
}

// TestDBFlowstoreConsumeRequiresUser：I-F1——`completed` 必须有 `user_id`。
// 在 Go 里先挡，而不是等 DB CHECK 回一个说不清的 400。
func TestDBFlowstoreConsumeRequiresUser(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	ctx := context.Background()

	anon, err := svc.Start(ctx, StartRequest{ClientID: f.clientID, TargetACR: "AAL1"})
	if err != nil {
		t.Fatalf("start anonymous flow: %v", err)
	}
	if anon.UserID != nil {
		t.Fatalf("匿名流的 user_id 必须为 NULL")
	}
	if _, err := svc.Consume(ctx, ConsumeRequest{FlowID: anon.ID}); !errors.Is(err, ErrUserRequired) {
		t.Fatalf("want ErrUserRequired, got %v", err)
	}

	uid := f.userID
	got, err := svc.Consume(ctx, ConsumeRequest{FlowID: anon.ID, UserID: &uid})
	if err != nil {
		t.Fatalf("带 user_id 消费应成功：%v", err)
	}
	if got.UserID == nil || *got.UserID != f.userID {
		t.Fatalf("user_id 应回填为 %s", f.userID)
	}
}

// TestDBFlowstoreConsumeSessionBackfillOnce：I-F7——`completed` 行允许**一次**
// `session_id: NULL → 值`（S5 完成、S6 才建会话的交接期），之后不得再改。
func TestDBFlowstoreConsumeSessionBackfillOnce(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	fl := f.startFlow(t, db)
	session := f.newSession(t, db)
	other := f.newSession(t, db)
	uid := f.userID
	ctx := context.Background()

	if _, err := svc.Consume(ctx, ConsumeRequest{FlowID: fl.ID, UserID: &uid}); err != nil {
		t.Fatalf("consume without session: %v", err)
	}
	got, err := svc.Consume(ctx, ConsumeRequest{FlowID: fl.ID, UserID: &uid, SessionID: &session})
	if err != nil {
		t.Fatalf("第一次回填 session 应允许：%v", err)
	}
	if got.SessionID == nil || *got.SessionID != session {
		t.Fatalf("session_id 应回填为 %s", session)
	}
	if _, err := svc.Consume(ctx, ConsumeRequest{FlowID: fl.ID, UserID: &uid, SessionID: &other}); !errors.Is(err, ErrSessionImmutable) {
		t.Fatalf("换一个 session 回填 want ErrSessionImmutable, got %v", err)
	}
}

// TestDBFlowstoreConcurrentAdvanceKeepsAllAMR：I-F5 的核心用例——
// 两个并发请求各自"通过一个因子"，行锁必须让两次累加都落到库里（不丢 amr）。
func TestDBFlowstoreConcurrentAdvanceKeepsAllAMR(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	fl := f.startFlow(t, db)
	ctx := context.Background()

	factors := []string{"pwd", "totp"}
	errs := make([]error, len(factors))
	var wg sync.WaitGroup
	for i, factor := range factors {
		wg.Add(1)
		go func(i int, factor string) {
			defer wg.Done()
			_, errs[i] = svc.Advance(ctx, AdvanceRequest{
				FlowID: fl.ID, Factor: factor, At: time.Now().UTC(),
			})
		}(i, factor)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("advance %s: %v", factors[i], err)
		}
	}

	got := reload(t, db, fl.ID)
	if len(got.AchievedAMR) != len(factors) || len(got.CompletedSteps) != len(factors) {
		t.Fatalf("并发补步丢了 amr：amr=%v steps=%d", []string(got.AchievedAMR), len(got.CompletedSteps))
	}
	seen := map[string]bool{}
	for _, a := range got.AchievedAMR {
		seen[a] = true
	}
	for _, want := range factors {
		if !seen[want] {
			t.Fatalf("achieved_amr 缺 %q：%v", want, []string(got.AchievedAMR))
		}
	}
}

// TestDBFlowstoreAbandonedFlowCannotAdvance：会话撤销把流置 `abandoned` 之后，
// 这条流就只能是"不在"——不能靠补步复活（撤销要实时生效，不给回头路）。
func TestDBFlowstoreAbandonedFlowCannotAdvance(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	fl := f.startFlow(t, db)
	session := f.newSession(t, db)
	ctx := context.Background()

	uid, sid := f.userID, session
	if _, err := svc.Consume(ctx, ConsumeRequest{FlowID: fl.ID, UserID: &uid, SessionID: &sid}); err != nil {
		t.Fatalf("consume: %v", err)
	}
	// 模拟会话撤销的级联（Core 侧调 AbandonBySession，只作用于 active 行）。
	if _, err := store.NewFlowRepo(db).AbandonBySession(ctx, session); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if _, err := svc.Advance(ctx, AdvanceRequest{FlowID: fl.ID, Factor: "pwd", At: time.Now().UTC()}); !errors.Is(err, ErrFlowNotActive) {
		t.Fatalf("want ErrFlowNotActive, got %v", err)
	}
}

// TestDBFlowstoreStartDefaultsToAAL1：`target_acr` 空 → `AAL1`。
// 这是 D5 的列默认值，**在应用层显式写出**（不靠 DB 默认），否则"空值"会被悄悄当成某个级别。
func TestDBFlowstoreStartDefaultsToAAL1(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)

	trace := "trace-s3-flowstore"
	fl, err := svc.Start(context.Background(), StartRequest{ClientID: f.clientID, TraceID: &trace})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if fl.TargetACR != "AAL1" {
		t.Fatalf("target_acr 默认应为 AAL1，实际 %q", fl.TargetACR)
	}
	// trace_id 只在创建时写一次（D4-C(4)），补步不得覆盖。
	if fl.TraceID == nil || *fl.TraceID != trace {
		t.Fatalf("trace_id 应写入 %q，实际 %v", trace, fl.TraceID)
	}
}

// TestDBFlowstoreConsumeExpiredFlow：过期流不能消费（否则"过期还能换令牌"就是漏洞）。
func TestDBFlowstoreConsumeExpiredFlow(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	fl := f.startFlow(t, db)
	ctx := context.Background()

	if err := db.Exec(`UPDATE flows SET expires_at = now() - interval '1 second' WHERE id = ?`,
		fl.ID).Error; err != nil {
		t.Fatalf("force expire: %v", err)
	}
	uid := f.userID
	if _, err := svc.Consume(ctx, ConsumeRequest{FlowID: fl.ID, UserID: &uid}); !errors.Is(err, ErrFlowExpired) {
		t.Fatalf("want ErrFlowExpired, got %v", err)
	}
	// 同样要落库：过期置位不能因返回错误而回滚。
	got := reload(t, db, fl.ID)
	if got.Status != string(flow.StatusExpired) {
		t.Fatalf("status: want expired, got %q", got.Status)
	}
}

// TestDBFlowstoreConsumeSessionSameValueIsIdempotent：同一个 session 重试 → 仍按"已消费"处理
// （幂等提示，不是"换会话"的越权尝试，两者错误不同才对）。
func TestDBFlowstoreConsumeSessionSameValueIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	fl := f.startFlow(t, db)
	session := f.newSession(t, db)
	uid, sid := f.userID, session
	ctx := context.Background()

	if _, err := svc.Consume(ctx, ConsumeRequest{FlowID: fl.ID, UserID: &uid, SessionID: &sid}); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if _, err := svc.Consume(ctx, ConsumeRequest{FlowID: fl.ID, UserID: &uid, SessionID: &sid}); !errors.Is(err, ErrFlowConsumed) {
		t.Fatalf("want ErrFlowConsumed, got %v", err)
	}
}

// TestDBFlowstoreAdvanceAbandonedFlow：会话已被撤销（流置 abandoned）→ 不能再推进。
func TestDBFlowstoreAdvanceAbandonedFlow(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	fl := f.startFlow(t, db)
	ctx := context.Background()

	if err := db.Exec(`UPDATE flows SET status = 'abandoned' WHERE id = ?`, fl.ID).Error; err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if _, err := svc.Advance(ctx, AdvanceRequest{FlowID: fl.ID, Factor: "pwd", At: time.Now().UTC()}); !errors.Is(err, ErrFlowNotActive) {
		t.Fatalf("want ErrFlowNotActive, got %v", err)
	}
}
