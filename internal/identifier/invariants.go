package identifier

import (
	"errors"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 不变式（数据模型 §4.1 的 I1~I6）。这里只放**规则**，读数是调用方给的。
//
// 为什么规则与读数分开：同一条不变式在"解绑标识符"和"撤销认证器"两个入口都要判，
// 但两处的 SQL 不同。把"数几个"写进规则里，就会出现两份计数口径（迟早不一致）。
var (
	// ErrLastVerifiedIdentifier 对应 I1：账户必须保有 ≥1 个已验证标识符。
	ErrLastVerifiedIdentifier = errors.New("identifier: refusing to leave zero verified identifiers (I1)")
	// ErrLastIdentifier 对应 I2：不能解绑最后一个标识符。
	ErrLastIdentifier = errors.New("identifier: refusing to unbind the last identifier (I2)")
	// ErrLastAuthenticator 对应 I3/I4：可变之后账户必须仍有 ≥1 种可用认证能力。
	ErrLastAuthenticator = errors.New("identifier: refusing to leave zero usable auth methods (I3/I4)")
	// ErrLastPrimary 对应 I6：不能把最后一个主标识符降级。
	//
	// D3-B：I6 **不设对外错误码**（V1 没有"设主标识符"端点），撞到即内部缺陷 → 500。
	ErrLastPrimary = errors.New("identifier: refusing to demote the last primary identifier (I6)")
	// ErrIdentifierNotVerified 表示要拿一个未验证标识符当登录方式/找回通道。
	ErrIdentifierNotVerified = errors.New("identifier: identifier is not verified")
)

// Snapshot 是判定不变式所需的**全部读数**（一次事务内取，别分两次查）。
//
// 字段口径：
//   - Total：该用户**可用**（status='active'）标识符数。已解绑的行是墓碑
//     （仍占 `(type,value)` 唯一约束、留着审计），**不计入** —— 否则解一条还剩一堆墓碑，
//     I2 永远不会触发；
//   - Verified：`verified AND status='active'`；
//   - VerifiedAuthable：`verified AND status='active' AND type IN (phone,email)`；
//   - UsableHolding：可用持有型认证器（active + 未锁定 + 未过期）；
//   - Primary：可用主标识符计数（`is_primary AND status='active'`）。
type Snapshot struct {
	Total            int64
	Verified         int64
	VerifiedAuthable int64
	UsableHolding    int64
	Primary          int64
}

// UsableAuthMethods 返回"可用认证能力"的数量 = 持有型认证器 ∪ 已验证的认证型标识符。
//
// 为什么不取"二者相加"：一个号码既是登录方式又是某认证器的载体时会被数两次，
// 而这条数的用途是"是否为 0"。并集语义按数据模型 §4.2 的定义实现。
func (s Snapshot) UsableAuthMethods() int64 {
	if s.UsableHolding > 0 || s.VerifiedAuthable > 0 {
		return 1
	}
	return 0
}

// CheckUnbind 判定"解绑某个标识符"是否允许（I1 + I2）。
//
// `verified`/`active` 描述的是**被解绑的那一条**：解绑一条未验证的标识符不会让
// I1 变差（它本来就不在分母里），所以只减它确实占的那一份。
func CheckUnbind(s Snapshot, verified, active bool) error {
	afterTotal := s.Total - 1
	if active && afterTotal < 1 {
		return ErrLastIdentifier
	}
	if active && verified {
		afterVerified := s.Verified - 1
		if afterVerified < 1 {
			return ErrLastVerifiedIdentifier
		}
	}
	return nil
}

// CheckRemoveAuthenticator 判定"撤销/删除一个持有型认证器"是否允许（I3/I4）。
//
// 用并集口径：允许删掉最后一个持有型认证器，**只要**账户还有已验证的认证型标识符
// （手机+短信仍能登录）。这就是"信道型用户不该被逼着凭空多一个 holding 凭据"的落地。
func CheckRemoveAuthenticator(s Snapshot) error {
	after := s.UsableHolding - 1
	if after < 0 {
		after = 0
	}
	if after == 0 && s.VerifiedAuthable == 0 {
		return ErrLastAuthenticator
	}
	return nil
}

// CheckDemotePrimary 判定"取消主标识符"是否允许（I6）。
func CheckDemotePrimary(s Snapshot) error {
	if s.Primary-1 < 1 {
		return ErrLastPrimary
	}
	return nil
}

// CheckUsableAsLoginMethod 判定某标识符能否当登录方式/找回通道（必须是已验证的认证型）。
//
// `authable` 由调用方按类型判断后传入（phone/email = true）——
// **不在本包内按字符串类型分支**，类型白名单只有一处（见 store 的 `CountVerifiedAuthable`）。
func CheckUsableAsLoginMethod(verified, active, authable bool) error {
	if !active || !authable {
		return ErrIdentifierNotVerified
	}
	if !verified {
		return ErrIdentifierNotVerified
	}
	return nil
}

// CodeOf 把不变式错误映射成对外错误码（接口 §5.4 / D3-B）。
//
// I6 故意**没有**映射：它不该到达 HTTP 层（没有对应端点），出现了就是缺陷。
func CodeOf(err error) (httpx.Code, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, ErrLastIdentifier), errors.Is(err, ErrLastVerifiedIdentifier):
		return httpx.CodeLastIdentifierCannotUnbind, true
	case errors.Is(err, ErrLastAuthenticator):
		return httpx.CodeLastAuthenticatorCannotRemove, true
	case errors.Is(err, ErrIdentifierNotVerified):
		return httpx.CodeIdentifierNotVerified, true
	default:
		return "", false
	}
}
