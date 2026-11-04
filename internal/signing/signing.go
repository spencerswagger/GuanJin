// Package signing 是「签名密钥的加载、解封、签发与发布」。
//
// 口径（D15④ / #36 / 索引 §2）：
//   - `signing_keys.private_key_enc` 是 SM4 信封 v2（`0x02|iv|ct|HMAC-SM3`），**先验 MAC 再解密**；
//   - **启动期**解不开（`ErrIntegrityMismatch`/`ErrCiphertextVersion`）→ 调用方非 0 退出并打印 `kid` 与原因；
//   - **禁止启动期自动生成密钥**（会静默换签名键、把既有 token 全废）：生成只走 `guanjind keygen`（幂等）；
//   - 对外令牌 `alg=SM3withSM2`，JWKS 里 `kty=EC / crv=SM2`，两类用途（`oidc` / `gateway`）用 `kid` 区分。
package signing

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/tjfoc/gmsm/sm2"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// 用途（与 `signing_keys.purpose` 的 CHECK 一致）。
const (
	PurposeOIDC    = "oidc"
	PurposeGateway = "gateway"
)

// 算法（与 `signing_keys_purpose_algo` CHECK 一致）。
const (
	AlgSM3WithSM2 = "SM3withSM2"
	AlgSM2        = "SM2"
)

var (
	// ErrNoActiveKey 表示某个用途没有可用密钥（启动期应非 0 退出）。
	ErrNoActiveKey = errors.New("signing: no active key for purpose")
	// ErrKeyUnusable 表示密钥行无法解封/解析（数据或配置坏了 → 启动期非 0 退出）。
	ErrKeyUnusable = errors.New("signing: signing key is unusable")
)

// Row 是 `signing_keys` 的一行（只取签发需要的列）。
type Row struct {
	Kid string
	// Purpose oidc | gateway；Algorithm SM3withSM2 | SM2。
	Purpose    string
	Algorithm  string
	PublicKey  string // base64(SEC1 压缩点 33B)
	PrivateEnc []byte // SM4 信封 v2 密文
}

// Key 是一把可用密钥（已解封）。
type Key struct {
	Kid       string
	Purpose   string
	Algorithm string
	Public    *sm2.PublicKey
	Private   *sm2.PrivateKey
}

// Set 是启动期加载好的一组密钥（多副本只读，运行期不换取）。
type Set struct {
	byPurpose map[string][]Key
}

// Load 解封全部密钥行；任何一行解不开都直接报错（**不跳过**：跳过等于带着坏密钥对外服务）。
func Load(keywrap crypto.SM4Key, rows []Row) (*Set, error) {
	out := &Set{byPurpose: map[string][]Key{}}
	for _, r := range rows {
		k, err := unsealRow(keywrap, r)
		if err != nil {
			// 双 `%w`：外层是本包的"密钥不可用"，内层保留 crypto 的哨兵
			// （`ErrCiphertextVersion` = 存量 v1 未重加密；`ErrIntegrityMismatch` = 可能被改）。
			return nil, fmt.Errorf("%w: kid=%s purpose=%s: %w", ErrKeyUnusable, r.Kid, r.Purpose, err)
		}
		out.byPurpose[r.Purpose] = append(out.byPurpose[r.Purpose], k)
	}
	return out, nil
}

func unsealRow(keywrap crypto.SM4Key, r Row) (Key, error) {
	if keywrap.IsZero() {
		return Key{}, errors.New("keywrap key is not configured")
	}
	pubRaw, err := base64.StdEncoding.DecodeString(r.PublicKey)
	if err != nil {
		return Key{}, fmt.Errorf("public_key is not base64: %w", err)
	}
	pub, err := crypto.UnmarshalSM2PublicKey(pubRaw)
	if err != nil {
		return Key{}, fmt.Errorf("public_key is not a SM2 point: %w", err)
	}
	privRaw, err := crypto.DecryptSM4(keywrap, r.PrivateEnc)
	if err != nil {
		// 保留 crypto 的哨兵（`ErrCiphertextVersion` = 存量未重加密；`ErrIntegrityMismatch` = 可能被改）。
		return Key{}, fmt.Errorf("unseal private key: %w", err)
	}
	priv, err := PrivateFromScalar(privRaw)
	if err != nil {
		return Key{}, err
	}
	return Key{Kid: r.Kid, Purpose: r.Purpose, Algorithm: r.Algorithm, Public: pub, Private: priv}, nil
}

