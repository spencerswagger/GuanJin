package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 这些用例全部**不需要真库**：验的是"缺配置就失败"和"句柄为空不 panic"这类
// fail-closed 行为——它们恰恰是最该在 CI 里每次跑的部分。

func TestOpenRequiresDSN(t *testing.T) {
	if _, err := Open(context.Background(), Options{}); err == nil {
		t.Fatal("empty DSN must be rejected (fail-closed)")
	}
	if _, err := Open(context.Background(), Options{DSN: "   "}); err == nil {
		t.Fatal("blank DSN must be rejected")
	}
}

func TestOpenRejectsMalformedDSN(t *testing.T) {
	if _, err := Open(context.Background(), Options{DSN: "://not-a-dsn"}); err == nil {
		t.Fatal("malformed DSN must be rejected at open time")
	}
}

// 连接池默认值有出处（技术栈 §7.2），不是随手拍的：这里把值钉住，
// 改默认值必须同时改文档，评审时才看得见。
func TestPoolDefaultsAreDocumented(t *testing.T) {
	if DefaultMaxOpenConns != 50 || DefaultMaxIdleConns != 10 ||
		DefaultConnMaxLifetime != 30*time.Minute {
		t.Fatalf("pool defaults changed: %d/%d/%v（技术栈 §7.2 是 50/10/30m，改这里要同步文档）",
			DefaultMaxOpenConns, DefaultMaxIdleConns, DefaultConnMaxLifetime)
	}
}

// 有了 DSN 但库不可达：Ping 必须报错（启动期 fail-fast），而不是静默成功。
func TestPingFailsOnUnreachableDB(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Options{DSN: "postgres://user:pass@127.0.0.1:1/guanjin_test"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = Close(db) }()

	if err := Ping(ctx, db); err == nil {
		t.Fatal("ping on an unreachable DB must fail")
	}
	if err := Ping(ctx, nil); err == nil {
		t.Fatal("ping(nil) must fail")
	}
}

func mustUUID(t *testing.T) uuid.UUID {
	t.Helper()
	return uuid.Must(uuid.NewV7())
}

func uuidNil() uuid.UUID { return uuid.Nil }

func TestCloseNilIsSafe(t *testing.T) {
	if err := Close(nil); err != nil {
		t.Fatalf("Close(nil) = %v, want nil", err)
	}
}

func TestTxRejectsNilArgs(t *testing.T) {
	if err := Tx(context.Background(), nil, func(*gorm.DB) error { return nil }); err == nil {
		t.Fatal("nil db must be rejected")
	}
	db, _ := dryRunDB(t)
	if err := Tx(context.Background(), db, nil); err == nil {
		t.Fatal("nil func must be rejected")
	}
}

// RuntimeConfigRepo.Lookup：空键直接拒绝；查不到 → ErrConfigKeyMissing（不是零值）。
func TestRuntimeConfigLookupEdges(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewRuntimeConfigRepo(db)

	if _, err := repo.Lookup(context.Background(), ""); !errors.Is(err, ErrConfigKeyMissing) {
		t.Fatalf("empty key err = %v, want ErrConfigKeyMissing", err)
	}
	// DryRun 下 GORM 不会返回 ErrRecordNotFound（没真正执行），所以这里只验 SQL 形态。
	// 真实的"键缺失 → ErrConfigKeyMissing"由 repo_flows_db_test.go 的
	// TestDBRuntimeConfigFlowTTL（真库）覆盖。
	if _, err := repo.Lookup(context.Background(), KeyFlowTTLSeconds); err != nil {
		t.Fatalf("DryRun lookup must not fail: %v", err)
	}
	sql := capture.last(t)
	if !strings.Contains(sql, `"key" = `) {
		t.Fatalf("lookup must filter by key column, got: %s", sql)
	}
}

// CountActiveByUserClient 在 DryRun 下不执行，但 SQL 形态要对（诊断用查询）。
func TestCountActiveByUserClientSQL(t *testing.T) {
	db, capture := dryRunDB(t)
	n, err := NewFlowRepo(db).CountActiveByUserClient(context.Background(), mustUUID(t), "client-a")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("DryRun count = %d, want 0", n)
	}
	sql := capture.last(t)
	if !strings.Contains(sql, "status = 'active'") && !strings.Contains(sql, "`status` = 'active'") {
		t.Fatalf("count must filter active flows, got: %s", sql)
	}
}

// GetByID 的边界：空 id 直接当"找不到"，不去打库。
func TestGetByIDRejectsNilID(t *testing.T) {
	db, _ := dryRunDB(t)
	if _, err := NewFlowRepo(db).GetByID(context.Background(), uuidNil()); !errors.Is(err, ErrFlowNotFound) {
		t.Fatalf("err = %v, want ErrFlowNotFound", err)
	}
}

