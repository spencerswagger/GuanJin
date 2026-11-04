// Package flowstore 是「阶梯认证中间态」的**落库版**：把 `internal/flow` 的纯规则
// 与 `internal/store` 的 `flows` 持久化接在**一次短事务**里。
//
// 为什么单独一个包，而不是塞进 `internal/flow` 或 `internal/store`：
//   - D5 的落点划分是 Core 写 `internal/flow`（纯逻辑：Descriptor / 状态推进规则 / amr 单调），
//     Edge 写 `internal/store`（模型 / 仓储 / SQL 形态与错误映射）。两侧**互不 import**；
//   - 但「锁 → 校验 → 累加 amr → 写回」必须是一个事务（I-F5），它同时需要两侧。
//     放进任一侧都会造成 `store→flow` 或 `flow→store` 的单向依赖，第二天对侧
//     import 回来就成环。本包只被 `cmd/*` 与编排层调用，是**单向的桥**。
//
// 本包负责的不变式（decisions.md D5）：
//   - I-F2 `achieved_amr` 单调不减（回退即 `flow.ErrAMRRegression`）；
//   - I-F3 过期判定用**库时间** `now()`，过期只置 `status='expired'`、**不删行**；
//   - I-F4 一次性消费（`completed` 与 `consumed_at` 同真同假），二次提交按「流已不在」处理；
//   - I-F5 并发补步靠 `SELECT … FOR UPDATE` 串行，不丢 amr；
//   - I-F7 `completed` 行允许**一次** `session_id: NULL → 值` 回填，之后不得再改；
//   - I-F8 全站锁序 `sessions → flows`（本包**从不**对 `sessions` 加锁）、
//     锁内禁重活（PBKDF2 / 投递 / 外部 HTTP 必须在调用本包**之前**算完）。
//
// 不负责的：`target_acr` 由档位映射算出（Core 的 Descriptor）、挑战应答材料（`challenges`）、
// 令牌签发（S6）。本包只认「这一步已通过、因子是哪个」这一事实。
package flowstore

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/flow"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本包的显式错误。**都不是对外错误码**：对外码由 `internal/httpx` 按 §5.4 给
// （`FLOW_EXPIRED` / `FLOW_NOT_FOUND` / `STEP_ALREADY_SATISFIED` 等），
// 这里只区分"哪种失败"，避免把码名散进数据层。
var (
	// ErrTTLRequired 表示构造时没拿到正的 TTL。
	// D5：`flow.ttl_seconds` 由 `runtime_config` 提供、**代码里不得留 600 常量**，
	// 所以 TTL 只能由调用方（读完配置后）显式传入；默认值在这里会变成隐性逻辑。
	ErrTTLRequired = errors.New("flowstore: a positive flow TTL is required (runtime_config flow.ttl_seconds)")
	// ErrMissingDependency 表示缺 DB 句柄或仓储。
	ErrMissingDependency = errors.New("flowstore: db and flow repo are required")
	// ErrFlowIDRequired 表示没给 flow_id（CH-1：阶梯内的流必须显式指明）。
	ErrFlowIDRequired = errors.New("flowstore: flow id is required")
	// ErrFactorRequired 表示没给因子。
	ErrFactorRequired = errors.New("flowstore: factor is required")
	// ErrStepTimeRequired 表示没给该步的达成时刻。
	ErrStepTimeRequired = errors.New("flowstore: step time is required")
	// ErrClientRequired 表示没给业务系统上下文（索引 §2：注册/登录必带 client）。
	ErrClientRequired = errors.New("flowstore: client id is required")
	// ErrTargetACRUnsupported 表示目标级别超出 V1 产出范围。
	// D12：对外只 `AAL1|AAL2`，`AAL3` 预留但 **V1 不得产出**——DB CHECK 允许 AAL3，
	// 所以这道闸只能在应用层；缺了它就会出现"对外契约不实"。
	ErrTargetACRUnsupported = errors.New("flowstore: target_acr must be AAL1 or AAL2 (AAL3 reserved, D12)")
	// ErrFlowExpired 对应 `FLOW_EXPIRED`（E1/400）。
	ErrFlowExpired = errors.New("flowstore: flow expired")
	// ErrFlowNotActive 表示流已 `completed`/`abandoned`/`expired`，不能再推进。
	ErrFlowNotActive = errors.New("flowstore: flow is not active")
	// ErrFlowConsumed 表示一次性消费已被用掉（对外按 `FLOW_NOT_FOUND` 处理，I-F4）。
	ErrFlowConsumed = errors.New("flowstore: flow already consumed")
	// ErrUserRequired 表示要 `completed` 却没有 `user_id`（I-F1 的前置检查，
	// 不靠 DB CHECK 兜：撞 CHECK 只能给出一个 400，说不清是"少了用户"）。
	ErrUserRequired = errors.New("flowstore: completed flow requires user_id (I-F1)")
	// ErrSessionImmutable 表示试图二次改写 `session_id`（I-F7 只允许 NULL→值一次）。
	ErrSessionImmutable = errors.New("flowstore: session_id may be backfilled only once (I-F7)")
	// ErrFlowVanished 表示刚锁住的行在写回时不见了（被两段式清理删掉）——属缺陷，不是业务错。
	ErrFlowVanished = errors.New("flowstore: flow vanished between lock and update")
)

