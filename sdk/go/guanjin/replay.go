package guanjin

import (
	"context"
	"sync"
	"time"
)

// ReplayGuard 是 `jti` 防重放（能力 GJ-CAP-07）：窗口内同一 `jti` 只允许第一次通过。
//
// fail-closed（契约 §4.4）：存储不可用 → 拒请求（**不能**当作"没重放"放行）。
type ReplayGuard interface {
	// FirstUse 返回 true 表示本窗口内首次使用；false 表示重放。
	// 实现必须保证 SetNX 语义（原子），并给键设置不短于窗口的 TTL。
	FirstUse(ctx context.Context, jti string, exp time.Time) (bool, error)
}

// NewReplayGuard 按配置返回实现（契约 #18：默认 redis；memory 仅 local|test）。
// rdb 为 nil 且配置为 redis 时返回错误——**不做静默降级**（那是索引 §2 禁止的降级放行）。
func NewReplayGuard(cfg Config, rdb RedisSetNXer) (ReplayGuard, error) {
	switch cfg.Authz.ReplayStore {
	case ReplayStoreMemory:
		return NewMemoryReplayGuard(), nil
	case ReplayStoreRedis:
		if rdb == nil {
			return nil, NewReplayUnavailable("replay-store=redis 但未注入 Redis 客户端（不降级到 memory）", nil)
		}
		return &RedisReplayGuard{rdb: rdb, window: cfg.Gateway.ReplayWindow}, nil
	default:
		return nil, NewReplayUnavailable("未知 replay-store="+cfg.Authz.ReplayStore, nil)
	}
}

// RedisSetNXer 是 Redis 的最小依赖面（只用到 SET NX EX），便于注入与测试。
// 生产用 go-redis 适配（见 go-redis 适配注释），SDK 不自己读 Redis 地址（契约 §3.5）。
type RedisSetNXer interface {
	SetNX(ctx context.Context, key string, value any, ttl time.Duration) (bool, error)
}

// RedisReplayGuard 用 Redis 做跨副本 `jti` 去重（多副本必须用它，契约 #18）。
type RedisReplayGuard struct {
	rdb    RedisSetNXer
	window time.Duration
}

// FirstUse 实现 ReplayGuard。
func (g *RedisReplayGuard) FirstUse(ctx context.Context, jti string, exp time.Time) (bool, error) {
	ttl := g.window
	if !exp.IsZero() {
		if d := time.Until(exp); d > ttl {
			ttl = d
		}
	}
	ok, err := g.rdb.SetNX(ctx, replayKey(jti), "1", ttl)
	if err != nil {
		return false, NewReplayUnavailable("jti 存储不可用", err)
	}
	return ok, nil
}

func replayKey(jti string) string { return "guanjin:jti:" + jti }

// MemoryReplayGuard 是单进程实现（**仅 local|test**；多副本用它会静默失效）。
type MemoryReplayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
	now  func() time.Time
}

// NewMemoryReplayGuard 构造内存实现。
func NewMemoryReplayGuard() *MemoryReplayGuard {
	return &MemoryReplayGuard{seen: map[string]time.Time{}, now: time.Now}
}

// FirstUse 实现 ReplayGuard（惰性清理过期键，避免无界增长）。
func (g *MemoryReplayGuard) FirstUse(_ context.Context, jti string, exp time.Time) (bool, error) {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, until := range g.seen {
		if now.After(until) {
			delete(g.seen, k)
		}
	}
	if until, ok := g.seen[jti]; ok && now.Before(until) {
		return false, nil
	}
	ttl := DefaultReplayWindow
	if !exp.IsZero() {
		if d := time.Until(exp); d > ttl {
			ttl = d
		}
	}
	g.seen[jti] = now.Add(ttl)
	return true, nil
}
