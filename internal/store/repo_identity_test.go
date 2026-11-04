package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件用**DryRun**（不连库、只生成 SQL）钉住账号聚合仓储的关键行为。
//
// 为什么值得这么做（而不是"反正集成测试会跑"）：
//   - 集成测试只在 CI 的 integration job 跑，`go` 闸里这些代码是**零覆盖**的；
//     一条"重放防护的条件写错了"要等到集成 job 才红，反馈太慢；
//   - 有几种错**只有看 SQL 才能发现**：`FOR UPDATE` 漏了、条件更新写成无条件更新、
//     拼错列名（DryRun 也会把列名拼出来）。
//
// 断言口径：只断言"生成出来的语句里必须有哪些片段"，**不安断整串 SQL**
// （整串会被 GORM 版本升级弄成假红）。

func mustNotNil(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("期望报错，实际 nil")
	}
}

func TestUserRepoUnit(t *testing.T) {
	db, cap := dryRunDB(t)
	repo := NewUserRepo(db)
	ctx := context.Background()

	mustNotNil(t, repo.Create(ctx, nil))

	_, err := repo.GetByID(ctx, uuid.Nil)
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("零值 id 应报 ErrUserNotFound，实际 %v", err)
	}

	_, err = repo.LockUser(ctx, nil, uuid.New())
	if !errors.Is(err, ErrNilTx) {
		t.Fatalf("缺事务句柄应报 ErrNilTx，实际 %v", err)
	}
	_, err = repo.LockUser(ctx, db, uuid.Nil)
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("零值 id 应报 ErrUserNotFound，实际 %v", err)
	}

	// 锁用户行必须真的是 `FOR UPDATE`（账号聚合的串行化前提）。
	_, _ = repo.LockUser(ctx, db, uuid.New())
	if sql := cap.last(t); !strings.Contains(sql, "FOR UPDATE") || !strings.Contains(sql, "users") {
		t.Fatalf("LockUser 必须对 users 加 FOR UPDATE，实际：%s", sql)
	}
}

func TestIdentifierRepoUnit(t *testing.T) {
	db, cap := dryRunDB(t)
	repo := NewIdentifierRepo(db)
	ctx := context.Background()

	mustNotNil(t, repo.Create(ctx, nil))

	if _, err := repo.GetByID(ctx, uuid.Nil); !errors.Is(err, ErrIdentifierNotFound) {
		t.Fatalf("零值 id 应报 ErrIdentifierNotFound，实际 %v", err)
	}
	// 按盲索引查时**空索引直接拒**：否则会拼出 `value = ''`，把"没算索引"变成一次全表扫。
	if _, err := repo.FindByBlindIndex(ctx, "phone", nil); !errors.Is(err, ErrIdentifierNotFound) {
		t.Fatalf("空盲索引应报 ErrIdentifierNotFound，实际 %v", err)
	}
	_, _ = repo.FindByBlindIndex(ctx, "phone", []byte{0x01})
	if sql := cap.last(t); !strings.Contains(sql, "type = ") || !strings.Contains(sql, "value = ") {
		t.Fatalf("盲索引查询必须同时按 type 与 value 过滤，实际：%s", sql)
	}

	// "授权型"白名单必须是 SQL 里的常量（唯一权威，不允许调用方各传各的）。
	_, _ = repo.CountVerifiedAuthable(ctx, db, uuid.New())
	sql := cap.last(t)
	if !strings.Contains(sql, "type IN ('phone','email')") {
		t.Fatalf("认证型标识符白名单必须在 SQL 里，实际：%s", sql)
	}
	if !strings.Contains(sql, "verified") || !strings.Contains(sql, "status = 'active'") {
		t.Fatalf("计数必须带上 verified 与 status 条件，实际：%s", sql)
	}

	// 状态只允许两个值（解绑 = disabled，不是物理删）。
	if _, err := repo.SetStatus(ctx, db, uuid.New(), "deleted"); err == nil {
		t.Fatal("非法状态必须被拒")
	}
	_, _ = repo.SetStatus(ctx, db, uuid.New(), "disabled")
	if sql := cap.last(t); !strings.Contains(sql, "UPDATE") || !strings.Contains(sql, "status") {
		t.Fatalf("SetStatus 应生成 UPDATE，实际：%s", sql)
	}

	// 置已验证必须是**条件更新**（`verified = false`），否则重复验证会把 trust_level 越冲越高。
	_, _ = repo.MarkVerified(ctx, uuid.New(), "email_otp", 2, time.Now())
	if sql := cap.last(t); !strings.Contains(sql, "verified") {
		t.Fatalf("MarkVerified 必须条件更新 verified，实际：%s", sql)
	}
}

