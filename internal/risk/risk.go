// Package risk 是「账号级失败计数 + 锁定」（Redis 权威计数）。
//
// 口径（decisions.md / 基线）：
//   - 索引 §4.2-2；**D3-D**：账号级失败计数/锁定 = **Redis 权威**，
//     `authenticators.fail_count/locked_until` 只做镜像、**不得作判定依据**；
//   - 键名**冻结**（D3-D，实现不得自行改写）：
//     `login:fail:{user_id}`（失败计数）、`login:lock:{user_id}`（锁定标记），
//     `user_id` 用 uuid 的小写带连字符形式；
//   - Redis 不可用 → **fail-closed**：返回 `ErrUnavailable`，由调用方映射成
//     `REDIS_UNAVAILABLE`（E6/503），绝不"降级放行"；
//   - `challenges.attempts`（单次挑战的尝试次数）是**另一个对象**，与这里无关。
//
// 本包只做**驱动 AC-LOGIN-03 所需的最小闭环**（计数 / 锁定 / 清零 / 读剩余次数）：
// 不含限流（RATE_LIMITED）、验证码预算（BUDGET_EXCEEDED）、风险评分——那些属 M5。
package risk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	// ErrLocked 表示账号已锁定（对外 `ACCOUNT_LOCKED`，E2/401）。
	ErrLocked = errors.New("risk: account locked")
	// ErrUnavailable 表示 Redis 不可用（对外 `REDIS_UNAVAILABLE`，E6/503）。
	//
	// **不是**可以忽略的降级信号：调用方必须拒绝本次请求（fail-closed）。
	ErrUnavailable = errors.New("risk: redis unavailable")
	// ErrWeakPolicy 表示阈值/锁定时长不可解释（配置缺陷，不是用户错）。
	ErrWeakPolicy = errors.New("risk: fail_threshold and lock_duration must be positive")
)

// Policy 是失败处理参数，来源是 `login_policies` 的 C 组两列
// （`fail_threshold` / `lock_duration_min`）。**代码里没有默认值**：读不到就是配置缺陷。
type Policy struct {
	// FailThreshold 连败多少次触发锁定（`fail_threshold`）。
	FailThreshold int
	// LockDuration 锁定与计数窗口的时长（`lock_duration_min`，秒级精度见调用方换算）。
	LockDuration time.Duration
}

// 键名前缀（D3-D 冻结；改这里等于改判据，必须同步 `decisions.md` 与 AC-LOGIN-03）。
const (
	failKeyPrefix = "login:fail:"
	lockKeyPrefix = "login:lock:"
)

// FailKey 返回失败计数键（`login:fail:{user_id}`）。
func FailKey(userID string) string { return failKeyPrefix + userID }

// LockKey 返回锁定标记键（`login:lock:{user_id}`）。
func LockKey(userID string) string { return lockKeyPrefix + userID }

// recordFailureScript 是"一次失败"的**原子**落账（INCR + 窗口 TTL + 达阈值即置锁）。
//
// 为什么用脚本而不是两条命令：两条命令之间进程崩溃会留下"计数涨了、没 TTL"的键，
// 那个键永不过期，等于把账号**永久**计数下去（下一次失败直接撞阈值）。
// `n == 1` 只给新建的键设 TTL：每次失败都刷新 TTL 会让"慢慢试"的人永远不被清账。
//
// KEYS[1]=fail, KEYS[2]=lock；ARGV[1]=fail 窗口 PX, ARGV[2]=阈值, ARGV[3]=锁定时长 PX。
var recordFailureScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
if n >= tonumber(ARGV[2]) then
  redis.call('SET', KEYS[2], '1', 'PX', ARGV[3])
