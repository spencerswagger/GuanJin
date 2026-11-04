#!/usr/bin/env bash
# 把 `go test -v` 的日志汇总成证据行（供 qa/gate-criteria.md 的 summary.txt 使用）。
# 用法：./scripts/test-summary.sh <go-test.log>
# 输出（单行或键值行）：
#   tests_run=N tests_pass=N tests_skip=N tests_fail=N packages_ok=N packages_fail=N cached=yes|no
#
# 为什么需要：没有 `-v` 就没有 `--- PASS/--- SKIP`，tests_run/skips 数不出来 → 证据链断在自家门禁。
set -uo pipefail
log="${1:-}"
if [ -z "$log" ] || [ ! -f "$log" ]; then
  echo "[FAIL] usage: $0 <go-test.log>（先跑 make test 生成）" >&2
  exit 2
fi

pass=$(grep -c '^--- PASS' "$log" || true)
skip=$(grep -c '^--- SKIP' "$log" || true)
fail=$(grep -c '^--- FAIL' "$log" || true)
pkg_ok=$(grep -c '^ok ' "$log" || true)
pkg_fail=$(grep -c '^FAIL' "$log" || true)
if grep -q '(cached)' "$log"; then cached=yes; else cached=no; fi
run=$((pass + fail))

printf 'tests_run=%s tests_pass=%s tests_skip=%s tests_fail=%s packages_ok=%s packages_fail=%s cached=%s\n' \
  "$run" "$pass" "$skip" "$fail" "$pkg_ok" "$pkg_fail" "$cached"

# 证据链自证：跑了却一条 PASS/FAIL 都没有 → 视为异常（避免"空跑也算绿"）
if [ "$run" -eq 0 ] && [ "$pkg_fail" -eq 0 ]; then
  echo "[FAIL] 日志里没有任何 --- PASS/--- FAIL，`-v` 可能没生效（空跑不算绿）" >&2
  exit 1
fi
# 命中缓存即视为门禁半失效（#3-R5）：主闸必须 -count=1
if [ "$cached" = yes ]; then
  echo "[FAIL] 检测到 (cached)：主闸必须 -count=1，禁止吃缓存" >&2
  exit 1
fi
[ "$fail" -eq 0 ] || { echo "[FAIL] 存在失败用例：$fail" >&2; exit 1; }
# SKIP = 未验证 = 红（Gate §9.2 / hugo 口径）：不是"跳过就不算数"，而是"跳过即失败"。
# 需要跳过的场景请用显式构建标签（如 -tags=integration）分区，而不是 t.Skip 蒙过门禁。
[ "$skip" -eq 0 ] || { echo "[FAIL] 存在 $skip 条 SKIP —— SKIP = 未验证 = 红（请去掉 t.Skip，改用构建标签分区）" >&2; exit 1; }
echo "[OK] test summary（SKIP=未验证=红：tests_skip 必须为 0）"
