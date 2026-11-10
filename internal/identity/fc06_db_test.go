//go:build integration

package identity

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/risk"
)

// AC-FC-06（Core 半边 · S11 收口补证）：**Redis 全挂期间不丢状态** —— 已签发的挑战在
// Redis 恢复后仍按**原 TTL / attempts** 判定。
//
// 判据原文（v11）：Redis 全挂期间不丢状态：已签发挑战在 Redis 恢复后仍按原 TTL/`attempts` 判定。
//
// 分工（Gate 账目）：Core = 挑战侧的"不丢状态"；Edge = S10 的故障注入场景（真停容器/断连）。
// 本用例做的是 Core 能独立自证的那半边，且**不碰共享 Redis 实例**：
//   - "全挂" = 把 `risk.Guard` 指到死端口（`127.0.0.1:1`），这是 AC-FC-01/02 系列既有写法；
//   - "恢复" = 换回真 Redis 客户端。
//
// 正控两处，否则用例可能"什么都没发生":
//
//	① 死端口上的 Guard 必须真报 `risk.ErrUnavailable`（证明 Redis 侧确实不可用）；
//	② 恢复后的 Guard 必须真 `Ping` 得通（证明"恢复"是真的）。
func TestAC_FC_06_ChallengeSurvivesRedisOutage(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)

	// 挑战窗口取短值（夹具自己给，不是实现默认值）：本用例要靠"真过期"证明 TTL 没被重置。
	const ttl = 3 * time.Second
	const maxAttempts = 3
	svc := newRegisterService(t, db, ttl, maxAttempts)
	ctx := context.Background()

	email := "s11fc06-" + uuid.NewString()[:12] + "@example.com"
	start, err := svc.StartRegistration(ctx, StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: email, TraceID: "tr-fc06",
	})
	if err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	if start.ChallengeID == nil {
		t.Fatal("email 型必须下发挑战")
	}
	chID, err := uuid.Parse(*start.ChallengeID)
	if err != nil {
		t.Fatalf("解析 challenge_id：%v", err)
	}

	// 挑战签发时把权威事实记下来（Redis 恢复后必须一模一样）。
	var expiresAt time.Time
	var attempts0, max0 int
	if err := db.Raw(`SELECT expires_at, attempts, max_attempts FROM challenges WHERE id = ?`, chID).
		Row().Scan(&expiresAt, &attempts0, &max0); err != nil {
		t.Fatalf("读挑战行：%v", err)
	}
	if max0 != maxAttempts || attempts0 != 0 {
		t.Fatalf("签发时 attempts/max 应为 0/%d，实际 %d/%d", maxAttempts, attempts0, max0)
	}

	// ── Redis 全挂 ────────────────────────────────────────────────
	dead, err := risk.New("127.0.0.1:1", "")
	if err != nil {
		t.Fatalf("risk.New(死端口): %v", err)
	}
	// 正控①：确实不可用（不是"配了个能连的地址却以为它挂"）。
	if _, _, serr := dead.Status(ctx, uuid.NewString(), risk.Policy{
		FailThreshold: 5, LockDuration: time.Minute,
	}); !errors.Is(serr, risk.ErrUnavailable) {
		t.Fatalf("正控①失败：死端口应报 risk.ErrUnavailable，实际 %v", serr)
	}

	// 挂掉期间：挑战判定照旧（权威在库），并且**照样扣次数**。
	if _, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: "000000",
		Password: "GuanJin#FC06-Strong1", TraceID: "tr-fc06",
	}); !errors.Is(err, ErrInvalidRegisterCode) {
		t.Fatalf("Redis 挂掉期间错码应仍为 ErrInvalidRegisterCode（判定不该依赖 Redis），实际 %v", err)
	}
	var attempts1 int
	if err := db.Raw(`SELECT attempts FROM challenges WHERE id = ?`, chID).Row().Scan(&attempts1); err != nil {
		t.Fatalf("读 attempts：%v", err)
	}
	if attempts1 != 1 {
		t.Fatalf("Redis 挂掉期间扣次必须落库：期望 attempts=1，实际 %d", attempts1)
	}

	// ── 等到原 TTL 过掉（不重置、不延长） ──────────────────────────
	if wait := time.Until(expiresAt) + 300*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}

	// ── Redis 恢复 ────────────────────────────────────────────────
	addr := os.Getenv("GUANJIN_TEST_REDIS_ADDR")
	if addr == "" {
		t.Fatalf("必须设置 GUANJIN_TEST_REDIS_ADDR")
	}
	live, err := risk.New(addr, os.Getenv("GUANJIN_TEST_REDIS_PASSWORD"))
	if err != nil {
		t.Fatalf("risk.New(真 Redis): %v", err)
	}
	// 正控②：恢复是真的。
	if err := live.Ping(ctx); err != nil {
		t.Fatalf("正控②失败：真 Redis 应 Ping 通，实际 %v", err)
	}

	// 恢复后：仍是**原 TTL**（已过期 → 拒），不是"从恢复时刻重新计时"。
	if _, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
		RegisterToken: start.RegisterToken, Code: "000000",
		Password: "GuanJin#FC06-Strong1", TraceID: "tr-fc06",
	}); !errors.Is(err, ErrRegisterTokenExpired) {
		t.Fatalf("Redis 恢复后应按原 TTL 判过期（ErrRegisterTokenExpired），实际 %v —— "+
			"若这里能继续尝试，说明 TTL 被 Redis 恢复重置了", err)
	}

	// 库里的权威事实：attempts 没被重置、expires_at 一字未改。
	var expiresAt2 time.Time
	var attempts2 int
	if err := db.Raw(`SELECT expires_at, attempts FROM challenges WHERE id = ?`, chID).
		Row().Scan(&expiresAt2, &attempts2); err != nil {
		t.Fatalf("恢复后读挑战行：%v", err)
	}
	if !expiresAt2.Equal(expiresAt) {
		t.Fatalf("expires_at 不得被 Redis 恢复改动：签发 %s / 恢复后 %s", expiresAt, expiresAt2)
	}
	if attempts2 != attempts1 {
		t.Fatalf("attempts 不得被 Redis 恢复重置：挂掉期间 %d / 恢复后 %d", attempts1, attempts2)
	}
	// 说明：本用例全程**不写不删任何 Redis 键**，只用 Guard 读状态/Ping —— 对共享实例零副作用。
	_ = live
}
