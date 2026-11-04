package guanjin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func encodeSeg(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestVerifyAndValidateClaimsHappyPath(t *testing.T) {
	priv := genKey(t)
	srv := jwksServer(t, func() []byte { return jwksBody("k1", &priv.PublicKey) })
	cfg := testConfig(t, srv.URL)
	v := NewVerifier(cfg, NewJwksClient(cfg, nil))

	raw := signAssertion(t, priv, "k1", claims(time.Now().Unix()))
	c, err := v.Verify(raw)
	if err != nil {
		t.Fatalf("验签应通过：%v", err)
	}
	if err := v.ValidateClaims(c); err != nil {
		t.Fatalf("claim 校验应通过：%v", err)
	}
	p := c.Principal()
	if p.Sub != "u-1" || p.Acr != "AAL2" || !p.HasRole("order_admin") || !p.HasAcr("AAL2") {
		t.Fatalf("身份视图不对：%+v", p)
	}
	if p.HasAcr("AAL3") {
		t.Fatal("AAL2 不应满足 AAL3")
	}
}

func TestVerifyRejectsTamperedAndBadAlg(t *testing.T) {
	priv := genKey(t)
	srv := jwksServer(t, func() []byte { return jwksBody("k1", &priv.PublicKey) })
	cfg := testConfig(t, srv.URL)
	v := NewVerifier(cfg, NewJwksClient(cfg, nil))

	good := signAssertion(t, priv, "k1", claims(time.Now().Unix()))
	// 翻转载荷首字符（签名不变）→ 必须验签失败
	b := []byte(good)
	i := indexByte(b, '.')
	if b[i+1] == 'A' {
		b[i+1] = 'B'
	} else {
		b[i+1] = 'A'
	}
	_, err := v.Verify(string(b))
	var ge *Error
	if !errors.As(err, &ge) || ge.Code != CodeTokenInvalid {
		t.Fatalf("篡改令牌应为 TOKEN_INVALID，实得 %v", err)
	}

	// alg=none → 拒绝（不做算法协商降级）
	hb, _ := json.Marshal(map[string]any{"alg": "none", "kid": "k1"})
	if _, err := v.Verify(encodeSeg(hb) + "." + encodeSeg([]byte(`{"sub":"x"}`)) + "."); err == nil {
		t.Fatal("alg=none 必须拒绝")
	}
	// 缺 kid
	hb2, _ := json.Marshal(map[string]any{"alg": SigningAlg})
	if _, err := v.Verify(encodeSeg(hb2) + "." + encodeSeg([]byte(`{}`)) + "."); err == nil {
		t.Fatal("缺 kid 必须拒绝")
	}
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func TestVerifyWithoutCacheIsKeyUnavailable(t *testing.T) {
	priv := genKey(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	cfg := testConfig(t, bad.URL)
	v := NewVerifier(cfg, NewJwksClient(cfg, nil))
	_, err := v.Verify(signAssertion(t, priv, "k1", claims(time.Now().Unix())))
	var ge *Error
	if !errors.As(err, &ge) || ge.Code != CodeKeyUnavailable {
		t.Fatalf("应为 KEY_UNAVAILABLE（AC-SDK-05 / AC-FC-04），实得 %v", err)
	}
	if ge.Status != http.StatusServiceUnavailable {
		t.Fatalf("KEY_UNAVAILABLE 必须是 503，实得 %d", ge.Status)
	}
}

func TestVerifyUnknownKidForceRefresh(t *testing.T) {
	priv1, priv2 := genKey(t), genKey(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write(jwksBody("k1", &priv1.PublicKey))
			return
		}
		_, _ = w.Write(jwksBody("k2", &priv2.PublicKey))
	}))
	defer srv.Close()
	cfg := testConfig(t, srv.URL)
	v := NewVerifier(cfg, NewJwksClient(cfg, nil))

	if _, err := v.Verify(signAssertion(t, priv2, "k2", claims(time.Now().Unix()))); err != nil {
		t.Fatalf("未知 kid 强刷后应验签通过（GJ-CAP-04）：%v", err)
	}
	if calls < 2 {
		t.Fatalf("应至少拉取两次（未知 kid 强刷），实得 %d", calls)
	}
}

func TestValidateClaimsRejectsBadClaims(t *testing.T) {
	priv := genKey(t)
	srv := jwksServer(t, func() []byte { return jwksBody("k1", &priv.PublicKey) })
	cfg := testConfig(t, srv.URL)
	v := NewVerifier(cfg, NewJwksClient(cfg, nil))
	now := time.Now().Unix()

	cases := map[string]func(m map[string]any){
		"iss 不符":  func(m map[string]any) { m["iss"] = "evil" },
		"aud 不符":  func(m map[string]any) { m["aud"] = "other-service" },
		"已过期":     func(m map[string]any) { m["exp"] = now - 3600 },
		"iat 在未来": func(m map[string]any) { m["iat"] = now + 3600 },
		"缺 jti":   func(m map[string]any) { delete(m, "jti") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := claims(now)
			mutate(m)
			c, err := v.Verify(signAssertion(t, priv, "k1", m))
			if err != nil {
				t.Fatalf("签名本身有效，验签应通过：%v", err)
			}
			if err := v.ValidateClaims(c); err == nil {
				t.Fatal("claim 校验必须失败")
			}
		})
	}
}

func TestParseJWKSSkipsForeignKeys(t *testing.T) {
	priv := genKey(t)
	body := []byte(`{"keys":[{"kty":"RSA","kid":"rsa1"},{"kty":"EC","crv":"SM2","kid":"k1","x":"` +
		coord(priv.X) + `","y":"` + coord(priv.Y) + `"}]}`)
	got, err := ParseJWKS(body)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(got) != 1 || got["k1"] == nil {
		t.Fatalf("应只保留 EC/SM2 一条，实得 %d 条", len(got))
	}
}

func TestParseJWKSRejectsBadCoordinateLength(t *testing.T) {
	body := []byte(`{"keys":[{"kty":"EC","crv":"SM2","kid":"k1","x":"AAAA","y":"AAAA"}]}`)
	if _, err := ParseJWKS(body); err == nil {
		t.Fatal("坐标长度不是 32 字节必须报错（不静默）")
	}
}
