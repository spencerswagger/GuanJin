//go:build integration

package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 注册面（`register/start` + `register/complete`）的**真库**用例。
//
// 覆盖的判据：AC-REG-01（挑战行 + `attempts_left`/TTL 来自 runtime_config，改值必须跟着变）、
// AC-REG-02（缺 `client_id`）、AC-REG-05（用户名免验证跳过挑战）、AC-REG-08（邀请码无效分支）、
// AC-REG-09（准入关闭）、AC-REG-10（口令三码由 `credential` 保证，这里只验端到端形态）。
//
// **不覆盖**（照实说）：AC-REG-04 的"耗时差异阈值"（需要统计口径，归 Gate 的 e2e）、
// AC-REG-06 的三件令牌（S6 协议面）、AC-REG-08 的"已使用/过期"两分支（M1 没有邀请码存储）。

// newRegisterService 用给定挑战参数装一个服务（模拟"运维改了 runtime_config 两个键"）。
func newRegisterService(t *testing.T, db *gorm.DB, ttl time.Duration, attempts int) *Service {
	t.Helper()
	cfg := testConfig(t)
	cfg.ChallengeTTL = ttl
	cfg.ChallengeMaxAttempts = attempts
	svc, err := New(db, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

// AC-REG-01：挑战行落库，且 `attempts_left` / TTL **跟着 runtime_config 的取值变**。
func TestAC_REG_01_StartCreatesRegisterChallengeFromConfig(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)

	first := newRegisterService(t, db, 5*time.Minute, 5)
	email := "s4c-" + uuid.NewString()[:12] + "@example.com"
	res, err := first.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: email, TraceID: "tr-1",
	})
	if err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	if res.ChallengeID == nil || *res.ChallengeID == "" {
		t.Fatal("email 型必须下发挑战")
	}
	if res.NextAction != factor.NextActionTypeCode {
		t.Fatalf("next_action 应为 type_code，实际 %s", res.NextAction)
	}
	if res.AttemptsLeft != 5 {
		t.Fatalf("attempts_left 应等于配置的 5，实际 %d", res.AttemptsLeft)
	}
	if d := time.Until(res.ExpiresAt); d < 4*time.Minute || d > 6*time.Minute {
		t.Fatalf("TTL 应约等于配置的 5 分钟，实际剩余 %s", d)
	}

	// 库里的行：purpose=register、factor_type=email_otp、max_attempts=配置值。
	var purpose, factorType string
	var maxAttempts int
	if err := db.Raw(`SELECT purpose, factor_type, max_attempts FROM challenges WHERE id = ?`,
		*res.ChallengeID).Row().Scan(&purpose, &factorType, &maxAttempts); err != nil {
		t.Fatalf("读挑战行：%v", err)
	}
	if purpose != string(factor.PurposeRegister) || factorType != string(factor.FactorEmailOTP) {
		t.Fatalf("挑战行不符：purpose=%s factor=%s", purpose, factorType)
	}
	if maxAttempts != 5 {
		t.Fatalf("max_attempts 应等于配置的 5，实际 %d", maxAttempts)
	}

	// 改配置再跑一次：**值必须跟着变**（证明不是硬编码）。用另一个标识符避免撞唯一性。
	second := newRegisterService(t, db, 90*time.Second, 3)
	email2 := "s4c-" + uuid.NewString()[:12] + "@example.com"
	res2, err := second.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: email2,
	})
	if err != nil {
		t.Fatalf("第二次 StartRegistration: %v", err)
	}
	if res2.AttemptsLeft != 3 {
		t.Fatalf("改键后 attempts_left 应为 3，实际 %d", res2.AttemptsLeft)
	}
	if d := time.Until(res2.ExpiresAt); d < time.Minute || d > 2*time.Minute {
		t.Fatalf("改键后 TTL 应约等于 90s，实际剩余 %s", d)
	}
}

// 手机号：挑战的因子类型/通道必须是 `sms_otp` / `sms`（与邮箱路径分开）。
func TestStartPhoneChallengeUsesSMS(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)

	res, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "phone", Value: fmt.Sprintf("+8613%08d", time.Now().UnixNano()%100000000),
	})
	if err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	if res.ChallengeID == nil {
		t.Fatal("手机号型必须下发挑战")
	}
	var factorType string
	var channel *string
	if err := db.Raw(`SELECT factor_type, channel FROM challenges WHERE id = ?`, *res.ChallengeID).
		Row().Scan(&factorType, &channel); err != nil {
		t.Fatalf("读挑战行：%v", err)
	}
	if factorType != string(factor.FactorSMSOTP) {
		t.Fatalf("factor_type 应为 sms_otp，实际 %s", factorType)
	}
	if channel == nil || *channel != "sms" {
		t.Fatalf("channel 应为 sms，实际 %v", channel)
	}
}

