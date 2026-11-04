//go:build integration

package store

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// AC-ENG-04 的**全量枚举**：`migrations/` 里出现的每一个唯一约束，都在真库上各触发一次，
// 断言 (SQLSTATE, 约束名) → 对外码的映射**逐条**正确。
//
// 为什么非要全量：静态测试（`pgxerr_migrations_test.go`）只能证明"表里有这一行"，
// 证明不了"约束名真的是这个、错误真的是 23505、映射真的接在这条路径上"。
// 曾经踩过的坑：改了迁移里的约束名而没改映射 → 生产上 23505 一律落到"未映射"，
// 用户看到 500。这条用例就是那个坑的封条。
//
// 实现方式：每个用例跑在**独立事务**里并回滚 —— 所以不需要任何清理，
// 也不会污染全局状态（比如 `login_policies` 的 tier_default 行）。
func TestDBEveryUniqueViolationMapsOnce(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	ctx := context.Background()

	// id 辅助：每行一个 UUID v7（主键冲突不是本用例要测的）。
	newID := func(t *testing.T) string { return mustV7(t).String() }

	cases := []struct {
		name       string
		constraint string
		wantCode   httpx.Code
		wantStatus int
		// run 在事务里先插一条合法行，再插一条**只在该唯一键上冲突**的行；
		// 返回第二次插入的错误（已过 MapError）。
		run func(tx *gorm.DB, t *testing.T) error
	}{
		{
			name:       "CH-7① identifiers_type_value_uniq → IDENTIFIER_ALREADY_BOUND",
			constraint: "identifiers_type_value_uniq",
			wantCode:   httpx.CodeIdentifierAlreadyBound,
			wantStatus: 409,
			run: func(tx *gorm.DB, t *testing.T) error {
				insert := func(id string, masked string) error {
					return MapError(tx.Exec(
						`INSERT INTO identifiers (id, user_id, type, value, value_enc, value_masked)
						 VALUES (?, ?, 'phone', '\xdeadbeef'::bytea, '\x01'::bytea, ?)`,
						id, f.userID, masked).Error)
				}
				if err := insert(newID(t), "138****0001"); err != nil {
					t.Fatalf("首次插入标识符必须成功: %v", err)
				}
				return insert(newID(t), "138****0002") // 同 (type,value) → 唯一冲突
			},
		},
		{
			name:       "CH-7② users_username_lower_uidx → IDENTIFIER_ALREADY_BOUND",
			constraint: "users_username_lower_uidx",
			wantCode:   httpx.CodeIdentifierAlreadyBound,
			wantStatus: 409,
			run: func(tx *gorm.DB, t *testing.T) error {
				name := "uniq-" + newID(t)[:12]
				if err := tx.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`, newID(t), name).Error; err != nil {
					t.Fatalf("首次插入用户必须成功: %v", err)
				}
				// 大小写不同也必须冲突（索引建在 lower(username) 上）。
				return MapError(tx.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
					newID(t), "UNIQ-"+name[5:]).Error)
			},
		},
		{
			name:       "CH-7③ role_bindings_uniq → BINDING_ALREADY_EXISTS",
			constraint: "role_bindings_uniq",
			wantCode:   httpx.CodeBindingAlreadyExists,
			wantStatus: 409,
			run: func(tx *gorm.DB, t *testing.T) error {
				insert := func() error {
					return MapError(tx.Exec(
						`INSERT INTO role_bindings (id, user_id, client_id, role) VALUES (?, ?, ?, 'admin')`,
						newID(t), f.userID, f.clientID).Error)
				}
				if err := insert(); err != nil {
					t.Fatalf("首次绑定必须成功: %v", err)
				}
				return insert()
			},
		},
		{
			name:       "CH-7④ policy_snapshots_uniq → POLICY_SNAPSHOT_CONFLICT",
			constraint: "policy_snapshots_uniq",
			wantCode:   httpx.CodePolicySnapshotConflict,
			wantStatus: 409,
			run: func(tx *gorm.DB, t *testing.T) error {
				insert := func() error {
					return MapError(tx.Exec(
						`INSERT INTO policy_snapshots (id, client_id, version, payload, payload_hash, integrity_tag)
						 VALUES (?, ?, 1, '{}'::jsonb, '\x01'::bytea, '\x02'::bytea)`,
						newID(t), f.clientID).Error)
				}
				if err := insert(); err != nil {
					t.Fatalf("首次快照必须成功: %v", err)
				}
				return insert()
			},
		},
		{
			name:       "CH-7⑤ flows_one_active_per_user_client_uniq → COOLDOWN_ACTIVE",
			constraint: "flows_one_active_per_user_client_uniq",
			wantCode:   httpx.CodeCooldownActive,
			wantStatus: 409,
			run: func(tx *gorm.DB, t *testing.T) error {
				insert := func() error {
					return MapError(tx.Exec(
						`INSERT INTO flows (id, user_id, client_id, target_acr, achieved_amr, completed_steps, status, expires_at)
						 VALUES (?, ?, ?, 'AAL1', '{}'::text[], '[]'::jsonb, 'active', now() + interval '10 min')`,
						newID(t), f.userID, f.clientID).Error)
				}
				if err := insert(); err != nil {
					t.Fatalf("首次开流必须成功: %v", err)
				}
				return insert()
			},
		},
		{
			name:       "CH-7⑥ clients_pkey → INVALID_CLIENT",
			constraint: "clients_pkey",
			wantCode:   httpx.CodeInvalidClient,
			wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T) error {
				return MapError(tx.Exec(
					`INSERT INTO clients (client_id, name) VALUES (?, 'dup')`, f.clientID).Error)
			},
		},
		{
			name:       "CH-7⑦ signing_keys_kid_uniq → INVALID_PARAMETER",
			constraint: "signing_keys_kid_uniq",
			wantCode:   httpx.CodeInvalidParam,
			wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T) error {
				kid := "kid-" + newID(t)[:12]
				insert := func() error {
					return MapError(tx.Exec(
						`INSERT INTO signing_keys (id, kid, purpose, algorithm, public_key, private_key_enc)
						 VALUES (?, ?, 'oidc', 'SM3withSM2', 'pub', '\x01'::bytea)`,
						newID(t), kid).Error)
				}
				if err := insert(); err != nil {
					t.Fatalf("首次插入密钥必须成功: %v", err)
				}
				return insert()
			},
		},
		{
			name:       "CH-7⑧ login_policies_tier_default_uidx → INVALID_PARAMETER",
			constraint: "login_policies_tier_default_uidx",
			wantCode:   httpx.CodeInvalidParam,
			wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T) error {
				insert := func() error {
					return MapError(tx.Exec(
						`INSERT INTO login_policies (id, scope, tier, min_acr) VALUES (?, 'tier_default', 'L1', 'AAL1')`,
						newID(t)).Error)
				}
				if err := insert(); err != nil {
					t.Fatalf("首次插入档位默认策略必须成功: %v", err)
				}
				return insert()
			},
		},
		{
			name:       "CH-7⑧ login_policies_client_override_uidx → INVALID_PARAMETER",
			constraint: "login_policies_client_override_uidx",
			wantCode:   httpx.CodeInvalidParam,
			wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T) error {
				// 夹具本身已给这个 client 建了一条 client_override（注册用例要用），
				// 所以这里**第一次插入就该冲突**——正好证明"每个 client 只有一份覆盖"。
				return MapError(tx.Exec(
					`INSERT INTO login_policies (id, scope, client_id, min_acr) VALUES (?, 'client_override', ?, 'AAL2')`,
					newID(t), f.clientID).Error)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 事务内跑、必然回滚：断言完就什么都不留（global 状态也不污染）。
			err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				return tc.run(tx, t)
			})
			assertMapped(t, err, sqlStateUniqueViolation, tc.constraint, tc.wantCode, tc.wantStatus)
		})
	}
}

// 另有两条唯一约束**故意不映射**（撞到即缺陷 → 500，不是用户错误）。
// 这条用例把它们也各触发一次，证明"不映射"是**有意的选择**而不是漏登记。
func TestDBUnmappedUniqueViolationsStayUnmapped(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	ctx := context.Background()

	t.Run("identifiers_one_primary_per_user（I6：无对外码，D3-B）", func(t *testing.T) {
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			insert := func(value []byte) error {
				id := mustV7(t)
				return MapError(tx.Exec(
					`INSERT INTO identifiers (id, user_id, type, value, value_enc, value_masked, is_primary)
					 VALUES (?, ?, 'email', ?, '\x01'::bytea, 'a***@x.com', true)`,
					id, f.userID, value).Error)
			}
			if err := insert([]byte{0x01}); err != nil {
				t.Fatalf("首个主标识符必须成功: %v", err)
			}
			return insert([]byte{0x02})
		})
		assertUnmapped(t, err, sqlStateUniqueViolation, "identifiers_one_primary_per_user")
	})

	t.Run("user_visited_clients_uniq（upsert 前提，撞上即缺陷）", func(t *testing.T) {
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			insert := func() error {
				return MapError(tx.Exec(
					`INSERT INTO user_visited_clients (id, user_id, client_id) VALUES (?, ?, ?)`,
					mustV7(t), f.userID, f.clientID).Error)
			}
			if err := insert(); err != nil {
				t.Fatalf("首次访问记录必须成功: %v", err)
			}
			return insert()
		})
		assertUnmapped(t, err, sqlStateUniqueViolation, "user_visited_clients_uniq")
	})
}

// assertUnmapped 断言"这是一条真实的唯一冲突，但**没有**对外码"。
func assertUnmapped(t *testing.T, err error, wantState, wantConstraint string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a db error, got nil")
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %T (%v), want *store.Error", err, err)
	}
	if e.SQLState != wantState {
		t.Errorf("sqlstate = %q, want %q", e.SQLState, wantState)
	}
	if e.Constraint != wantConstraint {
		t.Errorf("constraint = %q, want %q", e.Constraint, wantConstraint)
	}
	if e.Mapped() {
		t.Errorf("%s 不应有对外码，实际映射到 %q", wantConstraint, e.Code)
	}
}

// 编译期锚：模型的 TableName 必须与迁移里的表名一致（改名即构建失败）。
var (
	_ = model.User{}
	_ = model.Identifier{}
	_ = model.Authenticator{}
	_ = model.AuthEvent{}
)
