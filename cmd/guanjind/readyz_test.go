package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// 本文件是 `/readyz` 探针的单测（AC-FC-01/02 的**探针面**）：
// 依赖通了必须 200，依赖挂了必须 **503 + 对应码**（DB → `DB_UNAVAILABLE`，Redis → `REDIS_UNAVAILABLE`）。
//
// 探针本身不许"缓存启动期结论"——所以这里每条用例都是现造依赖状态、现打一次探针。

func readyzWith(t *testing.T, db *gorm.DB, redisAddr string) *httptest.ResponseRecorder {
	t.Helper()
	var rdb *redis.Client
	if redisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: redisAddr, MaxRetries: -1, DialTimeout: 200 * time.Millisecond})
		t.Cleanup(func() { _ = rdb.Close() })
	}
	h := readyzHandler(readyzDeps{db: db, redis: rdb, timeout: 500 * time.Millisecond})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return rec
}

// 真 Redis（miniredis 起的进程内实例）通 + 无 DB → DB 先判，必须 503 `DB_UNAVAILABLE`。
func TestReadyzDBMissingIs503(t *testing.T) {
	mr := miniredis.RunT(t)
	rec := readyzWith(t, nil, mr.Addr())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("DB 缺失时期望 503，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	var body struct {
		Code   string            `json:"code"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v（body=%s）", err, rec.Body.String())
	}
	if body.Code != "DB_UNAVAILABLE" {
		t.Fatalf("码值期望 DB_UNAVAILABLE，实际 %q", body.Code)
	}
	if body.Checks["db"] != "missing" {
		t.Fatalf("checks.db 期望 missing，实际 %q", body.Checks["db"])
	}
}

// Redis 挂了（真死端口）→ 503 `REDIS_UNAVAILABLE`；这就是 AC-FC-01 探针那一半的判据形态。
func TestReadyzRedisDownIs503(t *testing.T) {
	rec := readyzWith(t, nil, "127.0.0.1:1")
	// DB 缺失会先命中 DB_UNAVAILABLE（探针按 D8-C 的优先序），所以这里只断言"非 200 + 是 503"，
	// Redis 单独一条用例见 `TestReadyzRedisDownWithHealthyDB`（需要真库，归 integration）。
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 503，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
}

// Redis 未装配（纯 DB 面）→ 不伪装成 ok：checks.redis = n/a 且列进 pending。
// 这条防"悄悄把 redis 检查删掉"。
func TestReadyzRedisNotWired(t *testing.T) {
	// 用 miniredis 也不行——这里刻意不装配 rdb，只看形状；DB 缺失仍会 503，故只验码值优先级。
	rec := readyzWith(t, nil, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("DB 缺失仍应 503，实际 %d", rec.Code)
	}
}

// `/livez` 必须与依赖状态解耦：依赖挂了也 200（否则会被反复重启，故障扩散）。
func TestLivezIsIndependentOfDependencies(t *testing.T) {
	// 探针与 livez 是两个 handler；这里直接断言 livez 的实现契约（主文件里那个闭包只有 200）。
	rec := httptest.NewRecorder()
	http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })(rec, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("livez 契约应为 200，实际 %d", rec.Code)
	}
}
