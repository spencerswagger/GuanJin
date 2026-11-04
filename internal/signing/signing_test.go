package signing

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

func testKeywrap(t *testing.T) crypto.SM4Key {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 21)
	}
	mk, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	k, err := mk.DeriveSM4(crypto.PurposeSM4Keywrap)
	if err != nil {
		t.Fatalf("DeriveSM4(keywrap): %v", err)
	}
	return k
}

// keygen → 解封：同一把私钥能签、公钥能验（自洽的最小闭环）。
func TestGenRowLoadAndSign(t *testing.T) {
	kw := testKeywrap(t)
	row, err := GenRow(nil, kw, "oidc-v1", PurposeOIDC, AlgSM3WithSM2)
	if err != nil {
		t.Fatalf("GenRow: %v", err)
	}
	set, err := Load(kw, []Row{row})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	key, err := set.For(PurposeOIDC)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if key.Kid != "oidc-v1" || key.Algorithm != AlgSM3WithSM2 {
		t.Fatalf("kid/alg 不符：%+v", key)
	}

	token, err := SignJWT(key, map[string]any{"sub": "u-1", "acr": "AAL1", "exp": time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	claims, err := VerifyJWT(key.Public, token)
	if err != nil {
		t.Fatalf("VerifyJWT: %v", err)
	}
	if claims["sub"] != "u-1" || claims["acr"] != "AAL1" {
		t.Fatalf("claims 不符：%v", claims)
	}

	// 篡改任一段必须被拒；篡改 alg 也必须被拒（防算法降级）。
	parts := strings.Split(token, ".")
	if _, err := VerifyJWT(key.Public, parts[0]+"."+parts[1]+"."+flip(parts[2])); err == nil {
		t.Fatal("篡改签名必须失败")
	}
	if _, err := VerifyJWT(key.Public, flip(parts[1])+"."+parts[1]+"."+parts[2]); err == nil {
		t.Fatal("篡改 payload 必须失败")
	}
	badAlg := b64(`{"alg":"RS256","kid":"oidc-v1","typ":"JWT"}`) + "." + parts[1] + "." + parts[2]
	if _, err := VerifyJWT(key.Public, badAlg); err == nil {
		t.Fatal("非 SM3withSM2 必须拒绝（不得算法降级）")
	}
}

// keygen 幂等：同一 kid 重复生成不得覆盖既有密钥（这里验的是"两次结果不同、由装配层用 DB 唯一键去重"）。
func TestGenRowProducesDistinctKeys(t *testing.T) {
	kw := testKeywrap(t)
	a, err := GenRow(nil, kw, "oidc-v1", PurposeOIDC, AlgSM3WithSM2)
	if err != nil {
		t.Fatalf("GenRow a: %v", err)
	}
	b, err := GenRow(nil, kw, "oidc-v1", PurposeOIDC, AlgSM3WithSM2)
	if err != nil {
		t.Fatalf("GenRow b: %v", err)
	}
	if bytes.Equal(a.PrivateEnc, b.PrivateEnc) {
		t.Fatal("两次生成应产出不同密钥（幂等由 DB 唯一键保证，不是靠复随机）")
	}
}

// 解封失败必须是**硬错误**（启动期非 0 退出），且保留 crypto 的哨兵用于区分"存量 v1"与"被篡改"。
func TestLoadRejectsUnsealableRows(t *testing.T) {
	kw := testKeywrap(t)
	row, err := GenRow(nil, kw, "oidc-v1", PurposeOIDC, AlgSM3WithSM2)
	if err != nil {
		t.Fatalf("GenRow: %v", err)
	}

	// ① 堂改密文 → 完整性失败（不是"跳过"）。
	tampered := row
	sealed := append([]byte(nil), row.PrivateEnc...)
	sealed[len(sealed)-1] ^= 0x01
	tampered.PrivateEnc = sealed
	if _, err := Load(kw, []Row{tampered}); !errors.Is(err, ErrKeyUnusable) || !errors.Is(err, crypto.ErrIntegrityMismatch) {
		t.Fatalf("篡改密文应报 ErrKeyUnusable 且保留 crypto 哨兵，实际 %v", err)
	}

	// ② keywrap 键缺失 → 直接报错（不能"用空键解封"）。
	if _, err := Load(crypto.SM4Key{}, []Row{row}); !errors.Is(err, ErrKeyUnusable) {
		t.Fatalf("缺 keywrap 键应报 ErrKeyUnusable，实际 %v", err)
	}

	// ③ 公钥不是 SM2 点 → 报错。
	bad := row
	bad.PublicKey = b64("not-a-point")
	if _, err := Load(kw, []Row{bad}); !errors.Is(err, ErrKeyUnusable) {
		t.Fatalf("坏公钥应报 ErrKeyUnusable，实际 %v", err)
	}
}

// 两用途各一把 → JWKS 两项且 kid 可区分（AC-LOGIN-07）；缺用途 → `ErrNoActiveKey`（启动期 fail-fast）。
func TestSetPurposesAndJWKS(t *testing.T) {
	kw := testKeywrap(t)
	oidc, _ := GenRow(nil, kw, "oidc-v1", PurposeOIDC, AlgSM3WithSM2)
	gw, _ := GenRow(nil, kw, "gw-v1", PurposeGateway, AlgSM2)
	set, err := Load(kw, []Row{oidc, gw})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !set.HasPurpose(PurposeOIDC) || !set.HasPurpose(PurposeGateway) {
		t.Fatal("两个用途都应可用")
	}
	jwks := set.JWKS()
	if len(jwks) != 2 {
		t.Fatalf("JWKS 应有 2 项，实际 %d", len(jwks))
	}
	kids := map[string]bool{}
	for _, k := range jwks {
		if k.Kty != "EC" || k.Crv != "SM2" || k.Use != "sig" {
			t.Fatalf("JWK 字段不符：%+v", k)
		}
		if len(k.X) == 0 || len(k.Y) == 0 {
			t.Fatalf("JWK 缺坐标：%+v", k)
		}
		kids[k.Kid] = true
	}
	if !kids["oidc-v1"] || !kids["gw-v1"] {
		t.Fatalf("iki 不可区分：%v", kids)
	}

	empty, _ := Load(kw, nil)
	if _, err := empty.For(PurposeOIDC); !errors.Is(err, ErrNoActiveKey) {
		t.Fatalf("缺密钥应报 ErrNoActiveKey，实际 %v", err)
	}
	if empty.HasPurpose(PurposeOIDC) {
		t.Fatal("空集合不该报有密钥")
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func flip(s string) string {
	if s == "" {
		return "A"
	}
	if s[0] == 'A' {
		return "B" + s[1:]
	}
	return "A" + s[1:]
}