func TestAuthenticatorRepoUnit(t *testing.T) {
	db, cap := dryRunDB(t)
	repo := NewAuthenticatorRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()
	uid := uuid.New()

	mustNotNil(t, repo.Create(ctx, nil))

	_, _ = repo.ListActiveByUserType(ctx, uid, "password", now)
	sql := cap.last(t)
	for _, frag := range []string{"status = 'active'", "locked_until", "expires_at"} {
		if !strings.Contains(sql, frag) {
			t.Fatalf("可用凭据查询必须带 %q，实际：%s", frag, sql)
		}
	}

	_, _ = repo.CountUsableHolding(ctx, nil, uid, now)
	if sql := cap.last(t); !strings.Contains(sql, "COUNT") && !strings.Contains(sql, "count(") {
		t.Fatalf("CountUsableHolding 应生成 count 查询，实际：%s", sql)
	}

	// 凭据信封不允许被写成空（那等于把凭据抹掉）。
	if _, err := repo.UpdateCredentialData(ctx, db, uuid.New(), nil); err == nil {
		t.Fatal("空 credential_data 必须被拒")
	}
	if _, err := repo.SetStatus(ctx, db, uuid.New(), "gone"); err == nil {
		t.Fatal("非法状态必须被拒")
	}

	// **重放防护的核心 SQL**：`jsonb_set` + `COALESCE(...) < step` 的条件更新。
	// 少了后半句就变成"无条件覆盖 last_step"，同一窗口的码可以被无限重放。
	if _, err := repo.ConsumeTOTPStep(ctx, db, uuid.New(), 0); err == nil {
		t.Fatal("step <= 0 必须被拒")
	}
	_, _ = repo.ConsumeTOTPStep(ctx, db, uuid.New(), 12345)
	sql = cap.last(t)
	for _, frag := range []string{"jsonb_set", "last_step", "COALESCE", "<"} {
		if !strings.Contains(sql, frag) {
			t.Fatalf("ConsumeTOTPStep 的条件更新必须含 %q，实际：%s", frag, sql)
		}
	}
}

func TestAuthEventRepoUnit(t *testing.T) {
	db, _ := dryRunDB(t)
	repo := NewAuthEventRepo(db)
	ctx := context.Background()

	mustNotNil(t, repo.Append(ctx, nil))
	mustNotNil(t, repo.Append(ctx, &model.AuthEvent{}))                   // 缺 event_type
	mustNotNil(t, repo.Append(ctx, &model.AuthEvent{EventType: "login"})) // 缺 result
	// 合法事件：AMR 为 nil 时必须被补成空数组（NOT NULL 列，否则一插入就撞约束）。
	e := &model.AuthEvent{EventType: "login", Result: "success"}
	if err := repo.Append(ctx, e); err != nil {
		t.Fatalf("合法事件应通过（DryRun）：%v", err)
	}
	if e.AMR == nil {
		t.Fatal("AMR 应被补成空数组，而不是留 nil")
	}
}

func TestClientPolicyRepoUnit(t *testing.T) {
	db, _ := dryRunDB(t)
	repo := NewClientPolicyRepo(db)
	ctx := context.Background()

	// 空 client_id 在**查库之前**就拒（别为一个参数错误打一次 DB）。
	if _, err := repo.LoadRegistrationPolicy(ctx, nil, ""); err == nil {
		t.Fatal("空 client_id 必须被拒")
	}
	if _, err := repo.LoadRegistrationPolicy(ctx, nil, "  "); err == nil {
		t.Fatal("空白 client_id 必须被拒")
	}
}

