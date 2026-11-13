package agent

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/tjfoc/gmsm/sm3"

	"github.com/spencerswagger/guanjin/internal/crypto"
)

// spec/vectors/agent-hmac.json 的契约一致性向量（docs/sdk-contract.md §4.3 / D1）。
//
// 这不是"抄一份期望值"的测试：每条的 canonical 串由本包真实现（Canonical/SortedQuery）
// 现算，再与向量里的 expected_canonical 逐字节比；签名由 HMAC-SM3（gmsm）现算再比
// expected_signature。任何人改动 A1/A2 规则都会在这里红。
func TestSpecVectorsAgentHMAC(t *testing.T) {
	raw, err := os.ReadFile("../../spec/vectors/agent-hmac.json")
	if err != nil {
		t.Fatalf("读不到 spec/vectors/agent-hmac.json（仓库内路径必须存在）: %v", err)
	}
	var doc struct {
		SecretB64 string `json:"secret_b64"`
		Vectors   []struct {
			VectorID            string      `json:"vector_id"`
			Case                string      `json:"case"`
			Method              string      `json:"method"`
			Path                string      `json:"path"`
			Query               [][2]string `json:"query_pairs"`
			Timestamp           string      `json:"timestamp"`
			Nonce               string      `json:"nonce"`
			Body                string      `json:"body_utf8"`
			ExpectedSortedQuery string      `json:"expected_sorted_query"`
			ExpectedBodySM3     string      `json:"expected_body_sm3"`
			ExpectedCanonical   string      `json:"expected_canonical"`
			ExpectedSignature   string      `json:"expected_signature"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("agent-hmac.json 非法 JSON: %v", err)
	}
	key, err := base64.StdEncoding.DecodeString(doc.SecretB64)
	if err != nil {
		t.Fatalf("secret_b64 不是合法 base64: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("K 必须是 base64 解码后的 32 字节，得到 %d", len(key))
	}
	if len(doc.Vectors) < 10 {
		t.Fatalf("契约附录 C 要求 ≥10 条，得到 %d", len(doc.Vectors))
	}

	seen := map[string]bool{}
	for _, v := range doc.Vectors {
		if seen[v.VectorID] {
			t.Fatalf("vector_id 重复: %s", v.VectorID)
		}
		seen[v.VectorID] = true

		pairs := make([]KV, 0, len(v.Query))
		for _, p := range v.Query {
			pairs = append(pairs, KV{Name: p[0], Value: p[1]})
		}
		sq := SortedQuery(pairs)
		if sq != v.ExpectedSortedQuery {
			t.Errorf("%s(%s) SORTED_QUERY 不一致：\n got=%q\nwant=%q", v.VectorID, v.Case, sq, v.ExpectedSortedQuery)
			continue
		}
		bodySM3 := crypto.SM3Hex([]byte(v.Body))
		if bodySM3 != v.ExpectedBodySM3 {
			t.Errorf("%s(%s) body SM3 不一致：got=%s want=%s", v.VectorID, v.Case, bodySM3, v.ExpectedBodySM3)
			continue
		}
		canon := Canonical(v.Method, v.Path, sq, bodySM3, v.Timestamp, v.Nonce)
		if canon != v.ExpectedCanonical {
			t.Errorf("%s(%s) canonical 不一致：\n got=%q\nwant=%q", v.VectorID, v.Case, canon, v.ExpectedCanonical)
			continue
		}
		m := hmac.New(sm3.New, key)
		m.Write([]byte(canon))
		sig := base64.StdEncoding.EncodeToString(m.Sum(nil))
		if sig != v.ExpectedSignature {
			t.Errorf("%s(%s) 签名不一致：got=%s want=%s", v.VectorID, v.Case, sig, v.ExpectedSignature)
		}
	}
}
