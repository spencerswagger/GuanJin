//go:build integration

package store

// store 包的测试有**并行套件**、且跑在**共享库**上（`cmd/guanjind`/`internal/audit` 也用它），
// 风险比单个包高：一旦 DSN 指到非测试库，清理/改表就可能落到真数据上。
//
// 因此这里加一层**入口校验**：`TestMain` 第一件事就是校验库名（任何 DDL/DML 之前），
// 与 `testdb.RequireTestDB` 共用同一个 `DBName` 实现。

import (
	"context"
	"os"
	"testing"

	"github.com/spencerswagger/guanjin/internal/testdb"
)

func TestMain(m *testing.M) {
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		// 没有 DSN 时本包的 integration 用例本就该红（不许 Skip）：让 m.Run 去报。
		os.Exit(m.Run())
	}
	db, err := Open(context.Background(), Options{DSN: dsn})
	if err != nil {
		os.Exit(1)
	}
	testdb.MustBeTestDBOrExit(db)
	code := m.Run()
	_ = Close(db)
	os.Exit(code)
}
