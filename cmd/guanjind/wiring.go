package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/admin"
	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/factor/holding"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/flowstore"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/identity"
	"github.com/spencerswagger/guanjin/internal/risk"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/store"
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
	sessionHandler  *session.HTTPHandler
	adminSessions   *admin.SessionsHTTP
	riskGuard       *risk.Guard
}

// newWiring 装出本进程的全部业务面。
func newWiring(ctx context.Context, db *gorm.DB, cfg *config.Config, startup store.StartupConfig, logger *slog.Logger) (*wiring, error) {
	flowHandler, guard, err := newFlowHandler(ctx, db, cfg, startup, logger)
	if err != nil {
		return nil, err
	}
	registerHandler, err := newRegisterHandler(ctx, db, cfg, startup, logger)
	if err != nil {
		return nil, err
	}
	sessionHandler, adminSessions, err := newSessionWiring(db, logger)
	if err != nil {
		return nil, err
	}
	return &wiring{
		flowHandler:     flowHandler,
		registerHandler: registerHandler,
		sessionHandler:  sessionHandler,
		adminSessions:   adminSessions,
		riskGuard:       guard,
	}, nil
}

// newSessionWiring 装出会话自助面 + 管理面 sessions 段（S7）。
//
// **调用方身份解析**：`httpx.DenyAllResolver{}` 是 S6 接线前的显式选择——它不是"占位 TODO"，
// 而是 fail-closed 的行为约定：令牌解析归 S6（`SM3withSM2` 验签 + `sid`/`roles` 取值），
// 在它接线前，本仓**不允许**存在任何"从头部自认身份"的暗门（技术栈 §8.3）。
// 因此这两组端点现在一律回 401 `TOKEN_INVALID`；业务逻辑（撤销/级联/审计）已完整并有测试覆盖。
func newSessionWiring(db *gorm.DB, logger *slog.Logger) (*session.HTTPHandler, *admin.SessionsHTTP, error) {
	sessionRepo := store.NewSessionOpsRepo(db)
	// 撤销级联走 D5 的窄接口 `AbandonBySession`（不直接 import flowstore，避免成环）。
	flowRepo := store.NewFlowRepo(db)
	events := store.NewAuthEventRepo(db)

	mgr, err := session.NewManager(sessionRepo, flowRepo, events, session.Options{Logger: logger})
	if err != nil {
		return nil, nil, fmt.Errorf("startup: session manager: %w", err)
	}
	resolver := httpx.DenyAllResolver{}

	sessionHandler, err := session.NewHTTPHandler(mgr, resolver, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: session http: %w", err)
	}
	adminSessions, err := admin.NewSessionsHTTP(mgr, resolver, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: admin sessions http: %w", err)
	}
	return sessionHandler, adminSessions, nil
}

// newFlowHandler 装出因子与挑战面（S5）。
func newFlowHandler(ctx context.Context, db *gorm.DB, cfg *config.Config, startup store.StartupConfig, logger *slog.Logger) (*flow.HTTPHandler, *risk.Guard, error) {
	flowRepo := store.NewFlowRepo(db)
	flowSvc, err := flowstore.New(db, flowRepo, startup.FlowTTL)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: flowstore: %w", err)
	}
	flowAdapter, err := flowstore.NewAdapter(flowSvc, flowRepo)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: flow adapter: %w", err)
	}
	challengeAdapter, err := flowstore.NewChallengeAdapter(store.NewChallengeRepo(db))
	if err != nil {
		return nil, nil, fmt.Errorf("startup: challenge adapter: %w", err)
	}

	// 账号级失败计数（Redis 权威，D3-D）。**启动期不 ping**：Redis 挂了进程要活着、
	// 业务全拒（`/readyz` 与请求路径负责），否则会连探针都没有。
	guard, err := risk.New(cfg.RedisAddr, cfg.RedisPassword)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: risk guard: %w", err)
	}

	credStore, err := identity.NewCredentialStore(db)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: credential store: %w", err)
	}
	// 认证路径只做校验，不判口令策略（策略在注册/改密时有 client 上下文才判）——
	// 所以用 `NewPasswordVerifier`，不为了过构造器自检而伪造一份策略数字。
	password, err := holding.NewPasswordVerifier(credStore)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: password factor: %w", err)
	}
	registry := factor.NewRegistry()
	descriptors, err := holding.Register(registry, password)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: factor registry: %w", err)
	}

	policies := store.NewClientPolicyRepo(db)
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
		DisabledFactors: disabledFactorsOf(policies),
		TierOf:          tierOf(policies),
		ClientOf:        clientOf(policies),
		Guard:           &riskGuardAdapter{guard: guard},
		Lockout:         lockoutOf(policies),
		Now:             time.Now,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("startup: flow orchestrator: %w", err)
	}
	handler, err := flow.NewHTTPHandler(orc, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("startup: flow handler: %w", err)
	}
	return handler, guard, nil
}

// newRegisterHandler 装出注册面（S4：`/auth/register/start|complete`）。
func newRegisterHandler(ctx context.Context, db *gorm.DB, cfg *config.Config, startup store.StartupConfig, logger *slog.Logger) (*identity.RegisterHTTP, error) {
	indexKey, err := cfg.MasterKey.DeriveHMAC(crypto.PurposeHMACIdentifier)
	if err != nil {
		return nil, fmt.Errorf("startup: derive identifier key: %w", err)
	}
	fieldKey, err := cfg.MasterKey.DeriveSM4(crypto.PurposeSM4Field)
	if err != nil {
		return nil, fmt.Errorf("startup: derive field key: %w", err)
	}
	registerKey, err := cfg.MasterKey.DeriveHMAC(crypto.PurposeHMACRegister)
	if err != nil {
		return nil, fmt.Errorf("startup: derive register token key: %w", err)
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
		return nil, fmt.Errorf("startup: identity service: %w", err)
	}
	handler, err := identity.NewRegisterHTTP(svc, logger)
	if err != nil {
		return nil, fmt.Errorf("startup: register handler: %w", err)
	}
	return handler, nil
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
