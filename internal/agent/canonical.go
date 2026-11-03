// Package agent 是 Agent 面（SDK ↔ 平台）的协议落点。
//
// 口径（decisions.md D1，唯一拼装口径）：
//
//	canonical = METHOD "\n" PATH "\n" SORTED_QUERY "\n" SM3HEX(body) "\n" TIMESTAMP "\n" NONCE
//	X-GuanJin-Signature = Base64(HMAC-SM3(secret, canonical))
//
// A1：PATH 取**请求行里实际发出的 origin-form path**（含 context-path、不解码、不去尾斜杠、空 path → "/"）。
// A2：SORTED_QUERY 保留重复键、**先编码再排序**、name→value 两级字节序。
//
// 本包不实现 SM3/HMAC（密码学在 internal/crypto，由 Edge 在 S3 落地）；
// 这里只保证"两端能拼出逐字节相同的串"。
package agent

import (
	"sort"
	"strings"
)

// Canonical 按 D1 拼装待签串。
//
// 参数要求：
//   - method：大写 HTTP 方法；
//   - path：请求行原样 path（空传 ""，内部按 "/" 处理）；
//   - sortedQuery：已按 A2 规则排序并拼接的 query（无 query 传 ""）；
//   - bodySM3Hex：body 的小写 hex SM3 摘要；空 body 用 SM3("") 的固定值，**不得省略该段**；
//   - timestamp：Unix 秒；
//   - nonce：16~64 字节的 base64url（按字节计长）。
func Canonical(method, path, sortedQuery, bodySM3Hex, timestamp, nonce string) string {
	if path == "" {
		path = "/" // RFC 7230：空 path 等价 "/"
	}
	var b strings.Builder
	b.WriteString(method)
	b.WriteString("\n")
	b.WriteString(path)
	b.WriteString("\n")
	b.WriteString(sortedQuery)
	b.WriteString("\n")
	b.WriteString(bodySM3Hex)
	b.WriteString("\n")
	b.WriteString(timestamp)
	b.WriteString("\n")
	b.WriteString(nonce)
	return b.String()
}

// KV 是 query 的一个键值对（同名参数会出现多条）。
type KV struct {
	Name  string
	Value string
}

// SortedQuery 按 D1-A2 构造 SORTED_QUERY：
//  1. 每对 name/value 各自做 RFC3986 编码（保留 A-Za-z0-9-._~，其余按 UTF-8 取字节、大写十六进制）；
//  2. **编码之后**排序，避免"解码后比较"的歧义；
//  3. 排序键 = 编码后 name 的字节序，name 相同再比编码后 value（两级）；
//  4. 保留重复键（不去重、不合并）；
//  5. 用 "&" 连接 "name=value"。
func SortedQuery(pairs []KV) string {
	type enc struct{ n, v string }
	encs := make([]enc, 0, len(pairs))
	for _, p := range pairs {
		encs = append(encs, enc{n: PercentEncode(p.Name), v: PercentEncode(p.Value)})
	}
	sort.SliceStable(encs, func(i, j int) bool {
		if encs[i].n != encs[j].n {
			return encs[i].n < encs[j].n
		}
		return encs[i].v < encs[j].v
	})
	parts := make([]string, 0, len(encs))
	for _, e := range encs {
		parts = append(parts, e.n+"="+e.v)
	}
	return strings.Join(parts, "&")
}

const hexUpper = "0123456789ABCDEF"

// PercentEncode 是 RFC3986 编码：只放行 unreserved（A-Za-z0-9-._~），其余按 UTF-8 字节转 %XX（大写）。
// 注意与 url.QueryEscape 的区别：后者把空格编成 '+'、且不区分 unreserved 之外的语义，与 D1-A2 不一致。
func PercentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&0x0f])
		}
	}
	return b.String()
}
