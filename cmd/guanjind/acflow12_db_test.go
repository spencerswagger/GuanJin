//go:build integration

package main

import (
	"testing"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/flow"
)

// AC-FLOW-12（`D12`：`acr` 取值域上限）的**逐条证据**，方法名前缀 `ACFLOW12_`。
//
// 判据（v11，D12）：
//  1. 造 `clients.tier='L3'` → Descriptor `target_acr` **必须是 `AAL2`**（L3 → AAL2）；
//  2. **任何路径不得产出 `AAL3`**：`flows.target_acr` / `sessions.acr` 中 `='AAL3'` 的行数 = 0；
//  3. `AAL0` **任何表零落库**（DB CHECK 词表不含 `AAL0`）。
//
// 本文件证的是①②③。判据④（discovery `acr_values_supported`）属协议面，不在本文件重复。

// tierDefaultMinACR 读 `login_policies(scope='tier_default', tier=?)` 的落地门槛。
func tierDefaultMinACR(t *testing.T, db *gorm.DB, tier string) string {
	t.Helper()
	var acr string
	if err := db.Raw(`SELECT min_acr FROM login_policies WHERE scope = 'tier_default' AND tier = ?`,
		tier).Row().Scan(&acr); err != nil {
		t.Fatalf("读 tier_default(%s)：%v", tier, err)
	}
	return acr
}

// TestACFLOW12_01_L3TierTargetsAAL2AndL2TargetsAAL1 覆盖判据 ①，并带一条**正控**：
// 如果只断言"L3 → AAL2"，实现里写死 `target_acr='AAL2'` 也会绿；所以同一用例断言 L2 → AAL1。
func TestACFLOW12_01_L3TierTargetsAAL2AndL2TargetsAAL1(t *testing.T) {
	db := openTestDB(t)

	for _, tc := range []struct{ tier, want string }{
		{"L2", string(factor.ACRAAL1)},
		{"L3", string(factor.ACRAAL2)},
	} {
		// 目标值**不是**我写死的字面量，而是库里 `tier_default` 的落地值（D13 seed）：
		// 先断言它就是索引 §4.2-15 的映射，再用它建流。否则"Descriptor 回声我传的值"会假绿。
		if got := tierDefaultMinACR(t, db, tc.tier); got != tc.want {
			t.Fatalf("tier_default(%s).min_acr 应为 %s（索引 §4.2-15 / D12），实际 %q",
				tc.tier, tc.want, got)
		}
		f := newFlowFixture(t, db, tc.tier, "{}")
		row := f.startFlow(t, nil, tc.want)
		var d flow.Descriptor
		status, raw := getJSON(t, f.srv.URL+"/authorize/flow?flow_id="+row.ID.String(), &d)
		if status != 200 {
			t.Fatalf("tier=%s 取 Descriptor 应 200，实际 %d（body=%s）", tc.tier, status, raw)
		}
		if string(d.TargetACR) != tc.want {
			t.Fatalf("tier=%s 的 target_acr 应为 %s（索引 §4.2-15），实际 %q",
				tc.tier, tc.want, d.TargetACR)
		}
		// 同一条用例顺手挡掉"产出 AAL3"：目标级别只有 AAL1/AAL2 两种合法值。
		if d.TargetACR == factor.ACRAAL3Reserved {
			t.Fatalf("V1 不得产出 AAL3（D12），tier=%s 却给出 %q", tc.tier, d.TargetACR)
		}
	}
}

