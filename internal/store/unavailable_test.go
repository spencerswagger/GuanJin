package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件钉住 **D8-C / AC-FC-01·02 的判定边界**：哪些错误算"数据库当时不可用"（→ 503
// `DB_UNAVAILABLE`），哪些**不算**（业务错误必须保持自己的码，缺陷必须留成 500）。
//
// 这里不用 mock：拨号失败是**真连一个没人监听的端口**（`127.0.0.1:1`）拿到的真错误。

// deadAddr 是一个**必定没有监听**的本机端口（1 号端口不会给普通服务用）。
const deadAddr = "127.0.0.1:1"

// 真拨号失败 → `*pgconn.ConnectError`（AC-FC-02 的停库现场就是这样），且 `MapError`
// 必须把它翻成 `DB_UNAVAILABLE`/503，而不是原样上抛（上抛就是 500）。
func TestRealConnectFailureIsDBUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, "postgres://guanjin_test:x@"+deadAddr+"/guanjin_test?sslmode=disable")
	if err == nil {
		_ = conn.Close(ctx)
		t.Fatalf("%s 居然连上了：本用例要求真拨号失败，请改用一个确定没人监听的端口", deadAddr)
	}

	if !IsUnavailable(err) {
		t.Fatalf("真拨号失败必须判为不可用，实际 %T: %v", err, err)
	}

	mapped := MapError(err)
	var se *Error
	if !errors.As(mapped, &se) {
		t.Fatalf("MapError 应返回 *store.Error，实际 %T", mapped)
	}
	if se.Code != httpx.CodeDBUnavailable {
		t.Fatalf("code = %q, want %q", se.Code, httpx.CodeDBUnavailable)
	}
	if se.Status != 503 {
		t.Fatalf("status = %d, want 503", se.Status)
	}
	if httpx.HTTPStatus(se.Code) != 503 {
		t.Fatalf("码表里的 %s 必须映射 503", se.Code)
	}
	// 原始错误必须留在链上（运维要看得到"connect: connection refused"）。
	if !errors.Is(mapped, syscall.ECONNREFUSED) && !errors.As(mapped, new(*pgconn.ConnectError)) {
		t.Fatalf("原始连接错误不能丢，实际 %v", mapped)
	}
}

// SQLSTATE 层面的判定：连接类（08*、57P0x、53300）→ 不可用；业务类一律不算。
func TestIsUnavailableSQLStates(t *testing.T) {
	cases := []struct {
		name string
		code string
		want bool
	}{
		{"08006 connection_failure", "08006", true},
		{"08001 client_unable_to_establish", "08001", true},
		{"08P01 protocol_violation", "08P01", true},
		{"53300 too_many_connections", "53300", true},
		{"57P01 admin_shutdown（停库最常走这条）", "57P01", true},
		{"57P02 crash_shutdown", "57P02", true},
		{"57P03 cannot_connect_now", "57P03", true},
		// 下面这些**不许**算"不可用"：它们是业务错误或缺陷，伪装成 503 会让运维查错方向。
		{"23505 unique_violation", "23505", false},
		{"23514 check_violation", "23514", false},
		{"23503 foreign_key_violation", "23503", false},
		{"42P01 undefined_table（schema 漂移 = 缺陷）", "42P01", false},
		{"40001 serialization_failure（可重试，但不是不可用）", "40001", false},
		// 慢查询被服务端取消：`statement_timeout` 就长这样，**不是**依赖不可用。
		{"57014 query_canceled（statement_timeout）", "57014", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &pgconn.PgError{Code: tc.code}
			if got := IsUnavailable(err); got != tc.want {
				t.Fatalf("IsUnavailable(%s) = %v, want %v", tc.code, got, tc.want)
			}
			// PgError 走 MapError 的结构化分支：SQLSTATE 必须留在 Error 上（排障要按 SQLSTATE 分组）。
			mapped := MapError(err)
			var se *Error
			if !errors.As(mapped, &se) {
				t.Fatalf("MapError 应返回 *store.Error，实际 %T", mapped)
			}
			if se.SQLState != tc.code {
				t.Fatalf("sqlstate = %q, want %q", se.SQLState, tc.code)
			}
			if tc.want && se.Code != httpx.CodeDBUnavailable {
				t.Fatalf("连接类 SQLSTATE 必须映射 DB_UNAVAILABLE，实际 %q", se.Code)
			}
			if !tc.want && se.Code == httpx.CodeDBUnavailable {
				t.Fatalf("%s 不许映射 DB_UNAVAILABLE（业务错误/缺陷要保留自己的语义）", tc.code)
			}
		})
	}
}

