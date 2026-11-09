//go:build integration

package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是"超时不算依赖不可用"这条收窄的**真库证据**（`-tags=integration` + `GUANJIN_TEST_DSN`）。
//
// 为什么必须在真库上证：三种失败在真前端是**不同的错误类型**，靠构造 error 只能证明
// "我写的判定函数按我写的规则跑"，证明不了"真实故障会变成那个类型"：
//   - 慢查询被服务端取消 → `*pgconn.PgError` **57014**；
//   - 慢查询撞客户端 deadline → `*pgconn.errTimeout`（pgx 私有类型，**不是** `net.OpError`）；
//   - 真停库/端口不通 → `*pgconn.ConnectError`。
//
// 前两者**都不许**映成 503（否则慢 SQL 会被当成"库挂了"，客户端退避重试、告警淹没）；
// 第三者**必须**是 `DB_UNAVAILABLE`/503（否则真故障被降级成 500，客户端不退避）。

func TestDBRealSlowQueryIsNotUnavailable(t *testing.T) {
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		t.Fatal("必须设置 GUANJIN_TEST_DSN（真 PostgreSQL）")
	}
	ctx := context.Background()

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
			t.Fatalf("慢查询撞 deadline 不得算依赖不可用，实际 %T: %v", err, err)
		}
		if code, ok := mappedCode(err); ok {
			t.Fatalf("不得给出依赖码，实际 %q（%T）", code, err)
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
		if code, ok := mappedCode(err); ok {
			t.Fatalf("不得给出依赖码，实际 %q", code)
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
