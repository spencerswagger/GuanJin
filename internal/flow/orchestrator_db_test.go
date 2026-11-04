//go:build integration

package flow_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/flowstore"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 真库用例（**必须有真 PostgreSQL**）：不打 `t.Skip`，缺 DSN 即红（CI 强制 skip>0 = 红）。
//
// 这一组是 S5 的**端到端证据**：编排器 → flowstore（锁/并集/一次性）→ 真 `flows` 行。
// 为什么不能用单测替代：AC-FLOW-02/03/04/07 断言的 `details.flow` 与 `flows` 行必须
// **同时**成立（Descriptor 说 AAL1、库里 `achieved_amr` 也真是 `{pwd}`），
// 这只有在真库上跑一次才谈得上"证过"。

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		t.Fatalf("必须设置 GUANJIN_TEST_DSN（integration job 已注入）；不设就是不验证，按红处理")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{DSN: dsn})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	if err := store.Ping(ctx, db); err != nil {
		t.Fatalf("ping 测试库失败：%v", err)
	}
	t.Cleanup(func() { _ = store.Close(db) })
	return db
}

type fixture struct {
	db       *gorm.DB
	clientID string
	userID   uuid.UUID
}

// newFixture 建一个隔离的业务系统 + 一条**档位覆盖策略**（含禁用因子）。
func newFixture(t *testing.T, db *gorm.DB, tier string, disabled []string) *fixture {
	t.Helper()
	suffix := uuid.NewString()
	f := &fixture{db: db, clientID: "s5-" + suffix[:20]}
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier) VALUES (?, ?, ?)`,
		f.clientID, "S5 test client", tier).Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	policyID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	// `disabled_factors` 是 `text[] NOT NULL`：必须显式给 PG 数组字面量
	// （传 nil 会写 NULL → 23502；传 Go 字符串 "password" 会 22P02）。
	if err := db.Exec(`INSERT INTO login_policies
		(id, scope, client_id, min_acr, password_min_length, password_char_classes,
		 password_history_count, password_max_age_days, self_registration_allowed, disabled_factors)
		VALUES (?, 'client_override', ?, 'AAL1', 12, 3, 5, 90, true, ?::text[])`,
		policyID, f.clientID, pgArrayLiteral(disabled)).Error; err != nil {
		t.Fatalf("insert login_policies: %v", err)
	}
	uid, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	f.userID = uid
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`, uid, "s5-"+suffix[:12]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM auth_events WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM challenges WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM flows WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM login_policies WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, uid).Error
	})
	return f
}

// staticVerifier 是"永远成功"的因子实现：本组用例验的是**编排与落库**，
// 口令派生（PBKDF2）已在 S4a/S4b 的证据里，不必在这里再烧 100k 迭代。
type staticVerifier struct{ calls *int32 }

func (v staticVerifier) AuthenticatableSpec() factor.AuthenticatableSpec {
	return factor.AuthenticatableSpec{AsFirst: true}
}

func (v staticVerifier) BeginChallenge(context.Context, factor.ChallengeRequest) (factor.ChallengeTicket, error) {
	return factor.ChallengeTicket{}, nil
}

func (v staticVerifier) VerifyResponse(context.Context, factor.VerifyRequest) (factor.VerifyResult, error) {
	*v.calls++
	return factor.VerifyResult{Factor: factor.FactorPassword, AMR: "pwd", ACR: factor.ACRAAL1}, nil
}

