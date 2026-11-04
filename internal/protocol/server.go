// Package protocol 是协议面（OIDC / OAuth2）的实现：授权码换令牌、刷新轮换、自省、用户信息、发现与 JWKS。
//
// 边界（S6，与 S7/S8 的分工）：
//   - 只做**令牌相关**端点；续期/撤销/踢出/管理面列表属 S7，数据面转发属 S8；
//   - `/userinfo` 与 `/oauth/introspect` **本地解析、零查库**（D8-D′ / AC-LOGIN-08）；
//   - 授权码与刷新令牌走 Redis（裁定 A，键名冻结在 `store.go`），不新增表；
//   - 签名密钥在**启动期**解封（`internal/signing`），运行期不换键。
package protocol

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/signing"
)

// ClientPolicy 是签发令牌需要的客户端策略（来源 `login_policies`，双超时与 TTL 全在这）。
type ClientPolicy struct {
	ACR         string
	AccessTTL   time.Duration
	RefreshTTL  time.Duration
	IdleTimeout time.Duration
	AbsTimeout  time.Duration
	RequirePKCE bool
}

// ClientPolicies 读客户端策略（实现见装配层；测试用桩）。
type ClientPolicies interface {
	Policy(ctx context.Context, clientID string) (ClientPolicy, error)
}

// SessionSpec 是"建一条最小 active 会话"的入参（S6 只写，S7 负责后续生命周期）。
type SessionSpec struct {
	UserID   string
	ClientID string
	ACR      string
	AMR      []string
}

// SessionIssuer 是会话的最小写路径（`*store.SessionRepo` 的适配器在装配层）。
type SessionIssuer interface {
	Issue(ctx context.Context, spec SessionSpec) (sid string, err error)
}

// AuditEvent 是令牌面要留痕的三类动作（消费 / 轮换 / 撤销）。
type AuditEvent struct {
	EventType string
	ClientID  string
	UserID    string
	Result    string
	Reason    string
	TraceID   string
}

// AuditSink 写认证事件（实现见装配层；写失败不阻断签发，但要进日志）。
type AuditSink interface {
	Append(ctx context.Context, ev AuditEvent) error
}

// Deps 是协议面的全部依赖（**没有默认值**）。
type Deps struct {
	Keys     *signing.Set
	Codes    CodeStore
	Sessions SessionIssuer
	Clients  ClientPolicies
	Audit    AuditSink
	// Issuer 是 discovery 与令牌 `iss` 的基址（来自配置，不在代码里写死域名）。
	Issuer string
	Logger *slog.Logger
	Now    func() time.Time
}

// Server 承载协议面端点（无本地状态，多副本一致）。
type Server struct{ d Deps }

// New 构造；缺依赖即装配期失败。
func New(d Deps) (*Server, error) {
	switch {
	case d.Keys == nil:
		return nil, errors.New("protocol: signing keys are required")
	case d.Codes == nil:
		return nil, errors.New("protocol: code store is required")
	case d.Sessions == nil:
		return nil, errors.New("protocol: session issuer is required")
	case d.Clients == nil:
		return nil, errors.New("protocol: client policies are required")
	case d.Issuer == "":
		return nil, errors.New("protocol: issuer is required")
	case d.Logger == nil:
		return nil, errors.New("protocol: logger is required")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Audit == nil {
		// 审计缺失不静默：装配层必须给；这里兜一个"只进日志"的实现并明确标记。
		d.Logger.Warn("protocol: audit sink is not configured; token events will only be logged")
		d.Audit = nopAudit{}
	}
	return &Server{d: d}, nil
}

type nopAudit struct{}

func (nopAudit) Append(context.Context, AuditEvent) error { return nil }

// Routes 挂上 S6 的端点（端点名照抄接口清单的冻结名）。
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET /.well-known/jwks.json", s.jwks)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("POST /oauth/revoke", s.revoke)
	mux.HandleFunc("POST /oauth/introspect", s.introspect)
	mux.HandleFunc("GET /userinfo", s.userinfo)
	mux.HandleFunc("POST /userinfo", s.userinfo)
}

