package session

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件钉住 S10 的挂点语义（`notice.go`）：
//   - 撤销**提交成功后**每个被撤销的会话一条通知，字段与撤销事实一致；
//   - `Notifier` 为 nil 时**完全不变**：不通知、不打日志、不额外查库；
//   - 通知失败（含 panic）**不影响撤销**，也不把错误抛给调用方（`AC-DP-12`）；
//   - 通知拿到的 ctx **不带请求的取消信号**（HTTP 返回后仍要送达）。

// chanNotifier 把通知送到 channel，让异步投递可以被等待。
type chanNotifier struct {
	ch chan RevokeNotice
	// release 非 nil 时，投递先等它关闭（用来制造"请求已结束、投递还没开始"的时序）。
	release chan struct{}
	// ctxErrs 记录每次投递收到的 ctx.Err()。
	ctxErrs chan error
}

func newChanNotifier() *chanNotifier {
	return &chanNotifier{ch: make(chan RevokeNotice, 32), ctxErrs: make(chan error, 32)}
}

func (n *chanNotifier) NotifyRevoked(ctx context.Context, v RevokeNotice) {
	if n.release != nil {
		<-n.release
	}
	n.ctxErrs <- ctx.Err()
	n.ch <- v
}

// waitNotices 等 n 条通知（超时即失败，避免异步用例挂死）。
func waitNotices(t *testing.T, ch <-chan RevokeNotice, n int) []RevokeNotice {
	t.Helper()
	out := make([]RevokeNotice, 0, n)
	timeout := time.After(2 * time.Second)
	for len(out) < n {
		select {
		case v := <-ch:
			out = append(out, v)
		case <-timeout:
			t.Fatalf("want %d notices, got %d", n, len(out))
		}
	}
	return out
}

// assertNoNotice 断言一段时间内没有通知（异步投递没有"等待空"的原语，只能给一个观察窗）。
func assertNoNotice(t *testing.T, ch <-chan RevokeNotice) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected notice: %+v", v)
	case <-time.After(80 * time.Millisecond):
	}
}

// syncBuffer 是 slog 的安全输出目标（投递在别的 goroutine 上写）。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// seedSession 放一条 active 会话（`cascade` 会回读它来拼通知）。
func seedSession(st *fakeStore, user, id uuid.UUID, client, acr string) {
	st.sessions[id] = &model.Session{
		UserID: user, ClientID: client, ACR: acr,
		AMR:       model.StringArray{"pwd"},
		Status:    model.SessionStatusActive,
		ExpiresAt: time.Now().Add(time.Hour), IdleExpiresAt: time.Now().Add(time.Hour),
	}
	st.sessions[id].ID = id
}

