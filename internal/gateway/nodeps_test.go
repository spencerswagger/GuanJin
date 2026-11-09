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

// TestGatewayNeverStoresOrComparesJTI 是 **AC-DP-08 归属裁定**的结构性守线（反向断言，防"好心补错"）。
//
// 裁定（Spec，2026-11-10；裁定稿 `notes/ac-dp-08-归属裁定-2026-11-10.md`，未入库前以本条注释为准）：
//   - **S8 只判一半**：每请求现签新 `jti`、被拒时零注入；
//   - **「同 `jti` 二次 → 401」由验签方产生并记账**（SDK / S9），依据 `数据面设计` §4.6 第 3 步
//     「校验 `exp` 并用 `jti` 做重放检测」、`接口清单` §5.2/时序自环在 `SDK->>SDK`、
//     `SDK契约` 能力 7 与 §7「`jti` 重放 → 401」、`m1-slices` S9 行「无（SDK 侧本地 `jti` 落 Redis，不碰平台库）」、
//     `数据模型规格` 全文 `jti` 零命中。
//
// 因此：`internal/gateway`（含 `adapter`）一旦出现 **`jti` 的存储或比对** 即判红。
// 允许的只有"生成"（`uuid.NewV7()`）与"放进 claim"。这条判据值得一条机器守线，因为
// 平台侧判重看起来"更安全"，但会（1）让数据面多一条每请求写路径、（2）与 SDK 侧判重形成两套口径、
// （3）把 401 的归属从验签方搬到转发方，`AC-DP-08` 的账目就再也说不清在哪一半。
func TestGatewayNeverStoresOrComparesJTI(t *testing.T) {
	// 正控（自检）：这些片段就是"平台侧偷偷判重"的典型写法，必须命中——
	// 否则守线会因为在改模式串时手滑而静默失效（等于没有这条闸）。
	for _, snippet := range []string{
		`ok, err := g.rdb.SetNX(ctx, "jti:"+jti, 1, window).Result()`,
		`INSERT INTO jti_replay (jti, expires_at) VALUES (?, ?)`,
		`if seenJTI[jti] { return false }`,
		`if a.JTI == lastJTI {`,
		`SELECT 1 FROM jti_seen WHERE jti = ?`,
	} {
		if len(jtiForbiddenHits(snippet)) == 0 {
			t.Fatalf("守线自检失败：%q 应被判红（说明模式串被改坏了）", snippet)
		}
	}
	// 反控：正常的"生成 + 放进 claim"不得误伤。
	for _, snippet := range []string{
		`jti, err := newJTI()`,
		`claims["jti"] = jti`,
		`if jti == "" { return Assertion{}, errors.New("empty jti") }`,
		`seen := make(map[string]bool)` + `\n` + `seen[prefix] = true`,
	} {
		if hits := jtiForbiddenHits(snippet); len(hits) != 0 {
			t.Fatalf("误伤：%q 命中 %v（生成/空值校验不是「存储或比对」）", snippet, hits)
		}
	}

	files := guardSources(t)

	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // 注释里写"不判重"是正常的（本包多处注释解释了裁定）
			}
			for _, pattern := range jtiForbiddenHits(line) {
				t.Errorf("%s:%d 出现 %q —— AC-DP-08 的平台侧只允许「生成 jti + 放进 claim」；"+
					"「同 jti 二次 → 401」归验签方（SDK/S9），裁定见 notes/ac-dp-08-归属裁定-2026-11-10.md",
					file, i+1, pattern)
			}
		}
	}
}

// guardMustScan 是守线**必须扫到**的文件（相对本包目录）。
//
// 维护方式：这些是网关核心与适配层里会承载"判重/存储"语义的文件；改名、移动、拆包时
// 同步更新这里。它的作用不是"列清单免责"，而是让"扫不到"变成 Fatal——
// 守线的价值在于**以后也红**，而不是今天这一次红。
var guardMustScan = []string{
	"admit.go", "inject.go", "pipeline.go", "proxy.go", "revoke.go", "route.go", "slo.go",
	filepath.Join("adapter", "adapter.go"),
}

// guardSources 递归收集本包（含子包）的**生产代码**文件。
//
// 为什么递归：早先只扫 `.` 与 `./adapter` 两层，新增任何子目录（如 `internal/gateway/x/`）
// 都会自动落在守线之外——"扫不到"必须当失败，否则这条闸会在无人察觉时失效。
// 同时自检：必须真的看到 `adapter` 目录（结构一变就先红，而不是悄悄少扫一半）。
func guardSources(t *testing.T) []string {
	t.Helper()
	var files []string
	dirs := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs[path] = true
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk 包目录失败：%v", err)
	}
	if !dirs[filepath.Join(".", "adapter")] {
		t.Fatalf("递归扫描没看到 adapter 子包（WalkDir 起点写错？该子包一旦跳过，adapter 里的判重实现就漏检）：%v", dirs)
	}

	// 逐个点名"必须扫到"的文件，而不是比一个魔数：
	//   - 只判文件总数，会在"新增文件让数字变大"时误绿（正好掩盖漏扫）；
	//   - 只判"大于 0"，会在 WalkDir 部分失败时误绿。
	// 点了名的文件若被改名/移动，这里会 Fatal 并提示同步 `guardMustScan`——宁可红一次，
	// 也不要让守线在无人察觉时失效。
	got := make(map[string]bool, len(files))
	for _, f := range files {
		got[filepath.Clean(f)] = true
	}
	for _, want := range guardMustScan {
		if !got[filepath.Clean(want)] {
			t.Fatalf("递归扫描漏了 %q（文件改名/移动了？请同步 `guardMustScan`）：已扫到 %v", want, files)
		}
	}
	if len(files) < len(guardMustScan) {
		t.Fatalf("扫描文件数异常（%d < %d）：扫不到必须当失败", len(files), len(guardMustScan))
	}
	return files
}

// jtiForbiddenHits 返回该行命中的"平台侧判重"模式（存储或比对），空 = 干净。
//
// 模式刻意收窄：**只命中"落存储/查存储/比两个 jti"**，不误伤 `jti == ""`（空值校验）
// 与 `seen[...]`（路由去重、amr 并集等与 jti 无关的 map）。收窄的代价是覆盖不到
// 更绕的写法，但那类写法在评审里也藏不住；宁可精确，不要一条满屏误报最后被人关掉。
func jtiForbiddenHits(line string) []string {
	patterns := []string{
		"SetNX", "setnx", // 首次占位语义 = 判重存储
		"redis.", // 数据面不碰 Redis
		"INSERT INTO", "DELETE FROM", "UPDATE sessions", "SELECT 1 FROM jti",
		"jtiSeen", "seenJTI", "jtiStore", "storeJTI", "saveJTI", "claimJTI", "jtiReplay",
		".JTI ==", "== .JTI", "JTI !=", "!= .JTI",
		"EqualFold(jti", "EqualFold(a.JTI",
	}
	var hits []string
	for _, p := range patterns {
		if strings.Contains(line, p) {
			hits = append(hits, p)
		}
	}
	return hits
}
