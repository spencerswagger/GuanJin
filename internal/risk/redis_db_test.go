//go:build integration

package risk

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 本文件是 **AC-FC-01 的风控侧真证据**（`-tags=integration`）：
//
//   - 真 Redis（`GUANJIN_TEST_REDIS_ADDR`）：计数/锁定/清零在真服务上成立；
//   - **真死端口**（`127.0.0.1:1`，不 mock、不关 miniredis）：必须 `ErrUnavailable`，
//     装配层据此映射 503 `REDIS_UNAVAILABLE`，认证端点**不降级放行**。
//
// 为什么不在主闸用真 Redis：主闸要在无外部服务的机器上可重复跑；这里用 `integration`
// 标签隔离，CI 的 integration job 带真 Redis service（见 `.github/workflows/ci.yml`）。

// realRedisGuard 连真 Redis（地址/密码来自环境变量，取法与其它 integration 用例一致）。
func realRedisGuard(t *testing.T, addr, password string) *Guard {
	t.Helper()
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       0,
	})
	t.Cleanup(func() { _ = client.Close() })
	g, err := NewWithClient(client)
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	return g
}

func TestRealRedisCountsAndLocks(t *testing.T) {
	addr := os.Getenv("GUANJIN_TEST_REDIS_ADDR")
	if addr == "" {
		t.Fatal("必须设置 GUANJIN_TEST_REDIS_ADDR（真 Redis，不许用 mock 顶）")
	}
	g := realRedisGuard(t, addr, os.Getenv("GUANJIN_TEST_REDIS_PASSWORD"))
	ctx := context.Background()

	// 每次用独立 uid，避免与残留键互相干扰（不清库）。
	uid := "s10-fc-" + time.Now().Format("150405.000000000")
	t.Cleanup(func() { _ = g.Reset(ctx, uid) })

	policy := Policy{FailThreshold: 3, LockDuration: 15 * time.Minute}
	for i := 1; i <= 3; i++ {
		left, locked, err := g.RecordFailure(ctx, uid, policy)
		if err != nil {
			t.Fatalf("第 %d 次 RecordFailure：%v", i, err)
		}
		want := 3 - i
		if left != want {
			t.Fatalf("第 %d 次后剩余次数 = %d, want %d", i, left, want)
		}
		if locked != (i == 3) {
			t.Fatalf("第 %d 次 locked = %v", i, locked)
		}
	}
	if _, locked, err := g.Status(ctx, uid, policy); err != nil || !locked {
		t.Fatalf("达阈值后 Status 必须报锁定，实际 locked=%v err=%v", locked, err)
	}
	if err := g.Reset(ctx, uid); err != nil {
		t.Fatalf("Reset：%v", err)
	}
	if _, locked, err := g.Status(ctx, uid, policy); err != nil || locked {
		t.Fatalf("Reset 后必须解锁，实际 locked=%v err=%v", locked, err)
	}
}

// 真死端口：客户端连不上 → 每个入口都必须 `ErrUnavailable`（fail-closed，绝不"这次先不算"）。
func TestRealDeadRedisFailsClosed(t *testing.T) {
	g := realRedisGuard(t, "127.0.0.1:1", "")
	ctx := context.Background()
	policy := Policy{FailThreshold: 3, LockDuration: 15 * time.Minute}
	uid := "s10-fc-dead"

	if err := g.Ping(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ping 应报 ErrUnavailable，实际 %v", err)
	}
	if _, _, err := g.Status(ctx, uid, policy); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Status 应报 ErrUnavailable，实际 %v", err)
	}
	if _, _, err := g.RecordFailure(ctx, uid, policy); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("RecordFailure 应报 ErrUnavailable，实际 %v", err)
	}
	if err := g.Reset(ctx, uid); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Reset 应报 ErrUnavailable，实际 %v", err)
	}
}
