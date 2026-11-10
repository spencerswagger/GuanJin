//go:build integration

package store

// **AC-ENG-04 的真库矩阵**：每一行都由 PostgreSQL **真的抛出**错误，再走生产同一条
// `MapError` 路径，把「真实 SQLSTATE / 错误类型」与「实际返回的 code / HTTP 状态」并列打一行
// 机器可读日志（`AC_ENG_04|row=…`）。
//
// 为什么必须真触发（而不是 hand-craft 一个 `*pgconn.PgError`）：构造出来的错误只能证明
// "表里怎么写"，证明不了——约束名真的是这个字符串、驱动回来的形状真是 `*pgconn.PgError`
// （而不是 `*pgconn.PgConnError` / `*net.OpError` / `*pgconn.errTimeout`）、以及这条映射
// 真的挂在实际执行路径上。单测仍有价值（把表钉死），但**不能当"已逐行验证"**。
//
// 与既有用例的分工（不重复劳动，各管一段）：
//   - `TestDBEveryUniqueViolationMapsOnce`：**防漏登记**——迁移里每个唯一约束都要在
//     `uniqueRules` 里有位置（新增约束即红）；
//   - 本文件：**逐行取证**——每行给出"触发方式 + 实测 SQLSTATE + 映射结果"，并把
//     "未映射 ⇒ fail-closed"的行也列出来（不许用"未知错误一律 500 也算过"糊过去）。
//     本文件里的 CH-7①–⑧ 与上面的枚举**故意重复**：一份是登记完整性闸，一份是逐行证据；
//     防漂移由 `TestAC_ENG_04_MatrixCoversEveryUniqueRule` 钉住（矩阵少一行即红）。

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// matrixRow 是矩阵的一行。
type matrixRow struct {
	id         string // 行号（对外引用用，如 "CH-7③"）
	trigger    string // 怎么触发的（一句话）
	wantState  string // 期望/实测的 SQLSTATE
	wantConstr string // 期望的约束名（空 = 该错误没有约束名）
	wantCode   httpx.Code
	wantStatus int
	// retryable：并发冲突（40001/40P01）**故意不给业务码**——调用方按 `Retryable` 决定重试，
	// 所以它们"未映射"不等于"fail-closed 缺陷"。这一列把两者区分开，避免证据行自相矛盾。
	retryable bool
	// run 在**独立事务**里制造错误（事务必然回滚，不留数据）；返回**未经 MapError** 的原始错误。
	run func(tx *gorm.DB, t *testing.T, newID func() string) error
}

// logRow 打一行机器可读证据（供 PR/汇报直接引）并断言映射结果。
// `mapped==false` 表示"未映射 ⇒ HTTP 层 500 的缺陷分支"。
func logRow(t *testing.T, row, trigger, state, constraint string, got *Error, mapped, retryable bool) {
	t.Helper()
	code, status := string(got.Code), got.Status
	if !mapped {
		code, status = "UNMAPPED", 0
	}
	t.Logf("AC_ENG_04|row=%s|trigger=%s|sqlstate=%s|constraint=%s|code=%s|status=%d|mapped=%v|retryable=%v|failclosed=%v",
		row, trigger, firstNonEmpty(state, "-"), firstNonEmpty(constraint, "-"), code, status,
		mapped, retryable, !mapped && !retryable)
}

// assertRow 断言 (SQLSTATE, 约束名) 是**服务端真的给的**，且映射结果与期望一致。
// `wantCode == ""` 表示期望 **fail-closed（未映射）**。
func assertRow(t *testing.T, err error, wantState, wantConstr string, wantCode httpx.Code, wantStatus int) *Error {
	t.Helper()
	if err == nil {
		t.Fatal("期望 PG 报错，实际 nil")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("期望 *pgconn.PgError（服务端错误），实际 %T: %v", err, err)
	}
	if pgErr.Code != wantState {
		t.Fatalf("真实 SQLSTATE = %q，期望 %q（%v）", pgErr.Code, wantState, pgErr)
	}
	if wantConstr != "" && pgErr.ConstraintName != wantConstr {
		t.Fatalf("真实约束名 = %q，期望 %q", pgErr.ConstraintName, wantConstr)
	}
	mapped := MapError(err)
	var got *Error
	if !errors.As(mapped, &got) {
		t.Fatalf("MapError 返回 %T，期望 *store.Error", mapped)
	}
	if wantCode == "" {
		if got.Mapped() {
			t.Fatalf("期望 fail-closed（未映射），实际映射成 %q/%d（理由：%s）",
				got.Code, got.Status, got.Reason)
		}
		return got
	}
	if got.Code != wantCode || got.Status != wantStatus {
		t.Fatalf("映射结果 = %s/%d，期望 %s/%d（理由：%s）",
			got.Code, got.Status, wantCode, wantStatus, got.Reason)
	}
	if got.Reason == "" {
		t.Fatal("映射必须带 reason")
	}
	if got.Status != httpx.HTTPStatus(got.Code) {
		t.Fatalf("状态 %d 与码表 httpx.HTTPStatus(%s)=%d 不一致",
			got.Status, got.Code, httpx.HTTPStatus(got.Code))
	}
	return got
}

