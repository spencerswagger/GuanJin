package flow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件把 **challenge 的下发与校验**接到编排层上（S5：`/auth/challenge/{factor}`、
// `/auth/verify/{factor}`、`/authorize/flow`）。
//
// 三条边界，写在最前面：
//  1. **状态机是 `challenge.go` 的那一套**（过期 → 已消费 → 次数用尽），这里只负责
//     "把库里那一行读出来交给它、把它的结论落回去"。权威状态永远在 `challenges` 表，
//     Redis 只承载限流计数（D3-D）；因此 `FLUSHALL` 不影响判定（AC-FC-06）。
//  2. **无状态因子不落挑战行**：口令/TOTP 的"挑战就是那次提交本身"（见
//     `holding.PasswordProvider.BeginChallenge`），`challenge_id` 为空即跳过状态机。
//     落不落行由因子声明，编排层不按类型猜。
//  3. **已完成的步骤不再下发挑战**（D3-F / AC-FLOW-03）：判定必须在调用因子之前，
//     否则会先产生一行 `challenges` 再回 409，语义就反了。

var (
	// ErrFlowExpired 对应 `FLOW_EXPIRED`（E1/400）：行仍在（第一段只置 expired、不删行），
	// 所以这个错误是**稳定**的，不会时隐时现（AC-FLOW-10①）。
	ErrFlowExpired = errors.New("flow: flow expired")
	// ErrFlowIDRequired 表示 login/step_up 的挑战没带 `flow_id`（CH-1 已追认：必填）。
	ErrFlowIDRequired = errors.New("flow: flow_id is required for login/step_up")
	// ErrPurposeRequired 表示没给 `purpose`（D2 六值，**不设默认值**）。
	ErrPurposeRequired = errors.New("flow: purpose is required")
	// ErrChallengeNotPersisted 表示因子要求"发一次性码"，却没给出落库后的 `challenge_id`。
	//
	// 这是**装配缺陷**而不是用户错误：`type_code` 类挑战的权威状态必须在 `challenges` 行里，
	// 没有行就等于"码无处可验"。宁可 500，也不允许"看起来发了码其实没发"。
	ErrChallengeNotPersisted = errors.New("flow: stateful challenge ticket carries no challenge_id")
	// ErrNilChallengeStore 表示要用状态机却没有注入 `challenges` 仓储（fail-closed）。
	ErrNilChallengeStore = errors.New("flow: challenge store is required")
	// ErrInvalidClient 表示业务系统不存在或已停用（`INVALID_CLIENT`，E1/400）。
	//
	// 编排层不读 `clients` 表（那是装配层注入的读函数），所以这个哨兵由装配层在
	// 把仓储错误翻上来时使用 —— 不翻就会落到 500，而接口 §3.1.4 明确是 400。
	ErrInvalidClient = errors.New("flow: invalid client")
)

// ChallengeStore 是编排层需要的 `challenges` 能力（仓储实现见 `internal/store`，
// 转换见 `internal/flowstore.ChallengeAdapter` —— 那一层负责把"没查到"换成音一哨兵）。
type ChallengeStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (*model.Challenge, error)
	Consume(ctx context.Context, id uuid.UUID, at time.Time) (int64, error)
	RecordAttempt(ctx context.Context, id uuid.UUID, now time.Time) (remaining int32, exhausted bool, err error)
}

// BeginChallengeInput 是"下发一次挑战"的入参（接口 §3.2.1 的请求要点）。
type BeginChallengeInput struct {
	// FlowID 阶梯流的 id；`purpose ∈ {login, step_up}` 时**必填**（CH-1）。
	FlowID uuid.UUID
	// Factor 因子类型（由路径 `/auth/challenge/{factor}` 给出）。
	Factor factor.FactorType
	// Purpose 六值词表（D2），**必填、无默认**。
	Purpose factor.Purpose
	// ClientID 只在"无流挑战"（register/enroll/recover/verify_identifier）时使用：
	// 那些动作没有阶梯流，业务系统上下文由请求显式携带。
	ClientID string
}

// IssuedChallenge 是一次下发的产物（接口 §3.2.1 的响应要点）。
type IssuedChallenge struct {
	FlowID *uuid.UUID
	Factor factor.FactorType
	// NextAction 前端渲染原子（`input_password` / `input_totp` / `type_code`）。
	NextAction factor.NextAction
	// ChallengeID 空表示无状态挑战（口令/TOTP）——此时客户端直接提交应答。
	ChallengeID *string
	// TargetMasked 投递目标回显（channel 型才有）。
	TargetMasked string
	// ExpiresAt 该挑战（无状态时=该流）的失效时刻。
	ExpiresAt time.Time
	// AttemptsLeft 剩余尝试次数；无状态挑战为 0（次数由账号级计数管，D3-D）。
	AttemptsLeft int
}

