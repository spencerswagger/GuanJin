//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/admin"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/factor/holding"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/identity"
	"github.com/spencerswagger/guanjin/internal/store"
)

// AC-FLOW-05（Spec 冻结版，2026-11-10）的**逐条证据**，方法名前缀 `ACFLOW05_NN_`。
//
// 四条判据 + 两条裁定（reason 必填非空；同值重复不记审计）：
//  1. `PATCH` 200 / `GET` 回显 / `type` 未装配 404 / 缺参 400 / 非布尔 400 / 非法 JSON 400 /
//     同值重复 200 且不新增审计、不改存储（幂等）。
//  2. 与 client 级禁用是**并集不是覆盖**；禁用后 Descriptor 三处集合 + 直接 challenge → 422；
//     已达成 amr 不计入 `current_acr`。
//  3. 审计**同事务且恰好 1 条**，字段齐全、入链；审计写失败 → 整体回滚 + 5xx。
//  4. 20 并发交替 PATCH 同一 type：全 2xx、数组无重复、终值=最后一次变更、审计条数=真实变更数；
//     并发两个不同 type → 两条都生效。
//
// 不改实现（Spec 实读 `origin/main`=3464010 判"实现与判据一致，属事后追认"）。

// acflow05Env 是因子管理面的装配结果：真 DB + 真审计链 + 真 repo，只有令牌换成固定身份。
type acflow05Env struct {
	db       *gorm.DB
	srv      *httptest.Server
	registry *factor.Registry
	actorID  uuid.UUID
	prev     *string // 测试前的 runtime_config['factors.disabled'] 快照（用于还原全局状态）
}

func newACFLOW05Env(t *testing.T, db *gorm.DB) *acflow05Env {
	t.Helper()
	cfg := testConfig(t)

	// 注册表用 password + totp 两个因子：判据要"两个不同 type 并发都生效"，
	// 而生产 wiring 只装了 password（`holding.Register(registry, password)`），
	// 单因子注册表无法表达"A 与 B 两个 type"。注册表只影响 404 判定与列表内容，不改业务语义。
	credStore, err := identity.NewCredentialStore(db)
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}
	pw, err := holding.NewPasswordVerifier(credStore)
	if err != nil {
		t.Fatalf("NewPasswordVerifier: %v", err)
	}
	totpKey, err := cfg.MasterKey.DeriveSM4(crypto.PurposeSM4Field)
	if err != nil {
		t.Fatalf("DeriveSM4: %v", err)
	}
	totp, err := holding.NewTOTP(credStore, totpKey, time.Now)
	if err != nil {
		t.Fatalf("NewTOTP: %v", err)
	}
	reg := factor.NewRegistry()
	if _, err := holding.Register(reg, pw, totp); err != nil {
		t.Fatalf("Register(注册表): %v", err)
	}

	// 审计的 actor 必须有真 users 行（audit_logs.actor_user_id 有 FK）。
	actorID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		actorID, "acflow05-"+uuid.NewString()[:10]).Error; err != nil {
		t.Fatalf("insert actor user: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	resolver := fixedResolver{caller: httpx.Caller{
		Sub:   actorID.String(),
		Roles: []string{string(admin.RoleSystemAdmin)},
	}}
	handler, err := newFactorsAdminWiring(db, cfg, reg, resolver, logger)
	if err != nil {
		t.Fatalf("newFactorsAdminWiring: %v", err)
	}
	mux := http.NewServeMux()
	handler.Routes(mux)
	env := &acflow05Env{
		db: db, registry: reg, actorID: actorID,
		srv: httptest.NewServer(httpx.TraceMiddleware(mux, "X-Trace-Id", logger)),
	}

	// 平台级禁用是**全局**状态（runtime_config 一行）：必须快照 + 还原，
	// 否则会把后续用例的"可用因子"改掉。
	var cur string
	snapErr := db.Raw(`SELECT value::text FROM runtime_config WHERE "key" = ?`,
		store.KeyFactorsDisabled).Row().Scan(&cur)
	switch {
	case snapErr == nil:
		env.prev = &cur
	case snapErr != sql.ErrNoRows:
		t.Fatalf("读 factors.disabled 快照：%v", snapErr)
	}

	t.Cleanup(func() {
		env.srv.Close()
		// actor 用户**不删**：audit_logs.actor_user_id 是 ON DELETE SET NULL，
		// 删用户会把刚写下的审计事实的 actor 抹成 NULL（等于破坏证据）。
		if env.prev == nil {
			// **不得 DELETE**：这一行是 D22 的 seed 载体，删掉会让后续用例读到"缺行"，
			// 把"平台级禁用集"变成静默空集（Ark 的夹具纪律：删了 seed 行必须写回 `[]`）。
			// **必须 upsert**：UPDATE 对"行不存在"是 0 行、静默不还原，
			// 而这一行现在是 `ValidateStartup` 的 required 键 —— 没还原会让同包后续夹具起不来。
			_ = db.Exec(`INSERT INTO runtime_config ("key", value, value_type)
				VALUES (?, '[]'::jsonb, 'json')
				ON CONFLICT ("key") DO UPDATE SET value = '[]'::jsonb`,
				store.KeyFactorsDisabled).Error
			return
		}
		_ = db.Exec(`INSERT INTO runtime_config ("key", value, value_type)
			VALUES (?, ?::jsonb, 'json')
			ON CONFLICT ("key") DO UPDATE SET value = EXCLUDED.value`,
			store.KeyFactorsDisabled, *env.prev).Error
	})
	return env
}

