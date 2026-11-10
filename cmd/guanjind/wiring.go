package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/factor/holding"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/flowstore"
	"github.com/spencerswagger/guanjin/internal/identity"
	"github.com/spencerswagger/guanjin/internal/protocol"
	"github.com/spencerswagger/guanjin/internal/risk"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/signing"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是**装配层**：把仓储、因子、风控、编排器接成一根可用的业务线。
//
// 它只做"接线"，不做判断：档位怎么映射、禁用集怎么算、错误怎么映射，全部在各自包里
// （`flow` 判语义、`flowstore` 做类型与错误转换、`store` 出 SQL）。这里出现业务判断
// 就说明有一处口径被抄到了第二个地方。
//
// 为什么集中在一个函数里返回：装配期就是 fail-fast 的唯一时机 —— 缺仓储、缺因子、
// 缺 TTL 应当**进程起不来**，而不是等第一个用户登录时才 500。

// wiring 是装配产物（三个 HTTP 面 + 一个共享的风控守卫）。
type wiring struct {
	flowHandler     *flow.HTTPHandler
	registerHandler *identity.RegisterHTTP
	// meIdentifiers 是自助面 `POST /api/v1/me/identifiers`（AC-FLOW-09）。
	meIdentifiers *identity.MeIdentifiersHTTP
	riskGuard     *risk.Guard
	// registry 是启动期建好的因子注册表：管理面 `GET/PATCH /api/v1/factors` 要复用它，
	// **不能**第二个地方再 Register 一遍（两份注册表 = 两份能力声明，迟早不一致）。
	registry *factor.Registry
}

// newWiring 装出本进程的全部业务面。
func newWiring(ctx context.Context, db *gorm.DB, cfg *config.Config, startup store.StartupConfig,
	resolver session.TokenResolver, logger *slog.Logger) (*wiring, error) {
	flowHandler, guard, registry, err := newFlowHandler(ctx, db, cfg, startup, logger)
	if err != nil {
		return nil, err
	}
	registerHandler, meIdentifiers, err := newRegisterHandler(ctx, db, cfg, startup, resolver, logger)
	if err != nil {
		return nil, err
	}
	return &wiring{flowHandler: flowHandler, registerHandler: registerHandler, meIdentifiers: meIdentifiers,
		riskGuard: guard, registry: registry}, nil
}