// BeginChallenge 下发一次挑战。
//
// 顺序与 `VerifyStep` 对齐（同一套判定顺序，避免"下发时能过、校验时说不行"）：
//  1. purpose 合法性 → CH-1 的 `flow_id` 必填；
//  2. 读流 + 状态/过期（已完成/已放弃按"流已不在"，过期按 `FLOW_EXPIRED`）；
//  3. **该步已完成 → `STEP_ALREADY_SATISFIED`**（AC-FLOW-03：不产生新挑战行）；
//  4. 被业务系统禁用 → `FACTOR_DISABLED`；平台没装配 → `FACTOR_NOT_SUPPORTED`；
//  5. 交给因子的 `BeginChallenge`（能力断言，不看类型名）。
func (o *Orchestrator) BeginChallenge(ctx context.Context, in BeginChallengeInput) (*IssuedChallenge, error) {
	if in.Purpose == "" {
		return nil, ErrPurposeRequired
	}
	if !in.Purpose.Valid() {
		return nil, fmt.Errorf("%w: %q", factor.ErrUnknownPurpose, string(in.Purpose))
	}
	if in.Factor == "" {
		return nil, ErrNoEligibleFactor
	}

	now := o.deps.Now().UTC()

	var (
		f        *model.Flow
		clientID = in.ClientID
		userID   string
	)
	if RequiresFlow(in.Purpose) {
		if in.FlowID == uuid.Nil {
			return nil, ErrFlowIDRequired
		}
		var err error
		f, err = o.deps.Reader.Get(ctx, in.FlowID)
		if err != nil {
			return nil, err
		}
		if err := ensureFlowUsable(f, now); err != nil {
			return nil, err
		}
		clientID = f.ClientID
		if f.UserID != nil {
			userID = f.UserID.String()
		}
	}

	// ③ 已完成 → 409（**在调用因子之前**，所以不会留下挑战行）。
	amr, ok := factor.AMRFor(in.Factor)
	if !ok {
		return nil, ErrFactorNotSupported
	}
	if f != nil {
		for _, done := range f.AchievedAMR {
			if done == amr {
				return nil, ErrStepAlreadySatisfied
			}
		}
	}

	// ④ 禁用集：只有给了业务系统上下文才判（无上下文自助动作的"生效配置"判定不在本切片）。
	if clientID != "" {
		disabled, err := o.deps.DisabledFactors(ctx, clientID)
		if err != nil {
			return nil, err
		}
		if containsFactor(disabled, in.Factor) {
			return nil, ErrFactorDisabled
		}
	}

	verifier, ok := o.deps.Verifiers[in.Factor]
	if !ok {
		return nil, ErrFactorNotSupported
	}

	// ⑤ 因子自己决定要不要落一次性码（口令：不落；短信/邮箱：落）。
	ticket, err := verifier.BeginChallenge(ctx, factor.ChallengeRequest{
		FlowID:   flowIDString(f),
		ClientID: clientID,
		UserID:   userID,
		Purpose:  in.Purpose,
	})
	if err != nil {
		return nil, err
	}
	if !ticket.NextAction.Valid() {
		return nil, fmt.Errorf("%w: factor %q returned next_action %q",
			ErrNoEligibleFactor, string(in.Factor), string(ticket.NextAction))
	}
	// `type_code` 的码必须在 `challenges` 里有权威行（否则"发了码却无处验"）。
	if ticket.NextAction == factor.NextActionTypeCode && ticket.ChallengeID == "" {
		return nil, fmt.Errorf("%w: factor %q", ErrChallengeNotPersisted, string(in.Factor))
	}

	out := &IssuedChallenge{
		Factor:       in.Factor,
		NextAction:   ticket.NextAction,
		TargetMasked: ticket.TargetMasked,
		ExpiresAt:    ticket.ExpiresAt,
		AttemptsLeft: ticket.AttemptsLeft,
	}
	if ticket.ChallengeID != "" {
		id := ticket.ChallengeID
		out.ChallengeID = &id
	}
	if f != nil {
		fid := f.ID
		out.FlowID = &fid
		if out.ExpiresAt.IsZero() {
			// 无状态挑战没有自己的 TTL：以流的有效期为准（过期的流不允许再开挑战）。
			out.ExpiresAt = f.ExpiresAt
		}
	}
	return out, nil
}