// Service 是 `flows` 的事务级编排门面。
//
// 无本地状态（多副本下必须行为一致）：只持有 DB 句柄、仓储与 TTL。
type Service struct {
	db   *gorm.DB
	repo *store.FlowRepo
	ttl  time.Duration
}

// New 构造服务。
//
// **校验顺序是有意的**：先 TTL（它是最容易被"顺手给个默认值"糊过去的一条），
// 再依赖。缺 TTL 时绝不退化成 600 —— 那正是 D5 明令禁止的隐性默认。
func New(db *gorm.DB, repo *store.FlowRepo, ttl time.Duration) (*Service, error) {
	if ttl <= 0 {
		return nil, ErrTTLRequired
	}
	if db == nil || repo == nil {
		return nil, ErrMissingDependency
	}
	return &Service{db: db, repo: repo, ttl: ttl}, nil
}

// TTL 返回本实例使用的流 TTL（只读，便于上层把同一个值回显给前端 `expires_at` 之外的提示）。
func (s *Service) TTL() time.Duration { return s.ttl }

// StartRequest 是「开一条流」的入参。
type StartRequest struct {
	// UserID 匿名流（注册/登录前）为 nil，完成时回填。
	UserID *uuid.UUID
	// ClientID 必填（登录一定有业务系统上下文）。
	ClientID string
	// TargetACR 目标保证级别；空 → `AAL1`（D5 的列默认值，显式写出不靠 DB 默认）。
	TargetACR string
	// TraceID 只在这里写一次；补步**不覆盖**（D4-C(4)）。
	TraceID *string
}

// Start 开一条流，或**复用**该用户对该 client 已有的活跃流。
//
// 为什么"先查后建"：`flows_one_active_per_user_client_uniq` 的 23505 在 CH-7⑤ 里
// 映射成 `COOLDOWN_ACTIVE`(409)，那是"要等"的语义；而用户从第二个标签页打开登录页，
// 本意是"接着上一次走"。所以正常路径必须先查（命中即返回同一条 `flow_id`），
// 唯一索引只在**并发竞态**下兜底。
//
// `expires_at` 用**库时间**算（`now() + ttl`），不用应用时钟：I-F3 的过期判定是
// `expires_at <= now()`，同一个钟才谈得上一致（多副本时钟漂移会让 TTL 忽长忽短）。
func (s *Service) Start(ctx context.Context, req StartRequest) (*model.Flow, error) {
	if req.ClientID == "" {
		return nil, ErrClientRequired
	}
	target := req.TargetACR
	if target == "" {
		target = "AAL1"
	}
	if target != "AAL1" && target != "AAL2" {
		return nil, ErrTargetACRUnsupported
	}

	if req.UserID != nil {
		existing, err := s.repo.FindActiveByUserClient(ctx, *req.UserID, req.ClientID)
		if err == nil {
			return existing, nil // 续流：同一条 flow_id 继续走
		}
		if !errors.Is(err, store.ErrFlowNotFound) {
			return nil, err
		}
	}

	f := &model.Flow{
		UserID:         req.UserID,
		ClientID:       req.ClientID,
		TargetACR:      target,
		AchievedAMR:    model.StringArray{},
		CompletedSteps: model.StepList{},
		Status:         string(flow.StatusActive),
		TraceID:        req.TraceID,
		// 先占位、随后立刻用库时间改写：`expires_at` 是 NOT NULL，
		// 而唯一权威的钟在库里，所以不在这里用应用时钟凑一个值。
		ExpiresAt: time.Time{},
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := s.repo.WithTx(tx)
		if err := r.Create(ctx, f); err != nil {
			return err
		}
		var expires time.Time
		row := tx.WithContext(ctx).Raw(
			`UPDATE flows SET expires_at = now() + make_interval(secs => $2)
			  WHERE id = $1 RETURNING expires_at`, f.ID, s.ttl.Seconds()).Row()
		if scanErr := row.Scan(&expires); scanErr != nil {
			return scanErr
		}
		f.ExpiresAt = expires
		return nil
	})
	if err != nil {
		return nil, store.MapError(err)
	}
	return f, nil
}

