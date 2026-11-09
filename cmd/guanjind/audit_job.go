package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/audit"
	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/crypto"
)

// 本文件是 **S10 的审计链校验 Job 装配**（`AC-AUD-11`：Job 必须在 `cmd/guanjind` 内、
// **不新增二进制**——多一个巡检进程等于多一份部署/监控/权限面）。
//
// 两个指标由 `internal/audit` 暴露（`ChainBrokenTotal()` / `AnchorAge()`），
// 这里只负责"按日跑一次 + 记日志"；指标导出属监控侧（M1 不做 Prometheus 端点）。

// newAuditVerifier 装出整链复核器。
//
// fail-fast：链键派生失败、DB 为空 → 直接让进程起不来。审计校验"静默不跑"是最坏的形态
// （面板上永远绿，而链可能早断了）。
func newAuditVerifier(db *gorm.DB, cfg *config.Config, logger *slog.Logger) (*audit.Verifier, error) {
	if db == nil {
		return nil, errors.New("startup: audit verifier requires db")
	}
	if cfg == nil || cfg.MasterKey == nil {
		return nil, errors.New("startup: audit verifier requires master key")
	}
	key, err := cfg.MasterKey.DeriveHMAC(crypto.PurposeHMACAudit)
	if err != nil {
		return nil, fmt.Errorf("startup: derive audit key: %w", err)
	}
	chain, err := audit.NewChain(key)
	if err != nil {
		return nil, fmt.Errorf("startup: audit chain: %w", err)
	}
	verifier, err := audit.NewVerifier(chain, db, audit.NewAnchorStore(db), logger)
	if err != nil {
		return nil, fmt.Errorf("startup: audit verifier: %w", err)
	}
	return verifier, nil
}

// startAuditVerifyJob 起进程内的每日整链复核（返回停止函数）。
//
// 首个回合延迟一整个周期：启动期已经做过 DB/策略自检，再立刻扫全月会拖慢启动；
// 运维要"启动即校验"用 `verifier.RunOnce(ctx)` 显式调（测试也走这条）。
func startAuditVerifyJob(ctx context.Context, db *gorm.DB, cfg *config.Config,
	logger *slog.Logger) (func(), error) {
	verifier, err := newAuditVerifier(db, cfg, logger)
	if err != nil {
		return nil, err
	}
	stop := audit.StartJob(ctx, verifier, audit.DefaultVerifyInterval, logger)
	logger.Info("audit: chain verify job started", "interval", audit.DefaultVerifyInterval.String())
	return stop, nil
}