// VerifyChallenge 校验一次应答，并在有挑战行时驱动状态机（一次性消费 / 尝试次数）。
//
// 无 `challenge_id`（口令、TOTP）时**直接**走 `VerifyStep`：无状态挑战的次数限制属
// 账号级失败计数（Redis 权威，D3-D），不是单挑战计数，两者不能互相顶替。
//
// 有 `challenge_id` 时：
//   - 先按状态机判"能不能用"（过期 → 已消费 → 次数用尽，顺序见 `Challenge.Check`）；
//   - 校验失败且原因是**凭据不对**才扣一次尝试（业务性拒绝——如"该步已完成"——
//     不该消耗用户的尝试次数）；扣到用尽回 `ATTEMPTS_EXCEEDED`（而不是 `INVALID_CREDENTIAL`）；
//   - 校验成功才**一次性消费**；`UPDATE … WHERE consumed_at IS NULL` 影响 0 行说明被并发抢先，
//     回 `CHALLENGE_CONSUMED`（不允许"两个请求都算成功"）。
func (o *Orchestrator) VerifyChallenge(ctx context.Context, in VerifyInput) (*VerifyResult, error) {
	if in.ChallengeID == "" {
		return o.VerifyStep(ctx, in)
	}
	if o.deps.Challenges == nil {
		return nil, ErrNilChallengeStore
	}
	now := o.deps.Now().UTC()

	id, err := uuid.Parse(in.ChallengeID)
	if err != nil {
		return nil, ErrChallengeNotFound
	}
	row, err := o.deps.Challenges.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	state, err := ChallengeOfRow(row)
	if err != nil {
		return nil, err
	}
	if err := state.Check(now); err != nil {
		return nil, err
	}

	res, verr := o.VerifyStep(ctx, in)
	if verr != nil {
		if errors.Is(verr, credential.ErrInvalidCredential) {
			_, exhausted, rerr := o.deps.Challenges.RecordAttempt(ctx, id, now)
			if rerr != nil {
				return nil, rerr
			}
			if exhausted {
				return nil, ErrAttemptsExceeded
			}
		}
		return nil, verr
	}

	consumed, err := o.deps.Challenges.Consume(ctx, id, now)
	if err != nil {
		return nil, err
	}
	if consumed == 0 {
		// 顺序说明（**已知取舍，不是遗漏**）：先推进流、后消费挑战，两者不在同一个事务里
		// （`flows` 的短事务在 flowstore，`challenges` 的消费在仓储层，跨包接口不允许把它们
		// 合成一个事务）。并发同 `challenge_id` 双发时，先到的那个会完成推进，后到的在这里
		// 拿到 0 行 → `CHALLENGE_CONSUMED`；而那一步的 `amr` **不会重复累加**（I-F2 去重 +
		// `completed_steps` 按 factor 去重，AC-FLOW-11③）。要消掉这个窗口，得让编排层持有
		// `challenges` 的事务句柄 —— 那会打破"本包不写 SQL"的边界，留到 S6 会话建立时一并收口。
		return nil, ErrChallengeConsumed
	}
	return res, nil
}

// Describe 取一条流的 Descriptor（`GET /authorize/flow`）。
//
// 读路径也要判状态与过期：过期行仍在（第一段不删行），若不判就会给前端一份
// "看起来还能补步"的契约（AC-FLOW-10① 要求首次访问必得 `FLOW_EXPIRED`）。
func (o *Orchestrator) Describe(ctx context.Context, flowID uuid.UUID) (*Descriptor, error) {
	if flowID == uuid.Nil {
		return nil, ErrFlowNotFound
	}
	f, err := o.deps.Reader.Get(ctx, flowID)
	if err != nil {
		return nil, err
	}
	if err := ensureFlowUsable(f, o.deps.Now().UTC()); err != nil {
		return nil, err
	}
	client, err := o.deps.ClientOf(ctx, f.ClientID)
	if err != nil {
		return nil, err
	}
	d, err := o.descriptorOf(ctx, f, client)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// ChallengeOfRow 把 `challenges` 的一行折成状态机要的形态。
//
// 单独导出（而不是塞在 VerifyChallenge 里）是为了让"行 → 状态"的换算只有一个出口：
// 换算错了会把"已消费"看成"可用"，那是最危险的一类错。
func ChallengeOfRow(row *model.Challenge) (Challenge, error) {
	if row == nil {
		return Challenge{}, ErrChallengeNotFound
	}
	purpose, err := factor.ParsePurpose(row.Purpose)
	if err != nil {
		return Challenge{}, err
	}
	c, err := NewChallenge(row.ID.String(), purpose, factor.FactorType(row.FactorType),
		int(row.MaxAttempts), row.ExpiresAt)
	if err != nil {
		return Challenge{}, err
	}
	c.Attempts = int(row.Attempts)
	c.ConsumedAt = row.ConsumedAt
	return c, nil
}

// ensureFlowUsable 判定"这条流现在还能不能用"，并给出与接口一致的语义：
// active → 继续判过期；expired → `FLOW_EXPIRED`；completed/abandoned → `FLOW_NOT_FOUND`
// （I-F4 的"流已不在"，AC-FLOW-10②）。
func ensureFlowUsable(f *model.Flow, now time.Time) error {
	if f == nil {
		return ErrFlowNotFound
	}
	switch Status(f.Status) {
	case StatusActive:
		// 继续判过期
	case StatusExpired:
		return ErrFlowExpired
	case StatusCompleted, StatusAbandoned:
		return ErrFlowNotFound
	default:
		// 未知状态 = 数据缺陷：不猜"能不能用"，直接按"流不在"回。
		return fmt.Errorf("flow: flow %s has unknown status %q", f.ID, f.Status)
	}
	if !now.Before(f.ExpiresAt) {
		return ErrFlowExpired
	}
	return nil
}

func flowIDString(f *model.Flow) string {
	if f == nil {
		return ""
	}
	return f.ID.String()
}
