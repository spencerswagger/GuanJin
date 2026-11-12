// 命令 example 是 Go SDK 的**可运行示范**（S9 退出条件里的"最小接入跑通"）：
//
//	go run ./sdk/go/example
//
// 它做四件事，全部真实执行（无 mock 断言库、无 sleep 兜底）：
//
//	① 起一个**平台桩**：HTTP 服务只暴露 `GET /.well-known/jwks.json`（SM2 公钥，JB/T 形态不变）；
//	② 用 SM2 私钥签三种内部断言：合法 / 被篡改 / 已过期；
//	③ 把 SDK 中间件挂在一个业务 handler 外层，逐个请求打进去；
//	④ 打印每条请求的**状态码 + 错误码 + handler 侧看到的东西**——重点看两列：
//	   `断言头`（handler 侧必须是空，D10 第 ⑤ 步替换视图）与 `身份`（验签通过才有）。
//
// 它同时是"SDK 不依赖平台内部实现"的证据：只 import 公开 API + 第三方国密库，
// 不 import `internal/*`（契约 §4.2「不允许差异」+ #51 口径）。
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/tjfoc/gmsm/sm2"

	guanjin "github.com/spencerswagger/guanjin/sdk/go/guanjin"
)

const (
	clientID = "order-service"
	kid      = "demo-kid-1"
)

func main() {
	// ① 平台桩：只提供 JWKS。真实部署里这是 guanjind 的公开端点。
	priv, err := sm2.GenerateKey(rand.Reader)
	must(err, "生成 SM2 密钥")
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/jwks.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwksDoc(kid, &priv.PublicKey))
	}))
	defer platform.Close()

	// ② 配置：29 项交付集里的最小子集（键名逐字来自契约，不写别名）。
	cfg := guanjin.DefaultConfig()
	cfg.Platform.BaseURL = platform.URL
	cfg.Client.ID = clientID
	cfg.Client.HMACSecret = "demo-secret"
	// 内存 jti 只允许在 local/test 生效；这里显式声明 test（SDK 校验，不是注释约定）。
	cfg.Authz.ReplayStore = guanjin.ReplayStoreMemory

	sdk, err := guanjin.New(cfg,
		guanjin.WithEnv(guanjin.EnvTest),
		guanjin.WithLogger(func(string, ...any) {}), // 演示里不打 SDK 日志
	)
	must(err, "SDK 启动（含配置校验 + 公开路径编译）")

	// ③ 业务 handler：三列信息足够证明 D10 六步的落地效果。
	biz := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := "（无）"
		if p, ok := guanjin.PrincipalFrom(r.Context()); ok {
			identity = fmt.Sprintf("sub=%s uname=%s roles=%v acr=%s", p.Sub, p.Uname, p.Roles, p.Acr)
		}
		// handler 侧看到的断言头必须是空的（D10 ⑤ 替换了请求视图）。
		seen := "空"
		if v := r.Header.Get(guanjin.DefaultAssertionHeader); v != "" {
			seen = "泄漏(" + v[:12] + "…)"
		}
		// 下游转发看到的也是同一个视图：这里用 Clone 模拟再传一层。
		if v := r.Clone(r.Context()).Header.Get(guanjin.DefaultAssertionHeader); v != "" {
			seen = "下游泄漏"
		}
		fmt.Fprintf(w, "身份=%s | 断言头=%s", identity, seen)
	})

	handler := sdk.Middleware(biz)
	now := time.Now().Unix()
	cases := []struct {
		name string
		tok  string
	}{
		{"合法断言", sign(mustTok(priv, kid, claims(now)))},
		{"被篡改的断言", tamper(sign(mustTok(priv, kid, claims(now))))},
		// 过期要越过 `guanjin.gateway.clock-skew`（默认 60s，边界 +skew < now 才算过期），
		// 否则 exp=now-60 恰好落在容忍窗口边缘、仍是 200——这里取 exp=now-300 是"必然过期"。
		{"已过期的断言", sign(mustTok(priv, kid, claimsAt(now-600, now-300)))},
		{"无断言头（公开路径之外）", ""},
	}

	fmt.Printf("SDK v%s（契约 %s）· 平台桩 %s · 公开路径=%v\n\n",
		guanjin.SDKVersion, guanjin.ContractVersion, platform.URL, sdkPathPatterns(sdk))
	fmt.Printf("%-18s %-4s %-16s %s\n", "用例", "状态", "错误码", "handler 侧")
	fmt.Println("----------------------------------------------------------------------------------------")
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "http://biz.example/orders", nil)
		req.Header.Set("X-Forwarded-For", "10.0.0.8")
		if c.tok != "" {
			req.Header.Set(guanjin.DefaultAssertionHeader, c.tok)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		body := rec.Body.String()
		code := jsonCode(body)
		if len(body) > 90 {
			body = body[:90] + "…"
		}
		fmt.Printf("%-18s %-4d %-16s %s\n", c.name, rec.Code, code, body)
	}
	fmt.Println("----------------------------------------------------------------------------------------")
	fmt.Println("读法：只有合法断言那行 handler 能拿到身份且断言头为空（剥离发生在验签之后、handler 之前）；")
	fmt.Println("      篡改/过期/缺失三行都在 SDK 内被拒，业务 handler 根本没被执行到。")
}