// newFlowHandler 装出因子与挑战面（S5）。
func newFlowHandler(ctx context.Context, db *gorm.DB, cfg *config.Config, startup store.StartupConfig, logger *slog.Logger) (*flow.HTTPHandler, *risk.Guard, *factor.Registry, error) {
	flowRepo := store.NewFlowRepo(db)
	flowSvc, err := flowstore.New(db, flowRepo, startup.FlowTTL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: flowstore: %w", err)
	}
	flowAdapter, err := flowstore.NewAdapter(flowSvc, flowRepo)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: flow adapter: %w", err)
	}
	challengeAdapter, err := flowstore.NewChallengeAdapter(store.NewChallengeRepo(db))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: challenge adapter: %w", err)
	}

	// 账号级失败计数（Redis 权威，D3-D）。**启动期不 ping**：Redis 挂了进程要活着、
	// 业务全拒（`/readyz` 与请求路径负责），否则会连探针都没有。
	guard, err := risk.New(cfg.RedisAddr, cfg.RedisPassword)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: risk guard: %w", err)
	}

	credStore, err := identity.NewCredentialStore(db)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: credential store: %w", err)
	}
	// 认证路径只做校验，不判口令策略（策略在注册/改密时有 client 上下文才判）——
	// 所以用 `NewPasswordVerifier`，不为了过构造器自检而伪造一份策略数字。
	password, err := holding.NewPasswordVerifier(credStore)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: password factor: %w", err)
	}
	registry := factor.NewRegistry()
	descriptors, err := holding.Register(registry, password)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: factor registry: %w", err)
	}

	policies := store.NewClientPolicyRepo(db)
	// 有效禁用 = 平台级（runtime_config `factors.disabled`）∪ 客户端级（login_policies）；
	// 两级互不覆盖，任一级禁用即不可用（AC-FLOW-05）。
	platformFactors, err := store.NewPlatformFactorReader(db)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: platform factor reader: %w", err)
	}
	// **未知因子类型：保留不删，但必须点名**（2026-11-11 定稿口径）。
	//
	// 为什么保留：滚动升级时"新版本已加的类型"旧节点不认识；一旦在读-改-写里丢掉，
	// 那个类型在其它节点上就会**意外变为可用**（安全降级）。
	// 为什么必须 WARN：数组里出现未知类型（尤其把类型名写错）意味着"本该禁用的那个因子
	// 其实没被禁用"，只有一行 info 没人会看。
	// 读失败**不降级**：缺行/坏值由 `ValidateStartup`（`factors.disabled` 已在 startupSpecs 里）
	// 更早 fail-fast；这里再读一次只是为了点名，失败就照样让进程起不来。
	{
		disabledNow, derr := platformFactors.Disabled(ctx)
		if derr != nil {
			return nil, nil, nil, fmt.Errorf("startup: read %s: %w", store.KeyFactorsDisabled, derr)
		}
		known := make([]string, 0, len(registry.Types()))
		for _, t := range registry.Types() {
			known = append(known, string(t))
		}
		if unknown := store.UnknownFactorTypes(disabledNow, known); len(unknown) > 0 {
			logger.Warn("startup: 平台禁用集里有本进程未注册的因子类型（保留不删；可能来自更高版本的节点，或类型名写错）",
				"unknown", unknown, "factors_disabled", disabledNow, "known", known)
		}
	}
	orc, err := flow.New(flow.Deps{
		Reader:     flowAdapter,
		Starter:    flowAdapter,
		Advancer:   flowAdapter,
		Consumer:   flowAdapter,
		Challenges: challengeAdapter,
		Verifiers: map[factor.FactorType]factor.Authenticatable{
			factor.FactorPassword: password,
		},
		Factors:         flow.FactorInfosOf(descriptors),
		DisabledFactors: mergeDisabledFactors(disabledFactorsOf(policies), platformFactors.PlatformDisabledFactors),
		TierOf:          tierOf(policies),
		ClientOf:        clientOf(policies),
		Guard:           &riskGuardAdapter{guard: guard},
		Lockout:         lockoutOf(policies),
		Now:             time.Now,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: flow orchestrator: %w", err)
	}
	handler, err := flow.NewHTTPHandler(orc, logger)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("startup: flow handler: %w", err)
	}
	return handler, guard, registry, nil
}

// newRegisterHandler 装出注册面（S4：`/auth/register/start|complete`）。
func newRegisterHandler(ctx context.Context, db *gorm.DB, cfg *config.Config, startup store.StartupConfig,
	resolver session.TokenResolver, logger *slog.Logger) (*identity.RegisterHTTP, *identity.MeIdentifiersHTTP, error) {
	indexKey, err := cfg.MasterKey.DeriveHMAC(crypto.PurposeHMACIdentifier)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: derive identifier key: %w", err)
	}
	fieldKey, err := cfg.MasterKey.DeriveSM4(crypto.PurposeSM4Field)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: derive field key: %w", err)
	}
	registerKey, err := cfg.MasterKey.DeriveHMAC(crypto.PurposeHMACRegister)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: derive register token key: %w", err)
	}
	svc, err := identity.New(db, identity.Config{
		IndexKey:         indexKey,
		FieldKey:         fieldKey,
		RegisterTokenKey: registerKey,
		// 口令派生参数：平台冻结下限（iterations/keylen 都是基线值，不是"顺手写的数"）。
		PasswordParams:       crypto.PasswordParams{Iterations: crypto.MinPBKDF2Iterations, KeyLen: crypto.MinPasswordKeyLen},
		FlowTTL:              startup.FlowTTL,
		ChallengeTTL:         startup.ChallengeTTL,
		ChallengeMaxAttempts: startup.ChallengeMaxAttempts,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("startup: identity service: %w", err)
	}
	handler, err := identity.NewRegisterHTTP(svc, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: register handler: %w", err)
	}
	// 自助面 `POST /api/v1/me/identifiers`（AC-FLOW-09）：ACR 前置按"访问过的最高档位"推。
	visited, err := store.NewEffectiveACRRepo(db)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: effective ACR repo: %w", err)
	}
	gate, err := identity.NewACRGate(visited, store.PlatformDefaultTier)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: ACR gate: %w", err)
	}
	meHandler, err := identity.NewMeIdentifiersHTTP(svc, store.NewSessionOpsRepo(db), gate, resolver, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: me/identifiers handler: %w", err)
	}
	return handler, meHandler, nil
}