func (s *Server) discovery(w http.ResponseWriter, r *http.Request) {
	doc := map[string]any{
		"issuer":                                s.d.Issuer,
		"authorization_endpoint":                s.d.Issuer + "/authorize",
		"token_endpoint":                        s.d.Issuer + "/oauth/token",
		"userinfo_endpoint":                     s.d.Issuer + "/userinfo",
		"jwks_uri":                              s.d.Issuer + "/.well-known/jwks.json",
		"revocation_endpoint":                   s.d.Issuer + "/oauth/revoke",
		"introspection_endpoint":                s.d.Issuer + "/oauth/introspect",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"id_token_signing_alg_values_supported": []string{signing.AlgSM3WithSM2},
		"acr_values_supported":                  []string{"AAL1", "AAL2"},
		"subject_types_supported":               []string{"public"},
		"scopes_supported":                      []string{"openid", "profile"},
	}
	writeJSON(w, s.d.Logger, http.StatusOK, doc)
}

func (s *Server) jwks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.d.Logger, http.StatusOK, map[string]any{"keys": s.d.Keys.JWKS()})
}

// tokenBody 是 `application/x-www-form-urlencoded`（OAuth2 §4.1.3）。
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, httpx.CodeInvalidRequest, "请求体不是合法表单")
		return
	}
	grant := r.PostFormValue("grant_type")
	switch grant {
	case "authorization_code":
		s.exchangeCode(w, r)
	case "refresh_token":
		s.rotateRefresh(w, r)
	default:
		s.fail(w, r, httpx.CodeInvalidRequest, "不支持的 grant_type")
	}
}

type codePayload struct {
	Sub       string   `json:"sub"`
	SID       string   `json:"sid,omitempty"`
	ClientID  string   `json:"cid"`
	ACR       string   `json:"acr"`
	AMR       []string `json:"amr"`
	Nonce     string   `json:"nonce,omitempty"`
	Challenge string   `json:"ch,omitempty"`
	Redirect  string   `json:"rd,omitempty"`
}

func (s *Server) exchangeCode(w http.ResponseWriter, r *http.Request) {
	code := r.PostFormValue("code")
	clientID := r.PostFormValue("client_id")
	if code == "" || clientID == "" {
		s.fail(w, r, httpx.CodeMissingParameter, "缺少 code 或 client_id")
		return
	}
	state := auditState{r: r, clientID: clientID}
	raw, err := s.d.Codes.Take(r.Context(), CodeKey(code), ErrCodeGone)
	if err != nil {
		s.audit(r, AuditEvent{EventType: "token", ClientID: clientID, Result: "failure", Reason: "code_gone"})
		s.fail(w, r, mapStoreErr(err, httpx.CodeInvalidGrant), "授权码无效或已被使用")
		return
	}
	var p codePayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		s.fail(w, r, httpx.CodeInvalidGrant, "授权码内容损坏")
		return
	}
	if p.ClientID != clientID {
		s.fail(w, r, httpx.CodeInvalidGrant, "授权码不属于该客户端")
		return
	}
	state.userID = p.Sub
	s.audit(r, AuditEvent{EventType: "token", ClientID: clientID, UserID: p.Sub, Result: "success", Reason: "code_consumed"})

	if p.Challenge != "" {
		if codeErr := verifyPKCE(p.Challenge, r.PostFormValue("code_verifier")); codeErr != nil {
			s.fail(w, r, codeErr.(*protoError).code, codeErr.Error())
			return
		}
	}

	policy, err := s.d.Clients.Policy(r.Context(), clientID)
	if err != nil {
		s.fail(w, r, httpx.CodeInvalidClient, "客户端不存在或已停用")
		return
	}
	sid, err := s.d.Sessions.Issue(r.Context(), SessionSpec{
		UserID: p.Sub, ClientID: clientID, ACR: p.ACR, AMR: p.AMR,
	})
	if err != nil {
		s.d.Logger.ErrorContext(r.Context(), "protocol: create session failed", "trace_id", traceID(r), "error", err)
		s.fail(w, r, httpx.CodeInternalError, "会话创建失败")
		return
	}
	s.issueTokens(w, r, policy, p, sid)
}