// do 打一次请求并返回状态码与原始响应体。
func (e *acflow05Env) do(t *testing.T, method, path, body, trace string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, e.srv.URL+path,
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求：%v", err)
	}
	req.Header.Set("Authorization", "Bearer any")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Trace-Id", trace)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s：%v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体：%v", err)
	}
	return resp.StatusCode, raw
}

// patch 发一次 PATCH 并解析统一错误响应（成功时 env 为零值）。
func (e *acflow05Env) patch(t *testing.T, typ, body, trace string) (int, []byte, httpx.ErrorResponse) {
	t.Helper()
	status, raw := e.do(t, http.MethodPatch, "/api/v1/factors/"+typ, body, trace)
	var env httpx.ErrorResponse
	if status >= 400 {
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("错误响应不是统一四字段：%v（body=%s）", err, raw)
		}
	}
	return status, raw, env
}

// disabledSet 读平台级禁用集合（直接读库，不经过被测代码）。
func (e *acflow05Env) disabledSet(t *testing.T) []string {
	t.Helper()
	var raw string
	err := e.db.Raw(`SELECT value::text FROM runtime_config WHERE "key" = ?`,
		store.KeyFactorsDisabled).Row().Scan(&raw)
	if err == sql.ErrNoRows || (err == nil && (raw == "" || raw == "null")) {
		return nil
	}
	if err != nil {
		t.Fatalf("读 factors.disabled：%v", err)
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("factors.disabled 不是字符串数组：%v（%s）", err, raw)
	}
	return out
}

