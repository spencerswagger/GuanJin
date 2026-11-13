#!/usr/bin/env bash
# AC-ENG-05：由判据台账生成「AC-ID → 六栏」映射表（CI 可跑，零网络）。
#
# 为什么需要：AC-ENG-05 要求「CI 产出 AC-ID → PASS / FAIL / NOT-IMPLEMENTED / PENDING /
# BLOCKED / 后置 **六栏并列**的映射表」，但此前没有任何脚本产出它 —— 账目（§2 的 91 = 84/0/0/6/0/1）
# 与台账（§12/§13… 的判据行）之间**没有机器校验**，新增判据（如 v19 的 AC-SDK-11）只能靠人记住同步。
#
# 口径（写死，别两套说法）：
#   1. 台账 = 目标文件里形如 `| AC-XXX-NN | … | <状态> |` 的表格行（首列即 ID）；
#   2. 状态取**该行最后一列**，按优先级归类（一行只落一栏）：
#        后置（DEFERRED） > BLOCKED > NOT-IMPLEMENTED > PENDING > PASS-able > N/A
#      （台账里 N/A 写作 `NOT-APPLICABLE`，两种写法都归 N/A 栏）；
#   3. `AC-AUD-10` 必须落在「后置」栏（判据原文硬要求）；
#   4. **双向对账**：行数 = §2 声明总数、各栏计数 = §2 声明值；声明行解析不到即红（不静默跳过）；
#      出现无法归类的状态也即红。
#
# 用法：./scripts/ac-map.sh [--doc <path>] [--out <file>] [--self-test]
set -uo pipefail

doc="docs/acceptance-criteria.md"
out=""
self_test=0
while [ $# -gt 0 ]; do
  case "$1" in
    --doc) doc="${2:-}"; shift 2 ;;
    --out) out="${2:-}"; shift 2 ;;
    --self-test) self_test=1; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "[FAIL] 未知参数：$1" >&2; exit 2 ;;
  esac
done

# 输出格式（机器可读，别用 sed 解析含 `/` 的栏名）：
#   第 1 行 = 六栏（+ UNCLASSIFIED）表头，第 2 行 = 各栏计数（TAB 分隔）
#   随后 `# <栏>: <n> → id,id…` 人类可读行
#   然后 `COUNT<TAB><栏><TAB><n>` 每栏一行、`TOTAL<TAB><n>` 一行
map_table() {
  python3 - "$1" <<'PY'
import re, sys, collections
rows = []
for line in open(sys.argv[1], encoding='utf-8', errors='replace'):
    line = line.rstrip('\n')
    m = re.match(r'^\|\s*(AC-[A-Z]+-\d+)\s*\|', line)
    if not m:
        continue
    cells = [c.strip() for c in line.strip().strip('|').split('|')]
    rows.append((m.group(1), cells[-1]))

def cls(status):
    for key, name in (('后置', '后置'), ('DEFERRED', '后置'), ('BLOCKED', 'BLOCKED'),
                      ('NOT-IMPLEMENTED', 'NOT-IMPLEMENTED'), ('PENDING', 'PENDING'),
                      ('PASS', 'PASS-able'), ('NOT-APPLICABLE', 'N/A'), ('N/A', 'N/A')):
        if key in status:
            return name
    return 'UNCLASSIFIED'

cols = ('PASS-able', 'FAIL', 'NOT-IMPLEMENTED', 'PENDING', 'BLOCKED', '后置', 'N/A', 'UNCLASSIFIED')
b = collections.OrderedDict((k, []) for k in cols)
for cid, status in rows:
    b[cls(status)].append(cid)
print('\t'.join(cols))
print('\t'.join(str(len(v)) for v in b.values()))
for k, v in b.items():
    print(f'# {k}: {len(v)}' + ('' if not v else ' → ' + ','.join(v)))
for k, v in b.items():
    print(f'COUNT\t{k}\t{len(v)}')
print(f'TOTAL\t{len(rows)}')
PY
}

# §2 账目声明：行形如 `| \`PASS-able\` | **84** | …`，合计行 `| **合计** | **91** |`
declared() {
  python3 - "$1" <<'PY'
import re, sys
want = (('PASS-able', 'PASS-able'), ('PENDING', 'PENDING'), ('BLOCKED', 'BLOCKED'),
        ('NOT-APPLICABLE', 'N/A'), ('NOT-IMPLEMENTED', 'NOT-IMPLEMENTED'),
        ('后置', '后置'), ('FAIL', 'FAIL'))
found = {}
for line in open(sys.argv[1], encoding='utf-8', errors='replace'):
    s = line.strip()
    m = re.match(r'^\|\s*`([^`]+)`\s*\|\s*\*\*(\d+)\*\*\s*\|', s)
    if m:
        label, n = m.group(1), int(m.group(2))
        for key, col in want:
            if label.startswith(key):
                found.setdefault(col, n)
    m = re.match(r'^\|\s*\*\*合计\*\*\s*\|\s*\*\*(\d+)\*\*', s)
    if m:
        found['TOTAL'] = int(m.group(1))
for k, v in found.items():
    print(f'{k}={v}')
PY
}

