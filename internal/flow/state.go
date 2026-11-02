// Package flow 是阶梯认证编排（Flow Descriptor + 状态机）的落点。
//
// 口径（decisions.md）：
//   - D5：中间态唯一权威落点是第 15 张表 flows（13 列 + 不变式 I-F1~I-F8）；
//     本包只放**纯逻辑**（不碰数据库），持久化由 Edge 的 internal/store/repo_flows 提供；
//   - D2：purpose 六值词表；D7：Descriptor 字段；D3-F：STEP_ALREADY_SATISFIED 只在本包返回。
package flow

import (
	"errors"
	"fmt"
)

// Status 是 flows.status 的封闭取值（D5）。
type Status string

const (
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
	StatusExpired   Status = "expired"
	StatusAbandoned Status = "abandoned"
)

// ErrAMRRegression 表示「已达成因子集合变短」——违反 I-F2（amr 单调不减）。
var ErrAMRRegression = errors.New("flow: achieved_amr must not shrink (I-F2)")

// ErrStepAlreadySatisfied 对应 D3-F 分配的 STEP_ALREADY_SATISFIED（E4 / 409）。
// **只允许 internal/flow 返回**：CI 有禁码扫描把其余包的出现判红。
var ErrStepAlreadySatisfied = errors.New("flow: step already satisfied")

// AddAMR 把新达成的因子并入已达成集合。
//
// 不变式（I-F2）：结果必须是 current 的**超集**，顺序保持「先到先得」；重复因子去重。
// 任何会让集合变短的输入都返回 ErrAMRRegression —— 只增不减是安全属性，
// 不允许实现侧"重算一遍"来绕过。
func AddAMR(current []string, add ...string) ([]string, error) {
	out := make([]string, 0, len(current)+len(add))
	seen := make(map[string]struct{}, len(current)+len(add))
	for _, f := range current {
		if f == "" {
			return nil, fmt.Errorf("flow: empty factor in achieved_amr")
		}
		if _, dup := seen[f]; dup {
			continue // 幂等：重复项直接丢弃，不算回退
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	for _, f := range add {
		if f == "" {
			return nil, fmt.Errorf("flow: empty factor to add")
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	if len(out) < len(current) {
		return nil, ErrAMRRegression
	}
	return out, nil
}

// CanTransition 判断状态机是否允许该迁移（D5：无软删除、过期只置 expired 不删行）。
func CanTransition(from, to Status) bool {
	switch from {
	case StatusActive:
		// active 只能往前走：完成 / 过期 / 放弃
		return to == StatusCompleted || to == StatusExpired || to == StatusAbandoned
	case StatusCompleted, StatusExpired, StatusAbandoned:
		// 终态不可回退（I-F4 一次性；过期行保留 7 天后由清理器删除，见 I-F6）
		return false
	default:
		return false
	}
}

// CompletedSteps 是 flows.completed_steps 的元素形态（D5：只追加、不改写）。
type CompletedStep struct {
	Factor      string `json:"factor"`
	ChallengeID string `json:"challenge_id,omitempty"`
	At          string `json:"at"`
}

// HasCompletedFactor 判断某因子是否已完成（返回 ErrStepAlreadySatisfied 的依据）。
func HasCompletedFactor(steps []CompletedStep, factor string) bool {
	for _, s := range steps {
		if s.Factor == factor {
			return true
		}
	}
	return false
}

// AppendStep 追加一步；**已完成的因子不得重复追加**（D3-F：重输 → 409 STEP_ALREADY_SATISFIED）。
func AppendStep(steps []CompletedStep, step CompletedStep) ([]CompletedStep, error) {
	if HasCompletedFactor(steps, step.Factor) {
		return steps, ErrStepAlreadySatisfied
	}
	out := make([]CompletedStep, len(steps), len(steps)+1)
	copy(out, steps)
	return append(out, step), nil
}