// riskGuardAdapter 把 `internal/risk` 接到编排层的窄接口上。
//
// 它只做一件事：**把 risk 的错误翻成编排层哨兵**（`ErrAccountLocked` / `ErrRiskUnavailable`），
// 否则 `CodeOf` 不认识它们，会落到 500，而 AC-LOGIN-03 要的是 401 ACCOUNT_LOCKED、
// Redis 挂要的是 503 REDIS_UNAVAILABLE。
type riskGuardAdapter struct {
	guard *risk.Guard
}

// Status 见 flow.AccountGuard。
func (a *riskGuardAdapter) Status(ctx context.Context, userID string, p flow.LockoutPolicy) (int, bool, error) {
	left, locked, err := a.guard.Status(ctx, userID, toRiskPolicy(p))
	return left, locked, mapRiskError(err)
}

// RecordFailure 见 flow.AccountGuard。
func (a *riskGuardAdapter) RecordFailure(ctx context.Context, userID string, p flow.LockoutPolicy) (int, bool, error) {
	left, locked, err := a.guard.RecordFailure(ctx, userID, toRiskPolicy(p))
	return left, locked, mapRiskError(err)
}

// Reset 见 flow.AccountGuard。
func (a *riskGuardAdapter) Reset(ctx context.Context, userID string) error {
	return mapRiskError(a.guard.Reset(ctx, userID))
}

func toRiskPolicy(p flow.LockoutPolicy) risk.Policy {
	return risk.Policy{FailThreshold: p.FailThreshold, LockDuration: p.LockDuration}
}

// mapRiskError 是"风控错误 → 编排哨兵"的唯一转换点。
func mapRiskError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, risk.ErrLocked):
		return flow.ErrAccountLocked
	case errors.Is(err, risk.ErrUnavailable):
		return flow.ErrRiskUnavailable
	default:
		// 策略非法属配置缺陷：原样上抛（500），不伪装成业务码。
		return err
	}
}

// disabledFactorsOf 读某业务系统的禁用因子（`login_policies.disabled_factors`）。
//
// 未知因子名**直接报错**（不忽略）：库里出现平台不认识的因子名，说明策略被写坏了，
// 静默忽略等于"禁用了一条不存在的规则"，而用户会以为它生效了。
func disabledFactorsOf(policies *store.ClientPolicyRepo) func(context.Context, string) ([]factor.FactorType, error) {
	return func(ctx context.Context, clientID string) ([]factor.FactorType, error) {
		policy, err := policies.LoadRegistrationPolicy(ctx, nil, clientID)
		if err != nil {
			return nil, clientError(err)
		}
		return parseFactorTypes(policy.DisabledFactors)
	}
}

// tierOf 读业务系统档位（`clients.tier`，随策略快照一起读出）。
func tierOf(policies *store.ClientPolicyRepo) func(context.Context, string) (string, error) {
	return func(ctx context.Context, clientID string) (string, error) {
		policy, err := policies.LoadRegistrationPolicy(ctx, nil, clientID)
		if err != nil {
			return "", clientError(err)
		}
		return policy.Tier, nil
	}
}

// clientOf 读 Descriptor 里展示用的业务系统摘要。
//
// 只填 `id`/`tier`：`name` 目前没有读模型暴露（`clients.name` 属管理面读侧，S6 一起补），
// 留空而不是拿 id 顶替——`name` 字段是 `omitempty`，留空就是"没有"，不编造。
func clientOf(policies *store.ClientPolicyRepo) func(context.Context, string) (flow.Client, error) {
	return func(ctx context.Context, clientID string) (flow.Client, error) {
		policy, err := policies.LoadRegistrationPolicy(ctx, nil, clientID)
		if err != nil {
			return flow.Client{}, clientError(err)
		}
		return flow.Client{ID: clientID, Tier: policy.Tier}, nil
	}
}

