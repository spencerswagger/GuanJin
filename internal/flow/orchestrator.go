package flow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件是**编排引擎**：把"开一条流 / 校验一步 / 判定是否达标"接成一条链路。
//
// 三条硬边界（都是 D5 明文，写在最前面免得实现时顺手越界）：
//  1. **持久化只走接口**（`Starter`/`Advancer`/`Consumer`/`Reader`），实现由
//     `internal/flowstore` 提供（它的 `adapter.go` 做类型转换）。本包**不写 SQL**，
//     也不 import flowstore（那会成环：flowstore 已经 import flow）。
//  2. **重活一律在锁外**（I-F8）：`VerifyResponse`（PBKDF2 ≥10 万迭代、TOTP 解封）
//     必须在 `Advance` 之前完成。写成"锁里校验、锁里推进"会让行锁持有到几百毫秒，
//     200 QPS 下直接排队雪崩。
//  3. **不达标不是错误页**（索引 §2 / AC-FLOW-02）：返回 `ACRInsufficientError`，
//     里面带**完整 Descriptor**，调用方按 `ACR_INSUFFICIENT` + `details.flow` 回 403 引导补步。

var (
	// ErrFactorDisabled 表示该业务系统禁用了这个因子（`FACTOR_DISABLED`，E4/422）。
	ErrFactorDisabled = errors.New("flow: factor is disabled for this client")
	// ErrFactorNotSupported 表示平台没有装配这个因子（`FACTOR_NOT_SUPPORTED`）。
	ErrFactorNotSupported = errors.New("flow: factor is not registered")
	// ErrNoEligibleFactor 表示没有任何因子能推进这一步（`NO_ELIGIBLE_FACTOR`）。
	ErrNoEligibleFactor = errors.New("flow: no eligible factor for this step")
	// ErrFlowNotFound 表示流不存在（`FLOW_NOT_FOUND`，E1/404）。
	ErrFlowNotFound = errors.New("flow: flow not found")
	// ErrNilDependency 表示编排器构造时缺依赖（装配期 fail-fast 用）。
	ErrNilDependency = errors.New("flow: nil dependency")
	// ErrWeakClock 表示没给时钟（时间窗与"哪一步先达成"的判定必须同一个钟）。
	ErrWeakClock = errors.New("flow: clock is required")
	// ErrAccountLocked 对应 `ACCOUNT_LOCKED`（E2/401）：账号级失败计数达阈值（D3-D）。
	//
	// 与 `ATTEMPTS_EXCEEDED` 是两个对象：后者是**单次挑战**的次数用尽（`challenges.attempts`）。
	ErrAccountLocked = errors.New("flow: account locked")
	// ErrRiskUnavailable 表示失败计数/锁定判定不可用（Redis 挂了）→ `REDIS_UNAVAILABLE`（E6/503）。
	//
	// **不允许**降级成"跳过计数继续验"：那等于把爆破保护关掉还告诉用户一切正常。
	ErrRiskUnavailable = errors.New("flow: risk counter unavailable")
)

// ACRInsufficientError 是"档位不足，请补步"的载体（**不是失败**，是引导）。
//
// 它与普通 error 的区别在于携带 `Flow`：调用方要把它原样放进
// `details.flow`（AC-FLOW-02 断言 `details.flow` 非空且字段齐全）。
type ACRInsufficientError struct {
	Flow Descriptor
}

// Error 实现 error（文本只给日志看，用户看到的是 code + details）。
func (e *ACRInsufficientError) Error() string {
	return fmt.Sprintf("flow: acr insufficient (current=%s target=%s pending=%d)",
		string(e.Flow.CurrentACR), string(e.Flow.TargetACR), pendingCount(e.Flow.Steps))
}

func pendingCount(steps []Step) int {
	n := 0
	for _, s := range steps {
		if s.Status == StepPending {
			n++
		}
	}
	return n
}

// StartInput / AdvanceInput / ConsumeInput 是本包对持久化的**输入契约**。
//
// 为什么自己定义而不是复用 flowstore 的结构体：那样 `internal/flow` 就得 import
// `internal/flowstore`，而成环（flowstore → flow 已经存在）。转换只发生一次，
// 在 flowstore 的适配层里。
type StartInput struct {
	UserID    *uuid.UUID
	ClientID  string
	TargetACR string
	TraceID   *string
}