func (f *fixture) orchestrator(t *testing.T) (*flow.Orchestrator, *int32) {
	t.Helper()
	ctx := context.Background()
	ttl, err := store.LoadFlowTTL(ctx, store.NewRuntimeConfigRepo(f.db))
	if err != nil {
		t.Fatalf("读 flow.ttl_seconds 失败（迁移 000015 应已 seed）：%v", err)
	}
	svc, err := flowstore.New(f.db, store.NewFlowRepo(f.db), ttl)
	if err != nil {
		t.Fatalf("flowstore.New: %v", err)
	}
	adapter, err := flowstore.NewAdapter(svc, store.NewFlowRepo(f.db))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	policyRepo := store.NewClientPolicyRepo(f.db)
	calls := new(int32)
	orc, err := flow.New(flow.Deps{
		Reader: adapter, Starter: adapter, Advancer: adapter, Consumer: adapter,
		Verifiers: map[factor.FactorType]factor.Authenticatable{factor.FactorPassword: staticVerifier{calls: calls}},
		Factors: []flow.FactorInfo{
			{Type: factor.FactorPassword, AMR: "pwd", AsFirst: true, Sole: factor.ACRAAL1, Combined: factor.ACRAAL2},
			{Type: factor.FactorTOTP, AMR: "totp", AsSecond: true, Sole: factor.ACRAAL1, Combined: factor.ACRAAL2},
		},
		DisabledFactors: func(ctx context.Context, clientID string) ([]factor.FactorType, error) {
			p, err := policyRepo.LoadRegistrationPolicy(ctx, nil, clientID)
			if err != nil {
				return nil, err
			}
			out := make([]factor.FactorType, 0, len(p.DisabledFactors))
			for _, s := range p.DisabledFactors {
				out = append(out, factor.FactorType(s))
			}
			return out, nil
		},
		TierOf: func(ctx context.Context, clientID string) (string, error) {
			p, err := policyRepo.LoadRegistrationPolicy(ctx, nil, clientID)
			if err != nil {
				return "", err
			}
			return p.Tier, nil
		},
		ClientOf: func(_ context.Context, clientID string) (flow.Client, error) {
			return flow.Client{ID: clientID, Name: "S5 test client"}, nil
		},
		Now: time.Now,
	})
	if err != nil {
		t.Fatalf("flow.New: %v", err)
	}
	return orc, calls
}

// amrOf 读库里的 achieved_amr（证据必须来自 DB，不是来自返回值）。
// pgArrayLiteral 把 []string 编成 PG 数组字面量（`{}` / `{password,totp}`）。
func pgArrayLiteral(items []string) string {
	if len(items) == 0 {
		return "{}"
	}
	return "{" + strings.Join(items, ",") + "}"
}

func (f *fixture) amrOf(t *testing.T, flowID uuid.UUID) []string {
	t.Helper()
	var raw string
	if err := f.db.Raw(`SELECT array_to_string(achieved_amr, ',') FROM flows WHERE id = ?`, flowID).Scan(&raw).Error; err != nil {
		t.Fatalf("读 achieved_amr: %v", err)
	}
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// AC-FLOW-02：目标 AAL2、只有口令 → 补一步后仍是 **ACR_INSUFFICIENT + details.flow**（不是错误页），
// 且库里 achieved_amr 真的是 `{pwd}`。
func TestAC_FLOW_02_InsufficientReturnsDescriptor(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L3", nil) // L3 → 目标 AAL2
	orc, _ := f.orchestrator(t)
	ctx := context.Background()

	begun, err := orc.Begin(ctx, flow.BeginInput{ClientID: f.clientID, UserID: &f.userID})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if begun.Flow.TargetACR != factor.ACRAAL2 {
		t.Fatalf("L3 档位目标应为 AAL2，实际 %s", begun.Flow.TargetACR)
	}
	if begun.Satisfied {
		t.Fatal("刚开的流不该达标")
	}

	_, err = orc.VerifyStep(ctx, flow.VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "ok", UserID: f.userID.String(),
	})
	var insuff *flow.ACRInsufficientError
	if !errors.As(err, &insuff) {
		t.Fatalf("未达标应返回 *ACRInsufficientError，实际 %T (%v)", err, err)
	}
	if insuff.Flow.CurrentACR != factor.ACRAAL1 || insuff.Flow.TargetACR != factor.ACRAAL2 {
		t.Fatalf("Descriptor 的 acr 不对：current=%s target=%s", insuff.Flow.CurrentACR, insuff.Flow.TargetACR)
	}
	if insuff.Flow.TerminalState != flow.TerminalNeedMore {
		t.Fatalf("应为 need_more，实际 %s", insuff.Flow.TerminalState)
	}
	code, ok := flow.CodeOf(err)
	if !ok || code != httpx.CodeACRInsufficient || httpx.HTTPStatus(code) != 403 {
		t.Fatalf("应映射 ACR_INSUFFICIENT/403，实际 %q", code)
	}
	if got := f.amrOf(t, begun.FlowID); len(got) != 1 || got[0] != "pwd" {
		t.Fatalf("库里 achieved_amr 应为 {pwd}（附录 B 词表），实际 %v", got)
	}
}

