package guanjin

import (
	"net/http"
	"path"
	"strings"
)

// BuiltinPublicPaths 是内置白名单（契约 #25：`public-paths-include-builtin` 默认 true）。
// **只含平台自身与静态资源，不含任何业务接口**——业务接口要公开必须显式声明
// （`AC-SDK-*` 的 deny-by-default 口径）。
var BuiltinPublicPaths = []string{
	"/healthz",
	"/livez",
	"/readyz",
	"/error",
	"/favicon.ico",
	"/robots.txt",
}

// PathMatcher 做公开路径判定（能力 GJ-CAP-19）：命中即放行，未命中一律要求身份。
//
// 模式语法（够用即止，不引入第三方路由库；只支持**结尾 `*`** 两种形态）：
//   - `/a/b`   → 精确匹配；
//   - `/a/*`   → 该前缀下**任意深度**（含 `/a` 自身）；
//   - `/a*`    → **字符串前缀**（无段边界：`/static*` 会命中 `/staticX/secret`，写声明时自己收紧）；
//   - `*` 出现在**中间**（如 `/api/*/public`）的模式**永不命中**（fail-closed：声明错=不放行，
//     不会悄悄放行；请改成结尾形态）。
//
// 比较区分大小写（URL 路径本就区分）。
type PathMatcher struct {
	patterns []string
}

// NewPathMatcher 构造匹配器：内置白名单（可选）+ 显式声明的 `public-paths`。
func NewPathMatcher(cfg Config) *PathMatcher {
	pats := make([]string, 0, len(cfg.PublicPaths)+len(BuiltinPublicPaths))
	if cfg.PublicPathsIncludeBuiltin {
		pats = append(pats, BuiltinPublicPaths...)
	}
	for _, p := range cfg.PublicPaths {
		if p = strings.TrimSpace(p); p != "" {
			pats = append(pats, p)
		}
	}
	return &PathMatcher{patterns: pats}
}

// IsPublic 判断请求是否公开（能力 GJ-CAP-19）。方法不参与判定（公开即整条路径公开）。
func (m *PathMatcher) IsPublic(path string) bool {
	path = normalizePath(path)
	for _, p := range m.patterns {
		if matchPath(p, path) {
			return true
		}
	}
	return false
}

// Patterns 返回生效的模式清单（启动自检要打印它，能力 GJ-CAP-22）。
func (m *PathMatcher) Patterns() []string { return append([]string(nil), m.patterns...) }

func normalizePath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	// `path.Clean` 折掉 `.`/`..`/重复斜杠：否则 `/public/../admin/secret` 会被判成公开路径
	// （路径穿越绕过 deny-by-default——#51 评审 P2，fail-closed 加固）。Clean 对已规范路径恒等。
	p = path.Clean(p)
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// matchPath 支持三种形态：精确、`/a/*`（任意深度前缀）、结尾 `*`（字符串前缀）。
// `*` 在中间的模式不命中（见 PathMatcher 注释：声明错 = 不放行，fail-closed）。
func matchPath(pattern, path string) bool {
	pattern = normalizePath(pattern)
	if pattern == path {
		return true
	}
	switch {
	case strings.HasSuffix(pattern, "/*"):
		prefix := strings.TrimSuffix(pattern, "/*")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(path, strings.TrimSuffix(pattern, "*"))
	default:
		return false
	}
}

// PublicPath 是给宿主判断用的便捷方法（能力 GJ-CAP-19）。
func (s *SDK) PublicPath(path string) bool { return s.matcher.IsPublic(path) }

// PublicPathRequest 判断一个 `*http.Request` 是否公开。
func (s *SDK) PublicPathRequest(r *http.Request) bool {
	return r != nil && s.matcher.IsPublic(r.URL.Path)
}