// sdkPathPatterns 读公开路径清单（默认值来自契约，不在这里写死）。
func sdkPathPatterns(sdk *guanjin.SDK) []string {
	return guanjin.NewPathMatcher(sdk.Config()).Patterns()
}

func jwksDoc(k string, pub *sm2.PublicKey) []byte {
	doc := map[string]any{"keys": []map[string]string{{
		"kty": "EC", "crv": "SM2", "kid": k, "x": coord(pub.X), "y": coord(pub.Y),
	}}}
	b, _ := json.Marshal(doc)
	return b
}

func coord(v *big.Int) string {
	buf := make([]byte, 32)
	v.FillBytes(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// jsonCode 从 SDK 的错误响应体里取 `code`（SDK 只产出契约 §5.4 的码，不自造）。
func jsonCode(body string) string {
	var e struct {
		Code string `json:"code"`
	}
	if json.Unmarshal([]byte(body), &e) == nil && e.Code != "" {
		return e.Code
	}
	return "-"
}

func claims(now int64) map[string]any { return claimsAt(now, now+60) }

func claimsAt(iat, exp int64) map[string]any {
	return map[string]any{
		"iss":   guanjin.DefaultIssuer,
		"sub":   "u-1001",
		"aud":   clientID,
		"cid":   "web",
		"uname": "alice",
		"roles": []string{"order_admin"},
		"acr":   "AAL2",
		"amr":   []string{"pwd", "otp"},
		"iat":   iat,
		"exp":   exp,
		"jti":   fmt.Sprintf("demo-%d", iat),
	}
}

// mustTok 只把"三处相同的 iat/exp 取值"包成一次调用，便于上面表格可读。
func mustTok(priv *sm2.PrivateKey, k string, c map[string]any) signedClaims {
	return signedClaims{priv: priv, kid: k, claims: c}
}

type signedClaims struct {
	priv   *sm2.PrivateKey
	kid    string
	claims map[string]any
}

func sign(sc signedClaims) string {
	hdr, _ := json.Marshal(map[string]any{"alg": guanjin.SigningAlg, "kid": sc.kid, "typ": "JWT"})
	clo, _ := json.Marshal(sc.claims)
	p1 := base64.RawURLEncoding.EncodeToString(hdr)
	p2 := base64.RawURLEncoding.EncodeToString(clo)
	r, s, err := sm2.Sm2Sign(sc.priv, []byte(p1+"."+p2), []byte("1234567812345678"), rand.Reader)
	must(err, "SM2 签名")
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return p1 + "." + p2 + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// tamper 改动 payload 的一个字符（签名段不动）→ 验签必失败（不是"claim 不合法"）。
func tamper(tok string) string {
	// 只改 payload（第 2 段）里的一个字符：三段式结构仍合法、JSON 仍合法，
	// 变的是被签名的字节 → 必报验签失败（这才是"篡改令牌仍 401"的反向断言）。
	parts := []byte(tok)
	first := -1
	for i := range parts {
		if parts[i] == '.' {
			if first < 0 {
				first = i
				continue
			}
			break
		}
	}
	b := parts[first+2]
	if b == 'A' {
		parts[first+2] = 'B'
	} else {
		parts[first+2] = 'A'
	}
	return string(parts)
}

func must(err error, what string) {
	if err != nil {
		log.Printf("[FAIL] %s: %v", what, err)
		os.Exit(1)
	}
}
