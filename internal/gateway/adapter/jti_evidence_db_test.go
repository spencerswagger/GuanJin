//go:build integration

package adapter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/spencerswagger/guanjin/internal/gateway"
	"github.com/spencerswagger/guanjin/internal/signing"
)

// 本文件是 **AC-DP-08「S8 半边」的三条举证**（真 PG17 + 真 Redis + 假上游）。
//
// 归属裁定（Spec 2026-11-10，裁定稿 `notes/ac-dp-08-归属裁定-2026-11-10.md`）：
//   - **S8 只判**：每请求现签**新** `jti`、被拒绝时**零注入**；
//   - **「同 `jti` 二次 → 401」由验签方产生并记账**（SDK / S9）——平台数据面零 Redis、零 `jti` 存储。
//
// 所以这三条用例证明的是"平台把该做的事做到位、把不该做的事留给验签方"，**不是**证明平台会判重：
//
//	a. 同一 token 连发 2 次 → 上游收到**两个不同 `jti`**，且每个断言 `exp-iat==60`；
//	b. 假上游（自带**真 Redis** replay-guard，等价于 SDK 的 `replay-store`）对重复 `jti` 回 401
//	   → 网关**原样透传**（状态码/响应体逐字节一致）、**零注入**、不改写；
//	c. `replay-window=1s` + 1.1s 后重投同一 `jti` → 200，且验签方留痕（计数）。

// sdkLikeUpstream 是"业务系统 + SDK"的最小仿真：验签（JWKS 公钥）→ 读 `jti` → 真 Redis 判重。
//
// 用真 Redis 而不是内存 map：`AC-DP-08` 的判据环境写的是「集成（真实 Redis）」，
// 且验签方的判重是多副本共享状态，内存 map 测不出 `SetNX`/TTL 的真实行为。
type sdkLikeUpstream struct {
	srv    *httptest.Server
	rdb    *redis.Client
	window time.Duration
	keys   *signing.Set

	mu         sync.Mutex
	assertions []string
	claims     []map[string]any
	// reuseAfterWindow 记录"窗口过期后同 jti 又被放行"的次数（= 留痕，c 条用）。
	reuseAfterWindow int
	// seenAt 记每个 jti 第一次被放行的时刻（判"是否发生在窗口之后"）。
	seenAt map[string]time.Time
}

const sdkRejectBody = `{"code":"TOKEN_INVALID","message":"令牌已被使用","trace_id":"sdk-upstream-1"}`

func newSDKLikeUpstream(t *testing.T, rdb *redis.Client, keys *signing.Set, window time.Duration) *sdkLikeUpstream {
	t.Helper()
	u := &sdkLikeUpstream{rdb: rdb, window: window, keys: keys, seenAt: map[string]time.Time{}}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := ""
		for k, v := range r.Header {
			if strings.EqualFold(k, gateway.AssertionHeader) && len(v) > 0 {
				raw = v[0]
			}
		}
		u.mu.Lock()
		u.assertions = append(u.assertions, raw)
		u.mu.Unlock()

		claims := u.verify(t, raw)
		u.mu.Lock()
		u.claims = append(u.claims, claims)
		u.mu.Unlock()

		jti, _ := claims["jti"].(string)
		// 真 Redis 的「首次占位」语义：与 SDK 的 `replay-store=redis` 同一实现方式。
		ok, err := u.rdb.SetNX(context.Background(), "sdk:replay:"+jti, "1", u.window).Result()
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !ok {
			// 判重命中：验签方**自己**产出 401（不是网关产的）。
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(sdkRejectBody))
			return
		}
		u.mu.Lock()
		if at, seen := u.seenAt[jti]; seen && time.Since(at) > u.window {
			u.reuseAfterWindow++ // 留痕：窗口过期后允许再用
		}
		u.seenAt[jti] = time.Now()
		u.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// verify 用 JWKS 公钥验签并解出 claims（与业务系统 SDK 的路径一致）。
