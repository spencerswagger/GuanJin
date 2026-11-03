#!/usr/bin/env bash
# 门禁：禁 AutoMigrate / 禁 GORM 软删除（技术栈 §5，项目硬约束 2）
# 通过条件：**变异注入必须失败**——脚本存在不算通过（AC-ENG-03）
set -uo pipefail
fail=0

scan() {
  local pattern="$1" what="$2"
  local hits
  hits=$(grep -rn --include='*.go' -- "$pattern" . 2>/dev/null \
    | grep -v '^./scripts/' \
    | grep -vE ':[0-9]+:[[:space:]]*(//|\*|/\*)' || true)
  if [ -n "$hits" ]; then
    echo "[FAIL] $what detected:"
    echo "$hits" | sed 's/^/    /'
    fail=1
  fi
}

scan 'AutoMigrate(' 'AutoMigrate（schema 唯一权威是 migrations/，应用启动不得自动迁移）'
scan 'gorm.DeletedAt' 'GORM 软删除（业务表用 status 字段，禁 DeletedAt）'

if [ "$fail" -eq 0 ]; then
  echo "[OK] no AutoMigrate / no gorm.DeletedAt"
fi
exit "$fail"
