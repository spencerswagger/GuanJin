//go:build integration

package identity

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/identifier"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 真库用例（**必须有真 PostgreSQL**）：
//
//	GUANJIN_TEST_DSN=... go test -tags=integration ./internal/identity/
//
// 不打 `t.Skip`：缺 DSN 就红（CI 已机器强制「skip>0 即红 / 无用例即红」）。
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := osGetenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		t.Fatalf("必须设置 GUANJIN_TEST_DSN（integration job 已注入）；不设就是不验证，按红处理")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{DSN: dsn})
	if err != nil {
		t.Fatalf("打开测试库失败：%v", err)
	}
	if err := store.Ping(ctx, db); err != nil {
		t.Fatalf("ping 测试库失败：%v", err)
	}
	if !db.Migrator().HasTable("identifiers") {
		t.Fatalf("测试库缺 identifiers 表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN")
	}
	t.Cleanup(func() { _ = store.Close(db) })
	return db
}

// fixture 建一个隔离的业务系统 + 一份**客户端覆盖策略**。
//
// 为什么要显式造 login_policies 行：`tier_default` 目前**没有 seed**（迁移里只 seed 了
// `flow.ttl_seconds`），而本服务按基线拒绝"代码里垫默认值"（`ErrPolicyNotConfigured`）。
// 用 client_override 既证明"覆盖优先"，也不影响其他测试。
type fixture struct {
	clientID string
	userIDs  []uuid.UUID
}

func newFixture(t *testing.T, db *gorm.DB, selfRegistration bool) *fixture {
	t.Helper()
	suffix := uuid.NewString()
	f := &fixture{clientID: "s4b-" + suffix[:20]}
	if err := db.Exec(`INSERT INTO clients (client_id, name, tier) VALUES (?, ?, 'L2')`,
		f.clientID, "S4b test client").Error; err != nil {
		t.Fatalf("insert client: %v", err)
	}
	policyID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	if err := db.Exec(`INSERT INTO login_policies
		(id, scope, client_id, min_acr, password_min_length, password_char_classes,
		 password_history_count, password_max_age_days, self_registration_allowed)
		VALUES (?, 'client_override', ?, 'AAL1', 12, 3, 5, 90, ?)`,
		policyID, f.clientID, selfRegistration).Error; err != nil {
		t.Fatalf("insert login_policies: %v", err)
	}
	t.Cleanup(func() {
		// 顺序：先删引用方（FK RESTRICT）。
		_ = db.Exec(`DELETE FROM auth_events WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM challenges WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM flows WHERE client_id = ?`, f.clientID).Error
		for _, uid := range f.userIDs {
			_ = db.Exec(`DELETE FROM authenticators WHERE user_id = ?`, uid).Error
			_ = db.Exec(`DELETE FROM identifiers WHERE user_id = ?`, uid).Error
			_ = db.Exec(`DELETE FROM users WHERE id = ?`, uid).Error
		}
		_ = db.Exec(`DELETE FROM login_policies WHERE client_id = ?`, f.clientID).Error
		_ = db.Exec(`DELETE FROM clients WHERE client_id = ?`, f.clientID).Error
	})
	return f
}

