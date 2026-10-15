package store

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type Redis struct {
	client  *redis.Client
	timeout time.Duration
}

func NewRedis(rawURL string, timeout time.Duration) (*Redis, error) {
	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	opts.DialTimeout = timeout
	opts.ReadTimeout = timeout
	opts.WriteTimeout = timeout
	return &Redis{client: redis.NewClient(opts), timeout: timeout}, nil
}

func (r *Redis) Client() *redis.Client {
	return r.client
}

func (r *Redis) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	return r.client.Ping(pingCtx).Err()
}

func (r *Redis) Close() error {
	return r.client.Close()
}
