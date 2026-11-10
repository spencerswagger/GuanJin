package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 本文件是 `repo_admission.go`（S8 数据面的三条窄读取/写入）的**SQL 形状单测**：
// 不连库，只用 `dryRunDB` 看"生成的 SQL 长什么样"。
//
// 为什么要这么测：这三条是数据面的热路径（每请求都跑），最容易出的错不是"逻辑分支"，
// 而是**取错列/漏了优先级/upsert 少了累加**。行级行为由 `repo_admission_db_test.go`
// 与 `internal/gateway/adapter` 的真库用例兜；这里锁住"SQL 里必须出现什么、不许出现什么"。

func TestLoadAdmissionPolicyQueriesOnlyNeededColumns(t *testing.T) {
	db, capture := dryRunDB(t)
	_, _ = NewClientPolicyRepo(db).LoadAdmissionPolicy(context.Background(), "billing")

	sql := capture.last(t)
	// 只取四列：数据面每请求都跑这条查询，把 `secret_hash` 之类的列带出来是浪费也是风险。
	for _, want := range []string{"client_id", "name", "tier", "status", "FROM clients"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("clients 查询缺 %q：%s", want, sql)
		}
	}
	if strings.Contains(sql, "secret_hash") || strings.Contains(sql, "secret_enc") {
		t.Fatalf("数据面不得读对接密钥列：%s", sql)
	}
}

func TestAdmissionRowSelectsMinACRAndDisabledFactors(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewClientPolicyRepo(db)
	// DryRun + Raw 的 Scan 会直接报 "dry run mode unsupported"：这条形状断言只关心
	// **它发出的 SQL**，所以忽略返回错误（错误本身由真库用例判）。
	_, _ = repo.admissionRow(context.Background(), `scope = 'client_override' AND client_id = ?`, "billing")

	sql := capture.last(t)
	// `array_to_string` 是刻意的：`database/sql` 不认 PG 的 text[]→[]string（与 policyRow 同因）。
	for _, want := range []string{"min_acr", "array_to_string", "disabled_factors", "FROM login_policies", "LIMIT 1"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("login_policies 查询缺 %q：%s", want, sql)
		}
	}
}

// 空 client_id / nil repo 必须直接报错（不发一条无用查询，也不 panic）。
func TestLoadAdmissionPolicyRejectsEmptyInput(t *testing.T) {
	db, capture := dryRunDB(t)
	if _, err := NewClientPolicyRepo(db).LoadAdmissionPolicy(context.Background(), "   "); err != ErrClientNotFound {
		t.Fatalf("空 client_id 期望 ErrClientNotFound，实际 %v", err)
	}
	if len(capture.queries) != 0 {
		t.Fatalf("空 client_id 不该发查询，实际发了 %d 条", len(capture.queries))
	}

	var nilRepo *ClientPolicyRepo
	if _, err := nilRepo.LoadAdmissionPolicy(context.Background(), "billing"); err == nil {
		t.Fatal("nil repo 期望报错")
	}
}

// `RecordVisit` 必须是**单条 upsert**：`ON CONFLICT (user_id, client_id)` + `visit_count + 1`。
//
// 读-改-写在每请求路径上必撞并发（AC-SESS-04 的多副本形态），所以这条形状是硬要求。
func TestRecordVisitIsSingleUpsert(t *testing.T) {
	db, capture := dryRunDB(t)
	err := NewVisitRepo(db).RecordVisit(context.Background(), uuid.New(), "billing", "L2", time.Now().UTC())
	if err != nil {
		t.Fatalf("RecordVisit: %v", err)
	}
	sql := capture.last(t)
	for _, want := range []string{"INSERT INTO user_visited_clients", "ON CONFLICT (user_id, client_id)",
		"visit_count", "+ 1", "last_seen_at", "tier_at_visit"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("upsert 缺 %q：%s", want, sql)
		}
	}
	// 一次调用只许一条 SQL：两条就是"先查再写"，会撞并发。
	if len(capture.queries) != 1 {
		t.Fatalf("期望 1 条 SQL，实际 %d 条：%v", len(capture.queries), capture.queries)
	}
}

