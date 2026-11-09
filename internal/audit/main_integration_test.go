//go:build integration

package audit

import (
	"context"
	"fmt"
	"os"
	"testing"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/store"
)

// TestMain 在**跑任何用例之前**一次性建好测试分区（运行期零 DDL）。
//
// 为什么必须这样：早先每个用例自己 `CREATE TABLE ... PARTITION OF`，在 `-p 8` 的多包并行
// 下与并发写入撞出 **40P01 死锁**（`create partition audit_logs_pXXXXXX`）——这是测试自身
// 的并发缺陷，不是审计链实现的缺陷。修法是把 DDL 提到单线程的 `TestMain`：
//  1. 幂等建 `partitionMonthCount` 个月分区（`CREATE TABLE IF NOT EXISTS`，可重复跑）；
//  2. 跑完用例后统一 `DROP`（同样单线程，不会与写入竞争）。
//
// 生产不这么做（分区由 `cmd/guanjind` 的月度任务建，见 `migrations/000011` 的注释），
// 这里只是把"测试基建"与"被测行为"分开。
func TestMain(m *testing.M) {
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		// 没有 DSN 时本包的 integration 用例本就该红（不许 Skip）：让 m.Run 去报。
		os.Exit(m.Run())
	}
	db, err := store.Open(context.Background(), store.Options{DSN: dsn})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[audit.TestMain] open db: %v\n", err)
		os.Exit(1)
	}
	if err := preparePartitions(db); err != nil {
		fmt.Fprintf(os.Stderr, "[audit.TestMain] prepare partitions: %v\n", err)
		_ = store.Close(db)
		os.Exit(1)
	}
	code := m.Run()
	dropPartitions(db)
	_ = store.Close(db)
	os.Exit(code)
}

// preparePartitions 走 `EnsurePartitions`：**同一事务内先取链锁再建表**。
//
// 这一层是 Gate 复现 40P01 的根因修复：DDL 与 `Chain.Append` 抢同一张父表，
// 不共用一把 advisory 锁就会在多包并行（`-p 8`）下死锁。
func preparePartitions(db *gorm.DB) error {
	return EnsurePartitions(context.Background(), db, partitionMonths())
}

// dropPartitions 清掉本包建的分区（单线程、跑完用例之后；同样先取链锁）。
func dropPartitions(db *gorm.DB) {
	_ = DropPartitions(context.Background(), db, partitionMonths())
}
