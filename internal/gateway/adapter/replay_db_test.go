//go:build integration

package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/gateway"
)

// 本文件是 **AC-DP-08 平台侧的真 Redis 证据**（判据原文：集成·真实 Redis）。
//
// 三件事必须在这里证，且只有真 Redis 能证：
//  1. `SET NX` 的**原子性**语义：同一 `jti` 第二次一定 `false`（内存假实现测不出时钟/TTL 行为）；
//  2. **TTL 到期即释放**（"窗口过期后可再用"）——用长窗口/短窗口两档验；
//  3. 键真的落在 Redis 上、且带 TTL（不是"写了个本地 map 假装是 Redis"）。

// AC-DP-08：同 jti 窗口内第二次 → false；窗口到期后可再用。
func TestAC_DP_08_RedisReplayGuardSemantics(t *testing.T) {
	guard := NewRedisReplayGuard(testRedisClient(t))
	ctx := context.Background()
	jti := "it-jti-" + time.Now().UTC().Format("20060102150405.000000000")

	first, err := guard.Claim(ctx, jti, 2*time.Second)
	if err != nil {
		t.Fatalf("首次 Claim: %v", err)
	}
	if !first {
		t.Fatal("首次出现应返回 true")
	}
	second, err := guard.Claim(ctx, jti, 2*time.Second)
	if err != nil {
		t.Fatalf("二次 Claim: %v", err)
	}
	if second {
		t.Fatal("窗口内第二次必须返回 false（AC-DP-08：同 jti 二次 → 401）")
	}

	// 键必须真的在 Redis 里，且带 TTL（不是永不过期）。
	ttl, err := guard.rdb.PTTL(ctx, ReplayKeyPrefix+jti).Result()
	if err != nil {
		t.Fatalf("PTTL: %v", err)
	}
	if ttl <= 0 || ttl > 2*time.Second {
		t.Fatalf("TTL 期望 (0, 2s]，实际 %s", ttl)
	}

	// 窗口过期后可再用（AC-DP-08）——用 300ms 窗口实测，不靠"应该会过期"。
	short := "it-short-" + time.Now().UTC().Format("20060102150405.000000000")
	if ok, err := guard.Claim(ctx, short, 300*time.Millisecond); err != nil || !ok {
		t.Fatalf("短窗口首次 Claim: %v / %v", ok, err)
	}
	time.Sleep(400 * time.Millisecond)
	if ok, err := guard.Claim(ctx, short, 300*time.Millisecond); err != nil || !ok {
		t.Fatalf("窗口过期后应可再用，实际 ok=%v err=%v", ok, err)
	}
}

// 判重的边界：空 jti / 非正窗口 → 报错（**不许**当成"首次"放行）。
func TestRedisReplayGuardRejectsBadInput(t *testing.T) {
	guard := NewRedisReplayGuard(testRedisClient(t))
	ctx := context.Background()

	if _, err := guard.Claim(ctx, "   ", time.Second); err == nil {
		t.Fatal("空 jti 必须报错（否则判重失效却看不出来）")
	}
	if _, err := guard.Claim(ctx, "jti-x", 0); err == nil {
		t.Fatal("非正窗口必须报错")
	}
	var nilGuard *RedisReplayGuard
	if _, err := nilGuard.Claim(ctx, "jti-x", time.Second); err == nil {
		t.Fatal("nil guard 必须报错")
	}
}

// Redis 不可达 → 明确 error（由网关侧翻成 503 `REDIS_UNAVAILABLE`，fail-closed）。
func TestRedisReplayGuardSurfacesUnavailable(t *testing.T) {
	// 指向一个必然连不上的地址：只看"是否返回错误"，不看错误文本。
	dead := NewRedisReplayGuard(deadRedisClient(t))
	if _, err := dead.Claim(context.Background(), "jti-dead", time.Second); err == nil {
		t.Fatal("Redis 不可达必须返回错误（不许静默放行）")
	}
}

var _ gateway.ReplayGuard = (*RedisReplayGuard)(nil)
var _ = errors.Is
