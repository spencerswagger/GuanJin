package adapter

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/spencerswagger/guanjin/internal/gateway"
)

// 本文件是**断言重放判重**的 Redis 实现（AC-DP-08：同 `jti` 窗口内第二次 → 401）。
//
// 为什么落在 Redis 而不是 PG：判重是**每请求一次**的热路径写 + 天然按时间过期，
// 用 PG 会给数据面加一条"每请求一次写库"的路径（连接数与 WAL 都不划算），
// 而且 TTL 语义要自己写清理任务。`SET NX PX` 一条命令同时给"原子判重 + 自动过期"。
//
// 语义（与网关侧 `gateway.ReplayGuard` 的约定一致）：
//   - 首次出现 → `(true, nil)`；
//   - 窗口内重复 → `(false, nil)`（**不是错误**：重复是可预期的攻击/缺陷信号）；
//   - Redis 不可用 → `error`（由网关侧翻成 503 `REDIS_UNAVAILABLE`，fail-closed）。
//
// 键名前缀 `gw:assert:jti:`：与账号级失败计数的 `login:fail:` / `login:lock:`（D3-D）分开命名空间，
// 避免两套 TTL 语义混在一棵前缀下被运维误删。这是**内部缓存键**，不是对外 wire 契约。

// ReplayKeyPrefix 是断言判重的键前缀（内部缓存键）。
const ReplayKeyPrefix = "gw:assert:jti:"

// RedisReplayGuard 用 Redis 的 `SET NX PX` 做原子判重（多副本共享同一份状态）。
type RedisReplayGuard struct {
	rdb    *redis.Client
	prefix string
}

// NewRedisReplayGuard 绑定一个 Redis 客户端。
func NewRedisReplayGuard(rdb *redis.Client) *RedisReplayGuard {
	return &RedisReplayGuard{rdb: rdb, prefix: ReplayKeyPrefix}
}

// Claim 见 `gateway.ReplayGuard`。
func (g *RedisReplayGuard) Claim(ctx context.Context, jti string, window time.Duration) (bool, error) {
	if g == nil || g.rdb == nil {
		return false, errors.New("adapter: replay guard has no redis client")
	}
	jti = strings.TrimSpace(jti)
	if jti == "" {
		// 空 `jti` 判不了重：**不能**当成"首次"放行（那等于判据失效），交给上层按内部缺陷处理。
		return false, errors.New("adapter: replay claim requires jti")
	}
	if window <= 0 {
		return false, errors.New("adapter: replay window must be > 0")
	}
	// `SetNX` = `SET key 1 NX PX window`：首次成功、重复返回 false，全部原子。
	ok, err := g.rdb.SetNX(ctx, g.prefix+jti, "1", window).Result()
	if err != nil {
		return false, err
	}
	return ok, nil
}

// 编译期断言。
var _ gateway.ReplayGuard = (*RedisReplayGuard)(nil)
