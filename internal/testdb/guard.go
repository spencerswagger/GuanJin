//go:build integration

// Package testdb 提供**测试工具的安全护栏**（S10 后补，Gate 判据）。
//
// # 用法铁律
//
// **入口第一件事校验库名，任何 DDL 之前。**
// 具体说：`TestMain` 里拿到 DB 句柄后、`CREATE SCHEMA` / `CREATE TABLE` / `DROP` / `DELETE` /
// `ALTER … DISABLE TRIGGER` **之前**，先调 `MustBeTestDBOrExit`（或 `RequireTestDB`）。
// 这条规则是打补丁打出来的：先是"四处无护栏"，抽了 helper 之后又出现"`TestMain` 先建
// 68 张表、后面才校验"的绕过路径（指向非测试库时套件照样绿、库里多出 68 张表）。
// 顺序错了，护栏等于没有。
//
// 背景（实测教训）：测试工具里出现"关 append-only 触发器 + 删审计行 / DROP 分区"这类动作时，
// 只要有人把 `GUANJIN_TEST_DSN` 指到一个非测试库跑一次集成测试，就会**真删数据**。
// 因此凡是会删/改数据的测试工具，入口必须先过 `RequireTestDB`。
package testdb

import (
	"fmt"
	"log"
	"os"
	"testing"

	"gorm.io/gorm"
)

// AllowCleanupEnv 是"允许做破坏性清理（drop 未来分区等）"的显式开关。
const AllowCleanupEnv = "GUANJIN_ALLOW_TEST_CLEANUP"

// DBName 返回当前连接的数据库名。
func DBName(db *gorm.DB) (string, error) {
	var name string
	if err := db.Raw(`SELECT current_database()`).Scan(&name).Error; err != nil {
		return "", err
	}
	return name, nil
}

// IsTestDB 判断库名形如 `*_test`（测试库命名约定）。
func IsTestDB(name string) bool {
	return len(name) > len("_test") && name[len(name)-len("_test"):] == "_test"
}

// RequireTestDB 断言当前连接指向测试库；否则立即失败（**不许 skip**——skip 等于放行）。
//
// 用于所有"会删/改数据"的测试工具入口：`DISABLE TRIGGER`、`DELETE`、`DROP TABLE`。
func RequireTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	name, err := DBName(db)
	if err != nil {
		t.Fatalf("read current_database: %v", err)
	}
	if !IsTestDB(name) {
		t.Fatalf("拒绝对非测试库执行删改操作：current_database=%q（必须以 _test 结尾）", name)
	}
}

// CleanupAllowed 是否显式允许"批量破坏性清理"（drop 未来分区等）。
func CleanupAllowed() bool { return os.Getenv(AllowCleanupEnv) == "1" }

// MustBeTestDBOrExit 是 `TestMain` 专用入口校验：**必须在任何 DDL 之前调用**。
//
// 与 `RequireTestDB` 共用同一个 `DBName` 实现（两份判断迟早漂移）。
// 不满足时直接 `log.Fatalf`（退出码非 0），并在日志里打出库名。
func MustBeTestDBOrExit(db *gorm.DB) {
	name, err := DBName(db)
	if err != nil {
		log.Fatalf("[testdb] 读取 current_database() 失败，拒绝继续：%v", err)
	}
	if !IsTestDB(name) {
		log.Fatalf("[testdb] 拒绝对非测试库执行测试：current_database=%q（必须以 _test 结尾）；"+
			"如果这是测试库，请把库名改成以 _test 结尾", name)
	}
}

// Logf 统一日志前缀，便于在 CI 日志里 grep。
func Logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[testdb] "+format+"\n", args...)
}
