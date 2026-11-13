package guanjin

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tjfoc/gmsm/sm2"
)

func testConfig(t *testing.T, srvURL string) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Platform.BaseURL = srvURL
	cfg.Client.ID = "order-service"
	cfg.Client.HMACSecret = "test-secret"
	cfg.Authz.ReplayStore = ReplayStoreMemory // 仅测试用（Validate 在 EnvTest 下允许）
	return cfg
}

func genKey(t *testing.T) *sm2.PrivateKey {
	t.Helper()
	priv, err := sm2.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成 SM2 私钥失败: %v", err)
	}
	return priv
}

func coord(b *big.Int) string {
	buf := make([]byte, 32)
	b.FillBytes(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// jwksBody 生成 JWKS 文档（kty=EC / crv=SM2 / alg=SM2（网关用途）/ use=sig，坐标 base64url 定长 32 字节）。
func jwksBody(kid string, pubs ...*sm2.PublicKey) []byte {
	type jwk struct {
		Kty string `json:"kty"`
		Crv string `json:"crv"`
		Kid string `json:"kid"`
		Use string `json:"use"`
		Alg string `json:"alg"`
		X   string `json:"x"`
		Y   string `json:"y"`
	}
	doc := struct {
		Keys []jwk `json:"keys"`
	}{}
	for i, p := range pubs {
		k := kid
		if i > 0 {
			k = fmt.Sprintf("%s-%d", kid, i)
		}
		doc.Keys = append(doc.Keys, jwk{Kty: "EC", Crv: "SM2", Kid: k, Use: "sig", Alg: SigningAlg, X: coord(p.X), Y: coord(p.Y)})
	}
	b, _ := json.Marshal(doc)
	return b
}

// signAssertion 生成内部断言（r||s 64 字节，base64url 无填充）。
func signAssertion(t *testing.T, priv *sm2.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	return signAssertionWithAlg(t, priv, kid, SigningAlg, claims)
}

// signAssertionWithAlg 同 signAssertion，但允许指定 header 的 `alg`——**只服务负向用例**
// （旧值/错误 alg 必须被拒，D29 不兼容旧 alg、不协商）；生产路径只有 SigningAlg。
func signAssertionWithAlg(t *testing.T, priv *sm2.PrivateKey, kid, alg string, claims map[string]any) string {
	t.Helper()
	hdr := map[string]any{"alg": alg, "kid": kid, "typ": "JWT"}
	hb, _ := json.Marshal(hdr)
	cb, _ := json.Marshal(claims)
	p1 := base64.RawURLEncoding.EncodeToString(hb)
	p2 := base64.RawURLEncoding.EncodeToString(cb)
	signed := []byte(p1 + "." + p2)
	r, s, err := sm2.Sm2Sign(priv, signed, sm2UID, rand.Reader)
	if err != nil {
		t.Fatalf("签名失败: %v", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return p1 + "." + p2 + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func claims(now int64) map[string]any {
	return map[string]any{
		"iss":   DefaultIssuer,
		"sub":   "u-1",
		"aud":   "order-service",
		"cid":   "web",
		"uname": "alice",
		"roles": []string{"order_admin"},
		"acr":   "AAL2",
		"amr":   []string{"pwd", "otp"},
		"iat":   now,
		"exp":   now + 60,
		"jti":   fmt.Sprintf("jti-%d", now),
	}
}

func jwksServer(t *testing.T, body func() []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body())
	}))
	t.Cleanup(srv.Close)
	return srv
}
