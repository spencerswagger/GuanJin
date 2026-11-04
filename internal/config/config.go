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

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// Config 是进程级配置。**只从环境变量读取**，不从命令行、不读文件（密钥不落盘）。
type Config struct {
	// HTTPAddr 是监听地址，如 ":8080"。
	HTTPAddr string
	// DatabaseDSN 是 PostgreSQL 连接串。
	DatabaseDSN string
	// RedisAddr / RedisPassword 用于限流计数、jti 防重放、热缓存。
	RedisAddr     string
	RedisPassword string
	// MasterKey 是**已校验的主密钥对象**；只在进程内存，且只能用来派生（D4-B）。
	// 注意：这里刻意不暴露 []byte —— 主密钥原文一旦能自由传，就一定会被直传进算法。
	MasterKey *crypto.MasterKey
	// TraceHeader 是贯穿日志、auth_events、audit_logs 的 trace 头名（D4-C）。
	TraceHeader string
	// Issuer 是 OIDC `iss` 与 discovery 文档的基址（如 `https://auth.example.com`）。
	//
	// **可选**：只有装配协议面（S6）时才必需；未配置时由装配层报错（不在代码里写死域名）。
	Issuer string
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
		Issuer:        strings.TrimRight(strings.TrimSpace(getenv("GUANJIN_ISSUER")), "/"),
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

// parseMasterKey 解析 base64 主密钥并交给 crypto 做长度校验。
//
// 长度不足即拒绝（不截断、不补齐 —— 补齐会悄悄降低熵）；长度下限的唯一来源是
// crypto.MinMasterKeyLen，本包不另存一份数字。
func parseMasterKey(raw string) (*crypto.MasterKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("config: GUANJIN_MASTER_KEY is required (fail-closed)")
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("config: GUANJIN_MASTER_KEY must be standard base64: %w", err)
	}
	key, err := crypto.NewMasterKey(decoded)
	if err != nil {
		return nil, fmt.Errorf("config: GUANJIN_MASTER_KEY invalid (need >= %d bytes): %w",
			crypto.MinMasterKeyLen, err)
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
