//go:build integration

package store

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是"超时"口径的**真库证据**（`-tags=integration` + `GUANJIN_TEST_DSN`）。
//
// 口径一句话：**"连不上"始终算依赖不可用，超时（读写）不算。** 四种形态各自的真实错误类型
// 只有真 PG 能造出来，构造 error 只能证明"函数按我写的规则跑"：
//   - **socket 读超时**（评审的复现法：`DialFunc` 包一层，每次 Read 都打开一个短 deadline）
//     → `*pgconn.errTimeout` 包 `*net.OpError{op=read, timeout:true}` → **不算**（慢查询）；
//   - `statement_timeout` → `*pgconn.PgError` **57014** → **不算**；
//   - 客户端 ctx deadline → `*pgconn.errTimeout` → **不算**；
//   - 真停库（死端口）→ `*pgconn.ConnectError` → **算**（503 `DB_UNAVAILABLE`）。
//
// 反证价值：把 `unavailable.go` 里 `opErr.Timeout() && opErr.Op != "dial"` 这一条删掉，
// 第一条用例立刻变红——这就是"防以后有人再把它翻转回去"的锁。

// shortReadDeadlineConn 给每次读都挂一个短 deadline（复刻评审的 socket 读超时场景）。
type shortReadDeadlineConn struct {
	net.Conn
	readTimeout time.Duration
}

func (c *shortReadDeadlineConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.readTimeout))
	return c.Conn.Read(p)
}

func TestDBRealSlowQueryIsNotUnavailable(t *testing.T) {
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		t.Fatal("必须设置 GUANJIN_TEST_DSN（真 PostgreSQL）")
	}
	ctx := context.Background()

	t.Run("socket 读超时（评审复现路径）", func(t *testing.T) {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("parse dsn: %v", err)
		}
		cfg.DialFunc = func(dctx context.Context, network, addr string) (net.Conn, error) {
			raw, derr := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(dctx, network, addr)
			if derr != nil {
				return nil, derr
			}
			// 握手也要能过，所以 deadline 给 300ms（握手远小于它），
			// 真正会超时的是下面那条 `pg_sleep(2)` 的读。
			return &shortReadDeadlineConn{Conn: raw, readTimeout: 300 * time.Millisecond}, nil
		}
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })

		var one int
		err = conn.QueryRow(ctx, "select 1 from pg_sleep(2)").Scan(&one)
		if err == nil {
			t.Fatal("pg_sleep(2) 不可能在 300ms 读超时内返回")
		}
		var opErr *net.OpError
		isNetOp := errors.As(err, &opErr) && opErr.Op == "read"
		t.Logf("err=%T: %v（net.OpError(read)=%v, timeout=%v）", err, err, isNetOp, opErr != nil && opErr.Timeout())

		if IsUnavailable(err) {
			t.Fatalf("socket 读超时（慢查询）不得算依赖不可用，实际 %T: %v", err, err)
		}
		if code, ok := mappedCode(err); ok {
			t.Fatalf("不得给出依赖码，实际 %q（%T）", code, err)
		}
	})

	t.Run("客户端 deadline（errTimeout）", func(t *testing.T) {
		conn := freshConn(t, dsn)
		cctx, cancel := context.WithTimeout(ctx, 120*time.Millisecond)
		defer cancel()
		var one int
		err := conn.QueryRow(cctx, "select 1 from pg_sleep(2)").Scan(&one)
		if err == nil {
			t.Fatal("pg_sleep(2) 不可能在 120ms 内返回")
		}
		if IsUnavailable(err) {
			t.Fatalf("ctx 超时不得算依赖不可用，实际 %T: %v", err, err)
		}
	})

	t.Run("服务端 statement_timeout（57014）", func(t *testing.T) {
		conn := freshConn(t, dsn)
		if _, err := conn.Exec(ctx, "set statement_timeout='120ms'"); err != nil {
			t.Fatalf("set statement_timeout: %v", err)
		}
		var one int
		err := conn.QueryRow(ctx, "select 1 from pg_sleep(2)").Scan(&one)
		if err == nil {
			t.Fatal("statement_timeout 应取消慢查询")
		}
		if IsUnavailable(err) {
			t.Fatalf("57014 不得算依赖不可用，实际 %T: %v", err, err)
		}
	})

	t.Run("真停库（死端口）仍归 DB_UNAVAILABLE", func(t *testing.T) {
		conn, err := pgx.Connect(ctx, "postgres://guanjin_test:x@127.0.0.1:1/guanjin_test?sslmode=disable")
		if err == nil {
			_ = conn.Close(ctx)
			t.Fatal("127.0.0.1:1 居然连上了")
		}
		if !IsUnavailable(err) {
			t.Fatalf("真拨号失败必须算依赖不可用，实际 %T: %v", err, err)
		}
		code, ok := mappedCode(err)
		if !ok || code != httpx.CodeDBUnavailable {
			t.Fatalf("want %q，实际 %q（%T）", httpx.CodeDBUnavailable, code, err)
		}
	})
}

// TestDBRealDialTimeoutIsUnavailable 用**真拨号超时**兜住"拨号超时没被 ConnectError 包住"的边缘：
// 目标地址取 TEST-NET-1（`192.0.2.0/24`，RFC 5737 保留给文档，正常网络里没有路由/被丢弃），
// 配 300ms 拨号超时 → 真超时（若环境快速回 `network unreachable`，那也是"连不上"，同样必须算）。
func TestDBRealDialTimeoutIsUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	conn, err := (&net.Dialer{Timeout: 300 * time.Millisecond}).DialContext(ctx, "tcp", "192.0.2.1:5432")
	elapsed := time.Since(start)
	if err == nil {
		_ = conn.Close()
		t.Fatal("192.0.2.1:5432 不该连上（TEST-NET-1 是保留段）")
	}
	var opErr *net.OpError
	isOp := errors.As(err, &opErr)
	t.Logf("dial 失败：%T: %v（net.OpError=%v, Op=%q, Timeout=%v, 耗时=%v）",
		err, err, isOp, opName(opErr), opErr != nil && opErr.Timeout(), elapsed)

	// 无论这条路径是"真超时"还是"网络不可达"，都属"连不上"→ 必须算依赖不可用。
	if !IsUnavailable(err) {
		t.Fatalf("拨号失败/拨号超时必须算依赖不可用，实际 %T: %v", err, err)
	}
	if code, ok := mappedCode(err); !ok || code != httpx.CodeDBUnavailable {
		t.Fatalf("want %q，实际 %q（%T: %v）", httpx.CodeDBUnavailable, code, err, err)
	}
}

func opName(e *net.OpError) string {
	if e == nil {
		return ""
	}
	return e.Op
}

// freshConn 每次新开连接：超时会把连接弄坏，复用连接会让后一个用例报"conn closed"（假象）。
func freshConn(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// mappedCode 取 `MapError` 判出来的对外码（ok=false 表示没映射，按 500 处理）。
func mappedCode(err error) (httpx.Code, bool) {
	var se *Error
	if errors.As(MapError(err), &se) && se.Code != "" {
		return se.Code, true
	}
	return "", false
}