// AC-FLOW-03：`done` 的步再提交 → 409 `STEP_ALREADY_SATISFIED`，且不产生新挑战/不重复累加。
func TestAC_FLOW_03_StepAlreadySatisfied(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2", nil) // L2 → 目标 AAL1，一步即达标
	orc, calls := f.orchestrator(t)
	ctx := context.Background()

	begun, err := orc.Begin(ctx, flow.BeginInput{ClientID: f.clientID, UserID: &f.userID})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	res, err := orc.VerifyStep(ctx, flow.VerifyInput{FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "ok"})
	if err != nil || !res.Satisfied {
		t.Fatalf("L2 一步应达标：res=%+v err=%v", res, err)
	}
	if *calls != 1 {
		t.Fatalf("应校验一次，实际 %d", *calls)
	}

	_, err = orc.VerifyStep(ctx, flow.VerifyInput{FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "ok"})
	if !errors.Is(err, flow.ErrStepAlreadySatisfied) {
		t.Fatalf("应报 ErrStepAlreadySatisfied，实际 %v", err)
	}
	status, ok := flow.StatusFor(err)
	if !ok || status != 409 {
		t.Fatalf("应映射 409，实际 %d (ok=%v)", status, ok)
	}
	if *calls != 1 {
		t.Fatal("已完成的步不该再调 provider（省掉一次 PBKDF2）")
	}
	if got := f.amrOf(t, begun.FlowID); len(got) != 1 {
		t.Fatalf("amr 不得重复累加，实际 %v", got)
	}
}

// AC-FLOW-04：被禁用因子不出现在 steps/available，也不计入 current_acr。
func TestAC_FLOW_04_DisabledFactorNotCounted(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2", []string{"password"})
	orc, _ := f.orchestrator(t)
	ctx := context.Background()

	begun, err := orc.Begin(ctx, flow.BeginInput{ClientID: f.clientID, UserID: &f.userID})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if begun.Satisfied {
		t.Fatal("口令被禁用后不该达标")
	}
	if begun.Flow.CurrentACR != "" {
		t.Fatalf("被禁因子不得计入 current_acr，实际 %q", begun.Flow.CurrentACR)
	}
	for _, s := range begun.Flow.Steps {
		if s.Factor == factor.FactorPassword {
			t.Fatal("被禁因子不得出现在 steps")
		}
	}
	for _, a := range begun.Flow.AvailableFactors {
		if a == factor.FactorPassword {
			t.Fatal("被禁因子不得出现在 available_factors")
		}
	}
	if len(begun.Flow.DisabledFactors) != 1 || begun.Flow.DisabledFactors[0] != factor.FactorPassword {
		t.Fatalf("disabled_factors 应回显，实际 %v", begun.Flow.DisabledFactors)
	}

	// 直接提交被禁因子 → FACTOR_DISABLED（不是"就当他输了"）。
	if _, err := orc.VerifyStep(ctx, flow.VerifyInput{
		FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "ok",
	}); !errors.Is(err, flow.ErrFactorDisabled) {
		t.Fatalf("应报 ErrFactorDisabled，实际 %v", err)
	}
}

