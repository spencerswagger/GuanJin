//go:build integration

package audit

import (
	"context"
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

// preparePartitions 幂等建好固定窗口的月分区。
func preparePartitions(db *gorm.DB) error {
	for _, month := range partitionMonths() {
		name := fmt.Sprintf("audit_logs_p%s", month.Format("200601"))
		from := month.Format("2006-01-02")
		to := month.AddDate(0, 1, 0).Format("2006-01-02")
		if err := db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF audit_logs
			FOR VALUES FROM ('%s') TO ('%s')`, name, from, to)).Error; err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
	}
	return nil
}

// dropPartitions 清掉本包建的分区（单线程，跑完用例之后）。
func dropPartitions(db *gorm.DB) {
	for _, month := range partitionMonths() {
		name := fmt.Sprintf("audit_logs_p%s", month.Format("200601"))
		_ = db.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS %s`, name)).Error
	}
	_ = time.Now() // 保留 time 导入（preparePartitions 的类型依赖）
}
