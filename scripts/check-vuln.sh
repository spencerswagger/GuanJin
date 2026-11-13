#!/usr/bin/env bash
# 依赖漏洞门禁（AC-ENG-07）—— **零网络**，数据来自入库的离线快照。
#
# 为什么不是 govulncheck/trivy 现跑（与 `ci.yml` 顶部「#125 零网络化」一致）：
#   govulncheck 要 `vuln.go.dev`、trivy 要 `ghcr.io` 的库；零网络的 runner 上它们**跑不起来**，
#   而「跑不起来就跳过/只警告」等于把门禁关掉（`skipped` 不是绿）。所以口径改成：
#       **数据离线入库（PR 里可评审的 diff）+ 判定在本地可复算 + 数据过期即红**。
#   数据生产者是 `scripts/vuln-snapshot-refresh.py`（**需要外网，只在有网环境手动跑**，不在 CI 里）。
#   判定引擎是 `scripts/vuln-gate.py`（纯 python3 标准库，无第三方依赖）。
#
# 判据原文（`docs/acceptance-criteria.md` §AC-ENG-07）：govulncheck / trivy 必须真跑，
# 发现 HIGH/CRITICAL 判红。本闸的分级口径见 `scripts/vuln-gate.py` 顶部表格（Go 命中即红；npm HIGH/CRITICAL）。
#
# fail-closed：命中未白名单的阻断级漏洞 / 快照缺失 / 快照过期 / 白名单过期或失效 / 依赖版本不可解析 → 全部 exit 1。
# **不许** `continue-on-error`、不许 `|| true`、不许把阻断阈值调低来"变绿"。
#
# 与 `check-guomi-algorithms.sh`（AC-AUD-12⑤）零重叠：那条闸扫**源码里的算法标识**（SHA/RSA 字样、
# `MessageDigest` 调用），本闸扫**依赖坐标上的 CVE/GO-ID**；两条闸输出互不消费。
#
# 用法：
#   ./scripts/check-vuln.sh              # 判本仓（CI / make gates 用法）
#   ./scripts/check-vuln.sh --self-test  # 正控/负控 24 项（离线，临时目录注入，不动工作树）
#   ./scripts/check-vuln.sh --help       # 其余参数（--root / --snapshot / --max-age-days）透传给引擎
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GATE="$ROOT/scripts/vuln-gate.py"

[ -f "$GATE" ] || { echo "[FAIL] 缺判定引擎 $GATE"; exit 1; }
command -v python3 >/dev/null 2>&1 || { echo "[FAIL] 缺 python3（引擎是纯标准库 python3 脚本）"; exit 1; }

echo "== 依赖漏洞门禁（AC-ENG-07；离线快照，零网络）=="
echo "  引擎：scripts/vuln-gate.py（python3 $(python3 -c 'import sys;print("%d.%d"%sys.version_info[:2])'))"
echo "  数据：docs/qa/vuln-advisories.tsv（generated_at 见文件头；过期即红）"

# D23③ 第一段（立即可做）：`sdk/java` 未纳入扫描必须**显式打一行**，不许静默跳过。
# 第二段（2026-12-10 前）改离线 SBOM（Nova 产 → Gate 判），见 docs/decisions.md D23③。
echo "[UNCOVERED] sdk/java：pom 解析需外网（实测 429），本闸不覆盖——见 decisions.md D23③"

if [ "${1:-}" = "--self-test" ]; then
  echo "== 正控/负控：临时夹具注入已知漏洞依赖 → 必须判红并点名 文件:行号 =="
  exec python3 "$GATE" --self-test
fi

exec python3 "$GATE" --root "$ROOT" "$@"
