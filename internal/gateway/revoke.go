package gateway

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是数据面的**实时撤销判定**（S8 的 ⑤，AC-SESS-01/04）：会话是否还能用，**每次请求现判**。
//
// 三条纪律（缺一条就等于撤销不实时）：
//  1. **零缓存**：判据只来自"本次请求现读的会话行"，进程内不存任何 `sid → 状态` 映射。
//     加一层 TTL 缓存等于给撤销开一个窗口，而 `AC-SESS-01` 要求"撤销后**首个**请求即 403"。
//  2. **状态 + 时间双判**：`status != active` 只是其中一半；空闲/绝对超时到点但后台还没把它扫成
//     `expired` 时，数据面也必须按失效处理（后台任务只负责落状态，不负责把关）。
//  3. **不区分"不存在"与"已撤销"**：对外一律 403 `SESSION_REVOKED`，避免拿状态码枚举会话。
//
// 多副本一致性（AC-SESS-04）由第 1 条直接推出：2×`guanjind` 撤的是同一张表的同一行，
// 2×`guanjingw` 各自现读同一张表，故**不依赖会话粘滞**，也不需要副本间广播。

// checkSessionAlive 判定会话此刻是否可用；不可用时返回**已带对外码**的 `*AdmitError`。
func checkSessionAlive(s SessionSnapshot, now time.Time) *AdmitError {
	switch s.Status {
	case SessionActive:
		// 正常路径：继续判时间。
	case SessionRevoked:
		return &AdmitError{Code: httpx.CodeSessionRevoked, Status: http.StatusForbidden,
			Message:  "会话已失效，请重新登录",
			Internal: fmt.Errorf("session %s is revoked", s.ID)}
	case SessionExpired:
		return &AdmitError{Code: httpx.CodeSessionRevoked, Status: http.StatusForbidden,
			Message:  "会话已失效，请重新登录",
			Internal: fmt.Errorf("session %s is expired", s.ID)}
	default:
		// 未知状态 = schema/数据缺陷。**不当作 active**（fail-closed），按依赖错误上报，
		// 以免一个新状态值把"已失效"悄悄放行。
		return &AdmitError{Code: httpx.CodeDBUnavailable, Status: http.StatusServiceUnavailable,
			Message:  "服务暂不可用，请稍后重试",
			Internal: fmt.Errorf("session %s has unknown status %q", s.ID, s.Status)}
	}

	if !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt) {
		return &AdmitError{Code: httpx.CodeSessionRevoked, Status: http.StatusForbidden,
			Message:  "会话已失效，请重新登录",
			Internal: fmt.Errorf("session %s reached absolute expiry %s", s.ID, s.ExpiresAt.UTC())}
	}
	if !s.IdleExpiresAt.IsZero() && !now.Before(s.IdleExpiresAt) {
		return &AdmitError{Code: httpx.CodeSessionRevoked, Status: http.StatusForbidden,
			Message:  "会话已失效，请重新登录",
			Internal: fmt.Errorf("session %s reached idle expiry %s", s.ID, s.IdleExpiresAt.UTC())}
	}
	return nil
}

// 边界：撤销判定**只读**会话行，本包没有任何"撤销"写路径——撤销由 S7
// （`internal/session` 的管理器 + 管理面）负责，数据面只消费其结果。
// 这条边界写在这里，是因为"顺手在网关里加个 kick"会造成两套撤销审计。
