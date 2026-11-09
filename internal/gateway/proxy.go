package gateway

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// AssertionHeader 是内部断言头名（**冻结名**：D10 / 数据面设计 §4.6 / F0）。
const AssertionHeader = "X-GuanJin-Assertion"

// 上游超时的默认值来自技术栈 §7.2 的 envDefault（`GUANJIN_GATEWAY_UPSTREAM_TIMEOUT=5s`、
// `GUANJIN_GATEWAY_DIAL_TIMEOUT=2s`）—— 有出处，不是实现随手拍的数。
const (
	DefaultUpstreamTimeout = 5 * time.Second
	DefaultDialTimeout     = 2 * time.Second
)

// Options 是数据面转发内核的装配参数。
type Options struct {
	// TraceHeader 是贯穿日志与响应的 trace 头名（D4-C）。空则 `X-Trace-Id`。
	TraceHeader string
	// UpstreamTimeout / DialTimeout 为 0 时取上面登记的默认值。
	UpstreamTimeout time.Duration
	DialTimeout     time.Duration
	// Logger 为空时用 slog.Default()。
	Logger *slog.Logger
	// MaxIdleConnsPerHost 是连接池上限（0 取 64）。
	MaxIdleConnsPerHost int
}

// Handler 是数据面主处理器：剥离 → 路由 → 转发。
//
// 认证 / 档位判定 / 断言签发**不在这里**（S8·2 接进来）：本类型只负责
// "把一条已经允许通过的请求安全地送到正确的上游"，并把上游失败翻译成
// `503 UPSTREAM_UNAVAILABLE`（AC-DP-13）。
type Handler struct {
	router     *Router
	opts       Options
	logger     *slog.Logger
	transports map[string]http.RoundTripper
	mu         sync.Mutex
	proxies    map[string]*httputil.ReverseProxy
}

// New 构造数据面处理器。
func New(router *Router, opts Options) (*Handler, error) {
	if router == nil {
		return nil, errors.New("gateway: nil router (fail-closed: 没有路由表就不该起数据面)")
	}
	if opts.TraceHeader == "" {
		opts.TraceHeader = "X-Trace-Id"
	}
	if opts.UpstreamTimeout <= 0 {
		opts.UpstreamTimeout = DefaultUpstreamTimeout
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = DefaultDialTimeout
	}
	if opts.MaxIdleConnsPerHost <= 0 {
		opts.MaxIdleConnsPerHost = 64
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		router:     router,
		opts:       opts,
		logger:     logger,
		transports: make(map[string]http.RoundTripper, len(router.Routes())),
		proxies:    make(map[string]*httputil.ReverseProxy, len(router.Routes())),
	}, nil
}

// ServeHTTP 实现数据面请求处理。顺序**不可随意调整**：
//
//	① 先弃客户端伪造的同名头（AC-DP-02/03：哪怕这个请求马上要被拒，也不许头透传）
//	② 路由匹配（无路由 → 404 INVALID_REQUEST，**不裸转发**；`UPSTREAM_UNAVAILABLE` 只留给转发失败）
//	③ 转发（上游超时/拨号失败 → 503 UPSTREAM_UNAVAILABLE，AC-DP-13）
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ① 先弃：进任何分支之前就删掉，后面所有消费者（转发、日志、错误处理）都拿不到它。
	stripAssertionHeader(r.Header)

	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()
	defer func() {
		h.logger.Info("data-plane request",
			"trace_id", httpx.TraceIDFromContext(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"client_id", rec.clientID,
		)
	}()

	// ② 路由
	route, ok := h.router.Match(r.URL.Path)
	if !ok {
		// 404 + `INVALID_REQUEST`（`decisions.md:411` 的定稿）：§5.4 没有"路由缺失"专用码、
		// F0 禁自造码名，故复用 E1 的既有码、把状态覆盖成 404（该码默认 400）。
		// 为什么不是 `UPSTREAM_UNAVAILABLE`：那个码只表示"已开始转发但上游失败/超时"（D8-C′④ + AC-DP-13）。
		httpx.WriteErrorStatus(rec, httpx.TraceIDFromContext(r.Context()), httpx.CodeInvalidRequest,
			http.StatusNotFound, "该路径未归属任何业务系统", nil)
		return
	}
	rec.clientID = route.ClientID

	// ③ 转发
	h.ServeRoute(rec, r, route)
}

// ServeRoute 把一条**已通过准入**的请求转发到 `route` 指向的上游。
//
// 与 `ServeHTTP` 的分工：`ServeHTTP` 是"剥离 → 匹配 → 转发"的独立形态（S8·1 的基座，
// 直接挂在 `http.ServeMux` 上也能跑）；`Pipeline` 走"剥离 → 匹配 → 准入 → 签发 → ServeRoute"，
// 访问日志由 `Pipeline` 记一条（避免同一请求两条访问日志、两个耗时口径）。
func (h *Handler) ServeRoute(w http.ResponseWriter, r *http.Request, route Route) {
	proxy, err := h.proxyFor(route)
	if err != nil {
		// 构造期就该失败的东西在运行期失败 = 装配缺陷；仍按"拒服"处理，不裸转发。
		h.logger.Error("build proxy failed", "error", err, "client_id", route.ClientID)
		httpx.WriteError(w, httpx.TraceIDFromContext(r.Context()), httpx.CodeUpstreamUnavailable,
			"upstream unavailable", nil)
		return
	}
	proxy.ServeHTTP(w, r)
}