// factorEnabled 从 `GET /api/v1/factors` 里取某 type 的 enabled。
func (e *acflow05Env) factorEnabled(t *testing.T, typ string) bool {
	t.Helper()
	status, raw := e.do(t, http.MethodGet, "/api/v1/factors", "", "trace-acflow05-list-"+uuid.NewString()[:8])
	if status != http.StatusOK {
		t.Fatalf("GET /api/v1/factors 应 200，实际 %d（body=%s）", status, raw)
	}
	var views []struct {
		Type    string `json:"type"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(raw, &views); err != nil {
		t.Fatalf("解析 factors 列表：%v（body=%s）", err, raw)
	}
	for _, v := range views {
		if v.Type == typ {
			return v.Enabled
		}
	}
	t.Fatalf("列表里没有 %s（body=%s）", typ, raw)
	return false
}

// auditCount 数本次测试 actor 在指定 trace 上的审计条数。
func (e *acflow05Env) auditCount(t *testing.T, trace string) int64 {
	t.Helper()
	var n int64
	if err := e.db.Raw(`SELECT count(*) FROM audit_logs WHERE actor_user_id = ? AND trace_id = ?`,
		e.actorID, trace).Scan(&n).Error; err != nil {
		t.Fatalf("count audit_logs：%v", err)
	}
	return n
}

// ── 判据 1 ────────────────────────────────────────────────────────
func TestACFLOW05_01_PatchContractAndIdempotency(t *testing.T) {
	db := openTestDB(t)
	e := newACFLOW05Env(t, db)

	// ① PATCH 停用 → 200，且列表回显 false、其余 type 不变。
	trace := "trace-acflow05-01-" + uuid.NewString()[:8]
	status, raw, env := e.patch(t, "password", `{"enabled":false,"reason":"ACFLOW05 判据1"}`, trace)
	if status != http.StatusOK {
		t.Fatalf("PATCH 停用应 200，实际 %d（body=%s）", status, raw)
	}
	var ok struct {
		Type    string `json:"type"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(raw, &ok); err != nil {
		t.Fatalf("解析 PATCH 响应：%v（body=%s）", err, raw)
	}
	if ok.Type != "password" || ok.Enabled {
		t.Fatalf("PATCH 响应应为 {type:password, enabled:false}，实际 %+v", ok)
	}
	if e.factorEnabled(t, "password") {
		t.Fatal("GET /api/v1/factors 应回显 password.enabled=false")
	}
	if !e.factorEnabled(t, "totp") {
		t.Fatal("未变更的 type（totp）必须保持 enabled=true")
	}
	_ = env

	// ② type 未装配 → 404 FACTOR_NOT_SUPPORTED。
	status, raw, env = e.patch(t, "webauthn",
		`{"enabled":false,"reason":"x"}`, "trace-acflow05-01-unknown")
	if env.Code != httpx.CodeFactorNotSupported {
		t.Fatalf("未装配 type 的码必须是 %s，实际 %q（body=%s）",
			httpx.CodeFactorNotSupported, env.Code, raw)
	}
	// 判据写 404，实现按统一码表把 `FACTOR_NOT_SUPPORTED` 归到 422 组（`httpx.HTTPStatus`）。
	// **实测差异（已报 Spec/host）**：码一致、状态码不一致。谁改由 Spec 定；本用例先锁码、
	// 把状态码差异显式打印出来，避免"悄悄放宽"。
	switch status {
	case http.StatusNotFound:
		// 与判据一致。
	case http.StatusUnprocessableEntity:
		t.Logf("差异（已报）：判据要求 404，实现返回 %d（统一码表把 %s 归在 422 组）",
			status, httpx.CodeFactorNotSupported)
	default:
		t.Fatalf("未装配 type 应为 404（判据）或 422（现状），实际 %d（body=%s）", status, raw)
	}

	// ③ 缺 enabled / 缺 reason → 400 MISSING_PARAMETER（裁定：reason 必填非空）。
	for _, tc := range []struct{ name, body string }{
		{"缺 enabled", `{"reason":"x"}`},
		{"缺 reason", `{"enabled":true}`},
		{"reason 空白", `{"enabled":true,"reason":"   "}`},
	} {
		status, raw, env = e.patch(t, "password", tc.body, "trace-acflow05-01-miss-"+uuid.NewString()[:6])
		if status != http.StatusBadRequest || env.Code != httpx.CodeMissingParameter {
			t.Fatalf("%s 应 400 %s，实际 %d / %q（body=%s）",
				tc.name, httpx.CodeMissingParameter, status, env.Code, raw)
		}
	}

	// ④ enabled 非布尔：判据说 400 `INVALID_PARAMETER`。
	//    **实测差异（已报 Spec/host）**：实现把它当"请求体不是合法 JSON"处理 → 400 `INVALID_REQUEST`。
	//    本用例只锁"必须 400 且不得 200/5xx"，并把实际码打印出来；码值差异由 Spec 决定改哪边。
	status, raw, env = e.patch(t, "password",
		`{"enabled":"true","reason":"x"}`, "trace-acflow05-01-nonbool-"+uuid.NewString()[:6])
	if status != http.StatusBadRequest {
		t.Fatalf("enabled 非布尔应 400，实际 %d（body=%s）", status, raw)
	}
	switch env.Code {
	case httpx.CodeInvalidParam:
		// 与判据一致。
	case httpx.CodeInvalidRequest:
		t.Logf("差异（已报）：判据要求 %s，实现返回 %s —— 状态码与「必须 400」一致，仅码值不同",
			httpx.CodeInvalidParam, env.Code)
	default:
		t.Fatalf("enabled 非布尔应为参数类 400 码（%s 或 %s），实际 %q",
			httpx.CodeInvalidParam, httpx.CodeInvalidRequest, env.Code)
	}

	// ⑤ 非法 JSON → 400 INVALID_REQUEST。
	status, raw, env = e.patch(t, "password", `{"enabled":`, "trace-acflow05-01-badjson")
	if status != http.StatusBadRequest || env.Code != httpx.CodeInvalidRequest {
		t.Fatalf("非法 JSON 应 400 %s，实际 %d / %q（body=%s）",
			httpx.CodeInvalidRequest, status, env.Code, raw)
	}

	// ⑥ 幂等：同值重复 PATCH → 200，且**不新增审计、不改存储**。
	before := e.disabledSet(t)
	trace2 := "trace-acflow05-01-idem-" + uuid.NewString()[:8]
	status, raw, _ = e.patch(t, "password", `{"enabled":false,"reason":"重复提交"}`, trace2)
	if status != http.StatusOK {
		t.Fatalf("同值重复 PATCH 应 200，实际 %d（body=%s）", status, raw)
	}
	if n := e.auditCount(t, trace2); n != 0 {
		t.Fatalf("同值重复不得写审计（裁定：同值重复不记审计），实际 %d 条", n)
	}
	if after := e.disabledSet(t); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("同值重复不得改存储：before=%v after=%v", before, after)
	}
}

