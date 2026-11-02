#!/usr/bin/env bash
# 覆盖率门禁（技术栈 §9.3）：低于下限即失败。阈值可用 COVERAGE_MIN 覆盖。
set -uo pipefail
min="${COVERAGE_MIN:-60}"
out=$(go test ./... -coverprofile=/tmp/cover.out 2>&1) || { echo "$out"; exit 1; }
total=$(go tool cover -func=/tmp/cover.out | tail -1 | grep -oE '[0-9]+\.[0-9]+' || echo 0)
echo "[coverage] total=${total}% min=${min}%"
awk -v t="$total" -v m="$min" 'BEGIN{exit !(t+0 >= m+0)}' || { echo "[FAIL] coverage ${total}% < ${min}%"; exit 1; }
echo "[OK] coverage gate"
