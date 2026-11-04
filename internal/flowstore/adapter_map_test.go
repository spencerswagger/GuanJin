package flowstore

import (
	"errors"
	"testing"

	"github.com/spencerswagger/guanjin/internal/flow"
)

// `mapFlowError` 是"持久化错误 → 编排哨兵"的唯一转换点（无需数据库，纯函数）。
//
// 为什么这条必须有：`CodeOf` 只认 `internal/flow` 的哨兵；不翻的话
// `FLOW_EXPIRED` 会落到 handler 的 default 分支变成 500，AC-FLOW-10① 就永远是错的码。
func TestMapFlowError(t *testing.T) {
	if err := mapFlowError(nil); err != nil {
		t.Fatalf("nil 应保持 nil，实际 %v", err)
	}
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"过期 → FLOW_EXPIRED", ErrFlowExpired, flow.ErrFlowExpired},
		{"非活跃 → FLOW_NOT_FOUND", ErrFlowNotActive, flow.ErrFlowNotFound},
		{"已消费 → FLOW_NOT_FOUND", ErrFlowConsumed, flow.ErrFlowNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := mapFlowError(tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("应映射 %v，实际 %v", tc.want, err)
			}
		})
	}

	other := errors.New("db down")
	if err := mapFlowError(other); !errors.Is(err, other) {
		t.Fatalf("未知错误必须原样上抛，实际 %v", err)
	}
}
