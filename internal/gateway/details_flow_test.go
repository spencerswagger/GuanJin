package gateway

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// 本文件把 `details.flow` 的**序列化形状**钉死（口径来源：`internal/flow/descriptor.go`
// 的 `Descriptor` / `Step`，S5 与数据面共用同一个 `flow.BuildDescriptor`）。
//
// 为什么值得单独一批用例：这一层最容易被"顺手加个字段"破掉，而加字段等于**新增对外契约面**
// （F0 禁自造 wire 名），且前端与 SDK 会立刻按新形状对接。三条已核实的现状：
//
//  1. **顶层没有 `challenge_id`**——它只存在于 `steps[i].challenge_id`（`*string,omitempty`）；
//  2. **`current_acr` 无 `omitempty`**——无值时序列化成 `"current_acr": ""`（前端类型写
//     `Acr | null`，属已知待统一项，等裁定后再改，本切片不顺手改）；
//  3. `unreachable_reason` / `contributes_acr` 是 `omitempty`，不可达/无贡献时不出现。
//
// 数据面**只读** `flows` 并复用同一个构造函数，所以这里的断言同时也是"数据面没有第二套
// 渲染逻辑"的证据。

// detailsFlow 取出 403 响应里的 `details.flow` 原始 JSON 对象。
func detailsFlow(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var resp struct {
		Details struct {
			Flow map[string]any `json:"flow"`
		} `json:"details"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode body: %v (%s)", err, body)
	}
	if resp.Details.Flow == nil {
		t.Fatalf("响应里没有 details.flow：%s", body)
	}
	return resp.Details.Flow
}

// 顶层键集合必须与 `flow.Descriptor` 完全一致：多一个就是自造契约面。
func TestDetailsFlowTopLevelShape(t *testing.T) {
	tg := newTestGuard(t, func(g *testGuard) {
		g.policies.policy.MinACR = "AAL2"
		g.factors = testFactorsTwo()
	})
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d", rec.Code)
	}
	flow := detailsFlow(t, rec.Body.Bytes())

	// 顶层允许的键（`unreachable_reason` 只在不可达时出现，故单独判）。
	got := make([]string, 0, len(flow))
	for k := range flow {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"available_factors", "client", "current_acr", "disabled_factors",
		"expires_at", "flow_id", "satisfied", "steps", "target_acr", "terminal_state"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("details.flow 顶层键不符（多/少都是契约变更）\n got=%v\nwant=%v", got, want)
	}
	if _, exists := flow["challenge_id"]; exists {
		t.Fatal("details.flow 顶层出现了 challenge_id —— 该字段只属 steps[i]，顶层出现即自造契约面")
	}

	// `current_acr` / `target_acr` 必须出现（D16④ 的 AC-DP-09 断言要读它们）。
	if flow["current_acr"] != "AAL1" || flow["target_acr"] != "AAL2" {
		t.Fatalf("级别不符：current=%v target=%v", flow["current_acr"], flow["target_acr"])
	}

	// steps：每步有 factor/role/status/purpose；`challenge_id` 只在预签挑战时出现。
	steps, ok := flow["steps"].([]any)
	if !ok || len(steps) == 0 {
		t.Fatalf("steps 不是非空数组：%v", flow["steps"])
	}
	for i, raw := range steps {
		step, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("steps[%d] 不是对象：%v", i, raw)
		}
		for _, key := range []string{"factor", "role", "status", "purpose"} {
			if _, exists := step[key]; !exists {
				t.Fatalf("steps[%d] 缺 %q：%v", i, key, step)
			}
		}
		if v, exists := step["challenge_id"]; exists && v != nil {
			t.Fatalf("steps[%d].challenge_id 在未预签挑战时不该有值：%v", i, v)
		}
	}
}

// `current_acr` 无 `omitempty`：算不出级别时必须是 `"current_acr": ""`，
// 而不是省略字段或写成 null（前端类型是 `Acr | null`，空串是**现状**，改动等裁定）。
func TestDetailsFlowCurrentACRIsNeverOmitted(t *testing.T) {
	tg := newTestGuard(t, func(g *testGuard) {
		g.policies.policy.MinACR = "AAL2"
		g.factors = testFactorsTwo()
		// 会话没有任何已达成因子 → 算不出 current_acr。
		g.sessions.snap.AMR = []string{}
	})
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"current_acr":""`) {
		t.Fatalf("current_acr 必须序列化成空串（无 omitempty），实际：%s", rec.Body.String())
	}
	flow := detailsFlow(t, rec.Body.Bytes())
	if _, exists := flow["current_acr"]; !exists {
		t.Fatal("current_acr 键缺失：不得省略")
	}
}

// 不可达时才出现 `unreachable_reason`（`omitempty`）——M1 只有 password、目标 AAL2 即不可达。
func TestDetailsFlowUnreachableReasonOnlyWhenUnreachable(t *testing.T) {
	tg := newTestGuard(t, func(g *testGuard) { g.policies.policy.MinACR = "AAL2" })
	up := newRecordedUpstream(t)
	p := newTestPipeline(t, tg, up)

	rec := doRequest(p, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg.keys, nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("期望 403，实际 %d", rec.Code)
	}
	flow := detailsFlow(t, rec.Body.Bytes())
	if flow["terminal_state"] != "unreachable" {
		t.Fatalf("单因子下目标 AAL2 应判 unreachable，实际 %v", flow["terminal_state"])
	}
	if _, exists := flow["unreachable_reason"]; !exists {
		t.Fatalf("不可达必须带 unreachable_reason：%v", flow)
	}

	// 反向：可达（need_more）时该键不得出现。
	tg2 := newTestGuard(t, func(g *testGuard) {
		g.policies.policy.MinACR = "AAL2"
		g.factors = testFactorsTwo()
	})
	up2 := newRecordedUpstream(t)
	p2 := newTestPipeline(t, tg2, up2)
	rec2 := doRequest(p2, http.MethodGet, "/api/billing/orders", issueUserToken(t, tg2.keys, nil))
	flow2 := detailsFlow(t, rec2.Body.Bytes())
	if flow2["terminal_state"] != "need_more" {
		t.Fatalf("两因子下应判 need_more，实际 %v", flow2["terminal_state"])
	}
	if _, exists := flow2["unreachable_reason"]; exists {
		t.Fatalf("可达时不该出现 unreachable_reason：%v", flow2)
	}
}
