//go:build integration

package main

// AC-AUD-12 的**应用侧全链路国密实证**（S11 判据 ⑤ 之外的 ①–④⑥；⑤ 是静态算法扫描，
// 见 `scripts/check-guomi-algorithms.sh`）。
//
// 为什么单开一个"证据"用例，而不是把断言散在各自包里：验收要看的是**一条链路里六个位置
// 用的都是国密**，任何一个位置退回 SHA/RSA 都必须在这里看得出来。分散的用例单看每条都绿，
// 但"哪六条对应 AC-AUD-12 的六项"没有人能一眼说清。
//
// 口径（`docs/acceptance-criteria.md` AC-AUD-12）：
//   ①令牌 `SM3withSM2`；②审计完整性 `HMAC-SM3`；③敏感字段 `SM4`；
//   ④口令 `PBKDF2-SM3`（迭代 ≥100000、盐 ≥16B）；⑤全仓算法扫描不含 SHA 系/RSA（唯一白名单 PKCE S256）；
//   ⑥主密钥只做 HKDF-SM3 派生、不进业务算法。
//
// 本文件负责 ①②③④⑥ 的**运行期**证据；⑤ 由扫描脚本判。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/audit"
	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/signing"
)

// TestAC_AUD_12_FullChainGuomi 一条链路把 AC-AUD-12 的六项走一遍。
func TestAC_AUD_12_FullChainGuomi(t *testing.T) {
	ctx := context.Background()

	// ===== ⑥ 主密钥只做 HKDF-SM3 派生、不进业务算法 =====
	masterRaw := make([]byte, 32)
	if _, err := rand.Read(masterRaw); err != nil {
		t.Fatalf("rand: %v", err)
	}
	master, err := crypto.NewMasterKey(masterRaw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	// 业务侧拿到的只有"用途域派生出的句柄"：`crypto.HMACKey` / `crypto.SM4Key` 的字段全部不导出，
	// 调用方拿不到原始字节（要用只能走 `HMACSM3` / `EncryptSM4` 这两个算法入口）。
	auditKey, err := master.DeriveHMAC(crypto.PurposeHMACAudit)
	if err != nil {
		t.Fatalf("DeriveHMAC(audit): %v", err)
	}
	fieldKey, err := master.DeriveSM4(crypto.PurposeSM4Field)
	if err != nil {
		t.Fatalf("DeriveSM4(field): %v", err)
	}
	if auditKey.IsZero() || fieldKey.IsZero() {
		t.Fatal("派生密钥不得为零值（零值等于没有密钥）")
	}
	// 不同用途域必须派生不同密钥：同一消息的 HMAC 不同。
	otherKey, err := master.DeriveHMAC(crypto.PurposeHMACRegister)
	if err != nil {
		t.Fatalf("DeriveHMAC(register): %v", err)
	}
	msg := []byte("purpose-separation")
	if string(crypto.HMACSM3(auditKey, msg)) == string(crypto.HMACSM3(otherKey, msg)) {
		t.Fatal("不同用途域派生出了同一把键（用途域隔离失效）")
	}
	// 主密钥字节不进业务算法：`HMACSM3` 的入参类型是 `crypto.HMACKey`（不是 []byte），
	// 传原始字节在编译期就不可能——这条由类型系统保证，这里只登记说明。

	// ===== ① 令牌：alg=SM3withSM2，且能用 SM2 公钥验过 =====
	priv, err := crypto.GenerateSM2Key(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateSM2Key: %v", err)
	}
	signKey := &signing.Key{
		Kid:       "ac-aud-12-" + uuid.NewString()[:8],
		Purpose:   "oidc",
		Algorithm: signing.AlgSM3WithSM2,
		Public:    &priv.PublicKey,
		Private:   priv,
	}
	claims := map[string]any{
		"sub": "ac-aud-12-user",
		"iat": time.Now().UTC().Unix(),
		"exp": time.Now().UTC().Add(time.Minute).Unix(),
		"jti": uuid.NewString(),
	}
	token, err := signing.SignJWT(signKey, claims)
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token 段数应为 3，实际 %d", len(parts))
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("解 header: %v", err)
	}
	var header map[string]any
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatalf("解析 header: %v", err)
	}
	if alg, _ := header["alg"].(string); alg != "SM3withSM2" {
		t.Fatalf("①令牌 alg 必须是 SM3withSM2，实际 %q", alg)
	}
	got, err := signing.VerifyJWT(&priv.PublicKey, token)
	if err != nil {
		t.Fatalf("①SM2 验签失败：%v", err)
	}
	if got["sub"] != "ac-aud-12-user" {
		t.Fatalf("①验签后 claim 不对：%v", got["sub"])
	}
	// 算法降级必须被拒（改动 header 的 alg 后签名对不上）。
	tampered := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"x","typ":"JWT"}`)) +
		"." + parts[1] + "." + parts[2]
	if _, err := signing.VerifyJWT(&priv.PublicKey, tampered); err == nil {
		t.Fatal("①把 alg 改成 RS256 后仍验签通过 —— 算法降级没被拒")
	}

	// ===== ② 审计完整性：HMAC-SM3（密钥 + 前一条 tag + 内容都在输入里）=====
	chain, err := audit.NewChain(auditKey)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	base := audit.Record{
		ID:         uuid.New(),
		Action:     "ac-aud-12.evidence",
		Result:     "success",
		OccurredAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	prev := []byte("prev-tag-of-previous-record")
	tag, err := chain.Tag(prev, base)
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if len(tag) != 32 {
		t.Fatalf("②HMAC-SM3 输出应为 32 字节（SM3 摘要长度），实际 %d", len(tag))
	}
	if again, err := chain.Tag(prev, base); err != nil || string(again) != string(tag) {
		t.Fatalf("②同一输入必须得到同一 tag（确定性）：again=%x err=%v", again, err)
	}
	if withOtherKey, err := mustChain(t, otherKey).Tag(prev, base); err != nil ||
		string(withOtherKey) == string(tag) {
		t.Fatal("②换一把 key 应得到不同 tag（说明 key 真的进了 HMAC）")
	}
	if withOtherPrev, err := chain.Tag([]byte("another-prev"), base); err != nil ||
		string(withOtherPrev) == string(tag) {
		t.Fatal("②换 prev_tag 应得到不同 tag（说明 prev 真的进了 HMAC）")
	}
	changed := base
	changed.Result = "failure"
	if withOtherContent, err := chain.Tag(prev, changed); err != nil ||
		string(withOtherContent) == string(tag) {
		t.Fatal("②改内容应得到不同 tag（说明内容真的进了 HMAC）")
	}

	// ===== ③ 敏感字段：SM4 信封 v2（enc-then-MAC）=====
	plaintext := []byte("13800138000") // 手机号：典型敏感字段
	ciphertext, err := crypto.EncryptSM4(fieldKey, plaintext)
	if err != nil {
		t.Fatalf("EncryptSM4: %v", err)
	}
	if ciphertext[0] != crypto.SM4EnvelopeVersionV2 {
		t.Fatalf("③信封版本必须是 v2(0x02)，实际 0x%02x", ciphertext[0])
	}
	if strings.Contains(string(ciphertext), string(plaintext)) {
		t.Fatal("③密文里出现了明文（没有真正加密）")
	}
	if back, err := crypto.DecryptSM4(fieldKey, ciphertext); err != nil || string(back) != string(plaintext) {
		t.Fatalf("③解密应还原明文：got=%q err=%v", back, err)
	}
	// 篡改任意一位必须被 MAC 挡住（这是"敏感字段"与"完整性"两件事的交点）。
	tamperedCt := append([]byte(nil), ciphertext...)
	tamperedCt[len(tamperedCt)-1] ^= 0xFF
	if _, err := crypto.DecryptSM4(fieldKey, tamperedCt); !errors.Is(err, crypto.ErrIntegrityMismatch) {
		t.Fatalf("③篡改密文必须报 ErrIntegrityMismatch，实际 %v", err)
	}

	// ===== ④ 口令：PBKDF2-SM3，迭代 ≥100000、盐 ≥16B =====
	params := crypto.PasswordParams{Iterations: crypto.MinPBKDF2Iterations, KeyLen: crypto.MinPasswordKeyLen}
	env, err := credential.NewPasswordEnvelope("Passw0rd!-ac-aud-12", params, time.Now().UTC(), rand.Reader)
	if err != nil {
		t.Fatalf("NewPasswordEnvelope: %v", err)
	}
	if env.Algo != "PBKDF2-SM3" {
		t.Fatalf("④口令算法必须是 PBKDF2-SM3，实际 %q", env.Algo)
	}
	if env.Iterations < 100000 {
		t.Fatalf("④迭代次数必须 ≥100000，实际 %d", env.Iterations)
	}
	salt, err := base64.StdEncoding.DecodeString(env.Salt)
	if err != nil {
		t.Fatalf("④盐不是合法 base64：%v", err)
	}
	if len(salt) < 16 {
		t.Fatalf("④盐长度必须 ≥16B，实际 %d", len(salt))
	}
	// 用同一参数重算必须得到同一个派生键（证明它真的走的是这条 PBKDF2-SM3 路径）。
	recomputed, err := crypto.DerivePassword([]byte("Passw0rd!-ac-aud-12"), salt,
		crypto.PasswordParams{Iterations: env.Iterations, KeyLen: env.KeyLen})
	if err != nil {
		t.Fatalf("④DerivePassword: %v", err)
	}
	if base64.StdEncoding.EncodeToString(recomputed) != env.DK {
		t.Fatal("④口令派生键与信封不一致（PBKDF2-SM3 路径没被走到）")
	}

	// ===== ②补：审计链真落库（"集成"那一半）=====
	db := openTestDB(t)
	occurredAt := time.Now().UTC()
	// 既有链尾可能在未来月份（其它用例推高过）：取 max(now, 链尾) 保证不撞下界守卫。
	//
	// **空值语义必须分开，不许混成零值**：`max(occurred_at)` 在**空表**上返回 SQL NULL，
	// 那是"这条链还没有任何审计事实"这一合法初始状态（用例照跑，`occurred_at` 保持 now）；
	// 而**查询失败**（表不存在 / 连接断）必须当场炸——把它当"无数据"会让这条证据用例
	// 在库坏掉时照样绿，那是最坏的假绿。所以用 `sql.NullTime` + `Row().Scan`：
	// `err != nil` = 查询失败，`!Valid` = 空表（CI 首跑就是这么红的：
	// `Scan error … unsupported Scan, storing driver.Value type <nil> into type *time.Time`）。
	var tail sql.NullTime
	if err := db.Raw(`SELECT max(occurred_at) FROM audit_logs`).Row().Scan(&tail); err != nil {
		t.Fatalf("读链尾失败（查询失败；空表应返回 NULL 且 tail.Valid=false，不该报错）：%v", err)
	}
	if tail.Valid && occurredAt.Before(tail.Time) {
		occurredAt = tail.Time
	}
	rec := audit.Record{
		ID:         uuid.New(),
		Action:     "ac-aud-12.db-evidence",
		Result:     "success",
		TargetID:   "ac-aud-12-" + uuid.NewString()[:8],
		OccurredAt: occurredAt,
	}
	savedTag, err := chain.Append(ctx, db, rec)
	if err != nil {
		t.Fatalf("审计链落库失败（AC-AUD-12② 的集成那一半）：%v", err)
	}
	if len(savedTag) != 32 {
		t.Fatalf("②落库 tag 长度应为 32，实际 %d", len(savedTag))
	}
	loaded, err := chain.Load(ctx, db, occurredAt.Truncate(time.Second).Add(-time.Minute), occurredAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, one := range loaded {
		if one.ID == rec.ID {
			found = true
			if string(one.IntegrityTag) != string(savedTag) {
				t.Fatalf("②落库后读回的 tag 与写入不一致：%x != %x", one.IntegrityTag, savedTag)
			}
		}
	}
	if !found {
		t.Fatal("②落库的审计记录没读回来")
	}
}

func mustChain(t *testing.T, key crypto.HMACKey) *audit.Chain {
	t.Helper()
	c, err := audit.NewChain(key)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	return c
}
