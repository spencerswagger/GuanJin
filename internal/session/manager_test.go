package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是 Manager 的**纯单测**（不建库）：会话状态机、并发上限、撤销级联、审计写入
// 全部用内存假实现钉住。真库只用来验 SQL 形状与 DB 约束（`repo_sessions_db_test.go`）。

// fakeStore 是 SessionStore 的内存实现。
type fakeStore struct {
	sessions map[uuid.UUID]*model.Session
	policies map[string]*store.SessionPolicy
	names    map[string]string
	users    map[uuid.UUID]bool

	createErr error
	policyErr error
	revokeErr error

	// 记录调用，供断言"副作用发生了没有"。
	revoked      []uuid.UUID
	revokedAll   []uuid.UUID
	revokedOther []uuid.UUID
	touched      []uuid.UUID
	evicted      []uuid.UUID
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		sessions: map[uuid.UUID]*model.Session{},
		policies: map[string]*store.SessionPolicy{},
		names:    map[string]string{},
		users:    map[uuid.UUID]bool{},
	}
}

func (f *fakeStore) Create(_ context.Context, s *model.Session) error {
	if f.createErr != nil {
		return f.createErr
	}
	if s.ID == uuid.Nil {
		id, _ := uuid.NewV7()
		s.ID = id
	}
	cp := *s
	f.sessions[s.ID] = &cp
	return nil
}

func (f *fakeStore) GetByID(_ context.Context, id uuid.UUID) (*model.Session, error) {
	s, ok := f.sessions[id]
	if !ok {
		return nil, store.ErrSessionNotFound
	}
	cp := *s
	return &cp, nil
}

func (f *fakeStore) ListByUser(_ context.Context, userID uuid.UUID, _ int) ([]model.Session, error) {
	var out []model.Session
	for _, s := range f.sessions {
		if s.UserID == userID {
			out = append(out, *s)
		}
	}
	return out, nil
}