// 非 PgError 的连接层失败：套接字/驱动/协议字节流三类。
func TestIsUnavailableConnectionLayer(t *testing.T) {
	dialErr := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"net.OpError(dial refused)", dialErr, true},
		{"包裹后的 net.OpError", &wrappedError{cause: dialErr}, true},
		{"driver.ErrBadConn", driver.ErrBadConn, true},
		{"sql.ErrConnDone", sql.ErrConnDone, true},
		{"io.EOF（进程被 kill 时很常见）", io.EOF, true},
		{"io.ErrUnexpectedEOF", io.ErrUnexpectedEOF, true},
		{"ECONNRESET", syscall.ECONNRESET, true},
		{"EPIPE", syscall.EPIPE, true},
		{"ECONNABORTED", syscall.ECONNABORTED, true},
		// 拨号超时 = 库连不上（防火墙 DROP / 主机不存在 / 路由丢包）→ **仍算**依赖不可用。
		{"net.OpError(dial/ETIMEDOUT) = 库连不上", &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}, true},
		{"裸 syscall.ETIMEDOUT（无 net.OpError 包装）", syscall.ETIMEDOUT, false},
		// 调用方主动取消不是"数据库不可用"（否则告警会被客户端断开刷屏）。
		{"context.Canceled", context.Canceled, false},
		{"context.DeadlineExceeded（取舍见 unavailable.go 文件头）", context.DeadlineExceeded, false},
		{"业务错误", errors.New("some business failure"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUnavailable(tc.err); got != tc.want {
				t.Fatalf("IsUnavailable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsUnavailablePinsTimeoutSemantics 把"超时"的口径**成对**钉死（2026-11-10 定案：保留原用例名与断言，
// 方向由"一律算连接层"改为"只对 dial 放行超时"）。
//
// 一句话：**"连不上"始终算，超时不等于依赖不可用。**
//   - 拨号超时（`Op=="dial"`）：防火墙 DROP / 主机不存在 / 路由丢包时拨号会一直挂到超时，
//     那就是"库连不上" → **必须 503**；降级成 500 会让客户端不退避、把真故障伪装成应用 bug。
//   - 读写超时（`Timeout() && Op != "dial"`）：评审用自定义 `DialFunc` 在握手后开 300ms
//     socket 读超时跑 `select pg_sleep(2)` 真造出来过（`errTimeout` 包
//     `net.OpError{op:read, timeout:true}`）→ 那是慢查询，**不许**说成库挂了。
func TestIsUnavailablePinsTimeoutSemantics(t *testing.T) {
	// ① 反向：读写超时不算依赖不可用（否则慢查询被说成库挂）。
	readTimeout := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	if IsUnavailable(readTimeout) {
		t.Fatal("socket 读超时（慢查询）不得算依赖不可用：会给客户端错误的重试信号")
	}
	writeTimeout := &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded}
	if IsUnavailable(writeTimeout) {
		t.Fatal("socket 写超时同样不得算依赖不可用")
	}
	// 慢查询的两条真实路径：57014 与裸 deadline，都不映射。
	if IsUnavailable(&pgconn.PgError{Code: "57014"}) {
		t.Fatal("57014（statement_timeout）不得算依赖不可用")
	}
	if IsUnavailable(context.DeadlineExceeded) {
		t.Fatal("裸 context.DeadlineExceeded 不得算依赖不可用")
	}

	// ② 正控：**拨号超时仍算**（库连不上）——这条谁都不许砍。
	dialTimeout := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	if !IsUnavailable(dialTimeout) {
		t.Fatal("拨号超时是「库连不上」，必须算依赖不可用（否则客户端不退回重试）")
	}
	// ③ 正控：真停库（拨号被拒）与连接被重置/中止。
	if !IsUnavailable(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}) {
		t.Fatal("ECONNREFUSED 必须算依赖不可用")
	}
	if !IsUnavailable(&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}) {
		t.Fatal("ECONNRESET 必须算依赖不可用")
	}
	if !IsUnavailable(syscall.EPIPE) || !IsUnavailable(syscall.ECONNABORTED) {
		t.Fatal("EPIPE/ECONNABORTED 必须算依赖不可用")
	}
	// ④ 正控：`*pgconn.ConnectError`（真拨号失败）——本包最核心的一条。
	if !IsUnavailable(&pgconn.ConnectError{}) {
		t.Fatal("*pgconn.ConnectError 必须算依赖不可用")
	}
}

// 非连接层的错误过 `MapError` 必须**原样返回**：不许被吞、不许被伪装成 503。
func TestMapErrorPassesThroughNonConnectionErrors(t *testing.T) {
	raw := errors.New("codec blew up")
	if got := MapError(raw); got != raw {
		t.Fatalf("非 PG/连接错误应原样返回，实际 %v", got)
	}
}

type wrappedError struct{ cause error }

func (e *wrappedError) Error() string { return "wrap: " + e.cause.Error() }
func (e *wrappedError) Unwrap() error { return e.cause }
