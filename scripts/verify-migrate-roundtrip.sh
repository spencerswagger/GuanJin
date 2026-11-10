#!/usr/bin/env bash
# AC-ENG-02 的 down 半边：**迁移往返真验**（up → down → up → down）。
#
# 判据原文（acceptance-criteria.md AC-ENG-02）：「`migrate up` 全绿、`down` 可回滚」。
# 此前 CI 只跑 `up`，「可回滚」一直没证据 → 本脚本把它变成可执行断言（Gate 口径：SKIP = 未验证 = 红）。
#
# 它证明四件事，缺一条即红：
#   ① `up` 到最新版本，且**结构快照**（表/列/索引/触发器/约束/视图/函数/序列）可复现；
#   ② `down -all` 回到 Nil 版本，且**零残留**：public schema 里除 `schema_migrations` 外
#      没有任何表/索引/触发器/约束/视图/函数/序列（down 漏写一条就会在这里露出来）；
#   ③ 再次 `up` 得到的结构快照与第一次**逐字节相同**（往返无损，不是"能建起来就算过"）；
#   ④ 再次 `down -all` 仍然零残留（down 可重复执行，不是"只能回滚一次"）。
#
# 为什么必须用**独立库**：`guanjin_test` 上跑集成用例，回滚会把它清空。故本脚本自己
# DROP/CREATE 一个专用库（`ROUNDTRIP_DSN` 指向它），跑完再删。
#
# 用法（CI 里就是 integration job 的一步；本地有 PG 时同款命令）：
#   ROUNDTRIP_ADMIN_DSN='postgres://u:p@host:5432/postgres?sslmode=disable' \
#   ROUNDTRIP_DSN='postgres://u:p@host:5432/guanjin_miground?sslmode=disable' \
#   ./scripts/verify-migrate-roundtrip.sh
#
# 纪律：
#   - 只用 `migrate` CLI（版本单一来源 = Makefile 的 MIGRATE_VERSION）与 `psql`；
#   - 不引入 `awk`（沙盒里没有）、不依赖 jq；
#   - 每条断言都 `[ ... ] || { echo "[FAIL] ..."; exit 1; }`，打印值、不是打印后不看。

set -euo pipefail

: "${ROUNDTRIP_ADMIN_DSN:?需要 ROUNDTRIP_ADMIN_DSN（指向维护库，如 .../postgres）}"
: "${ROUNDTRIP_DSN:?需要 ROUNDTRIP_DSN（指向专用空库，如 .../guanjin_miground）}"

