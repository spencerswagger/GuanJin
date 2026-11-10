//go:build integration

package identity

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/spencerswagger/guanjin/internal/risk"
)

// AC-FLOW-06（S11 收口补证）：**挑战权威在库** —— Redis 侧状态全失后，该挑战仍可被
// 正确判定（成功或按 `attempts` 拒），**不得"蒸发"成 `CHALLENGE_INVALID`**。
//
// 判据依据（接口清单 §5.9「挑战状态（challenge）的存储 —— 已裁定」）：
// 权威 = `challenges` 表；Redis 只承载限流计数与热缓存，**不作权威源**。
//
// # 为什么不打真 `FLUSHALL`
//
// 共享 `redis:7` 被 `internal/risk` 与登录锁定用例同时用着（`login:fail:`/`login:lock:`），
// 一次 `FLUSHALL` 会让跨包并行的 integration 变成随机红（本项目的红线是"不许用放宽换取绿"，
// 但同样不许把门禁搞成 flaky）。Gate 口径：**允许"Redis 侧状态全失"，但不用共享实例的
// `FLUSHALL`** —— 用独立逻辑库 DB 15 + `FLUSHDB`，对被测应用等价于"缓存全没了"，
// 且一个字都不碰 db0。
//
// # 为什么要正控
//
// 只 `FLUSHDB` 一个空库，会得到"什么都没发生"的假绿。所以先用真 `risk.Guard` 在 DB15
// 上造出一个平台真键（`login:fail:<uid>`），断言 `EXISTS=1` → flush → `DBSIZE=0`，
// 证明这一刀确实清掉了 Redis 侧状态；随后挑战判定必须**一点不受影响**。
func TestAC_FLOW_06_ChallengeAuthorityInDBAfterRedisFlushed(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, true)

	const maxAttempts = 3
	svc := newRegisterService(t, db, 5*time.Minute, maxAttempts)
	ctx := context.Background()

	// ① 发一个**落库**的挑战（只有 `type_code` 才在 `challenges` 里有权威行）。
	email := "s11flow06-" + uuid.NewString()[:12] + "@example.com"
	start, err := svc.StartRegistration(ctx, StartRegistrationRequest{
		ClientID: f.clientID, Type: "email", Value: email, TraceID: "tr-flow06",
	})
	if err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	if start.ChallengeID == nil {
		t.Fatal("email 型必须下发挑战（否则本判据无从谈起）")
	}
	chID, err := uuid.Parse(*start.ChallengeID)
	if err != nil {
		t.Fatalf("解析 challenge_id：%v", err)
	}

	// ② 正控：Redis 侧先真有一个平台键，证明"清"的不是空库。
	addr := os.Getenv("GUANJIN_TEST_REDIS_ADDR")
	if addr == "" {
		t.Fatalf("必须设置 GUANJIN_TEST_REDIS_ADDR（判据要求真 Redis）")
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("GUANJIN_TEST_REDIS_PASSWORD"),
		DB:       15, // 独占逻辑库：不碰 db0（db0 属 risk/锁定用例）
	})
	defer func() { _ = rdb.Close() }()
	guard, err := risk.NewWithClient(rdb)
	if err != nil {
		t.Fatalf("risk.NewWithClient: %v", err)
	}
	probeUID := uuid.NewString()
	if _, _, err := guard.RecordFailure(ctx, probeUID, risk.Policy{
		FailThreshold: 5, LockDuration: 15 * time.Minute,
	}); err != nil {
		t.Fatalf("RecordFailure（造正控键）：%v", err)
	}
	failKey := risk.FailKey(probeUID)
	if n := rdb.Exists(ctx, failKey).Val(); n != 1 {
		t.Fatalf("正控失败：Redis 上应存在 %s（否则后面 flush 的是空库，等于假绿）", failKey)
	}

	// ③ 真清空该逻辑库（对被测应用 = Redis 侧状态全失）。
	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("FLUSHDB: %v", err)
	}
	if n := rdb.DBSize(ctx).Val(); n != 0 {
		t.Fatalf("flush 后 DB15 应为空，实际 %d 个键", n)
	}

	// ④ 挑战必须仍在库里、仍可判定：连续错码 → 逐次扣 `challenges.attempts`，
	//    打满后按次数拒。全程不得出现 CHALLENGE_INVALID（"蒸发"）。
	const password = "GuanJin#Flow06-Strong1"
	for i := 1; i <= maxAttempts; i++ {
		_, err := svc.CompleteRegistration(ctx, CompleteRegistrationRequest{
			RegisterToken: start.RegisterToken,
			Code:          "000000", // 错码
			Password:      password,
			TraceID:       "tr-flow06",
		})
		last := i == maxAttempts
		switch {
		case last && !errors.Is(err, ErrRegisterAttemptsExceeded):
			t.Fatalf("第 %d 次（打满）应 ErrRegisterAttemptsExceeded，实际 %v —— 扣减没落在库里",
				i, err)
		case !last && !errors.Is(err, ErrInvalidRegisterCode):
			t.Fatalf("第 %d 次应为 ErrInvalidRegisterCode，实际 %v", i, err)
		case err != nil && errors.Is(err, ErrInvalidRegisterToken):
			t.Fatalf("第 %d 次被判成「挑战不存在/令牌不合规」（%v）—— 挑战在 Redis 清空后蒸发了，"+
				"AC-FLOW-06 要的正是这条不得发生", i, err)
		}
		var got int
		if rerr := db.Raw(`SELECT attempts FROM challenges WHERE id = ?`, chID).
			Row().Scan(&got); rerr != nil {
			t.Fatalf("第 %d 次读挑战行（清缓存后仍必须存在）：%v", i, rerr)
		}
		if got != i {
			t.Fatalf("第 %d 次失败后 challenges.attempts 应为 %d，实际 %d（扣减没落库 = 权威不在库）",
				i, i, got)
		}
	}

	// ⑤ 打满后：行仍在、未消费、attempts 顶到 max。
	var attempts, maxSeen int
	var consumedAt *string
	if err := db.Raw(`SELECT attempts, max_attempts, consumed_at::text FROM challenges WHERE id = ?`, chID).
		Row().Scan(&attempts, &maxSeen, &consumedAt); err != nil {
		t.Fatalf("读挑战行：%v", err)
	}
	if attempts != maxSeen || maxSeen != maxAttempts {
		t.Fatalf("attempts 应顶到 max_attempts=%d，实际 %d/%d", maxAttempts, attempts, maxSeen)
	}
	if consumedAt != nil {
		t.Fatal("失败打满不应把挑战标记为已消费（只有成功才一次性消费）")
	}
}
