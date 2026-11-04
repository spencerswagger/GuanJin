package flow

import (
	"fmt"
	"sort"
	"time"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是 **Flow Descriptor 的计算**（D7 契约 + 索引 §2「档位即准入」）。
//
// 三条铁律（都是评审会问的）：
//  1. **`current_acr` 每次由 `achieved_amr` 现算**，绝不缓存成列 —— 缓存就会出现
//     "补步后 amr 变了、级别没变"这类不一致（D5 的 13 列里也确实没有该列）。
//  2. **禁用因子既不显示也不计入 `current_acr`**（AC-FLOW-04）：一个被业务系统禁掉的
//     因子若还贡献级别，等于"禁了等于没禁"。
//  3. **不可达必须是显式终态**（D7 / AC-FLOW-07）：不许让前端靠 `satisfied:false` 自己猜，
//     否则渲染层只能死循环重试。

// FactorInfo 是"编排层需要知道的因子事实"，由 SPI 的 `Descriptor` 折算而来。
//
// 为什么不直接持有 `factor.Provider`：Descriptor 的计算要能被单测**离线**跑（不构造 provider、
// 不碰数据库），而且编排只用到这几个字段——持有接口会让每次算 Descriptor 都做一遍类型断言。
type FactorInfo struct {
	Type factor.FactorType
	AMR  string
	// AsFirst / AsSecond 能否充当第一/第二因子（来自 `AuthenticatableSpec`）。
	AsFirst  bool
	AsSecond bool
	// Sole / Combined 单独使用 / 组合后可达的级别（来自 `ACRSpec`）。
	Sole     factor.ACR
	Combined factor.ACR
}

// FactorInfoOf 把因子 SPI 的能力声明折成编排层要用的信息。
//
// 只认**能力断言**（`factor.Capabilities`），不认类型名：新增因子只要实现
// `Authenticatable` + `ACRContributing` 就自动进编排，不需要改这个文件。
func FactorInfoOf(d factor.Descriptor) (FactorInfo, bool) {
	authable := false
	for _, c := range d.Capabilities {
		if c == factor.CapAuthenticatable {
			authable = true
			break
		}
	}
	if !authable || d.Authenticatable == nil || d.ACR == nil {
		// 不能认证、或没说清贡献级别的因子，不参与阶梯编排（如 recovery_code）。
		return FactorInfo{}, false
	}
	amr, ok := factor.AMRFor(d.Type)
	if !ok {
		// AMR 表未登记 = 新增因子没登记 amr：直接判"不参与"，而不是拿类型名兜底
		// （兜底会让 `achieved_amr` 里混进类型名，与附录 B 的词表打架）。
		return FactorInfo{}, false
	}
	return FactorInfo{
		Type:     d.Type,
		AMR:      amr,
		AsFirst:  d.Authenticatable.AsFirst,
		AsSecond: d.Authenticatable.AsSecond,
		Sole:     d.ACR.Sole,
		Combined: d.ACR.Combined,
	}, true
}

// FactorInfosOf 批量折算（保持入参顺序，调用方一般是注册表的稳定顺序）。
func FactorInfosOf(ds []factor.Descriptor) []FactorInfo {
	out := make([]FactorInfo, 0, len(ds))
	for _, d := range ds {
		if fi, ok := FactorInfoOf(d); ok {
			out = append(out, fi)
		}
	}
	return out
}

// Step 是 Descriptor 里的一步（D7：含 challenge_id / purpose / contributes_acr）。
type Step struct {
	Factor         factor.FactorType `json:"factor"`
	Role           factor.Role       `json:"role"`
	Status         string            `json:"status"` // pending | done
	Purpose        factor.Purpose    `json:"purpose"`
	ContributesACR factor.ACR        `json:"contributes_acr,omitempty"`
	// ChallengeID 该步已预签挑战时非空（M1 的口令是无状态挑战，这里恒为空）。
	ChallengeID *string `json:"challenge_id,omitempty"`
}

// Unreachable 是"这套配置永远达不到目标"的显式原因（D7）。
type Unreachable struct {
	Code           string              `json:"code"` // FACTORS_DISABLED | POLICY_UNSATISFIABLE
	MissingFactors []factor.FactorType `json:"missing_factors"`
}

// Descriptor 是 `GET /authorize/flow` 与 `/authorize/submit` 的 `details.flow` 共用结构（D7）。
type Descriptor struct {
	FlowID     string     `json:"flow_id"`
	Client     Client     `json:"client"`
	TargetACR  factor.ACR `json:"target_acr"`
	CurrentACR factor.ACR `json:"current_acr"`
	Satisfied  bool       `json:"satisfied"`
	Steps      []Step     `json:"steps"`
	// AvailableFactors 可用于推进本流的因子（已剔除 client 禁用集）。
	AvailableFactors []factor.FactorType `json:"available_factors"`
	DisabledFactors  []factor.FactorType `json:"disabled_factors"`
	// TerminalState satisfied | need_more | unreachable（D7，前端据此终止渲染）。
	TerminalState     string       `json:"terminal_state"`
	UnreachableReason *Unreachable `json:"unreachable_reason,omitempty"`
	ExpiresAt         time.Time    `json:"expires_at"`
}

// Client 是 Descriptor 里的业务系统摘要。
type Client struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Tier string `json:"tier,omitempty"`
}

