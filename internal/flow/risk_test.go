package flow

import (
	"context"
	"sync"
)

// fakeGuard 是账号级失败计数的测试桩：**内存计数**，语义与 `internal/risk` 对齐
// （达阈值即锁定、成功清零）。真实实现在 `internal/risk`，那边另有独立的键名/TTL/多副本用例。
type fakeGuard struct {
	mu        sync.Mutex
	threshold int
	fails     map[string]int
	locked    map[string]bool
	err       error
	recorded  int
	resets    int
	statuses  int
}

func (g *fakeGuard) Status(_ context.Context, userID string, p LockoutPolicy) (int, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.statuses++
	if g.err != nil {
		return 0, false, g.err
	}
	if g.locked == nil {
		g.locked = map[string]bool{}
	}
	threshold := p.FailThreshold
	if threshold <= 0 {
		threshold = g.threshold
	}
	left := threshold - g.fails[userID]
	if left < 0 {
		left = 0
	}
	return left, g.locked[userID], nil
}

func (g *fakeGuard) RecordFailure(_ context.Context, userID string, p LockoutPolicy) (int, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return 0, false, g.err
	}
	if g.fails == nil {
		g.fails = map[string]int{}
	}
	if g.locked == nil {
		g.locked = map[string]bool{}
	}
	g.recorded++
	threshold := p.FailThreshold
	if threshold <= 0 {
		threshold = g.threshold
	}
	g.fails[userID]++
	left := threshold - g.fails[userID]
	if left <= 0 {
		left = 0
		g.locked[userID] = true
	}
	return left, g.locked[userID], nil
}

func (g *fakeGuard) Reset(_ context.Context, userID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return g.err
	}
	g.resets++
	delete(g.fails, userID)
	delete(g.locked, userID)
	return nil
}
