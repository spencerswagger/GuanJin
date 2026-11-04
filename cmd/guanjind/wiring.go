package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/factor/holding"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/flowstore"
	"github.com/spencerswagger/guanjin/internal/identity"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是**装配层**：把仓储、因子、编排器接成一根可用的业务线。
//
// 它只做"接线"，不做判断：档位怎么映射、禁用集怎么算、错误怎么映射，全部在各自包里
// （`flow` 判语义、`flowstore` 做类型与错误转换、`store` 出 SQL）。这里出现业务判断
// 就说明有一处口径被抄到了第二个地方。
//
// 为什么集中在一个函数里返回：装配期就是 fail-fast 的唯一时机 —— 缺仓储、缺因子、
// 缺 TTL 应当**进程起不来**，而不是等第一个用户登录时才 500。

// newFlowHandler 装出因子与挑战面（S5）的处理器。
func newFlowHandler(ctx context.Context, db *gorm.DB, ttl time.Duration, logger *slog.Logger) (*flow.HTTPHandler, error) {
	flowRepo := store.NewFlowRepo(db)
	flowSvc, err := flowstore.New(db, flowRepo, ttl)
	if err != nil {
		return nil, fmt.Errorf("startup: flowstore: %w", err)
	}
	flowAdapter, err := flowstore.NewAdapter(flowSvc, flowRepo)
	if err != nil {
		return nil, fmt.Errorf("startup: flow adapter: %w", err)
	}
	challengeAdapter, err := flowstore.NewChallengeAdapter(store.NewChallengeRepo(db))
	if err != nil {
		return nil, fmt.Errorf("startup: challenge adapter: %w", err)
	}

	credStore, err := identity.NewCredentialStore(db)
	if err != nil {
		return nil, fmt.Errorf("startup: credential store: %w", err)
	}
	// 认证路径只做校验，不判口令策略（策略在注册/改密时有 client 上下文才判）——
	// 所以用 `NewPasswordVerifier`，不为了过构造器自检而伪造一份策略数字。
	password, err := holding.NewPasswordVerifier(credStore)
	if err != nil {
		return nil, fmt.Errorf("startup: password factor: %w", err)
	}
	registry := factor.NewRegistry()
	descriptors, err := holding.Register(registry, password)
	if err != nil {
		return nil, fmt.Errorf("startup: factor registry: %w", err)
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
		Now:             time.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("startup: flow orchestrator: %w", err)
	}
	return flow.NewHTTPHandler(orc, logger)
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