func (s *Server) rotateRefresh(w http.ResponseWriter, r *http.Request) {
	rt := r.PostFormValue("refresh_token")
	if rt == "" {
		s.fail(w, r, httpx.CodeMissingParameter, "缺少 refresh_token")
		return
	}
	raw, err := s.d.Codes.Take(r.Context(), RefreshKey(rt), ErrRefreshGone)
	if err != nil {
		// **复用检测**：拿不到就是"被用过/伪造" → `REFRESH_TOKEN_REUSED`（会话撤销归 S7）。
		s.audit(r, AuditEvent{EventType: "token", Result: "failure", Reason: "refresh_reused"})
		s.fail(w, r, mapStoreErr(err, httpx.CodeRefreshTokenReused), "刷新令牌已失效或已被使用")
		return
	}
	var p codePayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		s.fail(w, r, httpx.CodeInvalidGrant, "刷新令牌内容损坏")
		return
	}
	policy, err := s.d.Clients.Policy(r.Context(), p.ClientID)
	if err != nil {
		s.fail(w, r, httpx.CodeInvalidClient, "客户端不存在或已停用")
		return
	}
	s.audit(r, AuditEvent{EventType: "token", ClientID: p.ClientID, UserID: p.Sub, Result: "success", Reason: "refresh_rotated"})
	// 轮换**沿用同一 `sid`**：刷新令牌换来的是同一会话的新令牌，不是新会话。
	s.issueTokens(w, r, policy, p, p.SID)
}

func (s *Server) issueTokens(w http.ResponseWriter, r *http.Request, policy ClientPolicy, p codePayload, sid string) {
	now := s.d.Now().UTC()
	key, err := s.d.Keys.For(signing.PurposeOIDC)
	if err != nil {
		s.d.Logger.ErrorContext(r.Context(), "protocol: no oidc signing key", "error", err)
		s.fail(w, r, httpx.CodeKeyUnavailable, "签名密钥不可用")
		return
	}
	accessClaims := map[string]any{
		"iss": s.d.Issuer, "sub": p.Sub, "aud": p.ClientID, "cid": p.ClientID,
		"acr": p.ACR, "amr": p.AMR, "sid": sid,
		"iat": now.Unix(), "exp": now.Add(policy.AccessTTL).Unix(),
	}
	access, err := signing.SignJWT(key, accessClaims)
	if err != nil {
		s.fail(w, r, httpx.CodeInternalError, "令牌签发失败")
		return
	}
	idClaims := map[string]any{
		"iss": s.d.Issuer, "sub": p.Sub, "aud": p.ClientID,
		"acr": p.ACR, "amr": p.AMR, "sid": sid,
		"iat": now.Unix(), "exp": now.Add(policy.AccessTTL).Unix(),
	}
	if p.Nonce != "" {
		idClaims["nonce"] = p.Nonce
	}
	idToken, err := signing.SignJWT(key, idClaims)
	if err != nil {
		s.fail(w, r, httpx.CodeInternalError, "令牌签发失败")
		return
	}
	rtRaw, _ := json.Marshal(codePayload{
		Sub: p.Sub, SID: sid, ClientID: p.ClientID, ACR: p.ACR, AMR: p.AMR,
	})
	refresh, err := randomToken()
	if err != nil {
		s.fail(w, r, httpx.CodeInternalError, "刷新令牌生成失败")
		return
	}
	if err := s.d.Codes.Put(r.Context(), RefreshKey(refresh), string(rtRaw), policy.RefreshTTL); err != nil {
		s.fail(w, r, mapStoreErr(err, httpx.CodeRedisUnavailable), "令牌存储不可用")
		return
	}
	writeJSON(w, s.d.Logger, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer",
		"expires_in":    int64(policy.AccessTTL.Seconds()),
		"refresh_token": refresh,
		"id_token":      idToken,
		"scope":         "openid",
	})
}

