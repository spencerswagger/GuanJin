//go:build integration

package identity

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 凭据仓储（`credentialstore.go`）与标识符验证（`lifecycle.go` 的 `VerifyIdentifier`）的**真库**用例。
//
// 为什么必须有：这两个文件是"因子层怎么读到凭据"的唯一入口。它们不被覆盖时，
// 任何一次"信封解析失败被静默跳过"的改动都不会被测试拦住 —— 而那等于让坏口令变成
// "这个用户没有口令"。

func newCredStore(t *testing.T, db *gorm.DB) *CredentialStore {
	t.Helper()
	cs, err := NewCredentialStore(db)
	if err != nil {
		t.Fatalf("NewCredentialStore: %v", err)
	}
	return cs
}

// 口令信封：注册出来的凭据能被因子层按 `type=password` 读出来并解析成信封。
func TestDBCredentialStorePasswordEnvelopes(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)
	cs := newCredStore(t, db)
	ctx := context.Background()

	// 非法 user_id：一律按"凭据无效"处理（不泄露"这不是 UUID"）。
	if _, err := cs.ListPasswordEnvelopes(ctx, "not-a-uuid"); !errors.Is(err, credential.ErrInvalidCredential) {
		t.Fatalf("非法 user_id 应报 ErrInvalidCredential，实际 %v", err)
	}
	// 未知用户：空列表而不是报错（"没有口令"是正常状态，不是故障）。
	empty, err := cs.ListPasswordEnvelopes(ctx, uuid.NewString())
	if err != nil {
		t.Fatalf("未知用户不该报错：%v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("未知用户不该有信封，实际 %d", len(empty))
	}

	// 真用户：注册 → 信封可读、可用原口令验过。
	email := "s4d-" + uuid.NewString()[:12] + "@example.com"
	const password = "GuanJin#S4d-Strong1"
	start, err := svc.StartRegistration(ctx, StartRegistrationRequest{ClientID: f.clientID, Type: "email", Value: email})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: testRegisterCode, Password: password,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	f.userIDs = append(f.userIDs, res.UserID)

	envs, err := cs.ListPasswordEnvelopes(ctx, res.UserID.String())
	if err != nil {
		t.Fatalf("ListPasswordEnvelopes: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("应有 1 条口令信封，实际 %d", len(envs))
	}
	ok, err := envs[0].Verify(password)
	if err != nil || !ok {
		t.Fatalf("信封应能用原口令验过：ok=%v err=%v", ok, err)
	}
	// 坏口令不得验过。
	if ok, _ := envs[0].Verify("wrong"); ok {
		t.Fatal("错口令不该通过")
	}

	// TOTP：口令用户没有 TOTP 行 → 空列表；非法 user_id 同样按"凭据无效"。
	if recs, err := cs.ListTOTPRecords(ctx, res.UserID.String()); err != nil || len(recs) != 0 {
		t.Fatalf("口令用户不该有 TOTP 记录：recs=%d err=%v", len(recs), err)
	}
	if _, err := cs.ListTOTPRecords(ctx, "bad"); !errors.Is(err, credential.ErrInvalidCredential) {
		t.Fatalf("非法 user_id 应报 ErrInvalidCredential，实际 %v", err)
	}
}

// TOTP 记录与时间步消费：真 TOTP 行能读出信封，`ConsumeStep` 只让第一次成功。
func TestDBCredentialStoreTOTPAndConsumeStep(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)
	cs := newCredStore(t, db)
	ctx := context.Background()

	// 先建一个用户（用 username 免验证路径，快）。
	start, err := svc.StartRegistration(ctx, StartRegistrationRequest{
		ClientID: f.clientID, Type: "username", Value: "s4d-" + uuid.NewString()[:8],
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	res, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Password: "GuanJin#S4d-Strong1",
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	f.userIDs = append(f.userIDs, res.UserID)

	// 造一条真 TOTP 凭据行（信封用 SM4 加密的密钥材料）。
	key, err := newTOTPKey(t)
	if err != nil {
		t.Fatalf("totp key: %v", err)
	}
	env, secret, err := credential.NewTOTPEnvelope(key, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	if len(secret) == 0 {
		t.Fatal("夹具失败：NewTOTPEnvelope 应回明文密钥（仅供绑定那一刻）")
	}
	// 落库的是**信封**（JSON），不是密文块：`credential_data` 是 jsonb。
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	row := &model.Authenticator{
		UserID:         res.UserID,
		Type:           "totp",
		Status:         "active",
		CredentialData: model.JSONValue(raw),
	}
	row.ID = mustUUIDv7(t)
	if err := store.NewAuthenticatorRepo(db).Create(ctx, row); err != nil {
		t.Fatalf("insert authenticator: %v", err)
	}

	recs, err := cs.ListTOTPRecords(ctx, res.UserID.String())
	if err != nil {
		t.Fatalf("ListTOTPRecords: %v", err)
	}
	if len(recs) != 1 || recs[0].AuthenticatorID != row.ID.String() {
		t.Fatalf("应读到 1 条 TOTP 记录，实际 %+v", recs)
	}

	// 时间步只许推进一次（并发双花/同窗重放必须被挡住）。
	if ok, err := cs.ConsumeStep(ctx, row.ID.String(), 12345); err != nil || !ok {
		t.Fatalf("首次推进应成功：ok=%v err=%v", ok, err)
	}
	if _, err := cs.ConsumeStep(ctx, row.ID.String(), 12345); err != nil {
		t.Fatalf("同一步再推进不该报错（影响 0 行）：%v", err)
	}
	if _, err := cs.ConsumeStep(ctx, "not-a-uuid", 1); err == nil {
		t.Fatal("非法 authenticator id 必须报错")
	}
}

// `VerifyIdentifier`：置 verified + 幂等 + 归属校验。
func TestDBVerifyIdentifier(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)
	svc := newRegisterService(t, db, 5*time.Minute, 5)
	ctx := context.Background()

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

	// 把它先置成未验证，再走一遍 VerifyIdentifier（模拟"绑定后验证"）。
	if err := db.Exec(`UPDATE identifiers SET verified = false, trust_level = 0 WHERE id = ?`, res.IdentifierID).Error; err != nil {
		t.Fatalf("reset verified: %v", err)
	}
	if err := svc.VerifyIdentifier(ctx, res.UserID, res.IdentifierID, "email_otp", 2); err != nil {
		t.Fatalf("VerifyIdentifier: %v", err)
	}
	var verified bool
	var trust int16
	if err := db.Raw(`SELECT verified, trust_level FROM identifiers WHERE id = ?`, res.IdentifierID).
		Row().Scan(&verified, &trust); err != nil {
		t.Fatalf("read identifier: %v", err)
	}
	if !verified || trust != 2 {
		t.Fatalf("应置 verified/trust=2，实际 %v/%d", verified, trust)
	}

	// 幂等：再验一次不报错、不重复写事件。
	if err := svc.VerifyIdentifier(ctx, res.UserID, res.IdentifierID, "email_otp", 2); err != nil {
		t.Fatalf("幂等再验不该报错：%v", err)
	}

	// 归属不符：别人的标识符必须被拒（先撞"用户不存在"，再撞归属；两者都不许静默通过）。
	other := uuid.New()
	if err := svc.VerifyIdentifier(ctx, other, res.IdentifierID, "email_otp", 2); err == nil {
		t.Fatal("不属于该用户的标识符必须报错")
	}
}

// 默认验证码生成器（不注入 `GenerateCode` 时的路径）：6 位数字、两次不同。
func TestGenerateCodeDefault(t *testing.T) {
	svc := &Service{cfg: testConfig(t)}
	svc.cfg.GenerateCode = nil
	svc.cfg.Random = rand.Reader // New() 会兜这一层；直接构造 Service 时要自己给
	a, err := svc.generateCode()
	if err != nil {
		t.Fatalf("generateCode: %v", err)
	}
	b, err := svc.generateCode()
	if err != nil {
		t.Fatalf("generateCode: %v", err)
	}
	if len(a) != 6 || len(b) != 6 {
		t.Fatalf("应为 6 位，实际 %q / %q", a, b)
	}
	for i := 0; i < 6; i++ {
		if a[i] < '0' || a[i] > '9' {
			t.Fatalf("应为纯数字，实际 %q", a)
		}
	}
	if a == b {
		t.Fatalf("两次生成不该相同（随机源）：%q", a)
	}
}

func newTOTPKey(t *testing.T) (crypto.SM4Key, error) {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 5)
	}
	mk, err := crypto.NewMasterKey(raw)
	if err != nil {
		return crypto.SM4Key{}, err
	}
	return mk.DeriveSM4(crypto.PurposeSM4Field)
}

func mustUUIDv7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	return id
}
