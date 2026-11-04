//go:build integration

package flowstore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// 本文件给 I-F8 的「**锁内禁重活**」补一条**行为**证据，而不是再写一遍注释：
//
//	口令派生（PBKDF2-SM3，≥10 万迭代，实测约 0.45s）必须在 `flows` 行锁**之外**完成，
//	锁内只允许读该行、校验、写回。违反它的后果不是"慢一点"，而是：一把行锁被持有半秒，
//	同一用户的并发请求全部排队，200 QPS 下就是雪崩。
//
// 断言方式（两条互补，缺一条都能"假绿"）：
//  1. **锁外计算**：派生进行到一半时，用另一个事务 `SELECT … FOR UPDATE NOWAIT` 探针，
//     必须**立刻拿到锁**——若派生被塞进锁里，探针会撞 55P03（lock_not_available）；
//  2. **正控**：外部事务真的持锁时，`Advance` 必须阻塞到超时——否则说明锁根本没生效，
//     第 1 条就成了"因为没锁，所以锁外"的空话。

// lockProbeNowait 尝试**立刻**拿到该行的排他锁：拿到返回 nil，被占返回错误。
func lockProbeNowait(t *testing.T, db *gorm.DB, id uuid.UUID) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var got uuid.UUID
		return tx.Raw(`SELECT id FROM flows WHERE id = $1 FOR UPDATE NOWAIT`, id).Row().Scan(&got)
	})
}

func TestDBFlowstoreHeavyWorkRunsOutsideFlowLock(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db)
	svc := newService(t, db)
	fl := f.startFlow(t, db)

	salt := []byte("0123456789abcdef") // 16B，够 Validate
	params := crypto.PasswordParams{
		Iterations: crypto.MinPBKDF2Iterations, // 基线硬下限（≥10 万），不是为测试调小的数
		KeyLen:     32,
	}

	computing := make(chan struct{})
	type advanceResult struct {
		err         error
		computeTime time.Duration
	}
	done := make(chan advanceResult, 1)

	go func() {
		close(computing)
		start := time.Now()
		if _, err := crypto.DerivePassword([]byte("correct horse battery staple"), salt, params); err != nil {
			done <- advanceResult{err: err}
			return
		}
		computeTime := time.Since(start)
		_, err := svc.Advance(context.Background(), AdvanceRequest{
			FlowID: fl.ID, Factor: "pwd", At: time.Now().UTC(),
		})
		done <- advanceResult{err: err, computeTime: computeTime}
	}()

	<-computing
	time.Sleep(50 * time.Millisecond) // 让探针落在派生窗口内（派生实测 ~450ms，窗口很宽）

	if err := lockProbeNowait(t, db, fl.ID); err != nil {
		t.Fatalf("口令派生期间 flows 行锁竟被持有（%v）——重活进了锁内，违反 I-F8", err)
	}

	res := <-done
	if res.err != nil {
		t.Fatalf("advance 失败：%v", res.err)
	}
	if res.computeTime < 50*time.Millisecond {
		t.Fatalf("派生只花了 %v，探针窗口没覆盖计算期，本次结论不可信（不是通过）", res.computeTime)
	}

	// —— 正控：外部持锁 → Advance 必须阻塞 ——
	blockCtx, cancelBlock := context.WithCancel(context.Background())
	locked := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = db.Transaction(func(tx *gorm.DB) error {
			var got uuid.UUID
			if err := tx.Raw(`SELECT id FROM flows WHERE id = $1 FOR UPDATE`, fl.ID).Row().Scan(&got); err != nil {
				close(locked)
				return err
			}
			close(locked)
			<-release // 持锁直到主 goroutine 放行
			return nil
		})
	}()
	<-locked

	blockStart := time.Now()
	blockedErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(blockCtx, 800*time.Millisecond)
		defer cancel()
		_, err := svc.Advance(ctx, AdvanceRequest{FlowID: fl.ID, Factor: "totp", At: time.Now().UTC()})
		blockedErr <- err
	}()

	err := <-blockedErr
	elapsed := time.Since(blockStart)
	close(release) // 放锁，让外层事务提交
	cancelBlock()

	if err == nil {
		t.Fatalf("外部持锁期间 Advance 竟然成功了：锁没生效，锁外计算的结论也就无效")
	}
	if elapsed < 300*time.Millisecond {
		t.Fatalf("Advance 在 %v 就返回了，说明它没有等锁（预期阻塞到 ~800ms 超时）", elapsed)
	}

	// 被取消的那次推进不得留下半截写入（没有 totp）。
	final := reload(t, db, fl.ID)
	for _, amr := range final.AchievedAMR {
		if amr == "totp" {
			t.Fatalf("被取消的推进写入了 amr：%v", []string(final.AchievedAMR))
		}
	}
}
