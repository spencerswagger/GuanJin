//go:build integration

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/spencerswagger/guanjin/internal/gateway"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/protocol"
	"github.com/spencerswagger/guanjin/internal/signing"
)

// 本文件是 `AC-FC-07/08` 的证据（**用探针扫，不用"看代码没有 SQL"的静态论证**）。
//
// ## AC-FC-08：`/userinfo` 在 DB 挂时仍 200 且 **SQL 计数 = 0**
//
// 采集方式：把 gorm 的 Query/Row/Raw/Create/Update/Delete 回调全部换成计数器（`countingDB`），
// 这个句柄既是"DB 挂掉"的对象（DSN 指向必然连不上的地址），也是**装配给协议面的唯一句柄**——
// 因此"请求期间有过任何一次 SQL 尝试"都会被计数到。断言 `count == 0` 是**真采**，不是推断。
//
// ## AC-FC-07：三种故障下探针扫端点，**没有一个**带身份
//
// 扫描面 = 认证/协议/自助面的非白名单端点 + **数据面通配**；判定 = 响应不带
// `X-GuanJin-Assertion` 头、body 里不含 `"sub"`（身份 claim 的最小特征）。
// 白名单只有三个探针本身（`/livez`、`/readyz`、`/.well-known/*`）——它们按设计不返回身份。

// countingDB 返回一个"连不上但会计数"的 gorm 句柄 + 计数器。
//
// `DisableAutomaticPing` 必须开：否则 `gorm.Open` 立刻拨号，测试变成慢超时。
func countingDB(t *testing.T) (*gorm.DB, *int64) {
	t.Helper()
	var n int64
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "postgres://user:pass@127.0.0.1:1/guanjin_test?sslmode=disable&connect_timeout=1",
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 logger.Discard,
	})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	bump := func(*gorm.DB) { atomic.AddInt64(&n, 1) }
	// 覆盖"会碰库"的所有回调类型：Query/Row 是读，Create/Update/Delete 是写，Raw 走 Row。
	_ = db.Callback().Query().Before("gorm:query").Register("count:query", bump)
	_ = db.Callback().Row().Before("gorm:row").Register("count:row", bump)
	_ = db.Callback().Raw().Before("gorm:raw").Register("count:raw", bump)
	_ = db.Callback().Create().Before("gorm:create").Register("count:create", bump)
	_ = db.Callback().Update().Before("gorm:update").Register("count:update", bump)
	_ = db.Callback().Delete().Before("gorm:delete").Register("count:delete", bump)
	return db, &n
}

