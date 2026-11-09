#!/usr/bin/env bash
# 门禁：数据面不越界（AC-DP-11 的"静态脚本 + 依赖清单举证"那一半）
#
# 判据原文（acceptance-criteria.md · AC-DP-11）：`cmd/guanjingw` 不自己实现
# TLS / WAF / LB / 灰度 / 熔断 / 业务路由发现；TLS 完全不属本项目范围（spencer 口径 2026-11-02），
# **不设任何"跳过 TLS"的暗门**，非 TLS 判据一条不减。
#
# 为什么要有这个脚本（`internal/gateway/nodeps_test.go` 之外再设一道）：
#   ① `nodeps_test.go` 只在 `go test ./...` 跑到 gateway 包时生效，`go build` / `make gates`
#      单独跑时不会执行；② AC-ENG-08 的纪律是"脚本存在不算通过，变异注入必须红"——
#      判据自己必须能被证明会红（本脚本用 `--self-test` 内建这条举证，见下）。
#
# 纪律：
#   - **只读**：只 grep/读文件，绝不改写源码；
#   - **扫不到文件也算失败**：目标文件被删/改名时判据不许静默通过（#50 的教训）；
#   - **禁止用注释白名单开洞**：注释行同样被判据忽略（Go 注释里可以写 crypto/tls 说明），
#     但"忽略注释"由下面 `grep -vE` 的固定形态完成，不读任何配置文件。
set -uo pipefail

# 默认根目录 = 本脚本所在仓的根（脚本可被 --root 指到别处，供 --self-test 用）。
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SELF_TEST=0

while [ $# -gt 0 ]; do
  case "$1" in
    --root=*) ROOT="${1#--root=}" ;;
    --root) shift; ROOT="${1:-}" ;;
    --self-test) SELF_TEST=1 ;;
    -h|--help)
      echo "用法: $0 [--root=<仓库根>] [--self-test]"
      exit 0
      ;;
    *) echo "[FAIL] 未知参数: $1" >&2; exit 2 ;;
  esac
  shift
done

if [ -z "$ROOT" ] || [ ! -d "$ROOT" ]; then
  echo "[FAIL] 根目录不存在: ${ROOT:-<empty>}" >&2
  exit 2
fi

fail=0

# ---------- ① 目标文件必须存在（扫不到 = 判据失效 = 红） ----------
DP_FILES=(
  "cmd/guanjingw/main.go"
  "internal/gateway/doc.go"
  "internal/gateway/proxy.go"
  "internal/gateway/route.go"
)
for f in "${DP_FILES[@]}"; do
  if [ ! -f "$ROOT/$f" ]; then
    echo "[FAIL] 数据面目标文件缺失：$f（判据不得因文件改名而静默通过）"
    fail=1
  fi
done

# ---------- ② 生产代码扫描（cmd/guanjingw + internal/gateway，非测试、非注释） ----------
# 说明：`_test.go` 里允许出现禁用词（测试要断言"没有它"，得拼出这个名字），
# 生产代码不允许。注释行（`//`、`*`、`/*`）允许出现 —— 解释"我们不实现 TLS"必须能写出这个词。
scan_code() {
  local pattern="$1" what="$2" hits
  hits=$(grep -rnE --include='*.go' -- "$pattern" \
      "$ROOT/cmd/guanjingw" "$ROOT/internal/gateway" 2>/dev/null \
    | grep -v '_test\.go:' \
    | grep -vE ':[0-9]+:[[:space:]]*(//|\*|/\*)' || true)
  if [ -n "$hits" ]; then
    echo "[FAIL] $what:"
    echo "$hits" | sed "s#^$ROOT/#    #"
    fail=1
  fi
}

# TLS / 证书：TLS 由部署方前置 nginx/Kong 承担，本平台不写代码、不做判定、不设跳过开关。
scan_code '(^|[^A-Za-z0-9_])"crypto/tls"'                          '数据面不得 import crypto/tls（TLS 不属本平台范围）'
scan_code 'tls\.(Listen|Config|Server|Dial|DialWithDialer)\b'       '数据面不得自建 TLS 监听/握手'
scan_code 'autocert|InsecureSkipVerify|MinVersion:[[:space:]]*tls\.' '数据面不得做证书/握手参数判定'
# 熔断 / 灰度 / 服务发现：属前置或后置成熟组件（数据面设计 §1.2/§1.3）。
scan_code 'gobreaker|hystrix|sony/gobreaker|afex/hystrix'            '数据面不得内置熔断库'
scan_code '"(github\.com/)?(hashicorp/consul|etcd|nacos|eureka)'     '数据面不得做服务发现'
scan_code '(canary|CanaryWeight|weights?:[[:space:]]*\[)'            '数据面不得做灰度/权重路由'
# 断言头只能"逐请求现签"：出现解析入站断言的代码即在猜身份（先弃后签，网关永不信任入站值）。
scan_code 'func[[:space:]]+(parse|decode|verify)InboundAssertion'    '数据面不得解析入站断言（先弃后签）'

