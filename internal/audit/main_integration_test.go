//go:build integration

package audit

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

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
	// 基准月 = **链尾月份 + 1**（不用随机：后跑的用例若拿到更早月份会撞下界守卫）。
	if err := setPoolBaseMonth(db); err != nil {
		fmt.Fprintf(os.Stderr, "[audit.TestMain] pool base month: %v\n", err)
		_ = store.Close(db)
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

// setPoolBaseMonth 把分区池的基准月设为"当前链尾月份 + 1"（没有链尾时用当前月 + 1）。
//
// 这样按调用顺序分配的月份**严格单调且一定晚于链尾**，不会命中 `ErrOccurredAtTooOld`。
func setPoolBaseMonth(db *gorm.DB) error {
	// `max()` 在空表上返回 NULL：用 `sql.NullTime` 接，别让 Scan 直接报错。
	var tail sql.NullTime
	if err := db.Raw(`SELECT max(occurred_at) FROM audit_logs`).Scan(&tail).Error; err != nil {
		return err
	}
	// 基准月 = 链尾月份 + 1（链空则当月 + 1）。
	//
	// **不再用 2040 这种"远未来"地板**：`Append` 现在有上界守卫（不得晚于链尾月 + 1），
	// 而且审计用例已跑在独立 schema `audit_test`，不会再和 `cmd/guanjind` 的
	// "当月 + N" 分区操作撞车（那是 public schema 的事）。
	var base time.Time
	if tail.Valid && !tail.Time.IsZero() {
		base = time.Date(tail.Time.UTC().Year(), tail.Time.UTC().Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	} else {
		now := time.Now().UTC()
		base = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	}
	poolBaseMonth.Store(base)
	return nil
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
