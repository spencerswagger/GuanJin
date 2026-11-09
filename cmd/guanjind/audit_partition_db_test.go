//go:build integration

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/audit"
)

// 本文件补 Gate 指出的**生产缺口证据**：`audit.EnsurePartitions` 必须在 `cmd/guanjind`
// 内有调用方（migrations 只预建 -1/0/+1 三个月，第四个月起写入会因无分区失败）。
//
// 判据形态不看"函数被调用"，而是看**行为**：某月分区不存在 → 跑一次 `ensureAuditPartitions`
// → 该月分区存在。两次都查 `to_regclass`，是真实的库状态。

func auditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openTestDB(t)
	if !db.Migrator().HasTable("audit_logs") {
		t.Fatalf("缺 audit_logs 表：先跑 make migrate-up DATABASE_URL=$GUANJIN_TEST_DSN")
	}
	return db
}

func fakeMonth(now time.Time, ahead int) time.Time {
	base := now.UTC()
	start := time.Date(base.Year(), base.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start.AddDate(0, ahead, 0)
}

// 「某月分区不存在 → 一次 tick 后存在」。
//
// 做法：先删掉"未来第 3 个月"的分区（模拟"滚动任务缺席几个月"），断言不存在；
// 再调生产线用的 `ensureAuditPartitions`，断言该月分区已存在。
func TestAuditPartitionJobCreatesMissingMonth(t *testing.T) {
	db := auditTestDB(t)
	ctx := context.Background()
	target := fakeMonth(time.Now(), 3)
	name := fmt.Sprintf("public.audit_logs_p%s", target.Format("200601"))

	// 前置：清干净（跨运行可能残留），并确认"不存在"。
	if err := audit.DropPartitions(ctx, db, []time.Time{target}); err != nil {
		t.Fatalf("drop partition: %v", err)
	}
	if exists := auditPartitionExists(t, db, name); exists {
		t.Fatalf("前置失败：%s 应不存在", name)
	}

	// 生产线用的同一个入口（`cmd/guanjind` 的启动/每日任务都走它）。
	if err := ensureAuditPartitions(ctx, db); err != nil {
		t.Fatalf("ensureAuditPartitions: %v", err)
	}
	if exists := auditPartitionExists(t, db, name); !exists {
		t.Fatalf("跑完滚动任务后 %s 应存在（否则第四个月起审计写入会 23514 失败）", name)
	}
	// 幂等：再跑一次不报错。
	if err := ensureAuditPartitions(ctx, db); err != nil {
		t.Fatalf("第二次 ensureAuditPartitions 应幂等：%v", err)
	}
}

// 启动即跑一次：`startAuditPartitionJob` 里 `runOnce()` 在 goroutine 之前执行，
// 因此"进程刚起、当月分区缺失"也能当场补上。本用例直接验这条语义。
func TestAuditPartitionJobRunsOnceAtStartup(t *testing.T) {
	db := auditTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	target := fakeMonth(time.Now(), 1)
	name := fmt.Sprintf("public.audit_logs_p%s", target.Format("200601"))
	if err := audit.DropPartitions(ctx, db, []time.Time{target}); err != nil {
		t.Fatalf("drop partition: %v", err)
	}
	if auditPartitionExists(t, db, name) {
		t.Fatalf("前置失败：%s 应不存在", name)
	}

	stop, err := startAuditPartitionJob(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("startAuditPartitionJob: %v", err)
	}
	defer stop()

	// 启动即跑是同步的（在 goroutine 之前），所以这里无需等待。
	if !auditPartitionExists(t, db, name) {
		t.Fatalf("启动时 %s 应已被补上", name)
	}
}

func auditPartitionExists(t *testing.T, db *gorm.DB, name string) bool {
	t.Helper()
	var exists bool
	if err := db.Raw(`SELECT to_regclass(?) IS NOT NULL`, name).Scan(&exists).Error; err != nil {
		t.Fatalf("check %s: %v", name, err)
	}
	return exists
}
