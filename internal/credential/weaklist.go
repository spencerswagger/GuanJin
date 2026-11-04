package credential

import "strings"

// builtinWeakPasswords 是内置弱口令黑名单（小写，等值匹配）。
//
// 口径说明（避免"自由发挥"的误解）：
//   - 基线只要求"弱口令黑名单"（接口 §3.3 口令策略），**没有**规定名单内容、
//     也没有规定要按编辑距离/键盘序列做模糊判定。所以这里只做**等值命中**，
//     名单取公开常见弱口令的最小子集；
//   - 名单是**配置项**：`Policy.WeakPasswords` 非空时**完全替换**本表（不是追加）——
//     "内置 + 追加"听起来更友好，实际会让运维删不掉一条误报，属隐性行为；
//   - 不做"包含用户名/包含业务系统名"这类规则：基线没写，加了就是自造策略。
var builtinWeakPasswords = []string{
	"111111", "112233", "123123", "123321", "123456", "1234567", "12345678",
	"123456789", "1234567890", "1q2w3e4r", "1qaz2wsx", "5201314", "666666",
	"888888", "987654321", "aa123456", "abc123", "abcd1234", "admin", "admin123",
	"asdfgh", "changeme", "charlie", "dragon", "football", "guanjin123", "hello123",
	"iloveyou", "letmein", "monkey", "passw0rd", "password", "password1",
	"password123", "p@ssw0rd", "princess", "qazwsx", "qwerty", "qwerty123",
	"sunshine", "test123", "welcome", "welcome1", "zxcvbn", "zxcvbnm",
}

// builtinWeakSet 是 builtinWeakPasswords 的查找集（小写）。
var builtinWeakSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(builtinWeakPasswords))
	for _, w := range builtinWeakPasswords {
		m[w] = struct{}{}
	}
	return m
}()

// isWeakPassword 判断口令是否命中弱口令名单。
//
// 归一方式只有一种：去首尾空白 + 转小写。**不**做去数字后缀、去标点这类"聪明"归一——
// 那会让"password2026!"同时命中与不命中，取决于实现细节，是不可解释的策略。
func isWeakPassword(password string, custom []string) bool {
	candidate := strings.ToLower(strings.TrimSpace(password))
	if candidate == "" {
		return false
	}
	if custom != nil {
		for _, w := range custom {
			if strings.ToLower(strings.TrimSpace(w)) == candidate {
				return true
			}
		}
		return false
	}
	_, hit := builtinWeakSet[candidate]
	return hit
}
