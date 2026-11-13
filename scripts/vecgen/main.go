// 生成 spec/vectors/{agent-hmac,assertion-verify}.json（config-mapping.json 也在这里生成）。
//
// 用法：go run ./scripts/vecgen   （在仓库根目录跑；会**重写** spec/vectors 下三份文件）
//
// 口径：canonical 串走真实现 internal/agent，SM2 签名走 internal/crypto.SignSM2（与平台
// 签发路径 signing.SignJWT 同一套 r||s + base64url 形态）；HMAC-SM3 用 gmsm 独立实现
// （生产侧目前没有 Agent HMAC 实现，crypto 的 HMACSM3WithUserSecret 明确禁止用于 Agent HMAC）。
//
// 注意：SM2 密钥每次运行随机生成 → 重跑会换键、旧 token 全部失效（测试会跟着新向量跑，属预期）。
// 改向量规则时**先改这里再重跑**，不要手改 spec/vectors/*.json。
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"sort"
	"time"

	"github.com/tjfoc/gmsm/sm2"
	"github.com/tjfoc/gmsm/sm3"

	"github.com/spencerswagger/guanjin/internal/agent"
	"github.com/spencerswagger/guanjin/internal/crypto"
)

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func b64(b []byte) string  { return base64.StdEncoding.EncodeToString(b) }