// lockoutOf 读失败处理参数（`login_policies.fail_threshold` / `lock_duration_min`）。
func lockoutOf(policies *store.ClientPolicyRepo) func(context.Context, string) (flow.LockoutPolicy, error) {
	return func(ctx context.Context, clientID string) (flow.LockoutPolicy, error) {
		p, err := policies.LoadLockoutPolicy(ctx, clientID)
		if err != nil {
			return flow.LockoutPolicy{}, clientError(err)
		}
		return flow.LockoutPolicy{
			FailThreshold: p.FailThreshold,
			LockDuration:  time.Duration(p.LockDurationMinutes) * time.Minute,
		}, nil
	}
}

// clientError 把仓储的"业务系统不存在/已停用"翻成编排层的哨兵。
//
// 不翻的话它会落到 handler 的 default 分支变成 500，而接口 §3.1.4 明确 `INVALID_CLIENT`（E1/400）。
func clientError(err error) error {
	if errors.Is(err, store.ErrClientNotFound) || errors.Is(err, store.ErrClientDisabled) {
		return flow.ErrInvalidClient
	}
	return err
}

// parseFactorTypes 把库里的因子名折成 `factor.FactorType`（校验格式，不认的即报错）。
func parseFactorTypes(raw []string) ([]factor.FactorType, error) {
	out := make([]factor.FactorType, 0, len(raw))
	for _, name := range raw {
		t := factor.FactorType(name)
		if err := t.Validate(); err != nil {
			return nil, fmt.Errorf("startup: login_policies.disabled_factors: %w", err)
		}
		out = append(out, t)
	}
	return out, nil
}