func (f *fixture) newService(t *testing.T, db *gorm.DB) *Service {
	t.Helper()
	svc, err := New(db, testConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

func (f *fixture) register(t *testing.T, svc *Service, value, password string) (*RegisterResult, error) {
	t.Helper()
	res, err := svc.Register(context.Background(), RegisterRequest{
		ClientID:     f.clientID,
		Type:         identifier.TypeEmail,
		Value:        value,
		Password:     password,
		Verified:     true,
		VerifyMethod: "email_otp",
		TrustLevel:   2,
		TraceID:      "trace-" + uuid.NewString()[:8],
	})
	if res != nil && res.UserID != uuid.Nil {
		f.userIDs = append(f.userIDs, res.UserID)
	}
	return res, err
}

// AC-REG-06：注册成功后**一整套事实**都要落库（users / identifiers / authenticators /
// flows(completed+consumed) / auth_events）。
//
// 这条是"注册即登录"的数据侧证明：令牌由 S6 签发，但"账号已可登录"必须在这里成立。
func TestAC_REG_06_RegisterCreatesRows(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := f.newService(t, db)

	email := "s4b-" + uuid.NewString()[:12] + "@example.com"
	const password = "GuanJin#S4b-Strong1"
	res, err := f.register(t, svc, email, password)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if res.TargetACR != string(factor.ACRAAL1) {
		t.Fatalf("L2 档位的 target_acr 应为 AAL1，实际 %s", res.TargetACR)
	}
	if !strings.Contains(res.IdentifierMasked, "***") {
		t.Fatalf("返回的标识符必须脱敏，实际 %q", res.IdentifierMasked)
	}

	ctx := context.Background()

	// ① users：username = 规范化标识符（显式约定，见 createUser 注释）。
	var username string
	if err := db.WithContext(ctx).Raw(`SELECT username FROM users WHERE id = ?`, res.UserID).Scan(&username).Error; err != nil {
		t.Fatalf("读 users: %v", err)
	}
	if username != email {
		t.Fatalf("users.username 应等于规范化标识符 %q，实际 %q", email, username)
	}

	// ② identifiers：verified=true、trust_level=2、is_primary=true、落的是密文+盲索引。
	var idn struct {
		Verified    bool   `gorm:"column:verified"`
		TrustLevel  int16  `gorm:"column:trust_level"`
		IsPrimary   bool   `gorm:"column:is_primary"`
		ValueMasked string `gorm:"column:value_masked"`
		Len         int    `gorm:"column:len"`
	}
	if err := db.WithContext(ctx).Raw(
		`SELECT verified, trust_level, is_primary, value_masked, length(value_enc) AS len
		   FROM identifiers WHERE id = ?`, res.IdentifierID).Scan(&idn).Error; err != nil {
		t.Fatalf("读 identifiers: %v", err)
	}
	if !idn.Verified || idn.TrustLevel != 2 || !idn.IsPrimary {
		t.Fatalf("标识符状态不对：%+v", idn)
	}
	if idn.Len == 0 {
		t.Fatal("value_enc 不能为空（必须 SM4 密文落库）")
	}
	if strings.Contains(idn.ValueMasked, "@") && !strings.Contains(idn.ValueMasked, "***") {
		t.Fatalf("value_masked 未脱敏：%q", idn.ValueMasked)
	}

	// ③ authenticators：password 信封能解回来并校验通过（证明写入的不是坏数据）。
	var rawJSON string
	if err := db.WithContext(ctx).Raw(
		`SELECT credential_data FROM authenticators WHERE id = ?`, res.AuthenticatorID).Scan(&rawJSON).Error; err != nil {
		t.Fatalf("读 authenticators: %v", err)
	}
	raw := []byte(rawJSON)
	env, err := credential.ParsePasswordEnvelope(raw)
	if err != nil {
		t.Fatalf("落库的口令信封必须合法：%v", err)
	}
	if ok, err := env.Verify(password); err != nil || !ok {
		t.Fatalf("落库后应能用注册口令校验：ok=%v err=%v", ok, err)
	}
	if strings.Contains(string(raw), password) {
		t.Fatal("credential_data 里不得出现口令明文")
	}

	// ④ flows：completed + consumed_at 非空（I-F4）。
	var fl struct {
		Status     string     `gorm:"column:status"`
		ConsumedAt *time.Time `gorm:"column:consumed_at"`
		UserID     *uuid.UUID `gorm:"column:user_id"`
		AMR        string     `gorm:"column:amr"`
	}
	if err := db.WithContext(ctx).Raw(
		`SELECT status, consumed_at, user_id, array_to_string(achieved_amr, ',') AS amr
		   FROM flows WHERE id = ?`, res.FlowID).Scan(&fl).Error; err != nil {
		t.Fatalf("读 flows: %v", err)
	}
	if fl.Status != "completed" || fl.ConsumedAt == nil || fl.UserID == nil {
		t.Fatalf("注册流必须是 completed + consumed + 有 user_id：%+v", fl)
	}
	if fl.AMR != "pwd" {
		t.Fatalf("achieved_amr 应为 pwd，实际 %q", fl.AMR)
	}

	// ⑤ auth_events：写了一条 register/success（取证靠它，不靠日志）。
	var n int64
	if err := db.WithContext(ctx).Raw(
		`SELECT count(*) FROM auth_events WHERE user_id = ? AND event_type = 'register' AND result = 'success'`,
		res.UserID).Scan(&n).Error; err != nil {
		t.Fatalf("读 auth_events: %v", err)
	}
	if n != 1 {
		t.Fatalf("应恰好写 1 条 register 事件，实际 %d", n)
	}
}

// AC-REG-07：口令不合规 → 三表零残留（一次性事务）。
func TestAC_REG_07_PasswordRejectedLeavesNoRows(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := f.newService(t, db)

	email := "s4b-short-" + uuid.NewString()[:10] + "@example.com"
	// "abc123"：内置弱口令 + 太短 + 字符类不足（三码同时命中 → 首个是 WEAK_PASSWORD）。
	_, err := f.register(t, svc, email, "abc123")
	if err == nil {
		t.Fatal("弱口令必须被拒")
	}
	var rejected *PasswordRejected
	if !errors.As(err, &rejected) {
		t.Fatalf("应返回 *PasswordRejected，实际 %T: %v", err, err)
	}
	if code, ok := CodeOf(err); !ok || code != httpx.CodeWeakPassword {
		t.Fatalf("首个码应为 WEAK_PASSWORD（AC-REG-10 顺序），实际 %q", code)
	}
	if len(rejected.Violations()) < 2 {
		t.Fatalf("应带回全量命中项，实际 %d", len(rejected.Violations()))
	}

	ctx := context.Background()
	var users, flows int64
	if err := db.WithContext(ctx).Raw(`SELECT count(*) FROM users WHERE username = ?`, email).Scan(&users).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if err := db.WithContext(ctx).Raw(`SELECT count(*) FROM flows WHERE client_id = ? AND user_id IS NULL`, f.clientID).Scan(&flows).Error; err != nil {
		t.Fatalf("count flows: %v", err)
	}
	if users != 0 || flows != 0 {
		t.Fatalf("失败后必须零残留：users=%d flows=%d", users, flows)
	}
}

// 失败在**写库中途**发生时也必须整体回滚（这里用"标识符已被占用"制造后半程失败）。
func TestRegisterRollsBackWhenIdentifierTaken(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := f.newService(t, db)

	email := "s4b-taken-" + uuid.NewString()[:10] + "@example.com"
	if _, err := f.register(t, svc, email, "GuanJin#S4b-Strong1"); err != nil {
		t.Fatalf("首次注册应成功：%v", err)
	}
	// 第二次用同一标识符（不同口令）→ identifiers_type_value_uniq 撞 23505。
	_, err := f.register(t, svc, email, "GuanJin#S4b-Strong2")
	if err == nil {
		t.Fatal("重复标识符必须被拒")
	}
	if code, ok := CodeOf(err); !ok || code != httpx.CodeIdentifierAlreadyBound {
		t.Fatalf("应映射 IDENTIFIER_ALREADY_BOUND（CH-7①），实际 %q (%v)", code, err)
	}
	// 回滚证据：这个 username 只应有一条 users 行（第二次没留下半截）。
	var n int64
	if err := db.WithContext(context.Background()).Raw(
		`SELECT count(*) FROM users WHERE username = ?`, email).Scan(&n).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 1 {
		t.Fatalf("回滚后应只剩 1 条 users，实际 %d", n)
	}
}

// AC-REG-02 / AC-REG-09：client 上下文的两个负向路径。
func TestAC_REG_02_ClientContextRequired(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := f.newService(t, db)
	ctx := context.Background()

	_, err := svc.Register(ctx, RegisterRequest{
		Type: identifier.TypeEmail, Value: "x@example.com", Password: "GuanJin#S4b-Strong1",
	})
	if code, ok := CodeOf(err); !ok || code != httpx.CodeMissingParameter {
		t.Fatalf("缺 client_id 应 400 MISSING_PARAMETER，实际 %q (%v)", code, err)
	}

	_, err = svc.Register(ctx, RegisterRequest{
		ClientID: "not-exist-" + uuid.NewString()[:8], Type: identifier.TypeEmail,
		Value: "y@example.com", Password: "GuanJin#S4b-Strong1",
	})
	if code, ok := CodeOf(err); !ok || code != httpx.CodeInvalidClient {
		t.Fatalf("未知 client 应 INVALID_CLIENT，实际 %q (%v)", code, err)
	}
}

// AC-REG-09：目标 client 未开放自助注册 → 403 REGISTRATION_NOT_ALLOWED，且不建号。
func TestAC_REG_09_RegistrationNotAllowed(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, false) // self_registration_allowed = false
	svc := f.newService(t, db)

	email := "s4b-closed-" + uuid.NewString()[:10] + "@example.com"
	_, err := f.register(t, svc, email, "GuanJin#S4b-Strong1")
	if !errors.Is(err, ErrRegistrationNotAllowed) {
		t.Fatalf("应报 ErrRegistrationNotAllowed，实际 %v", err)
	}
	code, ok := CodeOf(err)
	if !ok || code != httpx.CodeRegistrationNotAllowed || httpx.HTTPStatus(code) != 403 {
		t.Fatalf("应映射 REGISTRATION_NOT_ALLOWED/403，实际 %q", code)
	}
	var n int64
	if err := db.WithContext(context.Background()).Raw(
		`SELECT count(*) FROM users WHERE username = ?`, email).Scan(&n).Error; err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 0 {
		t.Fatalf("被拒后不得建号，实际 %d 行", n)
	}
}

// AC-REG-03：`challenges.purpose` 六值词表 + 约束名显式（D2）。
// 本条不需要仓储：直接在真库上打两次写入，证明 CHECK 与约束名都对。
func TestAC_REG_03_PurposeVocabulary(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	ctx := context.Background()

	insert := func(purpose string) error {
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatalf("uuid v7: %v", err)
		}
		return store.MapError(db.WithContext(ctx).Exec(
			`INSERT INTO challenges (id, client_id, purpose, factor_type, response_hash, expires_at)
			 VALUES (?, ?, ?, 'email_otp', '\x01'::bytea, now() + interval '10 min')`,
			id, f.clientID, purpose).Error)
	}

	// 六个合法值全都能落库（含 register）。
	for _, p := range []string{"login", "register", "step_up", "enroll", "recover", "verify_identifier"} {
		if err := insert(p); err != nil {
			t.Fatalf("purpose=%q 应能落库：%v", p, err)
		}
	}
	// 作废值必须 23514，并且映射到 INVALID_PARAMETER（约束名显式 challenges_purpose_valid）。
	for _, dead := range []string{"bind", "stepup"} {
		err := insert(dead)
		if err == nil {
			t.Fatalf("purpose=%q 是作废值，必须被 CHECK 拒绝", dead)
		}
		var dbErr *store.Error
		if !errors.As(err, &dbErr) {
			t.Fatalf("应是 *store.Error，实际 %T", err)
		}
		if dbErr.SQLState != "23514" || dbErr.Constraint != "challenges_purpose_valid" {
			t.Fatalf("约束名/状态不对：sqlstate=%s constraint=%s", dbErr.SQLState, dbErr.Constraint)
		}
		if dbErr.Code != httpx.CodeInvalidParam {
			t.Fatalf("应映射 INVALID_PARAMETER，实际 %s", dbErr.Code)
		}
	}
}

