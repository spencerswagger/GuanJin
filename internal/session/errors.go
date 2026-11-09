package session

import (
	"errors"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本包对外暴露的**哨兵错误**。HTTP 层只认这些哨兵，不认底层存储错误：
// 码表只有一处（httpx），映射也只写一处（下面的 CodeOf），handler 里不再出现字符串码。
var (
	// ErrNilDependency 表示装配期缺依赖（fail-fast，不允许"缺了就当 nil 用"）。
	ErrNilDependency = errors.New("session: nil dependency")
	// ErrSessionNotFound 会话不存在（`SESSION_NOT_FOUND`，E3/404）。
	ErrSessionNotFound = errors.New("session: not found")
	// ErrSessionRevoked 会话已被撤销（`SESSION_REVOKED`，E3/403，D3-E）。
	ErrSessionRevoked = errors.New("session: revoked")
	// ErrSessionExpired 会话已过期（空闲超时或绝对超时）。
	//
	// 对外与令牌过期同码 `TOKEN_EXPIRED`（E2/401）：客户端的处置完全一样（重新认证），
	// 分成两个码只会让接入方多写一个分支、且无法据此做任何不同的事。
	ErrSessionExpired = errors.New("session: expired")
	// ErrSessionNotOwned 会话不属于当前主体（`SESSION_NOT_OWNED`，E4/404）。
	//
	// 用 404 而不是 403：403 会告诉调用方"这个 id 真实存在"，等于把别人的会话 id
	// 变成可探测的。E4 前缀默认区间（404/409/422）里 404 是唯一不泄露存在的选择。
	ErrSessionNotOwned = errors.New("session: not owned by caller")
	// ErrInvalidInput 入参不成立（`INVALID_PARAMETER`，E1/400）。
	ErrInvalidInput = errors.New("session: invalid input")
)

// CodeOf 把本包的哨兵映射成冻结错误码；无法映射返回空串（调用方按 500 处理并留日志）。
//
// 只映射 §5.4 里**已有**的码；并发上限（`deny_new`）的对外码尚未冻结，相关判定归 S6 的创建路径，
// 本包不再持有那个哨兵。
func CodeOf(err error) httpx.Code {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrSessionNotFound), errors.Is(err, store.ErrSessionNotFound):
		return httpx.CodeSessionNotFound
	case errors.Is(err, ErrSessionRevoked):
		return httpx.CodeSessionRevoked
	case errors.Is(err, ErrSessionExpired):
		return httpx.CodeTokenExpired
	case errors.Is(err, ErrSessionNotOwned):
		return httpx.CodeSessionNotOwned
	case errors.Is(err, ErrInvalidInput), errors.Is(err, store.ErrInvalidCursor):
		return httpx.CodeInvalidParam
	case errors.Is(err, store.ErrClientNotFound):
		return httpx.CodeClientNotFound
	case errors.Is(err, store.ErrClientDisabled):
		return httpx.CodeInvalidClient
	// D8-C / AC-FC-02：**数据库不可用 → 503 `DB_UNAVAILABLE`**（不降级、不伪装成 401/500）。
	// 两类来源都要认：
	//   1. `*store.Error` 已经判成 `DB_UNAVAILABLE`（PG 回了连接类 SQLSTATE）；
	//   2. 少数读路径把驱动层的原始错误直接上抛（拨号失败/连接被重置），由 `IsUnavailable` 兜住。
	// 认不出来就会落到 handler 的 default → 500，而判据要的是 503 + `Retry-After`。
	case storeIsDBUnavailable(err):
		return httpx.CodeDBUnavailable
	default:
		return ""
	}
}

// storeIsDBUnavailable 是"这次失败是数据库当时不可用"的唯一判定（见 `internal/store/unavailable.go`）。
func storeIsDBUnavailable(err error) bool {
	var se *store.Error
	if errors.As(err, &se) && se.Code == httpx.CodeDBUnavailable {
		return true
	}
	return store.IsUnavailable(err)
}
