package guanjin

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSdkDoesNotImportPlatformInternals 把「SDK 不依赖平台内部实现」这条**从注释变成断言**。
//
// 依据：契约 §4.2（允许差异只有打包物与接入点，不允许能力/配置/错误码差异）与
// `decisions.md` D17；#51 正文亦写明「未 import internal/*」——SDK 是要**交付给业务方**的构件，
// 一旦 import `internal/*`，业务方在本仓之外根本编不过，等于把"最小接入 SDK"做成"只能在本仓里用"。
//
// 反面证据（变异自证）：把 `internal/crypto` 加进本包任一文件的 import → 本用例红。
func TestSdkDoesNotImportPlatformInternals(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读包目录失败: %v", err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue // 只查生产代码：测试可以借用本仓能力（它不随交付物出门）
		}
		af, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", e.Name(), err)
		}
		checked++
		for _, imp := range af.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s 的 import 字面量不可解析: %v", e.Name(), err)
			}
			if strings.Contains(path, "/internal/") || strings.HasPrefix(path, "internal/") {
				t.Fatalf("%s import 了平台内部包 %q：SDK 交付物不得依赖 internal/*（契约 §4.2 / #51）",
					e.Name(), path)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("只扫到 %d 个生产 .go 文件：守线没生效（预期 ≥5）", checked)
	}
}

// TestSdkHasNoGoModOfItsOwn 钉住当前模块形态：契约 §5.3.2 **案 B**（主 module 的子包）。
//
// 若将来改 **案 A（独立 module）**，本用例会红——这正是我们要的：改形态要显式改这条断言
// 与 `go.mod`，不能悄悄变成"半独立"（父 module 与子 module 同时存在会导致
// `go test ./...` 漏测、CI 覆盖率并集少算）。
func TestSdkHasNoGoModOfItsOwn(t *testing.T) {
	if _, err := os.Stat("go.mod"); err == nil {
		t.Fatalf("sdk/go/guanjin 下出现 go.mod：模块形态变了（契约 §5.3.2 案 A），须同步改 CI 与覆盖率采集")
	} else if !os.IsNotExist(err) {
		t.Fatalf("检查 go.mod 失败: %v", err)
	}
}
