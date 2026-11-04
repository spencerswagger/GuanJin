package main

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// 装配期 fail-fast 的**可执行证据**：这两条不需要真库，所以在主闸（`go test ./...`）里每次都跑。
//
// 第 3 道（D13 三档 tier_default）要靠真库，正反两面在
// `internal/store/policy_seed_db_test.go`（integration）里钉住；
// 这里只验「它会挡住启动」这一层的接线：startup 返回非 nil error → main 以非 0 退出。

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// envMap 把 map 变成 config.Load 需要的 getenv。
func envMap(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// 主密钥用**测试里的假材料**（32 字节 0x01 的 base64）；它不是生产密钥，也不落任何文件。
func testMasterKeyB64() string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = 0x01
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// 配置缺失 → startup 失败（不认识半截配置）。
func TestStartupFailsOnMissingConfig(t *testing.T) {
	_, err := startup(context.Background(), envMap(map[string]string{}), discardLogger())
	if err == nil {
		t.Fatal("缺配置必须失败（fail-closed）")
	}
	if !strings.Contains(err.Error(), "GUANJIN_DB_DSN") {
		t.Fatalf("错误信息应点名缺失项，实际：%v", err)
	}
}

// 配置齐但库不可达 → startup 失败在 Ping，而不是起一个"看起来正常"的进程。
func TestStartupFailsOnUnreachableDB(t *testing.T) {
	_, err := startup(context.Background(), envMap(map[string]string{
		"GUANJIN_DB_DSN":     "postgres://user:pass@127.0.0.1:1/guanjin_test?sslmode=disable",
		"GUANJIN_REDIS_ADDR": "127.0.0.1:1",
		"GUANJIN_MASTER_KEY": testMasterKeyB64(),
	}), discardLogger())
	if err == nil {
		t.Fatal("库不可达必须失败（不得降级放行）")
	}
	if !strings.Contains(err.Error(), "database not usable") {
		t.Fatalf("错误信息应指明是数据库不可用，实际：%v", err)
	}
}

// 端口未监听时 close() 也必须安全（装配失败路径上会调到）。
func TestAppCloseNilSafe(t *testing.T) {
	if err := (*app)(nil).close(); err != nil {
		t.Fatalf("nil app close = %v, want nil", err)
	}
	if err := (&app{}).close(); err != nil {
		t.Fatalf("empty app close = %v, want nil", err)
	}
}
