package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// 本文件是 S8 的 ⑦ 单点登出通知（SLO）的**失败隔离**证据：AC-DP-12 的可判半边。
//
// R12（wire 契约：目标地址/报文/事件名）尚未裁定，故本切片只证"通知失败不阻塞本地撤销"
// 这条不变式；"送达并被验签"半边记 **PENDING·R12**（D16④），不得写成 PASS。

type stubNotifier struct {
	mu    sync.Mutex
	calls int
	err   error
	panic bool
	delay time.Duration
}

func (s *stubNotifier) Notify(ctx context.Context, _ RevocationNotice) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.panic {
		panic("notifier exploded")
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.err
}

func (s *stubNotifier) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// AC-DP-12（可判半边）：通知失败**不阻塞、不影响**本地撤销；失败有痕（计数）。
func TestAC_DP_12_NotifyFailureDoesNotBlockRevocation(t *testing.T) {
	cases := []struct {
		name     string
		notifier *stubNotifier
	}{
		{"notifier 返回错误", &stubNotifier{err: errors.New("业务系统 500")}},
		{"notifier panic", &stubNotifier{panic: true}},
		{"notifier 超时", &stubNotifier{delay: 5 * time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewSloBroadcaster(tc.notifier, 50*time.Millisecond, quietLogger())
			notice := RevocationNotice{
				UserID: testUserID, SessionID: testSID, ClientIDs: []string{"billing"}, Reason: "logout",
				At: time.Now().UTC(),
			}
			start := time.Now()
			b.NotifyRevoked(context.Background(), notice)
			elapsed := time.Since(start)

			// 撤销侧不等待通知：单条上限 50ms，这里留 10 倍余量防 CI 抖动。
			if elapsed > 500*time.Millisecond {
				t.Fatalf("通知阻塞了撤销 %s：AC-DP-12 要求失败不阻塞", elapsed)
			}
			if b.Failures() != 1 {
				t.Fatalf("失败计数期望 1，实际 %d（失败必须有痕，不能静默吞）", b.Failures())
			}
			if b.Sent() != 0 {
				t.Fatalf("Sent 期望 0，实际 %d", b.Sent())
			}
			if tc.notifier.count() != 1 {
				t.Fatalf("notifier 调用次数期望 1，实际 %d", tc.notifier.count())
			}
		})
	}
}

// 一个业务系统挂掉不该连累其他系统收到通知（逐 client 投递、互不影响）。
func TestNotifyIsolatesPerClient(t *testing.T) {
	// 第一个 client 必失败：用一个"第一次失败"的 notifier。
	notifier := &firstFailNotifier{}
	b := NewSloBroadcaster(notifier, time.Second, quietLogger())
	b.NotifyRevoked(context.Background(), RevocationNotice{
		UserID: testUserID, SessionID: testSID,
		ClientIDs: []string{"billing", "crm", "hr"}, Reason: "admin_revoke", At: time.Now().UTC(),
	})
	if notifier.count() != 3 {
		t.Fatalf("三个 client 应各投递一次，实际 %d 次", notifier.count())
	}
	if b.Failures() != 1 || b.Sent() != 2 {
		t.Fatalf("期望 failures=1 sent=2，实际 failures=%d sent=%d", b.Failures(), b.Sent())
	}
}

// 空集合/空 client_id 不投递（`all_clients` 但一条访问记录都没有时的常态）。
func TestNotifySkipsEmptyClients(t *testing.T) {
	notifier := &stubNotifier{}
	b := NewSloBroadcaster(notifier, time.Second, quietLogger())
	b.NotifyRevoked(context.Background(), RevocationNotice{UserID: testUserID, ClientIDs: []string{"", ""}})
	if notifier.count() != 0 {
		t.Fatalf("空 client 不该投递，实际调用 %d 次", notifier.count())
	}
	if b.Failures() != 0 || b.Sent() != 0 {
		t.Fatalf("期望 failures=0 sent=0，实际 %d/%d", b.Failures(), b.Sent())
	}
}

// 默认装配值 `NoNotifier` = **不发送**（R12 未裁前不签任何假报文）。
func TestNoNotifierSendsNothing(t *testing.T) {
	b := NewSloBroadcaster(nil, time.Second, quietLogger())
	b.NotifyRevoked(context.Background(), RevocationNotice{UserID: testUserID, ClientIDs: []string{"billing"}})
	if b.Failures() != 0 || b.Sent() != 1 {
		t.Fatalf("NoNotifier 应视为投递成功（无失败），实际 failures=%d sent=%d", b.Failures(), b.Sent())
	}
}

// R12 未裁的可执行痕迹：具体实现要如实返回哨兵，而不是自造报文。
func TestSloWireContractPendingSentinelExists(t *testing.T) {
	if !errors.Is(ErrSloWireContractPending, ErrSloWireContractPending) {
		t.Fatal("哨兵不可用")
	}
	if ErrSloWireContractPending.Error() == "" {
		t.Fatal("哨兵必须有文案（便于在日志里被认出）")
	}
}

// firstFailNotifier 第一次调用失败，其余成功（用于验证逐 client 隔离）。
type firstFailNotifier struct {
	mu    sync.Mutex
	calls int
}

func (f *firstFailNotifier) Notify(context.Context, RevocationNotice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return errors.New("first client is down")
	}
	return nil
}

func (f *firstFailNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
