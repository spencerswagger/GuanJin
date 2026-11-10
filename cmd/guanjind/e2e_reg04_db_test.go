//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/identity"
	"github.com/spencerswagger/guanjin/internal/store"
)

// AC-REG-04（S11 收口补证）：**已存在 email 再注册 → 403/409 `IDENTIFIER_ALREADY_BOUND`**。
//
// 本 PR 只补「码与状态」这半条的可执行证据；判据后半句「耗时与不存在账号差异 ≤ 阈值
// （阈值取自 `runtime_config`）」暂不可判 —— 见 PR 描述的「未决假设」节（现行
// `runtime_config` 只有 `challenge.ttl_seconds` / `challenge.max_attempts` / `flow.ttl_seconds`
// 三个键，没有时序阈值键；按纪律不许在代码/用例里留一个自造的数字冒充阈值）。
//
// 为什么要在 HTTP 层证：判据写的是 e2e。服务层已有 `TestStartRejectsBadAndBoundIdentifiers`
// （registration_db_test.go:274）证明 `StartRegistration` 会报 `ErrIdentifierAlreadyBound`，
// 但那证不到"对外到底是 409 还是 500"——状态码与码表映射在 handler，必须在装配后的链路上看。
func TestE2E_AC_REG_04_DuplicateEmailIs409AlreadyBound(t *testing.T) {
	db := openTestDB(t)
	f := newFlowFixture(t, db, "L2", "{}")

	// 夹具：先让这个 email 真被占用（走与注册面同一条 `identity.Service.Register`）。
	email := "reg04-" + uuid.NewString()[:12] + "@example.com"
	f.registerEmailUser(t, db, email, "GuanJin#Reg04-Strong1")

	before := countChallenges(t, db, f.clientID)

	// ① 同一 email 再走 `register/start`：必须 409 `IDENTIFIER_ALREADY_BOUND`。
	trace := "reg04-" + uuid.NewString()
	status, header, body, env := f.postRaw(t, "/auth/register/start",
		`{"client_id":"`+f.clientID+`","type":"email","value":"`+email+`"}`, trace)

	if status != http.StatusConflict {
		t.Fatalf("已存在 email 再注册应 409，实际 %d（体：%s）", status, body)
	}
	if env.Code != httpx.CodeIdentifierAlreadyBound {
		t.Fatalf("应 %s，实际 %q（体：%s）", httpx.CodeIdentifierAlreadyBound, env.Code, body)
	}

	// ② trace_id 可复制（排障入口），message 非空且不点名"该 email 属于谁"。
	if header.Get("X-Trace-Id") != trace || env.TraceID != trace {
		t.Fatalf("trace_id 应可复制：header=%q body=%q 期望 %q",
			header.Get("X-Trace-Id"), env.TraceID, trace)
	}
	if env.Message == "" {
		t.Fatal("message 不得为空（要给人看的一句话）")
	}

	// ③ 拒绝必须是**零副作用**：不下发挑战、不签发 register_token。
	if after := countChallenges(t, db, f.clientID); after != before {
		t.Fatalf("重复注册不得新建挑战行：before=%d after=%d", before, after)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("解析响应体：%v（体：%s）", err, body)
	}
	if _, ok := raw["register_token"]; ok {
		t.Fatalf("拒绝路径不得签发 register_token（体：%s）", body)
	}
}

// postRaw 打一个 JSON POST 并返回原始状态码/头/体（本判据要断言响应头与 trace_id）。
func (f *flowFixture) postRaw(t *testing.T, path, body, traceID string) (int, http.Header, []byte, httpx.ErrorResponse) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		f.srv.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("构造请求：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Trace-Id", traceID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s：%v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读响应体：%v", err)
	}
	var env httpx.ErrorResponse
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("错误响应不是统一四字段：%v（体：%s）", err, raw)
	}
	return resp.StatusCode, resp.Header, raw, env
}

// registerEmailUser 用**指定的** email 建一个已注册账号（`e2e_db_test.go` 的
// `registerUser` 自己生成 email，本判据需要拿得到那个值去"再注册一次"）。
//
// 走的是与注册面同一条 `identity.Service.Register`：夹具不另造表写入路径，
// 否则"已存在"这件事可能只是夹具造出来的假象。
func (f *flowFixture) registerEmailUser(t *testing.T, db *gorm.DB, email, password string) uuid.UUID {
	t.Helper()
	// 关键：必须用**装配链路同一把主密钥**派生盲索引键。
	// `e2e_db_test.go` 的 `registerUser` 用 `byte(i+7)` 自造主密钥，那是给"不查标识符索引"
	// 的用例准备的；拿它建"已存在"的 email，盲索引与装配侧算出来的不是同一个值，
	// 于是 `register/start` 查不到、返回 200 —— 首版就是这么红的（见 PR 讨论）。
	mk := testConfig(t).MasterKey
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
		FlowTTL:              time.Hour,
		ChallengeTTL:         startupCfg.ChallengeTTL,
		ChallengeMaxAttempts: startupCfg.ChallengeMaxAttempts,
	})
	if err != nil {
		t.Fatalf("identity.New: %v", err)
	}
	res, err := svc.Register(context.Background(), identity.RegisterRequest{
		ClientID:     f.clientID,
		Type:         "email",
		Value:        email,
		Password:     password,
		Verified:     true,
		VerifyMethod: "email_otp",
		TrustLevel:   2,
		TraceID:      "trace-" + uuid.NewString()[:8],
	})
	if err != nil {
		t.Fatalf("Register（夹具）：%v", err)
	}
	f.userIDs = append(f.userIDs, res.UserID)
	return res.UserID
}
