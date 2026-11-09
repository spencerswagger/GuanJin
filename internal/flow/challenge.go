package flow

import (
	"errors"
	"fmt"
	"time"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是 **challenge 状态机**（`challenges` 表的一次性/尝试次数/过期语义）。
//
// 边界（D3-D 与 D5 的交界处，最容易搞混）：
//   - 这里只管**单个挑战**：它过期没、被消费没、试了几次；
//   - **账号级失败计数与锁定不在这里**：那是 Redis 权威计数（`login:fail:{user_id}`），
//     按 `user_id` 全局共享，`challenges.attempts` 永远不是它的替代品；
//   - 挑战状态的**权威落库**（Redis 只承载限流计数与热缓存），所以这里的判定
//     输入必须是"从库里读出来的那一行"，不是缓存里的副本。
var (
	// ErrChallengeNotFound 表示挑战不存在（`CHALLENGE_INVALID`，E2/401）。
	ErrChallengeNotFound = errors.New("flow: challenge not found")
	// ErrChallengeExpired 表示挑战过期（`CHALLENGE_EXPIRED`，E2/401）。
	ErrChallengeExpired = errors.New("flow: challenge expired")
	// ErrChallengeConsumed 表示挑战已被消费（`CHALLENGE_CONSUMED`，E2/401）。
	//
	// 与 `STEP_ALREADY_SATISFIED` 的区别（D3-F 就是因为这个区别才分配新码）：
	// 前者是"这个 challenge_id 用过了，换一个 challenge_id 继续"；
	// 后者是"这一步已经完成，根本不需要再挑战"。
	ErrChallengeConsumed = errors.New("flow: challenge already consumed")
	// ErrAttemptsExceeded 表示该挑战的尝试次数用尽（`ATTEMPTS_EXCEEDED`，E2/401）。
	ErrAttemptsExceeded = errors.New("flow: challenge attempts exceeded")
)

// Challenge 是一次挑战的状态（映射 `challenges` 表里"状态机"用到的那几列）。
type Challenge struct {
	ID string
	// Purpose 六值词表（D2）；**没有别名映射**，构造时就要校验。
	Purpose factor.Purpose
	Factor  factor.FactorType
	// Attempts / MaxAttempts 是**单挑战**计数，与账号级失败计数无关（见文件头）。
	Attempts    int
	MaxAttempts int
	ExpiresAt   time.Time
	ConsumedAt  *time.Time
}

// NewChallenge 构造一个"刚创建的挑战"（调用方负责落库）。
//
// `maxAttempts` 必须显式给（DB CHECK 要求 > 0）：不给就是隐性默认，
// 而"试几次算完"是安全参数，不该由实现随手定。
func NewChallenge(id string, purpose factor.Purpose, f factor.FactorType, maxAttempts int, expiresAt time.Time) (Challenge, error) {
	if id == "" {
		return Challenge{}, fmt.Errorf("flow: empty challenge id")
	}
	if !purpose.Valid() {
		return Challenge{}, fmt.Errorf("%w: %q", factor.ErrUnknownPurpose, string(purpose))
	}
	if f == "" {
		return Challenge{}, fmt.Errorf("flow: empty challenge factor")
	}
	if maxAttempts <= 0 {
		return Challenge{}, fmt.Errorf("flow: max_attempts must be > 0")
	}
	return Challenge{
		ID: id, Purpose: purpose, Factor: f,
		MaxAttempts: maxAttempts, ExpiresAt: expiresAt,
	}, nil
}

// Check 判定"现在能不能拿它做一次校验"。
//
// 顺序有含义：**过期 → 已消费 → 次数用尽**。
// 过期排最前，是因为"过期"对用户最可解释（"验证码超出有效期"比"试太多次"更像事实）。
func (c Challenge) Check(now time.Time) error {
	if c.ID == "" {
		return ErrChallengeNotFound
	}
	if !now.Before(c.ExpiresAt) {
		return ErrChallengeExpired
	}
	if c.ConsumedAt != nil {
		return ErrChallengeConsumed
	}
	if c.Attempts >= c.MaxAttempts {
		return ErrAttemptsExceeded
	}
	return nil
}

// Fail 记一次失败（`attempts + 1`），返回更新后的挑战。
//
// 返回 `exhausted=true` 表示**这一击之后**次数用尽 —— 调用方据此删/停该挑战并回
// `ATTEMPTS_EXCEEDED`，而不是等下一个人再来读一次才知道。
//
// 只做加法、不做"清零"：清零是**账号级**计数在成功登录时做的事，不是单挑战的事。
func (c Challenge) Fail(now time.Time) (next Challenge, exhausted bool) {
	next = c
	next.Attempts = c.Attempts + 1
	if next.Attempts >= next.MaxAttempts {
		return next, true
	}
	return next, false
}

// Consume 一次性消费：把挑战置为已用。
//
// 幂等保护：已消费的再消费返回 `ErrChallengeConsumed`（而不是"成功但没变化"）——
// 后者会让上层误以为"这次校验有效"。真正的**并发**一次性由 SQL 行级条件更新保证
// （`UPDATE … WHERE consumed_at IS NULL`，见 store 的 `Consume`），这里只表达规则。
func (c Challenge) Consume(now time.Time) (Challenge, error) {
	if err := c.Check(now); err != nil {
		return c, err
	}
	t := now
	next := c
	next.ConsumedAt = &t
	return next, nil
}

// RequiresFlow 报告该 purpose 的挑战是否**必须挂在一条阶梯流上**。
//
// 结论（CH-1 的落点，S5 只做内部能力、不改端点）：`login` / `step_up` 必须有流
// —— 没有流就没有 `achieved_amr` 的落点，也就无法保证"只补缺、不重输"（I-F2/I-F4）。
// `register` / `enroll` / `recover` / `verify_identifier` 是**无流挑战**（一次性动作，
// 不推进阶梯），它们的完成态写在各自的业务行上（用户/标识符/凭据）。
func RequiresFlow(p factor.Purpose) bool {
	switch p {
	case factor.PurposeLogin, factor.PurposeStepUp:
		return true
	default:
		return false
	}
}

// ParsePurpose 严格解析 purpose（D2 六值；`bind`/`stepup` 直接报错）。
//
// 委派给 `factor`：词表**只有一处**，编排层不许再抄一遍。
func ParsePurpose(raw string) (factor.Purpose, error) {
	return factor.ParsePurpose(raw)
}

// CodeOf 把本包的错误映射成对外码（接口 §5.4）。**单一出口**。
//
// 注意 `STEP_ALREADY_SATISFIED` 的常量定义在本文件（`internal/flow`），
// 因为 CI 的禁码扫描只允许它出现在这个包里（D3-F）。
func CodeOf(err error) (httpx.Code, bool) {
	var insufficient *ACRInsufficientError
	switch {
	case err == nil:
		return "", false
	case errors.As(err, &insufficient):
		return httpx.CodeACRInsufficient, true
	case errors.Is(err, ErrStepAlreadySatisfied):
		return CodeStepAlreadySatisfied, true
	case errors.Is(err, ErrChallengeExpired):
		return httpx.CodeChallengeExpired, true
	case errors.Is(err, ErrChallengeConsumed):
		return httpx.CodeChallengeConsumed, true
	case errors.Is(err, ErrAttemptsExceeded):
		return httpx.CodeAttemptsExceeded, true
	case errors.Is(err, ErrChallengeNotFound):
		return httpx.CodeChallengeInvalid, true
	case errors.Is(err, ErrFlowExpired):
		return httpx.CodeFlowExpired, true
	case errors.Is(err, ErrInvalidClient):
		return httpx.CodeInvalidClient, true
	case errors.Is(err, ErrFlowNotFound):
		return httpx.CodeFlowNotFound, true
	case errors.Is(err, ErrFlowIDRequired):
		// CH-1：login/step_up 缺 `flow_id` 属**缺参数**，不是"流不存在"。
		return httpx.CodeMissingParameter, true
	case errors.Is(err, ErrPurposeRequired):
		return httpx.CodeMissingParameter, true
	case errors.Is(err, factor.ErrUnknownPurpose):
		return httpx.CodeInvalidParam, true
	// 因子层（`internal/credential`）的哨兵：不翻会落到 default 变 500，
	// 而 AC-LOGIN-02 要求"凭据不对"必须是 **401 `INVALID_CREDENTIAL`** 且带可复制 trace_id。
	case errors.Is(err, ErrAccountLocked):
		return httpx.CodeAccountLocked, true
	case errors.Is(err, ErrRiskUnavailable):
		return httpx.CodeRedisUnavailable, true
	case errors.Is(err, credential.ErrInvalidCredential):
		return httpx.CodeInvalidCredential, true
	case errors.Is(err, credential.ErrTemporalCodeReused):
		return httpx.CodeTemporalCodeReused, true
	case errors.Is(err, ErrFactorDisabled):
		return httpx.CodeFactorDisabled, true
	case errors.Is(err, ErrFactorNotSupported):
		return httpx.CodeFactorNotSupported, true
	case errors.Is(err, ErrNoEligibleFactor):
		return httpx.CodeNoEligibleFactor, true
	// D8-C / AC-FC-02：**数据库不可用 → 503 `DB_UNAVAILABLE`**（fail-closed，不降级放行）。
	//
	// 两类来源：① 仓储已判成 `DB_UNAVAILABLE` 的 `*store.Error`；② 少数读路径把驱动层
	// 原始错误（拨号失败/连接被重置）直接上抛。以前这两类都落到 handler 的 default → 500，
	// 而判据要的是 **503 + `Retry-After`**。
	//
	// 这里只 import `internal/store` 的**纯判定函数**（`IsUnavailable`）与错误载体：
	// 编排层仍然只通过窄接口（Reader/Starter/…）调用仓储，不碰任何仓储实现。
	case storeIsDBUnavailable(err):
		return httpx.CodeDBUnavailable, true
	default:
		return "", false
	}
}

// storeIsDBUnavailable 是"这次失败是数据库当时不可用"的唯一判定。
//
// 顺序有意义：先看仓储有没有**已经判定**（`*store.Error.Code`），再用连接层判定兜底
// （原始驱动错误没经过 `MapError` 时只有第二条能认出来）。
func storeIsDBUnavailable(err error) bool {
	var se *store.Error
	if errors.As(err, &se) && se.Code == httpx.CodeDBUnavailable {
		return true
	}
	return store.IsUnavailable(err)
}

// CodeStepAlreadySatisfied 是 D3-F 追加的码（E4 / 409）。
//
// 定义在 `internal/flow` 而不是 httpx：CI 的禁码扫描规定这个字符串只能出现在本包
// （`scripts/check-forbidden-error-codes.sh` 第二条），这样"谁返回它"在代码上是唯一的。
const CodeStepAlreadySatisfied httpx.Code = "STEP_ALREADY_SATISFIED"