func (f *fakeStore) ActiveIDsByUser(_ context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	var out []uuid.UUID
	for id, s := range f.sessions {
		if s.UserID == userID && s.Status == model.SessionStatusActive {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeStore) List(_ context.Context, q store.SessionQuery) (*store.SessionPage, error) {
	var out []model.Session
	for _, s := range f.sessions {
		if q.UserID != nil && s.UserID != *q.UserID {
			continue
		}
		if q.Active != nil && *q.Active && s.Status != model.SessionStatusActive {
			continue
		}
		out = append(out, *s)
	}
	return &store.SessionPage{Items: out}, nil
}

func (f *fakeStore) RevokeByID(_ context.Context, id uuid.UUID, reason string, at time.Time) (int64, error) {
	if f.revokeErr != nil {
		return 0, f.revokeErr
	}
	s, ok := f.sessions[id]
	if !ok || s.Status != model.SessionStatusActive {
		return 0, nil
	}
	s.Status = model.SessionStatusRevoked
	ts := at
	s.RevokedAt = &ts
	s.RevokeReason = &reason
	f.revoked = append(f.revoked, id)
	return 1, nil
}

func (f *fakeStore) RevokeAllByUser(_ context.Context, userID uuid.UUID, reason string, at time.Time) (int64, error) {
	if f.revokeErr != nil {
		return 0, f.revokeErr
	}
	var n int64
	for id, s := range f.sessions {
		if s.UserID != userID || s.Status != model.SessionStatusActive {
			continue
		}
		ts := at
		s.Status = model.SessionStatusRevoked
		s.RevokedAt = &ts
		s.RevokeReason = &reason
		f.revokedAll = append(f.revokedAll, id)
		n++
	}
	return n, nil
}

func (f *fakeStore) RevokeOthers(_ context.Context, userID, keepID uuid.UUID, reason string, at time.Time) (int64, error) {
	if f.revokeErr != nil {
		return 0, f.revokeErr
	}
	var n int64
	for id, s := range f.sessions {
		if s.UserID != userID || id == keepID || s.Status != model.SessionStatusActive {
			continue
		}
		ts := at
		s.Status = model.SessionStatusRevoked
		s.RevokedAt = &ts
		s.RevokeReason = &reason
		f.revokedOther = append(f.revokedOther, id)
		n++
	}
	return n, nil
}

func (f *fakeStore) CountActiveByUserClient(_ context.Context, userID uuid.UUID, clientID string) (int64, error) {
	var n int64
	for _, s := range f.sessions {
		if s.UserID == userID && s.ClientID == clientID && s.Status == model.SessionStatusActive {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) EvictOldestActive(_ context.Context, userID uuid.UUID, clientID, reason string, at time.Time) (uuid.UUID, error) {
	var oldest *model.Session
	var oldestID uuid.UUID
	for id, s := range f.sessions {
		if s.UserID != userID || s.ClientID != clientID || s.Status != model.SessionStatusActive {
			continue
		}
		if oldest == nil || s.IssuedAt.Before(oldest.IssuedAt) {
			oldest = s
			oldestID = id
		}
	}
	if oldest == nil {
		return uuid.Nil, nil
	}
	ts := at
	oldest.Status = model.SessionStatusRevoked
	oldest.RevokedAt = &ts
	oldest.RevokeReason = &reason
	f.evicted = append(f.evicted, oldestID)
	return oldestID, nil
}

func (f *fakeStore) TouchLastSeen(_ context.Context, id uuid.UUID, at, idle time.Time) (int64, error) {
	s, ok := f.sessions[id]
	if !ok || s.Status != model.SessionStatusActive {
		return 0, nil
	}
	ts := at
	s.LastSeenAt = &ts
	s.IdleExpiresAt = idle
	f.touched = append(f.touched, id)
	return 1, nil
}

func (f *fakeStore) LoadSessionPolicy(_ context.Context, clientID string) (*store.SessionPolicy, error) {
	if f.policyErr != nil {
		return nil, f.policyErr
	}
	p, ok := f.policies[clientID]
	if !ok {
		return nil, store.ErrPolicyNotConfigured
	}
	return p, nil
}

func (f *fakeStore) ClientNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if n, ok := f.names[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

func (f *fakeStore) UserExists(_ context.Context, id uuid.UUID) (bool, error) {
	return f.users[id], nil
}

// fakeFlows 记录"哪些会话的活跃流被作废"。
type fakeFlows struct {
	abandoned []uuid.UUID
	err       error
}

func (f *fakeFlows) AbandonBySession(_ context.Context, id uuid.UUID) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.abandoned = append(f.abandoned, id)
	return 1, nil
}

// fakeEvents 记录写出的 auth_events。
type fakeEvents struct {
	events []model.AuthEvent
	err    error
}

func (f *fakeEvents) Append(_ context.Context, e *model.AuthEvent) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, *e)
	return nil
}

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func newManager(t *testing.T, st SessionStore, flows FlowAbandoner, events EventWriter, now time.Time) *Manager {
	t.Helper()
	m, err := NewManager(st, flows, events, Options{Now: fixedNow(now)})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func mustID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid v7: %v", err)
	}
	return id
}

func TestNewManagerRequiresDependencies(t *testing.T) {
	st := newFakeStore()
	flows := &fakeFlows{}
	events := &fakeEvents{}
	cases := []struct {
		name   string
		st     SessionStore
		flows  FlowAbandoner
		events EventWriter
	}{
		{"no store", nil, flows, events},
		{"no flows", st, nil, events},
		{"no events", st, flows, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewManager(tc.st, tc.flows, tc.events, Options{}); !errors.Is(err, ErrNilDependency) {
				t.Fatalf("want ErrNilDependency, got %v", err)
			}
		})
	}
}