// TestAC_ENG_04_RealSQLStateMatrix 逐行触发 + 逐行取证。
func TestAC_ENG_04_RealSQLStateMatrix(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	ctx := context.Background()
	newID := func(t *testing.T) string { return mustV7(t).String() }

	// 同一 user×client 的第二条 active 流（CH-7⑤ 的触发体）。
	insertFlow := func(tx *gorm.DB, t *testing.T) error {
		return tx.Exec(`INSERT INTO flows (id, user_id, client_id, target_acr, achieved_amr,
				completed_steps, status, expires_at)
			VALUES (?, ?, ?, 'AAL1', '{}'::text[], '[]'::jsonb, 'active', now() + interval '10 min')`,
			mustV7(t), f.userID, f.clientID).Error
	}

	rows := []matrixRow{
		// ===== CH-7 八行（对齐 decisions.md D4-D₂）=====
		{
			id: "CH-7①", trigger: "identifiers (type,value) 重复插入",
			wantState: sqlStateUniqueViolation, wantConstr: "identifiers_type_value_uniq",
			wantCode: codeIdentifierAlreadyBound, wantStatus: 409,
			run: func(tx *gorm.DB, t *testing.T, newID func() string) error {
				ins := func(masked string) error {
					return tx.Exec(`INSERT INTO identifiers (id, user_id, type, value, value_enc, value_masked)
						VALUES (?, ?, 'phone', '\xdeadbeef'::bytea, '\x01'::bytea, ?)`,
						newID(), f.userID, masked).Error
				}
				if err := ins("138****0001"); err != nil {
					t.Fatalf("首次插入必须成功: %v", err)
				}
				return ins("138****0002")
			},
		},
		{
			id: "CH-7②", trigger: "users.username 大小写不同但 lower() 相同",
			wantState: sqlStateUniqueViolation, wantConstr: "users_username_lower_uidx",
			wantCode: codeIdentifierAlreadyBound, wantStatus: 409,
			run: func(tx *gorm.DB, t *testing.T, newID func() string) error {
				name := "mx-" + uuid.NewString()[:12]
				if err := tx.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`, newID(), name).Error; err != nil {
					t.Fatalf("首次插入必须成功: %v", err)
				}
				upper := "MX-" + name[3:]
				return tx.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`, newID(), upper).Error
			},
		},
		{
			id: "CH-7③", trigger: "role_bindings (user,client,role) 重复",
			wantState: sqlStateUniqueViolation, wantConstr: "role_bindings_uniq",
			wantCode: codeBindingAlreadyExists, wantStatus: 409,
			run: func(tx *gorm.DB, t *testing.T, newID func() string) error {
				ins := func() error {
					return tx.Exec(`INSERT INTO role_bindings (id, user_id, client_id, role)
						VALUES (?, ?, ?, 'admin')`, newID(), f.userID, f.clientID).Error
				}
				if err := ins(); err != nil {
					t.Fatalf("首次绑定必须成功: %v", err)
				}
				return ins()
			},
		},
		{
			id: "CH-7④", trigger: "policy_snapshots (client,version) 重复",
			wantState: sqlStateUniqueViolation, wantConstr: "policy_snapshots_uniq",
			wantCode: codePolicySnapshotConflict, wantStatus: 409,
			run: func(tx *gorm.DB, t *testing.T, newID func() string) error {
				ins := func() error {
					return tx.Exec(`INSERT INTO policy_snapshots (id, client_id, version, payload, payload_hash, integrity_tag)
						VALUES (?, ?, 1, '{}'::jsonb, '\x01'::bytea, '\x02'::bytea)`,
						newID(), f.clientID).Error
				}
				if err := ins(); err != nil {
					t.Fatalf("首次快照必须成功: %v", err)
				}
				return ins()
			},
		},
		{
			id: "CH-7⑤", trigger: "同一 user×client 第二条 active 流",
			wantState: sqlStateUniqueViolation, wantConstr: "flows_one_active_per_user_client_uniq",
			wantCode: codeCooldownActive, wantStatus: 409,
			run: func(tx *gorm.DB, t *testing.T, _ func() string) error {
				if err := insertFlow(tx, t); err != nil {
					t.Fatalf("首次开流必须成功: %v", err)
				}
				return insertFlow(tx, t)
			},
		},
		{
			id: "CH-7⑥", trigger: "clients 主键重复",
			wantState: sqlStateUniqueViolation, wantConstr: "clients_pkey",
			wantCode: codeInvalidClient, wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T, _ func() string) error {
				return tx.Exec(`INSERT INTO clients (client_id, name) VALUES (?, 'dup')`, f.clientID).Error
			},
		},
		{
			id: "CH-7⑦", trigger: "signing_keys.kid 重复",
			wantState: sqlStateUniqueViolation, wantConstr: "signing_keys_kid_uniq",
			wantCode: codeInvalidParameter, wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T, newID func() string) error {
				kid := "kid-" + uuid.NewString()[:12]
				ins := func() error {
					return tx.Exec(`INSERT INTO signing_keys (id, kid, purpose, algorithm, public_key, private_key_enc)
						VALUES (?, ?, 'oidc', 'SM3withSM2', 'pub', '\x01'::bytea)`, newID(), kid).Error
				}
				if err := ins(); err != nil {
					t.Fatalf("首次插入密钥必须成功: %v", err)
				}
				return ins()
			},
		},
		{
			id: "CH-7⑧a", trigger: "login_policies 每档位第二份 tier_default",
			wantState: sqlStateUniqueViolation, wantConstr: "login_policies_tier_default_uidx",
			wantCode: codeInvalidParameter, wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T, newID func() string) error {
				// D13（000017）已把 L1 档位默认 seed 进库；事务内先删掉再走"首个成功、第二个冲突"，
				// 回滚即复原（只删 tier_default/L1，不动 client_override 行）。
				if err := tx.Exec(`DELETE FROM login_policies WHERE scope = 'tier_default' AND tier = 'L1'`).Error; err != nil {
					t.Fatalf("清掉 L1 档位默认行必须成功: %v", err)
				}
				ins := func() error {
					return tx.Exec(`INSERT INTO login_policies (id, scope, tier, min_acr)
						VALUES (?, 'tier_default', 'L1', 'AAL1')`, newID()).Error
				}
				if err := ins(); err != nil {
					t.Fatalf("首次插入档位默认策略必须成功: %v", err)
				}
				return ins()
			},
		},
		{
			id: "CH-7⑧b", trigger: "login_policies 每 client 第二份 client_override",
			wantState: sqlStateUniqueViolation, wantConstr: "login_policies_client_override_uidx",
			wantCode: codeInvalidParameter, wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T, newID func() string) error {
				ins := func(acr string) error {
					return tx.Exec(`INSERT INTO login_policies (id, scope, client_id, min_acr)
						VALUES (?, 'client_override', ?, ?)`, newID(), f.clientID, acr).Error
				}
				if err := ins("AAL1"); err != nil {
					t.Fatalf("首次插入覆盖策略必须成功: %v", err)
				}
				return ins("AAL2")
			},
		},

		// ===== 非唯一约束类：每个对外有含义的 SQLSTATE 至少一条 =====
		{
			id: "R-23503", trigger: "flows.user_id 指向不存在的用户",
			wantState: sqlStateForeignKeyViolation, wantConstr: "flows_user_id_fkey",
			wantCode: codeUserNotFound, wantStatus: 404,
			run: func(tx *gorm.DB, t *testing.T, _ func() string) error {
				return tx.Exec(`INSERT INTO flows (id, user_id, client_id, target_acr, achieved_amr,
						completed_steps, status, expires_at)
					VALUES (?, '00000000-0000-0000-0000-000000000001', ?, 'AAL1', '{}'::text[], '[]'::jsonb,
						'active', now() + interval '10 min')`, mustV7(t), f.clientID).Error
			},
		},
		{
			id: "R-23502", trigger: "role_bindings.role 传 NULL",
			wantState: sqlStateNotNullViolation, wantConstr: "",
			wantCode: codeMissingParameter, wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T, _ func() string) error {
				return tx.Exec(`INSERT INTO role_bindings (id, user_id, client_id, role) VALUES (?, ?, ?, NULL)`,
					mustV7(t), f.userID, f.clientID).Error
			},
		},
		{
			id: "R-23514", trigger: "sessions.acr 超出 D12 取值域",
			wantState: sqlStateCheckViolation, wantConstr: "sessions_acr_check",
			wantCode: codeInvalidParameter, wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T, _ func() string) error {
				return tx.Exec(`INSERT INTO sessions (id, user_id, client_id, acr, expires_at, idle_expires_at)
					VALUES (?, ?, ?, 'AAL9', now() + interval '1 hour', now() + interval '30 min')`,
					mustV7(t), f.userID, f.clientID).Error
			},
		},
		{
			// 本行期望值就是"未映射"：无分区是**部署缺陷**（分区预建窗口不够），不是用户入参问题。
			id: "R-23514-NOPART", trigger: "audit_logs 写入没有分区的月份（2099-01）",
			wantState: sqlStateCheckViolation, wantConstr: "",
			wantCode: "", wantStatus: 0,
			run: func(tx *gorm.DB, t *testing.T, _ func() string) error {
				return tx.Exec(`INSERT INTO audit_logs (id, action, result, occurred_at)
					VALUES (?, 'ac-eng-04.matrix', 'success', '2099-01-15T00:00:00Z')`, mustV7(t)).Error
			},
		},
		{
			id: "R-23001", trigger: "UPDATE 只增表 auth_events（forbid_mutation 触发器）",
			wantState: sqlStateRestrictViolation, wantConstr: "",
			wantCode: "", wantStatus: 0, // 篡改只增表 = 缺陷 → fail-closed
			run: func(tx *gorm.DB, t *testing.T, _ func() string) error {
				// 先插一行再改它：空表上 `UPDATE ... WHERE id=<随机>` 命中 0 行，行级触发器不会触发
				// （第一次写成这样，用例当场红"期望 PG 报错，实际 nil"——这条注释是给下次的自己看的）。
				id := mustV7(t)
				if err := tx.Exec(`INSERT INTO auth_events (id, event_type, result) VALUES (?, 'matrix', 'success')`,
					id).Error; err != nil {
					t.Fatalf("写入只增表必须成功: %v", err)
				}
				return tx.Exec(`UPDATE auth_events SET result = 'failure' WHERE id = ?`, id).Error
			},
		},
		{
			id: "R-22P02", trigger: "把 'not-a-uuid' 当 uuid 传参",
			wantState: sqlStateInvalidTextRepr, wantConstr: "",
			wantCode: codeInvalidParameter, wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T, _ func() string) error {
				return tx.Exec(`SELECT ?::uuid`, "not-a-uuid").Error
			},
		},
		{
			id: "R-22003", trigger: "把 99999999999 当 int4",
			wantState: sqlStateNumericOutOfRange, wantConstr: "",
			wantCode: codeInvalidParameter, wantStatus: 400,
			run: func(tx *gorm.DB, t *testing.T, _ func() string) error {
				return tx.Exec(`SELECT 99999999999::int4`).Error
			},
		},
		{
			id: "R-42P01", trigger: "查询不存在的表（迁移没跑）",
			wantState: sqlStateUndefinedTable, wantConstr: "",
			wantCode: "", wantStatus: 0, // schema 漂移 = 缺陷 → fail-closed
			run: func(tx *gorm.DB, t *testing.T, _ func() string) error {
				var n int
				return tx.Raw(`SELECT count(*) FROM ac_eng_04_no_such_table`).Scan(&n).Error
			},
		},
		{
			id: "R-42703", trigger: "查询不存在的列（模型与迁移不一致）",
			wantState: sqlStateUndefinedColumn, wantConstr: "",
			wantCode: "", wantStatus: 0, // schema 漂移 = 缺陷 → fail-closed
			run: func(tx *gorm.DB, t *testing.T, _ func() string) error {
				var n int
				return tx.Raw(`SELECT count(ac_eng_04_no_such_column) FROM users`).Scan(&n).Error
			},
		},
	}

	for _, row := range rows {
		row := row
		t.Run(row.id+" "+row.trigger, func(t *testing.T) {
			var raw error
			// 事务内触发、必然回滚：断言完什么都不留（也不污染并行套件）。
			_ = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				raw = row.run(tx, t, func() string { return newID(t) })
				return errMatrixRollback
			})
			got := assertRow(t, raw, row.wantState, row.wantConstr, row.wantCode, row.wantStatus)
			logRow(t, row.id, row.trigger, got.SQLState, got.Constraint, got, got.Mapped(), row.retryable)
		})
	}

	// ===== 并发冲突（40001 / 40P01）：必须真撞出来，且"可重试但不算依赖不可用" =====
	t.Run("R-40001 可重复读并发更新 → 40001 可重试", func(t *testing.T) {
		sqlDB := mustSQLDB(t, db)
		u := mustV7(t)
		if err := db.Exec(`INSERT INTO users (id, username, display_name) VALUES (?, ?, 'v0')`,
			u, "ser-"+uuid.NewString()[:8]).Error; err != nil {
			t.Fatalf("insert user: %v", err)
		}
		t.Cleanup(func() { _ = db.Exec(`DELETE FROM users WHERE id = ?`, u).Error })

		txA, err := sqlDB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
		if err != nil {
			t.Fatalf("begin A: %v", err)
		}
		defer func() { _ = txA.Rollback() }()
		txB, err := sqlDB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
		if err != nil {
			t.Fatalf("begin B: %v", err)
		}
		defer func() { _ = txB.Rollback() }()

		var s string
		if err := txA.QueryRowContext(ctx, `SELECT display_name FROM users WHERE id = $1`, u).Scan(&s); err != nil {
			t.Fatalf("A read: %v", err)
		}
		if err := txB.QueryRowContext(ctx, `SELECT display_name FROM users WHERE id = $1`, u).Scan(&s); err != nil {
			t.Fatalf("B read: %v", err)
		}
		if _, err := txA.ExecContext(ctx, `UPDATE users SET display_name = 'a1' WHERE id = $1`, u); err != nil {
			t.Fatalf("A update: %v", err)
		}
		if err := txA.Commit(); err != nil {
			t.Fatalf("A commit: %v", err)
		}
		// B 的快照还是旧版本 → 更新必然 40001（could not serialize access due to concurrent update）。
		_, raw := txB.ExecContext(ctx, `UPDATE users SET display_name = 'b1' WHERE id = $1`, u)
		got := assertRow(t, raw, sqlStateSerializationFail, "", "", 0)
		if !Retryable(MapError(raw)) {
			t.Fatal("40001 必须被标记为可重试")
		}
		if IsUnavailable(raw) {
			t.Fatal("40001 是并发冲突，不能算依赖不可用（否则客户端会无脑退避）")
		}
		logRow(t, "R-40001", "REPEATABLE READ 并发更新同一行", got.SQLState, got.Constraint, got, got.Mapped(), true)
	})

	t.Run("R-40P01 反向加锁 → 死锁被检出且可重试", func(t *testing.T) {
		sqlDB := mustSQLDB(t, db)
		ids := []uuid.UUID{mustV7(t), mustV7(t)}
		for _, id := range ids {
			if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
				id, "dl-"+uuid.NewString()[:8]).Error; err != nil {
				t.Fatalf("insert user: %v", err)
			}
		}
		t.Cleanup(func() { _ = db.Exec(`DELETE FROM users WHERE id IN (?, ?)`, ids[0], ids[1]).Error })

		tx1, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin 1: %v", err)
		}
		defer func() { _ = tx1.Rollback() }()
		tx2, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin 2: %v", err)
		}
		defer func() { _ = tx2.Rollback() }()

		var n int
		if err := tx1.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, ids[0]).Scan(&n); err != nil {
			t.Fatalf("tx1 lock 0: %v", err)
		}
		if err := tx2.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, ids[1]).Scan(&n); err != nil {
			t.Fatalf("tx2 lock 1: %v", err)
		}

		// 交叉申请 → PG 在 deadlock_timeout（默认 1s）后选一个牺牲者报 40P01。
		errs := make(chan error, 2)
		req := func(tx *sql.Tx, id uuid.UUID) {
			var x int
			_, err := tx.ExecContext(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, id)
			if err == nil {
				err = tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&x)
			}
			errs <- err
		}
		go req(tx1, ids[1])
		go req(tx2, ids[0])

		var deadlock, other error
		for i := 0; i < 2; i++ {
			if err := <-errs; err != nil {
				if isSQLState(err, sqlStateDeadlockDetected) {
					deadlock = err
				} else if other == nil {
					other = err
				}
			}
		}
		if deadlock == nil {
			t.Fatalf("期望一条事务被 PG 判死锁（40P01），实际 deadlock=nil other=%v", other)
		}
		got := assertRow(t, deadlock, sqlStateDeadlockDetected, "", "", 0)
		if !Retryable(MapError(deadlock)) {
			t.Fatal("40P01 必须被标记为可重试")
		}
		if IsUnavailable(deadlock) {
			t.Fatal("40P01 是并发冲突，不能算依赖不可用")
		}
		logRow(t, "R-40P01", "两条事务反向 FOR UPDATE", got.SQLState, got.Constraint, got, got.Mapped(), true)
	})

	// ===== 连接/会话层：让**真 PG** 终止本连接（57P01，不是 mock）=====
	t.Run("R-57P01 服务端终止连接 → DB_UNAVAILABLE 503", func(t *testing.T) {
		sqlDB := mustSQLDB(t, db)
		pinned, err := sqlDB.Conn(ctx)
		if err != nil {
			t.Fatalf("pin conn: %v", err)
		}
		defer func() { _ = pinned.Close() }()
		var pid int
		if err := pinned.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatalf("pid: %v", err)
		}
		// 从另一条连接把它踢掉：服务端先发 FATAL 57P01（terminating connection due to
		// administrator command）再关连接——运维 `pg_ctl stop` / 容器停库时客户端看到的就是它。
		if err := db.Exec(`SELECT pg_terminate_backend(?)`, pid).Error; err != nil {
			t.Fatalf("terminate: %v", err)
		}
		var raw error
		for i := 0; i < 20 && raw == nil; i++ {
			var one int
			if err := pinned.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
				raw = err
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if raw == nil {
			t.Fatal("连接被 pg_terminate_backend 踢掉后查询仍然成功，期望连接层错误")
		}
		mapped := MapError(raw)
		var got *Error
		if !errors.As(mapped, &got) {
			t.Fatalf("MapError 返回 %T，期望 *store.Error（原始错误 %T: %v）", mapped, raw, raw)
		}
		if got.Code != codeDBUnavailable || got.Status != 503 {
			t.Fatalf("被服务端终止必须映射成 %s/503，实际 %s/%d（原始错误 %T: %v）",
				codeDBUnavailable, got.Code, got.Status, raw, raw)
		}
		logRow(t, "R-57P01", "服务端 pg_terminate_backend 踢掉本连接",
			firstNonEmpty(got.SQLState, rawSQLState(raw)), got.Constraint, got, got.Mapped(), false)
	})
}

