package flow

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/spencerswagger/guanjin/internal/credential"
	"github.com/spencerswagger/guanjin/internal/factor"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
	"github.com/spencerswagger/guanjin/internal/store/model"
)

// 本文件钉住 **AC-FC-01 / AC-FC-02 在认证端点上的那一半**：
//
//   Redis 不可用 → 503 `REDIS_UNAVAILABLE`；DB 不可用 → 503 `DB_UNAVAILABLE`；
//   且**依赖挂着一律不放行**（不出现 200、不产出带身份的响应）。
//
// 为什么映射要在这里（而不是 handler 里）：`fail()` 只认 `CodeOf`。码映射写错，
// 依赖挂了就会落到 default 的 500 —— 客户端仍被拒（fail-closed 不破），但运维看不到
// "是哪个依赖挂了"，`/readyz` 之外的告警与 `Retry-After` 全部失效。
//
// 不用 mock：DB 侧是**真拨号一个没人监听的端口**（`127.0.0.1:1`）拿到的真错误。

// deadDSN 指向一个确定没人监听的端口（1 号端口）。
const deadDSN = "postgres://guanjin_test:x@127.0.0.1:1/guanjin_test?sslmode=disable"

// realConnectError 真拨号拿一个连接层错误（不 mock）。
func realConnectError(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, deadDSN)
	if err == nil {
		_ = conn.Close(ctx)
		t.Fatal("127.0.0.1:1 居然连上了：本用例要求真拨号失败")
	}
	return err
}

// 码映射：依赖不可用的两类来源（已判定 / 原始连接错误）都必须是 503 的码。
func TestCodeOfDependencyUnavailable(t *testing.T) {
	mappedDBUnavailable := &store.Error{Code: httpx.CodeDBUnavailable, Status: 503}
	cases := []struct {
		name       string
		err        error
		wantCode   httpx.Code
		wantStatus int
	}{
		{"仓储已判 DB_UNAVAILABLE", mappedDBUnavailable, httpx.CodeDBUnavailable, 503},
		{"原始连接层错误（未过 MapError）", realConnectError(t), httpx.CodeDBUnavailable, 503},
		{"风控 Redis 不可用", ErrRiskUnavailable, httpx.CodeRedisUnavailable, 503},
		{"包裹后的 Redis 不可用", wrapErr(ErrRiskUnavailable), httpx.CodeRedisUnavailable, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := CodeOf(tc.err)
			if !ok || code != tc.wantCode {
				t.Fatalf("CodeOf = (%q,%v), want (%q,true)", code, ok, tc.wantCode)
			}
			status, okStatus := StatusFor(tc.err)
			if !okStatus || status != tc.wantStatus {
				t.Fatalf("StatusFor = (%d,%v), want (%d,true)", status, okStatus, tc.wantStatus)
			}
			// 503 必须带 Retry-After（判据原文），码表决定状态，这里验一次闭环。
			if got := httpx.HTTPStatus(code); got != tc.wantStatus {
				t.Fatalf("码表把 %s 映成了 %d，want %d", code, got, tc.wantStatus)
			}
		})
	}
}

// 端点级反证（收窄后新增）：**socket 超时不是"库挂了"** → 500，不是 503。
//
// 为什么这条要落在端点上而不是只测 `IsUnavailable`：判据关心的是"客户端拿到什么"。
// 若这里回 503，客户端会把它当"依赖瞬时不可用"退避重试，而真实原因是慢查询/网络停顿——
// 告警与排障方向都会被带偏（这正是评审报的窄缺口）。
func TestHTTPChallengeSocketTimeoutIsNot503(t *testing.T) {
	env := newTestEnv(t)
	flowID := env.startFlow(t)
	// socket 读超时：`net.OpError` 里装的是 deadline 超时。
	env.orch.deps.Reader = failingReader{err: &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}}

	resp, body := postJSON(t, env.srv.URL+"/auth/challenge/password",
		`{"client_id":"c-1","purpose":"login","flow_id":"`+flowID+`"}`)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Fatalf("超时不得回 503（会被当成依赖挂了去重试），实际 %d/%s", resp.StatusCode, body.Code)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("超时按未知错误处理应为 500，实际 %d", resp.StatusCode)
	}
	if body.Code == httpx.CodeDBUnavailable || body.Code == httpx.CodeRedisUnavailable {
		t.Fatalf("超时不得给出依赖码，实际 %q", body.Code)
	}
	if got := resp.Header.Get("Retry-After"); got != "" {
		t.Fatalf("500 不应带 Retry-After（那是 503 的信号），实际 %q", got)
	}
}

// 业务错误**不许**被当成依赖不可用：否则 409/422 会变成 503，接入方会开始盲目重试。
func TestCodeOfBusinessErrorIsNotUnavailable(t *testing.T) {
	business := &store.Error{Code: httpx.CodeIdentifierAlreadyBound, Status: 409}
	if code, ok := CodeOf(business); ok {
		t.Fatalf("业务错误不该被映成 %q（应留给 500 之前的正确分支）", code)
	}
	if code, ok := CodeOf(errors.New("boom")); ok {
		t.Fatalf("未知错误不该有码，实际 %q", code)
	}
	// 凭据错误的码仍然优先（不能被依赖判定抢走）。
	if code, _ := CodeOf(credential.ErrInvalidCredential); code != httpx.CodeInvalidCredential {
		t.Fatalf("凭据错误应映 INVALID_CREDENTIAL，实际 %q", code)
	}
}