// 标识符不变式 I1/I2/I3/I6 的**真库**用例（AC-ENG-04 点名的缺口）。
func TestDB_IdentifierInvariants(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	setup := func(t *testing.T, selfReg bool) (*fixture, *Service) {
		f := newFixture(t, db, selfReg)
		return f, f.newService(t, db)
	}

	t.Run("I1 最后一个已验证标识符不可解绑", func(t *testing.T) {
		f, svc := setup(t, true)
		res, err := f.register(t, svc, "i1-"+uuid.NewString()[:10]+"@example.com", "GuanJin#S4b-Strong1")
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		// 只有一条（已验证）：先撞 I2 —— 两个不变式对外同码（LAST_IDENTIFIER_CANNOT_UNBIND），
		// 但内部错误要能分开，否则排障时不知道"是数量问题还是验证状态问题"。
		if err := svc.UnbindIdentifier(ctx, res.UserID, res.IdentifierID); !errors.Is(err, identifier.ErrLastIdentifier) {
			t.Fatalf("只剩一条时应报 I2（总量），实际 %v", err)
		}
		// 再绑一条**未验证**的 → 总量 2、已验证 1：解绑那条已验证的 → 这时才是 I1。
		if _, err := svc.BindIdentifier(ctx, BindRequest{
			UserID: res.UserID, Type: identifier.TypeEmail,
			Value: "i1b-" + uuid.NewString()[:10] + "@example.com",
		}); err != nil {
			t.Fatalf("BindIdentifier: %v", err)
		}
		if err := svc.UnbindIdentifier(ctx, res.UserID, res.IdentifierID); !errors.Is(err, identifier.ErrLastVerifiedIdentifier) {
			t.Fatalf("解绑最后一条已验证标识符应报 I1，实际 %v", err)
		}
	})

	t.Run("I1/I2 的边界：两条都验证过则允许解绑其一", func(t *testing.T) {
		f, svc := setup(t, true)
		res, err := f.register(t, svc, "i1c-"+uuid.NewString()[:10]+"@example.com", "GuanJin#S4b-Strong1")
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		second, err := svc.BindIdentifier(ctx, BindRequest{
			UserID: res.UserID, Type: identifier.TypeEmail,
			Value:    "i1d-" + uuid.NewString()[:10] + "@example.com",
			Verified: true, VerifyMethod: "email_otp", TrustLevel: 2,
		})
		if err != nil {
			t.Fatalf("BindIdentifier: %v", err)
		}
		if err := svc.UnbindIdentifier(ctx, res.UserID, res.IdentifierID); err != nil {
			t.Fatalf("两个已验证标识符时应允许解绑：%v", err)
		}
		// 解绑是"置 disabled"，不是物理删（唯一索引与审计都要留痕）。
		var status string
		if err := db.WithContext(ctx).Raw(`SELECT status FROM identifiers WHERE id = ?`, res.IdentifierID).Scan(&status).Error; err != nil {
			t.Fatalf("读 status: %v", err)
		}
		if status != "disabled" {
			t.Fatalf("解绑后状态应为 disabled，实际 %q", status)
		}
		if second.IsPrimary {
			t.Fatal("第二条标识符不应自动成为主标识符（每用户至多一个）")
		}
	})

	t.Run("I2 最后一个标识符不可解绑", func(t *testing.T) {
		f, svc := setup(t, true)
		// 只有**未验证**标识符的账号才可能真的把"可用名字"降到 0（已验证的会被 I1 拦住）。
		res, err := svc.Register(ctx, RegisterRequest{
			ClientID: f.clientID, Type: identifier.TypeEmail,
			Value:    "i2-" + uuid.NewString()[:10] + "@example.com",
			Password: "GuanJin#S4b-Strong1", Verified: false,
		})
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		f.userIDs = append(f.userIDs, res.UserID)
		if err := svc.UnbindIdentifier(ctx, res.UserID, res.IdentifierID); !errors.Is(err, identifier.ErrLastIdentifier) {
			t.Fatalf("总量为 1 时应被 I2 拦住，实际 %v", err)
		}

		// 再绑一条（未验证）→ 两条就允许解绑第一条；解到只剩一条时再次被 I2 拦住。
		second, err := svc.BindIdentifier(ctx, BindRequest{
			UserID: res.UserID, Type: identifier.TypeEmail,
			Value: "i2b-" + uuid.NewString()[:10] + "@example.com",
		})
		if err != nil {
			t.Fatalf("BindIdentifier: %v", err)
		}
		if err := svc.UnbindIdentifier(ctx, res.UserID, res.IdentifierID); err != nil {
			t.Fatalf("两条时应允许解绑：%v", err)
		}
		if err := svc.UnbindIdentifier(ctx, res.UserID, second.ID); !errors.Is(err, identifier.ErrLastIdentifier) {
			t.Fatalf("解到只剩一条时应再次被 I2 拦住，实际 %v", err)
		}
	})

	t.Run("I3/I4 最后一个可用认证能力不可移除", func(t *testing.T) {
		f, svc := setup(t, true)
		res, err := f.register(t, svc, "i3-"+uuid.NewString()[:10]+"@example.com", "GuanJin#S4b-Strong1")
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		// 只有口令认证器 + 已验证邮箱（信道能力）→ 允许撤销口令（并集口径）。
		if err := svc.RevokeAuthenticator(ctx, res.UserID, res.AuthenticatorID); err != nil {
			t.Fatalf("有已验证邮箱时应允许撤销口令：%v", err)
		}
		var status string
		if err := db.WithContext(ctx).Raw(`SELECT status FROM authenticators WHERE id = ?`, res.AuthenticatorID).Scan(&status).Error; err != nil {
			t.Fatalf("读 status: %v", err)
		}
		if status != "revoked" {
			t.Fatalf("撤销后状态应为 revoked，实际 %q", status)
		}

		// 反向：把标识符也停用，只留一个口令认证器 → 撤销必须被 I3 拦住。
		f2, svc2 := setup(t, true)
		res2, err := f2.register(t, svc2, "i3b-"+uuid.NewString()[:10]+"@example.com", "GuanJin#S4b-Strong1")
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		if err := db.WithContext(ctx).Exec(
			`UPDATE identifiers SET status = 'disabled' WHERE user_id = ?`, res2.UserID).Error; err != nil {
			t.Fatalf("停用标识符: %v", err)
		}
		if err := svc2.RevokeAuthenticator(ctx, res2.UserID, res2.AuthenticatorID); !errors.Is(err, identifier.ErrLastAuthenticator) {
			t.Fatalf("没有其他可用能力时应被 I3 拦住，实际 %v", err)
		}
	})

	t.Run("I6 最后一个主标识符不可降级（规则 + 读数）", func(t *testing.T) {
		f, svc := setup(t, true)
		res, err := f.register(t, svc, "i6-"+uuid.NewString()[:10]+"@example.com", "GuanJin#S4b-Strong1")
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		snap, err := svc.Snapshot(ctx, nil, res.UserID)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snap.Primary != 1 {
			t.Fatalf("注册后应恰好 1 个主标识符，实际 %d", snap.Primary)
		}
		if err := identifier.CheckDemotePrimary(snap); !errors.Is(err, identifier.ErrLastPrimary) {
			t.Fatalf("期望 ErrLastPrimary（且该错误**无对外码**，D3-B），实际 %v", err)
		}
		if _, ok := identifier.CodeOf(identifier.ErrLastPrimary); ok {
			t.Fatal("I6 不得有对外错误码")
		}
		// 绑定第二条标识符不会让它变成主标识符（I6 的一次正面断言）。
		if _, err := svc.BindIdentifier(ctx, BindRequest{
			UserID: res.UserID, Type: identifier.TypeEmail,
			Value: "i6b-" + uuid.NewString()[:10] + "@example.com",
		}); err != nil {
			t.Fatalf("BindIdentifier: %v", err)
		}
		snap, err = svc.Snapshot(ctx, nil, res.UserID)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snap.Primary != 1 || snap.Total != 2 {
			t.Fatalf("应仍是 1 主 / 2 总，实际 %+v", snap)
		}
	})
}

