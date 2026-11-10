//go:build integration

package credential

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// 真库用例（**必须有真 PostgreSQL**）：
//
//	GUANJIN_TEST_DSN=... go test -tags=integration ./internal/credential/
//
// 口径（Gate §9.1 / CI integration job）：
//   - 本用例**不打 t.Skip**：缺 DSN 就**红**。"没库所以跳过"等于"未验证"，
//     而 Gate 已把 SKIP 判成红（`skip>0` 即红），留个 skip 只会让本地看着绿。
//   - 前置：目标库已按 `migrations/` 手动迁移（P1：迁移不随应用启动，也不由测试代跑）。
func openTestDB(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		t.Fatalf("必须设置 GUANJIN_TEST_DSN（integration job 已注入）；不设就是不验证，按红处理")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("连接测试库失败（job 里应已起 postgres:16 + migrate up）：%v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("ping 测试库失败：%v", err)
	}
	var regclass *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass('public.authenticators')::text`).Scan(&regclass); err != nil {
		t.Fatalf("查表存在性失败：%v", err)
	}
	if regclass == nil {
		t.Fatalf("测试库缺 authenticators 表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN")
	}
	return conn
}

// 建一个隔离用户（唯一 username），测试结束连认证器一起清掉。
func newTestUser(t *testing.T, conn *pgx.Conn) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	username := "s4a-" + uuid.NewString()[:20]
	if _, err := conn.Exec(ctx,
		`INSERT INTO users (id, username) VALUES ($1, $2)`, id, username); err != nil {
		t.Fatalf("建用户失败：%v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = conn.Exec(bg, `DELETE FROM authenticators WHERE user_id = $1`, id)
		_, _ = conn.Exec(bg, `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

// AC-AUD-12④ 的**真库证据**：口令信封落 `authenticators.credential_data` 后被读回，
// 参数仍是 PBKDF2-SM3 / 迭代 ≥100000 / 盐 ≥16B，且库里没有口令明文。
//
// 为什么这条必须是集成用例：AC-AUD-12 的判据是"算法与参数"，而"落库后参数还是不是那些"
// 只有真 jsonb 往返能证明——单测里的结构体自洽证明不了 DB 这一环。
func TestAC_AUD_12_PasswordEnvelopeRoundTripsInDB(t *testing.T) {
	conn := openTestDB(t)
	ctx := context.Background()
	userID := newTestUser(t, conn)

	const password = "GuanJin#S4a-Str0ng"
	envelope := newTestEnvelope(t, password)
	raw, err := envelope.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	authID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	if _, err := conn.Exec(ctx,
		`INSERT INTO authenticators (id, user_id, type, credential_data) VALUES ($1, $2, 'password', $3::jsonb)`,
		authID, userID, string(raw)); err != nil {
		t.Fatalf("写 authenticators 失败：%v", err)
	}

	var stored []byte
	if err := conn.QueryRow(ctx,
		`SELECT credential_data FROM authenticators WHERE id = $1`, authID).Scan(&stored); err != nil {
		t.Fatalf("读回 credential_data 失败：%v", err)
	}

	// ① 库里不得出现口令明文。
	if strings.Contains(string(stored), password) {
		t.Fatalf("credential_data 里出现口令明文：%s", string(stored))
	}

	// ② 算法与参数：解析这条路径本身就带 Validate（弱参数会被拒）。
	back, err := ParsePasswordEnvelope(stored)
	if err != nil {
		t.Fatalf("落库后的信封必须仍然合法：%v", err)
	}
	if back.Algo != AlgoPBKDF2SM3 {
		t.Fatalf("algo = %q，期望 %q", back.Algo, AlgoPBKDF2SM3)
	}
	if back.Iterations < crypto.MinPBKDF2Iterations {
		t.Fatalf("迭代 %d < %d（基线硬下限）", back.Iterations, crypto.MinPBKDF2Iterations)
	}
	salt, err := base64.StdEncoding.DecodeString(back.Salt)
	if err != nil || len(salt) < MinPasswordSaltBytes {
		t.Fatalf("盐解码 %d 字节（err=%v），要求 ≥%d", len(salt), err, MinPasswordSaltBytes)
	}

	// ③ 读回后仍能正确校验。
	if ok, err := back.Verify(password); err != nil || !ok {
		t.Fatalf("读回后正确口令应通过：ok=%v err=%v", ok, err)
	}
	if ok, err := back.Verify(password + "x"); err != nil || ok {
		t.Fatalf("读回后错误口令必须失败：ok=%v err=%v", ok, err)
	}

	// ④ 库里存的算法名不得出现 SHA 系 / RSA（索引 §2「禁与 SHA 系列混用」）。
	var probe struct {
		Algo string `json:"algo"`
	}
	if err := json.Unmarshal(stored, &probe); err != nil {
		t.Fatalf("解析 algo 失败：%v", err)
	}
	upper := strings.ToUpper(probe.Algo)
	for _, banned := range []string{"SHA", "RSA", "MD5", "BCRYPT", "SCRYPT"} { // NEGATIVE-SHA-TEST（负向用例：断言这些算法必须被拒；扫描器只在测试文件认这个标记）
		if strings.Contains(upper, banned) {
			t.Fatalf("口令算法 %q 命中禁用算法 %q", probe.Algo, banned)
		}
	}
}

// TOTP 信封也必须能落库往返（密文进 jsonb、解出来还能算码）。
//
// 这条不涉及 PBKDF2，所以很便宜；但它是"新增因子零 DDL"的直接证据：
// 同一张 `authenticators` 表、同一个 jsonb 列，换一个 type 就换了一种凭据结构。
func TestTOTPEnvelopeRoundTripsInDB(t *testing.T) {
	conn := openTestDB(t)
	ctx := context.Background()
	userID := newTestUser(t, conn)
	key := testSM4Key(t)

	envelope, secret, err := NewTOTPEnvelope(key, nil, time.Now())
	if err != nil {
		t.Fatalf("NewTOTPEnvelope: %v", err)
	}
	raw, err := envelope.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	authID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	// type 用开放取值 `totp`：DB 没有 CHECK，合法性由代码侧注册表管（D5 / 数据模型 §5）。
	if _, err := conn.Exec(ctx,
		`INSERT INTO authenticators (id, user_id, type, credential_data) VALUES ($1, $2, 'totp', $3::jsonb)`,
		authID, userID, string(raw)); err != nil {
		t.Fatalf("写 authenticators 失败：%v", err)
	}

	var stored []byte
	if err := conn.QueryRow(ctx,
		`SELECT credential_data FROM authenticators WHERE id = $1`, authID).Scan(&stored); err != nil {
		t.Fatalf("读回 credential_data 失败：%v", err)
	}
	back, err := ParseTOTPEnvelope(stored)
	if err != nil {
		t.Fatalf("落库后的 TOTP 信封必须仍然合法：%v", err)
	}
	opened, err := back.Open(key)
	if err != nil {
		t.Fatalf("读回后解封失败：%v", err)
	}
	if string(opened) != string(secret) {
		t.Fatal("读回后解出的共享密钥与原密钥不一致（SM4 往返失真）")
	}
	// 用读回的密钥算码并校验一次，闭环。
	at := time.Unix(1_760_000_000, 0).UTC()
	code, err := GenerateTOTP(opened, at, CodeOptions{Digits: back.Digits, Period: time.Duration(back.Period) * time.Second})
	if err != nil {
		t.Fatalf("GenerateTOTP: %v", err)
	}
	if _, err := VerifyTOTP(opened, code, at, TOTPOptions{
		Digits: back.Digits, Period: time.Duration(back.Period) * time.Second, Skew: 0,
	}); err != nil {
		t.Fatalf("读回的密钥算出的码应能校验通过：%v", err)
	}
}
