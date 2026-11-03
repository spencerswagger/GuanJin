package agent

import "testing"

func TestCanonicalShape(t *testing.T) {
	got := Canonical("POST", "/api/v1/agent/report", "a=1&b=2", "abc123", "1730000000", "n1")
	want := "POST\n/api/v1/agent/report\na=1&b=2\nabc123\n1730000000\nn1"
	if got != want {
		t.Fatalf("canonical mismatch:\n got %q\nwant %q", got, want)
	}
}

// A1：空 path 按 "/"；不去尾斜杠、不解码（两端取到同一事实）。
func TestCanonicalPathRules(t *testing.T) {
	if got := Canonical("GET", "", "", "h", "1", "n"); got != "GET\n/\n\nh\n1\nn" {
		t.Fatalf("empty path must become /: %q", got)
	}
	if Canonical("GET", "/a/", "", "h", "1", "n") == Canonical("GET", "/a", "", "h", "1", "n") {
		t.Fatalf("/a/ and /a must differ (no trailing-slash normalization)")
	}
	if Canonical("GET", "/a%2Fb", "", "h", "1", "n") == Canonical("GET", "/a/b", "", "h", "1", "n") {
		t.Fatalf("path must not be URL-decoded")
	}
}

// A2：同名参数保留、编码先于排序、两级字节序。
func TestSortedQuery(t *testing.T) {
	got := SortedQuery([]KV{
		{"b", "2"}, {"a", "z"}, {"a", "y"}, {"c", "空格"}, {"a", "z"},
	})
	// 编码后排序：a=y < a=z < a=z(dup) < b=2 < c=%E7%A9%BA%E6%A0%BC
	want := "a=y&a=z&a=z&b=2&c=%E7%A9%BA%E6%A0%BC"
	if got != want {
		t.Fatalf("sorted query:\n got %q\nwant %q", got, want)
	}
	if SortedQuery(nil) != "" {
		t.Fatalf("no query must be empty string, never omitted")
	}
}

func TestPercentEncode(t *testing.T) {
	if got := PercentEncode("a b&c=d/e~f"); got != "a%20b%26c%3Dd%2Fe~f" {
		t.Fatalf("percent encode = %q", got)
	}
	// 大小写十六进制必须是**大写**（避免两端大小写不一致）
	if got := PercentEncode("\u00e9"); got != "%C3%A9" {
		t.Fatalf("utf-8 bytes must be uppercase hex, got %q", got)
	}
}
