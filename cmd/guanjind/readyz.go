package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是 **S10 的 `/readyz` 探针**（m1-slices S10：「无新增业务端点（`/readyz` 探针 + `cmd/guanjind` 内定时任务）」）。
//
// 口径（D8-C / AC-FC-01/02）：
//   - **DB 不通** → `503 DB_UNAVAILABLE`（不是 200 + "degraded"：探针就是要让负载均衡摘流量）；
//   - **Redis 不通** → `503 REDIS_UNAVAILABLE`；
//   - 都通 → `200 {"status":"ready", ...}`。
//
// 三条实现纪律：
//  1. **每次探测都现打依赖**（带超时），不返回"启动时探测过一次"的缓存结论——
//     缓存结论会让探针在依赖挂掉后继续报 ready，等于把故障藏起来；
//  2. **`/livez` 不受影响**：存活探针只回答"进程还在跑吗"，依赖挂了不该被重启（否则会连锁雪崩）；
//  3. 响应体只写**事实**（哪个依赖不通），不写内部地址/口令/键名。

// readyzDeps 是探针需要的最小依赖面（接口化便于单测注入假探针）。
type readyzDeps struct {
	db    *gorm.DB
	redis *redis.Client
	// timeout 单次探测上限（三个依赖各一次，串行足够快；数据量小）。
	timeout time.Duration
}

// readyzHandler 返回 `/readyz` 的处理器。
func readyzHandler(deps readyzDeps) http.HandlerFunc {
	timeout := deps.timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return func(w http.ResponseWriter, r *http.Request) {
		traceID := httpx.TraceIDFromContext(r.Context())
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		checks := map[string]string{}
		pending := []string{}

		// ① DB：`SELECT 1`（最轻的存在性探测，不迁怒业务表）。
		if deps.db == nil {
			checks["db"] = "missing"
		} else if err := deps.db.WithContext(ctx).Raw(`SELECT 1`).Scan(new(int)).Error; err != nil {
			checks["db"] = "down"
		} else {
			checks["db"] = "ok"
		}

		// ② Redis：`PING`。未装配 = 该进程不依赖它（如纯 DB 面），如实标 `n/a` 而不是伪装成 ok。
		switch {
		case deps.redis == nil:
			checks["redis"] = "n/a"
		default:
			if err := deps.redis.Ping(ctx).Err(); err != nil {
				checks["redis"] = "down"
			} else {
				checks["redis"] = "ok"
			}
		}

		// ③ 密钥：装配期已 fail-fast 校验（缺钥进程起不来），故运行期如实标 ok；
		// 轮换状态不属 M1（索引 §4.2-22 的静态双密钥）。
		checks["keys"] = "ok"

		// 码值按 D8-C：依赖挂 → 对应的 503 码（不是统一 DEPENDENCY_UNAVAILABLE）。
		switch {
		case checks["db"] == "down" || checks["db"] == "missing":
			writeReadyz(w, http.StatusServiceUnavailable, traceID, httpx.CodeDBUnavailable, checks, pending)
			return
		case checks["redis"] == "down":
			writeReadyz(w, http.StatusServiceUnavailable, traceID, httpx.CodeRedisUnavailable, checks, pending)
			return
		}
		if checks["redis"] == "n/a" {
			pending = append(pending, "redis")
		}
		writeReadyz(w, http.StatusOK, traceID, "", checks, pending)
	}
}

// writeReadyz 写探针响应。
//
// 失败时**同时**给出 `code`/`message`（与其余接口同一套四字段，便于运维按码表排查）与 `checks`（哪个依赖不通）；
// 成功时保持原来的 `{"status":"ready","checks":{...},"pending":[...]}` 形状（不破坏既有探针消费方）。
func writeReadyz(w http.ResponseWriter, status int, traceID string, code httpx.Code,
	checks map[string]string, pending []string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status >= http.StatusInternalServerError {
		body := map[string]any{
			"code":     code,
			"message":  "服务暂不可用，请稍后重试",
			"trace_id": traceID,
			"checks":   checks,
		}
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	body := map[string]any{"status": "ready", "checks": checks, "pending": pending}
	_ = json.NewEncoder(w).Encode(body)
}

// dbProbe 供装配层复用（`/readyz` 与后续定时任务共用同一个"库还活着吗"的判定）。
func dbProbe(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return errors.New("readyz: nil db")
	}
	return db.WithContext(ctx).Raw(`SELECT 1`).Scan(new(int)).Error
}
