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
	"github.com/spencerswagger/guanjin/internal/testdb"
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

	// **入口第一件事：校验库名（任何 DDL 之前）**。
	// 之前这步放在建 schema 之后，导致"指向非测试库时套件照样绿、库里多出 68 张表"
	// —— 只建表不删数据，但同样不可接受（指到生产会安静地建出一个 schema + 68 张表）。
	if guardDB, err := store.Open(context.Background(), store.Options{DSN: dsn}); err == nil {
		testdb.MustBeTestDBOrExit(guardDB)
		_ = store.Close(guardDB)
	} else {
		fmt.Fprintf(os.Stderr, "[audit.TestMain] open for db-name guard: %v\n", err)
		os.Exit(1)
	}

	// **schema 隔离**：本包的用例必须往"链尾之后的月份"写，若落在共享库 `public` 的全局链上，
	// 会把链尾推到未来月份，导致任何用真实 `now()` 写审计的其它套件撞下界守卫。
	// 这里先把 public 里历史残留清掉（幂等），再切到独立 schema `audit_test` 跑本包用例。
	if base, err := store.Open(context.Background(), store.Options{DSN: dsn}); err == nil {
		// ① **自家隔离 schema 默认清空**（无风险、不需要任何开关）：
		//    `audit_test` 是本包独占的，残留行会把链尾推到未来月份、导致共享月落在链尾之前
		//    （实测 `occurred_at 2026-12 < tail 2027-01`，8 条用例直接红）。
		// ② **共享/public 库的清理**仍受 `GUANJIN_ALLOW_TEST_CLEANUP=1` + `_test` 库名双护栏约束
		//    （那才是有风险的那一半，不能因为①而放宽）。
		if err := truncateIsolatedSchema(base); err != nil {
			fmt.Fprintf(os.Stderr, "[audit.TestMain] truncate isolated schema: %v\n", err)
		}
		if b, a, perr := purgeSharedFutureRows(base); perr != nil {
			fmt.Fprintf(os.Stderr, "[audit.TestMain] purge future rows: %v\n", perr)
		} else {
			fmt.Fprintf(os.Stderr, "[audit.TestMain] shared max(occurred_at): before=%q after=%q\n", b, a)
		}
		_ = store.Close(base)
	}
	if auditIsolationEnabled() {
		if base, err := store.Open(context.Background(), store.Options{DSN: dsn}); err == nil {
			if err := ensureIsolatedSchema(base); err != nil {
				fmt.Fprintf(os.Stderr, "[audit.TestMain] %v\n", err)
				_ = store.Close(base)
				os.Exit(1)
			}
			_ = store.Close(base)
		}
		dsn = isolatedDSN(dsn)
		if err := os.Setenv("GUANJIN_TEST_DSN", dsn); err != nil {
			fmt.Fprintf(os.Stderr, "[audit.TestMain] setenv: %v\n", err)
			os.Exit(1)
		}
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
	// 基准月 = max(链尾月份 + 1, 2040-01)：既要晚于链尾（否则撞下界守卫），
	// 又要**远离真实月份**——`cmd/guanjind` 的分区用例会删/建"当月 + N 个月"的分区，
	// 若基准月与之重合，`DROP TABLE` 会把本包的行连同分区一起删掉
	// （实测：`AC-AUD_03` 报"应有 4 条，实际 0"）。
	var base time.Time
	if tail.Valid && !tail.Time.IsZero() {
		base = time.Date(tail.Time.UTC().Year(), tail.Time.UTC().Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	} else {
		now := time.Now().UTC()
		base = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	}
	if floor := time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC); base.Before(floor) {
		base = floor
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
