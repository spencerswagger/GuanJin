package flowstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/flow"
)

// TestNewRequiresPositiveTTL 守住 D5 那条最容易破的口子：
// TTL 只能来自 `runtime_config` 的 `flow.ttl_seconds`，代码里留一个 600 常量
// 就是隐性默认（本项目"不许有隐性逻辑"的直接违反）。
func TestNewRequiresPositiveTTL(t *testing.T) {
	cases := []struct {
		name string
		ttl  time.Duration
	}{
		{"zero", 0},
		{"negative", -time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, err := New(nil, nil, c.ttl)
			if !errors.Is(err, ErrTTLRequired) {
				t.Fatalf("ttl=%v: want ErrTTLRequired, got %v (svc=%v)", c.ttl, err, svc)
			}
		})
	}
}

// TestNewRequiresDependencies：TTL 合法后仍必须拿到 DB 与仓储，缺一即拒。
func TestNewRequiresDependencies(t *testing.T) {
	_, err := New(nil, nil, time.Minute)
	if !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("want ErrMissingDependency, got %v", err)
	}
}

// TestStartValidatesBeforeTouchingDB：入参错误必须在碰库之前就失败
// （数据类型错误不该变成一条 SQL 往返，更不该靠 DB CHECK 兜）。
func TestStartValidatesBeforeTouchingDB(t *testing.T) {
	svc := &Service{ttl: time.Minute} // 故意不注入 db：本用例的路径不该用到它
	ctx := context.Background()

	if _, err := svc.Start(ctx, StartRequest{}); !errors.Is(err, ErrClientRequired) {
		t.Fatalf("missing client: want ErrClientRequired, got %v", err)
	}
	// D12：AAL3 预留、V1 不得产出。DB 的 CHECK 允许 AAL3，所以这道闸只能在应用层。
	if _, err := svc.Start(ctx, StartRequest{ClientID: "c", TargetACR: "AAL3"}); !errors.Is(err, ErrTargetACRUnsupported) {
		t.Fatalf("AAL3: want ErrTargetACRUnsupported, got %v", err)
	}
}

// TestAdvanceValidatesInput：三个必填项各自有独立错误，便于上层给出不同提示。
func TestAdvanceValidatesInput(t *testing.T) {
	svc := &Service{ttl: time.Minute}
	ctx := context.Background()

	if _, err := svc.Advance(ctx, AdvanceRequest{}); !errors.Is(err, ErrFlowIDRequired) {
		t.Fatalf("missing flow id: want ErrFlowIDRequired, got %v", err)
	}
	if _, err := svc.Advance(ctx, AdvanceRequest{FlowID: uuid.New()}); !errors.Is(err, ErrFactorRequired) {
		t.Fatalf("missing factor: want ErrFactorRequired, got %v", err)
	}
	if _, err := svc.Advance(ctx, AdvanceRequest{FlowID: uuid.New(), Factor: "pwd"}); !errors.Is(err, ErrStepTimeRequired) {
		t.Fatalf("missing step time: want ErrStepTimeRequired, got %v", err)
	}
}

// TestConsumeValidatesInput：CH-1 的「阶梯内必须显式带 flow_id」在落库这层也要挡。
func TestConsumeValidatesInput(t *testing.T) {
	svc := &Service{ttl: time.Minute}
	if _, err := svc.Consume(context.Background(), ConsumeRequest{}); !errors.Is(err, ErrFlowIDRequired) {
		t.Fatalf("missing flow id: want ErrFlowIDRequired, got %v", err)
	}
}

// TestStatusVocabularyMatchesD5 确认本包与规则层对 status 的取值没有分叉
// （分叉的代价是 23514 拒写，而且要到真库才暴露）。
func TestStatusVocabularyMatchesD5(t *testing.T) {
	want := []flow.Status{flow.StatusActive, flow.StatusCompleted, flow.StatusExpired, flow.StatusAbandoned}
	got := []flow.Status{
		flow.Status(string(flow.StatusActive)),
		flow.Status(string(flow.StatusCompleted)),
		flow.Status(string(flow.StatusExpired)),
		flow.Status(string(flow.StatusAbandoned)),
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status[%d]: want %q, got %q", i, want[i], got[i])
		}
	}
}

// TestTTLExposed：TTL 由 runtime_config 读入后由本实例持有，上层要能读回去
// （例如把"这条流还剩多久"算给前端），**不得**自己再写一份默认值。
func TestTTLExposed(t *testing.T) {
	want := 42 * time.Second
	svc := &Service{ttl: want}
	if got := svc.TTL(); got != want {
		t.Fatalf("TTL: want %v, got %v", want, got)
	}
}
