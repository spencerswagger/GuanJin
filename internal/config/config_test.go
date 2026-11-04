package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"GUANJIN_DB_DSN":     "postgres://u:p@127.0.0.1:5432/db",
		"GUANJIN_REDIS_ADDR": "127.0.0.1:6379",
		"GUANJIN_MASTER_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}
}

func TestLoadOK(t *testing.T) {
	c, err := Load(env(validEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.HTTPAddr != ":8080" {
		t.Errorf("default addr = %q, want :8080", c.HTTPAddr)
	}
	if c.MasterKey == nil || c.MasterKey.Len() != 32 {
		t.Fatalf("master key must be a validated crypto object of 32 bytes, got %+v", c.MasterKey)
	}
	if c.TraceHeader != "X-Trace-Id" {
		t.Errorf("trace header = %q", c.TraceHeader)
	}
}

// fail-closed：缺任何必需项都必须报错，不许有"缺了就降级"的分支。
func TestLoadFailsClosed(t *testing.T) {
	cases := []struct{ name, key string }{
		{"no db dsn", "GUANJIN_DB_DSN"},
		{"no redis addr", "GUANJIN_REDIS_ADDR"},
		{"no master key", "GUANJIN_MASTER_KEY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validEnv()
			delete(m, tc.key)
			if _, err := Load(env(m)); err == nil {
				t.Fatalf("expected error when %s missing", tc.key)
			}
		})
	}
}

func TestMasterKeyRejectsShortAndBadBase64(t *testing.T) {
	m := validEnv()
	m["GUANJIN_MASTER_KEY"] = base64.StdEncoding.EncodeToString(make([]byte, 31))
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), ">= 32") {
		t.Fatalf("short key must be rejected, got %v", err)
	}

	m = validEnv()
	m["GUANJIN_MASTER_KEY"] = "not base64!!"
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("bad base64 must be rejected, got %v", err)
	}
}

func TestParseSeconds(t *testing.T) {
	if d, err := ParseSeconds("600"); err != nil || d.Seconds() != 600 {
		t.Fatalf("ParseSeconds(600) = %v, %v", d, err)
	}
	for _, bad := range []string{"", "abc", "0", "-5"} {
		if _, err := ParseSeconds(bad); err == nil {
			t.Errorf("ParseSeconds(%q) must fail", bad)
		}
	}
}