// AC-FLOW-07：client 禁用 password + 目标 AAL2 → **200 + terminal_state=unreachable**
// （不是错误码，也不是"一直 need_more"）。
func TestAC_FLOW_07_Unreachable(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L3", []string{"password", "totp"})
	orc, _ := f.orchestrator(t)
	ctx := context.Background()

	begun, err := orc.Begin(ctx, flow.BeginInput{ClientID: f.clientID, UserID: &f.userID})
	if err != nil {
		t.Fatalf("不可达不该是 error（它是 200 的终态）：%v", err)
	}
	if begun.Flow.TerminalState != flow.TerminalUnreachable {
		t.Fatalf("应判不可达，实际 %s", begun.Flow.TerminalState)
	}
	r := begun.Flow.UnreachableReason
	if r == nil || r.Code != flow.ReasonFactorsDisabled {
		t.Fatalf("原因应为 FACTORS_DISABLED，实际 %+v", r)
	}
	if len(r.MissingFactors) == 0 {
		t.Fatal("missing_factors 必须非空（前端要据此给建议）")
	}
	if code, ok := flow.HTTPCodeFor(begun.Flow); ok {
		t.Fatalf("不可达不该有错误码，实际 %s", code)
	}
}

// 一次性消费：达标后 Finish → completed + consumed_at 非空（I-F4 的编排侧）。
func TestFinishConsumesFlow(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2", nil)
	orc, _ := f.orchestrator(t)
	ctx := context.Background()

	begun, err := orc.Begin(ctx, flow.BeginInput{ClientID: f.clientID, UserID: &f.userID})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := orc.VerifyStep(ctx, flow.VerifyInput{FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "ok"}); err != nil {
		t.Fatalf("VerifyStep: %v", err)
	}
	if _, err := orc.Finish(ctx, begun.FlowID, &f.userID, nil); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	var row struct {
		Status     string     `gorm:"column:status"`
		ConsumedAt *time.Time `gorm:"column:consumed_at"`
	}
	if err := db.Raw(`SELECT status, consumed_at FROM flows WHERE id = ?`, begun.FlowID).Scan(&row).Error; err != nil {
		t.Fatalf("读 flows: %v", err)
	}
	if row.Status != "completed" || row.ConsumedAt == nil {
		t.Fatalf("消费后应为 completed + consumed_at，实际 %+v", row)
	}
}

// challenge 生命周期（真库）：一次性消费只能抢到一次；尝试次数单调且到上限即"用尽"。
func TestChallengeLifecycleInDB(t *testing.T) {
	db := openTestDB(t)
	repo := store.NewChallengeRepo(db)
	ctx := context.Background()

	suffix := uuid.NewString()
	clientID := "s5-ch-" + suffix[:16]
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier) VALUES (?, 'S5 challenge client', 'L2')`, clientID).Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM challenges WHERE client_id = ?`, clientID).Error })
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, clientID).Error })

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	now := time.Now().UTC()
	ch := &model.Challenge{}
	ch.ID = id
	ch.ClientID = &clientID
	ch.Purpose = "login" // D2 六值之一（DB CHECK 冻结）
	ch.FactorType = "sms_otp"
	ch.ResponseHash = []byte{0x01}
	ch.MaxAttempts = 2
	ch.ExpiresAt = now.Add(5 * time.Minute)
	if err := repo.Create(ctx, ch); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 记两次失败 → 用尽（第二次返回 exhausted=true）。
	remaining, exhausted, err := repo.RecordAttempt(ctx, id, now)
	if err != nil || exhausted || remaining != 1 {
		t.Fatalf("第一次失败：remaining=%d exhausted=%v err=%v", remaining, exhausted, err)
	}
	remaining, exhausted, err = repo.RecordAttempt(ctx, id, now)
	if err != nil || !exhausted || remaining != 0 {
		t.Fatalf("第二次失败应到上限：remaining=%d exhausted=%v err=%v", remaining, exhausted, err)
	}
	// 到上限后再记一次：UPDATE 的条件挡住（0 行），计数不越过 max_attempts。
	if _, _, err := repo.RecordAttempt(ctx, id, now); err != nil {
		t.Fatalf("超限后再记失败不该报错（应被条件挡住）：%v", err)
	}
	var attempts int32
	if err := db.Raw(`SELECT attempts FROM challenges WHERE id = ?`, id).Scan(&attempts).Error; err != nil {
		t.Fatalf("读 attempts: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts 不得越过 max_attempts(2)，实际 %d", attempts)
	}

	// 一次性消费：第一次 1 行，第二次 0 行。
	// 消费时刻必须晚于 created_at（DB CHECK `challenges_consumed_after_created`），
	// 所以这里取"现在"而不是前面那个 now。
	consumedAt := time.Now().UTC().Add(time.Second)
	affected, err := repo.Consume(ctx, id, consumedAt)
	if err != nil || affected != 1 {
		t.Fatalf("首次消费应抢到：affected=%d err=%v", affected, err)
	}
	affected, err = repo.Consume(ctx, id, consumedAt.Add(time.Second))
	if err != nil || affected != 0 {
		t.Fatalf("重复消费必须抢不到（affected=0）：affected=%d err=%v", affected, err)
	}
}

