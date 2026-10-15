#!/usr/bin/env bash
set -euo pipefail

# GORM 五条工程约束 + 因子守护规则（§5.1 / §9.1）的机械门禁。

fail=0

# 1) 禁止软删除字段
if grep -rn "gorm.DeletedAt\|DeletedAt \*time.Time" --include="*.go" internal/ ; then
  echo "❌ 禁止使用 gorm.DeletedAt（本项目用 status 字段，审计表物理保留）" >&2
  fail=1
fi

# 2) 禁止 AutoMigrate 上线
if grep -rn "AutoMigrate(" --include="*.go" internal/ cmd/ ; then
  echo "❌ 禁止调用 AutoMigrate（schema 由 golang-migrate 管理）" >&2
  fail=1
fi

# 3) 禁止按因子类型写分支（因子守护规则 1）
if grep -rn 'Type() == "sms"\|Type() == "email"\|factor == "password"' --include="*.go" internal/ ; then
  echo "❌ 禁止按因子类型写分支（按能力断言，见因子守护规则 1）" >&2
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi

echo "✅ 无软删除字段、无 AutoMigrate、无因子类型分支"