// Error 类型的边界行为（nil 接收者、Is、Mapped、Unwrap）。
func TestErrorTypeEdges(t *testing.T) {
	var nilErr *Error
	if nilErr.Error() != "<nil>" {
		t.Fatalf("nil Error() = %q", nilErr.Error())
	}
	if nilErr.Mapped() {
		t.Fatal("nil error must not be Mapped")
	}
	if nilErr.Unwrap() != nil {
		t.Fatal("nil Unwrap must be nil")
	}

	base := MapError(pgErr(sqlStateUniqueViolation, "role_bindings_uniq"))
	var se *Error
	if !errors.As(base, &se) {
		t.Fatal("must be *store.Error")
	}
	// Is：同码同 SQLSTATE 视为同一个业务错误，便于调用方 errors.Is 判断。
	same := &Error{SQLState: sqlStateUniqueViolation, Code: codeBindingAlreadyExists}
	if !errors.Is(base, same) {
		t.Fatal("Is must match on (code, sqlstate)")
	}
	other := &Error{SQLState: sqlStateUniqueViolation, Code: codeIdentifierAlreadyBound}
	if errors.Is(base, other) {
		t.Fatal("Is must not match a different code")
	}
	if errors.Is(base, errors.New("x")) {
		t.Fatal("Is must return false for non-*Error targets")
	}
	if se.Unwrap() == nil {
		t.Fatal("Unwrap must keep the original pg error")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "x"); got != "x" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("firstNonEmpty = %q, want empty", got)
	}
}

// model 侧的可空列映射（SessionID 一次性回填 I-F7）在 store 层的可见行为：
// patch 里给了值就写、没给就不写。
func TestFlowPatchSessionBackfill(t *testing.T) {
	sessionID := mustUUID(t)
	updates := flowPatchUpdates(FlowPatch{SessionID: &sessionID})
	if len(updates) != 1 {
		t.Fatalf("patch must only set session_id, got %v", updates)
	}
	if _, ok := updates["session_id"]; !ok {
		t.Fatalf("session_id missing: %v", updates)
	}

	uid := mustUUID(t)
	updates = flowPatchUpdates(FlowPatch{UserID: &uid})
	if _, ok := updates["user_id"]; !ok || len(updates) != 1 {
		t.Fatalf("patch must only set user_id, got %v", updates)
	}

	var empty model.StringArray
	updates = flowPatchUpdates(FlowPatch{AchievedAMR: &empty})
	if v, ok := updates["achieved_amr"]; !ok {
		t.Fatal("achieved_amr must be patchable even when set to empty")
	} else if _, ok := v.(*model.StringArray); !ok {
		t.Fatalf("achieved_amr value type = %T", v)
	}
}

// DBNow：nil db 必须拒绝；DryRun 下只验 SQL 形态（`SELECT now()`，**不用应用时钟**）。
func TestDBNowSQLShape(t *testing.T) {
	if _, err := DBNow(context.Background(), nil); err == nil {
		t.Fatal("nil db 必须拒绝")
	}
	db, capture := dryRunDB(t)
	// DryRun 不执行 SQL：`Scan` 一律返回 `ErrDryRunModeUnsupported`（已知限制）。
	// 这里只验"确实发的是取库时间那条语句"，真实返回值由真库用例覆盖。
	if _, err := DBNow(context.Background(), db); !errors.Is(err, gorm.ErrDryRunModeUnsupported) {
		t.Fatalf("DryRun 下应是 ErrDryRunModeUnsupported，实际 %v", err)
	}
	if sql := capture.last(t); !strings.Contains(strings.ToLower(sql), "select now()") {
		t.Fatalf("必须取库时间，实际 SQL：%s", sql)
	}
}

// LoadLockoutPolicy：空 client_id 直接拒绝；DryRun 下验查询形态（clients / client_override / tier_default）。
func TestLoadLockoutPolicySQLShape(t *testing.T) {
	db, capture := dryRunDB(t)
	repo := NewClientPolicyRepo(db)

	if _, err := repo.LoadLockoutPolicy(context.Background(), "  "); err == nil {
		t.Fatal("空 client_id 必须拒绝")
	}
	// DryRun 不真正执行（Scan 返回 ErrDryRunModeUnsupported）：只验"确实去查了这些列"。
	if _, err := repo.LoadLockoutPolicy(context.Background(), "c-1"); !errors.Is(err, gorm.ErrDryRunModeUnsupported) {
		t.Fatalf("DryRun 下应是 ErrDryRunModeUnsupported，实际 %v", err)
	}
	// 第一条语句是 `clients` 快照（档位决定回落哪一行 tier_default）；随后的策略行查询
	// 在 DryRun 下就返回了，所以只断言首条语句形态。
	if sql := capture.last(t); !strings.Contains(strings.ToLower(sql), "from clients") {
		t.Fatalf("首条 SQL 应查 clients 档位，实际：%s", sql)
	}
}
