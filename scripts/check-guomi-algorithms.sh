#!/usr/bin/env bash
# 国密算法扫描门禁（AC-AUD-12⑤：全仓不得用 SHA 系 / RSA 做签名、摘要、口令）
#
# 判据原文（acceptance-criteria §AC-AUD-12）：
#   ⑤ 全仓算法扫描**不含 SHA-1/256/512 与 RSA** 用于签名/摘要/口令（**唯一白名单：PKCE `S256`**，见 R5）
#
# 口径（这几条决定了这条闸是不是"真闸"）：
#   1. **扫源码，不扫注释口号**：只看 Go / Java / TS / JS / Vue 的**代码行**；注释里写
#      "SHA256 必须被拒"是测试断言，不是使用。剥注释按**三种引号 + 转义**走字符级扫描
#      （不是"数双引号奇偶"——那样 `'https://x'` 会把后半行吞掉，正好把违规藏起来）。
#   2. **两层规则**：
#      - 语言无关的**裸词兜底**：`SHA-1/256/512`、`HmacSHA*`、`RS256/384/512`、`DigestUtils.sha*`、
#        `SHA256Digest` 这类"换个 API 照样是它"的写法（`Mac.getInstance("HmacSHA256")`、
#        `jwt.sign(..., {algorithm:'RS256'})` 都靠这层抓）；
#      - 语言相关的**调用形态**：Go 的 `crypto/(rsa|sha*|md5|ecdsa|ed25519)` 与调用、
#        Java 的 `MessageDigest/Signature/KeyPairGenerator/Cipher/KeyFactory`、
#        JS 的 `createHash/createHmac/createSign/crypto.subtle.*`。
#      **跨行**写法（`MessageDigest\n .getInstance("SHA-256")`）先把逻辑行拼起来再匹配。
#   3. **白名单双条件**：只有 `internal/protocol/server.go` 与其测试可用 SHA-256（PKCE S256），
#      且那几行必须带标记 `PKCE-S256`；只把文件名加进白名单、不带标记 → 照样红。
#      `S256` 不会被 `SHA-?256` 规则吃掉（前者没有 `SHA` 前缀），所以白名单行在第 1 层不误报。
#   4. **负向用例的显式豁免**：`NEGATIVE-SHA-TEST` 标记**只在测试文件**（`_test.go` /
#      `*Test.java` / `*Tests.java` / `*IT.java` / `*.test.ts` / `*.spec.ts`）里生效——
#      生产文件带这个标记照样红（自检里有一条注入专门验这个）。
#   5. **`--self-test` 必须能红**：脚本存在不算通过（与 check-dataplane-boundaries 同一纪律）。
#      基线（带标记的 PKCE）必须绿；八类注入必须逐个**点名到「规则 + 具体算法」**。
#
# 用法：
#   ./scripts/check-guomi-algorithms.sh             # 扫本仓（0 违规才绿）
#   ./scripts/check-guomi-algorithms.sh --self-test # 扫本仓 + 注入必须红（门禁用这个）
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

SKIP_DIRS = {'.git', 'node_modules', 'target', 'vendor', 'dist', 'build', 'artifacts', '.octop', '.cache'}
EXTS = {'.go': 'go', '.java': 'java', '.kt': 'java', '.kts': 'java',
        '.ts': 'js', '.tsx': 'js', '.js': 'js', '.jsx': 'js', '.mjs': 'js', '.cjs': 'js', '.vue': 'js'}

# ---- 第 1 层：语言无关裸词（同一条规则，换 API 也躲不掉）----
BARE = [
    (r'(?<![A-Za-z0-9])(SHA-?(?:1|224|256|384|512))(?![A-Za-z0-9])', 'SHA 系摘要'),
    (r'(?<![A-Za-z0-9])(MD5)(?![A-Za-z0-9])', 'MD5 摘要'),
    (r'(?<![A-Za-z0-9])(HmacSHA(?:1|224|256|384|512))(?![A-Za-z0-9])', 'HMAC-SHA 系'),
    (r'(?<![A-Za-z0-9])(RS(?:256|384|512))(?![A-Za-z0-9])', 'RSA 签名算法标识'),
    (r'(?<![A-Za-z0-9])(SHA(?:1|256|512)Digest)(?![A-Za-z0-9])', 'SHA 摘要类'),
    (r'DigestUtils\s*\.\s*sha(?:1|256|512)', 'DigestUtils SHA'),
]