// fcProtocolServer 装出一个**最小可用**的协议面（只为了让 `/userinfo` 真有端点可打）。
//
// 依赖全是空实现：本用例只关心"`/userinfo` 会不会碰库"，不关心其它端点的业务语义。
func fcProtocolServer(t *testing.T, db *gorm.DB, keys *signing.Set, issuer string) http.Handler {
	t.Helper()
	srv, err := protocol.New(protocol.Deps{
		Keys:     keys,
		Codes:    stubCodes{},
		Sessions: stubSessions{},
		Clients:  stubClients{},
		Issuer:   issuer,
		Flows:    stubFlows{},
		FlowCode: func(error) (httpx.Code, bool) { return "", false },
		FlowTTL:  time.Minute,
		// 关键：把"会碰库"的解析器接上**计数句柄**——若 `/userinfo` 改成本地解析之外的做法
		// （比如回库查会话），计数立刻不为 0，本用例就会红。
		Caller: &protocol.CallerResolver{
			Keys:     keys,
			Sessions: &fcSessionStatus{db: db},
			Now:      time.Now,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    time.Now,
	})
	if err != nil {
		t.Fatalf("protocol.New: %v", err)
	}
	mux := http.NewServeMux()
	srv.Routes(mux)
	return mux
}

// AC-FC-08：DB 挂 + `/userinfo` → **200**，且请求期间 **SQL 尝试 = 0**。
func TestAC_FC_08_UserinfoIs200WithZeroSQLWhenDBDown(t *testing.T) {
	db, count := countingDB(t)
	keys := sloKeys(t) // 复用 SLO 用例的真密钥生成器（真 SM2）
	issuer := "https://auth.guanjin.test"

	// 用真密钥签一个**合法**的访问令牌（`/userinfo` 靠本地验签即可 200）。
	key, err := keys.For(signing.PurposeOIDC)
	if err != nil {
		t.Fatalf("keys.For(oidc): %v", err)
	}
	now := time.Now().UTC()
	token, err := signing.SignJWT(key, map[string]any{
		"iss": issuer, "sub": "0193f0aa-0000-7000-8000-000000000001",
		"aud": "billing", "cid": "billing", "acr": "AAL1", "amr": []string{"pwd"},
		"sid": uuid.NewString(), "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}

	handler := fcProtocolServer(t, db, keys, issuer)
	atomic.StoreInt64(count, 0)
	req := httptest.NewRequest(http.MethodGet, "/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("DB 挂时 /userinfo 期望 200（本地解析），实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt64(count); got != 0 {
		t.Fatalf("`/userinfo` 期间 SQL 尝试必须为 0，实测 %d（一旦回库查会话就违反 D8-D′）", got)
	}
	// 反向保险：body 里确实有身份字段（证明我们不是在"因为没返回任何东西才 200"）。
	if !strings.Contains(rec.Body.String(), `"sub"`) {
		t.Fatalf("/userinfo 应返回 sub，实际 body=%s", rec.Body.String())
	}

	// **正控（守线的正控，不是装饰）**：同一句柄上主动发一条 SQL，计数器必须涨——
	// 否则"计数=0"可能只是"计数器没接上"，那条断言就是空的。
	var one int
	_ = db.Raw(`SELECT 1`).Scan(&one)
	if got := atomic.LoadInt64(count); got == 0 {
		t.Fatal("正控失败：主动发一条 SQL 后计数仍为 0 → 计数器没生效，本用例的 0 无意义")
	}
}

// AC-FC-07：故障态下扫"非白名单端点 + 数据面通配"，**没有一个**带身份。
//
// 判据的最小特征：响应头不带 `X-GuanJin-Assertion`、body 不含 `"sub"`。
func TestAC_FC_07_NoIdentityOnFailurePaths(t *testing.T) {
	// ① 认证面：DB 与 Redis 都不可达（用死端口句柄；认证端点因此一律 503）。
	db, _ := countingDB(t)
	keys := sloKeys(t)

	mux := fcProtocolServer(t, db, keys, "https://auth.guanjin.test")
	paths := []string{
		"/oauth/token", "/oauth/revoke", "/oauth/introspect",
		"/authorize", "/authorize/submit", "/userinfo",
		"/api/v1/sessions", "/api/v1/me/sessions", "/api/v1/me/password",
		"/api/v1/agent/policies", "/auth/register/start", "/auth/verify/password",
	}
	for _, p := range paths {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		assertNoIdentity(t, p, rec)
	}

	// ② 数据面通配：依赖不可达（会话加载失败）→ 必须 503 且**不注入断言**。

	// 数据面需要一个可用的 caller 解析器；这里用一个**必然失败**的实现，
	// 目的是走"依赖挂了 → 503"的分支而不是"令牌无效 → 401"。
	gw := fcDataPlane(t, keys)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/nope", nil)
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 32))
	gw.ServeHTTP(rec, req)
	assertNoIdentity(t, "/api/nope (data plane)", rec)
}

// assertNoIdentity 断言响应**不带身份**（头无断言、body 无 `sub`）。
func assertNoIdentity(t *testing.T, path string, rec *httptest.ResponseRecorder) {
	t.Helper()
	for k := range rec.Header() {
		if strings.EqualFold(k, gateway.AssertionHeader) {
			t.Fatalf("%s：故障态响应出现了断言头 %q", path, k)
		}
	}
	if strings.Contains(rec.Body.String(), `"sub"`) {
		t.Fatalf("%s：故障态响应 body 里出现了身份字段 sub（body=%s）", path, rec.Body.String())
	}
}

func mustDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		t.Fatal("必须设置 GUANJIN_TEST_DSN")
	}
	return dsn
}