# ---------- ③ 依赖清单举证（go.mod 直接依赖） ----------
GOMOD="$ROOT/go.mod"
if [ ! -f "$GOMOD" ]; then
  echo "[FAIL] go.mod 不存在：无法举证依赖清单"
  fail=1
else
  # 只认 require 段里的模块路径；命中即说明"引了不该引的库"。
  # 只用 sed/grep（不用 awk）：判据必须在最小镜像里也能跑，工具缺失就等于判据不存在。
  dep_hits=$(
    {
      sed -n '/^require[[:space:]]*(/,/^)/p' "$GOMOD" 2>/dev/null \
        | grep -vE '^require[[:space:]]*\(|^\)|^[[:space:]]*//|^[[:space:]]*$'
      grep -E '^require[[:space:]]+[^(]' "$GOMOD" 2>/dev/null
    } | sed -E 's/^[[:space:]]+//; s/^(require[[:space:]]+)?//' | cut -d' ' -f1 \
      | grep -Ei 'tls|autocert|gobreaker|hystrix|consul|etcd|nacos|eureka|rate.?limit|waf' || true
  )
  if [ -n "$dep_hits" ]; then
    echo "[FAIL] go.mod 出现越界依赖（TLS/熔断/服务发现/通用限流/WAF 属前置组件）："
    echo "$dep_hits" | sed 's/^/    /'
    fail=1
  fi
  # 反向举证：数据面必须仍然只依赖标准库 + 项目内包（go.mod 是模块级，故只核对 module 行存在）。
  if ! grep -qE '^module[[:space:]]+github\.com/spencerswagger/guanjin$' "$GOMOD"; then
    echo "[FAIL] go.mod 的 module 行与冻结口径不一致（P4）"
    fail=1
  fi
fi

# ---------- ④ 变异自检：判据自己必须会红 ----------
# 只在 --self-test 时跑：把两处最关键的禁用词注入临时树，要求本脚本以非 0 退出。
# 这道自检是 AC-ENG-08「脚本存在不算通过」对 AC-DP-11 的直接落地。
if [ "$SELF_TEST" -eq 1 ]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  for f in "${DP_FILES[@]}"; do mkdir -p "$tmp/$(dirname "$f")"; done
  mkdir -p "$tmp/internal/gateway"
  cp "$GOMOD" "$tmp/go.mod" 2>/dev/null || printf 'module github.com/spencerswagger/guanjin\n' > "$tmp/go.mod"

  check_self_green() {
    local label="$1"
    if bash "${BASH_SOURCE[0]}" --root="$tmp" >/dev/null 2>&1; then
      echo "[OK] self-test/$label：干净树判绿"
      return 0
    fi
    echo "[FAIL] self-test/$label：干净树被判红（脚本自身有误）"
    return 1
  }

  check_self_red() {
    local label="$1"
    if bash "${BASH_SOURCE[0]}" --root="$tmp" >/dev/null 2>&1; then
      echo "[FAIL] self-test/$label：注入后仍判绿 —— 判据形同不存在"
      return 1
    fi
    echo "[OK] self-test/$label：注入后判红"
    return 0
  }

  reset_tree() {
    for f in "${DP_FILES[@]}"; do printf 'package main\n' > "$tmp/$f"; done
  }

  reset_tree; check_self_green 'baseline' || fail=1

  reset_tree; printf 'package main\n\nimport "crypto/tls"\n\nfunc f() { _ = tls.Config{} }\n' > "$tmp/cmd/guanjingw/main.go"
  check_self_red 'inject-crypto/tls' || fail=1

  reset_tree; printf 'package gateway\n\nimport "github.com/sony/gobreaker"\n\nvar _ = gobreaker.NewCircuitBreaker\n' > "$tmp/internal/gateway/proxy.go"
  check_self_red 'inject-breaker' || fail=1

  reset_tree; rm -f "$tmp/internal/gateway/doc.go"
  check_self_red 'remove-target-file' || fail=1

  reset_tree
  printf 'module github.com/spencerswagger/guanjin\n\nrequire (\n\tgithub.com/sony/gobreaker v0.5.0\n)\n' > "$tmp/go.mod"
  check_self_red 'inject-gomod-dep' || fail=1
fi

if [ "$fail" -eq 0 ]; then
  echo "[OK] data plane stays inside the 7 responsibilities (no TLS/WAF/LB/canary/breaker/discovery)"
fi
exit "$fail"