func hmacSM3(key, msg []byte) []byte {
	m := hmac.New(sm3.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// ---------- agent-hmac ----------

type hmacVec struct {
	VectorID            string      `json:"vector_id"`
	Case                string      `json:"case"`
	PendingSpec         bool        `json:"pending_spec,omitempty"`
	Method              string      `json:"method"`
	Path                string      `json:"path"`
	QueryRaw            string      `json:"query_raw"`
	QueryPairs          [][2]string `json:"query_pairs"`
	Timestamp           string      `json:"timestamp"`
	Nonce               string      `json:"nonce"`
	BodyUTF8            string      `json:"body_utf8"`
	ExpectedSortedQuery string      `json:"expected_sorted_query"`
	ExpectedBodySM3     string      `json:"expected_body_sm3"`
	ExpectedCanonical   string      `json:"expected_canonical"`
	ExpectedSignature   string      `json:"expected_signature"`
	Note                string      `json:"note,omitempty"`
}

func main() {
	secret := []byte("guanjin-agent-hmac-vec-secret-32") // 32 字节
	if len(secret) != 32 {
		panic("secret 长度必须 32")
	}
	ts := "1767225600"
	nonce := b64u([]byte("nonce-vector-0001")) // 17 字节，base64url 无填充

	type c struct {
		id, name, method, path, raw string
		pairs                       [][2]string
		body                        string
		note                        string
	}
	cases := []c{
		{"AH-01", "empty_query_empty_body", "GET", "/api/v1/agent/policies", "", nil, "", "空 query + 空 body：两段都不得省略"},
		{"AH-02", "single_query", "GET", "/api/v1/agent/policies", "since=12", [][2]string{{"since", "12"}}, "", ""},
		{"AH-03", "multi_query_out_of_order", "GET", "/api/v1/agent/policies", "c=3&a=1&b=2", [][2]string{{"c", "3"}, {"a", "1"}, {"b", "2"}}, "", "乱序 → 按编码后 name 字节序"},
		{"AH-04", "duplicate_name_diff_value", "GET", "/api/v1/agent/policies", "tag=b&tag=a", [][2]string{{"tag", "b"}, {"tag", "a"}}, "", "重复键保留，name 相同再比 value（两级排序）"},
		{"AH-05", "duplicate_name_same_value", "GET", "/api/v1/agent/policies", "x=1&x=1", [][2]string{{"x", "1"}, {"x", "1"}}, "", "同名同值：两条都留，顺序无关"},
		{"AH-06", "empty_query_after_question", "GET", "/api/v1/agent/policies", "?", nil, "", "`?` 后为空 → 空串（不是 `?`）"},
		{"AH-07", "post_empty_body", "POST", "/api/v1/agent/report", "", nil, "", "空 body 也要 SM3(\"\") 段"},
		{"AH-08", "utf8_body", "POST", "/api/v1/agent/report", "", nil, `{"note":"国密 SM3 摘要"}`, "body 按 UTF-8 字节取 SM3 hex"},
		{"AH-09", "context_path_included", "POST", "/order/api/v1/agent/report", "", nil, `{"version":7}`, "D1-A1：PATH 含 context-path，不得剥前缀"},
		{"AH-10", "path_no_trailing_slash", "GET", "/a", "", nil, "", "`/a` 与 `/a/` 是不同 PATH（本条 = 不带斜杠）"},
		{"AH-11", "path_trailing_slash", "GET", "/a/", "", nil, "", "`/a/` 必须与 `/a` 算出不同签名"},
		{"AH-12", "rfc3986_encoding_pending_spec", "GET", "/api/v1/agent/policies", "q=a b%2Bc/中&n=~-._", [][2]string{{"q", "a b+c/中"}, {"n", "~-._"}}, "", "**PENDING（消费者跳过）**：契约 A2 字面是「取 ? 之后原始串 → 编码在比较之前」且 A1 明写「不做 URL 解码」⇒ raw→再编码（%2B→%252B）；本向量取先解码再编码。口径未定前不参与断言，已提请 Spec 补 A2 一句"},
		{"AH-13", "case_sensitive_byte_order", "GET", "/api/v1/agent/policies", "B=2&a=1&A=3&b=4", [][2]string{{"B", "2"}, {"a", "1"}, {"A", "3"}, {"b", "4"}}, "", "字节序比较（大写字母在小写之前），不做大小写折叠"},
	}

	vectors := make([]hmacVec, 0, len(cases))
	for _, cs := range cases {
		pairs := make([]agent.KV, 0, len(cs.pairs))
		for _, p := range cs.pairs {
			pairs = append(pairs, agent.KV{Name: p[0], Value: p[1]})
		}
		sq := agent.SortedQuery(pairs)
		sbody := crypto.SM3Hex([]byte(cs.body))
		canon := agent.Canonical(cs.method, cs.path, sq, sbody, ts, nonce)
		sig := b64(hmacSM3(secret, []byte(canon)))
		vectors = append(vectors, hmacVec{
			VectorID: cs.id, Case: cs.name, Method: cs.method, Path: cs.path,
			QueryRaw: cs.raw, QueryPairs: cs.pairs, Timestamp: ts, Nonce: nonce,
			PendingSpec: cs.name == "rfc3986_encoding_pending_spec",
			BodyUTF8:    cs.body, ExpectedSortedQuery: sq, ExpectedBodySM3: sbody,
			ExpectedCanonical: canon, ExpectedSignature: sig, Note: cs.note,
		})
	}
	out := map[string]any{
		"spec":             "docs/sdk-contract.md §4.3 / decisions.md D1（canonical 逐字 FROZEN）",
		"algorithm":        "X-GuanJin-Signature = Base64(HMAC-SM3(K, canonical))；K = base64 解码后的 32 字节",
		"generated_by":     "Gate 2026-11-13：canonical 段用真实现 internal/agent（Canonical/SortedQuery），HMAC-SM3 用独立实现（gmsm）",
		"secret_b64":       b64(secret),
		"query_pairs_note": "query_pairs = 请求行 query 按 & / 首个 = 切分后的键值对（本文件取**解码后**形态）；query_raw 只记录来源形态、不参与断言。契约 A2 未明写「解码」这一步 → AH-12 标 pending_spec=true 由消费者跳过，待 Spec 补一句后转正",
		"vectors":          vectors,
	}
	write("spec/vectors/agent-hmac.json", out)
	genAssertion()
	genConfig()
}

// ---------- assertion-verify ----------

type avVec struct {
	VectorID string         `json:"vector_id"`
	Case     string         `json:"case"`
	Token    string         `json:"token,omitempty"`
	JwksMode string         `json:"jwks_mode"` // available | unavailable
	Expect   map[string]any `json:"expect"`
	Replay   int            `json:"replay_times,omitempty"`
	Note     string         `json:"note,omitempty"`
}

func genAssertion() {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	const skew = 60

	gwPriv, _ := crypto.GenerateSM2Key(rand.Reader)
	oidcPriv, _ := crypto.GenerateSM2Key(rand.Reader)
	roguePriv, _ := crypto.GenerateSM2Key(rand.Reader)

	jwks := map[string]any{"keys": []map[string]any{
		jwk("gw-2026-11", "SM2", &gwPriv.PublicKey),
		jwk("oidc-2026-11", "SM3withSM2", &oidcPriv.PublicKey),
	}}

	jti := func(s string) string { return s }
	claims := func(over map[string]any) map[string]any {
		base := map[string]any{
			"iss": "guanjin-gateway", "aud": "orders", "cid": "orders",
			"sub": "user-1", "uname": "alice", "roles": []string{"user"},
			"acr": "AAL2", "amr": []string{"pwd"},
			"iat": now - 10, "exp": now + 50,
			"jti": jti("11111111-2222-4333-8444-555555555555"),
		}
		for k, v := range over {
			if v == nil {
				delete(base, k)
			} else {
				base[k] = v
			}
		}
		return base
	}
	sign := func(kid, alg string, priv *sm2.PrivateKey, cl map[string]any) string {
		hdr, _ := json.Marshal(map[string]any{"alg": alg, "kid": kid, "typ": "JWT"})
		cb, _ := json.Marshal(cl)
		in := b64u(hdr) + "." + b64u(cb)
		r, s, err := crypto.SignSM2(priv, []byte(in))
		if err != nil {
			panic(err)
		}
		sig := make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		return in + "." + b64u(sig)
	}
	valid := sign("gw-2026-11", "SM2", gwPriv, claims(nil))

	vectors := []avVec{
		{VectorID: "AV-01", Case: "valid", Token: valid, JwksMode: "available",
			Expect: map[string]any{"decision": "allow", "status": 0, "code": "", "stage": ""},
			Note:   "真 SM2 签名（与平台签发路径同形态），claim 全部在窗"},
		{VectorID: "AV-02", Case: "tampered_payload", Token: tamper(valid), JwksMode: "available",
			Expect: map[string]any{"decision": "reject", "status": 401, "code": "TOKEN_INVALID", "stage": "signature"},
			Note:   "签名本身合法但 payload 被换 → 验签失败（不得进 claim 层）"},
		{VectorID: "AV-03", Case: "exp_boundary_inclusive", Token: sign("gw-2026-11", "SM2", gwPriv, claims(map[string]any{"exp": now - skew})), JwksMode: "available",
			Expect: map[string]any{"decision": "allow", "status": 0, "code": "", "stage": ""},
			Note:   "exp = now - skew：边界内放行（`exp + skew` 两侧各测）"},
		{VectorID: "AV-04", Case: "exp_boundary_plus_one", Token: sign("gw-2026-11", "SM2", gwPriv, claims(map[string]any{"exp": now - skew - 1})), JwksMode: "available",
			Expect: map[string]any{"decision": "reject", "status": 401, "code": "TOKEN_INVALID", "stage": "claims"},
			Note:   "exp = now - skew - 1：超窗 1 秒即拒"},
		{VectorID: "AV-05", Case: "iat_boundary_inclusive", Token: sign("gw-2026-11", "SM2", gwPriv, claims(map[string]any{"iat": now + skew})), JwksMode: "available",
			Expect: map[string]any{"decision": "allow", "status": 0, "code": "", "stage": ""},
			Note:   "iat = now + skew：未来侧边界内放行"},
		{VectorID: "AV-06", Case: "iat_boundary_plus_one", Token: sign("gw-2026-11", "SM2", gwPriv, claims(map[string]any{"iat": now + skew + 1})), JwksMode: "available",
			Expect: map[string]any{"decision": "reject", "status": 401, "code": "TOKEN_INVALID", "stage": "claims"},
			Note:   "iat = now + skew + 1：未来过远即拒"},
		{VectorID: "AV-07", Case: "iss_mismatch", Token: sign("gw-2026-11", "SM2", gwPriv, claims(map[string]any{"iss": "evil"})), JwksMode: "available",
			Expect: map[string]any{"decision": "reject", "status": 401, "code": "TOKEN_INVALID", "stage": "claims"}},
		{VectorID: "AV-08", Case: "aud_mismatch", Token: sign("gw-2026-11", "SM2", gwPriv, claims(map[string]any{"aud": "other"})), JwksMode: "available",
			Expect: map[string]any{"decision": "reject", "status": 401, "code": "TOKEN_INVALID", "stage": "claims"}},
		{VectorID: "AV-09", Case: "unknown_kid", Token: sign("gw-rogue", "SM2", roguePriv, claims(nil)), JwksMode: "available",
			Expect: map[string]any{"decision": "reject", "status": 401, "code": "TOKEN_INVALID", "stage": "jwks"},
			Note:   "kid 不在 JWKS：强刷一次仍无 → 401（不是 503，JWKS 可达）"},
		{VectorID: "AV-10", Case: "jwks_unavailable_no_cache", Token: valid, JwksMode: "unavailable",
			Expect: map[string]any{"decision": "reject", "status": 503, "code": "KEY_UNAVAILABLE", "stage": "jwks"},
			Note:   "JWKS 不可达且无缓存 → 503 KEY_UNAVAILABLE（fail-closed，不得放行）"},
		{VectorID: "AV-11", Case: "jti_replay", Token: valid, JwksMode: "available", Replay: 2,
			Expect: map[string]any{"decision": "reject", "status": 401, "code": "TOKEN_INVALID", "stage": "replay"},
			Note:   "同一 jti 第二次必拒（首次 allow）"},
		{VectorID: "AV-12", Case: "alg_not_sm2_oidc_track", Token: sign("oidc-2026-11", "SM3withSM2", oidcPriv, claims(nil)), JwksMode: "available",
			Expect: map[string]any{"decision": "reject", "status": 401, "code": "TOKEN_INVALID", "stage": "alg", "reason_contains": "alg"},
			Note:   "oidc 轨（alg=SM3withSM2）的键签的断言必须拒；注意 SDK 的 ParseJWKS 本来就会滤掉 oidc 键，所以**这条不能单独证明「拒的是 alg」**——判别力由 AV-14 提供"},
		{VectorID: "AV-14", Case: "alg_sm3withsm2_gateway_key", Token: sign("gw-2026-11", "SM3withSM2", gwPriv, claims(nil)), JwksMode: "available",
			Expect: map[string]any{"decision": "reject", "status": 401, "code": "TOKEN_INVALID", "stage": "alg", "reason_contains": "alg"},
			Note:   "**D29 的判别性证据**：kid=gw-2026-11（JWKS 里确有这把 SM2 键）+ 头里 alg=SM3withSM2 + 用**网关私钥真签**（SM3withSM2 与 SM2 是同一段字节的签名，去掉 alg 闸这种断言会被接受）。摘掉 alg 闸 → 本条与 AV-13 必红"},
		{VectorID: "AV-13", Case: "alg_hs256_downgrade", Token: signHS256("gw-2026-11", claims(nil)), JwksMode: "available",
			Expect: map[string]any{"decision": "reject", "status": 401, "code": "TOKEN_INVALID", "stage": "alg", "reason_contains": "alg"},
			Note:   "经典 alg 降级（HS256）也必须拒——不做算法协商；摘掉 alg 闸时本条会落到「签名必须 64 字节/验签失败」而不是「alg」，故 reason_contains 也能抓到"},
	}
	out := map[string]any{
		"spec":               "docs/sdk-contract.md §4.4/§4.5 + decisions.md D29（网关断言 alg=SM2 唯一；对外令牌 SM3withSM2 是另一条轨）",
		"generated_by":       "Gate 2026-11-13：SM2 真签名（internal/crypto.SignSM2，与 signing.SignJWT 同形态）",
		"clock_skew_seconds": skew,
		"reference_now":      time.Unix(now, 0).UTC().Format(time.RFC3339),
		"reference_now_unix": now,
		"jwks":               jwks,
		"jwks_note":          "JWKS 同时含 gateway（alg=SM2）与 oidc（alg=SM3withSM2）两把键；判别「拒的是 alg」靠 AV-14（网关 kid + alg=SM3withSM2 + 网关私钥真签），AV-12 只是 oidc 轨的补充面",
		"oidc_track_note":    "对外令牌（id_token/access_token）仍用 SM3withSM2 由平台签发/校验（signing 包），**不属本向量面**；本文件只覆盖网关内部断言，故不存在『oidc 令牌通过』向量",
		"vectors":            vectors,
	}
	write("spec/vectors/assertion-verify.json", out)
}

func jwk(kid, alg string, pub *sm2.PublicKey) map[string]any {
	x := pub.X.FillBytes(make([]byte, 32))
	y := pub.Y.FillBytes(make([]byte, 32))
	return map[string]any{"kty": "EC", "crv": "SM2", "use": "sig", "kid": kid, "alg": alg,
		"x": b64u(x), "y": b64u(y)}
}

func tamper(tok string) string {
	parts := split3(tok)
	hdr, _ := base64.RawURLEncoding.DecodeString(parts[0])
	_ = hdr
	// 换成一个签名不匹配的 payload：保留 iss/aud/exp 合法，只改 sub
	pb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var cl map[string]any
	if err := json.Unmarshal(pb, &cl); err != nil {
		panic(err)
	}
	cl["sub"] = "attacker"
	nb, _ := json.Marshal(cl)
	return parts[0] + "." + b64u(nb) + "." + parts[2]
}

func split3(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '.' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, cur)
	return out
}

func signHS256(kid string, cl map[string]any) string {
	hdr := map[string]any{"alg": "HS256", "kid": kid, "typ": "JWT"}
	hb, _ := json.Marshal(hdr)
	cb, _ := json.Marshal(cl)
	in := b64u(hb) + "." + b64u(cb)
	m := hmac.New(sm3.New, []byte("attacker-guess"))
	m.Write([]byte(in))
	return in + "." + b64u(m.Sum(nil))
}

func write(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		panic(err)
	}
	fmt.Printf("wrote %s (%d bytes)\n", path, len(b))
	_ = sort.Strings
}

var _ = hex.EncodeToString
var _ = big.NewInt