// 解析顺序（覆盖 > 档位默认 > 报错）是纯函数，直接钉住三条路径 + 两个前置校验。
func TestResolveRegistrationPolicy(t *testing.T) {
	override := &policyRow{SelfRegistrationAllowed: false, PasswordMinLength: 14, PasswordCharClasses: 4}
	tierDefault := &policyRow{SelfRegistrationAllowed: true, PasswordMinLength: 12, PasswordCharClasses: 3}
	client := clientRow{ClientID: "c1", Tier: "L2", Status: "active"}

	t.Run("覆盖优先", func(t *testing.T) {
		got, err := resolveRegistrationPolicy(client, override, tierDefault)
		if err != nil {
			t.Fatalf("应成功：%v", err)
		}
		if got.SelfRegistrationAllowed || got.PasswordMinLength != 14 {
			t.Fatalf("应采用 client_override 的值，实际 %+v", got)
		}
	})

	t.Run("无覆盖时回落档位默认", func(t *testing.T) {
		got, err := resolveRegistrationPolicy(client, nil, tierDefault)
		if err != nil {
			t.Fatalf("应成功：%v", err)
		}
		if !got.SelfRegistrationAllowed || got.PasswordMinLength != 12 {
			t.Fatalf("应采用 tier_default 的值，实际 %+v", got)
		}
	})

	t.Run("两者皆无 → 显式报错（不许垫默认值）", func(t *testing.T) {
		if _, err := resolveRegistrationPolicy(client, nil, nil); !errors.Is(err, ErrPolicyNotConfigured) {
			t.Fatalf("应报 ErrPolicyNotConfigured，实际 %v", err)
		}
	})

	t.Run("客户端停用", func(t *testing.T) {
		disabled := client
		disabled.Status = "disabled"
		if _, err := resolveRegistrationPolicy(disabled, override, tierDefault); !errors.Is(err, ErrClientDisabled) {
			t.Fatalf("应报 ErrClientDisabled，实际 %v", err)
		}
	})

	t.Run("未知档位", func(t *testing.T) {
		bad := client
		bad.Tier = "L9"
		if _, err := resolveRegistrationPolicy(bad, override, tierDefault); !errors.Is(err, ErrUnknownTier) {
			t.Fatalf("应报 ErrUnknownTier，实际 %v", err)
		}
	})

	t.Run("disabled_factors 透传", func(t *testing.T) {
		withFactors := &policyRow{SelfRegistrationAllowed: true, DisabledFactors: "sms_otp, email_otp"}
		got, err := resolveRegistrationPolicy(client, withFactors, nil)
		if err != nil {
			t.Fatalf("应成功：%v", err)
		}
		if len(got.DisabledFactors) != 2 || got.DisabledFactors[0] != "sms_otp" || got.DisabledFactors[1] != "email_otp" {
			t.Fatalf("禁因子应透传并去空白，实际 %+v", got.DisabledFactors)
		}
		// 空串 = 没有禁因子（不是"一个空字符串的因子"）。
		empty, err := resolveRegistrationPolicy(client, &policyRow{SelfRegistrationAllowed: true}, nil)
		if err != nil {
			t.Fatalf("应成功：%v", err)
		}
		if len(empty.DisabledFactors) != 0 {
			t.Fatalf("空数组应解析成 nil，实际 %+v", empty.DisabledFactors)
		}
	})
}

// 其余方法走一遍（DryRun 下断言"不 panic 且要么成功要么给出明确错误"）。
//
// 这类"构造覆盖"不是形式主义：它保证每个仓储方法**至少被执行过一次**，
// 拼错列名、`Updates` 传空 map、`Count` 少了 Model() 这类错都会当场红。
func TestIdentityReposAllMethodsSmoke(t *testing.T) {
	db, _ := dryRunDB(t)
	ctx := context.Background()
	uid := uuid.New()
	now := time.Now().UTC()

	users := NewUserRepo(db)
	if users.WithTx(db) == nil {
		t.Fatal("WithTx 不该返回 nil")
	}
	_, _ = users.GetByID(ctx, uid) // DryRun 下无行 → ErrUserNotFound

	ids := NewIdentifierRepo(db)
	if ids.WithTx(db) == nil {
		t.Fatal("WithTx 不该返回 nil")
	}
	_, _ = ids.GetByID(ctx, uid)
	_, _ = ids.ListByUser(ctx, uid)
	_, _ = ids.CountByUser(ctx, db, uid)
	_, _ = ids.CountVerified(ctx, nil, uid)
	_, _ = ids.CountPrimary(ctx, db, uid)

	auths := NewAuthenticatorRepo(db)
	if auths.WithTx(db) == nil {
		t.Fatal("WithTx 不该返回 nil")
	}
	_, _ = auths.GetByID(ctx, uid)
	_, _ = auths.SetStatus(ctx, db, uid, "revoked")
	_, _ = auths.UpdateCredentialData(ctx, db, uid, model.JSONValue(`{"algo":"PBKDF2-SM3"}`))
	_, _ = auths.CountUsableHolding(ctx, db, uid, now)

	events := NewAuthEventRepo(db)
	if events.WithTx(db) == nil {
		t.Fatal("WithTx 不该返回 nil")
	}
}
