package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/observability"
	"github.com/spencerswagger/GuanJin/internal/store"
)

type Deps struct {
	Logger          *zap.Logger
	Redis           *store.Redis
	Keys            *store.SigningKeyRepo
	Clients         *store.ClientRepo
	Sessions        *store.SessionRepo
	Policies        *store.LoginPolicyRepo
	Roles           *store.RoleBindingRepo
	Users           *store.UserRepo
	Visited         *store.UserVisitedClientRepo
	AuthEvents      *store.AuthEventRepo
	Router          Router
	Notifier        LogoutNotifier
	Registry        *factor.Registry
	Issuer          string
	AssertionIssuer string
	AssertionTTL    time.Duration
	UpstreamTimeout time.Duration
	DialTimeout     time.Duration
	RequireRedis    bool
	MaxConcurrent   int
}

type Gateway struct {
	dep      Deps
	log      *zap.Logger
	router   Router
	admitter *Admitter
	assertor Assertor
	proxy    *httputil.ReverseProxy
	slo      *SLO
	sem      chan struct{}
}

type proxyPayload struct {
	target      *url.URL
	assertion   string
	stripPrefix string
}

type proxyPayloadKey struct{}

func New(deps Deps) *Gateway {
	log := deps.Logger
	if log == nil {
		log = zap.NewNop()
	}
	router := deps.Router
	if router == nil {
		router = NewTableRouter(nil)
	}
	admitter := &Admitter{
		redis:        deps.Redis,
		requireRedis: deps.RequireRedis,
		keys:         deps.Keys,
		clients:      deps.Clients,
		sessions:     deps.Sessions,
		policies:     deps.Policies,
		roles:        deps.Roles,
		users:        deps.Users,
		visited:      deps.Visited,
		authEvents:   deps.AuthEvents,
		registry:     deps.Registry,
		issuer:       deps.Issuer,
	}
	g := &Gateway{
		dep:      deps,
		log:      log,
		router:   router,
		admitter: admitter,
		assertor: NewSM2Assertor(deps.Keys, deps.AssertionIssuer, deps.AssertionTTL),
		proxy:    newReverseProxy(deps),
		slo:      NewSLO(deps.Visited, deps.Notifier),
	}
	if deps.MaxConcurrent > 0 {
		g.sem = make(chan struct{}, deps.MaxConcurrent)
	}
	return g
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if g.sem != nil {
		select {
		case g.sem <- struct{}{}:
			defer func() { <-g.sem }()
		default:
			httpx.WriteError(w, r, httpx.New("SERVICE_DEGRADED", "网关负载过高"))
			return
		}
	}

	route, ok := g.router.Resolve(requestHost(r), r.URL.Path)
	if !ok {
		httpx.WriteError(w, r, httpx.New("NOT_FOUND", "route not found"))
		return
	}

	adm, appErr := g.admitter.Admit(r.Context(), r, route)
	if appErr != nil {
		if auditErr := g.admitter.record(r.Context(), r, route, nil, "gateway_deny", appErr); auditErr != nil {
			httpx.WriteError(w, r, auditErr)
			return
		}
		g.log.Warn("gateway deny",
			zap.String("client_id", route.ClientID),
			zap.String("err_code", appErr.Code),
			observability.TraceIDField(r.Context()),
		)
		httpx.WriteError(w, r, appErr)
		return
	}

	if err := g.admitter.record(r.Context(), r, route, adm, "gateway_admit", nil); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := g.admitter.markVisit(r.Context(), adm); err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	token, _, err := g.assertor.Issue(r.Context(), ClaimInput{
		Subject:      adm.Session.UserID.String(),
		Audience:     route.ClientID,
		OriginClient: adm.Session.ClientID,
		Username:     adm.Username,
		Roles:        adm.Roles,
		ACR:          adm.Session.ACR,
		AMR:          []string(adm.Session.AMR),
	})
	if err != nil {
		httpx.WriteError(w, r, keyUnavailable())
		return
	}

	target, err := url.Parse(route.Upstream)
	if err != nil {
		httpx.WriteError(w, r, httpx.New("INTERNAL_ERROR", "上游配置无效"))
		return
	}

	r.Header.Del(AssertionHeader)
	stripPrefix := ""
	if route.MatchType == MatchClient {
		stripPrefix = route.Match
	}
	ctx := context.WithValue(r.Context(), proxyPayloadKey{}, proxyPayload{target: target, assertion: token, stripPrefix: stripPrefix})
	r = r.WithContext(ctx)

	g.log.Info("gateway forward",
		zap.String("client_id", route.ClientID),
		zap.String("user_id", adm.UserID.String()),
		zap.String("session_id", adm.Session.ID.String()),
		observability.TraceIDField(r.Context()),
	)
	g.proxy.ServeHTTP(w, r)
}

func (g *Gateway) OnLogout(ctx context.Context, userID uuid.UUID, sessionID uuid.UUID, reason string) (int, error) {
	return g.slo.OnLogout(ctx, userID, sessionID, reason)
}

func (g *Gateway) SetRouter(router Router) {
	if router == nil {
		return
	}
	g.router = router
}

func newReverseProxy(deps Deps) *httputil.ReverseProxy {
	dialTimeout := deps.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 2 * time.Second
	}
	upstreamTimeout := deps.UpstreamTimeout
	if upstreamTimeout <= 0 {
		upstreamTimeout = 5 * time.Second
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: upstreamTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			payload, _ := req.Context().Value(proxyPayloadKey{}).(proxyPayload)
			if payload.stripPrefix != "" {
				req.URL.Path = strings.TrimPrefix(req.URL.Path, payload.stripPrefix)
				if req.URL.Path == "" {
					req.URL.Path = "/"
				}
			}
			if payload.target != nil {
				req.URL.Scheme = payload.target.Scheme
				req.URL.Host = payload.target.Host
				if payload.target.Path != "" && payload.target.Path != "/" {
					req.URL.Path = strings.TrimRight(payload.target.Path, "/") + req.URL.Path
				}
			}
			setAssertion(req.Header, payload.assertion)
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
				return
			}
			httpx.WriteError(w, r, httpx.New("UPSTREAM_UNAVAILABLE", "上游服务不可用"))
		},
	}
}

func requestHost(r *http.Request) string {
	if r.Host != "" {
		return r.Host
	}
	return r.Header.Get("Host")
}