func newNotifyingManager(t *testing.T, st SessionStore, flows FlowAbandoner, events EventWriter,
	now time.Time, n RevokeNotifier, logger *slog.Logger) *Manager {
	t.Helper()
	m, err := NewManager(st, flows, events, Options{Now: fixedNow(now), Notifier: n, Logger: logger})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// TestRevokeNoticeNilNotifierIsNoOp 钉住"默认行为完全不变"：不通知、不打日志、不额外查库。
func TestRevokeNoticeNilNotifierIsNoOp(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	user, id := mustID(t), mustID(t)
	st := newFakeStore()
	seedSession(st, user, id, "web", "AAL1")
	logger, buf := captureLogger()
	m := newNotifyingManager(t, st, &fakeFlows{}, &fakeEvents{}, now, nil, logger)

	if err := m.Revoke(context.Background(), RevokeInput{
		SessionID: id, Reason: ReasonAdmin, EventType: EventRevoke,
	}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// 撤销本身照常生效。
	if st.sessions[id].Status != model.SessionStatusRevoked {
		t.Fatalf("status = %q", st.sessions[id].Status)
	}
	// 没有通知 → 不该有第二类副作用，也不该出现任何日志。
	if got := buf.String(); got != "" {
		t.Fatalf("nil notifier must stay silent, got logs: %s", got)
	}
	// `NoticeRevoked` 在 nil 时也不许回库（否则等于给每个请求加一次查询）。
	before := st.getCalls
	m.NoticeRevoked(context.Background(), id, ReasonConcurrentLimit)
	if st.getCalls != before {
		t.Fatalf("nil notifier must not query store: %d -> %d", before, st.getCalls)
	}
}

// TestRevokeNoticeCarriesRevocationFacts 钉住字段：一个被撤销的会话一条，且 Subject 是撤销后的会话行。
func TestRevokeNoticeCarriesRevocationFacts(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	user, id := mustID(t), mustID(t)
	st := newFakeStore()
	seedSession(st, user, id, "crm", "AAL2")
	n := newChanNotifier()
	m := newNotifyingManager(t, st, &fakeFlows{}, &fakeEvents{}, now, n, discardLogger())

	if err := m.Revoke(context.Background(), RevokeInput{
		SessionID: id, Reason: ReasonAdmin, EventType: EventRevoke,
	}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got := waitNotices(t, n.ch, 1)[0]

	if got.UserID != user.String() || got.SessionID != id.String() {
		t.Fatalf("ids = %q/%q, want %q/%q", got.UserID, got.SessionID, user, id)
	}
	if got.Reason != string(ReasonAdmin) {
		t.Fatalf("reason = %q", got.Reason)
	}
	if !got.At.Equal(now) {
		t.Fatalf("at = %v, want %v", got.At, now)
	}
	sub, ok := got.Subject.(*model.Session)
	if !ok {
		t.Fatalf("Subject type = %T, want *model.Session", got.Subject)
	}
	// 通知必须在**提交之后**：Subject 上看到的应是已撤销的行（含 client/acr，够签断言）。
	if sub.Status != model.SessionStatusRevoked {
		t.Fatalf("subject status = %q", sub.Status)
	}
	if sub.ClientID != "crm" || sub.ACR != "AAL2" || len(sub.AMR) != 1 {
		t.Fatalf("subject facts = %+v", sub)
	}
	if err := <-n.ctxErrs; err != nil {
		t.Fatalf("notifier ctx err = %v", err)
	}
}

// TestRevokeNoticeCascadePerSession 钉住级联：每个被撤销的会话各一条（`RevokeAllByUser`）。
func TestRevokeNoticeCascadePerSession(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	user := mustID(t)
	a, b := mustID(t), mustID(t)
	st := newFakeStore()
	seedSession(st, user, a, "web", "AAL1")
	seedSession(st, user, b, "crm", "AAL2")
	n := newChanNotifier()
	m := newNotifyingManager(t, st, &fakeFlows{}, &fakeEvents{}, now, n, discardLogger())

	rows, err := m.RevokeAllByUser(context.Background(), user, ReasonLogoutAll, EventLogout, nil)
	if err != nil || rows != 2 {
		t.Fatalf("RevokeAllByUser = %d, %v", rows, err)
	}
	got := waitNotices(t, n.ch, 2)
	seen := map[string]bool{got[0].SessionID: true, got[1].SessionID: true}
	if !seen[a.String()] || !seen[b.String()] {
		t.Fatalf("notified sessions = %v", seen)
	}
	for _, one := range got {
		if one.Reason != string(ReasonLogoutAll) {
			t.Fatalf("reason = %q", one.Reason)
		}
	}
}

// TestRevokeOthersNoticeExcludesKeptSession 钉住"踢掉其它设备"不该通知被保留的那条。
func TestRevokeOthersNoticeExcludesKeptSession(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	user := mustID(t)
	keep, victim := mustID(t), mustID(t)
	st := newFakeStore()
	seedSession(st, user, keep, "web", "AAL1")
	seedSession(st, user, victim, "web", "AAL1")
	n := newChanNotifier()
	m := newNotifyingManager(t, st, &fakeFlows{}, &fakeEvents{}, now, n, discardLogger())

	if _, err := m.RevokeOthers(context.Background(), user, keep, ReasonPasswordChange, EventRevoke, nil); err != nil {
		t.Fatalf("RevokeOthers: %v", err)
	}
	got := waitNotices(t, n.ch, 1)
	if got[0].SessionID != victim.String() {
		t.Fatalf("notified %q, want victim %q", got[0].SessionID, victim)
	}
	assertNoNotice(t, n.ch)
}

// TestRevokeNoticeSkippedWhenNothingChanged 钉住幂等：早已撤销的会话不再产生通知（不重放审计/通知）。
func TestRevokeNoticeSkippedWhenNothingChanged(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	user, id := mustID(t), mustID(t)
	st := newFakeStore()
	seedSession(st, user, id, "web", "AAL1")
	st.sessions[id].Status = model.SessionStatusRevoked
	n := newChanNotifier()
	m := newNotifyingManager(t, st, &fakeFlows{}, &fakeEvents{}, now, n, discardLogger())

	if err := m.Revoke(context.Background(), RevokeInput{
		SessionID: id, Reason: ReasonLogout, EventType: EventLogout,
	}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	assertNoNotice(t, n.ch)
}

// TestRevokeNoticePanicDoesNotAffectRevoke 钉住"通知炸了也不许影响撤销"，并且留下一条可查的日志。
func TestRevokeNoticePanicDoesNotAffectRevoke(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	user, id := mustID(t), mustID(t)
	st := newFakeStore()
	seedSession(st, user, id, "web", "AAL1")
	logger, buf := captureLogger()
	called := make(chan struct{}, 1)
	m := newNotifyingManager(t, st, &fakeFlows{}, &fakeEvents{}, now, panicNotifier{called}, logger)

	if err := m.Revoke(context.Background(), RevokeInput{
		SessionID: id, Reason: ReasonRisk, EventType: EventRevoke,
	}); err != nil {
		t.Fatalf("revoke must not fail because of the notifier: %v", err)
	}
	if st.sessions[id].Status != model.SessionStatusRevoked {
		t.Fatalf("status = %q", st.sessions[id].Status)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("notifier never called")
	}
	// recover 之后必须有日志，否则"通知在崩"会长期无人知道。
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "panicked") {
		if time.Now().After(deadline) {
			t.Fatalf("want panic log, got: %s", buf.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// panicNotifier 模拟不守规矩的实现：先报到，再 panic。
type panicNotifier struct{ called chan struct{} }

func (p panicNotifier) NotifyRevoked(context.Context, RevokeNotice) {
	p.called <- struct{}{}
	panic("boom")
}

// TestRevokeNoticeContextDetached 钉住 ctx 语义：请求 ctx 已取消，投递拿到的 ctx 仍可用。
func TestRevokeNoticeContextDetached(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	user, id := mustID(t), mustID(t)
	st := newFakeStore()
	seedSession(st, user, id, "web", "AAL1")
	n := newChanNotifier()
	n.release = make(chan struct{})
	m := newNotifyingManager(t, st, &fakeFlows{}, &fakeEvents{}, now, n, discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Revoke(ctx, RevokeInput{
		SessionID: id, Reason: ReasonLogout, EventType: EventLogout,
	}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// 模拟"HTTP 响应已返回"：请求 ctx 取消后才放行投递。
	cancel()
	close(n.release)
	waitNotices(t, n.ch, 1)
	if err := <-n.ctxErrs; err != nil {
		t.Fatalf("notifier ctx must survive request cancellation, got %v", err)
	}
}

// TestNoticeRevokedForEvictedSession 钉住并发上限"挤掉最旧"的补发路径（`EvictOldestActive` 不走 cascade）。
func TestNoticeRevokedForEvictedSession(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	user, id := mustID(t), mustID(t)
	st := newFakeStore()
	seedSession(st, user, id, "web", "AAL1")
	// 仓储那条 CTE 已经把会话置为 revoked，这里复现同样的前置状态。
	st.sessions[id].Status = model.SessionStatusRevoked
	n := newChanNotifier()
	m := newNotifyingManager(t, st, &fakeFlows{}, &fakeEvents{}, now, n, discardLogger())

	m.NoticeRevoked(context.Background(), id, ReasonConcurrentLimit)
	got := waitNotices(t, n.ch, 1)[0]
	if got.SessionID != id.String() || got.Reason != string(ReasonConcurrentLimit) {
		t.Fatalf("notice = %+v", got)
	}
	if !got.At.Equal(now) {
		t.Fatalf("at = %v, want %v", got.At, now)
	}
}

// TestFireRevokeNoticeNilReceiverAndNilContext 钉住两个边界分支：nil 接收者不 panic、nil ctx 被兜住。
//
// 这两条都不是"正常路径"，但接缝一旦被装配层以奇怪方式调用（例如在 nil Manager 上补发），
// 崩掉的是撤销进程本身——所以写下来而不是靠"调用方不会这么干"。
func TestFireRevokeNoticeNilReceiverAndNilContext(t *testing.T) {
	var nilMgr *Manager
	nilMgr.fireRevokeNotice(context.Background(), RevokeNotice{}) // 不 panic 即通过

	st := newFakeStore()
	n := newChanNotifier()
	m := newNotifyingManager(t, st, &fakeFlows{}, &fakeEvents{},
		time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), n, discardLogger())
	m.fireRevokeNotice(nil, RevokeNotice{SessionID: "sid"})
	waitNotices(t, n.ch, 1)
	if err := <-n.ctxErrs; err != nil {
		t.Fatalf("nil ctx must be replaced by a usable one, got %v", err)
	}
}

// TestNoticeRevokedMissingSessionIsQuiet 钉住补发路径的失败半径：会话查不到只留日志，不 panic、不通知。
func TestNoticeRevokedMissingSessionIsQuiet(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	st := newFakeStore()
	n := newChanNotifier()
	logger, buf := captureLogger()
	m := newNotifyingManager(t, st, &fakeFlows{}, &fakeEvents{}, now, n, logger)

	m.NoticeRevoked(context.Background(), mustID(t), ReasonConcurrentLimit)
	assertNoNotice(t, n.ch)
	if !strings.Contains(buf.String(), "load evicted session") {
		t.Fatalf("want load-failure log, got: %s", buf.String())
	}
}
