#!/usr/bin/env bash
# 依赖漏洞门禁（AC-ENG-07：govulncheck + trivy，fail-closed）
#
# 判据原文（acceptance-criteria §AC-ENG-07）：govulncheck / trivy 必须真跑，发现 HIGH/CRITICAL 判红。
#
# 口径（这几条决定了这条闸是不是"真闸"）：
#   1. **fail-closed**：扫描器非 0 退出、漏洞库拉不到、库过期、白名单条目过期或失效 → 一律判红。
#      **不许** `continue-on-error`、不许 `|| true`、不许把 severity 调低来"变绿"。
#   2. **两层扫描，判的东西不一样**：
#      - `govulncheck`：只报**代码真能调用到**的漏洞（符号级可达性），比"按 CVSS 分级"更严——
#        因此本仓策略是**可达即红**，不分级（Go 官方 OSV 不带 CVSS，硬套 severity 只会自欺）。
#      - `trivy fs`：按 **HIGH/CRITICAL** 阈值判（依赖坐标级，含 npm lockfile）。
#   3. **白名单逐条、带理由、带到期日，不许整类 ignore**：
#      - Go 侧：`docs/qa/vuln-allowlist.tsv`（id / module / reason / expires 四列，缺一列即红）；
#      - 依赖侧：`.trivyignore.yaml`（trivy 原生支持 `statement` + `expired_at`，到期自动重新判红）；
#      - 白名单条目**不再命中**也判红（过期条目必须删，防"白名单只增不减"）。
#   4. **库新鲜度**：`MAX_DB_AGE_DAYS`（默认 7）天内的库才算"新"；trivy 读 `trivy version` 的
#      `UpdatedAt`，govulncheck 读 `-version` 的 `DB updated`。CI 每次冷启动、不缓存库 → 每次真拉。
#   5. **与 `check-guomi-algorithms.sh` 零重叠**：那条闸扫的是**源码里出现的算法标识/调用**
#      （SHA/RSA 字样、`MessageDigest` 调用），本闸扫的是**依赖坐标上的 CVE/GO-ID**。
#      同一条红只会由其中一条闸产生：测试文件里写 `RS256` 是源码闸的事（依赖没变），
#      某个库爆 CVE 是本闸的事（源码没变）。两条规则互不读取对方输出。
#
# 用法：
#   ./scripts/check-vuln.sh                 # 真扫本仓（CI 用法）
#   ./scripts/check-vuln.sh --self-test     # 正控：临时树注入已知漏洞依赖，必须判红并点名
#   ./scripts/check-vuln.sh --db-only       # 只验漏洞库新鲜度（排查用）
#
# 依赖：govulncheck（钉 v1.1.4）、trivy（钉 0.76.2）、python3
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ALLOW="$ROOT/docs/qa/vuln-allowlist.tsv"
TRIVY_IGNORE="$ROOT/.trivyignore.yaml"
SEVERITY="${SEVERITY:-HIGH,CRITICAL}"
MAX_DB_AGE_DAYS="${MAX_DB_AGE_DAYS:-7}"
MODE="${1:-}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() { echo "[FAIL] $*"; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || fail "缺工具 $1（CI 由 job 安装；本机请装到 PATH）"; }
need govulncheck
need trivy
need python3

echo "== 工具版本（升级工具 = 一次显式改动，必须进 PR）=="
govulncheck -version 2>&1 | sed -n '1,4p' | sed 's/^/  /'
trivy --version 2>&1 | sed -n '1,2p' | sed 's/^/  /'

