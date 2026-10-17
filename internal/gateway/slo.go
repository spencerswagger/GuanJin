package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/spencerswagger/GuanJin/internal/crypto"
	"github.com/spencerswagger/GuanJin/internal/store"
)

type SLOEvent struct {
	UserID     string    `json:"user_id"`
	ClientID   string    `json:"client_id"`
	SessionID  string    `json:"session_id"`
	Reason     string    `json:"reason"`
	OccurredAt time.Time `json:"occurred_at"`
}

type LogoutNotifier interface {
	NotifyLogout(ctx context.Context, event SLOEvent) error
}

type SLO struct {
	visited  *store.UserVisitedClientRepo
	notifier LogoutNotifier
}

func NewSLO(visited *store.UserVisitedClientRepo, notifier LogoutNotifier) *SLO {
	if notifier == nil {
		notifier = NoopSLO{}
	}
	return &SLO{visited: visited, notifier: notifier}
}

func (s *SLO) OnLogout(ctx context.Context, userID uuid.UUID, sessionID uuid.UUID, reason string) (int, error) {
	rows, err := s.visited.ListByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	for _, row := range rows {
		event := SLOEvent{
			UserID:     userID.String(),
			ClientID:   row.ClientID,
			SessionID:  sessionID.String(),
			Reason:     reason,
			OccurredAt: now,
		}
		if err := s.notifier.NotifyLogout(ctx, event); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

type NoopSLO struct{}

func (NoopSLO) NotifyLogout(context.Context, SLOEvent) error { return nil }

type RecordingSLO struct {
	mu     sync.Mutex
	events []SLOEvent
}

func NewRecordingSLO() *RecordingSLO {
	return &RecordingSLO{}
}

func (n *RecordingSLO) NotifyLogout(_ context.Context, event SLOEvent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, event)
	return nil
}

func (n *RecordingSLO) Events() []SLOEvent {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]SLOEvent{}, n.events...)
}

func (n *RecordingSLO) ClientIDs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.events))
	for _, e := range n.events {
		out = append(out, e.ClientID)
	}
	return out
}

func (n *RecordingSLO) Count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.events)
}

type SM2HTTPNotifier struct {
	keys    *store.SigningKeyRepo
	client  *http.Client
	baseURL string
	issuer  string
}

func NewSM2HTTPNotifier(keys *store.SigningKeyRepo, baseURL string, client *http.Client) *SM2HTTPNotifier {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &SM2HTTPNotifier{
		keys:    keys,
		client:  client,
		baseURL: strings.TrimRight(baseURL, "/"),
		issuer:  AssertionIssuer,
	}
}

func (n *SM2HTTPNotifier) NotifyLogout(ctx context.Context, event SLOEvent) error {
	kid, priv, err := n.keys.ActiveSigner(ctx, crypto.PurposeGateway)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	jti, err := uuid.NewV7()
	if err != nil {
		return err
	}
	claims := jwt.MapClaims{
		"iss":    n.issuer,
		"sub":    event.UserID,
		"aud":    event.ClientID,
		"typ":    "slo",
		"sid":    event.SessionID,
		"reason": event.Reason,
		"iat":    now.Unix(),
		"exp":    now.Add(AssertionTTL).Unix(),
		"jti":    jti.String(),
	}
	token := jwt.NewWithClaims(crypto.SigningMethodSM3WithSM2, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(priv)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{
		"user_id":    event.UserID,
		"session_id": event.SessionID,
		"reason":     event.Reason,
		"assertion":  signed,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.baseURL+"/"+event.ClientID+"/slo", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("slo notify %s returned %d", event.ClientID, resp.StatusCode)
	}
	return nil
}
