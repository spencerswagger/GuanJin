package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Route 是 `route → client` 的**静态**映射（索引 §4.2-18）：一个路径前缀归属一个业务系统。
//
// 为什么是静态且按路径前缀：V1 不做服务发现、不做正则路由（AC-DP-11 明确"无业务路由发现"，
// 那些属前置 nginx/Kong）。静态表的好处是「谁能转发到哪」可以在启动期一次性校验完，
// 运行期没有"猜"的空间。
type Route struct {
	// Prefix 是路径前缀，必须以 `/` 开头、不以 `/` 结尾（除根路径 `/` 本身）。
	Prefix string
	// ClientID 是该前缀归属的业务系统（`clients.client_id`，冻结名）。
	ClientID string
	// Upstream 是上游基址（scheme+host[+path 前缀]）。
	Upstream *url.URL
}

// Router 是一组 Route 的匹配器。构造后**只读**（并发安全）。
type Router struct {
	routes []Route
}

// 路由表校验的错误（sentinel，装配期 fail-closed）。
var (
	// ErrRouteEmpty 表示路由项缺字段。
	ErrRouteEmpty = errors.New("gateway: route requires prefix, client_id and upstream")
	// ErrRouteBadPrefix 表示前缀形态非法。
	ErrRouteBadPrefix = errors.New("gateway: route prefix must start with '/' and must not end with '/' (except \"/\")")
	// ErrRouteBadUpstream 表示上游地址非法。
	ErrRouteBadUpstream = errors.New("gateway: route upstream must be an absolute http/https URL")
	// ErrRouteDuplicate 表示同一前缀被映射了两次。
	ErrRouteDuplicate = errors.New("gateway: duplicate route prefix")
)

// NewRouter 校验并冻结路由表。
//
// 校验全部在**装配期**做完（fail-fast）：前缀形态、上游 scheme、重复前缀。
// 任何一个不合法都直接报错，不"跳过坏的那条继续跑"——静默跳过会让某个业务系统
// 在运行期忽然 503，而日志里看不出是配置写错。
func NewRouter(routes []Route) (*Router, error) {
	seen := make(map[string]string, len(routes))
	out := make([]Route, 0, len(routes))
	for i, rt := range routes {
		if strings.TrimSpace(rt.Prefix) == "" || strings.TrimSpace(rt.ClientID) == "" || rt.Upstream == nil {
			return nil, fmt.Errorf("%w (index %d)", ErrRouteEmpty, i)
		}
		prefix := rt.Prefix
		if !strings.HasPrefix(prefix, "/") {
			return nil, fmt.Errorf("%w: %q", ErrRouteBadPrefix, prefix)
		}
		if prefix != "/" && strings.HasSuffix(prefix, "/") {
			return nil, fmt.Errorf("%w: %q", ErrRouteBadPrefix, prefix)
		}
		if prev, dup := seen[prefix]; dup {
			return nil, fmt.Errorf("%w: %q (already mapped to client %q)", ErrRouteDuplicate, prefix, prev)
		}
		seen[prefix] = rt.ClientID

		if rt.Upstream.Scheme != "http" && rt.Upstream.Scheme != "https" {
			return nil, fmt.Errorf("%w: %q", ErrRouteBadUpstream, rt.Upstream.String())
		}
		if rt.Upstream.Host == "" {
			return nil, fmt.Errorf("%w: %q", ErrRouteBadUpstream, rt.Upstream.String())
		}

		up := *rt.Upstream
		out = append(out, Route{Prefix: prefix, ClientID: rt.ClientID, Upstream: &up})
	}

	// 长前缀优先：`/api/billing` 必须先于 `/api` 命中。
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Prefix) > len(out[j].Prefix) })
	return &Router{routes: out}, nil
}

// Routes 返回冻结后的路由表副本（只读用途：启动日志、诊断）。
func (r *Router) Routes() []Route {
	if r == nil {
		return nil
	}
	out := make([]Route, len(r.routes))
	copy(out, r.routes)
	return out
}

// Match 按路径找归属。**按路径段匹配**：前缀 `/api/bill` 命中 `/api/bill` 与
// `/api/bill/x`，但**不命中** `/api/billing` —— 字符串前缀匹配会把两个业务系统混成一个，
// 那是越权转发（把 A 的流量送去 B 的 client 身份），属安全事故而不是配置瑕疵。
func (r *Router) Match(path string) (Route, bool) {
	if r == nil {
		return Route{}, false
	}
	if path == "" {
		path = "/" // RFC 7230：空 path 等价 "/"
	}
	for _, rt := range r.routes {
		if rt.Prefix == "/" {
			// 根路由兜底：任何路径都不以前缀之外的形式排除。
			return rt, true
		}
		if path == rt.Prefix || strings.HasPrefix(path, rt.Prefix+"/") {
			return rt, true
		}
	}
	return Route{}, false
}

// parseRouteSpec 是路由表 JSON 的一项（只用于配置解析）。
type parseRouteSpec struct {
	Prefix   string `json:"prefix"`
	ClientID string `json:"client_id"`
	Upstream string `json:"upstream"`
}

// ParseRoutes 解析路由表 JSON：`[{"prefix":"/billing","client_id":"billing","upstream":"http://127.0.0.1:9101"}]`。
//
// 为什么用 JSON 而不是 `k=v` 串：路由项有三个字段且上游 URL 里本来就带 `=`/`?`，
// 自定义分隔符迟早把 URL 切坏；JSON 还能让配置错误在解析期就报出来（带下标）。
func ParseRoutes(raw string) ([]Route, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var specs []parseRouteSpec
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return nil, fmt.Errorf("gateway: routes must be a JSON array: %w", err)
	}
	routes := make([]Route, 0, len(specs))
	for i, s := range specs {
		up, err := url.Parse(strings.TrimSpace(s.Upstream))
		if err != nil {
			return nil, fmt.Errorf("gateway: route[%d] upstream %q: %w", i, s.Upstream, err)
		}
		routes = append(routes, Route{
			Prefix:   strings.TrimSpace(s.Prefix),
			ClientID: strings.TrimSpace(s.ClientID),
			Upstream: up,
		})
	}
	return routes, nil
}
