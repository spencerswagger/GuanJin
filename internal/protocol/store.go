package protocol

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// 授权码 / 刷新令牌的存储（裁定 A：走 Redis，不新增表）。
//
// 键名**冻结**（改动等于改判据）：
//   - 授权码 `auth:code:{code}`，TTL = `access_token_ttl_sec`；
//   - 刷新令牌 `auth:rt:{token}`，TTL = `refresh_token_ttl_sec`。
//
// 一次性消费必须**原子**（Redis `GETDEL`）：先 GET 再 DEL 会让两个并发请求都拿到同一个码。
// 落表排 M2（技术债，见 PR 风险段）。
const (
	codeKeyPrefix = "auth:code:"
	rtKeyPrefix   = "auth:rt:"
)

// CodeKey 返回授权码的键名（冻结）。
func CodeKey(code string) string { return codeKeyPrefix + code }

// RefreshKey 返回刷新令牌的键名（冻结）。
func RefreshKey(token string) string { return rtKeyPrefix + token }

// ErrCodeGone 表示授权码不存在/已被消费（对外 `INVALID_GRANT`）。
var ErrCodeGone = errors.New("protocol: authorization code is gone")

// ErrRefreshGone 表示刷新令牌不存在/已被消费（对外 `REFRESH_TOKEN_REUSED`）。
var ErrRefreshGone = errors.New("protocol: refresh token is gone")

// ErrStoreUnavailable 表示 Redis 不可用（fail-closed：拒绝签发，不降级）。
var ErrStoreUnavailable = errors.New("protocol: code store unavailable")

// CodeStore 是授权码/刷新令牌的**窄接口**（实现见 `RedisStore`，测试用内存桩）。
type CodeStore interface {
	// Put 写一条并设 TTL。
	Put(ctx context.Context, key, value string, ttl time.Duration) error
	// Take 原子取出并删除（GETDEL）。不存在 → `ErrCodeGone` / `ErrRefreshGone`（由调用方给哨兵）。
	Take(ctx context.Context, key string, gone error) (string, error)
	// Delete 幂等删除（撤销/轮换后的清理）。
	Delete(ctx context.Context, key string) error
}

// RedisStore 是 `CodeStore` 的 Redis 实现。
type RedisStore struct{ rdb *redis.Client }

// NewRedisStore 用已构造好的客户端（装配层负责连接与探活）。
func NewRedisStore(c *redis.Client) (*RedisStore, error) {
	if c == nil {
		return nil, ErrStoreUnavailable
	}
	return &RedisStore{rdb: c}, nil
}

// Put 见 CodeStore。
func (s *RedisStore) Put(ctx context.Context, key, value string, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("protocol: ttl must be positive")
	}
	if err := s.rdb.Set(ctx, key, value, ttl).Err(); err != nil {
		return errors.Join(ErrStoreUnavailable, err)
	}
	return nil
}

// Take 见 CodeStore（`GETDEL` 保证"两个并发请求只有一个能拿到"）。
func (s *RedisStore) Take(ctx context.Context, key string, gone error) (string, error) {
	val, err := s.rdb.GetDel(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", gone
	}
	if err != nil {
		return "", errors.Join(ErrStoreUnavailable, err)
	}
	return val, nil
}

// Delete 见 CodeStore。
func (s *RedisStore) Delete(ctx context.Context, key string) error {
	if err := s.rdb.Del(ctx, key).Err(); err != nil {
		return errors.Join(ErrStoreUnavailable, err)
	}
	return nil
}
