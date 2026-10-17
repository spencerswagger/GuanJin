package notify

import (
	"context"
	"sync"

	"github.com/spencerswagger/GuanJin/internal/factor/channel"
)

type Detail struct {
	Target       string `json:"target"`
	TargetMasked string `json:"target_masked"`
	Channel      string `json:"channel"`
	Message      string `json:"message"`
}

type Notifier interface {
	Notify(ctx context.Context, userID string, event string, detail Detail) error
}

func NoticeBody(event string) string {
	switch event {
	case "identifier_bind":
		return "您的账号新增了一种登录方式，如非本人操作请立即联系管理员。"
	case "identifier_unbind":
		return "您的账号解绑了一种登录方式，如非本人操作请立即联系管理员。"
	case "password_change":
		return "您的账号口令已变更，如非本人操作请立即联系管理员。"
	default:
		return "您的账号发生安全变更，如非本人操作请立即联系管理员。"
	}
}

type SenderNotifier struct {
	senders map[string]channel.Sender
}

func NewSenderNotifier(senders map[string]channel.Sender) *SenderNotifier {
	return &SenderNotifier{senders: senders}
}

func (n *SenderNotifier) Notify(ctx context.Context, _ string, event string, detail Detail) error {
	if n.senders == nil || detail.Target == "" {
		return nil
	}
	sender := n.senders[detail.Channel]
	if sender == nil {
		return nil
	}
	return sender.Send(ctx, detail.Target, NoticeBody(event))
}

type Record struct {
	UserID       string
	Event        string
	Target       string
	TargetMasked string
	Channel      string
	Message      string
}

type RecordingNotifier struct {
	mu      sync.Mutex
	records []Record
}

func NewRecordingNotifier() *RecordingNotifier {
	return &RecordingNotifier{}
}

func (n *RecordingNotifier) Notify(_ context.Context, userID string, event string, detail Detail) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.records = append(n.records, Record{
		UserID:       userID,
		Event:        event,
		Target:       detail.Target,
		TargetMasked: detail.TargetMasked,
		Channel:      detail.Channel,
		Message:      NoticeBody(event),
	})
	return nil
}

func (n *RecordingNotifier) Records() []Record {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Record{}, n.records...)
}

func (n *RecordingNotifier) Count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.records)
}

type NoopNotifier struct{}

func (NoopNotifier) Notify(context.Context, string, string, Detail) error { return nil }