// AC-REG-05：用户名型（"用户名+密码"）跳过挑战，`next_action=input_password`。
func TestAC_REG_05_UsernameSkipsChallenge(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)

	username := "s4c-" + uuid.NewString()[:10]
	res, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "username", Value: username,
	})
	if err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	if res.ChallengeID != nil {
		t.Fatalf("用户名型不该下发挑战，实际 %v", *res.ChallengeID)
	}
	if res.NextAction != factor.NextActionInputPassword {
		t.Fatalf("next_action 应为 input_password，实际 %s", res.NextAction)
	}
	var n int64
	if err := db.Raw(`SELECT count(*) FROM challenges WHERE client_id = ?`, f.clientID).Scan(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("用户名型不该落挑战行，实际 %d 行", n)
	}

	// 令牌可用：complete 直接建号（没有验证码）。
	done, err := svc.CompleteRegistration(context.Background(), CompleteRegistrationRequest{
		RegisterToken: res.RegisterToken,
		Password:      "GuanJin#S4c-Strong1",
		TraceID:       "tr-2",
	})
	if err != nil {
		t.Fatalf("CompleteRegistration: %v", err)
	}
	f.userIDs = append(f.userIDs, done.UserID)
	if done.IdentifierID != uuid.Nil {
		t.Fatalf("username 型不建 identifiers 行（索引 §4.2-1），实际 %s", done.IdentifierID)
	}
}

// AC-REG-02：缺 `client_id` → `MISSING_PARAMETER`，不得按平台默认档位静默放行。
func TestAC_REG_02_StartRequiresClient(t *testing.T) {
	db := openTestDB(t)
	svc := newRegisterService(t, db, 5*time.Minute, 5)
	_, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		Type: "email", Value: "a@example.com",
	})
	if !errors.Is(err, ErrMissingClientID) {
		t.Fatalf("应报 ErrMissingClientID，实际 %v", err)
	}
	if code, ok := CodeOf(err); !ok || code != httpx.CodeMissingParameter {
		t.Fatalf("应映射 MISSING_PARAMETER，实际 %q", code)
	}
}

// AC-REG-08（无效分支）：非空邀请码 → `INVITE_CODE_INVALID`，且**零新增行**。
//
// 已使用 / 过期两个分支当前不可达（M1 没有邀请码存储），照实记为未覆盖。
func TestAC_REG_08_InviteCodeInvalidCreatesNothing(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)

	before := countRows(t, db, f.clientID)
	_, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: "s4c-" + uuid.NewString()[:10] + "@example.com",
		InviteCode: "INVITE-123",
	})
	if !errors.Is(err, ErrInviteCodeInvalid) {
		t.Fatalf("应报 ErrInviteCodeInvalid，实际 %v", err)
	}
	if code, ok := CodeOf(err); !ok || code != httpx.CodeInviteCodeInvalid {
		t.Fatalf("应映射 INVITE_CODE_INVALID，实际 %q", code)
	}
	if after := countRows(t, db, f.clientID); after != before {
		t.Fatalf("邀请码无效时不得新增任何行：before=%d after=%d", before, after)
	}
}

// AC-REG-09：准入关闭 → `REGISTRATION_NOT_ALLOWED`（403），start 与 complete 两条路径各拒一次。
func TestAC_REG_09_RegistrationNotAllowedBothPaths(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, false) // self_registration_allowed = false
	svc := newRegisterService(t, db, 5*time.Minute, 5)

	_, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: "s4c-" + uuid.NewString()[:10] + "@example.com",
	})
	if !errors.Is(err, ErrRegistrationNotAllowed) {
		t.Fatalf("start 应报 ErrRegistrationNotAllowed，实际 %v", err)
	}
	if code, ok := CodeOf(err); !ok || code != httpx.CodeRegistrationNotAllowed {
		t.Fatalf("应映射 REGISTRATION_NOT_ALLOWED，实际 %q", code)
	}
	if status := httpx.HTTPStatus(httpx.CodeRegistrationNotAllowed); status != 403 {
		t.Fatalf("该码必须是 403（§5.4 接口可覆盖），实际 %d", status)
	}

	// complete 路径：拿一个（伪造但签名合法的）令牌也要在准入上被拒 —— 令牌的 cid 指向该 client。
	claims := RegisterClaims{V: 1, ClientID: f.clientID, Type: "username",
		Value: "s4c-" + uuid.NewString()[:8], ExpiresAt: time.Now().Add(time.Minute).Unix()}
	token, err := svc.sign(claims)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := svc.CompleteRegistration(context.Background(), CompleteRegistrationRequest{
		RegisterToken: token, Password: "GuanJin#S4c-Strong1",
	}); !errors.Is(err, ErrRegistrationNotAllowed) {
		t.Fatalf("complete 应报 ErrRegistrationNotAllowed，实际 %v", err)
	}
}