func TestCreateComputesTimeoutsFromPolicy(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	st := newFakeStore()
	st.policies["web"] = &store.SessionPolicy{
		ClientID:        "web",
		Tier:            "L2",
		IdleTimeout:     30 * time.Minute,
		AbsoluteTimeout: 12 * time.Hour,
		OverflowAction:  "deny_new",
		SSOLogoutScope:  "all_clients",
	}
	m := newManager(t, st, &fakeFlows{}, &fakeEvents{}, now)

	s, err := m.Create(context.Background(), CreateInput{
		UserID: mustID(t), ClientID: "web", ACR: "AAL1", AMR: []string{"pwd"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.Status != model.SessionStatusActive {
		t.Fatalf("status = %q, want active", s.Status)
	}
	if !s.IssuedAt.Equal(now) {
		t.Fatalf("issued_at = %s, want %s", s.IssuedAt, now)
	}
	if !s.IdleExpiresAt.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("idle_expires_at = %s", s.IdleExpiresAt)
	}
	if !s.ExpiresAt.Equal(now.Add(12 * time.Hour)) {
		t.Fatalf("expires_at = %s", s.ExpiresAt)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	st := newFakeStore()
	m := newManager(t, st, &fakeFlows{}, &fakeEvents{}, time.Now())
	cases := []CreateInput{
		{ClientID: "web", ACR: "AAL1"},       // 缺 user_id
		{UserID: mustID(t), ACR: "AAL1"},     // 缺 client_id
		{UserID: mustID(t), ClientID: "web"}, // 缺 acr
	}
	for i, in := range cases {
		if _, err := m.Create(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("case %d: want ErrInvalidInput, got %v", i, err)
		}
	}
}

func TestCreateRejectsIdleGreaterThanAbsolute(t *testing.T) {
	st := newFakeStore()
	st.policies["web"] = &store.SessionPolicy{
		IdleTimeout: 13 * time.Hour, AbsoluteTimeout: 12 * time.Hour,
	}
	m := newManager(t, st, &fakeFlows{}, &fakeEvents{}, time.Now())
	if _, err := m.Create(context.Background(), CreateInput{
		UserID: mustID(t), ClientID: "web", ACR: "AAL1",
	}); !errors.Is(err, ErrPolicyTimeOrder) {
		t.Fatalf("want ErrPolicyTimeOrder, got %v", err)
	}
}

func TestCreateDenyNewAtLimit(t *testing.T) {
	now := time.Now().UTC()
	user := mustID(t)
	st := newFakeStore()
	max := 1
	st.policies["web"] = &store.SessionPolicy{
		IdleTimeout: time.Minute, AbsoluteTimeout: time.Hour,
		MaxConcurrent: &max, OverflowAction: "deny_new",
	}
	st.sessions[mustID(t)] = &model.Session{
		UserID: user, ClientID: "web", Status: model.SessionStatusActive,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute),
	}
	m := newManager(t, st, &fakeFlows{}, &fakeEvents{}, now)

	_, err := m.Create(context.Background(), CreateInput{UserID: user, ClientID: "web", ACR: "AAL1"})
	if !errors.Is(err, ErrConcurrentLimit) {
		t.Fatalf("want ErrConcurrentLimit, got %v", err)
	}
}

func TestCreateEvictOldestRevokesOldestAndCascades(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	oldest := mustID(t)
	st := newFakeStore()
	max := 1
	st.policies["web"] = &store.SessionPolicy{
		IdleTimeout: time.Minute, AbsoluteTimeout: time.Hour,
		MaxConcurrent: &max, OverflowAction: "evict_oldest",
	}
	st.sessions[oldest] = &model.Session{
		UserID: user, ClientID: "web", Status: model.SessionStatusActive, ACR: "AAL1",
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute),
	}
	flows := &fakeFlows{}
	events := &fakeEvents{}
	m := newManager(t, st, flows, events, now)

	created, err := m.Create(context.Background(), CreateInput{UserID: user, ClientID: "web", ACR: "AAL1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created == nil {
		t.Fatal("Create returned nil session")
	}
	if got := st.sessions[oldest]; got.Status != model.SessionStatusRevoked {
		t.Fatalf("oldest status = %q, want revoked", got.Status)
	}
	if len(flows.abandoned) != 1 || flows.abandoned[0] != oldest {
		t.Fatalf("abandon cascade = %v, want [%s]", flows.abandoned, oldest)
	}
	if len(events.events) != 1 {
		t.Fatalf("auth events = %d, want 1", len(events.events))
	}
	ev := events.events[0]
	if ev.EventType != EventRevoke || ev.Reason == nil || *ev.Reason != string(ReasonConcurrentLimit) {
		t.Fatalf("event = %+v", ev)
	}
}

func TestCreateEvictOldestAlertLogsButStillCreates(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	oldest := mustID(t)
	st := newFakeStore()
	max := 1
	st.policies["web"] = &store.SessionPolicy{
		IdleTimeout: time.Minute, AbsoluteTimeout: time.Hour,
		MaxConcurrent: &max, OverflowAction: "evict_oldest_alert",
	}
	st.sessions[oldest] = &model.Session{
		UserID: user, ClientID: "web", Status: model.SessionStatusActive,
		IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute),
	}
	flows := &fakeFlows{err: errors.New("db down")}
	m := newManager(t, st, flows, &fakeEvents{}, now)

	if _, err := m.Create(context.Background(), CreateInput{UserID: user, ClientID: "web", ACR: "AAL1"}); err != nil {
		t.Fatalf("evict_oldest_alert must still create: %v", err)
	}
}

func TestCreateUnknownOverflowAction(t *testing.T) {
	st := newFakeStore()
	max := 0
	st.policies["web"] = &store.SessionPolicy{
		IdleTimeout: time.Minute, AbsoluteTimeout: time.Hour,
		MaxConcurrent: &max, OverflowAction: "explode",
	}
	m := newManager(t, st, &fakeFlows{}, &fakeEvents{}, time.Now())
	if _, err := m.Create(context.Background(), CreateInput{
		UserID: mustID(t), ClientID: "web", ACR: "AAL1",
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

func TestValidateStates(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	active := mustID(t)
	revoked := mustID(t)
	expiredStatus := mustID(t)
	idleOver := mustID(t)
	absOver := mustID(t)

	st := newFakeStore()
	st.sessions[active] = &model.Session{UserID: user, Status: model.SessionStatusActive,
		ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)}
	st.sessions[revoked] = &model.Session{UserID: user, Status: model.SessionStatusRevoked,
		ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)}
	st.sessions[expiredStatus] = &model.Session{UserID: user, Status: model.SessionStatusExpired,
		ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)}
	st.sessions[idleOver] = &model.Session{UserID: user, Status: model.SessionStatusActive,
		ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(-time.Second)}
	st.sessions[absOver] = &model.Session{UserID: user, Status: model.SessionStatusActive,
		ExpiresAt: now.Add(-time.Second), IdleExpiresAt: now.Add(time.Minute)}

	m := newManager(t, st, &fakeFlows{}, &fakeEvents{}, now)

	if _, err := m.Validate(context.Background(), active); err != nil {
		t.Fatalf("active: %v", err)
	}
	cases := []struct {
		name string
		id   uuid.UUID
		want error
	}{
		{"revoked", revoked, ErrSessionRevoked},
		{"status expired", expiredStatus, ErrSessionExpired},
		{"idle timeout", idleOver, ErrSessionExpired},
		{"absolute timeout", absOver, ErrSessionExpired},
		{"missing", mustID(t), ErrSessionNotFound},
		{"nil id", uuid.Nil, ErrSessionNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := m.Validate(context.Background(), tc.id); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestRevokeCascadesAndWritesEvent(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	id := mustID(t)
	st := newFakeStore()
	st.sessions[id] = &model.Session{UserID: user, ClientID: "web", ACR: "AAL2",
		Status: model.SessionStatusActive, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)}
	flows := &fakeFlows{}
	events := &fakeEvents{}
	m := newManager(t, st, flows, events, now)
	tr := "trace-1"

	if err := m.Revoke(context.Background(), RevokeInput{
		SessionID: id, Reason: ReasonAdmin, EventType: EventRevoke, TraceID: &tr,
	}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if st.sessions[id].Status != model.SessionStatusRevoked {
		t.Fatalf("status = %q", st.sessions[id].Status)
	}
	if len(flows.abandoned) != 1 || flows.abandoned[0] != id {
		t.Fatalf("abandoned = %v", flows.abandoned)
	}
	if len(events.events) != 1 {
		t.Fatalf("events = %d", len(events.events))
	}
	if events.events[0].TraceID == nil || *events.events[0].TraceID != tr {
		t.Fatalf("trace not propagated: %+v", events.events[0])
	}
	if events.events[0].ACR == nil || *events.events[0].ACR != "AAL2" {
		t.Fatalf("acr not propagated: %+v", events.events[0])
	}
}

func TestRevokeIsIdempotentWhenNotActive(t *testing.T) {
	now := time.Now().UTC()
	user := mustID(t)
	id := mustID(t)
	st := newFakeStore()
	ts := now
	reason := "admin"
	st.sessions[id] = &model.Session{UserID: user, Status: model.SessionStatusRevoked,
		RevokedAt: &ts, RevokeReason: &reason}
	flows := &fakeFlows{}
	events := &fakeEvents{}
	m := newManager(t, st, flows, events, now)

	if err := m.Revoke(context.Background(), RevokeInput{
		SessionID: id, Reason: ReasonAdmin, EventType: EventRevoke,
	}); err != nil {
		t.Fatalf("Revoke on already-revoked must be a no-op success: %v", err)
	}
	if len(flows.abandoned) != 0 || len(events.events) != 0 {
		t.Fatalf("no side effects expected: flows=%v events=%v", flows.abandoned, events.events)
	}
}

func TestRevokeMissingSession(t *testing.T) {
	m := newManager(t, newFakeStore(), &fakeFlows{}, &fakeEvents{}, time.Now())
	err := m.Revoke(context.Background(), RevokeInput{
		SessionID: mustID(t), Reason: ReasonAdmin, EventType: EventRevoke,
	})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
}

func TestRevokeInputValidation(t *testing.T) {
	m := newManager(t, newFakeStore(), &fakeFlows{}, &fakeEvents{}, time.Now())
	cases := []RevokeInput{
		{Reason: ReasonAdmin, EventType: EventRevoke},  // 缺 id
		{SessionID: mustID(t), EventType: EventRevoke}, // 缺 reason
		{SessionID: mustID(t), Reason: ReasonAdmin},    // 缺 event type
	}
	for i, in := range cases {
		if err := m.Revoke(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("case %d: want ErrInvalidInput, got %v", i, err)
		}
	}
}

func TestRevokeAllByUserCascadesEverySession(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	a, b := mustID(t), mustID(t)
	st := newFakeStore()
	for _, id := range []uuid.UUID{a, b} {
		st.sessions[id] = &model.Session{UserID: user, ClientID: "web", Status: model.SessionStatusActive,
			ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)}
	}
	flows := &fakeFlows{}
	events := &fakeEvents{}
	m := newManager(t, st, flows, events, now)

	n, err := m.RevokeAllByUser(context.Background(), user, ReasonAdmin, EventRevoke, nil)
	if err != nil {
		t.Fatalf("RevokeAllByUser: %v", err)
	}
	if n != 2 {
		t.Fatalf("revoked = %d, want 2", n)
	}
	if len(flows.abandoned) != 2 {
		t.Fatalf("abandoned = %v", flows.abandoned)
	}
	if len(events.events) != 2 {
		t.Fatalf("events = %d", len(events.events))
	}
}

func TestRevokeOthersKeepsCurrent(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	keep, other := mustID(t), mustID(t)
	st := newFakeStore()
	for _, id := range []uuid.UUID{keep, other} {
		st.sessions[id] = &model.Session{UserID: user, ClientID: "web", Status: model.SessionStatusActive,
			ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)}
	}
	flows := &fakeFlows{}
	m := newManager(t, st, flows, &fakeEvents{}, now)

	n, err := m.RevokeOthers(context.Background(), user, keep, ReasonPasswordChange, EventRevoke, nil)
	if err != nil {
		t.Fatalf("RevokeOthers: %v", err)
	}
	if n != 1 {
		t.Fatalf("revoked = %d, want 1", n)
	}
	if st.sessions[keep].Status != model.SessionStatusActive {
		t.Fatal("keep session must stay active")
	}
	if st.sessions[other].Status != model.SessionStatusRevoked {
		t.Fatal("other session must be revoked")
	}
	if len(flows.abandoned) != 1 || flows.abandoned[0] != other {
		t.Fatalf("abandoned = %v, want [%s]", flows.abandoned, other)
	}
}

func TestLogoutScopeCurrentClient(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	current, otra := mustID(t), mustID(t)
	st := newFakeStore()
	st.policies["web"] = &store.SessionPolicy{SSOLogoutScope: "current_client"}
	for _, id := range []uuid.UUID{current, otra} {
		st.sessions[id] = &model.Session{UserID: user, ClientID: "web", Status: model.SessionStatusActive,
			ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)}
	}
	m := newManager(t, st, &fakeFlows{}, &fakeEvents{}, now)

	if err := m.Logout(context.Background(), current, nil); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if st.sessions[current].Status != model.SessionStatusRevoked {
		t.Fatal("current session must be revoked")
	}
	if st.sessions[otra].Status != model.SessionStatusActive {
		t.Fatal("other session must stay active under current_client scope")
	}
}

func TestLogoutScopeAllClients(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	current, other := mustID(t), mustID(t)
	st := newFakeStore()
	st.policies["web"] = &store.SessionPolicy{SSOLogoutScope: "all_clients"}
	for _, id := range []uuid.UUID{current, other} {
		st.sessions[id] = &model.Session{UserID: user, ClientID: "web", Status: model.SessionStatusActive,
			ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)}
	}
	events := &fakeEvents{}
	m := newManager(t, st, &fakeFlows{}, events, now)

	if err := m.Logout(context.Background(), current, nil); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if st.sessions[current].Status != model.SessionStatusRevoked || st.sessions[other].Status != model.SessionStatusRevoked {
		t.Fatal("all_clients must revoke every active session")
	}
	for _, e := range events.events {
		if e.EventType != EventLogout {
			t.Fatalf("event type = %q, want logout", e.EventType)
		}
	}
}

