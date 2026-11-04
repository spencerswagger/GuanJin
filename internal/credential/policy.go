package credential

import (
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// Policy 是口令策略（取值来自 `login_policies`：A 组"凭据要求"）。
//
// 为什么把它做成显式结构而不是"从 DB 里现取"：策略判定必须是**纯函数**，
// 才能被 AC-REG-10 那样的"三码同时命中"用例稳定复现；DB 读取在调用方（S4b）。
type Policy struct {
	// MinLength 最小长度（`password_min_length`，DB CHECK 8..128）。
	MinLength int
	// MaxLength 最大长度；0 表示"只受 MaxPasswordBytes 硬上限约束"。
	MaxLength int
	// CharClasses 要求覆盖的字符类数量（`password_char_classes`，2..4）：
	// 小写 / 大写 / 数字 / 符号。
	CharClasses int
	// HistoryCount 历史口令不可复用的条数（`password_history_count`，0 = 不查历史）。
	HistoryCount int
	// MaxAgeDays 口令最长有效期（`password_max_age_days`，0 = 不判过期）。
	MaxAgeDays int
	// WeakPasswords 自定义弱口令名单；nil = 用内置表，非 nil = **完全替换**内置表。
	WeakPasswords []string
}

// Validate 检查策略取值本身是否合法（配置错误走 500，不是用户错）。
func (p Policy) Validate() error {
	if p.MinLength < 8 {
		return wrapf(ErrInvalidPolicy, "min_length %d < 8（login_policies CHECK）", p.MinLength)
	}
	if p.MinLength > MaxPasswordBytes {
		return wrapf(ErrInvalidPolicy, "min_length %d > %d", p.MinLength, MaxPasswordBytes)
	}
	if p.MaxLength != 0 {
		if p.MaxLength < p.MinLength {
			return wrapf(ErrInvalidPolicy, "max_length %d < min_length %d", p.MaxLength, p.MinLength)
		}
		if p.MaxLength > MaxPasswordBytes {
			return wrapf(ErrInvalidPolicy, "max_length %d > %d", p.MaxLength, MaxPasswordBytes)
		}
	}
	if p.CharClasses < 2 || p.CharClasses > 4 {
		// 与 `login_policies.password_char_classes` 的 DB CHECK（2..4）同源。
		// 允许 1 会在库里存不进、在内存里跑得动——两套口径。
		return wrapf(ErrInvalidPolicy, "char_classes %d out of range 2..4", p.CharClasses)
	}
	if p.HistoryCount < 0 {
		return wrapf(ErrInvalidPolicy, "history_count %d < 0", p.HistoryCount)
	}
	if p.MaxAgeDays < 0 {
		return wrapf(ErrInvalidPolicy, "max_age_days %d < 0", p.MaxAgeDays)
	}
	return nil
}

// ViolationKind 是命中项所属的判定组（顺序即 AC-REG-10 的冻结顺序）。
type ViolationKind string

const (
	// ViolationWeak 弱口令（黑名单）。
	ViolationWeak ViolationKind = "weak"
	// ViolationReused 与历史口令重复。
	ViolationReused ViolationKind = "reused"
	// ViolationPolicy 不满足长度/字符类等策略。
	ViolationPolicy ViolationKind = "policy"
)

// Violation 是一条命中项。
//
// `Code` 是对外错误码，`Rule` 是**可定位到具体规则**的稳定标识
// （AC-REG-10 要求 details.violations[] 每一项都能落到规则上），
// `Message` 面向人、且不含口令内容本身。
type Violation struct {
	Kind    ViolationKind `json:"-"`
	Code    httpx.Code    `json:"code"`
	Rule    string        `json:"rule"`
	Message string        `json:"message"`
}

// Result 是一次策略判定的完整结果。
//
// `Code` = **首个**命中项的错误码（AC-REG-10：三码同时命中只回首个）；
// `Violations` = 全量命中项，顺序固定 weak → reused → policy。
type Result struct {
	Code       httpx.Code  `json:"code,omitempty"`
	Violations []Violation `json:"violations,omitempty"`
}

// OK 报告是否没有任何命中项（口令可接受）。
func (r Result) OK() bool { return len(r.Violations) == 0 }

// EvaluateInput 是判定的输入。
type EvaluateInput struct {
	// Password 待判定的新口令（明文，调用方负责不落日志）。
	Password string
	// History 该用户的历史口令信封，**从新到旧**；只会用到前 `HistoryCount` 条。
	History []PasswordEnvelope
}

// Evaluate 按冻结顺序判定，返回"首个码 + 全量命中项"。
//
// **性能提示（必须遵守）**：历史复用检测会对每条历史信封跑一次 PBKDF2-SM3
// （100k 迭代）。因此本函数只允许出现在**低频**路径——注册、改密、重置；
// **禁止**放在登录校验里（那是 200 QPS 的热路径，且登录只需要 Verify 单条信封）。
func (p Policy) Evaluate(in EvaluateInput) (Result, error) {
	if err := p.Validate(); err != nil {
		return Result{}, err
	}

	var out []Violation

	// ① 弱口令（WEAK_PASSWORD）
	if isWeakPassword(in.Password, p.WeakPasswords) {
		out = append(out, Violation{
			Kind:    ViolationWeak,
			Code:    httpx.CodeWeakPassword,
			Rule:    "weak.common_password",
			Message: "该口令在常见弱口令名单中",
		})
	}

	// ② 历史复用（PASSWORD_REUSED）
	if p.HistoryCount > 0 && len(in.History) > 0 {
		limit := p.HistoryCount
		if limit > len(in.History) {
			limit = len(in.History)
		}
		for i := 0; i < limit; i++ {
			same, err := in.History[i].Verify(in.Password)
			if err != nil {
				// 历史信封坏了就如实报错：跳过坏信封等于让复用检测静默少查一条。
				return Result{}, err
			}
			if same {
				out = append(out, Violation{
					Kind:    ViolationReused,
					Code:    httpx.CodePasswordReused,
					Rule:    fmt.Sprintf("reused.history[%d]", i),
					Message: fmt.Sprintf("该口令是最近 %d 次内使用过的口令", p.HistoryCount),
				})
				break // 复用命中一次就够：再来几条也是同一个码、同一个结论
			}
		}
	}

	// ③ 策略不符（PASSWORD_POLICY_VIOLATION）——**逐条全量收集**，不短路。
	// 顺序固定：min_length → max_length → char_classes（与 Rule 名的字典序一致，便于核对）。
	if n := utf8.RuneCountInString(in.Password); n < p.MinLength {
		out = append(out, Violation{
			Kind:    ViolationPolicy,
			Code:    httpx.CodePasswordPolicyViolation,
			Rule:    "policy.min_length",
			Message: fmt.Sprintf("口令长度不得少于 %d 个字符", p.MinLength),
		})
	}
	maxLen := p.MaxLength
	if maxLen == 0 {
		maxLen = MaxPasswordBytes
	}
	if len(in.Password) > maxLen {
		out = append(out, Violation{
			Kind:    ViolationPolicy,
			Code:    httpx.CodePasswordPolicyViolation,
			Rule:    "policy.max_length",
			Message: fmt.Sprintf("口令长度不得超过 %d 字节", maxLen),
		})
	}
	if got := charClassCount(in.Password); got < p.CharClasses {
		out = append(out, Violation{
			Kind:    ViolationPolicy,
			Code:    httpx.CodePasswordPolicyViolation,
			Rule:    "policy.char_classes",
			Message: fmt.Sprintf("口令需覆盖 %d 类字符（当前 %d 类）", p.CharClasses, got),
		})
	}

	res := Result{Violations: out}
	if len(out) > 0 {
		res.Code = out[0].Code // AC-REG-10：只回首个
	}
	return res, nil
}

// Expired 报告"当前口令是否超过最长有效期"（`password_max_age_days`）。
//
// 返回 false 的两种情况要分清（都用第二个返回值表达）：
//   - 策略不判有效期（MaxAgeDays=0）；
//   - 信封没有 `updated_at`（历史数据）。
//
// 调用方必须据 `bool` 决定是否提示改密，**不能**把"没数据"当成"已过期"。
func (p Policy) Expired(envelope PasswordEnvelope, now time.Time) (expired bool, judged bool, err error) {
	if p.MaxAgeDays <= 0 {
		return false, false, nil
	}
	updatedAt, ok, err := envelope.UpdatedAtTime()
	if err != nil {
		return false, false, err
	}
	if !ok {
		return false, false, nil
	}
	return now.Sub(updatedAt) > time.Duration(p.MaxAgeDays)*24*time.Hour, true, nil
}

// charClassCount 统计口令覆盖的字符类数量（小写/大写/数字/符号）。
func charClassCount(password string) int {
	var lower, upper, digit, other bool
	for _, r := range password {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	n := 0
	for _, hit := range []bool{lower, upper, digit, other} {
		if hit {
			n++
		}
	}
	return n
}