// 标识符格式非法 / 已被占用：start 阶段就拒（不是等到建号）。
func TestStartRejectsBadAndBoundIdentifiers(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)

	if _, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: "not-an-email",
	}); err == nil {
		t.Fatal("非法邮箱必须报错")
	}

	// 先注册一个号，再对同一标识符 start → 已占用。
	email := "s4c-" + uuid.NewString()[:12] + "@example.com"
	start, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: email,
	})
	if err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	res, err := svc.CompleteRegistration(context.Background(), CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: testRegisterCode, Password: "GuanJin#S4c-Strong1",
	})
	if err != nil {
		t.Fatalf("CompleteRegistration: %v", err)
	}
	f.userIDs = append(f.userIDs, res.UserID)

	_, err = svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: email,
	})
	if !errors.Is(err, ErrIdentifierAlreadyBound) {
		t.Fatalf("已占用应报 ErrIdentifierAlreadyBound，实际 %v", err)
	}
	if code, ok := CodeOf(err); !ok || code != httpx.CodeIdentifierAlreadyBound {
		t.Fatalf("应映射 IDENTIFIER_ALREADY_BOUND，实际 %q", code)
	}
}

// AC-REG-06（数据侧）：complete 走通 → users / identifiers（verified）/ completed+consumed 的 flows。
// 令牌三件不在这里验（S6）。
func TestCompleteRegistrationCreatesAccount(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)

	email := "s4c-" + uuid.NewString()[:12] + "@example.com"
	start, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: email, TraceID: "tr-3",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res, err := svc.CompleteRegistration(context.Background(), CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: testRegisterCode, Password: "GuanJin#S4c-Strong1", TraceID: "tr-4",
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	f.userIDs = append(f.userIDs, res.UserID)

	var verified bool
	var trust int16
	if err := db.Raw(`SELECT verified, trust_level FROM identifiers WHERE id = ?`, res.IdentifierID).
		Row().Scan(&verified, &trust); err != nil {
		t.Fatalf("读 identifiers：%v", err)
	}
	if !verified || trust != 2 {
		t.Fatalf("标识符应为 verified + trust_level=2，实际 %v/%d", verified, trust)
	}

	var status string
	var consumedAt *time.Time
	if err := db.Raw(`SELECT status, consumed_at FROM flows WHERE id = ?`, res.FlowID).
		Row().Scan(&status, &consumedAt); err != nil {
		t.Fatalf("读 flows：%v", err)
	}
	if status != "completed" || consumedAt == nil {
		t.Fatalf("注册应落 completed + consumed 的流（I-F4），实际 %s / %v", status, consumedAt)
	}

	// 同一个令牌再来一次 → 挑战已消费（一次性）。
	if _, err := svc.CompleteRegistration(context.Background(), CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: testRegisterCode, Password: "GuanJin#S4c-Strong1",
	}); !errors.Is(err, ErrRegisterChallengeConsumed) {
		t.Fatalf("二次提交应报 CHALLENGE_CONSUMED，实际 %v", err)
	}
}