# ---------- 漏洞库新鲜度 ----------
check_db_freshness() {
  local out age_trivy age_govuln
  out="$(trivy version 2>/dev/null || true)"
  printf '%s\n' "$out" | sed -n '/Vulnerability DB/,$p' | sed 's/^/  /'
  age_trivy="$(printf '%s' "$out" | python3 -c '
import sys,re,datetime
t=sys.stdin.read()
m=re.search(r"UpdatedAt:\s*(\S+ \S+)", t)
if not m:
    print("missing"); raise SystemExit
try:
    d=datetime.datetime.strptime(m.group(1)[:19], "%Y-%m-%d %H:%M:%S")
except Exception:
    print("unparsable"); raise SystemExit
print(int((datetime.datetime.utcnow()-d).total_seconds()//86400))
')"
  case "$age_trivy" in
    missing|unparsable) fail "读不到 trivy 漏洞库的 UpdatedAt（库没拉下来 = 未验证 = 红）" ;;
  esac
  [ "$age_trivy" -le "$MAX_DB_AGE_DAYS" ] || fail "trivy 漏洞库已 $age_trivy 天未更新（上限 $MAX_DB_AGE_DAYS 天）→ 库太旧等于没扫"
  echo "  [OK] trivy 库年龄 ${age_trivy} 天（上限 ${MAX_DB_AGE_DAYS}）"

  age_govuln="$(govulncheck -version 2>&1 | python3 -c '
import sys,re,datetime
t=sys.stdin.read()
m=re.search(r"DB updated:\s*(\d{4}-\d{2}-\d{2})", t)
if not m:
    print("missing"); raise SystemExit
d=datetime.datetime.strptime(m.group(1), "%Y-%m-%d")
print(int((datetime.datetime.utcnow()-d).total_seconds()//86400))
')"
  case "$age_govuln" in
    missing|unparsable) fail "读不到 govulncheck 漏洞库的 DB updated（库没拉下来 = 未验证 = 红）" ;;
  esac
  [ "$age_govuln" -le "$MAX_DB_AGE_DAYS" ] || fail "govulncheck 漏洞库已 $age_govuln 天未更新（上限 $MAX_DB_AGE_DAYS 天）"
  echo "  [OK] govulncheck 库年龄 ${age_govuln} 天"
}

check_db_freshness
[ "$MODE" = "--db-only" ] && { echo "[OK] 漏洞库新鲜度检查通过（--db-only）"; exit 0; }

# ---------- 正控：注入已知漏洞依赖，必须判红并点名 ----------
if [ "$MODE" = "--self-test" ]; then
  echo "== 正控/负控：扫描器能不能真的判红并点名 =="

  mk_go_probe() { # $1=dir $2=go-redis 版本
    mkdir -p "$1"
    cat > "$1/go.mod" <<EOF
module vulnprobe

go 1.25.0

require github.com/redis/go-redis/v9 $2
EOF
    cat > "$1/main.go" <<'EOF'
package main

import (
	"context"

	"github.com/redis/go-redis/v9"
)

func main() {
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	_, _ = c.Pipeline().Exec(context.Background())
}
EOF
    ( cd "$1" && go mod tidy >/dev/null 2>&1 ) || fail "self-test：$1 的 go mod tidy 失败（网络/代理问题，不是门禁问题）"
  }

  mk_npm_probe() { # $1=dir $2=lodash 版本
    mkdir -p "$1"
    cat > "$1/package-lock.json" <<EOF
{
  "name": "vulnprobe", "version": "1.0.0", "lockfileVersion": 3, "requires": true,
  "packages": {
    "": { "name": "vulnprobe", "version": "1.0.0", "dependencies": { "lodash": "$2" } },
    "node_modules/lodash": { "version": "$2", "resolved": "https://registry.npmjs.org/lodash/-/lodash-$2.tgz" }
  }
}
EOF
  }

  # ① govulncheck：go-redis v9.7.0（GO-2025-3540 可达）→ 必须判红并点名 id + 坐标
  mk_go_probe "$WORK/go-vuln" v9.7.0
  ( cd "$WORK/go-vuln" && govulncheck ./... ) > "$WORK/go-vuln.log" 2>&1
  rc=$?
  [ "$rc" -eq 3 ] || fail "self-test/go-inject：期望 govulncheck 退出 3（有可达漏洞），实际 $rc"
  grep -q 'GO-2025-3540' "$WORK/go-vuln.log" || fail "self-test/go-inject：没点名 GO-2025-3540"
  grep -q 'github.com/redis/go-redis/v9@v9.7.0' "$WORK/go-vuln.log" || fail "self-test/go-inject：没点名依赖坐标 v9.7.0"
  echo "  [OK] self-test/go-inject：判红并点名 GO-2025-3540 / github.com/redis/go-redis/v9@v9.7.0（exit=$rc）"

  # ② 负控：同一条用例换到修好的 v9.7.3 → GO-2025-3540 必须消失（证明是版本敏感，不是"永远红"）
  mk_go_probe "$WORK/go-fixed" v9.7.3
  ( cd "$WORK/go-fixed" && govulncheck ./... ) > "$WORK/go-fixed.log" 2>&1
  if grep -q 'GO-2025-3540' "$WORK/go-fixed.log"; then
    fail "self-test/go-negative：升到 v9.7.3 后仍报 GO-2025-3540（扫描器不敏感或依赖没换成功）"
  fi
  echo "  [OK] self-test/go-negative：v9.7.3 上 GO-2025-3540 消失（版本敏感）"

  # ③ trivy：lodash 4.17.15 → 必须判红并点名 CVE + 坐标
  mk_npm_probe "$WORK/npm-vuln" 4.17.15
  trivy fs --scanners vuln --severity "$SEVERITY" --exit-code 1 --quiet "$WORK/npm-vuln" > "$WORK/npm-vuln.log" 2>&1
  rc=$?
  [ "$rc" -eq 1 ] || fail "self-test/npm-inject：期望 trivy 退出 1（有 HIGH/CRITICAL），实际 $rc"
  grep -q 'CVE-2021-23337' "$WORK/npm-vuln.log" || fail "self-test/npm-inject：没点名 CVE-2021-23337"
  grep -q 'lodash' "$WORK/npm-vuln.log" || fail "self-test/npm-inject：没点名 lodash 坐标"
  echo "  [OK] self-test/npm-inject：判红并点名 CVE-2021-23337 / lodash 4.17.15（exit=$rc）"

  # ④ 负控：lodash 4.18.0（含 4.17.21 之后修的 CVE-2026-4800）→ 必须绿
  mk_npm_probe "$WORK/npm-fixed" 4.18.0
  trivy fs --scanners vuln --severity "$SEVERITY" --exit-code 1 --quiet "$WORK/npm-fixed" > "$WORK/npm-fixed.log" 2>&1
  rc=$?
  [ "$rc" -eq 0 ] || { cat "$WORK/npm-fixed.log"; fail "self-test/npm-negative：lodash 4.18.0 仍判红（阈值/库有问题）"; }
  echo "  [OK] self-test/npm-negative：lodash 4.18.0 判绿（阈值有效，不是永远红）"

  echo "[OK] 漏洞门禁正控/负控 4 项全过（判红 + 点名 + 版本敏感）"
  exit 0
fi

# ---------- 真扫本仓 ----------
echo "== ① govulncheck（符号级可达性；可达即红）=="
( cd "$ROOT" && govulncheck ./... ) > "$WORK/govuln.log" 2>&1
grc=$?
grep -E '^(Vulnerability #|  Module:|    Found in:|    Fixed in:)' "$WORK/govuln.log" | sed 's/^/  /' || true
tail -4 "$WORK/govuln.log" | sed 's/^/  /'

case "$grc" in
  0) echo "  [OK] govulncheck：0 条可达漏洞" ;;
  3) echo "  [INFO] govulncheck：发现可达漏洞，进入白名单核对" ;;
  *) cat "$WORK/govuln.log"; fail "govulncheck 执行失败（exit=$grc）—— 扫描没跑成 = 未验证 = 红" ;;
