package guanjin

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
)

// SDK 是最小接入实例：**验签 → 读身份 → 剥离 → 公开路径**（S9 最小集）。
//
// 未实现的能力（判定 `GJ-CAP-12..18`、上报 `GJ-CAP-20`、拉取 `GJ-CAP-21`）在
// `Capabilities()` 里显式标 `Implemented=false`，调用返回 ErrNotImplemented。
type SDK struct {
	cfg      Config
	env      Env
	jwks     *JwksClient
	verifier *Verifier
	replay   ReplayGuard
	matcher  *PathMatcher
	logf     func(format string, args ...any)
}

// Option 是构造选项（只影响注入，不改配置语义）。
type Option func(*options)

type options struct {
	httpc  *http.Client
	replay ReplayGuard
	env    Env
	logf   func(format string, args ...any)
}

// WithHTTPClient 注入 HTTP 客户端（测试/自定义 TLS 用）。
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.httpc = c } }

// WithReplayGuard 注入 `jti` 存储实现（生产传 Redis 适配；不传则按配置构造）。
func WithReplayGuard(g ReplayGuard) Option { return func(o *options) { o.replay = g } }

// WithEnv 注入运行时环境（缺省读 GUANJIN_ENV，空值按 prod）。
func WithEnv(e Env) Option { return func(o *options) { o.env = e } }

// WithLogger 注入日志函数（默认标准库 log）。
func WithLogger(f func(format string, args ...any)) Option { return func(o *options) { o.logf = f } }

// New 构造 SDK：**先 Validate（fail-fast）再建内部件**，最后做启动自检
// （能力 GJ-CAP-22：打印放行清单，使"哪些接口对外"始终可见）。
//
// 任何配置错误都返回 error，调用方必须当启动失败处理——**不得忽略 err 继续跑**。
func New(cfg Config, opts ...Option) (*SDK, error) {
	o := options{env: EnvFromEnv(), logf: log.Printf}
	for _, fn := range opts {
		fn(&o)
	}
	if err := cfg.Validate(o.env); err != nil {
		return nil, err
	}
	s := &SDK{cfg: cfg, env: o.env, logf: o.logf}
	s.jwks = NewJwksClient(cfg, o.httpc)
	s.verifier = NewVerifier(cfg, s.jwks)
	s.matcher = NewPathMatcher(cfg)
	// 启动自检（GJ-CAP-22 / 契约 §5.1 构造步）：**校验必填项 → 拉 JWKS → 打印放行清单**。
	// JWKS 拉不到（404/500/超时）即**启动失败**（AC-SDK-03：404 也是失败，不是"先跑起来再说"）——
	// 与 Java `GuanjinStartupChecker.check()` 的 `jwks.refresh()` 同口径。
	// 注意：这里失败是"启动失败"；平台在**运行期**才不可达时仍走请求路径的 503 `KEY_UNAVAILABLE`
	// （AC-FC-04 / AC-SDK-05），两者不互相替代。
	if err := s.jwks.Refresh(); err != nil {
		// 统一成 KEY_UNAVAILABLE（与运行期同码、503），`details` 里带病因（连接被拒 / 404 / 500），
		// 供 AC-FC-04 的「把『密钥不可用』与『平台不可达』区分开」断言。
		return nil, NewKeyUnavailable("启动自检：拉取 JWKS 失败", err)
	}
	// 与 Java `GuanjinStartupChecker.check()` 同口径：拉到了但**没有网关用途的 SM2 公钥**只告警
	// （部署能起，但任何断言都会 401）——这里不升级为失败，否则与 Java 语义分叉（契约 §4.1 M2/M6）。
	if n := s.jwks.KeyCount(); n == 0 {
		s.logf("[guanjin] 警告：JWKS 里没有任何 SM2 公钥（kid 集合为空）——部署能起，但所有断言都会 401；"+
			"检查平台 signing_keys 的 purpose=gateway（%s）", cfg.JwksURI())
	}
	if o.replay != nil {
		s.replay = o.replay
	} else {
		g, err := NewReplayGuard(cfg, nil)
		if err != nil {
			// 配置为 redis 但没注入实现：本地/测试环境允许显式降级为内存实现**仅当**配置是 memory；
			// 否则一律失败（不静默降级，见契约 §3.5）。
			return nil, err
		}
		s.replay = g
	}
	if cfg.Startup.PrintPublicPaths {
		s.logf("[guanjin] v%s（契约 %s）env=%s 公开路径=%v（deny-by-default：未列出的路径一律要求身份）",
			SDKVersion, ContractVersion, s.env, s.matcher.Patterns())
	}
	return s, nil
}

