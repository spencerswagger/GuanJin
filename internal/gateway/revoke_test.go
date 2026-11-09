package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/spencerswagger/guanjin/internal/httpx"
)

// 本文件是**实时撤销判定**的纯单测（AC-SESS-01/04 的判定面）：
// 状态与时间两个维度都要能单独把请求挡住，且"不存在/已撤销/已过期"对外同形。
func TestCheckSessionAliveMatrix(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name     string
		session  SessionSnapshot
		wantCode httpx.Code // 空 = 放行
	}{
		{
			name: "active 且未到任何超时",
			session: SessionSnapshot{Status: SessionActive,
				ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Minute)},
		},
		{
			name:    "active 但没有有效期（零值）→ 不按时间拒（零值表示未提供）",
			session: SessionSnapshot{Status: SessionActive},
		},
		{
			name:     "revoked",
			session:  SessionSnapshot{Status: SessionRevoked, ExpiresAt: now.Add(time.Hour)},
			wantCode: httpx.CodeSessionRevoked,
		},
		{
			name:     "expired",
			session:  SessionSnapshot{Status: SessionExpired, ExpiresAt: now.Add(time.Hour)},
			wantCode: httpx.CodeSessionRevoked,
		},
		{
			name: "绝对超时到点（status 还是 active：后台还没扫）",
			session: SessionSnapshot{Status: SessionActive,
				ExpiresAt: now.Add(-time.Nanosecond), IdleExpiresAt: now.Add(time.Hour)},
			wantCode: httpx.CodeSessionRevoked,
		},
		{
			name: "空闲超时到点（status 还是 active）",
			session: SessionSnapshot{Status: SessionActive,
				ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(-time.Nanosecond)},
			wantCode: httpx.CodeSessionRevoked,
		},
		{
			name:     "未知状态（fail-closed，不当 active）",
			session:  SessionSnapshot{Status: "pending_review", ExpiresAt: now.Add(time.Hour)},
			wantCode: httpx.CodeDBUnavailable,
		},
		{
			name:     "空状态（数据缺陷，同样 fail-closed）",
			session:  SessionSnapshot{ExpiresAt: now.Add(time.Hour)},
			wantCode: httpx.CodeDBUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSessionAlive(tc.session, now)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("期望放行，实际 %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("期望拒绝，实际放行")
			}
			if err.Code != tc.wantCode {
				t.Fatalf("错误码期望 %s，实际 %s", tc.wantCode, err.Code)
			}
			if err.Status != httpx.HTTPStatus(err.Code) {
				t.Fatalf("状态码与码表不一致：%d vs %d", err.Status, httpx.HTTPStatus(err.Code))
			}
			if err.Status != http.StatusForbidden && err.Status != http.StatusServiceUnavailable {
				t.Fatalf("非预期状态码 %d", err.Status)
			}
		})
	}
}

// 撤销判定**只读**：本包不得有任何写 `sessions` 的路径（撤销归 S7，避免两套撤销审计）。
func TestGatewayDoesNotWriteSessions(t *testing.T) {
	// 这条靠"包内不出现 UPDATE sessions"守：源文件扫描比"记得别写"可靠。
	files := []string{"admit.go", "revoke.go", "pipeline.go", "inject.go", "slo.go", "proxy.go", "route.go"}
	for _, f := range files {
		src := readSource(t, f)
		for _, banned := range []string{"UPDATE sessions", "status = 'revoked'", "RevokeByID"} {
			if contains(src, banned) {
				t.Fatalf("%s 出现 %q：数据面不得写会话（撤销归 S7）", f, banned)
			}
		}
	}
}

// 结构性守卫：网关**核心逻辑**里不许直接持有 Redis 客户端。
//
// 这条不是"平台不用 Redis"——判重（AC-DP-08）**确实**要真 Redis，但它是 `ReplayGuard` 接缝，
// 实现落在 `internal/gateway/adapter`（`RedisReplayGuard`）。核心包直接 import 客户端会带来两个后果：
//   - 撤销判定（AC-SESS-01）与凭证判重混在同一层，"顺手把会话状态也缓存进 Redis"变得毫无门槛，
//     而撤销一旦有缓存窗口就不再是"撤销后首个请求即 403"；
//   - 核心逻辑没法用假实现做纯单测（本目录所有判据用例都是无网络跑的）。
func TestGatewayCoreHasNoRedisClient(t *testing.T) {
	// 递归扫全包（含子包）：新增目录不得自动落在守线之外。
	for _, f := range guardSources(t) {
		src := readSource(t, f)
		for _, banned := range []string{"redis.Client", "go-redis", "redis.NewClient", "redis.Get"} {
			if contains(src, banned) {
				t.Fatalf("%s 出现 %q：数据面不建 Redis 客户端（AC-DP-08 平台不判重）", f, banned)
			}
		}
	}
}
