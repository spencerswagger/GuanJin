## 这块解决什么

S11 的 `AC-AUD-12⑤`：**全仓算法扫描不得出现 SHA-1/256/512 与 RSA**（签名/摘要/口令），唯一白名单 PKCE `S256`（R5）。此前只能靠人读代码，现在有可执行判据：`scripts/check-guomi-algorithms.sh`。

## 判据怎么落

| 维度 | 口径 |
|---|---|
| 范围 | Go / Java / TS / TSX / JS / MJS / Vue 的**代码行**；跳过 node_modules / target / vendor / dist / artifacts |
| 模式 | Go：`crypto/(rsa\|sha1\|sha256\|sha512\|md5\|ecdsa\|ed25519)` 及其调用；Java：`MessageDigest / Signature / KeyPairGenerator / Cipher / KeyFactory`；JS：`createHash / createSign / crypto.subtle.(digest\|sign\|verify\|generateKey)` |
| 注释 | 先剥行尾注释再匹配（"SHA256 必须被拒"这类断言不算使用） |
| 白名单 | **文件 + 行内标记双条件**：只有 `internal/protocol/server.go` 与其测试，且必须带 `PKCE-S256`；只加文件名不带标记 = 红 |
| 自检 | `--self-test`：基线（带标记 PKCE）必须绿 + **五类注入必须逐个点名检出** |

接线：`make gates` 末尾 + CI `gates (forbidden patterns)` 步骤。

## 对既有代码的唯一改动

给 `internal/protocol/server.go` / `server_test.go` 里 4 行 PKCE S256 加标记注释，**算法与行为零变化**（`gofmt` 已跑）。

## 证据（本地实跑，A/B）

```
$ ./scripts/check-guomi-algorithms.sh
[OK] 国密算法扫描：无 SHA/RSA 系使用（白名单仅 PKCE S256）

$ printf 'package flow\n\nimport "crypto/sha256"\n\nvar GateProbe = sha256.Sum256\n' > internal/flow/zz_gate_probe.go
$ ./scripts/check-guomi-algorithms.sh
[FAIL] 发现 SHA/RSA 系算法使用（AC-AUD-12⑤：只允许 PKCE S256 白名单）
  internal/flow/zz_gate_probe.go:3: SHA/RSA 系算法包 → import "crypto/sha256"
  internal/flow/zz_gate_probe.go:5: SHA/RSA 系算法调用 → var GateProbe = sha256.Sum256
（删除探针后复绿）

$ ./scripts/check-guomi-algorithms.sh --self-test
  [OK] 基线（白名单 + 标记）判绿
  [OK] 注入「go-rsa」被点名检出
  [OK] 注入「go-sha1」被点名检出
  [OK] 注入「java-sha」被点名检出
  [OK] 注入「node-sha」被点名检出
  [OK] 注入「whitelist-without-marker」被点名检出
```

三闸：`gofmt -l` 空 / `go vet` 干净 / 单测全绿 / `make gates` exit=0（含审计串行套件）。
