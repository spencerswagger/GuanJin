package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/factor/holding"
	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/gateway"
	"github.com/spencerswagger/guanjin/internal/gateway/adapter"
	"github.com/spencerswagger/guanjin/internal/identity"
	"github.com/spencerswagger/guanjin/internal/protocol"
	"github.com/spencerswagger/guanjin/internal/signing"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是**数据面的装配层**（口径：decisions.md D16①②③ / m1-slices S8）。
//
// 只做三件事：把仓储折成 `gateway` 包要的接口（交给 `internal/gateway/adapter`）、
// 把 S6 的 `httpx.CallerResolver` 注进去、把签发器与转发内核串成 `Pipeline`。
// **判断一律不在这一层**（档位怎么比、断言哪个 claim 取哪列，都在 `internal/gateway` 里）——
// 装配层一旦出现业务判断，就说明有一处口径被抄到了第二个地方。
//
// 转发超时/拨号超时来自 `gateway.Options`（`GUANJIN_GATEWAY_UPSTREAM_TIMEOUT` /
// `GUANJIN_GATEWAY_DIAL_TIMEOUT`，技术栈 §7.2 登记值）：它们必须真的落到 transport 上，
// 否则"上游慢"会变成"连接池被占满"——这是数据面最典型的雪崩起点。
//
// fail-fast：拿不到签名密钥、缺 `gateway`/`oidc` 任一用途的密钥、接口没接齐 → 进程起不来。
// "带着半截依赖跑起来"的后果是每条请求 401 或上游收不到身份，比不启动更危险。

// newDataPlane 装出数据面主处理器。
func newDataPlane(ctx context.Context, db *gorm.DB, rdb *redis.Client, cfg *config.Config,
	router *gateway.Router, opts gateway.Options, skew time.Duration,
	logger *slog.Logger) (*gateway.Pipeline, error) {

	factors, err := platformFactors(db)
	if err != nil {
		return nil, err
	}

	keys, err := loadSigningKeys(ctx, db, cfg)
	if err != nil {
		return nil, fmt.Errorf("startup: %w", err)
	}
	signer, err := gateway.NewAssertionSigner(keys)
	if err != nil {
		return nil, fmt.Errorf("startup: assertion signer: %w", err)
	}

	// 转发参数由装配层给（超时/连接池是部署口径，不是网关语义）：
	// 这里只补上策略常量，不覆盖调用方显式配置。
	opts.TraceHeader = cfg.TraceHeader
	opts.Logger = logger
	forward, err := gateway.New(router, opts)
	if err != nil {
		return nil, fmt.Errorf("startup: forwarder: %w", err)
	}

	guard, err := gateway.NewGuard(&gateway.Guard{
		// 验签与会话状态检查复用 S6 的唯一实现（D16①：签发与验签必须同源）。
		Caller: &protocol.CallerResolver{
			Keys:     keys,
			Sessions: adapter.NewSessionStatus(db),
			Now:      time.Now,
		},
		Sessions: adapter.NewSessions(db),
		Policies: adapter.NewPolicies(db),
		Roles:    adapter.NewRoles(db),
		Users:    adapter.NewUsers(db),
		Visits:   adapter.NewVisits(db),
		Flows:    adapter.NewFlows(db),
		Factors:  factors,
		Issuer:   cfg.Issuer,
		Skew:     skew,
		Now:      time.Now,
		Logger:   logger,
	})
	if err != nil {
		return nil, fmt.Errorf("startup: guard: %w", err)
	}

	// 断言判重（AC-DP-08）用 Redis：判据写的是「集成·真实 Redis」，且判重是每请求热路径写，
	// 用 `SET NX PX` 一条命令同时拿到"原子判重 + 自动过期"。**启动期不 ping**：
	// Redis 挂了进程要活着、业务全拒（503 `REDIS_UNAVAILABLE`），而不是连探针都没有（D5-₂ 同理）。
	replay := adapter.NewRedisReplayGuard(rdb)
	pipeline, err := gateway.NewPipeline(forward, guard, signer, replay, logger)
	if err != nil {
		return nil, fmt.Errorf("startup: pipeline: %w", err)
	}
	return pipeline, nil
}

// loadSigningKeys 解封 `signing_keys` 里的全部可用密钥（与 `cmd/guanjind` 同一口径：
// 解不开即启动失败、**禁止启动期自动生成**——静默换键会把既有令牌全废）。
func loadSigningKeys(ctx context.Context, db *gorm.DB, cfg *config.Config) (*signing.Set, error) {
	keywrap, err := cfg.MasterKey.DeriveSM4(crypto.PurposeSM4Keywrap)
	if err != nil {
		return nil, fmt.Errorf("derive keywrap key: %w", err)
	}
	rows, err := store.NewSigningKeyRepo(db).ListUsable(ctx)
	if err != nil {
		return nil, fmt.Errorf("read signing_keys: %w", err)
	}
	signRows := make([]signing.Row, 0, len(rows))
	for _, r := range rows {
		signRows = append(signRows, signing.Row{
			Kid: r.Kid, Purpose: r.Purpose, Algorithm: r.Algorithm,
			PublicKey: r.PublicKey, PrivateEnc: r.PrivateEnc,
		})
	}
	set, err := signing.Load(keywrap, signRows)
	if err != nil {
		return nil, fmt.Errorf("unseal signing keys: %w", err)
	}
	// 数据面要**签**（gateway 用途）也要**验**用户令牌（oidc 用途）：任一缺失都属装配缺陷。
	for _, purpose := range []string{signing.PurposeGateway, signing.PurposeOIDC} {
		if !set.HasPurpose(purpose) {
			return nil, fmt.Errorf("no active signing key for purpose %q (run `guanjind keygen`)", purpose)
		}
	}
	return set, nil
}

// platformFactors 读"平台已装配哪些因子、各自声明什么能力"（`details.flow` 渲染补齐步骤要用）。
//
// 数据面**不做认证**，只读能力声明——但**不另列一份清单**：走的是与 `cmd/guanjind`
// 同一个注册表（`holding.Register`），避免两处各写一套"平台支持哪些因子/贡献什么级别"；
// 那种漂移在 `details.flow` 上会表现为"登录面说 AAL2 可达、数据面说不可达"。
func platformFactors(db *gorm.DB) ([]flow.FactorInfo, error) {
	credStore, err := identity.NewCredentialStore(db)
	if err != nil {
		return nil, fmt.Errorf("startup: credential store: %w", err)
	}
	password, err := holding.NewPasswordVerifier(credStore)
	if err != nil {
		return nil, fmt.Errorf("startup: password factor: %w", err)
	}
	registry := factor.NewRegistry()
	descriptors, err := holding.Register(registry, password)
	if err != nil {
		return nil, fmt.Errorf("startup: factor registry: %w", err)
	}
	return flow.FactorInfosOf(descriptors), nil
}