// 验证码错误：先 401，达次数上限后 ATTEMPTS_EXCEEDED；两次都不建号。
func TestCompleteRegistrationWrongCodeCountsAttempts(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 2)

	start, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: "s4c-" + uuid.NewString()[:12] + "@example.com",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// 基数在 start **之后**取：start 本来就会落一条挑战行，拿它当基线会误判。
	before := countRows(t, db, f.clientID)

	if _, err := svc.CompleteRegistration(context.Background(), CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: "000000", Password: "GuanJin#S4c-Strong1",
	}); !errors.Is(err, ErrInvalidRegisterCode) {
		t.Fatalf("错码应报 ErrInvalidRegisterCode，实际 %v", err)
	}
	if code, _ := CodeOf(ErrInvalidRegisterCode); code != httpx.CodeInvalidCredential {
		t.Fatalf("错码应映射 INVALID_CREDENTIAL，实际 %s", code)
	}
	// 第 2 次（= max_attempts）→ 用尽。
	if _, err := svc.CompleteRegistration(context.Background(), CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: "000000", Password: "GuanJin#S4c-Strong1",
	}); !errors.Is(err, ErrRegisterAttemptsExceeded) {
		t.Fatalf("达上限应报 ErrRegisterAttemptsExceeded，实际 %v", err)
	}
	if after := countRows(t, db, f.clientID); after != before {
		t.Fatalf("校验失败不得建号：before=%d after=%d", before, after)
	}
}

// 令牌：篡改 → INVALID_PARAMETER；过期 → CHALLENGE_EXPIRED（用库时间判）。
func TestCompleteRegistrationTokenGuards(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)

	// 篡改：改最后一段。
	start, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "username", Value: "s4c-" + uuid.NewString()[:8],
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// 改 payload 段（不是最后一位：base64 末位有填充位，换字符可能解出同一字节）。
	parts := splitToken(t, start.RegisterToken)
	tampered := parts[0] + "." + flipLast(parts[1]) + "." + parts[2]
	_, err = svc.CompleteRegistration(context.Background(), CompleteRegistrationRequest{
		RegisterToken: tampered, Password: "GuanJin#S4c-Strong1",
	})
	if !errors.Is(err, ErrInvalidRegisterToken) {
		t.Fatalf("篡改令牌应报 ErrInvalidRegisterToken，实际 %v", err)
	}
	if code, ok := CodeOf(err); !ok || code != httpx.CodeInvalidParam {
		t.Fatalf("应映射 INVALID_PARAMETER，实际 %q", code)
	}

	// 过期：签一个已经过期的令牌。
	claims := RegisterClaims{V: 1, ClientID: f.clientID, Type: "username",
		Value: "s4c-" + uuid.NewString()[:8], ExpiresAt: time.Now().Add(-time.Minute).Unix()}
	token, err := svc.sign(claims)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := svc.CompleteRegistration(context.Background(), CompleteRegistrationRequest{
		RegisterToken: token, Password: "GuanJin#S4c-Strong1",
	}); !errors.Is(err, ErrRegisterTokenExpired) {
		t.Fatalf("过期令牌应报 ErrRegisterTokenExpired，实际 %v", err)
	}
}

// 服务构造：缺签名键 / 缺挑战参数 → 装配期失败（不发明"不签名的令牌"或默认 TTL）。
func TestNewRejectsMissingRegisterConfig(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	cfg.RegisterTokenKey = crypto.HMACKey{}
	if _, err := New(db, cfg); !errors.Is(err, ErrMissingKey) {
		t.Fatalf("缺 register token 键应报 ErrMissingKey，实际 %v", err)
	}
	cfg = testConfig(t)
	cfg.ChallengeTTL = 0
	if _, err := New(db, cfg); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("缺 challenge ttl 应报 ErrInvalidTTL，实际 %v", err)
	}
	cfg = testConfig(t)
	cfg.ChallengeMaxAttempts = 0
	if _, err := New(db, cfg); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("缺 challenge max_attempts 应报 ErrInvalidTTL，实际 %v", err)
	}
}

// 令牌里的标识符值必须是**规范化后**的（大小写/空白已在 start 阶段归一）。
func TestStartNormalizesIdentifierInToken(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)

	raw := "  S4C-" + strings.ToUpper(uuid.NewString()[:8]) + "@Example.COM "
	start, err := svc.StartRegistration(context.Background(), StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: raw,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	claims, err := parseRegisterToken(svc.cfg.RegisterTokenKey, start.RegisterToken)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.Value != strings.ToLower(strings.TrimSpace(raw)) {
		t.Fatalf("令牌里的值应已规范化，实际 %q", claims.Value)
	}
}

