//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/flowstore"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/identity"
	"github.com/spencerswagger/guanjin/internal/risk"
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

	// 运行期参数从 `runtime_config` 读（D5/D14：代码里没有 600/300/5 常量）。
	startupCfg, err := store.ValidateStartup(ctx, store.NewRuntimeConfigRepo(db))
	if err != nil {
		t.Fatalf("ValidateStartup: %v", err)
	}
	f.ttl = startupCfg.FlowTTL

	// 本套用例只走 flow/register 两条面；`/api/v1/me/*` 需要一个 resolver 才能装配，
	// 这里给一个"永远拒绝"的桩（不伪造登录态，也不影响被测路径）。
	w, err := newWiring(ctx, db, testConfig(t), startupCfg, rejectResolver{}, discardLogger())
	if err != nil {
		t.Fatalf("newWiring: %v", err)
	}
	mux := http.NewServeMux()
	w.flowHandler.Routes(mux)
	w.registerHandler.Routes(mux)
	f.srv = httptest.NewServer(httpx.TraceMiddleware(mux, "X-Trace-Id", discardLogger()))
	t.Cleanup(f.srv.Close)

	f.flowRepo = store.NewFlowRepo(db)
	f.flows, err = flowstore.New(db, f.flowRepo, startupCfg.FlowTTL)
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
	registerKey, err := mk.DeriveHMAC(crypto.PurposeHMACRegister)
	if err != nil {
		t.Fatalf("DeriveHMAC(register): %v", err)
	}
	startupCfg, err := store.ValidateStartup(context.Background(), store.NewRuntimeConfigRepo(db))
	if err != nil {
		t.Fatalf("ValidateStartup: %v", err)
	}
	svc, err := identity.New(db, identity.Config{
		IndexKey:             indexKey,
		FieldKey:             fieldKey,
		RegisterTokenKey:     registerKey,
		PasswordParams:       crypto.PasswordParams{Iterations: crypto.MinPBKDF2Iterations, KeyLen: crypto.MinPasswordKeyLen},
		FlowTTL:              f.ttl,
		ChallengeTTL:         startupCfg.ChallengeTTL,
		ChallengeMaxAttempts: startupCfg.ChallengeMaxAttempts,
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

// testConfig 按环境变量装出进程配置（**不改宿主配置**：DSN/Redis 都由 integration job 注入）。
//
// 缺任何一个必需项就红：这些用例的意义就是"在真依赖上跑"，缺依赖 = 未验证。
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		t.Fatalf("必须设置 GUANJIN_TEST_DSN（integration job 已注入）")
	}
	redisAddr := os.Getenv("GUANJIN_TEST_REDIS_ADDR")
	if redisAddr == "" {
		t.Fatalf("必须设置 GUANJIN_TEST_REDIS_ADDR（账号级失败计数要真 Redis）")
	}
	env := map[string]string{
		"GUANJIN_DB_DSN":         dsn,
		"GUANJIN_REDIS_ADDR":     redisAddr,
		"GUANJIN_REDIS_PASSWORD": os.Getenv("GUANJIN_TEST_REDIS_PASSWORD"),
	}
	// 主密钥：只用于本进程派生（32 字节 0x2a 的 base64），不落任何文件。
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = 0x2a
	}
	env["GUANJIN_MASTER_KEY"] = base64.StdEncoding.EncodeToString(raw)
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// 注册面：`/auth/register/start` + `/auth/register/complete` 在装配后的真实链路上跑通。
//
// 覆盖：用户名免验证路径（AC-REG-05）、邮箱路径的挑战行与 `attempts_left`
// （AC-REG-01 的 data 侧：值来自 `runtime_config`）、邀请码显式拒绝（AC-REG-08 无效分支）。
func TestE2E_RegisterHTTPLine(t *testing.T) {
	db := openTestDB(t)
	f := newFlowFixture(t, db, "L2", "{}")
	startupCfg, err := store.ValidateStartup(context.Background(), store.NewRuntimeConfigRepo(db))
	if err != nil {
		t.Fatalf("ValidateStartup: %v", err)
	}

	// ① 用户名 + 口令：跳过挑战，complete 直接建号。
	username := "s5e2e-" + uuid.NewString()[:10]
	const password = "GuanJin#E2E-Strong1"
	var start struct {
		RegisterToken string  `json:"register_token"`
		ChallengeID   *string `json:"challenge_id"`
		NextAction    string  `json:"next_action"`
		AttemptsLeft  int     `json:"attempts_left"`
	}
	resp, env := f.post(t, "/auth/register/start",
		`{"client_id":"`+f.clientID+`","type":"username","value":"`+username+`"}`, &start)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start 应为 200，实际 %d / %s", resp.StatusCode, env.Code)
	}
	if start.ChallengeID != nil || start.NextAction != "input_password" || start.RegisterToken == "" {
		t.Fatalf("用户名型应免挑战：%+v", start)
	}
	resp2, env2 := f.post(t, "/auth/register/complete",
		`{"register_token":"`+start.RegisterToken+`","password":"`+password+`"}`, nil)
	_ = env2
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("complete 应为 200，实际 %d / %s / %s", resp2.StatusCode, env2.Code, env2.Message)
	}
	// `uuid.UUID` 直接 Scan 会被驱动当成字符串（database/sql 的通用类型），先扫成 string 再解析。
	var uidStr string
	if err := db.Raw(`SELECT id::text FROM users WHERE username = ?`, username).Scan(&uidStr).Error; err != nil || uidStr == "" {
		t.Fatalf("应建出用户：%v", err)
	}
	uid, err := uuid.Parse(uidStr)
	if err != nil {
		t.Fatalf("解析用户 id：%v", err)
	}
	f.userIDs = append(f.userIDs, uid)

	// 令牌二次使用：用户名型**没有挑战**，一次性由 `users.username` 的唯一索引兜 → 必须 409。
	// （不写成"只要不是 500 就算过"：那样第二次返回 200 也会绿，等于没断言。）
	resp3, env3 := f.post(t, "/auth/register/complete",
		`{"register_token":"`+start.RegisterToken+`","password":"`+password+`"}`, nil)
	if resp3.StatusCode != http.StatusConflict {
		t.Fatalf("重复建号应 409（users 唯一索引），实际 %d / %s", resp3.StatusCode, env3.Code)
	}

	// ② 邮箱路径：下发挑战，`attempts_left` 等于 runtime_config 的 `challenge.max_attempts`。
	var mail struct {
		ChallengeID  *string `json:"challenge_id"`
		AttemptsLeft int     `json:"attempts_left"`
		NextAction   string  `json:"next_action"`
	}
	resp4, env4 := f.post(t, "/auth/register/start",
		`{"client_id":"`+f.clientID+`","type":"email","value":"e2e-`+uuid.NewString()[:8]+`@example.com"}`, &mail)
	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("email start 应为 200，实际 %d / %s", resp4.StatusCode, env4.Code)
	}
	if mail.ChallengeID == nil {
		t.Fatal("email 型必须下发挑战")
	}
	if mail.AttemptsLeft != startupCfg.ChallengeMaxAttempts {
		t.Fatalf("attempts_left 应等于 runtime_config 的 %d，实际 %d",
			startupCfg.ChallengeMaxAttempts, mail.AttemptsLeft)
	}
	if mail.NextAction != "type_code" {
		t.Fatalf("next_action 应为 type_code，实际 %s", mail.NextAction)
	}

	// ③ 邀请码：任何非空值都判无效（M1 没有邀请码存储），且零新增行。
	resp5, env5 := f.post(t, "/auth/register/start",
		`{"client_id":"`+f.clientID+`","type":"email","value":"e2e-`+uuid.NewString()[:8]+`@example.com","invite_code":"X-1"}`, nil)
	if resp5.StatusCode != http.StatusBadRequest || env5.Code != httpx.CodeInviteCodeInvalid {
		t.Fatalf("邀请码应 400 INVITE_CODE_INVALID，实际 %d / %s", resp5.StatusCode, env5.Code)
	}
}