end
return n
`)

// Guard 是账号级失败计数的唯一实现（无本地状态：多副本共享同一个 Redis 就是"同一份计数"）。
type Guard struct {
	rdb *redis.Client
}

// New 绑定一个 Redis 连接。**启动期不 ping**：Redis 挂了进程要活着、业务全拒（D5-₂ 同理），
// 探活由 `/readyz` 与每次调用负责。
func New(addr, password string) (*Guard, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, fmt.Errorf("%w: empty redis address", ErrUnavailable)
	}
	return &Guard{rdb: redis.NewClient(&redis.Options{Addr: strings.TrimSpace(addr), Password: password})}, nil
}

// NewWithClient 用已构造好的客户端（测试与装配复用同一个连接时用）。
func NewWithClient(c *redis.Client) (*Guard, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: nil redis client", ErrUnavailable)
	}
	return &Guard{rdb: c}, nil
}

// Ping 探活（`/readyz` 与启动自检用）。
func (g *Guard) Ping(ctx context.Context) error {
	if g == nil || g.rdb == nil {
		return ErrUnavailable
	}
	if err := g.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// Status 读当前状态：还剩几次尝试、是否已锁定。
//
// 锁定判定**只看 lock 键**（不拿"计数到了阈值"倒推锁定）：锁可以提前解除（运维删键），
// 也可以用两个维度独立过期；倒推会让"删了锁键但计数还在"的账号继续被挡在门外。
func (g *Guard) Status(ctx context.Context, userID string, p Policy) (attemptsLeft int, locked bool, err error) {
	if err := p.validate(); err != nil {
		return 0, false, err
	}
	if strings.TrimSpace(userID) == "" {
		// 没有 user_id（注册前的匿名挑战）就没有"账号级"计数可言，如实返回"未锁定 + 满额"。
		return p.FailThreshold, false, nil
	}
	if g == nil || g.rdb == nil {
		return 0, false, ErrUnavailable
	}
	pipe := g.rdb.Pipeline()
	countCmd := pipe.Get(ctx, FailKey(userID))
	lockCmd := pipe.Exists(ctx, LockKey(userID))
	if _, execErr := pipe.Exec(ctx); execErr != nil && !errors.Is(execErr, redis.Nil) {
		return 0, false, fmt.Errorf("%w: %v", ErrUnavailable, execErr)
	}
	locked = lockCmd.Val() > 0
	var count int
	if v, cErr := countCmd.Int(); cErr == nil {
		count = v
	} else if !errors.Is(cErr, redis.Nil) {
		return 0, false, fmt.Errorf("%w: %v", ErrUnavailable, cErr)
	}
	return remaining(count, p.FailThreshold), locked, nil
}

// RecordFailure 记一次失败，返回**本次之后**的剩余次数与是否已锁定。
//
// 返回 `locked=true` 的那一次就是"达到阈值"的那一次：调用方据此把 `INVALID_CREDENTIAL`
// 换成 `ACCOUNT_LOCKED`（AC-LOGIN-03 的"连续失败达阈值 → ACCOUNT_LOCKED"）。
func (g *Guard) RecordFailure(ctx context.Context, userID string, p Policy) (attemptsLeft int, locked bool, err error) {
	if err := p.validate(); err != nil {
		return 0, false, err
	}
	if strings.TrimSpace(userID) == "" {
		return p.FailThreshold, false, nil
	}
	if g == nil || g.rdb == nil {
		return 0, false, ErrUnavailable
	}
	window := p.LockDuration.Milliseconds()
	n, runErr := recordFailureScript.Run(ctx, g.rdb,
		[]string{FailKey(userID), LockKey(userID)},
		window, p.FailThreshold, p.LockDuration.Milliseconds()).Int()
	if runErr != nil {
		return 0, false, fmt.Errorf("%w: %v", ErrUnavailable, runErr)
	}
	return remaining(n, p.FailThreshold), n >= p.FailThreshold, nil
}

// Reset 清零账号级失败计数（成功登录时调用；锁键一并清掉）。
//
// 清锁是有意的：能成功通过认证说明凭据是对的，继续挡着只会制造"证明自己是自己还被拒"。
func (g *Guard) Reset(ctx context.Context, userID string) error {
	if strings.TrimSpace(userID) == "" {
		return nil
	}
	if g == nil || g.rdb == nil {
		return ErrUnavailable
	}
	if err := g.rdb.Del(ctx, FailKey(userID), LockKey(userID)).Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

func remaining(count, threshold int) int {
	left := threshold - count
	if left < 0 {
		return 0
	}
	return left
}

func (p Policy) validate() error {
	if p.FailThreshold <= 0 || p.LockDuration <= 0 {
		return fmt.Errorf("%w: threshold=%d lock=%s", ErrWeakPolicy, p.FailThreshold, p.LockDuration)
	}
	return nil
}
