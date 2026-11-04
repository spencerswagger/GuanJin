package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AC-DP-11：`guanjingw` **不自己实现** TLS / WAF / 负载均衡 / 灰度 / 熔断。
//
// 这条判据靠"脚本 + 依赖清单举证"，不是靠谁记得。做法：扫**生产代码**（不含测试）
// 里是否出现这些能力的实现痕迹。命中即失败——包括"先预留一个开关"这种写法，
// 因为预留开关就是"跳过校验的暗门"，D8/AC-DP-11 明确要求不设。
func TestGatewayImplementsNoTLSOrResilience(t *testing.T) {
	// 测试的工作目录就是本包目录，所以扫 "."（写 "internal/gateway" 会一条都命中不到，
	// 而"扫不到"必须当失败处理——否则这条判据会在无人察觉时失效）。
	sources := map[string][]string{
		".": {".go"},
	}
	// 也扫数据面入口：装配层最容易偷偷加 TLS 或熔断。
	extra := []string{filepath.Join("..", "..", "cmd", "guanjingw", "main.go")}

	forbidden := []struct {
		pattern string
		why     string
	}{
		{"crypto/tls", "TLS 终结属部署方前置（P2②/AC-DP-11），本平台不做"},
		{"tls.Listen", "自建 TLS 监听 = 把传输层安全做进平台，越界"},
		{"autocert", "证书自动签发属部署方"},
		{"gobreaker", "熔断由前置承担，平台不自研"},
		{"srebreaker", "熔断由前置承担，平台不自研"},
		{"SkipVerify", "任何 InsecureSkipVerify 都是验签暗门"},
		{"InsecureSkipVerify", "任何 InsecureSkipVerify 都是验签暗门"},
	}

	var files []string
	for dir, exts := range sources {
		for _, ext := range exts {
			matches, err := filepath.Glob(filepath.Join(dir, "*"+ext))
			if err != nil {
				t.Fatalf("glob %s: %v", dir, err)
			}
			for _, m := range matches {
				if strings.HasSuffix(m, "_test.go") {
					continue // 测试里允许出现（比如断言"不存在"）
				}
				files = append(files, m)
			}
		}
	}
	files = append(files, extra...)

	if len(files) < 2 {
		t.Fatalf("扫描文件数异常（%d），检查 glob 路径", len(files))
	}

	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // 注释里说明"不做 TLS"是正常的
			}
			for _, f := range forbidden {
				if strings.Contains(line, f.pattern) {
					t.Errorf("%s:%d 出现 %q —— %s", file, i+1, f.pattern, f.why)
				}
			}
		}
	}
}

// 依赖清单举证：模块直接依赖里不得出现熔断 / TLS 自动签发类库。
func TestGoModHasNoResilienceOrCertDeps(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	forbidden := []string{"gobreaker", "autocert", "certmagic", "hystrix", "sony/"}
	lower := strings.ToLower(string(raw))
	for _, f := range forbidden {
		if strings.Contains(lower, f) {
			t.Errorf("go.mod 出现 %q —— 熔断/证书签发属部署方，不引库", f)
		}
	}
}
