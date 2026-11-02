// Package identifier 负责标识符的规范化、脱敏与不变式校验。
//
// 口径（decisions.md D4 / 数据模型 §3.2）：
//   - 三段式存储：盲索引 HMAC-SM3(value) + SM4 密文 value_enc + 脱敏 value_masked；
//   - 规范化必须在**算盲索引之前**完成（否则同一邮箱大小写不同会变成两条记录）；
//   - 本包只做纯函数规范化与校验；密码学实现由 internal/crypto 提供（S3 落地）。
package identifier

import (
	"errors"
	"strings"
)

// ErrInvalidIdentifier 表示标识符不合法（对外映射 E1 / 400）。
var ErrInvalidIdentifier = errors.New("identifier: invalid")

// NormalizeEmail 小写 + 去首尾空白。不做「本地部分去点」这类改写（会把别人的邮箱合成一个）。
func NormalizeEmail(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if strings.ContainsAny(s, " \t\r\n") {
		return "", ErrInvalidIdentifier
	}
	at := strings.LastIndex(s, "@")
	if at <= 0 || at == len(s)-1 || strings.Count(s, "@") != 1 {
		return "", ErrInvalidIdentifier
	}
	domain := s[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", ErrInvalidIdentifier
	}
	return s, nil
}

// NormalizePhone 归一为 E.164 形态（仅保留数字与可选的 + 前缀），长度 8..15。
// V1 不做区号智能补全（没有国家上下文时补全＝猜，属隐性逻辑）。
func NormalizePhone(raw string) (string, error) {
	var b strings.Builder
	for i, r := range strings.TrimSpace(raw) {
		switch {
		case r == '+' && i == 0:
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '(' || r == ')':
			// 分隔符丢弃
		default:
			return "", ErrInvalidIdentifier
		}
	}
	digits := strings.TrimPrefix(b.String(), "+")
	if len(digits) < 8 || len(digits) > 15 || digits[0] == '0' {
		return "", ErrInvalidIdentifier // E.164 不以 0 开头
	}
	return "+" + digits, nil
}

// NormalizeUsername 小写 + 去首尾空白；长度 3..64；只允许 ASCII 字母数字与 . _ -。
// 不做 Unicode 归一化：避免全角/兼容字符把两个不同用户名折叠成一个。
func NormalizeUsername(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if len(s) < 3 || len(s) > 64 {
		return "", ErrInvalidIdentifier
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return "", ErrInvalidIdentifier
		}
	}
	return s, nil
}

// Mask 生成脱敏展示值（138****8000 / a***@x.com）。**永不返回原文**。
func Mask(kind, normalized string) string {
	switch kind {
	case "email":
		at := strings.LastIndex(normalized, "@")
		if at <= 0 {
			return "***"
		}
		local, domain := normalized[:at], normalized[at:]
		if len(local) <= 1 {
			return "*" + domain
		}
		return local[:1] + strings.Repeat("*", len(local)-1) + domain
	case "phone":
		if len(normalized) < 7 {
			return "***"
		}
		return normalized[:3] + strings.Repeat("*", len(normalized)-7) + normalized[len(normalized)-4:]
	default:
		if len(normalized) <= 2 {
			return "**"
		}
		return normalized[:1] + strings.Repeat("*", len(normalized)-2) + normalized[len(normalized)-1:]
	}
}
