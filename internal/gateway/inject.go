package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/signing"
)

// 本文件是数据面的**身份注入**（S8 的 ③，AC-DP-01/02/03）：逐请求签 `X-GuanJin-Assertion`。
//
// 契约（decisions.md **D16①**，逐 claim 有出处；接口清单 §5.2）：
//
//	iss   = 常量 `guanjin-gateway`（SDK 侧按此值校验）
//	sub   = 会话归属用户（`users.id`；不换成用户名——用户名可改，`sub` 不可变）
//	aud   = **路由命中的 `client_id`**（目标业务系统）
//	cid   = 发起 client（本次取会话的 `client_id`；正常路径下 `cid == aud`）
//	uname = `users.username` 的规范化存储值（**不取 `display_name`**：断言是凭据，不是展示面）
//	roles = `role_bindings` 里 `(user_id, client_id=aud, status=active)` 的角色集合；空集必须是 `[]`
//	acr   = 会话已达成级别（`AAL1|AAL2`，D12）——**不从令牌取**（撤销/降档以库为准）
//	amr   = 会话因子集合（空数组照发 `[]`）
//	iat   = 本次请求时刻；**exp = iat + 60**（60s 是防重放窗口，不是会话生命周期）
//	jti   = 每请求新建 UUIDv7（与应用主键同一生成器）；平台**不存、不查、不比对**（重放判在 SDK 侧）
//
// 三条不可协商的实现纪律：
//  1. **同一请求只签一次**：`iat/exp/jti` 一次生成、整条链路复用（重试转发不得重签——
//     重签会换 `jti`，等于把防重放窗口清零）。
//  2. **空集合发 `[]` 不发 `null`**：`nil` slice 经 JSON 会变成 `null`，SDK 侧按数组解就会炸
//     （`roles` 空集是常态：用户没绑任何角色）。本文件用非 nil 切片兜住。
//  3. **断言绝不进日志**（AC-DP-10）：本文件不打印 token；访问日志只记
//     `trace_id/method/path/status/duration_ms/client_id`。

// AssertionIssuer 是断言里的 `iss` 常量（接口 §5.2 冻结值；**不得**改成域名或 issuer 基址）。
const AssertionIssuer = "guanjin-gateway"

// AssertionTTL 是断言有效期（索引 §4.2-23 / 接口 §5.2：`exp = iat + 60`）。
const AssertionTTL = 60 * time.Second

// ErrNoSigner 表示没有可用的网关签名密钥（启动期就该拦住）。
var ErrNoSigner = errors.New("gateway: no active signing key for purpose gateway")

// AssertionSigner 用 `purpose='gateway'` 的密钥逐请求签断言。
//
// 为什么不复用用户令牌的密钥：两类用途分钥（D4-B），混用会让"验用户令牌"与"验网关断言"
// 共用一把钥匙，轮换时必然互相拖累；JWKS 里靠 `kid` 区分。
type AssertionSigner struct {
	// Keys 是启动期解封好的密钥集（只读，运行期不换取）。
	Keys *signing.Set
	// Now 为 nil 时用 time.Now。
	Now func() time.Time
	// NewJTI 为 nil 时用 `uuid.NewV7`（与应用主键同一生成器）。
	NewJTI func() (string, error)
}

// NewAssertionSigner 校验密钥集里确有 `gateway` 用途的活动密钥（缺了就别起数据面）。
func NewAssertionSigner(keys *signing.Set) (*AssertionSigner, error) {
	if keys == nil {
		return nil, ErrNoSigner
	}
	if _, err := keys.For(signing.PurposeGateway); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoSigner, err)
	}
	return &AssertionSigner{Keys: keys}, nil
}

// AssertionInput 是签一份断言所需的**全部**输入（一次给齐，签发过程不回库）。
type AssertionInput struct {
	// Sub / CID / ACR / AMR 来自会话（`Admission.Session`）。
	Sub   string
	CID   string
	ACR   string
	AMR   []string
	Aud   string
	Uname string
	Roles []string
}

// Sign 生成 compact JWS（`alg=SM3withSM2`、带 `kid`）。
//
// 调用方必须**每请求只调一次**（见文件头纪律 1）。
func (s *AssertionSigner) Sign(in AssertionInput) (string, error) {
	if s == nil || s.Keys == nil {
		return "", ErrNoSigner
	}
	if in.Sub == "" || in.Aud == "" {
		return "", errors.New("gateway: assertion requires sub and aud")
	}
	if in.ACR == "" {
		// acr 空 = 会话行有问题（D12(2) 白名单非空）：宁可拒签，也不发一个"没说级别"的断言。
		return "", errors.New("gateway: assertion requires acr")
	}
	key, err := s.Keys.For(signing.PurposeGateway)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoSigner, err)
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	newJTI := s.NewJTI
	if newJTI == nil {
		newJTI = func() (string, error) {
			id, err := uuid.NewV7()
			if err != nil {
				return "", err
			}
			return id.String(), nil
		}
	}
	jti, err := newJTI()
	if err != nil {
		return "", fmt.Errorf("gateway: generate jti: %w", err)
	}

	claims := map[string]any{
		"iss":   AssertionIssuer,
		"sub":   in.Sub,
		"aud":   in.Aud,
		"cid":   in.CID,
		"uname": in.Uname,
		"roles": nonNilStrings(in.Roles),
		"acr":   in.ACR,
		"amr":   nonNilStrings(in.AMR),
		"iat":   now.Unix(),
		"exp":   now.Add(AssertionTTL).Unix(),
		"jti":   jti,
	}
	return signing.SignJWT(key, claims)
}

// Apply 把断言写进请求头（**覆盖**而非追加：下游只应看到网关现签的那一个）。
func Apply(req *http.Request, assertion string) error {
	if req == nil {
		return errors.New("gateway: nil request")
	}
	if assertion == "" {
		return errors.New("gateway: empty assertion")
	}
	// 先删再设：`Header.Set` 本身会覆盖，但显式删一次可以顺手清掉大小写变体
	// （`X-Guanjin-Assertion` 与冻结名 `X-GuanJin-Assertion` 不等价，见 proxy.go 的同一条坑）。
	stripAssertionHeader(req.Header)
	req.Header.Set(AssertionHeader, assertion)
	return nil
}

// AssertionFor 从准入结果装配签发输入。
//
// 单独抽出来是为了让"哪个 claim 从哪来"只有一处（D16① 的表格就是它的注释）——
// 分散在 pipeline 里，第二次改动必然抄错一列。
func AssertionFor(adm *Admission) AssertionInput {
	return AssertionInput{
		Sub:   adm.Session.UserID,
		CID:   adm.Session.ClientID,
		ACR:   adm.Session.ACR,
		AMR:   append([]string(nil), adm.Session.AMR...),
		Aud:   adm.Route.ClientID,
		Uname: adm.Username,
		Roles: sortRoles(adm.Roles),
	}
}

// sortRoles 输出稳定顺序（便于 SDK/测试逐字节比对；roles 的**集合**语义不变）。
func sortRoles(roles []string) []string {
	out := nonNilStrings(roles)
	sort.Strings(out)
	return out
}

// nonNilStrings 保证 JSON 序列化出 `[]` 而不是 `null`（见文件头纪律 2）。
func nonNilStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	return append([]string(nil), in...)
}
