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
// `context.DeadlineExceeded` **不认**：它既可能是"库卡住"，也可能是"慢查询/缺索引"（那是缺陷）；
// 真实停库走的是拨号失败（`*pgconn.ConnectError`，分支 1），已被覆盖。
//
// ## 超时到底算不算"依赖不可用"（2026-11-10 评审实测收口，口径到此为止）
//
// **结论（照此执行，别再翻转）**：**拨号超时视为依赖不可用**（否则真故障会被伪装成 500）；
// **仅在非 dial 的读写超时上不视为不可用**（那是慢查询/网络停顿，落 500 或调用方语义）。
// 一句话：**"连不上"始终算，超时不等于依赖不可用。**
//
//   - **算**（→ 503 `DB_UNAVAILABLE`）：拨号失败/拨号超时（`*pgconn.ConnectError`，或
//     `*net.OpError{Op:"dial"}` 带 `Timeout()`）、`ECONNREFUSED`/`ECONNRESET`/`EPIPE`/
//     `ECONNABORTED`、连接中途断开（`io.EOF`）、驱动判坏（`ErrBadConn`/`ErrConnDone`）。
//     拨号超时之所以仍要算：防火墙 DROP / 主机不存在 / 路由丢包时拨号会一直挂到超时，
//     那就是"库连不上"，落 500 会让客户端不退避、把真故障伪装成应用 bug。
//   - **不算**（→ 未映射，500）：**读写超时**（`net.OpError.Timeout() && Op != "dial"`）。
//     评审实测造出过真实场景：自定义 `DialFunc` 在握手后开 300ms socket 读超时再跑
//     `select pg_sleep(2)` → `*pgconn.errTimeout` 包 `*net.OpError{op=read, timeout:true}`
//     → 那是**慢查询**被说成"库挂了"，属判据级误判（`AC-FC-01/02` 不能靠误判达标）。
//   - 另外两条慢查询路径本就绕开 `net.OpError`：服务端 `statement_timeout` 走
//     `*pgconn.PgError` **57014**、客户端 ctx deadline 走 `*pgconn.errTimeout`，都不映射。
//
// 用例 `TestIsUnavailablePinsTimeoutSemantics` 把这条口径钉死（成对：读写超时 false、
// 拨号超时 + `ECONNREFUSED` + `ConnectError` true），`unavailable_db_test.go` 用真库复验。
//
// **裸 `syscall.ETIMEDOUT` 被判 false 是有意的、不是缺口**（评审 2026-11-10 确认可接受）：
// pgx 的拨号错误必被 `*pgconn.ConnectError` 包住、读写错误必是 `*net.OpError`，
// 裸 errno 到不了这个函数；真有裸 errno 进来，说明上游把错误链拆了，那是上游的缺陷，
// 不该在这里靠"猜 errno"补救。
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

	// 4) 套接字层：**只有"连不上"算依赖不可用**。
	//
	// 判据（2026-11-10 评审实测收口，别再翻转）：
	//   - **拨号超时仍算不可用**（`Op == "dial"`）：防火墙 DROP / 主机不存在 / 路由丢包时，
	//     拨号会一直挂着直到超时，那就是"库连不上"，必须给 503 让客户端退避——把它降级成 500
	//     等于把真故障伪装成应用 bug；
	//   - **读写超时不算**（`Timeout()==true && Op != "dial"`）：评审用自定义 `DialFunc` 在
	//     握手后开 300ms socket 读超时跑 `select pg_sleep(2)`，错误链是 `*pgconn.errTimeout`
	//     包 `*net.OpError{op=read, timeout:true}` —— 那是**慢查询**，不是库挂了。
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() && opErr.Op != "dial" {
			return false
		}
		return true
	}
	// 只留"连接被拒/被断/写失败/被中止"这几个硬 errno；**`ETIMEDOUT` 已移除**（超时类，
	// 语义见上：读写超时不算不可用；拨号超时由上面的 `Op == "dial"` 分支兜住）。
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
