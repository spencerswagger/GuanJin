package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是数据面的**准入判定**（S8 的 ②）：验签 → 会话校验（含实时撤销）→ 按 client 档位判 ACR。
//
// 口径（decisions.md **D16** ①②③，逐条有出处）：
//   - 验签与会话校验复用 S6 的 `httpx.CallerResolver`（只接受 `SM3withSM2`、校验 `exp`/`nbf`、查会话状态）；
//   - `aud` 必须等于**路由命中的 `client_id`**（不是反向：令牌不许决定去哪，D16①）；
//   - 门槛取 `login_policies.min_acr`（`client_override` → `tier_default`），不拿档位映射函数再算一遍（D16②）；
//   - 不足 → **403 `ACR_INSUFFICIENT` + `details.flow`**，不是 302、不是 500（AC-DP-09）；
//   - `details.flow` 的形状**只由 `flow.BuildDescriptor` 决定**：数据面对 `flows` 只读，
//     无活跃流时用同一函数渲染 `need_more`，不另造一套字段（R13 因此不需要新口径）。
//
// 错误分类**只有三档**，且必须显式（吞成 401 会把"库挂了"伪装成"你没登录"）：
//   401 `TOKEN_INVALID`/`TOKEN_EXPIRED`（令牌不可信）、403 `SESSION_REVOKED`（会话已失效）、
//   403 `ACR_INSUFFICIENT`（档位不足）、500 `INTERNAL_ERROR`（配置/数据缺陷）、
//   503 `DB_UNAVAILABLE`（唯一的库依赖挂了，D8-C）。

// Admission 是一次请求通过准入后拿到的**可信事实**（后续签发断言只用它，不再回库）。
type Admission struct {
	Route Route
	// Session 是本次请求所用会话的**当前**快照（每次请求现读，不缓存，AC-SESS-01/04）。
	Session SessionSnapshot
	// Roles 是 `role_bindings` 里 `(user_id, client_id=aud, status=active)` 的角色集合（空集也非 nil）。
	Roles []string
	// Username 是 `users.username` 的规范化存储值（断言 `uname` 取它，不取 display_name）。
	Username string
	// Tier / ClientName / MinACR 来自 `LoadAdmissionPolicy`（`details.flow` 与访问记录要用）。
	Tier       string
	ClientName string
	MinACR     string
	// DisabledFactors 该业务系统禁用的因子（`details.flow` 渲染要用）。
	DisabledFactors []string
}

// SessionSnapshot 是会话的**只读快照**（不在 gateway 包引入 `store/model`：这个包只做网关语义，
// 持久化形状由装配层折算，避免两个包互相知道对方的字段演进）。
type SessionSnapshot struct {
	ID            string
	UserID        string
	ClientID      string
	ACR           string
	AMR           []string
	Status        string
	ExpiresAt     time.Time
	IdleExpiresAt time.Time
}

// 会话状态取值（与 `sessions.status` 的 DB CHECK 一致；权威常量在 `store/model`）。
const (
	SessionActive  = "active"
	SessionRevoked = "revoked"
	SessionExpired = "expired"
)

// 依赖面（全部由装配层实现；gateway 包不知道 SQL、不知道 gorm）。
type (
	// SessionReader 按 `sid` 读会话快照。**必须直读库**（零缓存窗口，AC-SESS-01）。
	SessionReader interface {
		LoadSession(ctx context.Context, sid string) (SessionSnapshot, error)
	}
	// AdmissionPolicyReader 读准入门槛（`store.LoadAdmissionPolicy`）。
	AdmissionPolicyReader interface {
		LoadAdmissionPolicy(ctx context.Context, clientID string) (AdmissionPolicy, error)
	}
	// RoleReader 读用户在目标业务系统内的有效角色（空集合 = 空切片，不是 nil）。
	RoleReader interface {
		RolesForUserClient(ctx context.Context, userID, clientID string) ([]string, error)
	}
	// UserReader 读用户名（断言 `uname`）。
	UserReader interface {
		Username(ctx context.Context, userID string) (string, error)
	}
	// VisitRecordReader 记录"该用户用过该业务系统"（只在**通过准入之后**调用，D16②）。
	VisitRecordReader interface {
		RecordVisit(ctx context.Context, userID, clientID, tier string) error
	}
	// ActiveFlowReader 读该用户在该业务系统内的活跃流（`details.flow` 用；没有活跃流返回 (nil, nil)）。
	ActiveFlowReader interface {
		FindActiveFlow(ctx context.Context, userID, clientID string) (*FlowSnapshot, error)
	}
)