# ---- 第 2 层：语言相关调用形态 ----
PATTERNS = {
    'go': [
        (r'"crypto/(rsa|sha1|sha256|sha512|md5|ecdsa|ed25519)"', 'SHA/RSA 系算法包'),
        (r'\b(rsa|sha1|sha256|sha512|md5|ecdsa|ed25519)\s*\.\s*[A-Z]', 'SHA/RSA 系算法调用'),
    ],
    'java': [
        (r'MessageDigest\s*\.\s*getInstance\s*\(\s*"(SHA|MD5)', 'MessageDigest SHA/MD5'),
        (r'Signature\s*\.\s*getInstance\s*\(\s*"(SHA|MD5|NONEwithRSA|[A-Za-z0-9]*withRSA)', 'Signature SHA/RSA'),
        (r'Mac\s*\.\s*getInstance\s*\(\s*"HmacSHA', 'Mac HmacSHA'),
        (r'KeyPairGenerator\s*\.\s*getInstance\s*\(\s*"RSA', 'RSA 密钥对'),
        (r'Cipher\s*\.\s*getInstance\s*\(\s*"(RSA|.*withRSA)', 'RSA 加密'),
        (r'KeyFactory\s*\.\s*getInstance\s*\(\s*"RSA', 'RSA KeyFactory'),
    ],
    'js': [
        (r'createHash\s*\(\s*[\'"](sha1|sha256|sha512|md5)[\'"]', 'createHash SHA/MD5'),
        (r'createHmac\s*\(\s*[\'"](sha1|sha256|sha512|md5)[\'"]', 'createHmac SHA/MD5'),
        (r'createSign\s*\(\s*[\'"](RSA|sha)', 'createSign RSA/SHA'),
        (r'createVerify\s*\(\s*[\'"](RSA|sha)', 'createVerify RSA/SHA'),
        (r'algorithm\s*:\s*[\'"]RS(256|384|512)[\'"]', 'JWT alg RS*'),
        (r'crypto\s*\.\s*subtle\s*\.\s*digest\s*\(\s*[\'"]SHA', 'WebCrypto SHA 摘要'),
        (r'crypto\s*\.\s*subtle\s*\.\s*(sign|verify|generateKey)\s*\(\s*\{[^}]*RSA', 'WebCrypto RSA'),
    ],
}

# ---- 白名单：文件 → 允许的算法 + 必须出现的行内标记（仅 PKCE S256）----
ALLOW = {
    'internal/protocol/server.go': {'algo': 'sha256', 'marker': 'PKCE-S256'},
    'internal/protocol/server_test.go': {'algo': 'sha256', 'marker': 'PKCE-S256'},
}

TEST_FILE = re.compile(r'(_test\.go$|(?:Test|Tests|IT)\.(?:java|kt)$|\.(?:test|spec)\.(?:ts|tsx|js)$)')


def is_test_file(rel):
    return bool(TEST_FILE.search(rel))


def strip_comments(raw_lines, lang):
    """整文件剥离注释，**保留行号**（块注释跨行维护状态）。

    为什么不能按行剥（Owl II P1）：`/* harmless */ MessageDigest.getInstance("SHA-256")`
    这种同一行"注释在前、违规在后"会被整段截掉 → 漏检；
    反过来 Java 多行 javadoc 里写"本类不使用 SHA-256"→ 假红。
    两个方向都靠"跨行维护 in_block"一次修掉。

    三种引号（`'` `"` `` ` ``）+ 反斜杠转义；`.vue` 额外处理 HTML 注释 `<!-- -->`。
    """
    out = []
    in_block = False
    carry_quote = None  # 只有反引号（`多行模板串`）跨行携带
    i = 0
    while i < len(raw_lines):
        line = raw_lines[i]
        res = []
        quote = carry_quote
        j = 0
        while j < len(line):
            ch = line[j]
            if in_block:
                if ch == '*' and j + 1 < len(line) and line[j + 1] == '/':
                    in_block = False
                    j += 2
                    continue
                j += 1
                continue
            if quote:
                res.append(ch)
                if ch == '\\' and j + 1 < len(line):
                    res.append(line[j + 1])
                    j += 2
                    continue
                if ch == quote:
                    quote = None
                j += 1
                continue
            if ch in '\'"`':
                quote = ch
                res.append(ch)
                j += 1
                continue
            if ch == '/' and j + 1 < len(line):
                nxt = line[j + 1]
                if nxt == '/':
                    break
                if nxt == '*':
                    in_block = True
                    j += 2
                    continue
            res.append(ch)
            j += 1
        out.append(''.join(res))
        carry_quote = quote if quote == '`' else None
        i += 1
    return out


def _strip_html_comments(line, in_html):
    """`.vue` 的模板注释：`<!-- ... -->`（可跨行）。"""
    out = []
    i = 0
    while i < len(line):
        if in_html:
            end = line.find('-->', i)
            if end < 0:
                return ''.join(out), True
            i = end + 3
            in_html = False
            continue
        start = line.find('<!--', i)
        if start < 0:
            out.append(line[i:])
            break
        out.append(line[i:start])
        i = start + 4
        in_html = True
    return ''.join(out), in_html


