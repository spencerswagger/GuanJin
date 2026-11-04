package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/signing"
	"github.com/spencerswagger/guanjin/internal/store"
)

// keygen 是**离线**签名密钥供给予命令（裁定 A：禁止启动期自动生成）。
//
// 为什么必须离线：启动期自动生成会在"密钥行被删/库被清"时静默换一对新键，
// 把在途 token 全部变成验签失败——这类事故在生产上表现为"所有业务系统突然 401"，
// 而日志里什么也看不出来。所以生成只走 CLI，启动期只解封（解不开就非 0 退出）。
//
// 幂等：同一 `kid` 已存在则 no-op（`ON CONFLICT (kid) DO NOTHING`），重复执行安全。
func runKeygen(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, store.Options{DSN: cfg.DatabaseDSN})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close(db) }()
	if err := store.Ping(ctx, db); err != nil {
		return fmt.Errorf("keygen: database not usable: %w", err)
	}

	keywrap, err := cfg.MasterKey.DeriveSM4(crypto.PurposeSM4Keywrap)
	if err != nil {
		return fmt.Errorf("keygen: derive keywrap key: %w", err)
	}
	repo := store.NewSigningKeyRepo(db)
	// 两类密钥各一把（`oidc` 签用户令牌；`gateway` 签数据面断言，AC-LOGIN-07 要求同时发布）。
	targets := []struct{ kid, purpose, algorithm string }{
		{"oidc-v1", signing.PurposeOIDC, signing.AlgSM3WithSM2},
		{"gateway-v1", signing.PurposeGateway, signing.AlgSM2},
	}
	for _, t := range targets {
		row, err := signing.GenRow(rand.Reader, keywrap, t.kid, t.purpose, t.algorithm)
		if err != nil {
			return fmt.Errorf("keygen: generate %s: %w", t.kid, err)
		}
		inserted, err := repo.InsertIfAbsent(ctx, store.SigningKeyRow{
			Kid: row.Kid, Purpose: row.Purpose, Algorithm: row.Algorithm,
			PublicKey: row.PublicKey, PrivateEnc: row.PrivateEnc,
		})
		if err != nil {
			return fmt.Errorf("keygen: insert %s: %w", t.kid, err)
		}
		state := "exists (no-op)"
		if inserted {
			state = "created"
		}
		_, _ = fmt.Fprintf(stdout, "keygen: kid=%s purpose=%s %s\n", t.kid, t.purpose, state)
	}
	return nil
}

// loadSigningKeys 在启动期解封全部密钥并校验两个用途都存在（缺任一 → 非 0 退出）。
func loadSigningKeys(ctx context.Context, db *gorm.DB, cfg *config.Config) (*signing.Set, error) {
	keywrap, err := cfg.MasterKey.DeriveSM4(crypto.PurposeSM4Keywrap)
	if err != nil {
		return nil, fmt.Errorf("startup: derive keywrap key: %w", err)
	}
	rows, err := store.NewSigningKeyRepo(db).ListUsable(ctx)
	if err != nil {
		return nil, fmt.Errorf("startup: read signing_keys: %w", err)
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
		return nil, fmt.Errorf("startup: unseal signing keys: %w", err)
	}
	for _, purpose := range []string{signing.PurposeOIDC, signing.PurposeGateway} {
		if !set.HasPurpose(purpose) {
			return nil, fmt.Errorf("startup: no active signing key for purpose %q (run `guanjind keygen`)", purpose)
		}
	}
	return set, nil
}