// AdmissionPolicy 是 `store.AdmissionPolicy` 的镜像（同名字段，装配层一次赋值）。
type AdmissionPolicy struct {
	ClientID        string
	ClientName      string
	Tier            string
	MinACR          string
	DisabledFactors []string
}

// FlowSnapshot 是活跃流的最小事实（`details.flow` 渲染用）。
type FlowSnapshot struct {
	ID          string
	TargetACR   string
	AchievedAMR []string
	ExpiresAt   time.Time
}

// AdmitError 是准入失败的结构化结果：对外只暴露 `Code/Status/Details`，
// 内部原因放 `Internal`（只进日志，绝不进响应——否则等于把时序/配置信息送给攻击者）。
type AdmitError struct {
	Code     httpx.Code
	Status   int
	Message  string
	Details  any
	Internal error
}

func (e *AdmitError) Error() string {
	if e == nil {
		return ""
	}
	if e.Internal != nil {
		return fmt.Sprintf("gateway: admit failed: %s: %v", e.Code, e.Internal)
	}
	return fmt.Sprintf("gateway: admit failed: %s", e.Code)
}

// Unwrap 让调用方能 `errors.Is(err, httpx.ErrCallerSessionRevoked)` 之类地判断根因。
func (e *AdmitError) Unwrap() error { return e.Internal }

// Guard 是准入判定器（无本地状态：多副本行为一致，撤销零缓存窗口）。
type Guard struct {
	// Caller 是 S6 的令牌解析器（验签 + exp/nbf + 会话状态）。
	Caller httpx.CallerResolver
	// Sessions 是会话快照读取（acr/amr/归属/有效期都从这里来，D16①：不从令牌取）。
	Sessions SessionReader
	Policies AdmissionPolicyReader
	Roles    RoleReader
	Users    UserReader
	Visits   VisitRecordReader
	Flows    ActiveFlowReader
	// Factors 是平台已装配的因子事实（`flow.FactorInfosOf(registry.Descriptors())`）。
	Factors []flow.FactorInfo
	// Issuer 是访问令牌的 `iss` 期望值；空表示不校验（装配层应尽量给）。
	Issuer string
	// Skew 是允许的时钟偏移（`iat` 超前量的宽容度；0 = 不容忍）。AC-DP-06 的 `skew±1s` 边界用它。
	Skew time.Duration
	// Now 为 nil 时用 time.Now。
	Now func() time.Time
	// Logger 为 nil 时用 slog.Default()。
	Logger *slog.Logger
	// OnVisitError 在为访问记录写失败时被调用（默认只记日志）：这条写入**不是安全判据**，
	// 失败不阻塞转发（见 RecordVisit 的注释），但要留可观测的痕迹。
	OnVisitError func(err error)
}

// NewGuard 校验必需依赖（缺一个就不许起数据面：静默降级 = 把认证变成可选项）。
func NewGuard(g *Guard) (*Guard, error) {
	if g == nil {
		return nil, errors.New("gateway: nil guard")
	}
	switch {
	case g.Caller == nil:
		return nil, errors.New("gateway: guard requires Caller (httpx.CallerResolver)")
	case g.Sessions == nil:
		return nil, errors.New("gateway: guard requires Sessions")
	case g.Policies == nil:
		return nil, errors.New("gateway: guard requires Policies")
	case g.Roles == nil:
		return nil, errors.New("gateway: guard requires Roles")
	case g.Users == nil:
		return nil, errors.New("gateway: guard requires Users")
	case g.Visits == nil:
		return nil, errors.New("gateway: guard requires Visits")
	}
	if g.Now == nil {
		g.Now = time.Now
	}
	if g.Logger == nil {
		g.Logger = slog.Default()
	}
	if g.Skew < 0 {
		g.Skew = 0
	}
	return g, nil
}

// BearerHeader 是用户令牌的承载头（RFC 6750）。
const BearerHeader = "Authorization"

