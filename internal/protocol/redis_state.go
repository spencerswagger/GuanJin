package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/spencerswagger/GuanJin/internal/httpx"
	"github.com/spencerswagger/GuanJin/internal/store"
)

const (
	keyAuthCode       = "guanjin:authcode:"
	keyRefresh        = "guanjin:refresh:"
	keySessionRefresh = "guanjin:sess-refresh:"

	authCodeTTL = 10 * time.Minute
)

type authCodeData struct {
	ClientID      string   `json:"client_id"`
	RedirectURI   string   `json:"redirect_uri"`
	UserID        string   `json:"sub"`
	SessionID     string   `json:"sid"`
	ACR           string   `json:"acr"`
	AMR           []string `json:"amr"`
	Nonce         string   `json:"nonce"`
	Scope         string   `json:"scope"`
	CodeChallenge string   `json:"code_challenge"`
}

type StateStore struct {
	rdb *store.Redis
}

func NewStateStore(rdb *store.Redis) *StateStore {
	return &StateStore{rdb: rdb}
}

func (s *StateStore) client() *redis.Client {
	if s.rdb == nil {
		return nil
	}
	return s.rdb.Client()
}

func redisUnavailable() error {
	return httpx.New("REDIS_UNAVAILABLE", httpx.MessageFor("REDIS_UNAVAILABLE"))
}

func (s *StateStore) PutAuthCode(ctx context.Context, code string, data authCodeData, ttl time.Duration) error {
	client := s.client()
	if client == nil {
		return redisUnavailable()
	}
	if ttl <= 0 {
		ttl = authCodeTTL
	}
	blob, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if err := client.Set(ctx, keyAuthCode+code, blob, ttl).Err(); err != nil {
		return redisUnavailable()
	}
	return nil
}

func (s *StateStore) TakeAuthCode(ctx context.Context, code string) (*authCodeData, error) {
	client := s.client()
	if client == nil {
		return nil, redisUnavailable()
	}
	blob, err := client.GetDel(ctx, keyAuthCode+code).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, redisUnavailable()
	}
	var data authCodeData
	if err := json.Unmarshal(blob, &data); err != nil {
		return nil, nil
	}
	return &data, nil
}

func (s *StateStore) IssueRefresh(ctx context.Context, sessionID, token string, ttl time.Duration) error {
	client := s.client()
	if client == nil {
		return redisUnavailable()
	}
	pipe := client.TxPipeline()
	pipe.Set(ctx, keyRefresh+token, sessionID, ttl)
	pipe.Set(ctx, keySessionRefresh+sessionID, token, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return redisUnavailable()
	}
	return nil
}

func (s *StateStore) LookupRefresh(ctx context.Context, token string) (string, bool, error) {
	client := s.client()
	if client == nil {
		return "", false, redisUnavailable()
	}
	value, err := client.Get(ctx, keyRefresh+token).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, redisUnavailable()
	}
	return value, true, nil
}

func (s *StateStore) CurrentSessionRefresh(ctx context.Context, sessionID string) (string, error) {
	client := s.client()
	if client == nil {
		return "", redisUnavailable()
	}
	value, err := client.Get(ctx, keySessionRefresh+sessionID).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", redisUnavailable()
	}
	return value, nil
}

func (s *StateStore) RotateRefresh(ctx context.Context, sessionID, oldToken, newToken string, ttl time.Duration) error {
	client := s.client()
	if client == nil {
		return redisUnavailable()
	}
	pipe := client.TxPipeline()
	pipe.Set(ctx, keyRefresh+newToken, sessionID, ttl)
	pipe.Set(ctx, keySessionRefresh+sessionID, newToken, ttl)
	pipe.Expire(ctx, keyRefresh+oldToken, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return redisUnavailable()
	}
	return nil
}

func (s *StateStore) RevokeRefreshFamily(ctx context.Context, sessionID string) error {
	client := s.client()
	if client == nil {
		return redisUnavailable()
	}
	if err := client.Del(ctx, keySessionRefresh+sessionID).Err(); err != nil {
		return redisUnavailable()
	}
	return nil
}
