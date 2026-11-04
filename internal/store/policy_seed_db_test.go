//go:build integration

package store

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"
)

// D13 的启动期校验在**真库**上的正反两面。
//
// 为什么每个用例都在事务里跑并回滚：`login_policies` 的 tier_default 是**参考数据**
// （000017 seed 的三行），任何"删了再插"的构造都不能留在库里。
// 事务内先删后插是安全的：它是业务表（不是只增表 auth_events/audit_logs），
// 且只碰 `scope='tier_default'`，**不动 client_override 行**。

// errRollbackForTest 只用于"断言做完、主动让事务回滚"——
// 这样既保证参考数据不被改动，又能验证 gorm.Transaction 真的回滚了（而不是 commit）。
var errRollbackForTest = errors.New("test: rollback on purpose")

// countTierDefaults 数库里现有的 tier_default 行（用来证明事务回滚干净）。
func countTierDefaults(t *testing.T, db *gorm.DB) int {
	t.Helper()
	var n int
	if err := db.Raw(`SELECT count(*) FROM login_policies WHERE scope = 'tier_default'`).Scan(&n).Error; err != nil {
		t.Fatalf("数 tier_default 行必须成功: %v", err)
	}
	return n
}

// clearTierDefaults 在事务内清掉所有 tier_default 行（回滚即复原）。
func clearTierDefaults(t *testing.T, tx *gorm.DB) {
	t.Helper()
	if err := tx.Exec(`DELETE FROM login_policies WHERE scope = 'tier_default'`).Error; err != nil {
		t.Fatalf("清掉 tier_default 行必须成功: %v", err)
	}
}

// insertTierDefault 插一行档位默认策略（id 由应用层给，D4-A：迁移不写 DB 默认值）。
func insertTierDefault(t *testing.T, tx *gorm.DB, tier, minACR string) {
	t.Helper()
	if err := tx.Exec(
		`INSERT INTO login_policies (id, scope, tier, min_acr) VALUES (?, 'tier_default', ?, ?)`,
		mustV7(t), tier, minACR).Error; err != nil {
		t.Fatalf("插入 %s 档位默认策略必须成功: %v", tier, err)
	}
}

// 正面：三档各一行 → 通过。
//
// `min_acr` 按 §4.2-15 给（L1/L2 → AAL1、L3 → AAL2），但校验**只看行数**：
// D13 允许运维调整取值（幂等口径是「改过的不被覆盖」），把取值也钉死会让合规改动变成启动失败。
func TestDBTierDefaultSeedEachExactlyOne(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// 先记下库里**原有的** tier_default 行数：本用例不假设 seed 是否已落（迁移顺序无关），
	// 但必须证明自己没改动外部状态（回滚后行数原样）。
	before := countTierDefaults(t, db)

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		clearTierDefaults(t, tx)
		insertTierDefault(t, tx, "L1", "AAL1")
		insertTierDefault(t, tx, "L2", "AAL1")
		insertTierDefault(t, tx, "L3", "AAL2")

		if err := VerifyTierDefaults(ctx, tx); err != nil {
			t.Fatalf("三档各一行必须通过: %v", err)
		}
		return errRollbackForTest
	})
	if !errors.Is(err, errRollbackForTest) {
		t.Fatalf("事务应被回滚（不污染参考数据），实际 %v", err)
	}

	// 回滚后：行数必须和事务前一模一样（seed 有没有都不该被本用例改动）。
	if after := countTierDefaults(t, db); after != before {
		t.Fatalf("回滚后 tier_default 行数 = %d, 事务前 = %d（事务没回滚干净）", after, before)
	}
}

// 反面①：缺一档 → 必须报错并点名该档（不许静默用别的档位顶替、不许返回"默认值"）。
func TestDBTierDefaultSeedMissingTier(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		clearTierDefaults(t, tx)
		insertTierDefault(t, tx, "L1", "AAL1")
		insertTierDefault(t, tx, "L2", "AAL1")
		// L3 故意不插。

		seedErr := VerifyTierDefaults(ctx, tx)
		if seedErr == nil {
			t.Fatal("缺 L3 必须报错（缺策略 = 启动失败，不得退化成请求期 500）")
		}
		if !errors.Is(seedErr, ErrTierDefaultSeed) {
			t.Fatalf("err = %v, want errors.Is(ErrTierDefaultSeed)", seedErr)
		}
		var typed *TierDefaultSeedError
		if !errors.As(seedErr, &typed) {
			t.Fatalf("err = %T, want *TierDefaultSeedError", seedErr)
		}
		if len(typed.Missing) != 1 || typed.Missing[0] != "L3" {
			t.Fatalf("Missing = %v, want [L3]", typed.Missing)
		}
		if typed.Counts["L1"] != 1 || typed.Counts["L2"] != 1 || typed.Counts["L3"] != 0 {
			t.Fatalf("Counts = %v, want L1=1 L2=1 L3=0", typed.Counts)
		}
		return errRollbackForTest
	})
	if !errors.Is(err, errRollbackForTest) {
		t.Fatalf("事务应被回滚，实际 %v", err)
	}
}

// 反面②：一行都没有（迁移没跑 / seed 被清）→ 三档全报缺，且**不许**当成"通过"。
//
// 这条也是"SKIP = 未验证 = 红"的正面落地：空结果不是"没问题"，是"没有策略"。
func TestDBTierDefaultSeedEmptyTable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		clearTierDefaults(t, tx)

		seedErr := VerifyTierDefaults(ctx, tx)
		if seedErr == nil {
			t.Fatal("一行 tier_default 都没有时必须报错")
		}
		var typed *TierDefaultSeedError
		if !errors.As(seedErr, &typed) {
			t.Fatalf("err = %T, want *TierDefaultSeedError", seedErr)
		}
		if len(typed.Missing) != 3 {
			t.Fatalf("Missing = %v, want L1/L2/L3 三档全缺", typed.Missing)
		}
		return errRollbackForTest
	})
	if !errors.Is(err, errRollbackForTest) {
		t.Fatalf("事务应被回滚，实际 %v", err)
	}
}
