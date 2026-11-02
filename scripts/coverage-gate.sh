#!/usr/bin/env bash
# 覆盖率门禁（技术栈 §9.3 / AC-ENG-01）：低于下限即失败。
#   - 若给了 COVERAGE_PROFILE（已有 coverprofile），直接用它，**不重复跑测试**；
#   - 否则自己跑一遍（`-count=1`，不吃缓存）。
# 阈值：COVERAGE_MIN（默认 60）。逐模块下限（crypto≥90 等）在 AC-ENG-01 里按包判，
#      本脚本只做"总闸"，避免与判据文件两套数字打架。
set -uo pipefail
min="${COVERAGE_MIN:-60}"
profile="${COVERAGE_PROFILE:-}"
if [ -z "$profile" ] || [ ! -f "$profile" ]; then
  profile="$(mktemp -t guanjin-cover.XXXXXX).out"
  echo "[coverage] no profile given → running tests (COVERAGE_PROFILE 可复用 make test 的产物)"
  CGO_ENABLED=0 go test ./... -count=1 -coverprofile="$profile" >/dev/null || { echo "[FAIL] go test failed while producing coverage"; exit 1; }
fi
total=$(go tool cover -func="$profile" | tail -1 | grep -oE '[0-9]+\.[0-9]+' || echo 0)
echo "[coverage] total=${total}% min=${min}% profile=${profile}"

# 定点比较（不依赖 awk/bc：这个沙盒里两个都没有）
to_hundredths() {  # "61.5" -> 6150；"60" -> 6000
  local v="$1" i d
  i="${v%%.*}"; d="${v#*.}"
  [ "$d" = "$v" ] && d=0
  d="${d}00"; d="${d:0:2}"
  echo $(( 10#${i:-0} * 100 + 10#${d:-0} ))
}
t_h=$(to_hundredths "$total"); m_h=$(to_hundredths "$min")
if [ "$t_h" -lt "$m_h" ]; then
  echo "[FAIL] coverage ${total}% < ${min}%"; exit 1
fi
echo "[OK] coverage gate"
