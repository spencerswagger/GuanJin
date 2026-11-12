#!/usr/bin/env bash
# 让 integration job 的「客户端工具」不再依赖 apt 网络。
#
# 背景（2026-11-11 实测，run 43050572912）：GitHub runner 解析不到
# `azure.archive.ubuntu.com` / `archive.ubuntu.com`，`apt-get install postgresql-client redis-tools`
# 重试约 4 分钟后 `E: Unable to fetch some archives`（exit 100）→ 该步红，
# 后续 migrate / schema 断言 / 集成用例**整片 skipped**。**skipped 不是绿**。
#
# 口径：只用「runner 镜像自带」或「docker exec 进 job service 容器」两条路，
# 一个字节都不下载；两条都走不通即红（fail-closed），不静默跳过。
#   - psql / pg_isready：PATH → /usr/lib/postgresql/*/bin → docker exec 进 postgres 容器
#   - redis-cli：PATH → docker exec 进 redis 容器
# service 容器本来就跑在 runner 的 docker daemon 上（Actions 的 services 就是容器），
# 所以 `docker exec <cid> psql ...` 等价于本机 psql，只是不用装包。
#
# shim 目录**固定**为 `$RUNNER_TEMP/ci-deps-bin`（非 Actions 环境 = `/tmp/ci-deps-bin`）：
#   本步用 `export PATH="$RUNNER_TEMP/ci-deps-bin:$PATH"` 立刻生效，
#   后续 step 通过 `$GITHUB_PATH` 生效（脚本会追加）。
#
# 用法：
#   scripts/ci-integration-deps.sh              # 解析工具，必要时生成 shim
#   scripts/ci-integration-deps.sh --self-test  # 假 docker + 模拟缺工具，验三条分支（不需要真 docker）
#
# 退出码：0 = 三个工具可用；1 = 有工具既不在镜像里、也拿不到容器（红）。
set -euo pipefail

say() { printf '[deps] %s\n' "$*"; }
die() { printf '[FAIL] %s\n' "$*" >&2; exit 1; }

BIN_DIR="${RUNNER_TEMP:-/tmp}/ci-deps-bin"

# self-test 专用：把列出的工具当成"镜像里没有"，用来跑通 shim 分支（生产不设）
simulated_missing() {
  local t
  for t in ${CI_DEPS_SIMULATE_MISSING:-}; do [ "$t" = "$1" ] && return 0; done
  return 1
}

# 找发布指定端口的 service 容器 id（`0.0.0.0:5432->5432/tcp`）
# 只用 bash 内建 + docker --format，不依赖 awk（Ubuntu 镜像里 awk 是 alternatives 软链，断链时会静默缺工具）
container_for_port() {
  local port=$1 id ports
  command -v docker >/dev/null 2>&1 || return 0
  while read -r id ports; do
    case "$ports" in
      *"${port}->"*) printf '%s' "$id"; return 0 ;;
    esac
  done < <(docker ps --format '{{.ID}} {{.Ports}}' 2>/dev/null || true)
  return 0
}

