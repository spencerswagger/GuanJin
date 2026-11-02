package identifier

import "testing"

func TestNormalizeEmail(t *testing.T) {
	got, err := NormalizeEmail("  Alice.Wang@Example.COM ")
	if err != nil || got != "alice.wang@example.com" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", "no-at", "a@", "@b", "a@b@c", "a b@c", "a@nodot"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Errorf("NormalizeEmail(%q) must fail", bad)
		}
	}
}

func TestNormalizePhone(t *testing.T) {
	if got, err := NormalizePhone("+86 138-0013-8000"); err != nil || got != "+8613800138000" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", "123", "0123456789", "+86x13800138000"} {
		if _, err := NormalizePhone(bad); err == nil {
			t.Errorf("NormalizePhone(%q) must fail", bad)
		}
	}
}

func TestNormalizeUsername(t *testing.T) {
	if got, err := NormalizeUsername(" Zhang.San_01 "); err != nil || got != "zhang.san_01" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, bad := range []string{"ab", "a b", "用户名", "a@b"} {
		if _, err := NormalizeUsername(bad); err == nil {
			t.Errorf("NormalizeUsername(%q) must fail", bad)
		}
	}
}

// 脱敏值永不含完整原文。
func TestMaskNeverLeaksFullValue(t *testing.T) {
	if got := Mask("email", "alice.wang@example.com"); got == "alice.wang@example.com" {
		t.Fatalf("mask leaked original: %s", got)
	}
	if got := Mask("phone", "+8613800138000"); got != "+86*******8000" {
		t.Fatalf("unexpected phone mask: %s", got)
	}
}
