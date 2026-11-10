//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
)

// AC-LOGIN-02（S11 收口补证）：错误口令 → **401 `INVALID_CREDENTIAL`**，
// 响应含**可复制**的 `trace_id`，`message` **不带区分性信息**。
//
// 为什么单开一个用例：`e2e_db_test.go` 的 AC-LOGIN-03（锁定）顺带断言了 401 与码，
// 但那是"锁定链路"的副产物——判据要的三件事（可复制 trace_id、无区分性信息、
// 不签发令牌）一条都没有独立断言。把三件事压在锁定用例里，一旦锁定链路改口径，
// 本判据会跟着一起变红/变绿，等于没证。
//
// 判据原文（acceptance-criteria.md v11 · AC-LOGIN-02）：
//
//	错误口令 → 401 INVALID_CREDENTIAL，响应含可复制 trace_id，message 无区分性信息。
//
// 「无区分性」在本用例里落成**两条路径逐字段比对**（不是"看起来没泄露"）：
//
//	A. 账号存在 + 口令错
//	B. 账号存在但**没有任何口令凭据**（holding 型里同属"验不过"）
//
// 两条的 `code` / `message` / `details` 必须**完全相同**——否则攻击者可据响应差异
// 枚举"账号有没有设口令"。`trace_id` 则必须**各不相同**（每次请求一条，才谈得上追溯）。
func TestE2E_AC_LOGIN_02_WrongPasswordIs401WithTraceID(t *testing.T) {
	db := openTestDB(t)
	f := newFlowFixture(t, db, "L2", "{}")

	// A. 有口令的账号 + 错口令。
	withPassword := f.registerUser(t, db, "GuanJin#Login02-Strong1")
	flowA := f.startFlow(t, &withPassword, string(factor.ACRAAL1))

	// B. 有账号但没有任何 `authenticators` 行（没有口令凭据）。
	//    直接插 `users`：这是"账号存在、凭据缺失"的最短构造，且不碰实现。
	noCredID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		noCredID, "s5-nocred-"+uuid.NewString()[:10]).Error; err != nil {
		t.Fatalf("insert user(no credential): %v", err)
	}
	f.userIDs = append(f.userIDs, noCredID)
	flowB := f.startFlow(t, &noCredID, string(factor.ACRAAL1))

	// 入站 trace_id 用**固定值**：能原样回到响应体/响应头，才叫"可复制、能拿去 grep 日志"。
	traceA := "login02-a-" + uuid.NewString()
	respA, headerA, bodyA, envA := f.verifyPassword(t, flowA.ID.String(), "definitely-wrong-1", traceA)

	if respA != http.StatusUnauthorized {
		t.Fatalf("错误口令应 401，实际 %d（体：%s）", respA, bodyA)
	}
	if envA.Code != httpx.CodeInvalidCredential {
		t.Fatalf("应 %s，实际 %q（体：%s）", httpx.CodeInvalidCredential, envA.Code, bodyA)
	}

	// ① trace_id 可复制：响应头 == 请求头 == 响应体字段。
	if got := headerA.Get("X-Trace-Id"); got != traceA {
		t.Fatalf("响应头 X-Trace-Id 应回显 %q，实际 %q", traceA, got)
	}
	if envA.TraceID != traceA {
		t.Fatalf("响应体 trace_id 应等于入站值 %q（可复制/可追溯），实际 %q", traceA, envA.TraceID)
	}

	// ② message 不得带区分性信息：既不能点名"口令错"，也不能点名"用户不存在"。
	if envA.Message == "" {
		t.Fatal("message 不得为空（要给人看的一句话）")
	}
	for _, leak := range []string{"口令", "密码", "password", "用户不存在", "账号不存在", "没有设置", "未绑定"} {
		if strings.Contains(envA.Message, leak) {
			t.Fatalf("message 不得含区分性信息 %q：%q", leak, envA.Message)
		}
	}

	// ③ 不得签发令牌（失败即失败，不能"错口令也回 access_token"）。
	var rawA map[string]any
	if err := json.Unmarshal(bodyA, &rawA); err != nil {
		t.Fatalf("解析响应体：%v（体：%s）", err, bodyA)
	}
	for _, forbidden := range []string{"access_token", "id_token", "refresh_token"} {
		if _, ok := rawA[forbidden]; ok {
			t.Fatalf("错误口令响应不得含 %s（体：%s）", forbidden, bodyA)
		}
	}

	// B. 无口令凭据的账号 —— 对外表现必须与 A 一致。
	traceB := "login02-b-" + uuid.NewString()
	respB, headerB, bodyB, envB := f.verifyPassword(t, flowB.ID.String(), "definitely-wrong-2", traceB)
	if respB != http.StatusUnauthorized {
		t.Fatalf("无口令凭据应同样 401（不得暴露「有没有设口令」），实际 %d（体：%s）", respB, bodyB)
	}
	if envB.Code != envA.Code || envB.Message != envA.Message {
		t.Fatalf("两条路径必须同码同文案（无区分性）：\nA: %s / %q\nB: %s / %q",
			envA.Code, envA.Message, envB.Code, envB.Message)
	}
	if envA.Details != nil || envB.Details != nil {
		t.Fatalf("失败响应不得带 details（会泄露内部状态）：A=%v B=%v", envA.Details, envB.Details)
	}
	if envB.TraceID != traceB || headerB.Get("X-Trace-Id") != traceB {
		t.Fatalf("B 的 trace_id 应可复制：header=%q body=%q 期望 %q",
			headerB.Get("X-Trace-Id"), envB.TraceID, traceB)
	}
	if envA.TraceID == envB.TraceID {
		t.Fatalf("两次请求的 trace_id 不得相同（%q）——否则无法按 trace 区分请求", envA.TraceID)
	}
}

// verifyPassword 打一次 `POST /auth/verify/password` 并返回**原始**状态码/头/体。
//
// 不复用 `f.post`：那个助手只解码 `httpx.ErrorResponse`，拿不到原始字节与响应头，
// 而本判据要断言的就是"响应头与响应体里的 trace_id 一致"。
func (f *flowFixture) verifyPassword(t *testing.T, flowID, response, traceID string) (int, http.Header, []byte, httpx.ErrorResponse) {
	t.Helper()
	body := `{"flow_id":"` + flowID + `","response":"` + response + `"}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		f.srv.URL+"/auth/verify/password", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("构造请求：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Trace-Id", traceID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /auth/verify/password：%v", err)
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