// AdvanceRequest 是「推进已达成的一步」的入参。
//
// 注意这里**只有结论、没有凭据**：挑战应答的校验（`challenges` 一次性消费、
// PBKDF2 比对、TOTP 窗口）必须由调用方在**锁外**做完，再把"该因子已通过"这一事实交进来。
// I-F8 的锁内禁令即此：锁里只许读该行、校验、写回，不许算哈希、不许发短信。
type AdvanceRequest struct {
	FlowID      uuid.UUID
	Factor      string
	ChallengeID string
	// At 是该步达成的时刻（调用方在锁外取），不在这里取 `now()`：
	// "何时达成"与"何时写库"是两件事，混在一起就没法复盘。
	At time.Time
}

// Advance 在**一个短事务**里推进一条流：锁行 → 校验状态与过期 → amr 并集 + 追加步骤 → 写回。
//
// 返回的是写回后的流（就地更新，不再回查一次：锁还在手上，回查也是同一个值）。
// 失败时也返回**已更新的流**（过期分支会把它置成 `expired`），便于上层拼 `details.flow`。
func (s *Service) Advance(ctx context.Context, req AdvanceRequest) (*model.Flow, error) {
	if req.FlowID == uuid.Nil {
		return nil, ErrFlowIDRequired
	}
	if req.Factor == "" {
		return nil, ErrFactorRequired
	}
	if req.At.IsZero() {
		return nil, ErrStepTimeRequired
	}

	var out *model.Flow
	// bizErr 用于"必须提交事务、但仍要报业务错"的分支（过期置位即属此类）。
	var bizErr error
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := s.repo.WithTx(tx)
		f, err := r.LockByID(ctx, tx, req.FlowID)
		if err != nil {
			return err
		}
		out = f

		switch flow.Status(f.Status) {
		case flow.StatusActive:
			// 继续
		case flow.StatusExpired:
			return ErrFlowExpired
		default:
			return ErrFlowNotActive
		}

		// I-F3：过期判定交给库里判（`WHERE … expires_at <= now()`）。
		// 行**不删**——`expired` 保留 7 天，`FLOW_EXPIRED` 才能稳定返回（AC-FLOW-10 不 flaky）。
		expired, err := r.MarkExpiredIfActive(ctx, req.FlowID)
		if err != nil {
			return err
		}
		if expired > 0 {
			f.Status = string(flow.StatusExpired)
			// ⚠️ 这里**不能 return err**：那会让整个事务回滚，把刚置的 `expired` 一并撤掉，
			// 结果就是"报 FLOW_EXPIRED 但库里还是 active"。先让事务提交，再把业务错抛出去。
			bizErr = ErrFlowExpired
			return nil
		}

		steps := stepsToFlow(f.CompletedSteps)
		newSteps, err := flow.AppendStep(steps, flow.CompletedStep{
			Factor:      req.Factor,
			ChallengeID: req.ChallengeID,
			At:          req.At.UTC().Format(time.RFC3339Nano),
		})
		if err != nil {
			return err // flow.ErrStepAlreadySatisfied / 参数错
		}
		newAMR, err := flow.AddAMR(append([]string(nil), f.AchievedAMR...), req.Factor)
		if err != nil {
			return err // flow.ErrAMRRegression（本路径不会发生，留着防止将来被改写坏）
		}
		modelSteps, err := stepsFromFlow(newSteps)
		if err != nil {
			return err
		}
		amr := model.StringArray(newAMR)

		affected, err := r.Apply(ctx, tx, req.FlowID, store.FlowPatch{
			AchievedAMR:    &amr,
			CompletedSteps: &modelSteps,
		})
		if err != nil {
			return err
		}
		if affected != 1 {
			return ErrFlowVanished
		}
		f.AchievedAMR = amr
		f.CompletedSteps = modelSteps
		return nil
	})
	if err != nil {
		return out, err
	}
	if bizErr != nil {
		return out, bizErr
	}
	return out, nil
}

// ConsumeRequest 是「消费一条流」的入参（一次性，I-F4）。
type ConsumeRequest struct {
	FlowID uuid.UUID
	// UserID 注册前的匿名流在这里回填（I-F1：completed 必须 user_id NOT NULL）。
	UserID *uuid.UUID
	// SessionID 登录成功后回填；**只允许 NULL→值一次**（I-F7）。
	SessionID *uuid.UUID
}