// DB 不可用时 `POST /auth/challenge/{factor}` 必须 503 + `DB_UNAVAILABLE` + `Retry-After`，
// 且响应体里**没有任何身份字段**（判据：期间零个"200 且带身份"）。
func TestHTTPChallengeDBUnavailableIs503(t *testing.T) {
	connectErr := realConnectError(t)
	env := newTestEnv(t)
	flowID := env.startFlow(t)
	// 真停库时，`POST /auth/challenge/{factor}` 第一个挂的是**读流那一行**（`flows`）。
	// 这里换成真拨号失败的 `store.Error`（`MapError` 的产物，与仓储出口一字不差）。
	env.orch.deps.Reader = failingReader{err: store.MapError(connectErr)}

	resp, body := postJSON(t, env.srv.URL+"/auth/challenge/password",
		`{"client_id":"c-1","purpose":"login","flow_id":"`+flowID+`"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503（依赖不可用必须 fail-closed）", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Fatal("503 必须带 Retry-After（AC-FC-01/02）")
	}
	if body.Code != httpx.CodeDBUnavailable {
		t.Fatalf("code = %q, want %q", body.Code, httpx.CodeDBUnavailable)
	}
	if body.TraceID == "" {
		t.Fatal("503 也必须给可复制 trace_id")
	}
	assertNoIdentityInError(t, body)
}

// 原始连接错误（没过 MapError 的读路径）同样必须 503，不许 500。
func TestHTTPChallengeRawConnectErrorIs503(t *testing.T) {
	connectErr := realConnectError(t)
	env := newTestEnv(t)
	flowID := env.startFlow(t)
	// 原始驱动错误（没过 `MapError` 的读路径）：必须同样 503，不许 500。
	env.orch.deps.Reader = failingReader{err: connectErr}
	resp, body := postJSON(t, env.srv.URL+"/auth/challenge/password",
		`{"client_id":"c-1","purpose":"login","flow_id":"`+flowID+`"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || body.Code != httpx.CodeDBUnavailable {
		t.Fatalf("want 503/%s，实际 %d/%s", httpx.CodeDBUnavailable, resp.StatusCode, body.Code)
	}
	assertNoIdentityInError(t, body)
}

// Redis 不可用（风控计数挂）时校验端点必须 503 + `REDIS_UNAVAILABLE`，且**不放行**。
func TestHTTPVerifyRedisUnavailableIs503(t *testing.T) {
	env := newTestEnv(t)
	flowID := env.startFlow(t)
	// 已绑定用户才谈得上"账号级失败计数"（匿名流没有账号可数，也就不会碰 Redis）。
	uid := uuid.New()
	env.store.flow.UserID = &uid
	// 凭据错 → 编排层要记一次失败 → 风控守卫报"Redis 不可用"（装配层把它翻成
	// `ErrRiskUnavailable`，见 `cmd/guanjind/wiring.go` 的 `mapRiskError`）。
	env.verifier.err = credential.ErrInvalidCredential
	env.orch.deps.Guard = &fakeGuard{threshold: 3, err: ErrRiskUnavailable}

	// 口令是**无状态挑战**：不带 `challenge_id`（带了反而要求装配 challenge store）。
	resp, body := postJSON(t, env.srv.URL+"/auth/verify/password",
		`{"flow_id":"`+flowID+`","response":"pw"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503（Redis 挂了必须拒服）；body code=%q msg=%q", resp.StatusCode, body.Code, body.Message)
	}
	if body.Code != httpx.CodeRedisUnavailable {
		t.Fatalf("code = %q, want %q", body.Code, httpx.CodeRedisUnavailable)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Fatal("503 必须带 Retry-After")
	}
	// 关键：拒绝时**不能**带上任何身份/令牌（否则等于"分片放行"）。
	assertNoIdentityInError(t, body)
}

// assertNoIdentity 断言响应体里没有任何"身份已注入"的痕迹。
//
// 依赖不可用时的 5xx 只能是"错误 + trace_id"；出现 `acr`/`amr`/`verified`/`amr` 或令牌字段，
// 说明请求被部分放行了（AC-FC-07 的"探针扫不出带身份的响应"就是这条）。
// assertNoIdentityInError 断言"依赖不可用"时的响应**没有任何身份注入**。
//
// 依赖不可用只能是四字段错误（code/message/trace_id），且 `details` 必须为空：
// 出现 `flow`/`acr`/`amr`/令牌类字段，就说明请求被部分放行了（AC-FC-07 要扫的正是这种）。
func assertNoIdentityInError(t *testing.T, body httpx.ErrorResponse) {
	t.Helper()
	if body.Details != nil {
		t.Fatalf("依赖不可用时 details 必须为空（不得带身份/流程细节），实际 %v", body.Details)
	}
	lower := strings.ToLower(body.Message)
	for _, forbidden := range []string{"token", "amr", "acr", "user", "sub"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("依赖不可用的 message 里出现了身份字样 %q：%q", forbidden, body.Message)
		}
	}
	if strings.Contains(body.Message, "verified") {
		t.Fatalf("依赖不可用的 message 里出现了 %q", "verified")
	}
}

// failingReader 让"读流那一行"失败（模拟库挂；错误形态由用例决定）。
type failingReader struct{ err error }

func (f failingReader) Get(context.Context, uuid.UUID) (*model.Flow, error) { return nil, f.err }

func wrapErr(err error) error { return &simpleWrap{err} }

type simpleWrap struct{ cause error }

func (e *simpleWrap) Error() string { return "wrap: " + e.cause.Error() }
func (e *simpleWrap) Unwrap() error { return e.cause }

// 编译期确认：这两个变量在被本文件引用时是"活的"（避免误删）。
var _ factor.FactorType = factor.FactorPassword