// countRows 数**本切片夹具**相关的行（用于"零新增"断言）。
//
// 只数 `s4c-` 前缀的用户/标识符与该 client 的挑战：整个测试库是共用的，
// 数全表会被其它包的用例带偏（`go test ./...` 各包并行）。
func countRows(t *testing.T, db *gorm.DB, clientID string) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(`SELECT
		(SELECT count(*) FROM users WHERE username LIKE 's4c-%') +
		(SELECT count(*) FROM identifiers WHERE user_id IN (SELECT id FROM users WHERE username LIKE 's4c-%')) +
		(SELECT count(*) FROM challenges WHERE client_id = ?)`, clientID).Scan(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// 挑战/令牌的边界分支：挑战不存在、码为空、挑战已过期。
func TestCompleteRegistrationChallengeGuards(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)
	ctx := context.Background()

	// ① 令牌里的 chid 指向不存在的挑战 → 按令牌不合规（不泄漏"这个 id 曾存在"）。
	claims := RegisterClaims{V: 1, ClientID: f.clientID, Type: "email",
		Value:       "s4c-" + uuid.NewString()[:8] + "@example.com",
		ChallengeID: uuid.NewString(), ExpiresAt: time.Now().Add(time.Minute).Unix()}
	token, err := svc.sign(claims)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
		RegisterToken: token, Code: testRegisterCode, Password: "GuanJin#S4c-Strong1",
	}); !errors.Is(err, ErrInvalidRegisterToken) {
		t.Fatalf("挑战不存在应报 ErrInvalidRegisterToken，实际 %v", err)
	}

	// ② 有挑战但没给码 → INVALID_CREDENTIAL（不消耗尝试次数以外的任何东西）。
	start, err := svc.StartRegistration(ctx, StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: "s4c-" + uuid.NewString()[:12] + "@example.com",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Password: "GuanJin#S4c-Strong1",
	}); !errors.Is(err, ErrInvalidRegisterCode) {
		t.Fatalf("缺码应报 ErrInvalidRegisterCode，实际 %v", err)
	}

	// ③ 挑战行已过期（令牌未过期）→ CHALLENGE_EXPIRED。
	start2, err := svc.StartRegistration(ctx, StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: "s4c-" + uuid.NewString()[:12] + "@example.com",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	// 约束 `challenges_expires_after_created` 要求 expires_at > created_at，
	// 所以把**两列一起**往过去挪（只改 expires_at 会被 23514 拒）。
	if err := db.Exec(`UPDATE challenges
		SET created_at = now() - interval '10 minutes', expires_at = now() - interval '1 second'
		WHERE id = ?`, *start2.ChallengeID).Error; err != nil {
		t.Fatalf("force expire: %v", err)
	}
	if _, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
		RegisterToken: start2.RegisterToken, Code: testRegisterCode, Password: "GuanJin#S4c-Strong1",
	}); !errors.Is(err, ErrRegisterTokenExpired) {
		t.Fatalf("挑战过期应报 ErrRegisterTokenExpired，实际 %v", err)
	}
}

// 标识符生命周期（`BindIdentifier` / `UnbindIdentifier`）：绑定非主标识符、解绑不违 I1。
func TestDBBindAndUnbindIdentifier(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)
	ctx := context.Background()

	// 先有一个带口令 + 已验证邮箱的账号（首条标识符 = 主）。
	email := "s4d-" + uuid.NewString()[:12] + "@example.com"
	start, err := svc.StartRegistration(ctx, StartRegistrationRequest{ClientID: f.clientID, Type: "email", Value: email})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: testRegisterCode, Password: "GuanJin#S4d-Strong1",
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	f.userIDs = append(f.userIDs, res.UserID)

	// 绑第二条（非主）手机号。
	bound, err := svc.BindIdentifier(ctx, BindRequest{
		UserID: res.UserID, Type: "phone",
		Value:    fmt.Sprintf("+8615%08d", time.Now().UnixNano()%100000000),
		Verified: true, VerifyMethod: "sms_otp", TrustLevel: 2,
	})
	if err != nil {
		t.Fatalf("BindIdentifier: %v", err)
	}
	if bound.IsPrimary {
		t.Fatal("第二条标识符不该成为主标识符（I6 保护最后一个主）")
	}

	// 解绑这条非主标识符：允许（还剩主标识符 + 口令）。
	if err := svc.UnbindIdentifier(ctx, res.UserID, bound.ID); err != nil {
		t.Fatalf("UnbindIdentifier: %v", err)
	}
	var status string
	if err := db.Raw(`SELECT status FROM identifiers WHERE id = ?`, bound.ID).Row().Scan(&status); err != nil {
		t.Fatalf("read: %v", err)
	}
	if status != "disabled" {
		t.Fatalf("解绑应置 disabled（不物理删），实际 %s", status)
	}
}
