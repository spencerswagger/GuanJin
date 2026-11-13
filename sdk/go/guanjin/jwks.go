package guanjin

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/tjfoc/gmsm/sm2"
)

// JWK 是平台 JWKS 里的一条公钥（契约：`kty:"EC"` + `crv:"SM2"`，索引 §4.2-23）。
// 平台把两类密钥同时发布，`use` 都是 `sig`，靠 `alg` 区分：对外令牌=`SM3withSM2`、
// 网关断言=`SM2`（`decisions.md` D29）——`alg` 就是"网关用途"的判据。
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Use string `json:"use,omitempty"`
	Alg string `json:"alg,omitempty"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type jwksDoc struct {
	Keys []JWK `json:"keys"`
}

// JwksClient 是 JWKS 拉取与缓存（能力 GJ-CAP-02 / GJ-CAP-03 / GJ-CAP-04）。
//
// fail-closed 口径（契约 §4.4）：拉取失败但**有缓存**→沿用缓存继续真实验签（公钥公开，
// 不构成放行降级）；拉取失败且**无缓存**→ 503 `KEY_UNAVAILABLE`；未知 `kid` 且开了
// 强刷→刷一次，仍无该 `kid` → 401 `TOKEN_INVALID`（无匹配公钥）。
type JwksClient struct {
	uri          string
	httpc        *http.Client
	cacheTTL     time.Duration
	forceRefresh bool

	mu       sync.RWMutex
	keys     map[string]*sm2.PublicKey
	loadedAt time.Time
}

// NewJwksClient 构造 JWKS 客户端；httpc 为 nil 时用配置里的超时构造默认客户端。
func NewJwksClient(cfg Config, httpc *http.Client) *JwksClient {
	if httpc == nil {
		httpc = &http.Client{Timeout: cfg.Platform.ReadTimeout}
	}
	return &JwksClient{
		uri:          cfg.JwksURI(),
		httpc:        httpc,
		cacheTTL:     cfg.Gateway.JwksCacheTTL,
		forceRefresh: cfg.Gateway.JwksUnknownKidForceRefresh,
		keys:         map[string]*sm2.PublicKey{},
	}
}

// PublicKey 取 `kid` 对应公钥（能力 GJ-CAP-02）：缓存命中且未过期直接用；否则刷新一次。
// 未知 `kid` 时按配置强刷一次（GJ-CAP-04）。
func (j *JwksClient) PublicKey(kid string) (*sm2.PublicKey, error) {
	if pub, ok := j.lookup(kid); ok && !j.stale() {
		return pub, nil
	}
	// 缓存过期或未命中 → 刷新一次；刷新失败但已有该 kid 的缓存 → 沿用（非降级）。
	if err := j.Refresh(); err != nil {
		if pub, ok := j.lookup(kid); ok {
			return pub, nil
		}
		return nil, NewKeyUnavailable("JWKS 不可用且无本地缓存", err)
	}
	if pub, ok := j.lookup(kid); ok {
		return pub, nil
	}
	if j.forceRefresh {
		if err := j.Refresh(); err == nil {
			if pub, ok := j.lookup(kid); ok {
				return pub, nil
			}
		}
	}
	return nil, NewTokenInvalid(fmt.Sprintf("未找到可用验签公钥 kid=%q", kid), nil)
}

// KeyCount 返回当前缓存里的**网关用途**公钥数量（启动自检用来判断"JWKS 里一个能验断言
// 的键都没有"；oidc（`SM3withSM2`）键在 ParseJWKS 里已被滤掉，见 `decisions.md` D29）。
func (j *JwksClient) KeyCount() int {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return len(j.keys)
}

func (j *JwksClient) lookup(kid string) (*sm2.PublicKey, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	p, ok := j.keys[kid]
	return p, ok
}

func (j *JwksClient) stale() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.loadedAt.IsZero() || time.Since(j.loadedAt) > j.cacheTTL
}

// Refresh 拉取一次 JWKS 并合并进缓存（能力 GJ-CAP-03）。
func (j *JwksClient) Refresh() error {
	req, err := http.NewRequest(http.MethodGet, j.uri, nil)
	if err != nil {
		return err
	}
	resp, err := j.httpc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks: 状态码 %d（应为 200）", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	parsed, err := ParseJWKS(body)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for k, v := range parsed {
		j.keys[k] = v
	}
	j.loadedAt = time.Now()
	return nil
}

// ParseJWKS 解析 JWKS 文档为 `kid → 公钥`（只接受 `kty=EC` / `crv=SM2` / **`alg=SM2`**
// 三条同时成立——`alg` 即"网关用途"判据：平台 `signing.Set.JWKS()` 把 oidc（`SM3withSM2`）
// 与 gateway（`SM2`）两类键同时发布、`use` 都是 `sig`，而断言验签只认 SM2（`decisions.md`
// D29）；oidc 键收进来只会制造"能选到却必然验不过"的 401 噪声）。
// 不满足条件的键**跳过而不报错**：平台将来若同时发布别类公钥/算法，SDK 不该因此起不来。
func ParseJWKS(body []byte) (map[string]*sm2.PublicKey, error) {
	var doc jwksDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("jwks: 非法 JSON: %w", err)
	}
	out := make(map[string]*sm2.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "EC" || k.Crv != "SM2" || k.Alg != SigningAlg || k.Kid == "" {
			continue
		}
		pub, err := parseSM2JWK(k)
		if err != nil {
			return nil, err
		}
		out[k.Kid] = pub
	}
	return out, nil
}

func parseSM2JWK(k JWK) (*sm2.PublicKey, error) {
	xb, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("jwks: kid=%q 的 x 不是 base64url: %w", k.Kid, err)
	}
	yb, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, fmt.Errorf("jwks: kid=%q 的 y 不是 base64url: %w", k.Kid, err)
	}
	if len(xb) != 32 || len(yb) != 32 {
		return nil, fmt.Errorf("jwks: kid=%q 坐标必须各 32 字节（SM2），得到 %d/%d", k.Kid, len(xb), len(yb))
	}
	return &sm2.PublicKey{
		Curve: sm2.P256Sm2(),
		X:     new(big.Int).SetBytes(xb),
		Y:     new(big.Int).SetBytes(yb),
	}, nil
}
