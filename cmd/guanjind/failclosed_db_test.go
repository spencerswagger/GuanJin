//go:build integration

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/config"
	"github.com/spencerswagger/guanjin/internal/crypto"
	"github.com/spencerswagger/guanjin/internal/httpx"
	"github.com/spencerswagger/guanjin/internal/store"
)

// 本文件是 **`AC-FC-01/02` 的端点级证据**（真 PG17 `guanjin_test` + 真 Redis 7）。
//
// ## 证据强度（**如实标注：非"真停容器"**）
//
// 沙盒里起停"一次性 PG 集群"需要 `postgres` 用户 initdb，本轮未做；改用**真 TCP 转发器**：
// 在 `127.0.0.1:0` 上监听并转发到真实依赖，然后 `Close()` 掉监听——
// 对被测进程而言，效果就是"依赖突然不可达"（新连接被拒、已建连接断死）。
//
// 这与判据原文的"真停 Redis / 真停 PG"**强度不同**：容器被停是"进程消失"，
// 转发器被关是"链路消失"。两者的可观测行为（拨号被拒 / 连接重置）一致，
// 但**不能声称等价**。故本文件的结论在验收表里记
// 「AC-FC-01/02 端点级 503 已证（**非真停，强度低于判据原文**）」，
// 真停容器的证据由 S10 的环境侧另行采集（需宿主机配合）。
//
// ## 判据形态
//
//   - Redis 断 → 认证端点 **503 `REDIS_UNAVAILABLE`**、`/readyz` 非 200；
//   - PG 断 → 认证端点 **503 `DB_UNAVAILABLE`**；
//   - 两种情形下响应**都不带** `X-GuanJin-Assertion`（认证面本来不注入，这里锁死防回归）。

// tcpForwarder 是一个"可掐断"的 TCP 转发器：`Close()` 之后新连接被拒。
type tcpForwarder struct {
	ln     net.Listener
	target string
	mu     sync.Mutex
	conns  []net.Conn
	wg     sync.WaitGroup
	closed bool
}

func newTCPForwarder(t *testing.T, target string) *tcpForwarder {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen forwarder: %v", err)
	}
	f := &tcpForwarder{ln: ln, target: target}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.DialTimeout("tcp", target, time.Second)
			if err != nil {
				_ = conn.Close()
				continue
			}
			f.mu.Lock()
			f.conns = append(f.conns, conn, up)
			f.mu.Unlock()
			go pipe(conn, up)
			go pipe(up, conn)
		}
	}()
	t.Cleanup(f.Close)
	return f
}

func pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

// Close 掐断：先断已建连接，再关监听（顺序反了会让 Accept 先返回、已建连接仍活着）。
func (f *tcpForwarder) Close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	conns := f.conns
	f.conns = nil
	f.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	_ = f.ln.Close()
	f.wg.Wait()
}

func (f *tcpForwarder) addr() string { return f.ln.Addr().String() }

// fcEnv 把真实依赖地址换成"可掐断的转发器"，并装出配置。
func fcEnv(t *testing.T) (*config.Config, *tcpForwarder, *tcpForwarder) {
	t.Helper()
	dsn := os.Getenv("GUANJIN_TEST_DSN")
	if dsn == "" {
		t.Fatal("必须设置 GUANJIN_TEST_DSN（integration job 已注入）")
	}
	redisAddr := os.Getenv("GUANJIN_TEST_REDIS_ADDR")
	if redisAddr == "" {
		t.Fatal("必须设置 GUANJIN_TEST_REDIS_ADDR（真 Redis）")
	}
	pgHostPort := dsnHostPort(t, dsn)

	pgFwd := newTCPForwarder(t, pgHostPort)
	redisFwd := newTCPForwarder(t, redisAddr)

	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 0x11)
	}
	master, err := crypto.NewMasterKey(raw)
	if err != nil {
		t.Fatalf("NewMasterKey: %v", err)
	}
	cfg := &config.Config{
		HTTPAddr:      "127.0.0.1:0",
		DatabaseDSN:   replaceHostPort(t, dsn, pgFwd.addr()),
		RedisAddr:     redisFwd.addr(),
		MasterKey:     master,
		TraceHeader:   "X-Trace-Id",
		Issuer:        "https://auth.guanjin.test",
		RedisPassword: os.Getenv("GUANJIN_TEST_REDIS_PASSWORD"),
	}
	return cfg, pgFwd, redisFwd
}

// dsnHostPort 从 DSN 里抠出 `host:port`（够用即可，不引 url 解析以外的依赖）。
func dsnHostPort(t *testing.T, dsn string) string {
	t.Helper()
	at := strings.LastIndex(dsn, "@")
	slash := strings.Index(dsn[at+1:], "/")
	if at < 0 || slash < 0 {
		t.Fatalf("无法从 DSN 解析 host:port：%q", dsn)
	}
	return dsn[at+1 : at+1+slash]
}

// replaceHostPort 把 DSN 的 host:port 换掉。
func replaceHostPort(t *testing.T, dsn, hostPort string) string {
	t.Helper()
	at := strings.LastIndex(dsn, "@")
	slash := strings.Index(dsn[at+1:], "/")
	if at < 0 || slash < 0 {
		t.Fatalf("无法替换 DSN 的 host:port：%q", dsn)
	}
	return dsn[:at+1] + hostPort + dsn[at+1+slash:]
}