// AdvanceInput 是"这一步已通过"的事实（**不含应答材料**：校验发生在锁外）。
//
// `Factor` 与 `AMR` **两个都要给**，别合并：
//   - `Factor` 是因子类型（`password`），用于取证与审计；
//   - `AMR` 是 RFC 8176 取值（`pwd`），用于 `flows.achieved_amr`（数据模型附录 B 的词表）。
//
// 当前 `flowstore.AdvanceRequest` 只有一个 `Factor` 字段、且它落的是 amr 语义
// （见 flowstore 的用例：`Factor: "pwd"`）。适配层按 amr 传，属于已知的字段语义重叠，
// 已在 PR 里记为待 Edge 收口的 P2（要拆成两列必须同时改 migrations 之外的读侧，属 M2 前清理）。
type AdvanceInput struct {
	FlowID      uuid.UUID
	Factor      factor.FactorType
	AMR         string
	ChallengeID string
	At          time.Time
}

// ConsumeInput 是"流已完成、一次性消费"。
type ConsumeInput struct {
	FlowID    uuid.UUID
	UserID    *uuid.UUID
	SessionID *uuid.UUID
}

// Reader 读一条流（不加锁；只用于算 Descriptor）。
type Reader interface {
	Get(ctx context.Context, flowID uuid.UUID) (*model.Flow, error)
}

// Starter 开流或复用活跃流（`flowstore.Start` 的语义：先查后建）。
type Starter interface {
	Start(ctx context.Context, in StartInput) (*model.Flow, error)
}

// Advancer 推进一步（内部是一个短事务：锁行 → 校验 → amr 并集 → 写回）。
type Advancer interface {
	Advance(ctx context.Context, in AdvanceInput) (*model.Flow, error)
}

// Consumer 一次性消费（completed + consumed_at）。
type Consumer interface {
	Consume(ctx context.Context, in ConsumeInput) (*model.Flow, error)
}

// Deps 是编排器的全部依赖（**没有一项有默认值**）。
type Deps struct {
	Reader   Reader
	Starter  Starter
	Advancer Advancer
	Consumer Consumer
	// Verifiers 按因子类型索引的"能力断言后的校验入口"（来自 SPI 的 `Authenticatable`）。
	Verifiers map[factor.FactorType]factor.Authenticatable
	// Challenges 是 `challenges` 的能力（有状态挑战的一次性消费与次数扣减）。
	//
	// **允许为 nil**：口令/TOTP 是无状态挑战，不需要它；一旦真要用（`challenge_id` 非空）
	// 而它缺失，就返回 `ErrNilChallengeStore`（fail-closed，不存在"缺了也能验"的降级路径）。
	Challenges ChallengeStore
	// Factors 平台已装配的因子事实（`FactorInfosOf(registry.Descriptors())`）。
	Factors []FactorInfo
	// DisabledFactors 读某业务系统的禁用因子（`login_policies.disabled_factors`）。
	DisabledFactors func(ctx context.Context, clientID string) ([]factor.FactorType, error)
	// Lockout 读该业务系统的失败处理参数（`login_policies.fail_threshold` / `lock_duration_min`）。
	//
	// 阈值与锁定时长**只能来自策略**：代码里不留 5 / 15 这类数字（AC-LOGIN-03 的"改键值必须跟着变"）。
	Lockout func(ctx context.Context, clientID string) (LockoutPolicy, error)
	// Guard 是账号级失败计数与锁定（Redis 权威，D3-D）。**必填**：
	// 缺了就等于没有爆破保护，装配期就该失败，而不是"悄悄少做一件事"。
	Guard AccountGuard
	// TierOf 读业务系统的档位（用于 target_acr 映射）。
	TierOf func(ctx context.Context, clientID string) (string, error)
	// ClientOf 读业务系统摘要（Descriptor 里展示用，可返回空 Client）。
	ClientOf func(ctx context.Context, clientID string) (Client, error)
	// Now 时钟（测试注入）。
	Now func() time.Time
}

// Orchestrator 是阶梯认证编排器（无本地状态，多副本行为一致）。
type Orchestrator struct {
	deps Deps
}

// New 构造编排器；缺依赖即报错（装配期 fail-fast，不等到第一个用户登录才炸）。
func New(deps Deps) (*Orchestrator, error) {
	switch {
	case deps.Reader == nil, deps.Starter == nil, deps.Advancer == nil, deps.Consumer == nil:
		return nil, fmt.Errorf("%w: reader/starter/advancer/consumer are required", ErrNilDependency)
	case deps.DisabledFactors == nil, deps.TierOf == nil:
		return nil, fmt.Errorf("%w: disabled-factors and tier readers are required", ErrNilDependency)
	case deps.Guard == nil, deps.Lockout == nil:
		// 风控是认证链的一部分（AC-LOGIN-03）：没装配就没有爆破保护，必须启动期失败。
		return nil, fmt.Errorf("%w: risk guard and lockout policy are required", ErrNilDependency)
	case deps.Now == nil:
		return nil, ErrWeakClock
	}
	if deps.ClientOf == nil {
		deps.ClientOf = func(context.Context, string) (Client, error) { return Client{}, nil }
	}
	return &Orchestrator{deps: deps}, nil
}

