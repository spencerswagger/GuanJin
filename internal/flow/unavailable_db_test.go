//go:build integration

package flow

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件把「**拨号黑洞**」这条边角**端点级**钉住（`-tags=integration`）。
//
// 语义（2026-11-10 定案）：
//   - **拨号超时算依赖不可用**：防火墙 DROP、对端主机不存在、VPC 路由丢包时，客户端看到的
//     就是 dial 超时——那就是"库连不上"，必须 503 让客户端退避；落 500 会把真故障伪装成应用 bug。
//   - **读写超时不算**：那是慢查询/网络停顿（见 `unavailable_test.go` 的反向用例）。
//
// 造法（写清，别让人猜）：往 **TEST-NET-1**（`192.0.2.0/24`，RFC 5737 保留给文档，正常网络
// 里没有路由、包被丢弃）拨 `192.0.2.1:5432`，`Dialer.Timeout = 300ms` → 真 dial 超时。
// 若某个环境对保留段立即回 `unreachable`（快速失败而非超时），本用例仍成立（那也是"连不上"），
// 日志会打出实际形态；而 dial **超时**这一支由单元用例 `TestIsUnavailableDialVsReadTimeout`
// 用 `net.OpError{Op:"dial", Timeout:true}` 确定性地钉住，不依赖网络环境。
func TestEndpointDialBlackholeYields503(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	conn, dialErr := (&net.Dialer{Timeout: 300 * time.Millisecond}).DialContext(ctx, "tcp", "192.0.2.1:5432")
	elapsed := time.Since(start)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatal("192.0.2.1:5432 不该连上（TEST-NET-1 为保留段）")
	}
	var opErr *net.OpError
	op := ""
	timedOut := false
	if ok := asNetOp(dialErr, &opErr); ok {
		op = opErr.Op
		timedOut = opErr.Timeout()
	}
	t.Logf("黑洞拨号：%T: %v（Op=%q, Timeout=%v, 耗时=%v）", dialErr, dialErr, op, timedOut, elapsed)

	// ① 库层：真 dial 失败/超时必须算依赖不可用。
	if !store.IsUnavailable(dialErr) {
		t.Fatalf("拨号黑洞必须算依赖不可用，实际 %T: %v", dialErr, dialErr)
	}

	// ② 端点级：把这条**真实的** dial 错误喂给认证端点 → 503 `DB_UNAVAILABLE` + `Retry-After`。
	env := newTestEnv(t)
	flowID := env.startFlow(t)
	env.orch.deps.Reader = failingReader{err: dialErr}

	resp, body := postJSON(t, env.srv.URL+"/auth/challenge/password",
		`{"client_id":"c-1","purpose":"login","flow_id":"`+flowID+`"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("拨号黑洞应回 503（库连不上），实际 %d/%s", resp.StatusCode, body.Code)
	}
	if body.Code != httpx.CodeDBUnavailable {
		t.Fatalf("code = %q, want %q", body.Code, httpx.CodeDBUnavailable)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Fatal("503 必须带 Retry-After（客户端据此退避）")
	}
	if body.TraceID == "" {
		t.Fatal("503 也必须给可复制 trace_id")
	}
}

// asNetOp 是 `errors.As` 的薄封装（避免在本文件 import errors 只为一次断言的可读性问题）。
func asNetOp(err error, target **net.OpError) bool {
	e, ok := err.(*net.OpError)
	if !ok {
		return false
	}
	*target = e
	return true
}
