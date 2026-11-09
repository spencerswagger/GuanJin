package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/admin"
	"github.com/spencerswagger/guanjin/internal/audit"
	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/session"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是**因子管理面**（`AC-FLOW-05`）的装配点：
//
//	GET   /api/v1/factors
//	GET   /api/v1/factors/{type}
//	PATCH /api/v1/factors/{type}   ← 平台级启停（写 audit_logs）
//
// 三件事在这里被钉死：
//  1. **审计链的键来源**：与整链复核同一个派生（`crypto.PurposeHMACAudit`），
//     绝不另造第二把键——两把键会让新写入的 `integrity_tag` 与既有链对不上；
//  2. **同事务**：`store.PlatformFactorRepo.SetEnabled` 把"改 runtime_config"与"写 audit_logs"
//     放进同一个事务，适配器只是把 `store.FactorAuditRecord` 翻成 `audit.Record`；
//  3. **注册表复用**：管理面下发的能力声明必须与编排层用的是**同一份**注册表
//     （`wiring.registry`），否则"平台列出 X 可用、编排却认为 X 不可用"。
//
// fail-fast：主密钥缺失/派生失败/链构造失败都让进程起不来——审计写不进去时若继续跑，
// 每一次启停都会变成"改了但没留痕"。

// factorAuditAdapter 把平台的因子启停记录交给审计链（链在**调用方事务**内追加）。
type factorAuditAdapter struct{ chain *audit.Chain }

// AppendInTx 实现 `store.FactorAuditWriter`。
func (a factorAuditAdapter) AppendInTx(ctx context.Context, tx *gorm.DB, rec store.FactorAuditRecord) error {
	if a.chain == nil {
		return errors.New("wiring: audit chain is nil")
	}
	actor := rec.ActorUserID
	_, err := a.chain.Append(ctx, tx, audit.Record{
		ID:          rec.ID,
		ActorUserID: &actor,
		ActorRole:   rec.ActorRole,
		Action:      rec.Action,
		TargetType:  rec.TargetType,
		TargetID:    rec.TargetID,
		Before:      rec.Before,
		After:       rec.After,
		Result:      rec.Result,
		IP:          rec.IP,
		TraceID:     rec.TraceID,
		OccurredAt:  rec.OccurredAt,
	})
	if err != nil {
		return fmt.Errorf("wiring: append factor audit: %w", err)
	}
	return nil
}

// newFactorsAdminWiring 装出因子管理面。
func newFactorsAdminWiring(db *gorm.DB, cfg *config.Config, registry *factor.Registry,
	resolver session.TokenResolver, logger *slog.Logger) (*admin.FactorsHTTP, error) {
	switch {
	case db == nil:
		return nil, errors.New("startup: factors admin requires db")
	case cfg == nil || cfg.MasterKey == nil:
		return nil, errors.New("startup: factors admin requires master key")
	case registry == nil:
		return nil, errors.New("startup: factors admin requires the startup factor registry")
	case resolver == nil:
		return nil, errors.New("startup: factors admin requires caller resolver")
	}

	key, err := cfg.MasterKey.DeriveHMAC(crypto.PurposeHMACAudit)
	if err != nil {
		return nil, fmt.Errorf("startup: derive audit key: %w", err)
	}
	chain, err := audit.NewChain(key)
	if err != nil {
		return nil, fmt.Errorf("startup: audit chain: %w", err)
	}
	reader, err := store.NewPlatformFactorReader(db)
	if err != nil {
		return nil, fmt.Errorf("startup: platform factor reader: %w", err)
	}
	repo, err := store.NewPlatformFactorRepo(reader, factorAuditAdapter{chain: chain})
	if err != nil {
		return nil, fmt.Errorf("startup: platform factor repo: %w", err)
	}
	handler, err := admin.NewFactorsHTTP(registry, repo, resolver, logger)
	if err != nil {
		return nil, fmt.Errorf("startup: factors http: %w", err)
	}
	return handler, nil
}