func (u *sdkLikeUpstream) verify(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Errorf("上游收到的断言不是三段式：%q", token)
		return nil
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Errorf("decode header: %v", err)
		return nil
	}
	var header struct {
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hb, &header); err != nil {
		t.Errorf("unmarshal header: %v", err)
		return nil
	}
	pub := jwkPublic(t, u.keys, header.Kid)
	claims, err := verifyGatewayAssertion(t, pub, token)
	if err != nil {
		t.Errorf("验签失败（平台签出来的断言必须能被上游 JWKS 验过）：%v", err)
		return nil
	}
	return claims
}

func (u *sdkLikeUpstream) snapshot() ([]string, []map[string]any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.assertions...), append([]map[string]any(nil), u.claims...)
}

func (u *sdkLikeUpstream) reuseCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reuseAfterWindow
}

// ---- a) 同一 token 连发两次 → 两个不同 jti，且 exp-iat==60 ----

func TestAC_DP_08a_FreshJtiPerRequest(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	itCurrentClient = f.clientID
	t.Cleanup(func() { itCurrentClient = "" })

	keys := newITKeys(t)
	rdb := testRedisClient(t)
	up := newSDKLikeUpstream(t, rdb, keys.set, time.Minute)
	pipeline := newITPipeline(t, db, keys, up.srv)

	token := keys.userToken(t, f.userID.String(), f.clientID, f.session.String())
	for i := 0; i < 2; i++ {
		rec := itRequest(pipeline, "/api/orders", token)
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求期望 200，实际 %d（body=%s）", i+1, rec.Code, rec.Body.String())
		}
	}

	_, claims := up.snapshot()
	if len(claims) != 2 {
		t.Fatalf("上游应收到 2 份断言，实际 %d", len(claims))
	}
	jti1, _ := claims[0]["jti"].(string)
	jti2, _ := claims[1]["jti"].(string)
	if jti1 == "" || jti2 == "" {
		t.Fatalf("断言缺 jti：%q / %q", jti1, jti2)
	}
	if jti1 == jti2 {
		t.Fatalf("同一 token 的两次请求拿到了同一个 jti（%q）——平台必须逐请求现签新 jti", jti1)
	}
	for i, c := range claims {
		iat, _ := c["iat"].(float64)
		exp, _ := c["exp"].(float64)
		if int64(exp)-int64(iat) != 60 {
			t.Fatalf("第 %d 份断言 exp-iat = %d，期望 60", i+1, int64(exp)-int64(iat))
		}
		if c["iss"] != gateway.AssertionIssuer {
			t.Fatalf("第 %d 份断言 iss = %v，期望 %q", i+1, c["iss"], gateway.AssertionIssuer)
		}
	}
}

// ---- b) 验签方对重复 jti 回 401 → 网关原样透传、零注入、不改写 ----