func TestRecordVisitRejectsBadInput(t *testing.T) {
	db, _ := dryRunDB(t)
	repo := NewVisitRepo(db)
	if err := repo.RecordVisit(context.Background(), uuid.Nil, "billing", "L2", time.Now()); err == nil {
		t.Fatal("nil user id 必须报错")
	}
	if err := repo.RecordVisit(context.Background(), uuid.New(), "  ", "L2", time.Now()); err == nil {
		t.Fatal("空 client_id 必须报错")
	}
	var nilRepo *VisitRepo
	if err := nilRepo.RecordVisit(context.Background(), uuid.New(), "billing", "L2", time.Now()); err == nil {
		t.Fatal("nil repo 必须报错")
	}
}

// 解析顺序是**纯函数**，直接钉住：覆盖优先 → 同档位默认 → 都没有即配置缺陷。
//
// 这三条任一写错，数据面就会用错门槛：拿默认档位放行本该系统专属更严的 L3 业务，
// 或者反过来把"配置没配"当成"默认 AAL1"（D16② 明确禁止垫默认值）。
func TestResolveAdmissionPolicyOrder(t *testing.T) {
	client := admissionClient{ClientID: "billing", Name: "计费", Tier: "L3", Status: "active"}
	override := &admissionRow{MinACR: "AAL2", DisabledFactors: "otp"}
	tierDefault := &admissionRow{MinACR: "AAL1", DisabledFactors: ""}

	t.Run("覆盖行优先", func(t *testing.T) {
		got, err := resolveAdmissionPolicy(client, override, tierDefault)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got.MinACR != "AAL2" || len(got.DisabledFactors) != 1 || got.DisabledFactors[0] != "otp" {
			t.Fatalf("覆盖行未生效：%+v", got)
		}
		if got.Tier != "L3" || got.ClientName != "计费" {
			t.Fatalf("client 字段未搬运：%+v", got)
		}
	})

	t.Run("回落到同档位默认", func(t *testing.T) {
		got, err := resolveAdmissionPolicy(client, nil, tierDefault)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got.MinACR != "AAL1" {
			t.Fatalf("回落值不符：%+v", got)
		}
	})

	t.Run("两侧都没有 = 配置缺陷", func(t *testing.T) {
		if _, err := resolveAdmissionPolicy(client, nil, nil); err != ErrPolicyNotConfigured {
			t.Fatalf("期望 ErrPolicyNotConfigured，实际 %v", err)
		}
	})

	t.Run("业务系统不存在", func(t *testing.T) {
		if _, err := resolveAdmissionPolicy(admissionClient{}, override, nil); err != ErrClientNotFound {
			t.Fatalf("期望 ErrClientNotFound，实际 %v", err)
		}
	})

	t.Run("业务系统已停用", func(t *testing.T) {
		disabled := client
		disabled.Status = "disabled"
		if _, err := resolveAdmissionPolicy(disabled, override, nil); err != ErrClientDisabled {
			t.Fatalf("期望 ErrClientDisabled，实际 %v", err)
		}
	})
}

// ListClientIDs 必须是一条有序查询（`all_clients` 的 SLO 级联范围）；非法入参直接报错。
func TestListClientIDsShapeAndValidation(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewVisitRepo(db)

	// DryRun 下 Scan 报 "dry run mode unsupported"：只验它发出的 SQL。
	_, _ = repo.ListClientIDs(context.Background(), uuid.MustParse("0193f0aa-0000-7000-8000-000000000001"))
	sql := capture.last(t)
	for _, want := range []string{"SELECT client_id", "user_visited_clients", "WHERE user_id = ", "ORDER BY client_id"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("ListClientIDs 查询缺 %q：%s", want, sql)
		}
	}

	if _, err := repo.ListClientIDs(context.Background(), uuid.Nil); err == nil {
		t.Fatal("nil user_id 必须报错")
	}
	var nilRepo *VisitRepo
	if _, err := nilRepo.ListClientIDs(context.Background(), uuid.New()); err == nil {
		t.Fatal("nil repo 必须报错")
	}
}
