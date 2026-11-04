//go:build integration

package flowstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 适配层（`adapter.go`）的**真库**用例。
//
// 为什么必须放在本包、而且必须真库：
//   - 只有 `package flowstore` 的测试二进制才会给 adapter.go 记覆盖率。编排层的
//     `internal/flow` 用例虽然也调 adapter，但那份 profile 只统计 `internal/flow`，
//     adapter 在那里面永远是 0% —— 这正是 S5 合入后 flowstore 掉到 78% 的原因。
//   - 适配层是「编排 ↔ 持久化」的唯一缝：它换的是**类型**，不该改语义。所以断言要落在
//     库里的行（`achieved_amr` / `completed_steps` / `session_id`），而不是返回值：
//     返回值对、库里错，说明适配层把 half-done 的结果当成完成了。
//
// 覆盖的语义路径（D5）：
//   - I-F2/I-F5：锁读后校验 → amr 单调累加 / 步骤只追加 → 写回；
//   - I-F3/I-F6：过期只置 `expired`、**行不删**（否则 `FLOW_EXPIRED` 会时隐时现）；
//   - I-F4/I-F7：`completed` 与 `consumed_at` 同真同假，`session_id` 只允许回填一次；
//   - 两段式清理的**选择条件**只命中 `status='expired'`（这里只验选择，不真删——本包的
//     用例与其他包的用例共用同一个测试库，真删会误伤别的包的夹具；DELETE 行为已由
//     `internal/store` 的 `TestDBFlowCleanupStage2*` 钉住）。

func newAdapter(t *testing.T, db *gorm.DB) *Adapter {
	t.Helper()
	a, err := NewAdapter(newService(t, db), store.NewFlowRepo(db))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	return a
}

// 入参守卫（不需要数据库）：装配期缺依赖必须当场失败，不能等到第一次登录才炸。
func TestNewAdapterRequiresBothDeps(t *testing.T) {
	if _, err := NewAdapter(nil, nil); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("nil 依赖应报 ErrMissingDependency，实际 %v", err)
	}
	if _, err := NewAdapter(&Service{}, nil); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("缺 repo 应报 ErrMissingDependency，实际 %v", err)
	}
	if _, err := NewAdapter(nil, store.NewFlowRepo(nil)); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("缺 Service 应报 ErrMissingDependency，实际 %v", err)
	}
}