func TestAC_DP_08b_UpstreamReplayRejectionPassesThrough(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	itCurrentClient = f.clientID
	t.Cleanup(func() { itCurrentClient = "" })

	keys := newITKeys(t)
	rdb := testRedisClient(t)
	// 1 分钟窗口：保证这次"重复"确实命中判重（不是靠 TTL 过期）。
	up := newSDKLikeUpstream(t, rdb, keys.set, time.Minute)

	// 让网关**固定**签发同一个 jti（等价于"同一份断言被重放"），并预先占位，
	// 使上游第一次就会判成重复 → 401 由验签方产生。
	// 每次运行唯一：判重键在真 Redis 里有 TTL，写死常量会让同一次 CI 的
	// 「集成测试」与「覆盖率采集」两趟互相命中（Gate 用 -count=2 复现过）。
	fixed := uuid.NewString()
	if err := rdb.Set(context.Background(), "sdk:replay:"+fixed, "1", time.Minute).Err(); err != nil {
		t.Fatalf("预占位: %v", err)
	}
	signer, err := gateway.NewAssertionSigner(keys.set)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	signer.NewJTI = func() (string, error) { return fixed, nil }
	pipeline := newITPipelineWithSigner(t, db, keys, up.srv, signer)

	token := keys.userToken(t, f.userID.String(), f.clientID, f.session.String())
	rec := itRequest(pipeline, "/api/orders", token)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("上游 401 必须原样透传，实际网关回 %d（body=%s）", rec.Code, rec.Body.String())
	}
	// 逐字节一致：网关不得改写验签方的响应体（也不许套自己的错误码）。
	if got := rec.Body.String(); got != sdkRejectBody {
		t.Fatalf("响应体被改写：\n got=%s\nwant=%s", got, sdkRejectBody)
	}
	// 零注入：响应上不得出现任何断言头（也不得回填别的身份头）。
	for k := range rec.Header() {
		if strings.EqualFold(k, gateway.AssertionHeader) {
			t.Fatalf("响应上出现了断言头 %q：被拒时不得注入身份", k)
		}
	}
	// 断言确实发出去了（401 是验签方的决定，不是网关自己拒的）。
	assertions, claims := up.snapshot()
	if len(assertions) != 1 {
		t.Fatalf("上游应收到 1 次请求，实际 %d", len(assertions))
	}
	if jti, _ := claims[0]["jti"].(string); jti != fixed {
		t.Fatalf("上游收到的 jti = %q，期望固定的 %q（用例前提）", jti, fixed)
	}
}

// ---- c) replay-window=1s + 1.1s 后重投 → 200，且验签方留痕 ----

func TestAC_DP_08c_ReplayWindowExpiryAllowsReuse(t *testing.T) {
	db := openTestDB(t)
	f := newFixture(t, db, "L2")
	itCurrentClient = f.clientID
	t.Cleanup(func() { itCurrentClient = "" })

	keys := newITKeys(t)
	rdb := testRedisClient(t)
	// 判据里的 `replay-window`（SDK 配置项，默认 60s）——这里压到 1s 才跑得动。
	up := newSDKLikeUpstream(t, rdb, keys.set, time.Second)

	fixed := uuid.NewString() // 同上：跨运行不得复用同一个 jti
	signer, err := gateway.NewAssertionSigner(keys.set)
	if err != nil {
		t.Fatalf("NewAssertionSigner: %v", err)
	}
	signer.NewJTI = func() (string, error) { return fixed, nil }
	pipeline := newITPipelineWithSigner(t, db, keys, up.srv, signer)

	token := keys.userToken(t, f.userID.String(), f.clientID, f.session.String())
	if rec := itRequest(pipeline, "/api/orders", token); rec.Code != http.StatusOK {
		t.Fatalf("首次投递期望 200，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	// 窗口内重投：同 jti → 验签方 401（这条是"窗口生效"的正控）。
	if rec := itRequest(pipeline, "/api/orders", token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("窗口内重投期望 401，实际 %d（body=%s）", rec.Code, rec.Body.String())
	}

	time.Sleep(1100 * time.Millisecond)

	rec := itRequest(pipeline, "/api/orders", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("窗口过期后重投期望 200（不误杀），实际 %d（body=%s）", rec.Code, rec.Body.String())
	}
	if got := up.reuseCount(); got != 1 {
		t.Fatalf("验签方应留痕「窗口过期后放行 1 次」，实际 %d", got)
	}
	_, claims := up.snapshot()
	if len(claims) != 3 {
		t.Fatalf("上游应收到 3 次投递，实际 %d", len(claims))
	}
	// 三次的 jti 相同（用例固定它），差别只在时间：这正是"窗口"语义的证据。
	for i, c := range claims {
		if jti, _ := c["jti"].(string); jti != fixed {
			t.Fatalf("第 %d 次 jti = %q，期望固定值（用例前提）", i+1, jti)
		}
	}
}

// 保证测试辅助不因未使用而报错（`strconv` 在诊断输出里可能用到）。
var _ = strconv.Itoa
var _ = io.Discard