func TestLogoutMissingSession(t *testing.T) {
	m := newManager(t, newFakeStore(), &fakeFlows{}, &fakeEvents{}, time.Now())
	if err := m.Logout(context.Background(), mustID(t), nil); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
}

func TestTouchClampsIdleToAbsolute(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	user := mustID(t)
	id := mustID(t)
	st := newFakeStore()
	st.policies["web"] = &store.SessionPolicy{IdleTimeout: 12 * time.Hour, AbsoluteTimeout: 12 * time.Hour}
	st.sessions[id] = &model.Session{UserID: user, ClientID: "web", Status: model.SessionStatusActive,
		ExpiresAt: now.Add(30 * time.Minute), IdleExpiresAt: now.Add(10 * time.Minute)}
	m := newManager(t, st, &fakeFlows{}, &fakeEvents{}, now)

	if err := m.Touch(context.Background(), id); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	if !st.sessions[id].IdleExpiresAt.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("idle clamped = %s, want %s", st.sessions[id].IdleExpiresAt, now.Add(30*time.Minute))
	}
}

func TestGetReturnsRevokedSession(t *testing.T) {
	now := time.Now().UTC()
	ts := now
	reason := "admin"
	id := mustID(t)
	st := newFakeStore()
	st.sessions[id] = &model.Session{Status: model.SessionStatusRevoked, RevokedAt: &ts, RevokeReason: &reason}
	m := newManager(t, st, &fakeFlows{}, &fakeEvents{}, now)

	s, err := m.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Status != model.SessionStatusRevoked {
		t.Fatal("Get must return revoked sessions (ownership checks need them)")
	}
	if _, err := m.Get(context.Background(), uuid.Nil); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("nil id: %v", err)
	}
}