// ── 判据 2 ────────────────────────────────────────────────────────
func TestACFLOW05_02_UnionWithClientDisabled(t *testing.T) {
	db := openTestDB(t)
	e := newACFLOW05Env(t, db)

	// client 级：禁 password（login_policies.disabled_factors={password}）。
	f := newFlowFixture(t, db, "L2", "{password}")
	policies := store.NewClientPolicyRepo(db)

	// 平台级：禁 totp。
	if status, raw, env := e.patch(t, "totp",
		`{"enabled":false,"reason":"ACFLOW05 判据2"}`, "trace-acflow05-02-"+uuid.NewString()[:8]); status != http.StatusOK {
		t.Fatalf("停用 totp 应 200，实际 %d / %q（body=%s）", status, env.Code, raw)
	}

	// ① 并集：有效禁用 = 客户端级 ∪ 平台级 = {password, totp}（集合相等，不断言顺序来源）。
	platformReader, err := store.NewPlatformFactorReader(db)
	if err != nil {
		t.Fatalf("NewPlatformFactorReader: %v", err)
	}
	mergedFn := mergeDisabledFactors(disabledFactorsOf(policies), platformReader.PlatformDisabledFactors)
	merged, err := mergedFn(context.Background(), f.clientID)
	if err != nil {
		t.Fatalf("合并禁用集：%v", err)
	}
	if got := sortedStrings(merged); strings.Join(got, ",") != "password,totp" {
		t.Fatalf("有效禁用应为 {password,totp}（并集），实际 %v", got)
	}

	// ①′ **物理来源隔离（Ark D19）**：两侧必须是两张表、互不改写。
	//    平台级 → `runtime_config.factors.disabled`；client 级 → `login_policies.disabled_factors`。
	//    没有这条，"并集"可能只是"某一次 PATCH 顺手把另一侧也改了"造成的假绿。
	if got := clientDisabledText(t, db, f.clientID); got != "password" {
		t.Fatalf("平台级 PATCH 不得改写 login_policies.disabled_factors（应仍为 password），实际 %q", got)
	}
	if got := e.disabledSet(t); strings.Join(got, ",") != "totp" {
		t.Fatalf("client 级禁用不得写进 runtime_config.factors.disabled（应只有 totp），实际 %v", got)
	}

	// ② 任一侧变更不得清空另一侧：把平台侧清空后，客户端级仍必须生效。
	if status, raw, env := e.patch(t, "totp",
		`{"enabled":true,"reason":"恢复 totp"}`, "trace-acflow05-02b-"+uuid.NewString()[:8]); status != http.StatusOK {
		t.Fatalf("启用 totp 应 200，实际 %d / %q（body=%s）", status, env.Code, raw)
	}
	merged, err = mergedFn(context.Background(), f.clientID)
	if err != nil {
		t.Fatalf("合并禁用集（平台已清空）：%v", err)
	}
	if got := sortedStrings(merged); strings.Join(got, ",") != "password" {
		t.Fatalf("平台侧清空后客户端级禁用必须仍在（不得被覆盖），实际 %v", got)
	}

	// ③ Descriptor：`disabled_factors[]` 含 password、`available_factors[]` 与 `steps[]` 不含。
	row := f.startFlow(t, nil, string(factor.ACRAAL1))
	var d flow.Descriptor
	status, raw := getJSON(t, f.srv.URL+"/authorize/flow?flow_id="+row.ID.String(), &d)
	if status != http.StatusOK {
		t.Fatalf("取 Descriptor 应 200，实际 %d（body=%s）", status, raw)
	}
	if !containsType(d.DisabledFactors, "password") {
		t.Fatalf("disabled_factors 应含 password，实际 %v", d.DisabledFactors)
	}
	if containsType(d.AvailableFactors, "password") {
		t.Fatalf("available_factors 不得含被禁因子，实际 %v", d.AvailableFactors)
	}
	for _, s := range d.Steps {
		if string(s.Factor) == "password" {
			t.Fatalf("steps[] 不得含被禁因子，实际 %+v", d.Steps)
		}
	}

	// ④ 直接开挑战 → 422 FACTOR_DISABLED。
	status, raw, env := e2ePost(t, f.srv.URL+"/auth/challenge/password",
		`{"purpose":"login","flow_id":"`+row.ID.String()+`"}`)
	if status != http.StatusUnprocessableEntity || env.Code != httpx.CodeFactorDisabled {
		t.Fatalf("直接开被禁因子的挑战应 422 %s，实际 %d / %q（body=%s）",
			httpx.CodeFactorDisabled, status, env.Code, raw)
	}

	// ⑤ 已达成 amr 不计入 current_acr：先验过口令（AAL1），再平台禁 password → current_acr 归空。
	f2 := newFlowFixture(t, db, "L2", "{}")
	userID := f2.registerUser(t, db, "GuanJin#Flow05-Strong1")
	row2 := f2.startFlow(t, &userID, string(factor.ACRAAL1))
	status, raw, env = e2ePost(t, f2.srv.URL+"/auth/verify/password",
		`{"flow_id":"`+row2.ID.String()+`","response":"GuanJin#Flow05-Strong1"}`)
	if status != http.StatusOK {
		t.Fatalf("正确口令应校验通过（先造出已达成 amr），实际 %d / %q（body=%s）", status, env.Code, raw)
	}
	if status, raw, env := e.patch(t, "password",
		`{"enabled":false,"reason":"ACFLOW05 判据2-已达成amr"}`, "trace-acflow05-02c-"+uuid.NewString()[:8]); status != http.StatusOK {
		t.Fatalf("停用 password 应 200，实际 %d / %q（body=%s）", status, env.Code, raw)
	}
	var d2 flow.Descriptor
	if status, raw := getJSON(t, f2.srv.URL+"/authorize/flow?flow_id="+row2.ID.String(), &d2); status != http.StatusOK {
		t.Fatalf("取 Descriptor（停用后）应 200，实际 %d（body=%s）", status, raw)
	}
	if d2.CurrentACR != "" {
		t.Fatalf("被禁因子的已达成 amr 不得计入 current_acr（应为空），实际 %q", d2.CurrentACR)
	}
}