esac

[ -f "$ALLOW" ] || fail "缺白名单文件 $ALLOW（没有它就等于没有逐条理由）"

python3 - "$WORK/govuln.log" "$ALLOW" <<'PY'
import sys, datetime

log, allow = sys.argv[1], sys.argv[2]
findings = []   # (id, module, found_version, fixed_version)
cur = None
for line in open(log, encoding="utf-8", errors="replace"):
    s = line.strip()
    if s.startswith("Vulnerability #"):
        cur = {"id": s.split(":", 1)[1].strip()}
    elif cur is not None and s.startswith("Found in:"):
        coord = s.split(":", 1)[1].strip()
        mod, _, ver = coord.rpartition("@")
        cur["module"], cur["found"] = mod, ver
        findings.append(cur)
    elif cur is not None and s.startswith("Fixed in:"):
        coord = s.split(":", 1)[1].strip()
        cur["fixed"] = coord.rpartition("@")[2]

entries = {}
today = datetime.date.today()
bad_header = []
for i, raw in enumerate(open(allow, encoding="utf-8")):
    line = raw.rstrip("\n")
    if not line.strip() or line.lstrip().startswith("#"):
        continue
    parts = line.split("\t")
    if len(parts) != 4:
        bad_header.append(f"第 {i+1} 行不是 4 列（id/module/reason/expires）：{line}")
        continue
    vid, mod, reason, expires = [p.strip() for p in parts]
    if not reason:
        bad_header.append(f"第 {i+1} 行缺理由（id={vid}）")
    try:
        exp = datetime.date.fromisoformat(expires)
    except ValueError:
        bad_header.append(f"第 {i+1} 行到期日不是 YYYY-MM-DD：{expires}")
        continue
    entries[(vid, mod)] = (reason, exp)

