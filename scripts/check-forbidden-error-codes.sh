#!/usr/bin/env bash
# 门禁：禁码 / 禁词扫描（decisions.md D8-C′⑤ / D3-F / D11）
# 四个注入各自必须失败（AC-ENG-08，S1 建、S11 只复核）：
#   ① SERVICE_DEGRADED 永不出现在任何返回值/码表（唯一允许：deprecated 列表与判据文档）
#   ② STEP_ALREADY_SATISFIED 只允许出现在 internal/flow
#   ③ dataLabel( 永不出现（密级唯一写法 withClearance(level)）
#   ④ 裸 clearance( 永不出现（正则大小写敏感、排除 withClearance(）
set -uo pipefail
fail=0

report() { echo "[FAIL] $1"; echo "$2" | sed 's/^/    /'; fail=1; }

# ① SERVICE_DEGRADED
hits=$(grep -rn --include='*.go' 'SERVICE_DEGRADED' . 2>/dev/null \
  | grep -v 'DeprecatedCodes' | grep -v '^./scripts/' \
  | grep -vE ':[0-9]+:[[:space:]]*(//|\*|/\*)' || true)
[ -n "$hits" ] && report "SERVICE_DEGRADED 已废弃、不得出现在任何返回值（D8-C′⑤）" "$hits"

# ② STEP_ALREADY_SATISFIED 只许 internal/flow 返回
hits=$(grep -rn --include='*.go' 'STEP_ALREADY_SATISFIED' . 2>/dev/null \
  | grep -v '^./internal/flow/' | grep -v '^./scripts/' \
  | grep -vE ':[0-9]+:[[:space:]]*(//|\*|/\*)' || true)
[ -n "$hits" ] && report "STEP_ALREADY_SATISFIED 只允许出现在 internal/flow（D3-F）" "$hits"

# ③④ 密级写法：只许 withClearance(level)；dataLabel( 与裸 clearance( 均作废（D11 C1）
scan_paths() {
  local target="$1"
  [ -e "$target" ] || return 0
  local hits
  # 无 lookbehind 的等价写法：(^|非单词字符) + clearance( —— withClearance( 的 c 前是 h，不会命中
  # 范围（D11/AC-ENG-08③）：SDK 三语言仓 + examples/ + README* + 仓内 docs/。
  #   例外：**台账类文件必须能写出旧名**（否则作废声明自己被门禁点红）——
  #   排除 docs/decisions.md、docs/m1-slices.md、docs/acceptance-criteria.md、qa/（判据正文要引用禁词）。
  #   基线 `/docs/baseline/` 不在仓内 SDK 树里，本脚本不扫它。
  hits=$(grep -rnE 'dataLabel\(|(^|[^A-Za-z0-9_])clearance\(' "$target" 2>/dev/null \
    | grep -v '^./scripts/' \
    | grep -vE '^\./docs/(decisions|m1-slices|acceptance-criteria)\.md' \
    | grep -vE '^\./qa/' || true)
  [ -n "$hits" ] && report "密级唯一写法是 withClearance(level)：$target 出现 dataLabel( 或裸 clearance(（D11）" "$hits"
}
for d in sdk/go sdk/java sdk/node sdk examples docs; do scan_paths "$d"; done
# 仓根 README（SDK 示例常贴在这里）
for f in README.md README; do [ -f "$f" ] && scan_paths "$f"; done

if [ "$fail" -eq 0 ]; then
  echo "[OK] forbidden codes / deprecated clearance names absent"
fi
exit "$fail"