// TargetACRForTier 把档位映射成目标级别（索引 §4.2-15 / D12：L1|L2 → AAL1、L3 → AAL2）。
//
// **映射只此一处**：任何"顺手写个 switch"的地方，最后都会出现两个口径。
func TargetACRForTier(tier string) (factor.ACR, error) {
	switch tier {
	case "L1", "L2":
		return factor.ACRAAL1, nil
	case "L3":
		return factor.ACRAAL2, nil
	default:
		return "", fmt.Errorf("flow: unknown client tier %q", tier)
	}
}

// BeginInput 是"进入某个业务系统"的入参。
type BeginInput struct {
	ClientID string
	UserID   *uuid.UUID
	TraceID  string
	// FlowID 已带流时直接续流（`/authorize/flow?flow_id=`）；为空则按 (user, client) 找活跃流或新建。
	FlowID *uuid.UUID
}

// BeginResult 是"开始/续接一条流"的产物。
type BeginResult struct {
	Flow      Descriptor
	FlowID    uuid.UUID
	Satisfied bool
	// Satisfied 时调用方可直接发码；否则按 Flow.Steps 引导补步。
}

// Begin 取/建一条流并算出 Descriptor。
//
// 语义（AC-FLOW-01/02/07）：
//   - 已达标 → `Satisfied=true`（调用方发码，不再打扰用户）；
//   - 未达标 → 返回完整 Descriptor（**不是错误**），前端按 `steps` 渲染补步；
//   - 不可达 → `TerminalState='unreachable'`（HTTP 200，**不是错误页**，AC-FLOW-07）。
func (o *Orchestrator) Begin(ctx context.Context, in BeginInput) (*BeginResult, error) {
	if in.ClientID == "" {
		return nil, fmt.Errorf("flow: client_id is required")
	}
	tier, err := o.deps.TierOf(ctx, in.ClientID)
	if err != nil {
		return nil, err
	}
	target, err := TargetACRForTier(tier)
	if err != nil {
		return nil, err
	}
	client, err := o.deps.ClientOf(ctx, in.ClientID)
	if err != nil {
		return nil, err
	}

	var f *model.Flow
	switch {
	case in.FlowID != nil:
		// 续流：`flow_id` 已由调用方校验过归属（S6），这里只读。
		f, err = o.deps.Reader.Get(ctx, *in.FlowID)
	default:
		var trace *string
		if in.TraceID != "" {
			t := in.TraceID
			trace = &t
		}
		f, err = o.deps.Starter.Start(ctx, StartInput{
			UserID:   in.UserID,
			ClientID: in.ClientID,
			// **target_acr 由档位算**，不交给调用方传：传参数就会出现"客户端自己声明要 AAL1"。
			TargetACR: string(target),
			TraceID:   trace,
		})
	}
	if err != nil {
		return nil, err
	}

	d, err := o.descriptorOf(ctx, f, client)
	if err != nil {
		return nil, err
	}
	return &BeginResult{Flow: d, FlowID: f.ID, Satisfied: d.Satisfied}, nil
}

// VerifyInput 是"提交某一步的应答"。
type VerifyInput struct {
	FlowID uuid.UUID
	Factor factor.FactorType
	// Response 是应答材料（口令/TOTP 码）；**校验发生在锁外**。
	Response string
	// ChallengeID 该步由挑战驱动时给出（口令是无状态挑战，可空）。
	ChallengeID string
	UserID      string
}

// VerifyResult 是"这一步通过之后"的结果。
type VerifyResult struct {
	Flow      Descriptor
	Satisfied bool
}