// Consume 把流一次性消费成 `completed`。
//
// 两条路径，都是 D5 明文：
//  1. `active` → `completed`：置状态 + `consumed_at = now()`（与 status 同真同假，I-F4）；
//  2. 已 `completed` 且 `session_id IS NULL` 且本次给了 session：**一次性回填**（I-F7）。
//     这条例外是给 S5 编排完成、S6 才建会话的交接期用的；回填后不得再改。
//
// `consumed_at` 同样用库时间：TTL/超时/留存全是库里的钟，审计口径必须一致。
func (s *Service) Consume(ctx context.Context, req ConsumeRequest) (*model.Flow, error) {
	if req.FlowID == uuid.Nil {
		return nil, ErrFlowIDRequired
	}

	var out *model.Flow
	// bizErr：过期置位必须提交事务（同 Advance 的理由）。
	var bizErr error
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := s.repo.WithTx(tx)
		f, err := r.LockByID(ctx, tx, req.FlowID)
		if err != nil {
			return err
		}
		out = f

		switch flow.Status(f.Status) {
		case flow.StatusExpired:
			return ErrFlowExpired
		case flow.StatusAbandoned:
			return ErrFlowNotActive
		case flow.StatusCompleted:
			return s.backfillSession(ctx, tx, f, req)
		case flow.StatusActive:
			// 下面继续
		default:
			return ErrFlowNotActive
		}

		expired, err := r.MarkExpiredIfActive(ctx, req.FlowID)
		if err != nil {
			return err
		}
		if expired > 0 {
			f.Status = string(flow.StatusExpired)
			bizErr = ErrFlowExpired
			return nil
		}

		userID := f.UserID
		if userID == nil {
			userID = req.UserID
		}
		if userID == nil {
			return ErrUserRequired
		}
		// I-F7：已有归属会话时不允许换一个（能换就等于能劫持他人的流）。
		if f.SessionID != nil && req.SessionID != nil && *f.SessionID != *req.SessionID {
			return ErrSessionImmutable
		}

		var consumedAt time.Time
		row := tx.WithContext(ctx).Raw(
			`UPDATE flows
			    SET status = 'completed',
			        consumed_at = now(),
			        user_id = $2,
			        session_id = COALESCE($3, session_id)
			  WHERE id = $1 AND status = 'active'
			 RETURNING consumed_at`, req.FlowID, *userID, req.SessionID).Row()
		if err := row.Scan(&consumedAt); err != nil {
			// 没有返回行 = 这行已被别的事务消费（或清理器删了）——按"流已不在"处理（I-F4）。
			return ErrFlowConsumed
		}
		f.Status = string(flow.StatusCompleted)
		f.ConsumedAt = &consumedAt
		f.UserID = userID
		if req.SessionID != nil {
			f.SessionID = req.SessionID
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if bizErr != nil {
		return out, bizErr
	}
	return out, nil
}

// backfillSession 是 I-F7 的「completed 行一次性回填 session_id」。
//
// 只在 `status='completed' AND session_id IS NULL` 时生效；条件写在 SQL 的 WHERE 里，
// 保证并发下也只有一次成功（另一路拿不到行 → `ErrFlowConsumed`）。
func (s *Service) backfillSession(ctx context.Context, tx *gorm.DB, f *model.Flow, req ConsumeRequest) error {
	if req.SessionID == nil {
		return ErrFlowConsumed
	}
	if f.SessionID != nil {
		// 已回填过：同值幂等（重试友好），异值按 I-F7 拒绝。
		if *f.SessionID == *req.SessionID {
			return ErrFlowConsumed
		}
		return ErrSessionImmutable
	}
	res := tx.WithContext(ctx).Exec(
		`UPDATE flows SET session_id = $2
		  WHERE id = $1 AND status = 'completed' AND session_id IS NULL`, f.ID, *req.SessionID)
	if res.Error != nil {
		return store.MapError(res.Error)
	}
	if res.RowsAffected != 1 {
		return ErrFlowConsumed
	}
	f.SessionID = req.SessionID
	return nil
}

// stepsToFlow 把库形态（`model.StepList`）转成规则层形态（`[]flow.CompletedStep`）。
//
// 时刻用 RFC3339Nano：jsonb 里的字符串必须能原样解析回来，否则规则层的时间语义会漂。
func stepsToFlow(list model.StepList) []flow.CompletedStep {
	out := make([]flow.CompletedStep, 0, len(list))
	for _, s := range list {
		out = append(out, flow.CompletedStep{
			Factor:      s.Factor,
			ChallengeID: s.ChallengeID,
			At:          s.At.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}

// stepsFromFlow 把规则层形态转回库形态。解析失败即报错——不吞掉、不猜时间。
func stepsFromFlow(list []flow.CompletedStep) (model.StepList, error) {
	out := make(model.StepList, 0, len(list))
	for _, s := range list {
		at, err := time.Parse(time.RFC3339Nano, s.At)
		if err != nil {
			return nil, err
		}
		out = append(out, model.Step{
			Factor:      s.Factor,
			ChallengeID: s.ChallengeID,
			At:          at.UTC(),
		})
	}
	return out, nil
}