// AC-LOGIN-03：连续失败达阈值 → `ACCOUNT_LOCKED`；权威计数在 Redis 的两个冻结键上。
func TestE2E_AC_LOGIN_03_Lockout(t *testing.T) {
	db := openTestDB(t)
	f := newFlowFixture(t, db, "L2", "{}")

	// 把该业务的失败阈值降到 2（夹具自己的取值，不是实现默认值），用例才跑得快。
	if err := db.Exec(`UPDATE login_policies SET fail_threshold = 2, lock_duration_min = 5 WHERE client_id = ?`,
		f.clientID).Error; err != nil {
		t.Fatalf("update policy: %v", err)
	}

	// 造一个带口令的账号（注册服务 = 上一刀的落点，这里只借它建号）。
	userID := f.registerUser(t, db, "GuanJin#S5-Strong1")
	row := f.startFlow(t, &userID, string(factor.ACRAAL1)) // L2 → AAL1，一次口令即可达标

	// 第 1 次错口令：401 INVALID_CREDENTIAL（还没到阈值）。
	resp, env := f.post(t, "/auth/verify/password",
		`{"flow_id":"`+row.ID.String()+`","response":"wrong-password-1"}`, nil)
	if resp.StatusCode != http.StatusUnauthorized || env.Code != httpx.CodeInvalidCredential {
		t.Fatalf("第 1 次应 401 INVALID_CREDENTIAL，实际 %d / %s", resp.StatusCode, env.Code)
	}
	// 第 2 次（= 阈值）：ACCOUNT_LOCKED。
	resp2, env2 := f.post(t, "/auth/verify/password",
		`{"flow_id":"`+row.ID.String()+`","response":"wrong-password-2"}`, nil)
	if resp2.StatusCode != http.StatusUnauthorized || env2.Code != httpx.CodeAccountLocked {
		t.Fatalf("达阈值应 401 ACCOUNT_LOCKED，实际 %d / %s", resp2.StatusCode, env2.Code)
	}

	// 直接按 D3-D 的两个冻结键断言（AC-LOGIN-03 要求"直接按上述两键断言"）。
	guard, err := risk.New(os.Getenv("GUANJIN_TEST_REDIS_ADDR"), os.Getenv("GUANJIN_TEST_REDIS_PASSWORD"))
	if err != nil {
		t.Fatalf("risk.New: %v", err)
	}
	left, locked, err := guard.Status(context.Background(), userID.String(),
		risk.Policy{FailThreshold: 2, LockDuration: 5 * time.Minute})
	if err != nil {
		t.Fatalf("读 Redis 状态：%v", err)
	}
	if !locked || left != 0 {
		t.Fatalf("Redis 上应已锁定且剩余 0，实际 locked=%v left=%d", locked, left)
	}

	// 锁定后即使口令正确也不放行（fail-closed），且不会再去算 PBKDF2。
	resp3, env3 := f.post(t, "/auth/verify/password",
		`{"flow_id":"`+row.ID.String()+`","response":"GuanJin#S5-Strong1"}`, nil)
	if resp3.StatusCode != http.StatusUnauthorized || env3.Code != httpx.CodeAccountLocked {
		t.Fatalf("锁定后正确口令也应 401 ACCOUNT_LOCKED，实际 %d / %s", resp3.StatusCode, env3.Code)
	}

	// 清理该账号的计数，避免影响其它用例（同一测试库共用）。
	if err := guard.Reset(context.Background(), userID.String()); err != nil {
		t.Fatalf("reset: %v", err)
	}
}

// rejectResolver 是 `session.TokenResolver` 的桩：任何令牌都判未认证。
//
// 只给"不需要登录态的集成用例"装配用（flow/register/fail-closed 那几条）；
// 自助面用例必须用真解析器（否则等于没验认证）。
type rejectResolver struct{}

func (rejectResolver) Resolve(context.Context, string) (httpx.Caller, error) {
	return httpx.Caller{}, httpx.ErrCallerUnauthenticated
}