func TestListByUserValidatesUserID(t *testing.T) {
	m := newManager(t, newFakeStore(), &fakeFlows{}, &fakeEvents{}, time.Now())
	if _, err := m.ListByUser(context.Background(), uuid.Nil, 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("want ErrInvalidInput, got %v", err)
	}
}

// expireStore 在 fakeStore 上补 ExpireStale（可选能力）。
type expireStore struct {
	*fakeStore
	calls int
}

func (e *expireStore) ExpireStale(_ context.Context, _ time.Time) (int64, error) {
	e.calls++
	return 3, nil
}

func TestExpireStaleDelegatesWhenSupported(t *testing.T) {
	es := &expireStore{fakeStore: newFakeStore()}
	m := newManager(t, es, &fakeFlows{}, &fakeEvents{}, time.Now())
	n, err := m.ExpireStale(context.Background())
	if err != nil || n != 3 || es.calls != 1 {
		t.Fatalf("ExpireStale = %d, %v, calls=%d", n, err, es.calls)
	}
}

func TestExpireStaleUnsupportedStore(t *testing.T) {
	m := newManager(t, newFakeStore(), &fakeFlows{}, &fakeEvents{}, time.Now())
	if _, err := m.ExpireStale(context.Background()); !errors.Is(err, ErrNilDependency) {
		t.Fatalf("want ErrNilDependency, got %v", err)
	}
}

