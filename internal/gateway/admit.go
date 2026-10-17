package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/factor"
	"github.com/spencerswagger/GuanJin/internal/flow"
	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/observability"
	"github.com/spencerswagger/GuanJin/internal/store"
	"github.com/spencerswagger/GuanJin/internal/store/model"
)

const sessionCookieName = "guanjin_session"

type Admission struct {
	UserID   uuid.UUID
	Username string
	Session  *model.Session
	Client   *model.Client
	Roles    []string
}

type Admitter struct {
	redis        *store.Redis
	requireRedis bool
	keys         *store.SigningKeyRepo
	clients      *store.ClientRepo
	sessions     *store.SessionRepo
	policies     *store.LoginPolicyRepo
	roles        *store.RoleBindingRepo
	users        *store.UserRepo
	visited      *store.UserVisitedClientRepo
	authEvents   *store.AuthEventRepo
	registry     *factor.Registry
	issuer       string
}

func (a *Admitter) Admit(ctx context.Context, r *http.Request, route Route) (*Admission, *httpx.AppError) {
	if a.redis != nil && a.requireRedis {
		if err := a.redis.Ping(ctx); err != nil {
			return nil, httpx.New("REDIS_UNAVAILABLE", "Redis 不可用")
		}
	}

	client, err := a.clients.GetByID(ctx, route.ClientID)
	if err != nil {
		if errors.Is(err, store.ErrClientNotFound) {
			return nil, httpx.New("CLIENT_NOT_FOUND", "业务系统不存在")
		}
		return nil, dbUnavailable()
	}

	sessionID, appErr := a.resolveSessionID(ctx, r, route)
	if appErr != nil {
		return nil, appErr
	}

	sess, err := a.sessions.GetByID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return nil, httpx.New("SESSION_NOT_FOUND", "会话不存在")
		}
		return nil, dbUnavailable()
	}
	if rejectErr := sessionRejectReason(sess, time.Now().UTC()); rejectErr != nil {
		return nil, rejectAppError(rejectErr)
	}
	if sess.ClientID != route.ClientID {
		return nil, httpx.New("SESSION_NOT_FOUND", "会话与目标业务系统不匹配")
	}

	user, err := a.users.GetByID(ctx, sess.UserID)
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			return nil, httpx.New("SESSION_NOT_FOUND", "会话用户不存在")
		}
		return nil, dbUnavailable()
	}

	roles, err := a.roles.ActiveRolesForUser(ctx, sess.UserID, route.ClientID)
	if err != nil {
		return nil, dbUnavailable()
	}
	if roles == nil {
		roles = []string{}
	}

	policy, err := a.policies.GetEffective(ctx, client.ClientID, client.Tier)
	if err != nil {
		return nil, dbUnavailable()
	}
	target := targetACR(client.Tier, policy)
	if !acrAtLeast(sess.ACR, target) {
		return nil, httpx.New("ACR_INSUFFICIENT", "认证档位不足").
			WithDetails(map[string]any{"flow": a.flowDescriptor(sess, client, policy)})
	}

	return &Admission{
		UserID:   sess.UserID,
		Username: user.Username,
		Session:  sess,
		Client:   client,
		Roles:    roles,
	}, nil
}

func (a *Admitter) resolveSessionID(ctx context.Context, r *http.Request, route Route) (uuid.UUID, *httpx.AppError) {
	raw := trimBearer(r.Header.Get("Authorization"))
	if raw == "" {
		cookie, err := r.Cookie(sessionCookieName)
		if err == nil && cookie.Value != "" {
			id, perr := uuid.Parse(cookie.Value)
			if perr != nil {
				return uuid.Nil, httpx.New("TOKEN_INVALID", "会话 cookie 无效")
			}
			return id, nil
		}
		return uuid.Nil, httpx.New("TOKEN_INVALID", "缺少访问令牌")
	}

	_, priv, err := a.keys.ActiveSigner(ctx, crypto.PurposeOIDC)
	if err != nil {
		return uuid.Nil, keyUnavailable()
	}
	parsed, err := jwt.Parse(raw, func(*jwt.Token) (interface{}, error) {
		return &priv.PublicKey, nil
	}, jwt.WithValidMethods([]string{crypto.AlgoSM3WithSM2}))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return uuid.Nil, httpx.New("TOKEN_EXPIRED", "令牌已过期")
		}
		return uuid.Nil, httpx.New("TOKEN_INVALID", "令牌无效")
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return uuid.Nil, httpx.New("TOKEN_INVALID", "令牌声明无效")
	}
	if a.issuer != "" {
		if iss, _ := claims["iss"].(string); iss != a.issuer {
			return uuid.Nil, httpx.New("TOKEN_INVALID", "令牌签发者不匹配")
		}
	}
	if aud, _ := claims["aud"].(string); aud != route.ClientID {
		return uuid.Nil, httpx.New("TOKEN_INVALID", "令牌受众与业务系统不匹配")
	}
	sid, _ := claims["sid"].(string)
	id, perr := uuid.Parse(sid)
	if perr != nil {
		return uuid.Nil, httpx.New("TOKEN_INVALID", "令牌缺少会话标识")
	}
	return id, nil
}