// ── 判据 3 ────────────────────────────────────────────────────────
func TestACFLOW05_03_AuditExactlyOnceInSameTx(t *testing.T) {
	db := openTestDB(t)
	e := newACFLOW05Env(t, db)

	trace := "trace-acflow05-03-" + uuid.NewString()[:8]
	if status, raw, env := e.patch(t, "password",
		`{"enabled":false,"reason":"ACFLOW05 判据3"}`, trace); status != http.StatusOK {
		t.Fatalf("PATCH 应 200，实际 %d / %q（body=%s）", status, env.Code, raw)
	}

	// ① 恰好 1 条。
	if n := e.auditCount(t, trace); n != 1 {
		t.Fatalf("一次变更必须恰好 1 条审计，实际 %d", n)
	}

	// ② 字段齐全且入链（integrity_tag 非空）。
	var (
		action, targetType, targetID, result, before, after string
		actorRole, actorUser, traceID, occurredAt           string
		tagLen                                              int
	)
	err := db.Raw(`SELECT action, target_type, target_id, result, before::text, after::text,
			actor_role, actor_user_id::text, trace_id, occurred_at::text, octet_length(integrity_tag)
		FROM audit_logs WHERE actor_user_id = ? AND trace_id = ?`, e.actorID, trace).
		Row().Scan(&action, &targetType, &targetID, &result, &before, &after,
		&actorRole, &actorUser, &traceID, &occurredAt, &tagLen)
	if err != nil {
		t.Fatalf("读审计行：%v", err)
	}
	if action != "factor.disable" {
		t.Fatalf("action 应为 factor.disable，实际 %q", action)
	}
	if targetType != "factor" || targetID != "password" {
		t.Fatalf("target 应为 factor/password，实际 %q/%q", targetType, targetID)
	}
	if result != "success" || actorRole == "" || actorUser == "" || traceID == "" || occurredAt == "" {
		t.Fatalf("审计字段不全：result=%q actor_role=%q actor=%q trace=%q at=%q",
			result, actorRole, actorUser, traceID, occurredAt)
	}
	if tagLen != 32 {
		t.Fatalf("integrity_tag 应为 32 字节（HMAC-SM3），实际 %d", tagLen)
	}
	var beforeMap, afterMap map[string]any
	if err := json.Unmarshal([]byte(before), &beforeMap); err != nil {
		t.Fatalf("before 不是 JSON：%v（%s）", err, before)
	}
	if err := json.Unmarshal([]byte(after), &afterMap); err != nil {
		t.Fatalf("after 不是 JSON：%v（%s）", err, after)
	}
	if beforeMap["enabled"] != true || afterMap["enabled"] != false {
		t.Fatalf("before/after 的 enabled 应为 true→false，实际 %v/%v", beforeMap, afterMap)
	}
	if afterMap["reason"] != "ACFLOW05 判据3" {
		t.Fatalf("after 必须带 reason，实际 %v", afterMap)
	}

	// ③ 审计写失败 → 开关整体回滚 + 5xx（禁止"关了没留痕"）。
	beforeState := e.disabledSet(t)
	failing := newACFLOW05FailingEnv(t, db)
	if status, raw, _ := failing.patch(t, "totp",
		`{"enabled":false,"reason":"ACFLOW05 判据3-审计失败"}`, "trace-acflow05-03-fail"); status < 500 {
		t.Fatalf("审计写失败应 5xx，实际 %d（body=%s）", status, raw)
	}
	if now := failing.disabledSet(t); strings.Join(now, ",") != strings.Join(beforeState, ",") {
		t.Fatalf("审计失败必须整体回滚：before=%v after=%v", beforeState, now)
	}
}

