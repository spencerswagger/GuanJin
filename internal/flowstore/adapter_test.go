package flowstore

import (
	"context"
	"errors"
	"testing"

	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 适配层的**入参守卫**单测（不需要数据库）。
//
// 为什么值得单独写：适配层是"编排 ↔ 持久化"的唯一缝，它的守卫（缺依赖、缺 amr）
// 一旦失效，错误会在很深的地方才暴露（比如 amr 写成空串 → `flows.achieved_amr` 里多个空元素）。
// 这几条在集成 profile 里也几乎不会自然命中，所以用单测钉住。
func TestNewAdapterRequiresDeps(t *testing.T) {
	if _, err := NewAdapter(nil, nil); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("nil 依赖应报 ErrMissingDependency，实际 %v", err)
	}
	repo := store.NewFlowRepo(nil)
	if _, err := NewAdapter(nil, repo); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("缺 Service 应报 ErrMissingDependency，实际 %v", err)
	}
	if _, err := NewAdapter(&Service{}, nil); !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("缺 repo 应报 ErrMissingDependency，实际 %v", err)
	}
	if _, err := NewAdapter(&Service{}, repo); err != nil {
		t.Fatalf("依赖齐全应通过：%v", err)
	}
}

// AMR 缺失必须**在碰数据库之前**拒（`achieved_amr` 里绝不能出现空串）。
func TestAdapterAdvanceRequiresAMR(t *testing.T) {
	a, err := NewAdapter(&Service{}, store.NewFlowRepo(nil))
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	if _, err := a.Advance(context.Background(), flow.AdvanceInput{AMR: ""}); !errors.Is(err, ErrFactorRequired) {
		t.Fatalf("缺 AMR 应报 ErrFactorRequired，实际 %v", err)
	}
}