if bad_header:
    print("[FAIL] 白名单格式不合规（必须四列、理由非空、到期日 YYYY-MM-DD）：")
    for b in bad_header:
        print("  -", b)
    sys.exit(1)

def is_stdlib(mod):
    # 标准库包的坐标形如 net/http、mime/multipart（首段无点）；第三方是 github.com/x/y、gopkg.in/z
    return "." not in mod.split("/")[0]


fail = False
seen = set()
for f in findings:
    key = (f["id"], f["module"])
    # 标准库同一 ID 可能挂在多个包上（实测 GO-2026-6608 会在 net/textproto 与 mime/multipart 间漂移，
    # 取决于 govulncheck 找到的第一条调用路径）→ 标准库按 `id + stdlib` 逐条登记，仍是一条一个理由。
    if key not in entries and is_stdlib(f["module"]):
        key = (f["id"], "stdlib")
    if key in entries:
        reason, exp = entries[key]
        if today > exp:
            print(f"[FAIL] 白名单已过期：{f['id']} / {f['module']}（到期 {exp}）—— 到期即重新判红，请升级或续期并说明")
            fail = True
        else:
            print(f"[INFO] 白名单内：{f['id']} / {f['module']}（到期 {exp}）：{reason}")
        seen.add(key)
    else:
        fixed = f.get("fixed") or "无修复版本"
        print(f"[FAIL] 未登记的漏洞：{f['id']} / {f['module']}@{f['found']}（修复版本：{fixed}）")
        fail = True

for key, (reason, exp) in entries.items():
    if key not in seen:
        print(f"[FAIL] 白名单条目已失效：{key[0]} / {key[1]} 本次扫描不再命中 —— 请删除该行（白名单不许只增不减）")
        fail = True

if fail:
    sys.exit(1)
print(f"[OK] govulncheck：{len(findings)} 条可达漏洞全部在白名单内且未过期（白名单 {len(entries)} 条）")
PY
[ $? -eq 0 ] || exit 1

echo "== ② trivy（依赖坐标；HIGH/CRITICAL 判红）=="
# 只扫 lockfile 类生态（go.mod / package-lock.json）。刻意 --skip-dirs sdk：
# sdk/java 的 pom 解析要访问 Maven 中央仓（实测 429），属外网抖动 → 换成离线 SBOM 是后续项，
# 不拿"可能连不上"的步骤当门禁（docs/qa/vuln-scan.md §5）。
trivy fs --scanners vuln --severity "$SEVERITY" --exit-code 1 --ignorefile "$TRIVY_IGNORE" \
  --skip-dirs web/node_modules --skip-dirs .git --skip-dirs sdk --skip-dirs docs \
  --format table "$ROOT" > "$WORK/trivy.log" 2>&1
trc=$?
grep -E '^(go\.mod|web/package-lock\.json|Total:|│ )' "$WORK/trivy.log" | sed 's/^/  /' | head -40 || true
case "$trc" in
  0) echo "  [OK] trivy：HIGH/CRITICAL 0 条（白名单见 .trivyignore.yaml）" ;;
  1) cat "$WORK/trivy.log"; fail "trivy：存在未白名单的 HIGH/CRITICAL（见上表；白名单必须逐条给理由+到期日）" ;;
  *) cat "$WORK/trivy.log"; fail "trivy 执行失败（exit=$trc）—— 库/网络问题 = 未验证 = 红" ;;
esac

echo "[OK] 依赖漏洞门禁通过（govulncheck 可达漏洞 + trivy HIGH/CRITICAL，均 fail-closed）"