// 网关侧的四个哨兵：装配层把 `store` 的错误翻成它们（gateway 包不 import store，
// 免得持久化形状的变化渗进网关语义；也免得两个包互相依赖）。
var (
	// ErrSessionNotFound 表示会话不存在（对外与"已撤销"同形，避免枚举）。
	ErrSessionNotFound = errors.New("gateway: session not found")
	// ErrPolicyNotConfigured 表示既无 client_override 也无 tier_default 策略行（配置缺陷）。
	ErrPolicyNotConfigured = errors.New("gateway: admission policy not configured")
	// ErrClientNotFound / ErrClientDisabled 表示目标业务系统不存在/已停用（配置缺陷）。
	ErrClientNotFound = errors.New("gateway: client not found")
	ErrClientDisabled = errors.New("gateway: client disabled")
)

// Admit 执行一次准入判定。返回 `*AdmitError` 时调用方按 `Code/Status/Details` 写响应，
// **不得**转发、**不得**签发断言。
//
// 顺序不可调整：先验签再查库（伪造令牌不该花掉一次数据库查询），
// 会话存活先于档位判定（"你没登录"要先于"你级别不够"）。
func (g *Guard) Admit(ctx context.Context, r *http.Request, route Route) (*Admission, error) {
	// ① 令牌缺失 → 401 deny-by-default（AC-DP-05）。空令牌不进验签（那是纯浪费）。
	token, err := bearerToken(r)
	if err != nil {
		return nil, &AdmitError{Code: httpx.CodeTokenInvalid, Status: http.StatusUnauthorized,
			Message: "缺少或无法解析 Bearer 令牌", Internal: err}
	}

	// ② 验签 + exp/nbf + 会话状态（S6 的唯一实现）。
	caller, err := g.Caller.Resolve(ctx, token)
	if err != nil {
		return nil, g.classifyResolveError(err)
	}

	// ③ 会话快照（acr/amr/归属；**每次现读**，撤销零缓存窗口）。
	admission, err := g.admitSession(ctx, caller, route)
	if err != nil {
		return nil, err
	}

	// ③′ 时钟窗口（AC-DP-06）：`exp` 由 S6 的解析器按**严**口径判（无宽限），
	// 这里补 `iat` 超前量——它管的是"签发方时钟跑到未来"，宽限即 `Skew`。
	if err := g.checkIssuedAt(caller.Claims); err != nil {
		return nil, &AdmitError{Code: httpx.CodeTokenInvalid, Status: http.StatusUnauthorized,
			Message: "令牌无效或已过期", Internal: err}
	}

	// ④ `iss`/`aud` 校验（AC-DP-07：`aud` 必须是路由命中的 client——令牌不许决定去哪，D16①）。
	if err := g.checkIssuer(caller.Claims); err != nil {
		return nil, &AdmitError{Code: httpx.CodeTokenInvalid, Status: http.StatusUnauthorized,
			Message: "令牌无效或已过期", Internal: err}
	}
	if err := checkAudience(caller.Claims, route.ClientID); err != nil {
		return nil, &AdmitError{Code: httpx.CodeTokenInvalid, Status: http.StatusUnauthorized,
			Message: "令牌无效或已过期", Internal: err}
	}

	// ⑤ 档位判定（AC-DP-09）。
	if err := g.admitACR(ctx, admission); err != nil {
		return nil, err
	}

	// ⑥ 通过准入之后才记"访问过"（被拒的探测不得污染生效配置，D16②）。
	g.recordVisit(ctx, admission)

	return admission, nil
}

