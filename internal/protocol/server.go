package protocol

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/flow"
	"github.com/spencerswagger/GuanJin/internal/identifier"
	"github.com/spencerswagger/GuanJin/internal/notify"
	"github.com/spencerswagger/GuanJin/internal/session"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"

	"github.com/spencerswagger/GuanJin/internal/admin"
	"github.com/spencerswagger/GuanJin/internal/agent"
)

const (
	sessionCookieName = "guanjin_session"
	cookiePath        = "/"

	defaultAccessTokenTTL  = 600 * time.Second
	defaultRefreshTokenTTL = 28800 * time.Second
)

type Config struct {
	Issuer        string
	PublicBaseURL string
	SecureCookie  bool
}

type Deps struct {
	Config         Config
	Logger         *zap.Logger
	Redis          *store.Redis
	DB             *gorm.DB
	Users          *store.UserRepo
	Clients        *store.ClientRepo
	Identifiers    *store.IdentifierRepo
	Authenticators *store.AuthenticatorRepo
	Challenges     *store.ChallengeRepo
	Sessions       *store.SessionRepo
	Keys           *store.SigningKeyRepo
	Policies       *store.LoginPolicyRepo
	Roles          *store.RoleBindingRepo
	Visited        *store.UserVisitedClientRepo
	AuthEvents     *store.AuthEventRepo
	SessionMgr     *session.Manager
	Blind          *identifier.BlindIndexer
	Registry       *factor.Registry
	Notifier       notify.Notifier
	Agent          *agent.Service
	Admin          *admin.Server
}

type Server struct {
	cfg   Config
	log   *zap.Logger
	state *StateStore

	users          *store.UserRepo
	clients        *store.ClientRepo
	identifiers    *store.IdentifierRepo
	authenticators *store.AuthenticatorRepo
	challenges     *store.ChallengeRepo
	sessions       *store.SessionRepo
	keys           *store.SigningKeyRepo
	policies       *store.LoginPolicyRepo
	roles          *store.RoleBindingRepo
	sessionMgr     *session.Manager
	blind          *identifier.BlindIndexer

	registry *factor.Registry
	flows    *flow.Store
	orch     *flow.Orchestrator

	registrar  *flow.Registrar
	visited    *store.UserVisitedClientRepo
	authEvents *store.AuthEventRepo
	notifier   notify.Notifier
	agent      *agent.Service
	admin      *admin.Server
}

func New(deps Deps) *Server {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	registry := deps.Registry
	if registry == nil {
		registry = factor.NewRegistry()
	}
	notifier := deps.Notifier
	if notifier == nil {
		notifier = notify.NoopNotifier{}
	}
	registrar := flow.NewRegistrar(flow.RegistrarDeps{
		DB:          deps.DB,
		Clients:     deps.Clients,
		Users:       deps.Users,
		Identifiers: deps.Identifiers,
		Challenges:  deps.Challenges,
		Policies:    deps.Policies,
		Visited:     deps.Visited,
		AuthEvents:  deps.AuthEvents,
		Registry:    registry,
		Blind:       deps.Blind,
		State:       flow.NewRegisterStore(deps.Redis),
	})
	return &Server{
		cfg:            deps.Config,
		log:            logger,
		state:          NewStateStore(deps.Redis),
		users:          deps.Users,
		clients:        deps.Clients,
		identifiers:    deps.Identifiers,
		authenticators: deps.Authenticators,
		challenges:     deps.Challenges,
		sessions:       deps.Sessions,
		keys:           deps.Keys,
		policies:       deps.Policies,
		roles:          deps.Roles,
		sessionMgr:     deps.SessionMgr,
		blind:          deps.Blind,
		registry:       registry,
		flows:          flow.NewStore(deps.Redis),
		orch:           flow.NewOrchestrator(registry),
		registrar:      registrar,
		visited:        deps.Visited,
		authEvents:     deps.AuthEvents,
		notifier:       notifier,
		agent:          deps.Agent,
		admin:          deps.Admin,
	}
}

func (s *Server) RegisterRoutes(engine *gin.Engine) {
	engine.GET("/.well-known/openid-configuration", s.Discovery)
	engine.GET("/.well-known/jwks.json", s.JWKS)
	engine.GET("/authorize", s.Authorize)
	engine.GET("/authorize/flow", s.AuthorizeFlow)
	engine.POST("/authorize/submit", s.AuthorizeSubmit)
	engine.POST("/auth/challenge/:factor", s.Challenge)
	engine.POST("/auth/verify/:factor", s.Verify)
	engine.POST("/oauth/token", s.Token)
	engine.POST("/auth/register/start", s.RegisterStart)
	engine.POST("/auth/register/complete", s.RegisterComplete)
	engine.GET("/userinfo", s.UserInfo)
	engine.POST("/userinfo", s.UserInfo)
	engine.GET("/logout", s.Logout)
	engine.POST("/logout", s.Logout)
	engine.GET("/login", s.LoginPage)
	engine.POST("/login", s.Login)
	me := engine.Group("/api/v1/me", s.UserAuth())
	me.GET("/identifiers", s.ListIdentifiers)
	me.POST("/identifiers", s.BindIdentifier)
	me.POST("/identifiers/:id/verify", s.VerifyIdentifier)
	me.DELETE("/identifiers/:id", s.UnbindIdentifier)
	if s.agent != nil {
		s.agent.RegisterRoutes(engine)
	}
	if s.admin != nil {
		s.admin.RegisterRoutes(engine)
	}
}

func (s *Server) issuer() string {
	if s.cfg.Issuer != "" {
		return s.cfg.Issuer
	}
	return s.cfg.PublicBaseURL
}

func (s *Server) setSessionCookie(c *gin.Context, sessionID string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     cookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(c *gin.Context) {
	s.setSessionCookie(c, "", -1)
}

func (s *Server) currentSession(c *gin.Context) *model.Session {
	raw, err := c.Cookie(sessionCookieName)
	if err != nil || raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil
	}
	sess, err := s.sessions.GetByID(c.Request.Context(), id)
	if err != nil {
		return nil
	}
	return sess
}

func newOpaqueToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func durationFromPolicy(seconds int32, fallback time.Duration) time.Duration {
	if seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func clientCredentials(c *gin.Context) (clientID, secret string) {
	if id, pass, ok := c.Request.BasicAuth(); ok {
		return id, pass
	}
	return c.PostForm("client_id"), c.PostForm("client_secret")
}

func withQuery(rawURL string, params map[string]string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func trimBearer(header string) string {
	if len(header) > 7 && strings.EqualFold(header[:7], "Bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}