# 镜像自带的本机二进制（PATH 或 postgres 版本目录）
host_bin() {
  local name=$1 f
  simulated_missing "$name" && return 1
  if command -v "$name" >/dev/null 2>&1; then command -v "$name"; return 0; fi
  for f in /usr/lib/postgresql/*/bin/"$name"; do
    [ -x "$f" ] && { printf '%s' "$f"; return 0; }
  done
  return 1
}

write_pg_shim() {
  local tool=$1 cid=$2
  cat >"$BIN_DIR/$tool" <<EOF
#!/usr/bin/env bash
# 由 scripts/ci-integration-deps.sh 生成：转发到 postgres service 容器（不装包）。
# 容器内 localhost:5432 就是它自己，故调用方的 -h localhost / -h 127.0.0.1 语义不变。
exec docker exec -i -e PGPASSWORD="\${PGPASSWORD:-}" $cid $tool "\$@"
EOF
  chmod +x "$BIN_DIR/$tool"
}

write_redis_shim() {
  local cid=$1
  cat >"$BIN_DIR/redis-cli" <<EOF
#!/usr/bin/env bash
# 由 scripts/ci-integration-deps.sh 生成：转发到 redis service 容器（不装包）。
exec docker exec -i $cid redis-cli "\$@"
EOF
  chmod +x "$BIN_DIR/redis-cli"
}

resolve_all() {
  local pg_cid="" redis_cid="" missing=0 t p
  mkdir -p "$BIN_DIR"

  if host_bin psql >/dev/null; then
    say "psql 用镜像自带：$(host_bin psql)"
  else
    pg_cid=$(container_for_port 5432 || true)
    [ -n "$pg_cid" ] || die "镜像里没有 psql，也没找到发布 5432 的 service 容器 → schema 断言无从执行"
    write_pg_shim psql "$pg_cid"; say "psql 用 docker exec shim → $pg_cid"
  fi
  if host_bin pg_isready >/dev/null; then
    say "pg_isready 用镜像自带：$(host_bin pg_isready)"
  else
    [ -n "$pg_cid" ] || pg_cid=$(container_for_port 5432 || true)
    [ -n "$pg_cid" ] || die "镜像里没有 pg_isready，也没找到发布 5432 的 service 容器"
    write_pg_shim pg_isready "$pg_cid"; say "pg_isready 用 docker exec shim → $pg_cid"
  fi
  if host_bin redis-cli >/dev/null; then
    say "redis-cli 用镜像自带：$(host_bin redis-cli)"
  else
    redis_cid=$(container_for_port 6379 || true)
    [ -n "$redis_cid" ] || die "镜像里没有 redis-cli，也没找到发布 6379 的 service 容器 → Redis 无法验证"
    write_redis_shim "$redis_cid"; say "redis-cli 用 docker exec shim → $redis_cid"
  fi

  # 本步立刻可用（脚本是子进程，PATH 不回流，故 ci.yml 里显式 export 同一个固定目录）
  export PATH="$BIN_DIR:$PATH"
  if [ -n "${GITHUB_PATH:-}" ]; then
    printf '%s\n' "$BIN_DIR" >>"$GITHUB_PATH"
    say "已把 $BIN_DIR 追加到 \$GITHUB_PATH（后续 step 生效）"
  else
    say "非 Actions 环境，未写 \$GITHUB_PATH"
  fi

  # 证据行：每个工具最终解析到哪，日志里可直接核对
  for t in psql pg_isready redis-cli; do
    p=$(command -v "$t" || true)
    if [ -z "$p" ] || [ ! -x "$p" ]; then say "缺工具：$t"; missing=1; continue; fi
    say "use $t=$p"
  done
  [ "$missing" -eq 0 ] || die "仍有工具不可用（不许跳过：skipped 不是绿）"
}

self_test() {
  local tmp; tmp=$(mktemp -d)
  SELFTEST_TMP="$tmp"          # trap 在函数返回后触发，不能用 local 变量
  trap 'rm -rf "$SELFTEST_TMP"' EXIT
  local fake="$tmp/fake"; mkdir -p "$fake"

  # 假 docker：`ps` 报两个 service 容器；`exec` 剥掉 -i/-e 后在真 PATH 里跑同名工具
  cat >"$fake/docker" <<'EOF'
#!/usr/bin/env bash
sub=${1:-}; shift || true
case "$sub" in
  ps) echo "deadbeefcafe 0.0.0.0:5432->5432/tcp"; echo "cafebabe 0.0.0.0:6379->6379/tcp" ;;
  exec)
    while [ $# -gt 0 ]; do
      case "$1" in
        -i) shift ;;
        -e) shift 2 ;;
        *) break ;;
      esac
    done
    shift                     # 容器 id
    PATH=/usr/bin:/bin:/usr/local/bin exec "$@"
    ;;
  *) exit 0 ;;
esac
EOF
  chmod +x "$fake/docker"

  # A：镜像里三个工具都没有 → 必须生成 3 个 shim，且写进 GITHUB_PATH
  PATH="$fake:/usr/local/bin:/usr/bin:/bin" RUNNER_TEMP="$tmp/rtA" GITHUB_PATH="$tmp/ghA" \
    CI_DEPS_SIMULATE_MISSING="psql pg_isready redis-cli" bash "$0" >"$tmp/a.log" 2>&1 \
    || { sed -n '1,40p' "$tmp/a.log"; die "self-test A：定位失败"; }
  for t in psql pg_isready redis-cli; do
    [ -x "$tmp/rtA/ci-deps-bin/$t" ] || { sed -n '1,40p' "$tmp/a.log"; die "self-test A：没生成 shim $t"; }
  done
  grep -q "use psql=.*ci-deps-bin/psql" "$tmp/a.log" || die "self-test A：证据行不对"
  grep -qx "$tmp/rtA/ci-deps-bin" "$tmp/ghA" || die "self-test A：没写 GITHUB_PATH"
  say "self-test A OK：缺工具 → 3 个 docker exec shim + GITHUB_PATH"

  # B：镜像自带 → 不依赖 docker（这里用真本机工具）
  PATH="/usr/local/bin:/usr/bin:/bin:/.keys/bin" RUNNER_TEMP="$tmp/rtB" GITHUB_PATH="$tmp/ghB" \
    bash "$0" >"$tmp/b.log" 2>&1 \
    || { sed -n '1,40p' "$tmp/b.log"; die "self-test B：定位失败"; }
  grep -q '用镜像自带' "$tmp/b.log" || { sed -n '1,40p' "$tmp/b.log"; die "self-test B：没走本机工具分支"; }
  [ ! -e "$tmp/rtB/ci-deps-bin/psql" ] || die "self-test B：本机有 psql 却生成了 shim"
  say "self-test B OK：镜像自带 → 零 shim、零网络"

  # C：既没工具、也没有容器 → 必须红（fail-closed），不许静默跳过
  printf '#!/usr/bin/env bash\nexit 0\n' >"$fake/docker2"; chmod +x "$fake/docker2"
  mkdir -p "$tmp/fake2"; mv "$fake/docker2" "$tmp/fake2/docker"
  if PATH="$tmp/fake2:/usr/local/bin:/usr/bin:/bin" RUNNER_TEMP="$tmp/rtC" GITHUB_PATH="$tmp/ghC" \
      CI_DEPS_SIMULATE_MISSING="psql pg_isready redis-cli" bash "$0" >"$tmp/c.log" 2>&1; then
    sed -n '1,40p' "$tmp/c.log"; die "self-test C：无工具无容器竟然绿（fail-open）"
  fi
  grep -q '\[FAIL\]' "$tmp/c.log" || { sed -n '1,40p' "$tmp/c.log"; die "self-test C：没打 [FAIL]"; }
  say "self-test C OK：无工具且无容器 → 红"

  say "self-test 全绿（3/3）"
}

if [ "${1:-}" = "--self-test" ]; then self_test; exit 0; fi
resolve_all
