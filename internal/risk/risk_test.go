package risk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// 本包用 miniredis 做测试（Redis 协议兼容）：断言的是**键名 / 阈值 / TTL / 原子性语义**，
// 不需要真 Redis 进程，因此能在主闸（`go test ./...`）每次跑，而不是只在有 Redis 的环境跑。
func newGuard(t *testing.T) (*Guard, *miniredis.Miniredis) {
	t.Helper()
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	g, err := NewWithClient(client)
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	return g, srv
}

func testPolicy() Policy {
	// 阈值/时长**显式给**（代码里没有默认值）。
	return Policy{FailThreshold: 3, LockDuration: 15 * time.Minute}
}

// 键名是冻结口径（D3-D）：改一个字符就等于改判据。
func TestKeyNamesAreFrozen(t *testing.T) {
	const uid = "0192f0a0-0000-7000-8000-000000000000"
	if got := FailKey(uid); got != "login:fail:"+uid {
		t.Fatalf("失败计数键名不符：%s", got)
	}
	if got := LockKey(uid); got != "login:lock:"+uid {
		t.Fatalf("锁定键名不符：%s", got)
	}
}

// 连败到阈值：第 n 次返回 locked=true，并真的写下 `login:lock:{user_id}`（带 TTL）。
func TestRecordFailureLocksAtThreshold(t *testing.T) {
	g, srv := newGuard(t)
	ctx := context.Background()
	p := testPolicy()
	const uid = "0192f0a0-0000-7000-8000-000000000001"

	for i := 1; i <= 2; i++ {
		left, locked, err := g.RecordFailure(ctx, uid, p)
		if err != nil {
			t.Fatalf("第 %d 次: %v", i, err)
		}
		if locked {
			t.Fatalf("第 %d 次不该锁定（阈值 %d）", i, p.FailThreshold)
		}
		if want := p.FailThreshold - i; left != want {
			t.Fatalf("第 %d 次剩余次数应为 %d，实际 %d", i, want, left)
		}
	}
	left, locked, err := g.RecordFailure(ctx, uid, p)
	if err != nil {
		t.Fatalf("第 3 次: %v", err)
	}
	if !locked || left != 0 {
		t.Fatalf("达阈值应锁定且剩余 0，实际 locked=%v left=%d", locked, left)
	}

	// 直接按冻结键名断言（AC-LOGIN-03 要求"直接按上述两键断言"）。
	if got, _ := srv.Get(FailKey(uid)); got != "3" {
		t.Fatalf("计数键应为 3，实际 %q", got)
	}
	if !srv.Exists(LockKey(uid)) {
		t.Fatal("锁定键必须存在")
	}
	if ttl := srv.TTL(LockKey(uid)); ttl <= 0 || ttl > p.LockDuration {
		t.Fatalf("锁定键 TTL 应为 (0, %s]，实际 %s", p.LockDuration, ttl)
	}
	if ttl := srv.TTL(FailKey(uid)); ttl <= 0 || ttl > p.LockDuration {
		t.Fatalf("计数键应有窗口 TTL，实际 %s", ttl)
	}

	// Status 只认 lock 键：删掉锁键后即使计数还满额也应报"未锁定"（可运维解除）。
	srv.Del(LockKey(uid))
	_, locked, err = g.Status(ctx, uid, p)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if locked {
		t.Fatal("锁定只认 lock 键，删键后不应再判锁定")
	}
}

// 成功登录清零：计数与锁键一起消失（否则"证明自己是自己"还被挡）。
func TestResetClearsBothKeys(t *testing.T) {
	g, srv := newGuard(t)
	ctx := context.Background()
	p := testPolicy()
	const uid = "0192f0a0-0000-7000-8000-000000000002"

	for i := 0; i < 3; i++ {
		if _, _, err := g.RecordFailure(ctx, uid, p); err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
	}
	if !srv.Exists(LockKey(uid)) {
		t.Fatal("夹具失败：应已锁定")
	}
	if err := g.Reset(ctx, uid); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if srv.Exists(FailKey(uid)) || srv.Exists(LockKey(uid)) {
		t.Fatal("清零后两个键都必须不存在")
	}
	left, locked, err := g.Status(ctx, uid, p)
	if err != nil || locked || left != p.FailThreshold {
		t.Fatalf("清零后应回到满额未锁定，实际 left=%d locked=%v err=%v", left, locked, err)
	}
}