func (a *Admitter) record(ctx context.Context, r *http.Request, route Route, adm *Admission, eventType string, appErr *httpx.AppError) *httpx.AppError {
	now := time.Now().UTC()
	event := &model.AuthEvent{
		EventType:  eventType,
		Result:     normalizeResult(appErr),
		AMR:        pq.StringArray{},
		OccurredAt: now,
	}
	clientID := route.ClientID
	event.ClientID = &clientID
	if traceID := observability.FromContext(ctx); traceID != "" {
		event.TraceID = &traceID
	}
	if ip := clientIP(r); ip != "" {
		event.IP = &ip
	}
	if ua := r.UserAgent(); ua != "" {
		event.UserAgent = &ua
	}
	if adm != nil {
		userID := adm.Session.UserID
		sessionID := adm.Session.ID
		acr := adm.Session.ACR
		event.UserID = &userID
		event.SessionID = &sessionID
		event.ACR = &acr
		if len(adm.Session.AMR) > 0 {
			event.AMR = adm.Session.AMR
		}
	}
	if appErr != nil {
		reason := appErr.Code
		event.Reason = &reason
	}
	if err := a.authEvents.Create(ctx, event); err != nil {
		return dbUnavailable()
	}
	return nil
}

func (a *Admitter) markVisit(ctx context.Context, adm *Admission) *httpx.AppError {
	if err := a.visited.Upsert(ctx, adm.Session.UserID, adm.Client.ClientID, adm.Client.Tier); err != nil {
		return dbUnavailable()
	}
	return nil
}

func (a *Admitter) flowDescriptor(sess *model.Session, client *model.Client, policy *model.LoginPolicy) flow.FlowDescriptor {
	available := []string{}
	disabled := []string{}
	if policy != nil {
		disabled = append(disabled, policy.DisabledFactors...)
	}
	steps := []flow.FlowStep{}
	if a.registry != nil {
		orch := flow.NewOrchestrator(a.registry)
		for _, typ := range a.registry.EnabledTypes() {
			available = append(available, typ)
		}
		if provider, role, ok := orch.NextStep(nil, disabled); ok {
			next := string(factor.NextActionInputPassword)
			if namer, ok := provider.(factor.NextActionNamer); ok {
				next = string(namer.NextAction())
			}
			steps = append(steps, flow.FlowStep{
				Factor:        provider.Type(),
				Role:          role,
				Status:        flow.StatusPending,
				ChallengeHint: &flow.ChallengeHint{NextAction: next},
			})
		}
	}
	return flow.FlowDescriptor{
		Client:           flow.ClientRef{ID: client.ClientID, Name: client.Name, Tier: client.Tier},
		TargetACR:        targetACR(client.Tier, policy),
		CurrentACR:       sess.ACR,
		Satisfied:        false,
		Steps:            steps,
		AvailableFactors: available,
		DisabledFactors:  disabled,
		RegistrationMode: "self",
	}
}

func targetACR(tier string, policy *model.LoginPolicy) string {
	if policy != nil && policy.MinACR != "" {
		return policy.MinACR
	}
	if tier == "L3" {
		return factor.ACRAAL2
	}
	return factor.ACRAAL1
}

func acrRank(acr string) int {
	switch acr {
	case factor.ACRAAL2:
		return 2
	case factor.ACRAAL1:
		return 1
	default:
		return 0
	}
}

func acrAtLeast(current, target string) bool {
	return acrRank(current) >= acrRank(target)
}

func normalizeResult(appErr *httpx.AppError) string {
	if appErr == nil {
		return "success"
	}
	switch appErr.Code {
	case "SESSION_REVOKED", "SESSION_NOT_FOUND", "ACR_INSUFFICIENT", "INSUFFICIENT_ROLE", "INSUFFICIENT_SCOPE":
		return "denied"
	case "ACCOUNT_LOCKED":
		return "locked"
	case "TOKEN_INVALID", "TOKEN_EXPIRED", "TOKEN_REVOKED", "INVALID_CREDENTIAL":
		return "failure"
	default:
		return "error"
	}
}

func rejectAppError(err error) *httpx.AppError {
	switch {
	case errors.Is(err, errSessionRevoked):
		return httpx.New("SESSION_REVOKED", "会话已撤销")
	case errors.Is(err, errSessionExpired):
		return httpx.New("TOKEN_EXPIRED", "会话已过期")
	default:
		return httpx.New("SESSION_NOT_FOUND", "会话不存在")
	}
}

func trimBearer(header string) string {
	if len(header) > 7 && strings.EqualFold(header[:7], "Bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSpace(host)
}

func dbUnavailable() *httpx.AppError {
	return &httpx.AppError{Code: "DB_UNAVAILABLE", HTTP: http.StatusServiceUnavailable, Message: "数据库不可用"}
}

func keyUnavailable() *httpx.AppError {
	return &httpx.AppError{Code: "KEY_UNAVAILABLE", HTTP: http.StatusServiceUnavailable, Message: "密钥不可用"}
}