def logical_lines(lines):
    """把跨行调用拼成一条逻辑行（`MessageDigest\\n .getInstance("SHA-256")`）。"""
    i = 0
    n = len(lines)
    while i < n:
        start = i
        merged = lines[i].strip()
        depth = merged.count('(') - merged.count(')')
        while i + 1 < n and (depth > 0 or merged.rstrip().endswith(('.', ',', '+')) or
                             lines[i + 1].lstrip().startswith('.')):
            i += 1
            nxt = lines[i]
            merged += ' ' + nxt.strip()
            depth += nxt.count('(') - nxt.count(')')
        yield start + 1, merged, start, i
        i += 1


def _span_marker(allow, orig_lines, start, end):
    """白名单标记 `PKCE-S256`：写在逻辑行覆盖的任意一行、或紧邻上一行都算。"""
    marker = allow['marker']
    if any(marker in l for l in orig_lines[start:end + 1]):
        return True
    return start >= 1 and marker in orig_lines[start - 1]


def _hit_line(code_lines, start, end, text):
    """命中行号：优先取 span 内**真正出现命中文本**的那一行（合并导入块时不会指到 `import (`）。"""
    for k in range(start, end + 1):
        if text and text in code_lines[k]:
            return k + 1
    return start + 1


def scan_file(path, lang, is_vue=False):
    with open(path, encoding='utf-8', errors='replace') as f:
        raw_lines = f.read().splitlines()
    orig_lines = list(raw_lines)  # 标记写在注释里，剥注释后看不到 → 判定标记时用原文
    if is_vue:
        stripped = []
        in_html = False
        for line in raw_lines:
            code, in_html = _strip_html_comments(line, in_html)
            stripped.append(code)
        raw_lines = stripped
    code_lines = strip_comments(raw_lines, lang)
    rel = os.path.relpath(path, root).replace(os.sep, '/')
    allow = ALLOW.get(rel)
    test_file = is_test_file(rel)
    hits = []
    for lineno, code, start, end in logical_lines(code_lines):
        # 负向用例的显式豁免：只在测试文件生效；标记写在命中行或**紧邻上一行**都算
        # （与 `PKCE-S256` 的口径一致）。
        span_orig = orig_lines[start:end + 1]
        prev_orig = orig_lines[start - 1] if start >= 1 else ''
        marker_here = any('NEGATIVE-SHA-TEST' in l for l in span_orig)
        marker_prev = 'NEGATIVE-SHA-TEST' in prev_orig
        if test_file and (marker_here or marker_prev):
            continue
        for pat, reason in BARE:
            m = re.search(pat, code)
            if m:
                if allow and allow['algo'] in code and _span_marker(allow, orig_lines, start, end):
                    continue
                hits.append((_hit_line(code_lines, start, end, m.group(0)), m.group(0), '裸词/' + reason, rel))
        for pat, reason in PATTERNS[lang]:
            m = re.search(pat, code)
            if not m:
                continue
            if allow and allow['algo'] in code and _span_marker(allow, orig_lines, start, end):
                continue
            hits.append((_hit_line(code_lines, start, end, m.group(0)), m.group(0), '调用/' + reason, rel))
    return hits


violations = []
for dirpath, dirnames, filenames in os.walk(root):
    dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
    for name in filenames:
        lang = EXTS.get(os.path.splitext(name)[1])
        if not lang:
            continue
        full = os.path.join(dirpath, name)
        violations.extend(scan_file(full, lang, is_vue=name.endswith('.vue')))

if violations:
    print('[FAIL] 发现 SHA/RSA 系算法使用（AC-AUD-12⑤：只允许 PKCE S256 白名单）')
    for lineno, code, reason, rel in sorted(set(violations)):
        print(f'  {rel}:{lineno}: {reason} → {code}')
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

echo "== 2/2 自检：15 条注入必须逐个被点名检出（规则名 + 算法），另 3 条不误伤 =="
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

expect_green() { # expect_green <名字> <相对路径> <内容>
  local name="$1" rel="$2" body="$3"
  local dir="$tmp/$name"
  mkdir -p "$dir/$(dirname "$rel")"
  printf '%s\n' "$body" >"$dir/$rel"
  if ! scan "$dir" >/dev/null 2>&1; then
    echo "  [FAIL] 「$name」应判绿，实际判红（假阳性会把第一个撞上的人逼去改白名单）："
    scan "$dir" 2>&1 | sed 's/^/        /'
    return 1
  fi
  echo "  [OK] 「$name」判绿（不误伤）"
}

