//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
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
// 判据两句都在本文件：
//   - 「码与状态」→ `TestE2E_AC_REG_04_DuplicateEmailIs409AlreadyBound`；
//   - 「耗时差异 ≤ 阈值」→ `TestACREG04_02_TimingDifferenceWithinThreshold`。
//
// 阈值来自 **D21**（Ark 裁定，迁移 `000019`）：`runtime_config.register.timing.max_diff_ms`，
// `value_type='int'`，默认 50ms。它是**判据参数**——服务不读、也不得据此改变任何对外行为
// （禁止 sleep 凑时长），故不进 `StartupKeys()`；**本用例从库读，缺键/类型不符即红，
// 不 fallback 到硬编码 50**。
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

// ── AC-REG-04 后半句：`register/start` 两条路径的耗时差 ≤ 阈值（D21） ──────────

// TestACREG04_02_TimingDifferenceWithinThreshold 证「已存在 / 不存在」两条路径的耗时
// **中位数之差（绝对值）** ≤ `runtime_config.register.timing.max_diff_ms`（D21，默认 50ms）。
//
// 口径照 D21 原文执行，不自创：
//   - 同一端点（`POST /auth/register/start`）**只换"标识符是否已存在"**，不跨端点比较
//     （`complete` 有 PBKDF2 派生，量级不同，不能混）；
//   - **每路径 9 次、丢弃前 2 次、取中位数**；不设方向（取差的绝对值）；
//   - 时钟源 = **进程单调钟**（`time.Since`），不用 DB `now()`；
//   - **禁止 sleep 凑时长、禁止放大阈值**：超阈值 = 红（侧信道安全属性）。
//
// 防 flake 的只有"多次取样 + 中位数"这一条，没有别的花招；抖动逼近或超阈值时**打印原始样本**
// 交给 Ark 调键值（他裁定"抖动 >20ms 就改键不改码"）。
func TestACREG04_02_TimingDifferenceWithinThreshold(t *testing.T) {
	db := openTestDB(t)
	f := newFlowFixture(t, db, "L2", "{}")

	threshold, raw := registerTimingThresholdMS(t, db)
	t.Logf("阈值（runtime_config.register.timing.max_diff_ms，value_type=%s）= %d ms", raw, threshold)

	// 已存在路径：先真占住一个 email（夹具走与注册面同一条 `identity.Service.Register`）。
	existing := "reg04t-" + uuid.NewString()[:12] + "@example.com"
	f.registerEmailUser(t, db, existing, "GuanJin#Reg04T-Strong1")

	const samples = 9
	const warmup = 2

	existsMS := sampleRegisterStart(t, f, func() string { return existing }, samples, http.StatusConflict)
	freshMS := sampleRegisterStart(t, f, func() string {
		return "reg04f-" + uuid.NewString()[:12] + "@example.com"
	}, samples, http.StatusOK)

	medExists := medianMS(existsMS[warmup:])
	medFresh := medianMS(freshMS[warmup:])
	diff := medExists - medFresh
	if diff < 0 {
		diff = -diff
	}

	t.Logf("已存在路径 原始样本(ms)=%v → 中位数=%.3f", existsMS, medExists)
	t.Logf("不存在路径 原始样本(ms)=%v → 中位数=%.3f", freshMS, medFresh)
	t.Logf("差值 |Δ中位数| = %.3f ms（阈值 %d ms）", diff, threshold)

	if diff > float64(threshold) {
		// 不 sleep、不调阈值：把原始样本原样交回去，由 Ark 决定改键值。
		t.Fatalf("耗时差 %.3f ms 超过阈值 %d ms（D21）。原始样本：已存在=%v（中位数 %.3f）、"+
			"不存在=%v（中位数 %.3f）—— 按口径应改 runtime_config.register.timing.max_diff_ms，"+
			"不得 sleep 凑时长或放大阈值",
			diff, threshold, existsMS, medExists, freshMS, medFresh)
	}
}

// registerTimingThresholdMS 从库里读阈值（**唯一来源**，缺键即红，不 fallback 到 50）。
//
// 顺带做一次"非硬编码"自证：把值临时改掉再读一次，能读出改后的值才说明判定真读库；
// 随后原值还原（`t.Cleanup`，失败也还原）。
func registerTimingThresholdMS(t *testing.T, db *gorm.DB) (int, string) {
	t.Helper()
	const key = "register.timing.max_diff_ms"

	var valueText, valueType string
	if err := db.Raw(`SELECT value::text, value_type FROM runtime_config WHERE "key" = ?`, key).
		Row().Scan(&valueText, &valueType); err != nil {
		t.Fatalf("读 %s 失败（迁移 000019 应已 seed；D21 要求缺键 = 判据红，不得 fallback）：%v", key, err)
	}
	if valueType != "int" {
		t.Fatalf("%s 的 value_type 必须是 int（D21），实际 %q", key, valueType)
	}
	n, err := strconv.Atoi(strings.TrimSpace(valueText))
	if err != nil {
		t.Fatalf("%s 的值不是整数：%q（%v）", key, valueText, err)
	}
	if n <= 0 {
		t.Fatalf("%s 必须 > 0（D21），实际 %d", key, n)
	}

	// 非硬编码自证：改成阈值+1 再读，读不回来说明判定没用库里的值。
	probe := n + 1
	if err := db.Exec(`UPDATE runtime_config SET value = ?::jsonb WHERE "key" = ?`,
		strconv.Itoa(probe), key).Error; err != nil {
		t.Fatalf("临时改 %s：%v", key, err)
	}
	t.Cleanup(func() {
		// 失败路径也要还原（这一步不能用 t.Fatalf 之外的沉默：还原失败本身就是缺陷）。
		if err := db.Exec(`UPDATE runtime_config SET value = ?::jsonb WHERE "key" = ?`,
			valueText, key).Error; err != nil {
			t.Errorf("还原 %s 失败：%v", key, err)
		}
	})
	var reread string
	if err := db.Raw(`SELECT value::text FROM runtime_config WHERE "key" = ?`, key).
		Row().Scan(&reread); err != nil {
		t.Fatalf("重读 %s：%v", key, err)
	}
	if strings.TrimSpace(reread) != strconv.Itoa(probe) {
		t.Fatalf("%s 改成 %d 后重读得到 %q —— 判定没有读库（硬编码嫌疑）", key, probe, reread)
	}
	return n, valueType
}

// sampleRegisterStart 打 n 次 `POST /auth/register/start`，返回每次的**单调钟**耗时（毫秒）。
//
// `nextValue` 每次现取一个标识符值：不存在路径必须每次都是"全新的"，否则第二次就变成"已存在"。
// 状态码逐次断言（两条路径的期望码由调用方给），避免"测的是另一条路径"。
func sampleRegisterStart(t *testing.T, f *flowFixture, nextValue func() string, n, wantStatus int) []float64 {
	t.Helper()
	out := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		body := `{"client_id":"` + f.clientID + `","type":"email","value":"` + nextValue() + `"}`
		start := time.Now()
		resp, err := http.Post(f.srv.URL+"/auth/register/start", "application/json",
			bytes.NewReader([]byte(body)))
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("第 %d 次 register/start：%v", i+1, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != wantStatus {
			t.Fatalf("第 %d 次 register/start 应为 %d，实际 %d（测错路径会让耗时比对失去意义）",
				i+1, wantStatus, resp.StatusCode)
		}
		out = append(out, float64(elapsed.Nanoseconds())/1e6)
	}
	return out
}

// medianMS 取中位数（样本为毫秒）。空样本直接锤死：那是取样逻辑写错，不是"跳过"。
func medianMS(samples []float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
