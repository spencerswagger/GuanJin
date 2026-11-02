// Package config 负责启动期配置的加载与「fail-fast」校验。
//
// 口径（decisions.md）：
//   - D4-B：主密钥只从环境变量注入、只做 HKDF-SM3 派生的输入，不进任何业务算法、不落库；
//   - P1：不做自动迁移 —— 本包绝不触发 schema 变更；
//   - D5：flow.ttl_seconds 必须由 runtime_config 提供，代码里不得出现 600 常量。
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// MinMasterKeyLen 是主密钥的最小字节数（HKDF-SM3 派生 32B 子密钥要求输入熵足够）。
const MinMasterKeyLen = 32

// Config 是进程级配置。**只从环境变量读取**，不从命令行、不读文件（密钥不落盘）。
type Config struct {
	// HTTPAddr 是监听地址，如 ":8080"。
	HTTPAddr string
	// DatabaseDSN 是 PostgreSQL 连接串。
	DatabaseDSN string
	// RedisAddr / RedisPassword 用于限流计数、jti 防重放、热缓存。
	RedisAddr     string
	RedisPassword string
	// MasterKey 是主密钥原文；**只在进程内存**，派生后不保留在业务结构里。
	MasterKey []byte
	// TraceHeader 是贯穿日志、auth_events、audit_logs 的 trace 头名（D4-C）。
	TraceHeader string
}

// Load 读取环境变量并做 fail-fast 校验。
//
// fail-closed（项目硬约束）：任何必需项缺失或非法 → 返回错误，由 main 以非 0 退出，
// **不存在"缺了就降级放行"的分支**。
func Load(getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}

	c := &Config{
		HTTPAddr:      strings.TrimSpace(getenv("GUANJIN_HTTP_ADDR")),
		DatabaseDSN:   strings.TrimSpace(getenv("GUANJIN_DB_DSN")),
		RedisAddr:     strings.TrimSpace(getenv("GUANJIN_REDIS_ADDR")),
		RedisPassword: getenv("GUANJIN_REDIS_PASSWORD"),
		TraceHeader:   strings.TrimSpace(getenv("GUANJIN_TRACE_HEADER")),
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8080"
	}
	if c.TraceHeader == "" {
		c.TraceHeader = "X-Trace-Id"
	}

	if c.DatabaseDSN == "" {
		return nil, errors.New("config: GUANJIN_DB_DSN is required (fail-closed)")
	}
	if c.RedisAddr == "" {
		return nil, errors.New("config: GUANJIN_REDIS_ADDR is required (fail-closed)")
	}

	key, err := parseMasterKey(getenv("GUANJIN_MASTER_KEY"))
	if err != nil {
		return nil, err
	}
	c.MasterKey = key
	return c, nil
}

// parseMasterKey 解析 base64 主密钥；长度不足即拒绝（不截断、不补齐 —— 补齐会悄悄降低熵）。
func parseMasterKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("config: GUANJIN_MASTER_KEY is required (fail-closed)")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("config: GUANJIN_MASTER_KEY must be standard base64: %w", err)
	}
	if len(key) < MinMasterKeyLen {
		return nil, fmt.Errorf("config: GUANJIN_MASTER_KEY must be >= %d bytes, got %d", MinMasterKeyLen, len(key))
	}
	return key, nil
}

// ParseSeconds 解析形如 "600" 的秒数（runtime_config 的 int 值口径）。
// 用于 flow.ttl_seconds：**键缺失必须由调用方按 fail-fast 处理**，不得回退默认值。
func ParseSeconds(raw string) (time.Duration, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("config: not an integer second: %q", raw)
	}
	if n <= 0 {
		return 0, fmt.Errorf("config: must be > 0, got %d", n)
	}
	return time.Duration(n) * time.Second, nil
}