// State 是算 Descriptor 所需的**全部输入**（都来自一次事务/一次读，避免边算边查）。
type State struct {
	FlowID string
	Client Client
	// TargetACR 由档位映射而来（L1|L2 → AAL1、L3 → AAL2；D12 不产出 AAL3）。
	TargetACR factor.ACR
	// AchievedAMR 已达成因子集合（I-F2 单调）。
	AchievedAMR []string
	// DisabledFactors 该业务系统禁用的因子（`login_policies.disabled_factors`）。
	DisabledFactors []factor.FactorType
	// Factors 该平台已装配的因子（`factor.Registry.Descriptors()` 折算而来）。
	Factors []FactorInfo
	// RemainingTTL 剩余有效期；<=0 表示已过期（过期由 flowstore 判定，这里只用于展示）。
	ExpiresAt time.Time
	Now       time.Time
}

// BuildDescriptor 计算 Descriptor（纯函数：同样的输入永远同样的输出）。
func BuildDescriptor(st State) (Descriptor, error) {
	if !st.TargetACR.Valid() {
		return Descriptor{}, fmt.Errorf("flow: invalid target_acr %q", string(st.TargetACR))
	}
	if !st.TargetACR.Issuable() {
		// D12：AAL3 预留、V1 任何路径不得产出。目标级别出现 AAL3 就是上游算错了。
		return Descriptor{}, fmt.Errorf("flow: target_acr %q is not issuable (D12)", string(st.TargetACR))
	}

	disabled := make(map[factor.FactorType]bool, len(st.DisabledFactors))
	for _, f := range st.DisabledFactors {
		disabled[f] = true
	}
	achieved := make(map[string]bool, len(st.AchievedAMR))
	for _, a := range st.AchievedAMR {
		achieved[a] = true
	}

	// ① 可用因子 = 平台已装配 − client 禁用集（**禁用因子不进可用集，也不进 current_acr**）。
	available := make([]FactorInfo, 0, len(st.Factors))
	disabledTypes := make([]factor.FactorType, 0, len(st.DisabledFactors))
	for _, fi := range st.Factors {
		if disabled[fi.Type] {
			disabledTypes = append(disabledTypes, fi.Type)
			continue
		}
		available = append(available, fi)
	}
	sort.Slice(disabledTypes, func(i, j int) bool { return disabledTypes[i] < disabledTypes[j] })

	// ② current_acr 由**可用且已达成**的因子现算。
	current := currentACR(available, achieved)

	d := Descriptor{
		FlowID:           st.FlowID,
		Client:           st.Client,
		TargetACR:        st.TargetACR,
		CurrentACR:       current,
		Satisfied:        current.AtLeast(st.TargetACR),
		DisabledFactors:  disabledTypes,
		AvailableFactors: typesOf(available),
		ExpiresAt:        st.ExpiresAt,
	}

	// ③ 步骤：已完成的在前（按 amr 顺序），待补的在后（先第一因子、再第二因子）。
	d.Steps = buildSteps(available, achieved)

	// ④ 终态。
	switch {
	case d.Satisfied:
		d.TerminalState = TerminalSatisfied
	case !canReach(available, st.TargetACR):
		d.TerminalState = TerminalUnreachable
		d.UnreachableReason = &Unreachable{
			Code:           ReasonFactorsDisabled,
			MissingFactors: missingFactorsFor(st.TargetACR, available, st.Factors, disabled),
		}
	default:
		d.TerminalState = TerminalNeedMore
	}
	return d, nil
}

// 终态取值（D7 冻结）。
const (
	TerminalSatisfied   = "satisfied"
	TerminalNeedMore    = "need_more"
	TerminalUnreachable = "unreachable"
)

// Unreachable 的原因取值（D7 冻结）。
const (
	ReasonFactorsDisabled     = "FACTORS_DISABLED"
	ReasonPolicyUnsatisfiable = "POLICY_UNSATISFIABLE"
)

// currentACR 用**已达成且未被禁用**的因子算当前级别。
//
// 规则（显式写出来，避免"看起来对"）：
//   - 只有一个因子达成 → 取该因子的 `Sole`；
//   - 两个及以上 → 取其中最大的 `Combined`（因子模型 §3 第 4 项：组合可达级别由因子自己声明）；
//   - 没有任何达成因子 → 空（对应前端展示的 `AAL0`，**不落库**，D12）。
func currentACR(available []FactorInfo, achieved map[string]bool) factor.ACR {
	var (
		n         int
		bestSole  factor.ACR
		bestCombi factor.ACR
	)
	for _, fi := range available {
		if !achieved[fi.AMR] {
			continue
		}
		n++
		// 首次赋值必须直接取（`AtLeast` 对空值返回 false，否则永远更新不上）。
		if bestSole == "" || fi.Sole.AtLeast(bestSole) {
			bestSole = fi.Sole
		}
		if fi.Combined != "" && (bestCombi == "" || fi.Combined.AtLeast(bestCombi)) {
			bestCombi = fi.Combined
		}
	}
	switch {
	case n == 0:
		return ""
	case n == 1:
		return bestSole
	default:
		if bestCombi.AtLeast(bestSole) {
			return bestCombi
		}
		return bestSole
	}
}

