package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是数据面的**断言重放判重**（AC-DP-08：同 `jti` 窗口内第二次 → 401）。
//
// 为什么平台侧也要判（口径以**判据**为准，不是 D16 那句"归 SDK"）：
// `acceptance-criteria.md` 的 AC-DP-08 明确写「集成（真实 Redis）」，`m1-slices.md` §4-S8 把
// `AC-DP-04~08` 归 S8；数据面是这个窗口的**唯一签发方**，由签发方自己守"这份断言只许用一次"
// 成本最低、也最不容易被绕过（SDK 侧的 `GJ-CAP-07` 仍独立存在，两侧互不替代）。
//
// 判据语义（照 AC-DP-08 原话）：
//   - 同一个 `jti` 在**窗口内**（= 断言有效期 `exp`，60s）第二次出现 → **401**，且不转发；
//   - 窗口过期后可再用（`SET NX PX` 的 TTL 到期即释放，不需要后台清理）；
//   - 存储不可用 → **503 `REDIS_UNAVAILABLE`**（fail-closed：无法确认是否重放就拒，
//     D8-C 的 Redis 语义；不许"存不了就当没重放"）。
//
// 为什么判的是**断言 `jti`** 而不是访问令牌：访问令牌是**会话级**凭证，同一令牌在有效期内被
// 正常复用几千次（每个请求一次），按它判重会把正常业务全打成 401；而断言是**逐请求**签发、
// 天然一次性的，它的 `jti` 才是"这一份凭证只能用一次"的载体（与 SDK 侧 GJ-CAP-07 同一对象）。

// ErrReplayGuardMissing 表示没有装配判重存储（启动期就该拦住：没有它就等于没有 AC-DP-08）。
var ErrReplayGuardMissing = errors.New("gateway: replay guard is required (AC-DP-08)")

// ReplayGuard 是"这个 jti 在窗口内是否首次出现"的唯一接缝。
//
// 实现必须在**多副本**下共享同一份状态（生产用 Redis）；`Claim` 必须原子（`SET NX PX`），
// 读-改-写会在并发下漏判——而漏判的正是"同一个断言被两个副本同时放行"。
type ReplayGuard interface {
	// Claim 首次出现返回 (true, nil)；窗口内重复返回 (false, nil)；存储不可用返回 error。
	Claim(ctx context.Context, jti string, window time.Duration) (bool, error)
}

// replayWindow 是判重窗口：等于断言有效期（`exp - iat`）。
//
// 窗口必须**不小于** `exp`（AC-DP-08："窗口过期后可再用"指令牌本身过期之后），
// 取等是最严的：断言一过期，它的 jti 就允许再次出现（现实中不可能，因为 jti 是新 UUID）。
const replayWindowBuffer = 0 * time.Second

// replayWindowFor 返回某次签发应使用的判重窗口。
func replayWindowFor(a Assertion) time.Duration {
	window := a.Exp.Sub(a.Iat) + replayWindowBuffer
	if window <= 0 {
		// 签发出错（exp 早于 iat）时不得退化成"不判重"：给一个下限并报错由调用方处理。
		return AssertionTTL
	}
	return window
}

// checkReplay 判定并返回对外错误（`nil` = 首次出现，可继续转发）。
func (p *Pipeline) checkReplay(ctx context.Context, a Assertion) error {
	if p.replay == nil {
		// 装配期已拦过；运行期再兜一道：宁可 503 也不放行"无法确认是否重放"的请求。
		return &AdmitError{Code: httpx.CodeRedisUnavailable, Status: http.StatusServiceUnavailable,
			Message: "服务暂不可用，请稍后重试", Internal: ErrReplayGuardMissing}
	}
	first, err := p.replay.Claim(ctx, a.JTI, replayWindowFor(a))
	if err != nil {
		return &AdmitError{Code: httpx.CodeRedisUnavailable, Status: http.StatusServiceUnavailable,
			Message:  "服务暂不可用，请稍后重试",
			Internal: fmt.Errorf("replay claim %s: %w", a.JTI, err)}
	}
	if !first {
		return &AdmitError{Code: httpx.CodeTokenInvalid, Status: http.StatusUnauthorized,
			Message:  "令牌无效或已过期",
			Internal: fmt.Errorf("assertion jti %s replayed within %s", a.JTI, replayWindowFor(a))}
	}
	return nil
}