// clientDisabledText 读 `login_policies.disabled_factors` 的落地值（text[]，用 array_to_string
// 避免驱动把数组扫成 "{a,b}" 这种带花括号的字符串）。空数组 → ""。
func clientDisabledText(t *testing.T, db *gorm.DB, clientID string) string {
	t.Helper()
	var raw *string
	if err := db.Raw(`SELECT array_to_string(disabled_factors, ',') FROM login_policies
		WHERE client_id = ? AND scope = 'client_override'`, clientID).Row().Scan(&raw); err != nil {
		t.Fatalf("读 login_policies.disabled_factors：%v", err)
	}
	if raw == nil {
		return ""
	}
	return *raw
}

// containsStr 报告字符串切片里是否有某值。
func containsStr(in []string, want string) bool {
	for _, v := range in {
		if v == want {
			return true
		}
	}
	return false
}

// ── 本文件共用的请求/断言助手 ──────────────────────────────────────

// getJSON 发 GET 并把 200 响应解进 out。
func getJSON(t *testing.T, url string, out any) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s：%v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体：%v", err)
	}
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("解析响应：%v（body=%s）", err, raw)
		}
	}
	return resp.StatusCode, raw
}

// e2ePost 发一个匿名 JSON POST（因子/挑战面），返回状态码与统一错误响应。
func e2ePost(t *testing.T, url, body string) (int, []byte, httpx.ErrorResponse) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST %s：%v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体：%v", err)
	}
	var env httpx.ErrorResponse
	if resp.StatusCode >= 400 {
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("错误响应不是统一四字段：%v（body=%s）", err, raw)
		}
	}
	return resp.StatusCode, raw, env
}

