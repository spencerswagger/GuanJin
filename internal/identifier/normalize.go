package identifier

import (
	"errors"
	"strings"
)

const (
	TypePhone    = "phone"
	TypeEmail    = "email"
	TypeUsername = "username"
)

var ErrInvalidFormat = errors.New("IDENTIFIER_INVALID_FORMAT")

func Normalize(typ, value string) (string, error) {
	switch typ {
	case TypeEmail:
		return normalizeEmail(value)
	case TypePhone:
		return normalizePhone(value)
	case TypeUsername:
		return normalizeUsername(value)
	default:
		v := strings.TrimSpace(value)
		if v == "" {
			return "", ErrInvalidFormat
		}
		return v, nil
	}
}

func normalizeEmail(value string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	at := strings.IndexByte(v, '@')
	if at <= 0 || at == len(v)-1 {
		return "", ErrInvalidFormat
	}
	domain := v[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", ErrInvalidFormat
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return "", ErrInvalidFormat
	}
	return v, nil
}

func normalizePhone(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", ErrInvalidFormat
	}
	plus := strings.HasPrefix(raw, "+") || strings.HasPrefix(raw, "00")
	digits := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c >= '0' && c <= '9':
			digits = append(digits, c)
		case c == '+' || c == '-' || c == ' ' || c == '(' || c == ')' || c == '.':
		default:
			return "", ErrInvalidFormat
		}
	}
	if len(digits) < 8 || len(digits) > 15 {
		return "", ErrInvalidFormat
	}
	if digits[0] == '0' {
		return "", ErrInvalidFormat
	}
	if plus {
		return "+" + string(digits), nil
	}
	return "+" + string(digits), nil
}

func normalizeUsername(value string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	if len(v) < 3 || len(v) > 64 {
		return "", ErrInvalidFormat
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.':
		default:
			return "", ErrInvalidFormat
		}
	}
	return v, nil
}