// TestACFLOW12_02_NoAAL3RowAnywhere 覆盖判据 ②：跑完链路后两张表的 `AAL3` 行数必须为 0。
//
// 计数用库里的**真实行**而不是代码常量：这条判据要的就是"任何路径都没把它写进去"。
func TestACFLOW12_02_NoAAL3RowAnywhere(t *testing.T) {
	db := openTestDB(t)

	// 先跑一段真实链路，让"会写 acr 的路径"有机会把 AAL3 写进去。
	//
	// 用 **L2**（目标 AAL1）：这里只装配了 password 一个因子，L3/AAL2 必然 `ACR_INSUFFICIENT`
	// （那是 AC-FLOW-07 的正确行为，不是缺陷）。L2 这条能真拿到 200，链路才算真的走完一次。
	f := newFlowFixture(t, db, "L2", "{}")
	userID := f.registerUser(t, db, "GuanJin#Flow12-Strong1")
	row := f.startFlow(t, &userID, string(factor.ACRAAL1))
	if status, raw, env := e2ePost(t, f.srv.URL+"/auth/verify/password",
		`{"flow_id":"`+row.ID.String()+`","response":"GuanJin#Flow12-Strong1"}`); status != 200 {
		t.Fatalf("口令校验应 200（让链路真的推进一次），实际 %d / %q（body=%s）", status, env.Code, raw)
	}
	// 另外把 **L3 档位**也真建一条流（D12 的关键档位）：它是最容易产出 AAL3 的地方。
	f3 := newFlowFixture(t, db, "L3", "{}")
	l3row := f3.startFlow(t, nil, string(factor.ACRAAL2))
	if l3row.TargetACR != string(factor.ACRAAL2) {
		t.Fatalf("L3 流的落库 target_acr 应为 AAL2，实际 %q", l3row.TargetACR)
	}

	for _, q := range []struct{ name, sql string }{
		{"flows.target_acr", `SELECT count(*) FROM flows WHERE target_acr = 'AAL3'`},
		{"sessions.acr", `SELECT count(*) FROM sessions WHERE acr = 'AAL3'`},
	} {
		var n int64
		if err := db.Raw(q.sql).Scan(&n).Error; err != nil {
			t.Fatalf("数 %s：%v", q.name, err)
		}
		if n != 0 {
			t.Fatalf("%s 出现 %d 行 'AAL3' —— D12 规定 V1 任何路径不得产出", q.name, n)
		}
	}
}

// TestACFLOW12_03_AAL0NeverLandsButAAL3IsReserved 覆盖判据 ③，并把它与 AAL3 的语义**分开**证：
//
//   - `AAL0` 在 CHECK 词表里**根本不存在** → 写库必被拒（23514）；
//   - `AAL3` 在词表里**存在**（预留），CHECK 允许写 → 说明"D12 不产出"是**代码侧约束**、
//     不是靠 DB 兜底。两条一起证，才谈得上"取值域上限"是设计而不是巧合。
func TestACFLOW12_03_AAL0NeverLandsButAAL3IsReserved(t *testing.T) {
	db := openTestDB(t)
	f := newFlowFixture(t, db, "L2", "{}")
	row := f.startFlow(t, nil, string(factor.ACRAAL1))

	// ① AAL0 任何表都写不进：flows.target_acr 与 sessions 同理（同一 CHECK 写法，抽 flows 验）。
	if err := db.Exec(`UPDATE flows SET target_acr = 'AAL0' WHERE id = ?`, row.ID).Error; err == nil {
		t.Fatalf("flows.target_acr='AAL0' 必须被 DB CHECK 拒（D12：AAL0 只做前端展示，零落库）")
	}
	// 全表零落库（防"别的路径绕过 CHECK"）。
	for _, q := range []struct{ name, sql string }{
		{"flows.target_acr=AAL0", `SELECT count(*) FROM flows WHERE target_acr = 'AAL0'`},
		{"sessions.acr=AAL0", `SELECT count(*) FROM sessions WHERE acr = 'AAL0'`},
	} {
		var n int64
		if err := db.Raw(q.sql).Scan(&n).Error; err != nil {
			t.Fatalf("数 %s：%v", q.name, err)
		}
		if n != 0 {
			t.Fatalf("%s 出现 %d 行 —— AAL0 不得落库", q.name, n)
		}
	}

	// ② AAL3 是**预留值**：CHECK 允许（这是它与 AAL0 的区别），写进去再改回来。
	if err := db.Exec(`UPDATE flows SET target_acr = 'AAL3' WHERE id = ?`, row.ID).Error; err != nil {
		t.Fatalf("AAL3 应在 CHECK 词表里（D12 预留值），实际被拒：%v", err)
	}
	if err := db.Exec(`UPDATE flows SET target_acr = ? WHERE id = ?`,
		string(factor.ACRAAL1), row.ID).Error; err != nil {
		t.Fatalf("还原 target_acr：%v", err)
	}

	// ③ 收尾再数一次 AAL3：本用例的临时写已还原，全局仍必须为 0。
	var leaked int64
	if err := db.Raw(`SELECT count(*) FROM flows WHERE target_acr = 'AAL3'`).Scan(&leaked).Error; err != nil {
		t.Fatalf("收尾数 AAL3：%v", err)
	}
	if leaked != 0 {
		t.Fatalf("收尾后 flows 仍有 %d 行 AAL3（临时写没还原）", leaked)
	}
}