// proxyFor 按上游基址缓存 ReverseProxy（每个上游一套连接池）。
func (h *Handler) proxyFor(route Route) (*httputil.ReverseProxy, error) {
	key := route.Upstream.String()
	h.mu.Lock()
	defer h.mu.Unlock()
	if p, ok := h.proxies[key]; ok {
		return p, nil
	}

	transport, ok := h.transports[key]
	if !ok {
		transport = &http.Transport{
			Proxy: nil, // 数据面不走环境代理：出口必须可预测
			DialContext: (&net.Dialer{
				Timeout:   h.opts.DialTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          h.opts.MaxIdleConnsPerHost * 4,
			MaxIdleConnsPerHost:   h.opts.MaxIdleConnsPerHost,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   h.opts.DialTimeout, // 上游若是 https，握手也受同一上限约束
			ExpectContinueTimeout: 1 * time.Second,
			// 背压的核心：上游迟迟不回响应头就断开，避免慢上游把连接池占满。
			ResponseHeaderTimeout: h.opts.UpstreamTimeout,
			ForceAttemptHTTP2:     false,
		}
		h.transports[key] = transport
	}

	upstream := route.Upstream
	target := *upstream
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		// Director 只改目标地址：**不做 path 重写**（D1-A1：PATH 必须两端逐字节一致，
		// 中间件剥前缀会让 Agent 面签名永远验不过；这里同理，业务系统看到的路径就是
		// 客户端发出来的路径）。
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			if base := target.Path; base != "" && base != "/" {
				req.URL.Path = singleJoiningSlash(base, req.URL.Path)
			}
			req.Host = target.Host
			// **禁止在这里删断言头**：Director 是"已通过准入 → 网关现签的头"出门的地方，
			// 在这里 strip 会把刚签好的身份删掉（S8·2 的实测坑）。剥离只有两个时机：
			// ① 请求**进**数据面时（`Pipeline.ServeHTTP` / `Handler.ServeHTTP` 的第一步）；
			// ② 业务系统侧验签之后（SDK，D10 的六步时序）——那是另一件事，不在这里。
		},
		// 上游失败语义（AC-DP-13）：**503 UPSTREAM_UNAVAILABLE**，
		// 不是 500、不是 DEPENDENCY_UNAVAILABLE、更不是已废弃的 SERVICE_DEGRADED；
		// 且此时**不注入**任何身份（这条路径根本没签过断言）。
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h.logger.Warn("upstream failed",
				"error", err,
				"trace_id", httpx.TraceIDFromContext(r.Context()),
				"path", r.URL.Path,
				"client_id", route.ClientID,
			)
			httpx.WriteError(w, httpx.TraceIDFromContext(r.Context()),
				httpx.CodeUpstreamUnavailable, "upstream unavailable", nil)
		},
		ErrorLog: nil, // 用结构化日志（上面的 ErrorHandler/Warn），不走 std log
	}
	h.proxies[key] = proxy
	return proxy, nil
}

// stripAssertionHeader 删掉客户端自带（或任何中间环节塞进来）的断言头。
//
// 用 `Del` 而不是置空：置空仍会让下游 `Header.Get` 拿到空串并可能被当成"有该头但为空"，
// 而 `Header.Values` 也可能残留（同名多值）。删除是唯一能保证"下游取不到"的做法。
func stripAssertionHeader(h http.Header) {
	if h == nil {
		return
	}
	h.Del(AssertionHeader)
	// 大小写兜底：`http.Header.Del` 会把键规范成 `X-Guanjin-Assertion`（CanonicalMIMEHeaderKey
	// 只保留首字母大写），而我们的冻结名是 `X-GuanJin-Assertion`（J 大写）——**两者不相等**。
	// 所以这里必须用 EqualFold 扫一遍，否则非规范键（对手工构造/上游转发的 header map）
	// 会带着伪造断言溜过去。这条是单测 TestInboundAssertionHeaderIsStripped 逼出来的。
	for k := range h {
		if strings.EqualFold(k, AssertionHeader) {
			delete(h, k)
		}
	}
}

func singleJoiningSlash(a, b string) string {
	switch {
	case len(a) == 0:
		return b
	case len(b) == 0:
		return a
	}
	aslash := a[len(a)-1] == '/'
	bslash := b[0] == '/'
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// statusRecorder 记录实际写出的状态码与命中的 client（日志用）。
type statusRecorder struct {
	http.ResponseWriter
	status      int
	clientID    string
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wroteHeader {
		return
	}
	s.status = code
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}

// Flush 透传（SSE/流式响应依赖它；不透传会让长连接上游"卡住不吐"）。
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