// newProtocolHandler 装出协议面（S6）。
//
// 启动期顺序（任何一步失败都非 0 退出）：读 `signing_keys` → **解封**（坏密钥不起来）→
// 校验两个用途都有可用键（缺键提示跑 `guanjind keygen`）→ 才构造处理器。
func newProtocolHandler(ctx context.Context, db *gorm.DB, cfg *config.Config, startup store.StartupConfig, logger *slog.Logger) (*protocol.Server, error) {
	keys, err := loadSigningKeys(ctx, db, cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Issuer == "" {
		return nil, errors.New("startup: GUANJIN_ISSUER is required to serve the protocol endpoints")
	}
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	codeStore, err := protocol.NewRedisStore(rdb)
	if err != nil {
		return nil, fmt.Errorf("startup: code store: %w", err)
	}
	policies := store.NewClientPolicyRepo(db)
	// 授权端点需要编排器：复用 flow 那条装配（同进程内共享，避免两套 Descriptor 口径）。
	flowHandler, _, _, err := newFlowHandler(ctx, db, cfg, startup, logger)
	if err != nil {
		return nil, err
	}
	orch := flowHandler.Orchestrator()
	caller, err := newCallerResolver(keys, db)
	if err != nil {
		return nil, err
	}
	return protocol.New(protocol.Deps{
		Keys:       keys,
		Codes:      codeStore,
		Sessions:   &sessionIssuer{repo: store.NewSessionRepo(db), policies: policies},
		Clients:    &clientPolicies{repo: policies},
		Audit:      &auditSink{repo: store.NewAuthEventRepo(db)},
		Issuer:     cfg.Issuer,
		Logger:     logger,
		Now:        time.Now,
		Flows:      &flowStarter{orch: orch},
		FlowVerify: &flowVerify{orch: orch},
		Redirects:  &redirects{repo: policies},
		FlowCode:   flow.CodeOf,
		FlowTTL:    startup.FlowTTL,
		Caller:     caller,
	})
}

// clientPolicies 把 `login_policies` 的 A/B 组折成协议面要的 TTL 与 ACR。
type clientPolicies struct{ repo *store.ClientPolicyRepo }

func (c *clientPolicies) Policy(ctx context.Context, clientID string) (protocol.ClientPolicy, error) {
	p, err := c.repo.LoadTokenPolicy(ctx, clientID)
	if err != nil {
		return protocol.ClientPolicy{}, clientError(err)
	}
	return protocol.ClientPolicy{
		ACR:         p.MinACR,
		AccessTTL:   time.Duration(p.AccessTokenTTLSec) * time.Second,
		RefreshTTL:  time.Duration(p.RefreshTokenTTLSec) * time.Second,
		IdleTimeout: time.Duration(p.SessionIdleTimeoutM) * time.Minute,
		AbsTimeout:  time.Duration(p.SessionAbsoluteMin) * time.Minute,
	}, nil
}

// sessionIssuer 是 S6 的**最小写路径**（S7 负责续期/撤销/列表）。
type sessionIssuer struct {
	repo     *store.SessionRepo
	policies *store.ClientPolicyRepo
}

func (s *sessionIssuer) Issue(ctx context.Context, spec protocol.SessionSpec) (string, error) {
	uid, err := uuid.Parse(spec.UserID)
	if err != nil {
		return "", fmt.Errorf("startup: invalid user id in session spec: %w", err)
	}
	p, err := s.policies.LoadTokenPolicy(ctx, spec.ClientID)
	if err != nil {
		return "", clientError(err)
	}
	now := time.Now().UTC()
	row := &model.Session{
		UserID:        uid,
		ClientID:      spec.ClientID,
		ACR:           spec.ACR,
		AMR:           model.StringArray(spec.AMR),
		Status:        "active",
		IssuedAt:      now,
		ExpiresAt:     now.Add(time.Duration(p.SessionAbsoluteMin) * time.Minute),
		IdleExpiresAt: now.Add(time.Duration(p.SessionIdleTimeoutM) * time.Minute),
	}
	row.ID = uuid.Must(uuid.NewV7())
	if err := s.repo.Create(ctx, row); err != nil {
		return "", err
	}
	return row.ID.String(), nil
}

// auditSink 把令牌面的消费/轮换/撤销写成 `auth_events`。
type auditSink struct{ repo *store.AuthEventRepo }

func (a *auditSink) Append(ctx context.Context, ev protocol.AuditEvent) error {
	row := &model.AuthEvent{
		EventType:  ev.EventType,
		Result:     ev.Result,
		AMR:        model.StringArray{},
		OccurredAt: time.Now().UTC(),
	}
	if ev.ClientID != "" {
		cid := ev.ClientID
		row.ClientID = &cid
	}
	if ev.UserID != "" {
		if uid, err := uuid.Parse(ev.UserID); err == nil {
			row.UserID = &uid
		}
	}
	if ev.Reason != "" {
		reason := ev.Reason
		row.Reason = &reason
	}
	if ev.TraceID != "" {
		trace := ev.TraceID
		row.TraceID = &trace
	}
	return a.repo.Append(ctx, row)
}

// flowStarter 把编排器的 `Begin` 折成协议面需要的形状（只回 flow_id）。
type flowStarter struct{ orch *flow.Orchestrator }

func (f *flowStarter) Begin(ctx context.Context, clientID, traceID string) (string, error) {
	res, err := f.orch.Begin(ctx, flow.BeginInput{ClientID: clientID, TraceID: traceID})
	if err != nil {
		return "", err
	}
	return res.FlowID.String(), nil
}

// flowVerify 用编排器校验一步应答，并把 Descriptor 折成"是否达标 + acr/amr + 用户"。
type flowVerify struct{ orch *flow.Orchestrator }

func (f *flowVerify) Verify(ctx context.Context, flowID, factorType, challengeID, response, userID string) (protocol.FlowResult, error) {
	id, err := uuid.Parse(flowID)
	if err != nil {
		return protocol.FlowResult{}, flow.ErrFlowNotFound
	}
	in := flow.VerifyInput{FlowID: id, Factor: factor.FactorType(factorType), ChallengeID: challengeID, Response: response, UserID: userID}
	res, verr := f.orch.VerifyChallenge(ctx, in)
	if verr != nil {
		// `ACR_INSUFFICIENT` 是"还没补完"，不是失败：如实回 satisfied=false。
		var insufficient *flow.ACRInsufficientError
		if errors.As(verr, &insufficient) {
			return protocol.FlowResult{Satisfied: false, ACR: string(insufficient.Flow.CurrentACR), AMR: amrOfDescriptor(insufficient.Flow)}, nil
		}
		return protocol.FlowResult{}, verr
	}
	return protocol.FlowResult{
		Satisfied: res.Satisfied,
		UserID:    userIDFromFlow(ctx, f.orch, id),
		ACR:       string(res.Flow.CurrentACR),
		AMR:       amrOfDescriptor(res.Flow),
	}, nil
}

// userIDFromFlow 读流的 `user_id`（登录流在 `/authorize` 之后由 challenge/verify 绑定）。
func userIDFromFlow(ctx context.Context, orch *flow.Orchestrator, flowID uuid.UUID) string {
	uid, err := orch.UserOfFlow(ctx, flowID)
	if err != nil {
		return ""
	}
	return uid
}

func amrOfDescriptor(d flow.Descriptor) []string {
	out := make([]string, 0, len(d.Steps))
	for _, s := range d.Steps {
		if s.Status == flow.StepDone {
			out = append(out, string(s.Factor))
		}
	}
	return out
}

// callerResolver 把 `protocol.CallerResolver` 接到 `httpx.CallerResolver`（验签 + 会话状态）。
func newCallerResolver(keys *signing.Set, db *gorm.DB) (*protocol.CallerResolver, error) {
	if keys == nil {
		return nil, errors.New("startup: caller resolver requires signing keys")
	}
	return &protocol.CallerResolver{
		Keys:     keys,
		Sessions: &sessionState{repo: store.NewSessionRepo(db)},
		Now:      time.Now,
	}, nil
}

// sessionState 把 `store.SessionRepo.SessionStatus` 折成协议面要的形状（把"没查到"翻成哨兵）。
type sessionState struct{ repo *store.SessionRepo }

func (s *sessionState) SessionStatus(ctx context.Context, sid string) (string, error) {
	st, err := s.repo.SessionStatus(ctx, sid)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			return "", protocol.ErrSessionGone
		}
		return "", err
	}
	return st, nil
}

