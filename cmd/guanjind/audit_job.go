package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

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

// partitionAheadMonths 是审计分区**预建窗口**（当月 + 未来 5 个月）。
//
// 为什么不是"只建当月"：分区必须**在写入之前**存在（`audit_logs_pYYYYMM` 不存在时
// `INSERT` 直接 23514 "no partition of relation"）。预建 6 个月给了运维足够的缓冲：
// 即使月度任务连续几个月没跑（例如刚发布、进程停机），写入也不会立刻失败。
const partitionAheadMonths = 6

// auditPartitionMonths 返回需要保证存在的月份（当月 1 号起，连续 `partitionAheadMonths` 个月，UTC）。
func auditPartitionMonths(now time.Time) []time.Time {
	base := now.UTC()
	start := time.Date(base.Year(), base.Month(), 1, 0, 0, 0, 0, time.UTC)
	out := make([]time.Time, 0, partitionAheadMonths)
	for i := 0; i < partitionAheadMonths; i++ {
		out = append(out, start.AddDate(0, i, 0))
	}
	return out
}

// ensureAuditPartitions 保证"当月 + 未来 5 个月"的审计分区存在。
//
// **不在这里再包一层事务/锁**：`audit.EnsurePartitions` 自己会在**一个事务内**先取链锁
// （与 `Chain.Append` 共用同一把 `pg_advisory_xact_lock`），外面再包一层会引入第二把锁、
// 与写入形成新的锁序——那正是 40P01 的死锁来源。
func ensureAuditPartitions(ctx context.Context, db *gorm.DB) error {
	return audit.EnsurePartitions(ctx, db, auditPartitionMonths(time.Now()))
}

// startAuditPartitionJob 起进程内的分区滚动任务（返回停止函数）。
//
// 频率取**每日**而不是每月 1 号整点：多跑几次是幂等的（`CREATE TABLE IF NOT EXISTS`），
// 而"恰好每月 1 号 00:00 跑一次"在进程重启/时钟漂移下很容易错过整月。
func startAuditPartitionJob(ctx context.Context, db *gorm.DB, logger *slog.Logger) (func(), error) {
	if db == nil {
		return nil, errors.New("startup: audit partition job requires db")
	}
	jobCtx, cancel := context.WithCancel(ctx)
	runOnce := func() {
		if err := ensureAuditPartitions(jobCtx, db); err != nil {
			logger.Warn("audit: ensure partitions failed", "error", err)
		}
	}
	// 启动即跑一次：这是"当月分区缺失"能被立刻补上的唯一时机。
	runOnce()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				runOnce()
			}
		}
	}()
	logger.Info("audit: partition roll job started", "ahead_months", partitionAheadMonths)
	return cancel, nil
}