// I-F7 的一次性回填与"不许换会话"（真库）：编排层把 session 交给 flowstore 之后的行为。
//
// 这三步必须都在真库上跑：回填是带 `WHERE session_id IS NULL` 的条件 UPDATE，
// 没有库根本验不了"只能成功一次"。
func TestFinishBackfillsSessionOnce(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2", nil)
	orc, _ := f.orchestrator(t)
	ctx := context.Background()

	// ① Begin 两次应复用同一条活跃流（Start 的"先查后建"）。
	first, err := orc.Begin(ctx, flow.BeginInput{ClientID: f.clientID, UserID: &f.userID})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	second, err := orc.Begin(ctx, flow.BeginInput{ClientID: f.clientID, UserID: &f.userID})
	if err != nil {
		t.Fatalf("Begin(2): %v", err)
	}
	if first.FlowID != second.FlowID {
		t.Fatalf("同一用户同一 client 应复用活跃流，实际 %s vs %s", first.FlowID, second.FlowID)
	}

	if _, err := orc.VerifyStep(ctx, flow.VerifyInput{FlowID: first.FlowID, Factor: factor.FactorPassword, Response: "ok"}); err != nil {
		t.Fatalf("VerifyStep: %v", err)
	}

	// ② 第一次 Finish 不带 session → completed 且 session_id 仍为空。
	if _, err := orc.Finish(ctx, first.FlowID, &f.userID, nil); err != nil {
		t.Fatalf("Finish(1): %v", err)
	}
	s1 := f.newSession(t)
	s2 := f.newSession(t)

	// ③ 第二次带 session → 一次性回填（I-F7）。
	if _, err := orc.Finish(ctx, first.FlowID, nil, &s1); err != nil {
		t.Fatalf("Finish(2) 回填应成功：%v", err)
	}
	// 用 *string 接：这条 Raw 路径下 uuid 列以文本返回，直接扫 *uuid.UUID 会报
	// "converting driver.Value type string to a uint8"（与 S4b 的 jsonb 同类问题）。
	var got *string
	if err := db.Raw(`SELECT session_id::text FROM flows WHERE id = ?`, first.FlowID).Scan(&got).Error; err != nil {
		t.Fatalf("读 session_id: %v", err)
	}
	if got == nil || *got != s1.String() {
		t.Fatalf("session_id 应回填为 %s，实际 %v", s1, got)
	}

	// ④ 再换一个会话 → 拒绝（能换就等于能劫持）。
	if _, err := orc.Finish(ctx, first.FlowID, nil, &s2); !errors.Is(err, flowstore.ErrSessionImmutable) {
		t.Fatalf("换会话应被拒（ErrSessionImmutable），实际 %v", err)
	}
}

