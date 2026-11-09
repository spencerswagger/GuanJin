//go:build integration

package store

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件复刻评审（Gate）的**掐链路**验法：在真 PG 前面挂一个本机 TCP 转发器，
// DSN 指向转发器 → 先证明链路通（基线）→ 掐掉监听并断开在途连接 → 再断言
// 这次失败仍被判成 `DB_UNAVAILABLE`（没有"未知错误 → 500"）。
//
//   - 端口不再监听（`ECONNREFUSED`）：`TestRealConnectFailureIsDBUnavailable` 已覆盖；
//   - **进程还在但连接被掐断**（`ECONNRESET`/`io.EOF`/`ErrBadConn`）：只有这条能造出来。
//
// 2026-11-10 把"超时"从依赖不可用里剔除之后，必须确认**这一种没被误伤**——
// 否则真故障会被降级成 500，客户端就不退避了。

// tcpProxy 是最小的双向转发器（只为本用例存在）。
type tcpProxy struct {
	ln     net.Listener
	target string

	mu    sync.Mutex
	conns []net.Conn
}

func newTCPProxy(t *testing.T, target string) *tcpProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen proxy: %v", err)
	}
	p := &tcpProxy{ln: ln, target: target}
	go p.serve()
	return p
}

func (p *tcpProxy) addr() string { return p.ln.Addr().String() }

func (p *tcpProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return // 监听已关
		}
		upstream, err := net.DialTimeout("tcp", p.target, 2*time.Second)
		if err != nil {
			_ = client.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, client, upstream)
		p.mu.Unlock()
		go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
		go func() { _, _ = io.Copy(client, upstream); _ = client.Close() }()
	}
}

// kill 关监听 + 断开所有在途连接（= 生产上"库进程被 kill / 网络被掐"）。
func (p *tcpProxy) kill() {
	if p == nil {
		return
	}
	_ = p.ln.Close()
	p.mu.Lock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
	p.mu.Unlock()
}

func TestDBOutageThroughKilledProxyIsDBUnavailable(t *testing.T) {
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		t.Fatal("必须设置 GUANJIN_TEST_DSN（真 PostgreSQL）")
	}
	realAddr := pgAddrOf(t, dsn)
	proxy := newTCPProxy(t, realAddr)
	t.Cleanup(proxy.kill)

	ctx := context.Background()
	db, err := Open(ctx, Options{DSN: withHost(t, dsn, proxy.addr())})
	if err != nil {
		t.Fatalf("open via proxy: %v", err)
	}
	t.Cleanup(func() { _ = Close(db) })

	if err := Ping(ctx, db); err != nil {
		t.Fatalf("基线：经转发器应能连上（否则本用例证不到链路通→掐断）: %v", err)
	}
	var one int
	if err := db.Raw("select 1").Scan(&one).Error; err != nil || one != 1 {
		t.Fatalf("基线查询失败：%v", err)
	}

	// 掐链路（关监听 + 断在途连接）。
	proxy.kill()

	raw := db.Raw("select 1").Scan(&one).Error
	if raw == nil {
		t.Fatal("掐链路后查询居然成功了：本用例无法复现，不给假绿")
	}

	mapped := MapError(raw)
	var se *Error
	if errors.As(mapped, &se) {
		if se.Code != httpx.CodeDBUnavailable {
			t.Fatalf("掐链路后应得 %q，实际 %q（%T: %v）", httpx.CodeDBUnavailable, se.Code, raw, raw)
		}
	} else if !IsUnavailable(raw) {
		t.Fatalf("掐链路后既没被 MapError 归到 DB_UNAVAILABLE，也没判为不可用：%T: %v", raw, raw)
	}
	if status := httpx.HTTPStatus(httpx.CodeDBUnavailable); status != 503 {
		t.Fatalf("码表把 %s 映成了 %d，want 503", httpx.CodeDBUnavailable, status)
	}
}

// pgAddrOf 从 DSN 里取真实 host:port（默认 5432）。
func pgAddrOf(t *testing.T, dsn string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// withHost 把 DSN 的 host:port 换成转发器地址（账号/库名不动）。
func withHost(t *testing.T, dsn, hostport string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.Host = hostport
	return u.String()
}
