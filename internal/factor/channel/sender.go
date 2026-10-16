package channel

import (
	"context"
	"sync"
	"time"
)

type SentCode struct {
	Target string
	Code   string
	At     time.Time
}

type RecordingSender struct {
	mu   sync.Mutex
	sent []SentCode
}

func NewRecordingSender() *RecordingSender { return &RecordingSender{} }

func (s *RecordingSender) Send(_ context.Context, target, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, SentCode{Target: target, Code: code, At: time.Now().UTC()})
	return nil
}

func (s *RecordingSender) Last() (SentCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		return SentCode{}, false
	}
	return s.sent[len(s.sent)-1], true
}

func (s *RecordingSender) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func (s *RecordingSender) All() []SentCode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SentCode{}, s.sent...)
}
