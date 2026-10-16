package flow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/spencerswagger/GuanJin/internal/store"
)

const (
	flowKeyPrefix = "guanjin:flow:"
	FlowTTL       = 15 * time.Minute
)

var ErrStoreUnavailable = errors.New("REDIS_UNAVAILABLE")

type State struct {
	FlowID        string    `json:"flow_id"`
	ClientID      string    `json:"client_id"`
	ClientName    string    `json:"client_name"`
	Tier          string    `json:"tier"`
	RedirectURI   string    `json:"redirect_uri"`
	Scope         string    `json:"scope"`
	State         string    `json:"state"`
	Nonce         string    `json:"nonce"`
	CodeChallenge string    `json:"code_challenge"`
	TargetACR     string    `json:"target_acr"`
	CurrentACR    string    `json:"current_acr"`
	Achieved      []string  `json:"achieved"`
	UserID        string    `json:"user_id,omitempty"`
	Satisfied     bool      `json:"satisfied"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (s *State) Expired(now time.Time) bool {
	return !s.ExpiresAt.IsZero() && now.After(s.ExpiresAt)
}

func (s *State) HasFactor(typ string) bool {
	for _, f := range s.Achieved {
		if f == typ {
			return true
		}
	}
	return false
}

func (s *State) AddFactor(typ string) {
	if typ == "" || s.HasFactor(typ) {
		return
	}
	s.Achieved = append(s.Achieved, typ)
}

type Store struct {
	rdb *store.Redis
}

func NewStore(rdb *store.Redis) *Store {
	return &Store{rdb: rdb}
}

func (s *Store) client() *redis.Client {
	if s.rdb == nil {
		return nil
	}
	return s.rdb.Client()
}

func (s *Store) Put(ctx context.Context, state *State) error {
	client := s.client()
	if client == nil {
		return ErrStoreUnavailable
	}
	blob, err := json.Marshal(state)
	if err != nil {
		return err
	}
	ttl := FlowTTL
	if !state.ExpiresAt.IsZero() {
		if remaining := time.Until(state.ExpiresAt); remaining > 0 && remaining < ttl {
			ttl = remaining
		}
	}
	if err := client.Set(ctx, flowKeyPrefix+state.FlowID, blob, ttl).Err(); err != nil {
		return ErrStoreUnavailable
	}
	return nil
}

func (s *Store) Get(ctx context.Context, flowID string) (*State, error) {
	client := s.client()
	if client == nil {
		return nil, ErrStoreUnavailable
	}
	blob, err := client.Get(ctx, flowKeyPrefix+flowID).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrStoreUnavailable
	}
	var state State
	if err := json.Unmarshal(blob, &state); err != nil {
		return nil, nil
	}
	return &state, nil
}

func (s *Store) Delete(ctx context.Context, flowID string) error {
	client := s.client()
	if client == nil {
		return ErrStoreUnavailable
	}
	if err := client.Del(ctx, flowKeyPrefix+flowID).Err(); err != nil {
		return ErrStoreUnavailable
	}
	return nil
}