// fcServer 装出认证面（flow + register）+ `/readyz`，返回 httptest 服务。
func fcServer(t *testing.T, cfg *config.Config) (*httptest.Server, *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{DSN: cfg.DatabaseDSN})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close(db) })
	if err := store.Ping(ctx, db); err != nil {
		t.Fatalf("ping（转发器应连通）：%v", err)
	}
	startupCfg, err := store.ValidateStartup(ctx, store.NewRuntimeConfigRepo(db))
	if err != nil {
		t.Fatalf("ValidateStartup: %v", err)
	}
	w, err := newWiring(ctx, db, cfg, startupCfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newWiring（Redis 走转发器）：%v", err)
	}
	mux := http.NewServeMux()
	w.flowHandler.Routes(mux)
	w.registerHandler.Routes(mux)
	mux.HandleFunc("/readyz", readyzHandler(readyzDeps{db: db, redis: nil}))
	srv := httptest.NewServer(httpx.TraceMiddleware(mux, "X-Trace-Id", slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv, db
}

// AC-FC-01（端点级）：Redis 链路掐断 → 认证端点 503 `REDIS_UNAVAILABLE`，且响应不带断言头。
//
// 注：`/readyz` 的 Redis 检查在 `TestReadyzRedisDownIs503` 里单独钉（那里用死端口）；
// 这里只证"认证端点"这一半，避免把两条判据混成一条。
func TestAC_FC_01_AuthEndpointRedisUnavailable(t *testing.T) {
	cfg, _, redisFwd := fcEnv(t)
	srv, db := fcServer(t, cfg)
	clientID := fcClientID(t)
	// **只有带 user_id 的流才会去读 Redis**（`VerifyStep` 的账号级锁定，D3-D）：
	// `/auth/register/start` 不碰 Redis（实测 200），所以探针必须落在 verify 上。
	flowID := fcFlowWithUser(t, db, clientID)

	verifyBody := `{"flow_id":"` + flowID + `","response":"wrong-password-1"}`
	// 基线对照：掐断前不是 503（排除"无论怎样都 503"的假阳性）。
	if before := postJSON(t, srv.URL+"/auth/verify/password", verifyBody); before == http.StatusServiceUnavailable {
		t.Fatalf("掐断前不该 503（基线对照失败）")
	}

	redisFwd.Close()
	time.Sleep(50 * time.Millisecond)

	rec := postJSONRec(t, srv.URL+"/auth/verify/password", verifyBody)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Redis 断后认证端点期望 503，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	code := errorCodeOf(t, rec.Body.Bytes())
	if code != "REDIS_UNAVAILABLE" {
		t.Fatalf("码值期望 REDIS_UNAVAILABLE，实际 %q（body=%s）", code, rec.Body.String())
	}
	if rec.Header().Get("X-GuanJin-Assertion") != "" {
		t.Fatal("认证端点不该返回断言头")
	}
}

// AC-FC-02（端点级）：PG 链路掐断 → 认证端点 503 `DB_UNAVAILABLE`，且不注入身份。
func TestAC_FC_02_AuthEndpointDBUnavailable(t *testing.T) {
	cfg, pgFwd, _ := fcEnv(t)
	srv, _ := fcServer(t, cfg)

	pgFwd.Close()
	time.Sleep(50 * time.Millisecond)

	rec := postJSONRec(t, srv.URL+"/auth/register/start",
		`{"client_id":"`+fcClientID(t)+`","type":"email","value":"fc2@example.com"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("PG 断后期望 503，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, rec.Body.Bytes()); code != "DB_UNAVAILABLE" {
		t.Fatalf("码值期望 DB_UNAVAILABLE，实际 %q（body=%s）", code, rec.Body.String())
	}
	if rec.Header().Get("X-GuanJin-Assertion") != "" {
		t.Fatal("认证端点不该返回断言头")
	}
}

// fcClientID 取一个真实存在的业务系统（用 seed 的档位默认策略，避免依赖夹具）。
func fcClientID(t *testing.T) string {
	t.Helper()
	db := openTestDB(t)
	var id string
	if err := db.Raw(`SELECT client_id FROM clients WHERE status = 'active' LIMIT 1`).Scan(&id).Error; err != nil || id == "" {
		t.Fatalf("测试库里没有可用 client（先跑迁移/夹具）：%v", err)
	}
	return id
}

func postJSON(t *testing.T, url, body string) int {
	t.Helper()
	return postJSONRec(t, url, body).Code
}

func postJSONRec(t *testing.T, url, body string) *httptest.ResponseRecorder {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	rec := httptest.NewRecorder()
	rec.Code = resp.StatusCode
	for k, v := range resp.Header {
		rec.Header()[k] = v
	}
	_, _ = rec.Body.Write(raw)
	return rec
}

func errorCodeOf(t *testing.T, body []byte) string {
	t.Helper()
	var v struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode error body: %v（body=%s）", err, body)
	}
	return v.Code
}

var _ = uuid.Nil
var _ = base64.StdEncoding

// fcFlowWithUser 造一条**带 user_id 的活跃流**（只有这种流才会在 verify 时读 Redis 的锁定状态）。
func fcFlowWithUser(t *testing.T, db *gorm.DB, clientID string) string {
	t.Helper()
	userID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`,
		userID, "fc-"+userID.String()[:8]).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
	flowID, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	if err := db.Exec(`INSERT INTO flows (id, user_id, client_id, target_acr, achieved_amr, status, expires_at)
		VALUES (?, ?, ?, 'AAL1', '{}', 'active', now() + interval '10 minutes')`,
		flowID, userID, clientID).Error; err != nil {
		t.Fatalf("insert flow: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Exec(`DELETE FROM flows WHERE id = ?`, flowID).Error
		_ = db.Exec(`DELETE FROM users WHERE id = ?`, userID).Error
	})
	return flowID.String()
}