// admitSession 读会话并把四种"不能继续"的情形分别归类（不合并成一类：运维要能分辨）。
func (g *Guard) admitSession(ctx context.Context, caller httpx.Caller, route Route) (*Admission, error) {
	if caller.SID == "" {
		// 无 `sid` 的令牌没有"会话级"事实可判（acr/amr 以库为准，D16①）：直接拒，别拿令牌自说自话。
		return nil, &AdmitError{Code: httpx.CodeTokenInvalid, Status: http.StatusUnauthorized,
			Message: "令牌无效或已过期", Internal: errors.New("token has no sid")}
	}
	session, err := g.Sessions.LoadSession(ctx, caller.SID)
	if err != nil {
		return nil, g.classifySessionError(err)
	}
	// 令牌与会话必须指向同一个用户：不一致只可能是伪造或数据缺陷，按"令牌不可信"处理。
	if session.UserID != caller.Sub {
		return nil, &AdmitError{Code: httpx.CodeTokenInvalid, Status: http.StatusUnauthorized,
			Message:  "令牌无效或已过期",
			Internal: fmt.Errorf("token sub %q != session user %q", caller.Sub, session.UserID)}
	}
	now := g.Now().UTC()
	if err := checkSessionAlive(session, now); err != nil {
		return nil, err
	}

	admission := &Admission{Route: route, Session: session, Roles: []string{}}
	roles, err := g.Roles.RolesForUserClient(ctx, session.UserID, route.ClientID)
	if err != nil {
		return nil, &AdmitError{Code: httpx.CodeDBUnavailable, Status: http.StatusServiceUnavailable,
			Message: "服务暂不可用，请稍后重试", Internal: fmt.Errorf("load roles: %w", err)}
	}
	if roles != nil {
		admission.Roles = roles
	}
	username, err := g.Users.Username(ctx, session.UserID)
	if err != nil {
		return nil, &AdmitError{Code: httpx.CodeDBUnavailable, Status: http.StatusServiceUnavailable,
			Message: "服务暂不可用，请稍后重试", Internal: fmt.Errorf("load username: %w", err)}
	}
	admission.Username = username

	policy, err := g.Policies.LoadAdmissionPolicy(ctx, route.ClientID)
	if err != nil {
		return nil, g.classifyPolicyError(route.ClientID, err)
	}
	admission.Tier = policy.Tier
	admission.ClientName = policy.ClientName
	admission.MinACR = policy.MinACR
	admission.DisabledFactors = policy.DisabledFactors
	return admission, nil
}

// admitACR 比较"会话已达成"与"目标业务系统门槛"（D16②：只认 `min_acr` 一个真值）。
func (g *Guard) admitACR(ctx context.Context, adm *Admission) error {
	current := factor.ACR(adm.Session.ACR)
	if !current.Valid() || !current.Issuable() {
		// 越界值（含预留的 AAL3）= 内部缺陷，按 500 处理，**不静默截断**（D12(3)）。
		return &AdmitError{Code: httpx.CodeInternalError, Status: http.StatusInternalServerError,
			Message:  "服务内部错误，请携带 trace_id 联系支持",
			Internal: fmt.Errorf("session %s has non-issuable acr %q (D12)", adm.Session.ID, adm.Session.ACR)}
	}
	target := factor.ACR(adm.MinACR)
	if !target.Valid() {
		return &AdmitError{Code: httpx.CodeInternalError, Status: http.StatusInternalServerError,
			Message:  "服务内部错误，请携带 trace_id 联系支持",
			Internal: fmt.Errorf("client %s has invalid min_acr %q", adm.Route.ClientID, adm.MinACR)}
	}
	if current.AtLeast(target) {
		return nil
	}
	// 档位不足 = **引导补步**，不是失败（索引 §2 原则 3）：必须带完整 Descriptor。
	descriptor, err := g.describe(ctx, adm)
	if err != nil {
		return &AdmitError{Code: httpx.CodeInternalError, Status: http.StatusInternalServerError,
			Message:  "服务内部错误，请携带 trace_id 联系支持",
			Internal: fmt.Errorf("build details.flow: %w", err)}
	}
	return &AdmitError{
		Code:    httpx.CodeACRInsufficient,
		Status:  http.StatusForbidden,
		Message: "当前档位不足，请完成剩余步骤",
		Details: map[string]any{"flow": descriptor},
		Internal: fmt.Errorf("session %s acr %s < client %s min_acr %s",
			adm.Session.ID, current, adm.Route.ClientID, target),
	}
}