// Config 返回生效配置（只读副本）。
func (s *SDK) Config() Config { return s.cfg }

// JwksClient 暴露 JWKS 客户端（主要用于启动期预热与测试）。
func (s *SDK) JwksClient() *JwksClient { return s.jwks }

// Middleware 是 `net/http` 中间件，**必须包在 router 外层**（见 sdk/README.md）。
//
// 剥离按 D10 六步时序（唯一口径，禁止换序）：
// ① 读原始头 → ② 验签 → ③ 校验 claim（`exp`/`iat`/`iss`/`aud`/`jti`）→ ④ 注入上下文 →
// ⑤ **替换请求视图**（新 `*http.Request`，`Header` 重建、断言头已消失）→ ⑥ handler。
// 因此 handler、业务转发、日志三类消费者**都取不到**该头；而验签发生在剥离之前，
// 篡改令牌仍 401（不得"没令牌可验"）。
func (s *SDK) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.matcher.IsPublic(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !s.cfg.Enabled {
			// C-1：非 local|test 已在 Validate 阶段拒绝；能走到这里只可能 local|test。
			next.ServeHTTP(w, r)
			return
		}

		// ① 读原始头（此时**不删**——它是唯一凭证来源）
		raw := r.Header.Get(s.cfg.Gateway.AssertionHeader)

		// ② 验签
		claims, err := s.verifier.Verify(r.Context(), raw)
		if err != nil {
			s.reject(w, r, err)
			return
		}
		// ③ 校验 claim（含 exp/iat/iss/aud）与 jti 重放
		if err := s.verifier.ValidateClaims(claims); err != nil {
			s.reject(w, r, err)
			return
		}
		ok, err := s.replay.FirstUse(r.Context(), claims.Jti, claims.ExpiresAt())
		if err != nil {
			// 存储不可用 = 无法确认是否重放 → fail-closed 503（不降级放行）。
			var ge *Error
			if errors.As(err, &ge) {
				s.reject(w, r, ge)
			} else {
				s.reject(w, r, NewReplayUnavailable("jti 存储不可用", err))
			}
			return
		}
		if !ok {
			s.reject(w, r, NewTokenInvalid("断言重放（jti 已在窗口内使用）", nil))
			return
		}

		// ④ 注入上下文
		p := claims.Principal()
		// ⑤ 替换请求视图：新 request + 重建 Header（断言头不再存在）
		clean := replaceView(r, s.cfg.Gateway.AssertionHeader).
			WithContext(WithPrincipal(r.Context(), p))

		// ⑥ handler
		next.ServeHTTP(w, clean)
	})
}

// StripHeader 是 GJ-CAP-08 的**对外面**（契约 §2.1 Go 列 `stripHeader(r, name)`，大小写按 Go
// 导出惯例）：返回**新的** `*http.Request`——头表重建、指定头（大小写不敏感）去掉；同时把
// 原请求上的该头也删掉（防御性：外层日志中间件若在之后读取原 `r`，同样拿不到）。
// 业务方/网关适配层**不得**拿它替代 Middleware（D10：剥离发生在验签之后，单独调用 =
// "验签前剥离"，判据 AC-SDK-08 会红）；它只是把 ⑤ 步的实现暴露出来供宿主组合与测试。
func StripHeader(r *http.Request, header string) *http.Request {
	return replaceView(r, header)
}

// replaceView 返回**新的** `*http.Request`：头表重建、断言头（大小写不敏感）去掉。
// 同时把原请求上的该头也删掉（防御性：外层日志中间件若在之后读取原 `r`，同样拿不到）。
func replaceView(r *http.Request, header string) *http.Request {
	hdr := make(http.Header, len(r.Header))
	for k, vs := range r.Header {
		if strings.EqualFold(k, header) {
			continue
		}
		hdr[k] = append([]string(nil), vs...)
	}
	out := r.Clone(r.Context())
	out.Header = hdr
	r.Header.Del(header)
	return out
}

func (s *SDK) reject(w http.ResponseWriter, r *http.Request, err error) {
	code, status, msg := CodeTokenInvalid, http.StatusUnauthorized, "unauthorized"
	var ge *Error
	if errors.As(err, &ge) {
		code, status, msg = ge.Code, ge.Status, ge.Message
	}
	s.logf("[guanjin] %s %s → %d %s: %v", r.Method, r.URL.Path, status, code, err)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": msg})
}
