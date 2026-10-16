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

	"github.com/spencerswagger/GuanJin/internal/identifier"
	"github.com/spencerswagger/GuanJin/internal/session"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const (
	sessionCookieName = "guanjin_session"
	cookiePath        = "/"

	defaultAccessTokenTTL  = 600 * time.Second
	defaultRefreshTokenTTL = 28800 * time.Second

	acrAAL1 = "AAL1"
	acrAAL2 = "AAL2"
	acrAAL3 = "AAL3"
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
	Users          *store.UserRepo
	Clients        *store.ClientRepo
	Identifiers    *store.IdentifierRepo
	Authenticators *store.AuthenticatorRepo
	Sessions       *store.SessionRepo
	Keys           *store.SigningKeyRepo
	Policies       *store.LoginPolicyRepo
	Roles          *store.RoleBindingRepo
	SessionMgr     *session.Manager
	Blind          *identifier.BlindIndexer
}

type Server struct {
	cfg   Config
	log   *zap.Logger
	state *StateStore

	users          *store.UserRepo
	clients        *store.ClientRepo
	authenticators *store.AuthenticatorRepo
	sessions       *store.SessionRepo
	keys           *store.SigningKeyRepo
	policies       *store.LoginPolicyRepo
	roles          *store.RoleBindingRepo
	sessionMgr     *session.Manager
}

func New(deps Deps) *Server {
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Server{
		cfg:            deps.Config,
		log:            logger,
		state:          NewStateStore(deps.Redis),
		users:          deps.Users,
		clients:        deps.Clients,
		authenticators: deps.Authenticators,
		sessions:       deps.Sessions,
		keys:           deps.Keys,
		policies:       deps.Policies,
		roles:          deps.Roles,
		sessionMgr:     deps.SessionMgr,
	}
}

func (s *Server) RegisterRoutes(engine *gin.Engine) {
	engine.GET("/.well-known/openid-configuration", s.Discovery)
	engine.GET("/.well-known/jwks.json", s.JWKS)
	engine.GET("/authorize", s.Authorize)
	engine.POST("/oauth/token", s.Token)
	engine.GET("/userinfo", s.UserInfo)
	engine.POST("/userinfo", s.UserInfo)
	engine.GET("/logout", s.Logout)
	engine.POST("/logout", s.Logout)
	engine.GET("/login", s.LoginPage)
	engine.POST("/login", s.Login)
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

func acrRank(acr string) int {
	switch acr {
	case acrAAL3:
		return 3
	case acrAAL2:
		return 2
	case acrAAL1:
		return 1
	default:
		return 0
	}
}

func acrSatisfies(have, required string) bool {
	return acrRank(have) >= acrRank(required)
}

func targetACR(tier string, policy *model.LoginPolicy) string {
	if policy != nil && policy.MinACR != "" {
		return policy.MinACR
	}
	if tier == "L3" {
		return acrAAL2
	}
	return acrAAL1
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

func queryString(params map[string]string) string {
	values := url.Values{}
	for k, v := range params {
		if v != "" {
			values.Set(k, v)
		}
	}
	return values.Encode()
}

func authorizationRequestParams(c *gin.Context) map[string]string {
	q := c.Request.URL.Query()
	get := func(key string) string {
		if v := c.PostForm(key); v != "" {
			return v
		}
		return q.Get(key)
	}
	return map[string]string{
		"response_type":         get("response_type"),
		"client_id":             get("client_id"),
		"redirect_uri":          get("redirect_uri"),
		"scope":                 get("scope"),
		"state":                 get("state"),
		"nonce":                 get("nonce"),
		"code_challenge":        get("code_challenge"),
		"code_challenge_method": get("code_challenge_method"),
		"acr_values":            get("acr_values"),
	}
}

func trimBearer(header string) string {
	if len(header) > 7 && strings.EqualFold(header[:7], "Bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}
