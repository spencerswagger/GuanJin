package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewRedisRejectsInvalidURL(t *testing.T) {
	_, err := NewRedis("not-a-valid-url", time.Second)
	require.Error(t, err)
}

func TestRedisHealthCheckReportsDownWithoutServer(t *testing.T) {
	r, err := NewRedis("redis://127.0.0.1:1/0", 200*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })

	require.NotNil(t, r.Client())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.Error(t, r.Ping(ctx), "ping must fail when no Redis server is reachable (no in-memory fallback)")
}