inject() { # inject <名字> <相对路径> <内容> <期望输出里必须同时出现的关键字1> [关键字2]
  local name="$1" rel="$2" body="$3" want="$4" want2="${5:-}"
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
    *"$want"*) ;;
    *)
      echo "  [FAIL] 注入「$name」判红了，但没点到 $want："
      printf '%s\n' "$out" | sed 's/^/        /'
      return 1
      ;;
  esac
  if [ -n "$want2" ]; then
    case "$out" in
      *"$want2"*) ;;
      *)
        echo "  [FAIL] 注入「$name」没点到具体算法 $want2："
        printf '%s\n' "$out" | sed 's/^/        /'
        return 1
        ;;
    esac
  fi
  echo "  [OK] 注入「$name」被点名检出（$want + ${want2:-$want}）"
}

rc=0
# 假阳性方向：多行 javadoc 里写"不使用 SHA-256/RSA"必须绿；测试文件里上一行写豁免标记必须绿。
expect_green javadoc-mention sdk/java/core/src/main/java/Doc.java '/**
 * 本类不使用 SHA-256 / RSA，全部走 SM3 / SM2。
 */
class Doc { int x = 1; }' || rc=1
expect_green kt-negative-test sdk/kotlin/src/BadTest.kt '// NEGATIVE-SHA-TEST（负向用例：断言 SHA256 必须被拒）
val bad = "SHA256"' || rc=1
expect_green marker-prev-line-test internal/credential/foo_test.go '// NEGATIVE-SHA-TEST（负向用例：断言 SHA256 必须被拒）
var bad = "SHA256"' || rc=1

inject go-rsa internal/crypto/bad.go 'package crypto

import "crypto/rsa"

var _ = rsa.GenerateKey' 'crypto/rsa' 'RSA' || rc=1
inject go-sha1 internal/flow/bad.go 'package flow

import "crypto/sha1"

var _ = sha1.New' 'crypto/sha1' 'SHA' || rc=1
inject java-digest sdk/java/core/src/main/java/Bad.java 'class Bad {
  java.security.MessageDigest d = java.security.MessageDigest.getInstance("SHA-256");
}' 'MessageDigest' 'SHA-256' || rc=1
inject java-hmac sdk/java/core/src/main/java/BadMac.java 'class BadMac {
  Object m = javax.crypto.Mac.getInstance("HmacSHA256");
}' 'HmacSHA' 'HmacSHA256' || rc=1
inject java-multiline sdk/java/core/src/main/java/BadLine.java 'class BadLine {
  Object d = java.security.MessageDigest
      .getInstance("SHA-256");
}' 'MessageDigest' 'SHA-256' || rc=1
inject node-hash sdk/node/src/bad.ts "import { createHash } from 'crypto';
export const h = () => createHash('sha256');" 'createHash' 'sha256' || rc=1
inject node-hmac sdk/node/src/bad2.ts "import { createHmac } from 'crypto';
export const h = () => createHmac('sha512', 'k');" 'createHmac' 'sha512' || rc=1
inject jwt-rs256 web/src/bad.ts "const t = jwt.sign({}, key, { algorithm: 'RS256' });" 'RS256' 'RS256' || rc=1
inject comment-bypass sdk/node/src/bad3.ts "const base = 'https://x/y'; export const h = () => createHash('sha256');" 'createHash' 'sha256' || rc=1
inject whitelist-without-marker internal/protocol/server.go 'package protocol

import "crypto/sha256"

func challenge(v string) []byte { s := sha256.Sum256([]byte(v)); return s[:] }' 'server.go' 'sha256' || rc=1
inject block-comment-bypass sdk/java/core/src/main/java/BadTwo.java 'class BadTwo {
  void f() { /* harmless */ java.security.MessageDigest.getInstance("SHA-256"); }
}' 'MessageDigest' 'SHA-256' || rc=1
inject kt-sha sdk/kotlin/src/Bad.kt 'import java.security.MessageDigest
val d = MessageDigest.getInstance("SHA-256")' 'MessageDigest' 'SHA-256' || rc=1
inject cjs-hash sdk/node/src/bad.cjs "const { createHash } = require('crypto');
module.exports = () => createHash('sha1');" 'createHash' 'sha1' || rc=1
inject template-block-bypass sdk/node/src/bad4.ts "const tpl = \`a /* b\`;
export const h = () => createHash('sha256');" 'createHash' 'sha256' || rc=1
inject marker-in-production internal/crypto/bad4.go 'package crypto
// NEGATIVE-SHA-TEST 生产文件不许用这个豁免
import "crypto/sha1"

var _ = sha1.New' 'crypto/sha1' 'SHA' || rc=1

if [ "$rc" -ne 0 ]; then
  echo "[FAIL] 自检未通过：有注入没被检出（脚本存在 ≠ 门禁存在）"
  exit 1
fi
echo "[OK] 自检通过：15 条注入（含跨行、HmacSHA、JWT RS256、行内块注释绕过、模板串含 /*、.kt/.cjs、白名单缺标记、生产文件伪造豁免）全部被点名检出；javadoc 提及、go/Kotlin 测试文件豁免 3 条不误伤；基线判绿"