# 纯 shell 解析（本沙盒没有可用的 awk；CI 上也别依赖 GNU 扩展）
count_of() { # $1=表文本 $2=栏名
  local txt="$1" key="$2" a b c
  while IFS=$'\t' read -r a b c; do
    if [ "$a" = "COUNT" ] && [ "$b" = "$key" ]; then printf '%s' "$c"; return; fi
  done <<< "$txt"
}
total_of() { # $1=表文本
  local txt="$1" a b
  while IFS=$'\t' read -r a b; do
    if [ "$a" = "TOTAL" ]; then printf '%s' "$b"; return; fi
  done <<< "$txt"
}

if [ "$self_test" -eq 1 ]; then
  ok=0; bad=0
  t=$(mktemp -d); trap 'rm -rf "$t"' EXIT
  # ① 正常台账 → 行数正确、NOT-APPLICABLE 归 N/A 栏
  printf '| AC-X-01 | a | PASS-able |\n| AC-X-02 | b | NOT-APPLICABLE · M2 |\n' > "$t/ok.md"
  mt=$(map_table "$t/ok.md")
  if [ "$(total_of "$mt")" = "2" ] && [ "$(count_of "$mt" N/A)" = "1" ] && [ "$(count_of "$mt" PASS-able)" = "1" ]; then
    echo "[OK] 自检①：行数 2、NOT-APPLICABLE 归 N/A 栏"; ok=$((ok+1))
  else echo "[FAIL] 自检①"; bad=$((bad+1)); fi
  # ② 后置优先于 PASS（同一行两个关键词）
  printf '| AC-X-03 | c | PASS-able 但后置（DEFERRED） |\n' > "$t/mix.md"
  mt=$(map_table "$t/mix.md")
  if [ "$(count_of "$mt" 后置)" = "1" ] && [ "$(count_of "$mt" PASS-able)" = "0" ]; then
    echo "[OK] 自检②：后置优先于 PASS"; ok=$((ok+1))
  else echo "[FAIL] 自检②"; bad=$((bad+1)); fi
  # ③ 不可归类 → UNCLASSIFIED 计数非 0（红，不静默）
  printf '| AC-X-04 | d | 待定 |\n' > "$t/un.md"
  mt=$(map_table "$t/un.md")
  if [ "$(count_of "$mt" UNCLASSIFIED)" = "1" ]; then
    echo "[OK] 自检③：不可归类被点名"; ok=$((ok+1))
  else echo "[FAIL] 自检③"; bad=$((bad+1)); fi
  # ④ 声明值解析（含合计行；缺声明 → 空 = 红）
  printf '| `PASS-able` | **2** | x |\n| `NOT-APPLICABLE · M1` | **1** | y |\n| **合计** | **3** | z |\n' > "$t/decl.md"
  d=$(declared "$t/decl.md" | tr '\n' ' ')
  case "$d" in
    *"PASS-able=2"*"N/A=1"*"TOTAL=3"*) echo "[OK] 自检④：声明值解析（含合计）"; ok=$((ok+1)) ;;
    *) echo "[FAIL] 自检④ d=$d"; bad=$((bad+1)) ;;
  esac
  echo "[OK] 自检 $ok 项通过（$bad 失败）"
  [ "$bad" -eq 0 ] || exit 1
  exit 0
fi

[ -f "$doc" ] || { echo "[FAIL] 读不到 $doc（要在仓库根跑）" >&2; exit 2; }

out_text=$(map_table "$doc") || { echo "[FAIL] 映射表生成失败" >&2; exit 1; }
printf '%s\n' "$out_text"

total=$(total_of "$out_text")
unclass=$(count_of "$out_text" UNCLASSIFIED)
if [ "${unclass:-MISSING}" != "0" ]; then
  echo "[FAIL] 有 ${unclass:-?} 条判据状态无法归类 → 六栏映射表不完整（状态列须含六栏之一）" >&2
  exit 1
fi
if ! printf '%s\n' "$out_text" | grep -q '^# 后置: .*AC-AUD-10'; then
  echo "[FAIL] AC-AUD-10 不在「后置」栏 → 违反 AC-ENG-05 原文" >&2; exit 1
fi
printf '%s\n' "$out_text" | grep -q 'AC-SDK-11' || { echo "[FAIL] AC-SDK-11 不在映射表里（v19 新增判据未进表）" >&2; exit 1; }

decl=$(declared "$doc")
[ -n "$decl" ] || { echo "[FAIL] §2 的账目声明行解析不到 → 无法对账（不许静默跳过）" >&2; exit 1; }
fail=0
while IFS='=' read -r k v; do
  [ -n "$k" ] || continue
  if [ "$k" = "TOTAL" ]; then got="$total"; kind="总数"; else got=$(count_of "$out_text" "$k"); kind="栏 ${k}"; fi
  if [ "${got:-MISSING}" != "$v" ]; then
    echo "[FAIL] ${kind}：映射表 = ${got:-缺栏}，§2 声明 = $v" >&2
    fail=1
  fi
done <<< "$decl"

[ "$fail" -eq 0 ] || exit 1
if [ -n "$out" ]; then printf '%s\n' "$out_text" > "$out"; echo "[OK] 已写 $out" >&2; fi
echo "[OK] AC 映射表：$total 条判据，六栏并列且与 §2 声明逐栏一致（AC-AUD-10 在「后置」栏，AC-SDK-11 在册）" >&2
