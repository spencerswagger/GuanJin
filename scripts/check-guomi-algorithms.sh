#!/usr/bin/env bash
# 国密算法扫描门禁（AC-AUD-12⑤：全仓不得用 SHA 系 / RSA 做签名、摘要、口令）
#
# 判据原文（acceptance-criteria §AC-AUD-12）：
#   ⑤ 全仓算法扫描**不含 SHA-1/256/512 与 RSA** 用于签名/摘要/口令（**唯一白名单：PKCE `S256`**，见 R5）
#
# 口径（这三条决定了这条闸是不是"真闸"）：
#   1. **扫源码，不扫注释口号**：只看 Go / Java / TS / JS / Vue 的**代码行**；
#      注释里写"SHA256 必须被拒"是测试断言，不是使用。
#   2. **白名单按「文件 + 行内标记」双条件**：只有 `internal/protocol/server.go` 与它的测试
#      可以用 SHA-256（PKCE S256 的 code_challenge），而且那几行**必须**带标记 `PKCE-S256`。
#      只有其一不成立就判红 —— 否则"把算法换成 SHA256 但把文件名加进白名单"就能蒙过。
#   3. **`--self-test` 必须能红**：脚本存在不算通过（与 check-dataplane-boundaries 同一纪律）。
#      自检会在一份临时树里逐个注入五类违规，每一次都必须被点名检出；少检出一个即判红。
#
# 用法：
#   ./scripts/check-guomi-algorithms.sh            # 扫本仓（0 违规才绿）
#   ./scripts/check-guomi-algorithms.sh --self-test # 扫本仓 + 五个注入必须红（门禁用这个）
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PY="${PYTHON:-python3}"

if ! command -v "$PY" >/dev/null 2>&1; then
  echo "[FAIL] 需要 python3（扫描器用它做"按语言 + 白名单 + 行号"判定；沙盒与 CI 均已有）"
  exit 2
fi

scan() { # scan <root> ; 0 = 无违规
  "$PY" - "$1" <<'PYEOF'
import os
import re
import sys

root = sys.argv[1]

# ---- 扫描范围：源码目录；排除生成物/依赖/构建产物 ----
SKIP_DIRS = {'.git', 'node_modules', 'target', 'vendor', 'dist', 'build', 'artifacts', '.octop', '.cache'}
EXTS = {'.go': 'go', '.java': 'java', '.ts': 'js', '.tsx': 'js', '.js': 'js', '.mjs': 'js', '.vue': 'js'}

# ---- 违规模式：按语言分开写，避免"Go 的正则把 Java 的注释当代码" ----
PATTERNS = {
    'go': [
        (r'"crypto/(rsa|sha1|sha256|sha512|md5|ecdsa|ed25519)"', 'SHA/RSA 系算法包'),
        (r'\b(rsa|sha1|sha256|sha512|md5|ecdsa|ed25519)\.[A-Z]', 'SHA/RSA 系算法调用'),
    ],
    'java': [
        (r'MessageDigest\s*\.\s*getInstance\s*\(\s*"(SHA|MD5)', 'MessageDigest SHA/MD5'),
        (r'Signature\s*\.\s*getInstance\s*\(\s*"(SHA|MD5|NONEwithRSA|.*withRSA)', 'Signature SHA/RSA'),
        (r'KeyPairGenerator\s*\.\s*getInstance\s*\(\s*"RSA', 'RSA 密钥对'),
        (r'Cipher\s*\.\s*getInstance\s*\(\s*"RSA', 'RSA 加密'),
        (r'KeyFactory\s*\.\s*getInstance\s*\(\s*"RSA', 'RSA KeyFactory'),
    ],
    'js': [
        (r'createHash\s*\(\s*[\'"](sha1|sha256|sha512|md5)[\'"]', 'createHash SHA/MD5'),
        (r'createSign\s*\(\s*[\'"](RSA|sha)', 'createSign RSA/SHA'),
        (r'crypto\s*\.\s*subtle\s*\.\s*digest\s*\(\s*[\'"]SHA', 'WebCrypto SHA 摘要'),
        (r'crypto\s*\.\s*subtle\s*\.\s*(sign|verify|generateKey)\s*\(\s*\{[^}]*RSA', 'WebCrypto RSA'),
    ],
}

# ---- 白名单：文件 → 允许的算法 + 必须出现的行内标记 ----
# 只允许 PKCE S256（R5）。标记写在"用到它的那一行"或"紧邻上一行"。
ALLOW = {
    'internal/protocol/server.go': {'algo': 'sha256', 'marker': 'PKCE-S256'},
    'internal/protocol/server_test.go': {'algo': 'sha256', 'marker': 'PKCE-S256'},
}


def strip_comment(line, lang):
    """去掉行尾注释（保守：只处理最常见的 // 与 #；字符串里的 // 会误伤，故仅在疑似注释处截断）。"""
    out = line
    idx = out.find('//')
    if idx >= 0 and out[:idx].count('"') % 2 == 0:
        out = out[:idx]
    if lang == 'java' and '/*' in out:
        out = out.split('/*')[0]
    return out


def scan_file(path, lang):
    """返回 [(lineno, text, reason)]。"""
    hits = []
    with open(path, encoding='utf-8', errors='replace') as f:
        lines = f.read().splitlines()
    rel = os.path.relpath(path, root).replace(os.sep, '/')
    allow = ALLOW.get(rel)
    for i, raw in enumerate(lines, start=1):
        code = strip_comment(raw, lang)
        for pat, reason in PATTERNS[lang]:
            if not re.search(pat, code):
                continue
            if allow and allow['algo'] in code and allow['marker'] in raw:
                continue  # 白名单命中且带标记：PKCE S256，允许
            if allow and allow['algo'] in code and allow['marker'] in (lines[i - 2] if i >= 2 else ''):
                continue  # 标记写在上一行也算（便于长表达式换行）
            hits.append((i, raw.strip(), reason, rel))
    return hits


violations = []
for dirpath, dirnames, filenames in os.walk(root):
    dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
    for name in filenames:
        ext = os.path.splitext(name)[1]
        lang = EXTS.get(ext)
        if not lang:
            continue
        full = os.path.join(dirpath, name)
        for lineno, text, reason, rel in scan_file(full, lang):
            violations.append((rel, lineno, reason, text))

if violations:
    print('[FAIL] 发现 SHA/RSA 系算法使用（AC-AUD-12⑤：只允许 PKCE S256 白名单）')
    for rel, lineno, reason, text in violations:
        print(f'  {rel}:{lineno}: {reason} → {text}')
    sys.exit(1)

print(f'[OK] 国密算法扫描：{root} 无 SHA/RSA 系使用（白名单仅 PKCE S256：{", ".join(sorted(ALLOW))}）')
sys.exit(0)
PYEOF
}

