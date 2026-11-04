package flowstore

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// Adapter 把本包（持久化）接到 `internal/flow` 的编排接口上。
//
// 为什么需要它：`internal/flow` 不能用 `flowstore.StartRequest` 之类做入参 —— 那会让
// flow import flowstore，而 flowstore 已经 import flow（成环）。所以在**持久化侧**
// 做一次薄转换：入参换类型、错误换哨兵，别的一律不加工。
//
// 这一层**不做业务判断**（不判档位、不算 amr）：判断在 flow，SQL 在本包，
// 转换只负责"两边说得上话"。
type Adapter struct {
	svc  *Service
	repo *store.FlowRepo
}

// NewAdapter 绑定一个 flowstore.Service。nil 直接报错（装配期 fail-fast）。
func NewAdapter(svc *Service, repo *store.FlowRepo) (*Adapter, error) {
	if svc == nil || repo == nil {
		return nil, ErrMissingDependency
	}
	return &Adapter{svc: svc, repo: repo}, nil
}

// Get 读一条流（不加锁，只用于算 Descriptor）。
//
// 错误统一成 `flow.ErrFlowNotFound`：编排层不该知道"仓储没查到这个行"和
// "流不存在"是两种不同的错误——对外都是 `FLOW_NOT_FOUND`。
func (a *Adapter) Get(ctx context.Context, flowID uuid.UUID) (*model.Flow, error) {
	f, err := a.repo.GetByID(ctx, flowID)
	if err != nil {
		if errors.Is(err, store.ErrFlowNotFound) {
			return nil, flow.ErrFlowNotFound
		}
		return nil, err
	}
	return f, nil
}

// Start 开流或复用活跃流（语义完全委托 Service.Start：先查后建）。
func (a *Adapter) Start(ctx context.Context, in flow.StartInput) (*model.Flow, error) {
	return a.svc.Start(ctx, StartRequest{
		UserID:    in.UserID,
		ClientID:  in.ClientID,
		TargetACR: in.TargetACR,
		TraceID:   in.TraceID,
	})
}

// Advance 推进一步（短事务：锁行 → 校验 → amr 并集 → 写回）。
//
// **字段语义**：`AdvanceRequest.Factor` 在这个包里落的是 **amr 取值**（`pwd`），
// 不是因子类型（`password`）——本包既有用例就是按 `Factor: "pwd"` 写的。
// 所以适配层传 `in.AMR`；`in.Factor` 只用于调用方取证，不进库。
// 这个重叠已在 PR 里记为待 Edge 收口的 P2（要拆成两列得连带改读侧，M2 前清理）。
func (a *Adapter) Advance(ctx context.Context, in flow.AdvanceInput) (*model.Flow, error) {
	if in.AMR == "" {
		return nil, ErrFactorRequired
	}
	f, err := a.svc.Advance(ctx, AdvanceRequest{
		FlowID:      in.FlowID,
		Factor:      in.AMR,
		ChallengeID: in.ChallengeID,
		At:          in.At,
	})
	if err != nil {
		return f, mapFlowError(err)
	}
	return f, nil
}

// mapFlowError 把**持久化层的错误**翻成编排层的哨兵。
//
// 为什么必须在这里翻：`flow` 不认识 `flowstore` 的错误（否则成环），而 `CodeOf` 只认
// 本包哨兵 —— 不翻的话 `FLOW_EXPIRED` 会落到 default 变成 500，AC-FLOW-10① 就永远是错的码。
//   - 过期 → `FLOW_EXPIRED`（E1/400，行仍在所以稳定）；
//   - 已完成/已放弃/已消费 → `FLOW_NOT_FOUND`（I-F4 的"流已不在"，AC-FLOW-10②）；
//   - 其余（含"行在锁与写回之间消失"）原样上抛，那是缺陷，不该有业务码。
func mapFlowError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrFlowExpired):
		return flow.ErrFlowExpired
	case errors.Is(err, ErrFlowNotActive), errors.Is(err, ErrFlowConsumed):
		return flow.ErrFlowNotFound
	default:
		return err
	}
}

// Consume 一次性消费（completed + consumed_at；I-F4）。
func (a *Adapter) Consume(ctx context.Context, in flow.ConsumeInput) (*model.Flow, error) {
	f, err := a.svc.Consume(ctx, ConsumeRequest{
		FlowID:    in.FlowID,
		UserID:    in.UserID,
		SessionID: in.SessionID,
	})
	if err != nil {
		return f, mapFlowError(err)
	}
	return f, nil
}

// 编译期断言：适配器必须同时满足编排层的四个接口（改名即构建失败）。
var (
	_ flow.Reader   = (*Adapter)(nil)
	_ flow.Starter  = (*Adapter)(nil)
	_ flow.Advancer = (*Adapter)(nil)
	_ flow.Consumer = (*Adapter)(nil)
)