// TestAC_ENG_04_MatrixCoversEveryUniqueRule 钉住"矩阵与生产映射表同步"：
// `uniqueRules` 里出现的每个约束名，必须在本文件的矩阵里各有一行（少一行即红）。
// 这样"CH-7 八行"不会因为矩阵漏写而变成假证据。
func TestAC_ENG_04_MatrixCoversEveryUniqueRule(t *testing.T) {
	covered := map[string]bool{
		// 与 TestAC_ENG_04_RealSQLStateMatrix 的 CH-7 行**一一对应**（新增行必须两边都加）。
		"identifiers_type_value_uniq":           true,
		"users_username_lower_uidx":             true,
		"role_bindings_uniq":                    true,
		"policy_snapshots_uniq":                 true,
		"flows_one_active_per_user_client_uniq": true,
		"clients_pkey":                          true,
		"signing_keys_kid_uniq":                 true,
		"login_policies_tier_default_uidx":      true,
		"login_policies_client_override_uidx":   true,
	}
	for name := range uniqueRules {
		if !covered[name] {
			t.Errorf("uniqueRules 里的 %q 在真库矩阵里没有对应行：补一行，否则这条映射没有逐行证据", name)
		}
	}
	for name := range covered {
		if _, ok := uniqueRules[name]; !ok {
			t.Errorf("矩阵覆盖了 %q，但 uniqueRules 里没有它（矩阵与映射表漂移）", name)
		}
	}
}

var errMatrixRollback = errors.New("matrix: rollback on purpose")

func mustSQLDB(t *testing.T, db *gorm.DB) *sql.DB {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB(): %v", err)
	}
	return sqlDB
}

func isSQLState(err error, state string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == state
}

func rawSQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