// PrivateFromScalar 由 32 字节私钥标量还原 `*sm2.PrivateKey`。
func PrivateFromScalar(raw []byte) (*sm2.PrivateKey, error) {
	if len(raw) != 32 {
		return nil, fmt.Errorf("private key must be 32 bytes, got %d", len(raw))
	}
	curve := sm2.P256Sm2()
	d := new(big.Int).SetBytes(raw)
	if d.Sign() <= 0 || d.Cmp(curve.Params().N) >= 0 {
		return nil, errors.New("private scalar out of range")
	}
	priv := new(sm2.PrivateKey)
	priv.PublicKey.Curve = curve
	priv.D = d
	priv.PublicKey.X, priv.PublicKey.Y = curve.ScalarBaseMult(raw)
	return priv, nil
}

// For 取某用途的第一把可用密钥（启动期已保证每用途至少一把）。
func (s *Set) For(purpose string) (*Key, error) {
	if s == nil {
		return nil, ErrNoActiveKey
	}
	keys := s.byPurpose[purpose]
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoActiveKey, purpose)
	}
	return &keys[0], nil
}

// Purpose 是否存在可用密钥（启动期 fail-fast 用）。
func (s *Set) HasPurpose(purpose string) bool {
	if s == nil {
		return false
	}
	return len(s.byPurpose[purpose]) > 0
}

// GenRow 生成一把新密钥并封好私钥（**只给 CLI 用**；绝不在启动期调用）。
func GenRow(random io.Reader, keywrap crypto.SM4Key, kid, purpose, algorithm string) (Row, error) {
	if random == nil {
		random = rand.Reader
	}
	if keywrap.IsZero() {
		return Row{}, errors.New("keywrap key is not configured")
	}
	if kid == "" {
		return Row{}, errors.New("kid is required")
	}
	priv, err := crypto.GenerateSM2Key(random)
	if err != nil {
		return Row{}, err
	}
	pubRaw, err := crypto.MarshalSM2PublicKey(&priv.PublicKey)
	if err != nil {
		return Row{}, err
	}
	sealed, err := crypto.EncryptSM4(keywrap, priv.D.FillBytes(make([]byte, 32)))
	if err != nil {
		return Row{}, err
	}
	return Row{
		Kid:        kid,
		Purpose:    purpose,
		Algorithm:  algorithm,
		PublicKey:  base64.StdEncoding.EncodeToString(pubRaw),
		PrivateEnc: sealed,
	}, nil
}

// JWK 是 JWKS 里的一项（`kty=EC/crv=SM2`，见接口 §3.1.2）。
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// JWKS 返回全部用途的公钥（两类密钥同时发布，用 `kid` 区分）。
func (s *Set) JWKS() []JWK {
	if s == nil {
		return nil
	}
	out := make([]JWK, 0, 2)
	for _, purpose := range []string{PurposeOIDC, PurposeGateway} {
		for _, k := range s.byPurpose[purpose] {
			out = append(out, JWK{
				Kty: "EC", Crv: "SM2", Kid: k.Kid, Use: "sig", Alg: k.Algorithm,
				X: base64.RawURLEncoding.EncodeToString(k.Public.X.FillBytes(make([]byte, 32))),
				Y: base64.RawURLEncoding.EncodeToString(k.Public.Y.FillBytes(make([]byte, 32))),
			})
		}
	}
	return out
}

// SignJWT 签一个 compact JWS：`alg=SM3withSM2`、`kid` 指向当前密钥。
//
// 自己拼而不用第三方 JWT 库：国密算法不在标准库里，第三方库要么不支持 SM2，
// 要么会把 `alg` 白名单开成可配置（这正是"验签可关/可换算法"的入口）。
func SignJWT(k *Key, claims map[string]any) (string, error) {
	if k == nil || k.Private == nil {
		return "", ErrNoActiveKey
	}
	header := map[string]any{"alg": k.Algorithm, "kid": k.Kid, "typ": "JWT"}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	r, s, err := crypto.SignSM2(k.Private, []byte(signingInput))
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifyJWT 用给定公钥验签并解出 claims（`/userinfo`、`/introspect` 的本地解析入口，零查库）。
//
// **不做 alg 协商**：只接受 `alg=SM3withSM2`，未知 alg 一律拒（防算法降级）。
func VerifyJWT(pub *sm2.PublicKey, token string) (map[string]any, error) {
	if pub == nil {
		return nil, errors.New("signing: nil public key")
	}
	parts := splitDots(token)
	if len(parts) != 3 {
		return nil, errors.New("signing: malformed jwt")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("signing: malformed header")
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(hb, &header); err != nil {
		return nil, errors.New("signing: malformed header")
	}
	if header.Alg != AlgSM3WithSM2 {
		return nil, fmt.Errorf("signing: unsupported alg %q", header.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return nil, errors.New("signing: malformed signature")
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !crypto.VerifySM2(pub, []byte(parts[0]+"."+parts[1]), r, s) {
		return nil, errors.New("signing: bad signature")
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("signing: malformed payload")
	}
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		return nil, errors.New("signing: malformed payload")
	}
	return claims, nil
}

func splitDots(s string) []string {
	out := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
