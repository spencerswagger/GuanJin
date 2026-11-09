package store

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
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

	// 4) 套接字层：拨号被拒、连接被重置、写失败。
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}

	// 5) 连接在协议中途断开（PG 进程被 kill 时很常见）。
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	return false
}