// newSession 建一条真实会话（flows.session_id 有 FK，随机 uuid 会撞 flows_session_id_fkey）。
func (f *fixture) newSession(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	if err := f.db.Exec(`INSERT INTO sessions (id, user_id, client_id, acr, expires_at, idle_expires_at)
		VALUES (?, ?, ?, 'AAL1', now() + interval '1 hour', now() + interval '30 min')`,
		id, f.userID, f.clientID).Error; err != nil {
		t.Fatalf("insert session: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Exec(`DELETE FROM sessions WHERE id = ?`, id).Error })
	return id
}

// flowstore 的其余错误分支（真库）：过期、已作废、匿名流缺 user_id。
//
// 为什么这些必须在这里补：`internal/flowstore` 的模块下限是 85%（D8 补登），而它的
// 大部分语句是**错误分支**——正常路径一条用例就能走到，错误分支不走就永远 0 覆盖。
// 这些分支恰恰是"用户被锁在门外"或"孤儿账号"最容易发生的地方。
func TestFlowstoreErrorPaths(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2", nil)
	orc, _ := f.orchestrator(t)
	ctx := context.Background()

	t.Run("流过期后推进 → FLOW_EXPIRED，且行被置 expired", func(t *testing.T) {
		begun, err := orc.Begin(ctx, flow.BeginInput{ClientID: f.clientID, UserID: &f.userID})
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		// 直接把 TTL 推到过去（等价于"用户开着登录页去泡了杯咖啡"）。
		if err := db.Exec(`UPDATE flows SET expires_at = now() - interval '1 minute' WHERE id = ?`,
			begun.FlowID).Error; err != nil {
			t.Fatalf("改 expires_at: %v", err)
		}
		_, err = orc.VerifyStep(ctx, flow.VerifyInput{
			FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "ok",
		})
		if !errors.Is(err, flowstore.ErrFlowExpired) {
			t.Fatalf("过期流应报 ErrFlowExpired，实际 %v", err)
		}
		var status string
		if err := db.Raw(`SELECT status FROM flows WHERE id = ?`, begun.FlowID).Scan(&status).Error; err != nil {
			t.Fatalf("读 status: %v", err)
		}
		if status != "expired" {
			t.Fatalf("过期后行状态应为 expired（不删行，I-F6 两段式），实际 %q", status)
		}
	})

	t.Run("已作废的流不能再推进/消费 → FLOW_NOT_ACTIVE", func(t *testing.T) {
		begun, err := orc.Begin(ctx, flow.BeginInput{ClientID: f.clientID, UserID: &f.userID})
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		if err := db.Exec(`UPDATE flows SET status = 'abandoned' WHERE id = ?`, begun.FlowID).Error; err != nil {
			t.Fatalf("置 abandoned: %v", err)
		}
		if _, err := orc.VerifyStep(ctx, flow.VerifyInput{
			FlowID: begun.FlowID, Factor: factor.FactorPassword, Response: "ok",
		}); !errors.Is(err, flowstore.ErrFlowNotActive) {
			t.Fatalf("作废流推进应报 ErrFlowNotActive，实际 %v", err)
		}
		if _, err := orc.Finish(ctx, begun.FlowID, nil, nil); !errors.Is(err, flowstore.ErrFlowNotActive) {
			t.Fatalf("作废流消费应报 ErrFlowNotActive，实际 %v", err)
		}
	})

	t.Run("匿名流没有 user_id 时不能消费 → ErrUserRequired", func(t *testing.T) {
		// 注册前的匿名流（Begin 的 UserID 传 nil）：完成时必须回填 user_id（I-F1）。
		anon, err := orc.Begin(ctx, flow.BeginInput{ClientID: f.clientID})
		if err != nil {
			t.Fatalf("Begin(匿名): %v", err)
		}
		if _, err := orc.VerifyStep(ctx, flow.VerifyInput{
			FlowID: anon.FlowID, Factor: factor.FactorPassword, Response: "ok",
		}); err != nil {
			t.Fatalf("VerifyStep: %v", err)
		}
		if _, err := orc.Finish(ctx, anon.FlowID, nil, nil); !errors.Is(err, flowstore.ErrUserRequired) {
			t.Fatalf("缺 user_id 应报 ErrUserRequired（I-F1），实际 %v", err)
		}
	})
}