// VerifyStep 校验一步并推进流。
//
// 顺序（每一步的位置都有理由）：
//  1. 读流 + 算 Descriptor（拿到 achieved_amr 与禁用集）；
//  2. **已完成？** → `STEP_ALREADY_SATISFIED`（D3-F：不产生新 challenge、amr 不重复）；
//  3. **被禁用？** → `FACTOR_DISABLED`；**没装配？** → `FACTOR_NOT_SUPPORTED`；
//  4. 账号级锁定判定 → 已锁回 `ACCOUNT_LOCKED`（D3-D）；
//  5. 调 `VerifyResponse`（**锁外重活**）；失败且是凭据错误 → 记一次账号级失败；
//  6. 成功 → 清零计数 → `Advance`（短事务里锁行、并集、写回）；
//  7. 重算 Descriptor：达标 → `Satisfied`；未达标 → `ACRInsufficientError`（带 Descriptor）。
func (o *Orchestrator) VerifyStep(ctx context.Context, in VerifyInput) (*VerifyResult, error) {
	if in.FlowID == uuid.Nil {
		return nil, ErrFlowNotFound
	}
	if in.Factor == "" {
		return nil, ErrNoEligibleFactor
	}

	f, err := o.deps.Reader.Get(ctx, in.FlowID)
	if err != nil {
		return nil, err
	}
	// 读路径也判状态与过期：过期行仍在（第一段不删行），不判就会把 `FLOW_EXPIRED`
	// 拖到 `Advance` 的事务里才报，且在中间白做一次 PBKDF2。
	if err := ensureFlowUsable(f, o.deps.Now().UTC()); err != nil {
		return nil, err
	}
	// **匿名流的已知边界**：`flows.user_id IS NULL` 时既没有账号可锁，也没有账号可计数，
	// 因此 D3-D 的失败计数只对"已绑定用户的流"生效。S6 建登录流时必须把 `user_id` 绑上
	// （登录页已解析出账号时）；否则匿名链路等于没有爆破保护 —— 这一点在 PR 里如实登记，
	// 要改 `verify` 入参就必须走契约变更（CH），不在这里私自加字段。
	//
	// 用户以**流上的 `user_id` 为准**：流已绑定用户时，请求体/调用方给的用户标识一律忽略
	// （否则等于让调用方指定"验谁的口令"）。只有匿名流（`user_id IS NULL`）才采信调用方
	// 解析出来的用户 —— 那种情况下不采信就没法验（比如登录页先解析标识符再验口令）。
	if f.UserID != nil {
		in.UserID = f.UserID.String()
	}
	client, err := o.deps.ClientOf(ctx, f.ClientID)
	if err != nil {
		return nil, err
	}

	// ① 已完成 → 409（**唯一合法返回点就是这里**，D3-F）。
	amr, ok := factor.AMRFor(in.Factor)
	if !ok {
		return nil, ErrFactorNotSupported
	}
	for _, done := range f.AchievedAMR {
		if done == amr {
			return nil, ErrStepAlreadySatisfied
		}
	}

	// ② 禁用/未装配。
	disabled, err := o.deps.DisabledFactors(ctx, f.ClientID)
	if err != nil {
		return nil, err
	}
	if containsFactor(disabled, in.Factor) {
		return nil, ErrFactorDisabled
	}
	verifier, ok := o.deps.Verifiers[in.Factor]
	if !ok {
		return nil, ErrFactorNotSupported
	}

	// ③ 账号级锁定（Redis 权威，D3-D）：已锁定就不必再算一次 PBKDF2（那正是爆破的成本）。
	userID := in.UserID
	policy, err := o.deps.Lockout(ctx, f.ClientID)
	if err != nil {
		return nil, err
	}
	if userID != "" {
		if _, locked, lerr := o.deps.Guard.Status(ctx, userID, policy); lerr != nil {
			return nil, lerr
		} else if locked {
			return nil, ErrAccountLocked
		}
	}

	// ④ 锁外校验（PBKDF2/TOTP 解封都在这里）。
	if _, verr := verifier.VerifyResponse(ctx, factor.VerifyRequest{
		ChallengeID: in.ChallengeID,
		FlowID:      in.FlowID.String(),
		UserID:      userID,
		Response:    in.Response,
	}); verr != nil {
		// 只有"凭据不对"才计入账号级失败数；业务性拒绝（如"该步已完成"）已在上面前置返回，
		// 走到这里仍可能是别的哨兵 → 用 credential 的错误判定，不按"任何错误都计数"。
		if userID != "" && isCredentialFailure(verr) {
			_, locked, rerr := o.deps.Guard.RecordFailure(ctx, userID, policy)
			if rerr != nil {
				return nil, rerr
			}
			if locked {
				// 达阈值的那一次就回 ACCOUNT_LOCKED（AC-LOGIN-03），而不是再抛 INVALID_CREDENTIAL。
				return nil, ErrAccountLocked
			}
		}
		return nil, verr
	}

	// ⑤ 成功即全局清零（接口 §3.2.2：成功时清零）。
	if userID != "" {
		if err := o.deps.Guard.Reset(ctx, userID); err != nil {
			return nil, err
		}
	}

	// ⑥ 短事务推进（锁内只做读该行 + 校验 + 并集 + 写回）。
	// 用返回值直接算下一步 Descriptor：再查一次会多一次往返，也多一个"读到别人改过"的窗口。
	updated, err := o.deps.Advancer.Advance(ctx, AdvanceInput{
		FlowID:      in.FlowID,
		Factor:      in.Factor,
		AMR:         amr,
		ChallengeID: in.ChallengeID,
		At:          o.deps.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}

	// ⑦ 用**推进后的**流重算 Descriptor（Advance 已返回写回后的行，不再回查）。
	d, err := o.descriptorOf(ctx, updated, client)
	if err != nil {
		return nil, err
	}
	if !d.Satisfied {
		// 未达标不是失败：把 Descriptor 原样交回去（`details.flow`）。
		return &VerifyResult{Flow: d, Satisfied: false}, &ACRInsufficientError{Flow: d}
	}
	return &VerifyResult{Flow: d, Satisfied: true}, nil
}

// UserOfFlow 返回流绑定的用户（匿名流返回空串）。
//
// 授权端点发码时需要 `sub`；用户只从**流**上取（不信请求体），与 `VerifyStep` 同口径。
func (o *Orchestrator) UserOfFlow(ctx context.Context, flowID uuid.UUID) (string, error) {
	if flowID == uuid.Nil {
		return "", ErrFlowNotFound
	}
	f, err := o.deps.Reader.Get(ctx, flowID)
	if err != nil {
		return "", err
	}
	if f.UserID == nil {
		return "", nil
	}
	return f.UserID.String(), nil
}

// Finish 在达标之后消费流（一次性，F4）。
//
// 单独一个方法而不是藏在 VerifyStep 里：消费的时机属于**业务决定**
// （"发码前消费"还是"令牌换到手再消费"），编排层不替它做主。
func (o *Orchestrator) Finish(ctx context.Context, flowID uuid.UUID, userID, sessionID *uuid.UUID) (*model.Flow, error) {
	return o.deps.Consumer.Consume(ctx, ConsumeInput{FlowID: flowID, UserID: userID, SessionID: sessionID})
}

// descriptorOf 读禁用集 + 算 Descriptor（Begin 与 VerifyStep 共用，避免两处口径）。
func (o *Orchestrator) descriptorOf(ctx context.Context, f *model.Flow, client Client) (Descriptor, error) {
	disabled, err := o.deps.DisabledFactors(ctx, f.ClientID)
	if err != nil {
		return Descriptor{}, err
	}
	return DescriptorOfFlow(f, client, disabled, o.deps.Factors, o.deps.Now().UTC())
}

func containsFactor(list []factor.FactorType, want factor.FactorType) bool {
	for _, f := range list {
		if f == want {
			return true
		}
	}
	return false
}

// SortedFactors 返回按类型字典序的因子清单（Descriptor 输出稳定，便于前端 diff 与测试）。
func SortedFactors(infos []FactorInfo) []FactorInfo {
	out := append([]FactorInfo(nil), infos...)
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// StatusFor 给出"该回什么 HTTP 状态"（含只有本包才认识的那个码）。
//
// 为什么放在这里：`STEP_ALREADY_SATISFIED` 的常量**只允许出现在 internal/flow**
// （CI 禁码扫描第二条），所以 httpx 的 `HTTPStatus` 里不会有它 —— 缺了这个函数，
// 它会落到 default 分支变成 500。
func StatusFor(err error) (int, bool) {
	code, ok := CodeOf(err)
	if !ok {
		return 0, false
	}
	if code == CodeStepAlreadySatisfied {
		return 409, true // E4：资源与不变式
	}
	return httpx.HTTPStatus(code), true
}

// Descriptor 的 HTTP 语义 helper：决定该用哪个码（调用方不必自己 switch）。
//
// 返回 ok=false 表示"这不是 Descriptor 相关的错误"，交上层按 500 处理。
func HTTPCodeFor(d Descriptor) (httpx.Code, bool) {
	if d.TerminalState == TerminalUnreachable {
		// AC-FLOW-07：不可达是 **200 + terminal_state**，不是错误码。
		return "", false
	}
	if d.Satisfied {
		return "", false
	}
	return httpx.CodeACRInsufficient, true
}