echo "== 1/2 扫本仓 =="
if ! scan "$ROOT"; then
  exit 1
fi

if [ "${1:-}" != "--self-test" ]; then
  exit 0
fi

echo "== 2/2 自检：五类注入必须逐个被点名检出 =="
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# 基线：一份"合法"的树（含白名单文件 + 标记），必须绿。
mkdir -p "$tmp/base/internal/protocol"
cat >"$tmp/base/internal/protocol/server.go" <<'EOF'
package protocol

import "crypto/sha256" // PKCE-S256（AC-AUD-12 唯一白名单，R5）

func challenge(v string) []byte { s := sha256.Sum256([]byte(v)); return s[:] } // PKCE-S256
EOF
if ! scan "$tmp/base" >/dev/null; then
  echo "[FAIL] 自检基线（带 PKCE-S256 标记）应判绿，实际判红 —— 白名单规则写错了"
  exit 1
fi
echo "  [OK] 基线（白名单 + 标记）判绿"

inject() { # inject <名字> <相对路径> <内容> <期望命中的关键字>
  local name="$1" rel="$2" body="$3" want="$4"
  local dir="$tmp/$name"
  mkdir -p "$dir/$(dirname "$rel")"
  printf '%s\n' "$body" >"$dir/$rel"
  local out
  out="$(scan "$dir" 2>&1)"
  if [ $? -eq 0 ]; then
    echo "  [FAIL] 注入「$name」未被检出（门禁形同不存在）：$rel"
    return 1
  fi
  case "$out" in
    *"$want"*) echo "  [OK] 注入「$name」被点名检出（$want）" ;;
    *)
      echo "  [FAIL] 注入「$name」判红了，但没点到 $want："
      printf '%s\n' "$out" | sed 's/^/        /'
      return 1
      ;;
  esac
}

rc=0
inject go-rsa internal/crypto/bad.go 'package crypto

import "crypto/rsa"

var _ = rsa.GenerateKey' 'RSA' || rc=1
inject go-sha1 internal/flow/bad.go 'package flow

import "crypto/sha1"

var _ = sha1.New' 'SHA' || rc=1
inject java-sha sdk/java/core/src/main/java/Bad.java 'class Bad {
  java.security.MessageDigest d = java.security.MessageDigest.getInstance("SHA-256");
}' 'MessageDigest' || rc=1
inject node-sha sdk/node/src/bad.ts "import { createHash } from 'crypto';
export const h = () => createHash('sha256');" 'createHash' || rc=1
inject whitelist-without-marker internal/protocol/server.go 'package protocol

import "crypto/sha256"

func challenge(v string) []byte { s := sha256.Sum256([]byte(v)); return s[:] }' 'server.go' || rc=1

if [ "$rc" -ne 0 ]; then
  echo "[FAIL] 自检未通过：有注入没被检出（脚本存在 ≠ 门禁存在）"
  exit 1
fi
echo "[OK] 自检通过：五类注入全部被点名检出，基线判绿"
