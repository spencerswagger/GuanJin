//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/flowstore"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/identity"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 真库用例（**必须有真 PostgreSQL**）：证明"因子与挑战面"这条线在真实 schema 上跑得通。
//
//	GUANJIN_TEST_DSN=... go test -tags=integration ./cmd/guanjind/
//
// 不打 `t.Skip`：缺 DSN 就红（CI 已机器强制「skip>0 即红 / 无用例即红」）。
// 这里走的是**装配后的真实链路**（`newFlowHandler` + `ServeMux` + trace 中间件），
// 不是直接调 handler 函数——路由写错、中间件顺序写错，都会在这里暴露。
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
	for _, table := range []string{"flows", "challenges", "login_policies", "runtime_config"} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("测试库缺 %s 表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN", table)
		}
	}
	t.Cleanup(func() { _ = store.Close(db) })
	return db
}

// flowFixture 建一个隔离业务系统 + 它的策略行，并装出"装配后的" HTTP 服务。
type flowFixture struct {
	clientID string
	userIDs  []uuid.UUID
	ttl      time.Duration
	srv      *httptest.Server
	flows    *flowstore.Service
	flowRepo *store.FlowRepo
}

func newFlowFixture(t *testing.T, db *gorm.DB, tier, disabledFactors string) *flowFixture {
	t.Helper()
	ctx := context.Background()
	f := &flowFixture{clientID: "s5-" + uuid.NewString()[:20]}

	if err := db.Exec(`INSERT INTO clients (client_id, name, tier) VALUES (?, ?, ?)`,
		f.clientID, "S5 test client", tier).Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	policyID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	minACR := "AAL1"
	if tier == "L3" {
		minACR = "AAL2"
	}
	if err := db.Exec(`INSERT INTO login_policies
		(id, scope, client_id, min_acr, password_min_length, password_char_classes,
		 password_history_count, password_max_age_days, self_registration_allowed, disabled_factors)
		VALUES (?, 'client_override', ?, ?, 12, 3, 5, 90, true, ?)`,
		policyID, f.clientID, minACR, disabledFactors).Error; err != nil {
		t.Fatalf("insert login_policies: %v", err)
	}

	// TTL 从 `runtime_config` 读（D5：代码里没有 600 常量）；读不到就是配置缺陷。
	ttl, err := store.LoadFlowTTL(ctx, store.NewRuntimeConfigRepo(db))
	if err != nil {
		t.Fatalf("LoadFlowTTL: %v", err)
	}
	f.ttl = ttl

	handler, err := newFlowHandler(ctx, db, ttl, discardLogger())
	if err != nil {
		t.Fatalf("newFlowHandler: %v", err)
	}
	mux := http.NewServeMux()
	handler.Routes(mux)
	f.srv = httptest.NewServer(httpx.TraceMiddleware(mux, "X-Trace-Id", discardLogger()))
	t.Cleanup(f.srv.Close)

	f.flowRepo = store.NewFlowRepo(db)
	f.flows, err = flowstore.New(db, f.flowRepo, ttl)
	if err != nil {
		t.Fatalf("flowstore.New: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM auth_events WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM challenges WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM flows WHERE client_id = ?`, f.clientID).Error
		for _, uid := range f.userIDs {
			_ = db.Exec(`DELETE FROM authenticators WHERE user_id = ?`, uid).Error
			_ = db.Exec(`DELETE FROM identifiers WHERE user_id = ?`, uid).Error
			_ = db.Exec(`DELETE FROM users WHERE id = ?`, uid).Error
		}
		_ = db.Exec(`DELETE FROM login_policies WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, f.clientID).Error
	})
	return f
}

// startFlow 开一条匿名流（真实链路里由 S6 的 `/authorize` 建流）。
func (f *flowFixture) startFlow(t *testing.T, userID *uuid.UUID, target string) *model.Flow {
	t.Helper()
	row, err := f.flows.Start(context.Background(), flowstore.StartRequest{
		UserID:    userID,
		ClientID:  f.clientID,
		TargetACR: target,
		TraceID:   strPtr("trace-" + uuid.NewString()[:8]),
	})
	if err != nil {
		t.Fatalf("flowstore.Start: %v", err)
	}
	return row
}

func (f *flowFixture) get(t *testing.T, path string, out any) (*http.Response, httpx.ErrorResponse) {
	t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp, decodeBody(t, resp, out)
}

func (f *flowFixture) post(t *testing.T, path, body string, out any) (*http.Response, httpx.ErrorResponse) {
	t.Helper()
	resp, err := http.Post(f.srv.URL+path, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp, decodeBody(t, resp, out)
}

func decodeBody(t *testing.T, resp *http.Response, out any) httpx.ErrorResponse {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	if resp.StatusCode >= 400 {
		var env httpx.ErrorResponse
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("错误响应不是统一四字段：%v（体：%s）", err, raw)
		}
		return env
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("解码响应体: %v（体：%s）", err, raw)
		}
	}
	return httpx.ErrorResponse{}
}

// AC-FLOW-07：因子全禁用 + 目标 AAL2 → **HTTP 200** + `terminal_state=unreachable`。
//
// 用真库里的 `login_policies.disabled_factors` 构造，不用假 provider —— 这条判据的
// 意思是"策略禁用了就不能假装还能补步"，只有在真实策略读出来时才算证明。
func TestE2E_AC_FLOW_07_UnreachableIsHTTP200(t *testing.T) {
	db := openTestDB(t)
	f := newFlowFixture(t, db, "L3", "{password}")
	row := f.startFlow(t, nil, string(factor.ACRAAL2))

	var d flow.Descriptor
	resp, env := f.get(t, "/authorize/flow?flow_id="+row.ID.String(), &d)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("不可达必须是 200（不是错误页），实际 %d / %s", resp.StatusCode, env.Code)
	}
	if d.TerminalState != flow.TerminalUnreachable {
		t.Fatalf("terminal_state 应为 unreachable，实际 %q", d.TerminalState)
	}
	if d.UnreachableReason == nil || d.UnreachableReason.Code != flow.ReasonFactorsDisabled {
		t.Fatalf("原因应为 FACTORS_DISABLED，实际 %+v", d.UnreachableReason)
	}
	if len(d.UnreachableReason.MissingFactors) == 0 {
		t.Fatal("missing_factors 不得为空（前端要能说清缺哪个因子）")
	}
	if d.CurrentACR != "" {
		t.Fatalf("一个因子都没达成时 current_acr 应为空（AAL0 不落库），实际 %q", d.CurrentACR)
	}
	if len(d.DisabledFactors) != 1 || d.DisabledFactors[0] != factor.FactorPassword {
		t.Fatalf("disabled_factors 应为 [password]，实际 %v", d.DisabledFactors)
	}
}

// AC-FLOW-01 + AC-FLOW-03①：Descriptor 字段齐备；已完成的步再下发 → 409 且**不产生挑战行**。
func TestE2E_AC_FLOW_01_03_DescriptorAndDoneStep(t *testing.T) {
	db := openTestDB(t)
	f := newFlowFixture(t, db, "L2", "{}")
	row := f.startFlow(t, nil, string(factor.ACRAAL1))

	var d flow.Descriptor
	resp, env := f.get(t, "/authorize/flow?flow_id="+row.ID.String(), &d)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("应为 200，实际 %d / %s", resp.StatusCode, env.Code)
	}
	if d.FlowID != row.ID.String() || d.TargetACR != factor.ACRAAL1 {
		t.Fatalf("Descriptor 与 flows 行不一致：%+v", d)
	}
	if d.TerminalState != flow.TerminalNeedMore {
		t.Fatalf("应为 need_more，实际 %q", d.TerminalState)
	}
	if len(d.Steps) == 0 || d.Steps[0].Factor != factor.FactorPassword || d.Steps[0].Status != flow.StepPending {
		t.Fatalf("应给出口令的待补步骤，实际 %+v", d.Steps)
	}
	if d.Steps[0].Purpose != factor.PurposeLogin {
		t.Fatalf("步骤 purpose 应是 D2 词表内的 login，实际 %q", d.Steps[0].Purpose)
	}

	var ch struct {
		FlowID       *string           `json:"flow_id"`
		ChallengeID  *string           `json:"challenge_id"`
		NextAction   factor.NextAction `json:"next_action"`
		TTLSeconds   int64             `json:"ttl"`
		AttemptsLeft int               `json:"attempts_left"`
	}
	resp2, env2 := f.post(t, "/auth/challenge/password",
		`{"flow_id":"`+row.ID.String()+`","purpose":"login"}`, &ch)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("下发挑战应为 200，实际 %d / %s", resp2.StatusCode, env2.Code)
	}
	if ch.NextAction != factor.NextActionInputPassword {
		t.Fatalf("口令的 next_action 应为 input_password，实际 %s", ch.NextAction)
	}
	if ch.ChallengeID != nil {
		t.Fatalf("口令是无状态挑战，不得有 challenge_id，实际 %v", *ch.ChallengeID)
	}
	if ch.TTLSeconds <= 0 {
		t.Fatalf("应回正的 ttl，实际 %d", ch.TTLSeconds)
	}
	challengeRows := countChallenges(t, db, f.clientID)

	// 把口令标记为已达成（真实链路里由 `/auth/verify/password` 写，这里直接落库模拟"已完成"）。
	if err := db.Exec(`UPDATE flows SET achieved_amr = '{pwd}' WHERE id = ?`, row.ID).Error; err != nil {
		t.Fatalf("update flows: %v", err)
	}
	resp3, env3 := f.post(t, "/auth/challenge/password",
		`{"flow_id":"`+row.ID.String()+`","purpose":"login"}`, nil)
	if resp3.StatusCode != http.StatusConflict {
		t.Fatalf("已完成的步应 409，实际 %d / %s", resp3.StatusCode, env3.Code)
	}
	// 码值由 `internal/flow` 导出（D3-F：常量只许定义在那个包），这里不写字符串字面量。
	if env3.Code != flow.CodeStepAlreadySatisfied {
		t.Fatalf("应为 %s，实际 %s", flow.CodeStepAlreadySatisfied, env3.Code)
	}
	if env3.TraceID == "" {
		t.Fatal("错误响应必须带可复制 trace_id")
	}
	if got := countChallenges(t, db, f.clientID); got != challengeRows {
		t.Fatalf("409 不得产生新的 challenges 行：之前 %d，之后 %d", challengeRows, got)
	}

	// 不存在的流 → 404 FLOW_NOT_FOUND（不是 500）。
	resp4, env4 := f.get(t, "/authorize/flow?flow_id="+uuid.New().String(), nil)
	if resp4.StatusCode != http.StatusNotFound || env4.Code != httpx.CodeFlowNotFound {
		t.Fatalf("不存在的 flow 应 404 FLOW_NOT_FOUND，实际 %d / %s", resp4.StatusCode, env4.Code)
	}
}

// AC-FLOW-02 + AC-FLOW-10①：口令验证通过但档位不足 → 403 `ACR_INSUFFICIENT` + `details.flow`；
// 到期后首次访问 → 400 `FLOW_EXPIRED`（行仍在，所以稳定）。
func TestE2E_AC_FLOW_02_10_VerifyAndExpiry(t *testing.T) {
	db := openTestDB(t)
	f := newFlowFixture(t, db, "L3", "{}")

	// 先造一个**有口令**的用户（注册服务 = S4 的落点，这里只借它建号）。
	userID := f.registerUser(t, db, "GuanJin#S5-Strong1")
	row := f.startFlow(t, &userID, string(factor.ACRAAL2))

	// 先测"口令不对"：401 INVALID_CREDENTIAL（与"账号不存在"同码，不做区分性提示）。
	// 必须排在成功之前 —— 一旦 `pwd` 已达成，D3-F 会先回 409 STEP_ALREADY_SATISFIED。
	wrongResp, wrongEnv := f.post(t, "/auth/verify/password",
		`{"flow_id":"`+row.ID.String()+`","response":"wrong-password"}`, nil)
	if wrongResp.StatusCode != http.StatusUnauthorized || wrongEnv.Code != httpx.CodeInvalidCredential {
		t.Fatalf("错口令应 401 INVALID_CREDENTIAL，实际 %d / %s", wrongResp.StatusCode, wrongEnv.Code)
	}

	var vr struct {
		Verified bool       `json:"verified"`
		AMR      []string   `json:"amr"`
		ACR      factor.ACR `json:"acr"`
	}
	resp, env := f.post(t, "/auth/verify/password",
		`{"flow_id":"`+row.ID.String()+`","response":"GuanJin#S5-Strong1"}`, &vr)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("达成 AAL1 但目标 AAL2 时必须是 403（不是 500/401），实际 %d", resp.StatusCode)
	}
	if env.Code != httpx.CodeACRInsufficient {
		t.Fatalf("应为 ACR_INSUFFICIENT，实际 %s", env.Code)
	}
	details, ok := env.Details.(map[string]any)
	if !ok || details["flow"] == nil {
		t.Fatalf("details.flow 必须是完整 Descriptor，实际 %v", env.Details)
	}
	descriptor, _ := details["flow"].(map[string]any)
	if descriptor["current_acr"] != string(factor.ACRAAL1) {
		t.Fatalf("current_acr 应为 AAL1，实际 %v", descriptor["current_acr"])
	}

	// 达成的 amr 必须真的落库（I-F2 只增不减）。
	//
	// 读回用 `array_to_string`：PG 的 `text[]` 直接扫进 Go 的切片会被驱动当成一个
	// 字面量字符串（`{pwd}`）返回，断言就会"看起来失败"——这是驱动层的坑，不是数据错。
	var amrRaw string
	if err := db.Raw(`SELECT array_to_string(achieved_amr, ',') FROM flows WHERE id = ?`, row.ID).
		Scan(&amrRaw).Error; err != nil {
		t.Fatalf("读 flows: %v", err)
	}
	if amrRaw != "pwd" {
		t.Fatalf("achieved_amr 应只含 pwd，实际 %q", amrRaw)
	}

	// AC-FLOW-10①：到期后首次访问必得 FLOW_EXPIRED（第一段只置 expired、不删行）。
	if err := db.Exec(`UPDATE flows SET expires_at = now() - interval '1 second' WHERE id = ?`, row.ID).Error; err != nil {
		t.Fatalf("update expires_at: %v", err)
	}
	resp3, env3 := f.get(t, "/authorize/flow?flow_id="+row.ID.String(), nil)
	if resp3.StatusCode != http.StatusBadRequest || env3.Code != httpx.CodeFlowExpired {
		t.Fatalf("过期流应 400 FLOW_EXPIRED，实际 %d / %s", resp3.StatusCode, env3.Code)
	}
	var stillThere int64
	if err := db.Raw(`SELECT count(*) FROM flows WHERE id = ?`, row.ID).Scan(&stillThere).Error; err != nil {
		t.Fatalf("count flows: %v", err)
	}
	if stillThere != 1 {
		t.Fatalf("过期行必须仍在（行不删才有稳定的 FLOW_EXPIRED），实际 %d", stillThere)
	}
}

// registerUser 用 S4 的注册服务建一个有口令的用户（口令信封要真材实料，验签才有意义）。
func (f *flowFixture) registerUser(t *testing.T, db *gorm.DB, password string) uuid.UUID {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 7)
	}
	mk, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	indexKey, err := mk.DeriveHMAC(crypto.PurposeHMACIdentifier)
	if err != nil {
		t.Fatalf("DeriveHMAC: %v", err)
	}
	fieldKey, err := mk.DeriveSM4(crypto.PurposeSM4Field)
	if err != nil {
		t.Fatalf("DeriveSM4: %v", err)
	}
	svc, err := identity.New(db, identity.Config{
		IndexKey:       indexKey,
		FieldKey:       fieldKey,
		PasswordParams: crypto.PasswordParams{Iterations: crypto.MinPBKDF2Iterations, KeyLen: crypto.MinPasswordKeyLen},
		FlowTTL:        f.ttl,
	})
	if err != nil {
		t.Fatalf("identity.New: %v", err)
	}
	res, err := svc.Register(context.Background(), identity.RegisterRequest{
		ClientID:     f.clientID,
		Type:         "email",
		Value:        "s5-" + uuid.NewString()[:12] + "@example.com",
		Password:     password,
		Verified:     true,
		VerifyMethod: "email_otp",
		TrustLevel:   2,
		TraceID:      "trace-" + uuid.NewString()[:8],
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	f.userIDs = append(f.userIDs, res.UserID)
	return res.UserID
}

func countChallenges(t *testing.T, db *gorm.DB, clientID string) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT count(*) FROM challenges WHERE client_id = ?`, clientID).Scan(&n).Error; err != nil {
		t.Fatalf("count challenges: %v", err)
	}
	return n
}

func strPtr(s string) *string { return &s }