// introspect 与 userinfo 都是**本地解析**：只验签 + 校验 exp，不查库（AC-LOGIN-08 / D8-D′）。
func (s *Server) introspect(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, httpx.CodeInvalidRequest, "请求体不是合法表单")
		return
	}
	claims, ok := s.parseAccessToken(r, r.PostFormValue("token"))
	if !ok {
		writeJSON(w, s.d.Logger, http.StatusOK, map[string]any{"active": false})
		return
	}
	writeJSON(w, s.d.Logger, http.StatusOK, map[string]any{
		"active": true, "sub": claims["sub"], "client_id": claims["cid"],
		"acr": claims["acr"], "amr": claims["amr"], "sid": claims["sid"], "exp": claims["exp"],
	})
}

func (s *Server) userinfo(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if token == "" {
		s.fail(w, r, httpx.CodeMissingParameter, "缺少 Bearer 令牌")
		return
	}
	claims, ok := s.parseAccessToken(r, token)
	if !ok {
		s.fail(w, r, httpx.CodeTokenInvalid, "令牌无效或已过期")
		return
	}
	writeJSON(w, s.d.Logger, http.StatusOK, map[string]any{
		"sub": claims["sub"], "acr": claims["acr"], "amr": claims["amr"], "sid": claims["sid"],
	})
}

// parseAccessToken 只做"验签 + exp"两件事：任何查库都会破坏 /userinfo 的零查库判据。
func (s *Server) parseAccessToken(r *http.Request, token string) (map[string]any, bool) {
	key, err := s.d.Keys.For(signing.PurposeOIDC)
	if err != nil {
		return nil, false
	}
	claims, err := signing.VerifyJWT(key.Public, token)
	if err != nil {
		return nil, false
	}
	exp, ok := claims["exp"].(float64)
	if !ok || s.d.Now().UTC().Unix() >= int64(exp) {
		return nil, false
	}
	return claims, true
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, httpx.CodeInvalidRequest, "请求体不是合法表单")
		return
	}
	tok := r.PostFormValue("token")
	if tok == "" {
		s.fail(w, r, httpx.CodeMissingParameter, "缺少 token")
		return
	}
	// 刷新令牌：删键即撤销；访问令牌的实时撤销走会话（S7），这里记为审计事实。
	if err := s.d.Codes.Delete(r.Context(), RefreshKey(tok)); err != nil {
		s.fail(w, r, mapStoreErr(err, httpx.CodeRedisUnavailable), "撤销存储不可用")
		return
	}
	s.audit(r, AuditEvent{EventType: "revoke", Result: "success", Reason: "token_revoked"})
	w.WriteHeader(http.StatusOK)
}

// ---- 内部小工具 ----

type protoError struct{ code httpx.Code }

func (e *protoError) Error() string { return string(e.code) }

func verifyPKCE(challenge, verifier string) error {
	if verifier == "" || len(verifier) < 43 || len(verifier) > 128 {
		// D3-E：缺/畸形 verifier → `INVALID_CODE_VERIFIER`（400），与"不匹配"分开。
		return &protoError{code: httpx.CodeInvalidCodeVerifier}
	}
	sum := sha256.Sum256([]byte(verifier))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		return &protoError{code: httpx.CodePKCEFailed}
	}
	return nil
}

func mapStoreErr(err error, fallsBack httpx.Code) httpx.Code {
	if errors.Is(err, ErrStoreUnavailable) {
		return httpx.CodeRedisUnavailable
	}
	return fallsBack
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

type auditState struct {
	r        *http.Request
	clientID string
	userID   string
}

func (s *Server) audit(r *http.Request, ev AuditEvent) {
	if ev.TraceID == "" {
		ev.TraceID = traceID(r)
	}
	if err := s.d.Audit.Append(r.Context(), ev); err != nil {
		s.d.Logger.WarnContext(r.Context(), "protocol: audit append failed", "trace_id", ev.TraceID, "error", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, code httpx.Code, msg string) {
	httpx.WriteError(w, traceID(r), code, msg, nil)
}

func traceID(r *http.Request) string { return httpx.TraceIDFromContext(r.Context()) }

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func writeJSON(w http.ResponseWriter, logger *slog.Logger, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Warn("protocol: write response failed", "error", err)
	}
}

var _ = url.Values{}
var _ = fmt.Sprintf
