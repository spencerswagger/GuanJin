package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// 本文件回答一个问题：**这个错误是不是"数据库当时不可用"**（D8-C / AC-FC-01·02）。
//
// ## 为什么需要单独判
//
// PG 真被停掉时，客户端拿到的往往**不是** `*pgconn.PgError`——那要求服务端还能回一个
// 错误包。进程不在监听时是 `*pgconn.ConnectError`（拨号失败），连接被掐断时是
// `*net.OpError` / `io.EOF` / `driver.ErrBadConn`。`MapError` 原来只翻译
// `*pgconn.PgError`，这类错误被**原样上抛**，HTTP 层认不出来只能回 500，
// 而判据要的是 **503 + `DB_UNAVAILABLE` + `Retry-After`**（`AC-FC-02`）。
//
// ## 边界（宁缺勿滥）
//
// 只认"连接 / 会话层"的失败，**不认业务错误**：约束冲突（23505/23514/23503）、
// NOT NULL、类型不合法、schema 漂移都不算——否则所有 500 都会被伪装成 503，
// 运维会去查数据库，而真正的原因在代码里。
//
// 也不认 `context.Canceled`：那是调用方主动取消（客户端断开），不是数据库挂了；
// 把取消报成"数据库不可用"会让 `/readyz` 与告警出现假阳性。
//
// `context.DeadlineExceeded` **同样不认**（如实记一笔取舍）：数据库卡住（网络分区）时
// 查询会以它失败，理论上也属"不可用"；但它同时是"这条查询超过了我们给的上限"的信号，
// 可能是慢查询/缺索引这类缺陷。区分这两者需要一条判据（例如按 DSN 级拨号超时判定），
// 目前没有，故**不映射**——`AC-FC-02` 的真实停库场景走的是拨号失败，已被覆盖。
//
// **超时一律不算"依赖不可用"**（2026-11-10 收窄，评审 Gate 报的窄缺口）：判据原文说
// 「超时/停顿不要把真实故障掩盖成瞬时问题」——客户端看到 5xx 会退避重试，看到 503 也会
// 重试，但**把所有超时都说成"库挂了"会让告警淹没**：慢查询与真停库会被报成同一件事，
// 运维就失去了"是查库还是查代码"的分叉。故 `net.OpError` 分支显式排除 timeout
// （`Timeout()==true` / `os.ErrDeadlineExceeded` / `syscall.ETIMEDOUT`），
// 只保留**硬失败**：`ECONNREFUSED` / `ECONNRESET` / `EPIPE` / `ECONNABORTED`。
//
// 为什么这条收窄在实测下不会漏掉真故障（Gate 在 main 上复跑确认过）：
//   - 慢查询走服务端取消 → `*pgconn.PgError` **57014**（不进本分支）；
//   - 慢查询撞客户端 deadline → `*pgconn.errTimeout`（`pgx` 自己的超时类型，也不进本分支）；
//   - 真停库走拨号被拒 → `*pgconn.ConnectError`（分支 1），或 `ECONNREFUSED`（本分支的硬失败）。
//
// 唯一代价：**拨号被网络黑洞吞掉（dial timeout）会落 500 而不是 503**——这是本收窄的已知
// 取舍，若日后按 `Op == "dial"` 放行 dial 超时，应先补一条真黑洞用例再改（别只改判断）。
//
// ## 谁用它
//
//   - `MapError`：把连接层失败统一折成 `*Error{Code: DB_UNAVAILABLE}`（仓储出口一致）；
//   - `internal/flow` / `internal/session` 的 `CodeOf`：兜住"没经过 `MapError` 的原始错误"
//     （少数读路径直接把驱动错误上抛），避免这些端点落到 500。
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}

	// 1) 拨号失败：PG 进程/端口不通（AC-FC-02 的真实停库就是这个）。
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return true
	}

	// 2) 服务端还能回错误包，但回的是"连接类"SQLSTATE：
	//    08*（connection exception）、57P01 admin_shutdown、57P02 crash_shutdown、
	//    57P03 cannot_connect_now、53300 too_many_connections。
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlStateConnectionFailure, sqlStateTooManyConnections,
			sqlStateAdminShutdown, sqlStateCrashShutdown, sqlStateCannotConnectNow,
			sqlStateProtocolViolation, sqlStateSQLClientUnableToEstablish:
			return true
		}
		return false
	}

	// 3) 连接已被驱动层判定为坏/已关闭（连接池内的陈旧连接）。
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return true
	}

	// 4) 套接字层：**只认硬失败**（拨号被拒、连接被重置、写失败、连接被中止）。
	//
	// 超时（socket read/dial deadline、`os.ErrDeadlineExceeded`、`syscall.ETIMEDOUT`）
	// **不算**依赖不可用：慢查询/网络停顿以超时暴露时，说成"库挂了"会让客户端把它当瞬时
	// 故障退避重试，反而掩盖真问题（判据原话，见文件头）。注意检查顺序：先判超时再判
	// 具体 errno，因为 `net.OpError.Timeout()` 对 `ETIMEDOUT` 也为 true。
	if isTimeoutError(err) {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// 到这里已经排除超时：剩下的 `net.OpError` 就是"连接不上 / 被断"。
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNABORTED) {
		return true
	}

	// 5) 连接在协议中途断开（PG 进程被 kill 时很常见）。
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	return false
}

// isTimeoutError 报告 err 是否属于"超时"（**不是**依赖不可用）。
//
// 三类都要认，少认一类就等于留下一个误判口子：
//   - `net.Error.Timeout()`：`*net.OpError` / `*net.DNSError` 自己就实现了它，覆盖面最广；
//   - `os.ErrDeadlineExceeded` / `context.DeadlineExceeded`：deadline 的两种标准哨兵
//     （`os.ErrDeadlineExceeded` 与 `context.DeadlineExceeded` 在 net 包内部同一语义）；
//   - `syscall.ETIMEDOUT` 与字符串 `i/o timeout`：少数驱动/中间层只把 errno 或文本带上来
//     （没有实现 `Timeout()` 的自定义错误）。文本兜底是最后一道，**只匹配这一种固定措辞**，
//     不做宽泛的子串猜测。
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	return strings.Contains(err.Error(), "i/o timeout")
}