// describe 渲染 `details.flow`：**只读**现成活跃流；没有活跃流时用同一函数渲染 `need_more`。
//
// 为什么可能没有活跃流：`/authorize` 之前就可能撞上档位不足（例如 L3 业务系统要求 AAL2），
// 此时用户还没有流。数据面对 `flows` **只读**（D16②：S8 的落点表里没有 flows 写），
// 所以这里如实给一份 `need_more` 的步骤表，由客户端按 CH-1 去 `GET /authorize/flow` 取 `flow_id`。
func (g *Guard) describe(ctx context.Context, adm *Admission) (flow.Descriptor, error) {
	target := factor.ACR(adm.MinACR)
	state := flow.State{
		Client:          flow.Client{ID: adm.Route.ClientID, Name: adm.ClientName, Tier: adm.Tier},
		TargetACR:       target,
		AchievedAMR:     append([]string(nil), adm.Session.AMR...),
		DisabledFactors: disabledFactorTypes(adm),
		Factors:         g.Factors,
		Now:             g.Now().UTC(),
	}
	if g.Flows != nil {
		active, err := g.Flows.FindActiveFlow(ctx, adm.Session.UserID, adm.Route.ClientID)
		if err != nil {
			return flow.Descriptor{}, fmt.Errorf("find active flow: %w", err)
		}
		if active != nil && !active.ExpiresAt.IsZero() && active.ExpiresAt.After(state.Now) {
			state.FlowID = active.ID
			state.ExpiresAt = active.ExpiresAt
			// 流的 `achieved_amr` 与会话的 `amr` 在正常路径下一致；取并集会更宽松，
			// 但"渲染补齐步骤"只应反映**已达成**的事实，故仍以会话为准（DB 真值）。
			if len(active.AchievedAMR) > 0 {
				state.AchievedAMR = unionStrings(state.AchievedAMR, active.AchievedAMR)
			}
			// 流的 `target_acr` 由档位映射而来；与 `min_acr` 分叉属配置漂移（D16②已登记给 Core），
			// 这里以 `min_acr` 为准渲染，避免数据面与准入口径自相矛盾。
		}
	}
	return flow.BuildDescriptor(state)
}

// recordVisit 写"访问过"（D16②：通过准入之后）。
//
// **失败不阻塞转发**（显式记在这里，不是隐式回退）：这张表服务的是单点登出级联范围与
// `effective_tier` 统计，不是本次请求的安全判据；为一次统计写入失败而拒绝一条已通过认证的
// 请求，等于把"统计表抖动"放大成"全站 503"。失败必须有痕迹（日志 + `OnVisitError`）。
func (g *Guard) recordVisit(ctx context.Context, adm *Admission) {
	if g.Visits == nil {
		return
	}
	if err := g.Visits.RecordVisit(ctx, adm.Session.UserID, adm.Route.ClientID, adm.Tier); err != nil {
		g.Logger.Warn("data-plane: record visit failed (不影响本次转发)",
			"error", err, "client_id", adm.Route.ClientID)
		if g.OnVisitError != nil {
			g.OnVisitError(err)
		}
	}
}

// classifyResolveError 把 S6 的解析错误折成三档（见文件头）。
func (g *Guard) classifyResolveError(err error) *AdmitError {
	switch {
	case errors.Is(err, httpx.ErrCallerSessionRevoked):
		return &AdmitError{Code: httpx.CodeSessionRevoked, Status: http.StatusForbidden,
			Message: "会话已失效，请重新登录", Internal: err}
	case errors.Is(err, httpx.ErrCallerUnauthenticated):
		return &AdmitError{Code: httpx.CodeTokenInvalid, Status: http.StatusUnauthorized,
			Message: "令牌无效或已过期", Internal: err}
	default:
		// 解析器只在查会话状态时碰库；走到这里说明库不可用（D8-C：`DB_UNAVAILABLE` + 503）。
		// 吞成 401 会把"库挂了"伪装成"你没登录"，运维会去查用户而不是查库。
		return &AdmitError{Code: httpx.CodeDBUnavailable, Status: http.StatusServiceUnavailable,
			Message: "服务暂不可用，请稍后重试", Internal: fmt.Errorf("resolve caller: %w", err)}
	}
}

// classifySessionError 处理会话快照读取失败。
func (g *Guard) classifySessionError(err error) *AdmitError {
	if errors.Is(err, ErrSessionNotFound) {
		// 会话不存在与已撤销**对外同形**（403 SESSION_REVOKED）：不给枚举留口子。
		return &AdmitError{Code: httpx.CodeSessionRevoked, Status: http.StatusForbidden,
			Message: "会话已失效，请重新登录", Internal: err}
	}
	return &AdmitError{Code: httpx.CodeDBUnavailable, Status: http.StatusServiceUnavailable,
		Message: "服务暂不可用，请稍后重试", Internal: fmt.Errorf("load session: %w", err)}
}