// TestDBAdapterGet：读一条流（不加锁，仅用于算 Descriptor）；查不到统一成
// `flow.ErrFlowNotFound`（编排层不该知道"仓储没这行"和"流不存在"是两回事）。
func TestDBAdapterGet(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	a := newAdapter(t, db)
	ctx := context.Background()

	fl := f.startFlow(t, db)

	got, err := a.Get(ctx, fl.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != fl.ID || got.TargetACR != "AAL2" || got.Status != string(flow.StatusActive) {
		t.Fatalf("读到的行不对：id=%s acr=%s status=%s", got.ID, got.TargetACR, got.Status)
	}

	if _, err := a.Get(ctx, mustV7(t)); !errors.Is(err, flow.ErrFlowNotFound) {
		t.Fatalf("不存在的流应报 flow.ErrFlowNotFound，实际 %v", err)
	}

	// **非"查不到"的错误不得被吞成 NotFound**：否则一次库故障会对外表现为 404，
	// 用户看到的是"流转眼没了"，运维看到的是一切正常。
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.Get(canceled, fl.ID); err == nil || errors.Is(err, flow.ErrFlowNotFound) {
		t.Fatalf("上下文取消必须原样上抛（不能伪装成 ErrFlowNotFound），实际 %v", err)
	}
}

// TestDBAdapterStartReusesActiveFlow：语义完全委托 `Service.Start`（先查后建）——
// 同一用户对同一 client 再开流拿回**同一条** flow_id，且不新增行。
func TestDBAdapterStartReusesActiveFlow(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	a := newAdapter(t, db)
	ctx := context.Background()

	trace := "trace-adapter-start"
	uid := f.userID
	first, err := a.Start(ctx, flow.StartInput{
		UserID: &uid, ClientID: f.clientID, TargetACR: "AAL2", TraceID: &trace,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if first.TraceID == nil || *first.TraceID != trace {
		t.Fatalf("trace_id 应在开流时写入，实际 %v", first.TraceID)
	}

	second, err := a.Start(ctx, flow.StartInput{UserID: &uid, ClientID: f.clientID, TargetACR: "AAL2"})
	if err != nil {
		t.Fatalf("续流不该报错（撞唯一索引才是缺陷）：%v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("再开流应复用同一条：first=%s second=%s", first.ID, second.ID)
	}
	// 续流不得改写 trace_id（D4-C(4)：只有创建那次能写）。
	if second.TraceID == nil || *second.TraceID != trace {
		t.Fatalf("续流不得覆盖 trace_id，实际 %v", second.TraceID)
	}

	var n int64
	if err := db.Raw(`SELECT count(*) FROM flows WHERE client_id = ?`, f.clientID).Scan(&n).Error; err != nil {
		t.Fatalf("count flows: %v", err)
	}
	if n != 1 {
		t.Fatalf("复用路径不得新建行：flows=%d", n)
	}
}

// TestDBAdapterAdvanceAccumulates：I-F2/I-F5 的主路径——
// 锁读后校验 → amr 单调累加、步骤只追加（带 challenge_id）→ 写回。
//
// 另外钉住这里**已知的字段语义**（PR 里记的待 Edge 收口 P2）：
// `AdvanceInput.Factor` 是因子类型（`password`），而落库的是 `AMR`（`pwd`）；
// 适配层必须传 AMR，两列语义将来拆开时这条断言会提醒改哪边。
func TestDBAdapterAdvanceAccumulates(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	a := newAdapter(t, db)
	ctx := context.Background()
	now := time.Now().UTC()

	fl, err := a.Start(ctx, flow.StartInput{UserID: &f.userID, ClientID: f.clientID, TargetACR: "AAL2"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := a.Advance(ctx, flow.AdvanceInput{
		FlowID: fl.ID, Factor: factor.FactorPassword, AMR: "pwd", ChallengeID: "ch-adapter-1", At: now,
	}); err != nil {
		t.Fatalf("advance pwd: %v", err)
	}
	if _, err := a.Advance(ctx, flow.AdvanceInput{
		FlowID: fl.ID, Factor: factor.FactorTOTP, AMR: "totp", ChallengeID: "ch-adapter-2", At: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("advance totp: %v", err)
	}

	got := reload(t, db, fl.ID)
	if len(got.AchievedAMR) != 2 || got.AchievedAMR[0] != "pwd" || got.AchievedAMR[1] != "totp" {
		t.Fatalf("achieved_amr 应为 [pwd totp]（附录 B 词表），实际 %v", []string(got.AchievedAMR))
	}
	if len(got.CompletedSteps) != 2 {
		t.Fatalf("completed_steps 应有 2 步，实际 %d", len(got.CompletedSteps))
	}
	if got.CompletedSteps[0].ChallengeID != "ch-adapter-1" || got.CompletedSteps[1].ChallengeID != "ch-adapter-2" {
		t.Fatalf("challenge_id 必须原样追加，实际 %+v", got.CompletedSteps)
	}

	// 同一个 amr 再来一次 → 409 语义；库里一步都不许多（重输不能把计数刷高）。
	if _, err := a.Advance(ctx, flow.AdvanceInput{
		FlowID: fl.ID, Factor: factor.FactorPassword, AMR: "pwd", At: now.Add(2 * time.Second),
	}); !errors.Is(err, flow.ErrStepAlreadySatisfied) {
		t.Fatalf("重输 want flow.ErrStepAlreadySatisfied，实际 %v", err)
	}
	got = reload(t, db, fl.ID)
	if len(got.CompletedSteps) != 2 || len(got.AchievedAMR) != 2 {
		t.Fatalf("重输不得写入：steps=%d amr=%v", len(got.CompletedSteps), []string(got.AchievedAMR))
	}

	// 缺 AMR 必须在**碰数据库之前**拒：`achieved_amr` 里绝不能出现空串。
	if _, err := a.Advance(ctx, flow.AdvanceInput{FlowID: fl.ID, Factor: factor.FactorPassword, At: now}); !errors.Is(err, ErrFactorRequired) {
		t.Fatalf("缺 AMR want ErrFactorRequired，实际 %v", err)
	}
}

// TestDBAdapterAdvanceExpiredKeepsRow：I-F3/I-F6——过期只置 `status='expired'`、行**不删**，
// 且这条行才是两段式清理第二段（`DELETE … WHERE status='expired' AND updated_at < cutoff`）
// 的**唯一**候选；`completed` / `active` 行永远不该落进那个条件。
func TestDBAdapterAdvanceExpiredKeepsRow(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	a := newAdapter(t, db)
	ctx := context.Background()

	fl, err := a.Start(ctx, flow.StartInput{UserID: &f.userID, ClientID: f.clientID, TargetACR: "AAL2"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := db.Exec(`UPDATE flows SET expires_at = now() - interval '1 second' WHERE id = ?`,
		fl.ID).Error; err != nil {
		t.Fatalf("force expire: %v", err)
	}

	_, err = a.Advance(ctx, flow.AdvanceInput{
		FlowID: fl.ID, Factor: factor.FactorPassword, AMR: "pwd", At: time.Now().UTC(),
	})
	if !errors.Is(err, ErrFlowExpired) {
		t.Fatalf("want ErrFlowExpired，实际 %v", err)
	}

	got := reload(t, db, fl.ID) // 行还在 = 没被删（能读出来就说明还在）
	if got.Status != string(flow.StatusExpired) {
		t.Fatalf("status 应为 expired，实际 %q", got.Status)
	}
	if len(got.AchievedAMR) != 0 {
		t.Fatalf("过期流不得累加 amr，实际 %v", []string(got.AchievedAMR))
	}

	// 再拿一条已完成的行做对照：清理条件对 completed 必须为假。
	done, err := a.Start(ctx, flow.StartInput{UserID: &f.userID, ClientID: f.clientID, TargetACR: "AAL1"})
	if err != nil {
		t.Fatalf("start AAL1: %v", err)
	}
	if done.ID == fl.ID {
		t.Fatal("过期流不该被「先查后建」复用")
	}
	uid := f.userID
	if _, err := a.Consume(ctx, flow.ConsumeInput{FlowID: done.ID, UserID: &uid}); err != nil {
		t.Fatalf("consume: %v", err)
	}

	var sweepable int64
	if err := db.Raw(`SELECT count(*) FROM flows
		WHERE id = $1 AND status = 'expired' AND updated_at < now() + interval '1 day'`,
		fl.ID).Scan(&sweepable).Error; err != nil {
		t.Fatalf("count sweepable(expired): %v", err)
	}
	if sweepable != 1 {
		t.Fatalf("过期行应命中清理条件，实际 %d", sweepable)
	}
	if err := db.Raw(`SELECT count(*) FROM flows
		WHERE id = $1 AND status = 'expired' AND updated_at < now() + interval '1 day'`,
		done.ID).Scan(&sweepable).Error; err != nil {
		t.Fatalf("count sweepable(completed): %v", err)
	}
	if sweepable != 0 {
		t.Fatalf("completed 行不得命中清理条件（否则一次性证据被清掉），实际 %d", sweepable)
	}
}

// TestDBAdapterConsumeBackfillsSessionOnce：I-F4/I-F7——
// ① 首次消费：`completed` 与 `consumed_at` 同时成立，匿名流回填 user_id；
// ② `completed` 行允许**一次** `session_id: NULL → 值`（S5 完成、S6 才建会话的交接期）；
// ③ 回填后同值重试按"已消费"幂等，**异值**必须拒（能换就等于能劫持他人的流）。
func TestDBAdapterConsumeBackfillsSessionOnce(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	a := newAdapter(t, db)
	ctx := context.Background()

	// 匿名开流（注册场景）：user_id 只能是 NULL，消费时回填。
	fl, err := a.Start(ctx, flow.StartInput{ClientID: f.clientID, TargetACR: "AAL1"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if fl.UserID != nil {
		t.Fatal("匿名流的 user_id 必须为 NULL")
	}

	// I-F1：没有 user_id 的完成在应用层就挡住（不靠 DB CHECK 给一个说不清的 400）。
	if _, err := a.Consume(ctx, flow.ConsumeInput{FlowID: fl.ID}); !errors.Is(err, ErrUserRequired) {
		t.Fatalf("want ErrUserRequired，实际 %v", err)
	}

	uid := f.userID
	consumed, err := a.Consume(ctx, flow.ConsumeInput{FlowID: fl.ID, UserID: &uid})
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if consumed.Status != string(flow.StatusCompleted) || consumed.ConsumedAt == nil {
		t.Fatalf("completed 与 consumed_at 必须同时成立：status=%q consumed=%v", consumed.Status, consumed.ConsumedAt)
	}
	if consumed.SessionID != nil {
		t.Fatalf("这次没给 session，不该凭空写一个：%v", *consumed.SessionID)
	}

	session := f.newSession(t, db)
	backfilled, err := a.Consume(ctx, flow.ConsumeInput{FlowID: fl.ID, UserID: &uid, SessionID: &session})
	if err != nil {
		t.Fatalf("第一次回填 session 应允许：%v", err)
	}
	if backfilled.SessionID == nil || *backfilled.SessionID != session {
		t.Fatalf("session_id 应回填为 %s", session)
	}

	// 同值重试：幂等（仍按"已消费"），不是越权。
	if _, err := a.Consume(ctx, flow.ConsumeInput{FlowID: fl.ID, UserID: &uid, SessionID: &session}); !errors.Is(err, ErrFlowConsumed) {
		t.Fatalf("同值重试 want ErrFlowConsumed，实际 %v", err)
	}
	// 异值：I-F7 拒绝。
	other := f.newSession(t, db)
	if _, err := a.Consume(ctx, flow.ConsumeInput{FlowID: fl.ID, UserID: &uid, SessionID: &other}); !errors.Is(err, ErrSessionImmutable) {
		t.Fatalf("换 session want ErrSessionImmutable，实际 %v", err)
	}

	// 证据必须来自库：status / consumed_at / user_id / session_id 四列同时成立。
	row := reload(t, db, fl.ID)
	if row.Status != string(flow.StatusCompleted) || row.ConsumedAt == nil {
		t.Fatalf("库里应为 completed + consumed_at：status=%q consumed=%v", row.Status, row.ConsumedAt)
	}
	if row.UserID == nil || *row.UserID != f.userID {
		t.Fatalf("匿名流的 user_id 应回填为 %s，实际 %v", f.userID, row.UserID)
	}
	if row.SessionID == nil || *row.SessionID != session {
		t.Fatalf("session_id 应保持第一次回填的值 %s，实际 %v", session, row.SessionID)
	}
}

// TestDBAdapterConsumeUnknownFlow：不存在的流按"已消费/不在"处理，不新建、不静默成功。
func TestDBAdapterConsumeUnknownFlow(t *testing.T) {
	db := openTestDB(t)
	a := newAdapter(t, db)

	uid := mustV7(t)
	if _, err := a.Consume(context.Background(), flow.ConsumeInput{FlowID: uuid.Nil, UserID: &uid}); !errors.Is(err, ErrFlowIDRequired) {
		t.Fatalf("缺 flow_id want ErrFlowIDRequired，实际 %v", err)
	}
	if _, err := a.Consume(context.Background(), flow.ConsumeInput{FlowID: mustV7(t), UserID: &uid}); err == nil {
		t.Fatal("不存在的流不得消费成功")
	}
}