func TestCodeOfMapping(t *testing.T) {
	cases := []struct {
		err  error
		want httpx.Code
	}{
		{nil, ""},
		{ErrSessionNotFound, httpx.CodeSessionNotFound},
		{store.ErrSessionNotFound, httpx.CodeSessionNotFound},
		{ErrSessionRevoked, httpx.CodeSessionRevoked},
		{ErrSessionExpired, httpx.CodeTokenExpired},
		{ErrSessionNotOwned, httpx.CodeSessionNotOwned},
		{ErrInvalidInput, httpx.CodeInvalidParam},
		{store.ErrInvalidCursor, httpx.CodeInvalidParam},
		{store.ErrClientNotFound, httpx.CodeClientNotFound},
		{store.ErrClientDisabled, httpx.CodeInvalidClient},
		{ErrConcurrentLimit, ""}, // 对外码未冻结，故意不映射
		{errors.New("boom"), ""},
	}
	for i, tc := range cases {
		if got := CodeOf(tc.err); got != tc.want {
			t.Fatalf("case %d: CodeOf = %q, want %q", i, got, tc.want)
		}
	}
	// 包装后仍能识别（错误链）。
	if got := CodeOf(wrapped(ErrSessionRevoked)); got != httpx.CodeSessionRevoked {
		t.Fatalf("wrapped: %q", got)
	}
}

func wrapped(err error) error { return errors.Join(errors.New("ctx"), err) }