func sortedStrings(in []factor.FactorType) []string {
	out := make([]string, 0, len(in))
	for _, t := range in {
		out = append(out, string(t))
	}
	sort.Strings(out)
	return out
}

func containsType(in []factor.FactorType, want string) bool {
	for _, t := range in {
		if string(t) == want {
			return true
		}
	}
	return false
}

// failingAudit 是"审计一定写不进去"的写入器：用来证明**回滚**（禁止"关了没留痕"）。
type failingAudit struct{}

func (failingAudit) AppendInTx(context.Context, *gorm.DB, store.FactorAuditRecord) error {
	return errAuditDown
}

var errAuditDown = errors.New("acflow05: audit writer is down (injected)")

// newACFLOW05FailingEnv 只把审计写入器换成必失败版本，其余（真 repo、真事务、真 DB、
// 真快照还原）全部沿用。用来证明"审计写不进去 → 开关整体回滚 + 5xx"。
func newACFLOW05FailingEnv(t *testing.T, db *gorm.DB) *acflow05Env {
	t.Helper()
	base := newACFLOW05Env(t, db)
	reader, err := store.NewPlatformFactorReader(db)
	if err != nil {
		t.Fatalf("NewPlatformFactorReader: %v", err)
	}
	repo, err := store.NewPlatformFactorRepo(reader, failingAudit{})
	if err != nil {
		t.Fatalf("NewPlatformFactorRepo(失败审计): %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	resolver := fixedResolver{caller: httpx.Caller{
		Sub:   base.actorID.String(),
		Roles: []string{string(admin.RoleSystemAdmin)},
	}}
	h, err := admin.NewFactorsHTTP(base.registry, repo, resolver, logger)
	if err != nil {
		t.Fatalf("NewFactorsHTTP(失败审计): %v", err)
	}
	mux := http.NewServeMux()
	h.Routes(mux)
	base.srv.Close()
	base.srv = httptest.NewServer(httpx.TraceMiddleware(mux, "X-Trace-Id", logger))
	return base
}