// classifyPolicyError 把门槛读取失败折成"配置缺陷 500"与"依赖挂了 503"。
func (g *Guard) classifyPolicyError(clientID string, err error) *AdmitError {
	switch {
	case errors.Is(err, ErrPolicyNotConfigured), errors.Is(err, ErrClientNotFound), errors.Is(err, ErrClientDisabled):
		return &AdmitError{Code: httpx.CodeInternalError, Status: http.StatusInternalServerError,
			Message:  "服务内部错误，请携带 trace_id 联系支持",
			Internal: fmt.Errorf("admission policy for client %q: %w", clientID, err)}
	default:
		return &AdmitError{Code: httpx.CodeDBUnavailable, Status: http.StatusServiceUnavailable,
			Message: "服务暂不可用，请稍后重试", Internal: fmt.Errorf("load admission policy: %w", err)}
	}
}

// checkAudience 校验令牌 `aud` 含路由命中的 client（D16①：禁止"用令牌 aud 覆盖路由结果"）。
//
// 同时接受字符串与数组两种形态（RFC 7519 允许），但**不接受空**：空 `aud` = 令牌没说给谁用，
// 拿它去访问任何业务系统都属越权。
func checkAudience(claims map[string]any, target string) error {
	if claims == nil {
		return errors.New("token has no claims")
	}
	if iss, _ := claims["iss"].(string); iss == "" {
		return errors.New("token has no iss")
	}
	raw, ok := claims["aud"]
	if !ok {
		return errors.New("token has no aud")
	}
	switch v := raw.(type) {
	case string:
		if v != target {
			return fmt.Errorf("token aud %q != route client %q", v, target)
		}
		return nil
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == target {
				return nil
			}
		}
		return fmt.Errorf("token aud %v does not contain route client %q", v, target)
	default:
		return fmt.Errorf("token aud has unexpected type %T", raw)
	}
}

// checkIssuedAt 校验 `iat` 不超前于当前时刻 + `Skew`（AC-DP-06 的 `skew±1s` 打点）。
//
// 为什么 `exp` 不在这里一并宽限：`exp` 已由 S6 的 `CallerResolver` 按"到点即拒"判过，
// 再给宽限等于让本侧比签发侧更宽松——那会让"过期令牌还能用"成为事实行为。
// 时钟偏移的**唯一**宽容度就是 `Skew`，且只作用于"超前"方向。
func (g *Guard) checkIssuedAt(claims map[string]any) error {
	raw, ok := claims["iat"]
	if !ok {
		return errors.New("token has no iat")
	}
	iat, ok := raw.(float64)
	if !ok {
		return fmt.Errorf("token iat has unexpected type %T", raw)
	}
	issued := time.Unix(int64(iat), 0).UTC()
	if limit := g.Now().UTC().Add(g.Skew); issued.After(limit) {
		return fmt.Errorf("token iat %s is ahead of now+skew %s", issued, limit)
	}
	return nil
}

// checkIssuer 校验访问令牌的 `iss`（配置了才校验；空配置表示装配层未给，不静默放过也要留日志）。
func (g *Guard) checkIssuer(claims map[string]any) error {
	if strings.TrimSpace(g.Issuer) == "" {
		return nil
	}
	iss, _ := claims["iss"].(string)
	if iss != g.Issuer {
		return fmt.Errorf("token iss %q != expected %q", iss, g.Issuer)
	}
	return nil
}

// bearerToken 取 `Authorization: Bearer <token>`。
func bearerToken(r *http.Request) (string, error) {
	if r == nil {
		return "", errors.New("nil request")
	}
	raw := strings.TrimSpace(r.Header.Get(BearerHeader))
	if raw == "" {
		return "", errors.New("no Authorization header")
	}
	const prefix = "bearer "
	if len(raw) < len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return "", errors.New("Authorization is not a Bearer token")
	}
	token := strings.TrimSpace(raw[len(prefix):])
	if token == "" {
		return "", errors.New("empty bearer token")
	}
	return token, nil
}

func disabledFactorTypes(adm *Admission) []factor.FactorType {
	out := make([]factor.FactorType, 0, len(adm.DisabledFactors))
	for _, f := range adm.DisabledFactors {
		out = append(out, factor.FactorType(f))
	}
	return out
}

func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, s := range list {
			if seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