// canReach 判断"在可用因子范围内"是否还有可能达到目标级别。
//
// 恒等式：单个可用因子能独当一面（Sole ≥ target），**或**有两个以上可用因子能组合（Combined ≥ target）。
// 这条判断是 AC-FLOW-07 的核心：不可达必须能提前算出来，不能等用户把能做的都做完才卡住。
func canReach(available []FactorInfo, target factor.ACR) bool {
	for _, fi := range available {
		if fi.Sole.AtLeast(target) {
			return true
		}
	}
	if len(available) >= 2 {
		for _, fi := range available {
			if fi.Combined != "" && fi.Combined.AtLeast(target) {
				return true
			}
		}
	}
	return false
}

// buildSteps 生成步骤清单：已 done 的按 amr 顺序在前，待补的按 first → second 在后。
func buildSteps(available []FactorInfo, achieved map[string]bool) []Step {
	steps := make([]Step, 0, len(available))
	for _, fi := range available {
		if !achieved[fi.AMR] {
			continue
		}
		steps = append(steps, Step{
			Factor:         fi.Type,
			Role:           roleOf(fi),
			Status:         StepDone,
			Purpose:        factor.PurposeLogin,
			ContributesACR: fi.Sole,
		})
	}
	for _, fi := range available {
		if achieved[fi.AMR] {
			continue
		}
		steps = append(steps, Step{
			Factor:         fi.Type,
			Role:           roleOf(fi),
			Status:         StepPending,
			Purpose:        factor.PurposeLogin,
			ContributesACR: fi.Sole,
		})
	}
	return steps
}

// 步骤状态（接口 §3.1.4 冻结两值）。
const (
	StepPending = "pending"
	StepDone    = "done"
)

// roleOf 给一步定角色：能当第一因子就是 first，否则 second（两样都能时按 first，先满足低门槛）。
func roleOf(fi FactorInfo) factor.Role {
	if fi.AsFirst {
		return factor.RoleFirst
	}
	return factor.RoleSecond
}

func typesOf(infos []FactorInfo) []factor.FactorType {
	out := make([]factor.FactorType, 0, len(infos))
	for _, fi := range infos {
		out = append(out, fi.Type)
	}
	return out
}

// missingFactorsFor 给出"为什么不可达"的缺口清单：
// 被禁用 + 若能解除禁用就够用的因子，加上"平台根本没收这个能力"的提示。
//
// 口径：优先列**被禁用的因子**（运维一改就能救）；没有再列平台已装配但级别不够的（要改策略）。
func missingFactorsFor(target factor.ACR, available, all []FactorInfo, disabled map[factor.FactorType]bool) []factor.FactorType {
	var out []factor.FactorType
	for _, fi := range all {
		if !disabled[fi.Type] {
			continue
		}
		if fi.Sole.AtLeast(target) || (fi.Combined != "" && fi.Combined.AtLeast(target)) {
			out = append(out, fi.Type)
		}
	}
	if len(out) > 0 {
		return out
	}
	// 没有"恢复禁用就够"的因子：说明可用因子里级别根本不够，把可用但级别不足的列出来。
	for _, fi := range available {
		out = append(out, fi.Type)
	}
	return out
}

// DescriptorOfFlow 是 `store/model.Flow` → `State` 的折算（编排层的唯一入口）。
//
// 放这里而不是放 flowstore：Descriptor 是**编排语义**，不该由持久化层决定怎么算。
func DescriptorOfFlow(f *model.Flow, client Client, disabled []factor.FactorType, factors []FactorInfo, now time.Time) (Descriptor, error) {
	if f == nil {
		return Descriptor{}, fmt.Errorf("flow: nil flow")
	}
	target := factor.ACR(f.TargetACR)
	if target == "" {
		// 空目标 = 数据缺陷（列 NOT NULL + 应用层必填）；不许默认成 AAL1 混过去。
		return Descriptor{}, fmt.Errorf("flow: flow %s has empty target_acr", f.ID)
	}
	return BuildDescriptor(State{
		FlowID:          f.ID.String(),
		Client:          client,
		TargetACR:       target,
		AchievedAMR:     append([]string(nil), f.AchievedAMR...),
		DisabledFactors: disabled,
		Factors:         factors,
		ExpiresAt:       f.ExpiresAt,
		Now:             now,
	})
}