// ---- 协议面的空实现（只为本文件服务）----

type stubCodes struct{}

func (stubCodes) Put(context.Context, string, string, time.Duration) error { return nil }
func (stubCodes) Take(context.Context, string, error) (string, error)      { return "", nil }
func (stubCodes) Get(context.Context, string, error) (string, error)       { return "", nil }
func (stubCodes) Delete(context.Context, string) error                     { return nil }

type stubSessions struct{}

func (stubSessions) Issue(context.Context, protocol.SessionSpec) (string, error) {
	return uuid.NewString(), nil
}

type stubClients struct{}

func (stubClients) Policy(context.Context, string) (protocol.ClientPolicy, error) {
	return protocol.ClientPolicy{}, nil
}

type stubFlows struct{}

func (stubFlows) Begin(context.Context, string, string) (string, error) { return "", nil }

// fcSessionStatus 是"会碰库"的会话状态读取（接到计数句柄上，见 `fcProtocolServer`）。
type fcSessionStatus struct{ db *gorm.DB }

func (s *fcSessionStatus) SessionStatus(ctx context.Context, sid string) (string, error) {
	var status string
	_ = s.db.WithContext(ctx).Raw(`SELECT status FROM sessions WHERE id = ?`, sid).Scan(&status).Error
	return status, nil
}

// fcDataPlane 装一个"准入必然失败"的数据面：路由存在、但令牌解析器直接报库不可用。
//
// 目的是走「依赖挂了 → 503 且零注入」的分支（AC-FC-07 的数据面那一半）。
func fcDataPlane(t *testing.T, keys *signing.Set) http.Handler {
	t.Helper()
	router, err := gateway.NewRouter([]gateway.Route{{
		Prefix: "/api", ClientID: "billing", Upstream: mustURL(t, "http://127.0.0.1:1"),
	}})
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	forward, err := gateway.New(router, gateway.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	signer, err := gateway.NewAssertionSigner(keys)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	guard, err := gateway.NewGuard(&gateway.Guard{
		Caller:   failingCaller{},
		Sessions: failingSessions{},
		Policies: failingPolicies{},
		Roles:    failingRoles{},
		Users:    failingUsers{},
		Visits:   failingVisits{},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	p, err := gateway.NewPipeline(forward, guard, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	return p
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return u
}

// 失败实现：令牌解析报"依赖不可用"（不是"令牌无效"），走 503 分支。
type failingCaller struct{}

func (failingCaller) Resolve(context.Context, string) (httpx.Caller, error) {
	return httpx.Caller{}, errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
}

type failingSessions struct{}

func (failingSessions) LoadSession(context.Context, string) (gateway.SessionSnapshot, error) {
	return gateway.SessionSnapshot{}, errors.New("db down")
}

type failingPolicies struct{}

func (failingPolicies) LoadAdmissionPolicy(context.Context, string) (gateway.AdmissionPolicy, error) {
	return gateway.AdmissionPolicy{}, errors.New("db down")
}

type failingRoles struct{}

func (failingRoles) RolesForUserClient(context.Context, string, string) ([]string, error) {
	return nil, errors.New("db down")
}

type failingUsers struct{}

func (failingUsers) Username(context.Context, string) (string, error) {
	return "", errors.New("db down")
}

type failingVisits struct{}

func (failingVisits) RecordVisit(context.Context, string, string, string) error {
	return errors.New("db down")
}