// 用户名的唯一冲突同样要走 CH-7②（`users_username_lower_uidx` → IDENTIFIER_ALREADY_BOUND）。
//
// 注意：username 型注册**不建 identifiers 行**（索引 §4.2-1），所以这条冲突只可能来自
// users 的唯一索引——正好用来证明 CH-7② 的映射接在这条路径上。
func TestRegisterUsernameCollisionMapsToIdentifierBound(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := f.newService(t, db)
	ctx := context.Background()

	name := "s4buser" + uuid.NewString()[:8]
	req := RegisterRequest{
		ClientID: f.clientID, Type: identifier.TypeUsername, Value: name,
		Password: "GuanJin#S4b-Strong1", Verified: true,
	}
	res, err := svc.Register(ctx, req)
	if err != nil {
		t.Fatalf("首次注册: %v", err)
	}
	f.userIDs = append(f.userIDs, res.UserID)
	if res.IdentifierID != uuid.Nil {
		t.Fatalf("username 型不应建 identifiers 行，实际 %s", res.IdentifierID)
	}

	// 大小写不同也必须冲突（唯一索引建在 lower(username) 上）。
	req.Value = strings.ToUpper(name)
	_, err = svc.Register(ctx, req)
	if code, ok := CodeOf(err); !ok || code != httpx.CodeIdentifierAlreadyBound {
		t.Fatalf("用户名冲突应映射 IDENTIFIER_ALREADY_BOUND，实际 %q (%v)", code, err)
	}
}

// osGetenv 单独包一层：将来换配置源时只改这里（测试与 CI job 现在都用环境变量）。
func osGetenv(key string) string { return os.Getenv(key) }