// redirects 读 `clients.redirect_uris`。
type redirects struct{ repo *store.ClientPolicyRepo }

func (r *redirects) RedirectURIs(ctx context.Context, clientID string) ([]string, error) {
	uris, err := r.repo.RedirectURIs(ctx, clientID)
	if err != nil {
		return nil, clientError(err)
	}
	return uris, nil
}

// mergeDisabledFactors 把**平台级**（`runtime_config.factors.disabled`）与**客户端级**
// （`login_policies.disabled_factors`）合成"有效禁用"集合（AC-FLOW-05：两级互不覆盖，取并集）。
//
// fail-closed：平台级集合读不出来就报错，**不**降级成"只按客户端级算"——那等于把管理员
// 关掉的因子悄悄放回来。平台集合只由 `PATCH /api/v1/factors/{type}` 写入且写入前已校验
// 因子存在，所以这里不会出现未知类型。
func mergeDisabledFactors(clientDisabled func(context.Context, string) ([]factor.FactorType, error),
	platformDisabled func(context.Context) (map[string]bool, error)) func(context.Context, string) ([]factor.FactorType, error) {
	return func(ctx context.Context, clientID string) ([]factor.FactorType, error) {
		out, err := clientDisabled(ctx, clientID)
		if err != nil {
			return nil, err
		}
		plat, err := platformDisabled(ctx)
		if err != nil {
			return nil, err
		}
		seen := make(map[factor.FactorType]bool, len(out)+len(plat))
		merged := make([]factor.FactorType, 0, len(out)+len(plat))
		for _, t := range out {
			if !seen[t] {
				seen[t] = true
				merged = append(merged, t)
			}
		}
		for t := range plat {
			ft := factor.FactorType(t)
			if !seen[ft] {
				seen[ft] = true
				merged = append(merged, ft)
			}
		}
		return merged, nil
	}
}
