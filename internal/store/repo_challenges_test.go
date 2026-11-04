package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// DryRun 单测：挑战仓储的入参守卫与关键 SQL 形状。
//
// 为什么值得写：一次性消费与"计数不越界"都是**条件 UPDATE**，写成无条件 UPDATE
// 在功能测试里看不出来（单线程跑总能过），只有并发下才双花。这里把条件钉住。
func TestChallengeRepoUnit(t *testing.T) {
	db, cap := dryRunDB(t)
	repo := NewChallengeRepo(db)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := repo.Create(ctx, nil); err == nil {
		t.Fatal("nil 挑战必须被拒")
	}
	// 四类必填项各自的守卫（每一条漏了都会写出一条无意义的挑战）。
	bad := []*model.Challenge{
		{Purpose: "", FactorType: "password", ResponseHash: []byte{1}, ExpiresAt: now},
		{Purpose: "login", FactorType: "", ResponseHash: []byte{1}, ExpiresAt: now},
		{Purpose: "login", FactorType: "password", ResponseHash: nil, ExpiresAt: now},
		{Purpose: "login", FactorType: "password", ResponseHash: []byte{1}},
	}
	for i, c := range bad {
		if err := repo.Create(ctx, c); err == nil {
			t.Errorf("第 %d 条非法挑战必须被拒", i)
		}
	}

	if _, err := repo.GetByID(ctx, uuid.Nil); err != ErrChallengeNotFound {
		t.Fatalf("零值 id 应报 ErrChallengeNotFound，实际 %v", err)
	}
	if _, err := repo.Consume(ctx, uuid.Nil, now); err != ErrChallengeNotFound {
		t.Fatalf("零值 id 消费应报 ErrChallengeNotFound，实际 %v", err)
	}

	// 一次性消费必须是「WHERE consumed_at IS NULL」（否则同一个验证码能反复用）。
	_, _ = repo.Consume(ctx, uuid.New(), now)
	if sql := cap.last(t); !strings.Contains(sql, "consumed_at IS NULL") {
		t.Fatalf("Consume 必须带 consumed_at IS NULL 条件，实际：%s", sql)
	}

	// 记失败必须是「attempts < max_attempts」（否则计数会越过上限）。
	_, _, _ = repo.RecordAttempt(ctx, uuid.New(), now)
	sql := cap.last(t)
	_ = sql // 最后一条是 SELECT remaining；下面单独验 UPDATE
	if _, _, err := repo.RecordAttempt(ctx, uuid.Nil, now); err != ErrChallengeNotFound {
		t.Fatalf("零值 id 记失败应报 ErrChallengeNotFound，实际 %v", err)
	}
}

// 单独验 RecordAttempt 的 UPDATE 条件（用 DrRun 捕获第一条语句更直观）。
func TestChallengeRecordAttemptSQLGuards(t *testing.T) {
	db, cap := dryRunDB(t)
	repo := NewChallengeRepo(db)
	ctx := context.Background()

	_, _, err := repo.RecordAttempt(ctx, uuid.New(), time.Now().UTC())
	if err != nil {
		// DryRun 下 SELECT 会失败，这里只关心 UPDATE 语句已被生成。
		t.Logf("RecordAttempt (dry-run) 返回：%v", err)
	}
	found := false
	for _, q := range cap.queries {
		if strings.Contains(q, "attempts = attempts + 1") {
			found = true
			if !strings.Contains(q, "attempts < max_attempts") {
				t.Fatalf("记失败必须带 attempts < max_attempts 条件，实际：%s", q)
			}
			if !strings.Contains(q, "consumed_at IS NULL") {
				t.Fatalf("已消费的挑战不得再记失败，实际：%s", q)
			}
		}
	}
	if !found {
		t.Fatal("没有生成 UPDATE challenges ... attempts = attempts + 1")
	}
}