// 多副本：两个实例共用同一个 Redis 就是"同一份计数"（AC-LOGIN-03 的 2 副本口径）。
func TestTwoReplicasShareCounters(t *testing.T) {
	_, srv := newGuard(t)
	clientA := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	clientB := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = clientA.Close(); _ = clientB.Close() })
	a, _ := NewWithClient(clientA)
	b, _ := NewWithClient(clientB)

	ctx := context.Background()
	p := testPolicy()
	const uid = "0192f0a0-0000-7000-8000-000000000003"

	// A 失败 n-1 次、B 失败第 n 次 → 在 B 上触发锁定，A 也立刻看到锁定。
	for i := 1; i < p.FailThreshold; i++ {
		if _, locked, err := a.RecordFailure(ctx, uid, p); err != nil || locked {
			t.Fatalf("A 第 %d 次: locked=%v err=%v", i, locked, err)
		}
	}
	if _, locked, err := b.RecordFailure(ctx, uid, p); err != nil || !locked {
		t.Fatalf("B 的第 %d 次应触发锁定，实际 locked=%v err=%v", p.FailThreshold, locked, err)
	}
	if _, locked, err := a.Status(ctx, uid, p); err != nil || !locked {
		t.Fatalf("另一副本必须立即看到锁定，实际 locked=%v err=%v", locked, err)
	}
}

// 匿名挑战（没有 user_id）不产生账号级计数：如实返回"满额未锁定"，不写任何键。
func TestEmptyUserIsNotCounted(t *testing.T) {
	g, srv := newGuard(t)
	ctx := context.Background()
	left, locked, err := g.RecordFailure(ctx, "  ", testPolicy())
	if err != nil || locked || left != 3 {
		t.Fatalf("空 user_id 不该计数：left=%d locked=%v err=%v", left, locked, err)
	}
	if len(srv.Keys()) != 0 {
		t.Fatalf("不该写下任何键，实际 %v", srv.Keys())
	}
	if err := g.Reset(ctx, ""); err != nil {
		t.Fatalf("空 user_id 的 Reset 应是无害的：%v", err)
	}
}

// Redis 不可用 → fail-closed（绝不"降级放行"），并且错误要能被上层识别成 503 语义。
func TestRedisUnavailableFailsClosed(t *testing.T) {
	g, srv := newGuard(t)
	ctx := context.Background()
	const uid = "0192f0a0-0000-7000-8000-000000000004"
	srv.Close()

	if _, _, err := g.Status(ctx, uid, testPolicy()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Status 应报 ErrUnavailable，实际 %v", err)
	}
	if _, _, err := g.RecordFailure(ctx, uid, testPolicy()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("RecordFailure 应报 ErrUnavailable，实际 %v", err)
	}
	if err := g.Reset(ctx, uid); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Reset 应报 ErrUnavailable，实际 %v", err)
	}
	if err := g.Ping(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ping 应报 ErrUnavailable，实际 %v", err)
	}
}

// 阈值/时长非法是**配置缺陷**：报错而不是拿默认值顶着。
func TestWeakPolicyRejected(t *testing.T) {
	g, _ := newGuard(t)
	ctx := context.Background()
	for _, p := range []Policy{{}, {FailThreshold: 5}, {FailThreshold: 0, LockDuration: time.Minute}} {
		if _, _, err := g.Status(ctx, "u", p); !errors.Is(err, ErrWeakPolicy) {
			t.Fatalf("策略 %+v 应报 ErrWeakPolicy，实际 %v", p, err)
		}
		if _, _, err := g.RecordFailure(ctx, "u", p); !errors.Is(err, ErrWeakPolicy) {
			t.Fatalf("策略 %+v 应报 ErrWeakPolicy，实际 %v", p, err)
		}
	}
}

// 装配期守卫：空地址 / nil 客户端 / 未初始化对象都必须 fail-closed。
func TestConstructorGuards(t *testing.T) {
	if _, err := New("  ", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("空地址应报错，实际 %v", err)
	}
	if _, err := NewWithClient(nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil 客户端应报错，实际 %v", err)
	}
	var zero *Guard
	if _, _, err := zero.Status(context.Background(), "u", testPolicy()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil Guard 应报 ErrUnavailable，实际 %v", err)
	}
	if err := zero.Ping(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil Guard 的 Ping 应报错，实际 %v", err)
	}
	if err := zero.Reset(context.Background(), "u"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil Guard 的 Reset 应报错，实际 %v", err)
	}
	g, _ := New("127.0.0.1:1", "") // 不可达地址：构造成功，调用期 fail-closed
	if err := g.Ping(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("不可达地址的 Ping 应报 ErrUnavailable，实际 %v", err)
	}
}