MIGRATIONS_DIR="${MIGRATIONS_DIR:-migrations}"
MIGRATE="${MIGRATE:-$(command -v migrate 2>/dev/null || { [ -x bin/migrate ] && echo bin/migrate; } || true)}"
[ -n "$MIGRATE" ] || { echo "[FAIL] 找不到 migrate（PATH 与 ./bin 都没有）"; exit 2; }
command -v psql >/dev/null || { echo "[FAIL] 找不到 psql"; exit 2; }
[ -d "$MIGRATIONS_DIR" ] || { echo "[FAIL] 迁移目录不存在：$MIGRATIONS_DIR"; exit 2; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# ---------- 小工具 ----------
db_from_dsn() { printf '%s' "$1" | sed -E 's#^[a-zA-Z0-9+]+://[^/]*/([^?]*).*$#\1#'; }

# 跑 migrate；第二个参数为 allow-nochange 时，"no change" 视为成功（幂等语义）。
run_migrate() {
  local mode="$1"; shift
  local out rc
  set +e
  out="$("$MIGRATE" -path "$MIGRATIONS_DIR" -database "$ROUNDTRIP_DSN" "$@" 2>&1)"
  rc=$?
  set -e
  if [ "$rc" -ne 0 ]; then
    if [ "$mode" = "allow-nochange" ] && printf '%s' "$out" | grep -qi 'no change'; then
      echo "[INFO] migrate $* → no change（幂等语义，视为成功）"
      return 0
    fi
    # 失败时必须把 migrate 的原始输出打出来（别 > /dev/null 把现场吞掉）
    echo "[FAIL] migrate $* 失败（rc=$rc）；原始输出："
    printf '%s\n' "$out" | sed 's/^/    /'
    return "$rc"
  fi
  echo "[INFO] migrate $* → ok"
  return 0
}

version_of() {
  local out
  set +e
  out="$("$MIGRATE" -path "$MIGRATIONS_DIR" -database "$ROUNDTRIP_DSN" version 2>&1)"
  set -e
  if printf '%s' "$out" | grep -qi 'no migration'; then
    printf 'nil'
    return 0
  fi
  # 正常输出形如 "18\n" 或 "18 dirty"
  printf '%s' "$out" | head -1 | sed -E 's/[[:space:]]+dirty$//' | tr -d '[:space:]'
}

expected_version() {
  local f base max=0
  for f in "$MIGRATIONS_DIR"/*.up.sql; do
    base="$(basename "$f")"
    base="${base%%_*}"
    # 去掉前导 0 后比较（000019 → 19）
    base="$(printf '%s' "$base" | sed -E 's/^0+//')"
    [ -n "$base" ] || base=0
    [ "$base" -gt "$max" ] && max="$base"
  done
  printf '%s' "$max"
}

# 结构快照：一行一个对象，`类型|标识|定义`；只取 public schema，排除 golang-migrate 自己的记账表。
snapshot() {
  psql "$ROUNDTRIP_DSN" -X -q -t -A -F'|' -c "
    SELECT 'table', c.relname, c.relkind::text
      FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = 'public' AND c.relkind IN ('r','p','v','m','S')
       -- partition 子表也算（丢掉分区 = 结构丢了）
       AND c.relname <> 'schema_migrations'
    UNION ALL
    SELECT 'column', table_name || '.' || column_name,
           coalesce(column_default,'-') || ':' || data_type || ':' || is_nullable ||
           ':' || coalesce(character_maximum_length::text,'-')
      FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name <> 'schema_migrations'
    UNION ALL
    SELECT 'index', indexname, indexdef
      FROM pg_indexes
     WHERE schemaname = 'public' AND tablename <> 'schema_migrations'
    UNION ALL
    SELECT 'trigger', t.tgname, t.tgrelid::regclass::text || ':' || pg_get_triggerdef(t.oid)
      FROM pg_trigger t
     WHERE NOT t.tgisinternal
       AND t.tgrelid::regclass::text NOT LIKE 'schema_migrations%'
    UNION ALL
    SELECT 'constraint', co.conname, co.conrelid::regclass::text || ':' || pg_get_constraintdef(co.oid)
      FROM pg_constraint co
     WHERE co.connamespace = 'public'::regnamespace
       AND co.conrelid::regclass::text NOT LIKE 'schema_migrations%'
    UNION ALL
    SELECT 'function', p.proname, pg_get_function_identity_arguments(p.oid)
      FROM pg_proc p
     WHERE p.pronamespace = 'public'::regnamespace
    ORDER BY 1, 2, 3;"
}

count_rows() { [ -s "$1" ] && grep -c '' "$1" || echo 0; }

# ---------- 建库（专用，不碰 guanjin_test） ----------
DB_NAME="$(db_from_dsn "$ROUNDTRIP_DSN")"
[ -n "$DB_NAME" ] || { echo "[FAIL] 无法从 ROUNDTRIP_DSN 解析库名"; exit 2; }
echo "[INFO] roundtrip_db=$DB_NAME migrations_dir=$MIGRATIONS_DIR migrate=$MIGRATE"
psql "$ROUNDTRIP_ADMIN_DSN" -X -q -v ON_ERROR_STOP=1 \
  -c "DROP DATABASE IF EXISTS \"$DB_NAME\";" \
  -c "CREATE DATABASE \"$DB_NAME\";" \
  || { echo "[FAIL] 无法重建专用库 $DB_NAME"; exit 1; }

EXPECTED="$(expected_version)"
echo "[INFO] expected_version=$EXPECTED"

# ---------- 第 1 轮 up ----------
run_migrate "" up
v="$(version_of)"
echo "roundtrip_up1_version=$v"
[ "$v" = "$EXPECTED" ] || { echo "[FAIL] 第 1 轮 up 后期望版本 $EXPECTED，实际 $v"; exit 1; }

snapshot > "$WORK/snapA.txt"
a="$(count_rows "$WORK/snapA.txt")"
echo "roundtrip_up1_signature_objects=$a"
[ "$a" -gt 20 ] || { echo "[FAIL] 结构快照只有 $a 个对象，明显不对"; exit 1; }

# 关键锚点（AC-ENG-02 的六项在这里复核一遍，防止快照"空对空"也算相等）
q() { psql "$ROUNDTRIP_DSN" -X -q -t -A -c "$1"; }
tables="$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' AND table_name NOT LIKE 'audit_logs_p%' AND table_name <> 'schema_migrations';")"
echo "roundtrip_logical_tables=$tables"
[ "$tables" = "15" ] || { echo "[FAIL] up 后逻辑表期望 15，实际 $tables"; exit 1; }

# ---------- 第 1 轮 down -all ----------
run_migrate allow-nochange down -all
v="$(version_of)"
echo "roundtrip_down1_version=$v"
[ "$v" = "nil" ] || { echo "[FAIL] down -all 后版本应为 nil，实际 $v"; exit 1; }

snapshot > "$WORK/emptyA.txt"
r="$(count_rows "$WORK/emptyA.txt")"
echo "roundtrip_down1_residue_objects=$r"
if [ "$r" -ne 0 ]; then
  echo "[FAIL] down -all 后仍有残留对象（期望 0）："
  sed 's/^/    /' "$WORK/emptyA.txt"
  exit 1
fi

# ---------- down 可重复（幂等） ----------
run_migrate allow-nochange down -all
v="$(version_of)"
echo "roundtrip_down1_again_version=$v"
[ "$v" = "nil" ] || { echo "[FAIL] 重复 down 后版本应为 nil，实际 $v"; exit 1; }

# ---------- 第 2 轮 up（往返无损） ----------
run_migrate "" up
v="$(version_of)"
echo "roundtrip_up2_version=$v"
[ "$v" = "$EXPECTED" ] || { echo "[FAIL] 第 2 轮 up 后期望版本 $EXPECTED，实际 $v"; exit 1; }

snapshot > "$WORK/snapB.txt"
b="$(count_rows "$WORK/snapB.txt")"
echo "roundtrip_up2_signature_objects=$b"
if ! diff -u "$WORK/snapA.txt" "$WORK/snapB.txt" > "$WORK/snap.diff"; then
  echo "[FAIL] up → down → up 后结构快照不一致（往返有损）："
  head -60 "$WORK/snap.diff" | sed 's/^/    /'
  exit 1
fi
echo "roundtrip_schema_diff_lines=0"

# ---------- 第 2 轮 down（再验零残留） ----------
run_migrate allow-nochange down -all
v="$(version_of)"
echo "roundtrip_down2_version=$v"
[ "$v" = "nil" ] || { echo "[FAIL] 第 2 轮 down -all 后版本应为 nil，实际 $v"; exit 1; }

snapshot > "$WORK/emptyB.txt"
r2="$(count_rows "$WORK/emptyB.txt")"
echo "roundtrip_down2_residue_objects=$r2"
if [ "$r2" -ne 0 ]; then
  echo "[FAIL] 第 2 轮 down -all 后仍有残留对象（期望 0）："
  sed 's/^/    /' "$WORK/emptyB.txt"
  exit 1
fi

echo "[OK] AC-ENG-02 down 半边：up($EXPECTED) → down(nil, 残留 0) → up($EXPECTED, 快照一致) → down(nil, 残留 0)"
