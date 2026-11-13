#!/usr/bin/env bash
# 覆盖率门禁（技术栈 §9.3 / §9.4 / AC-ENG-01）
#
# 双闸：
#   1) **逐模块下限**（§9.3，唯一权威）：crypto/audit 90、factor/credential/identifier/flow/authz 85、
#      risk/gateway 80、store 75、flowstore 85（D8 补登）、identity 85（D8 补登）、protocol/admin/agent 70；
#      只对**已有可执行语句**的模块生效 —— 尚未实现的包（只有 doc.go）不出现在 profile 里，**跳过但不放行**：
#      一旦该包有了语句，这闸立刻开始管它。
#   2) **总闸**（COVERAGE_MIN，默认 60）：兜底，防止"每个模块都刚好压线、整体却很低"。
#
# 用法：
#   COVERAGE_TAGS=integration COVERAGE_PROFILE=x.out ./scripts/coverage-gate.sh   # integration profile（含 //go:build integration 用例）
#   make coverage                                   # 复用 make test 产出的 profile
#   COVERAGE_PROFILE=x.out ./scripts/coverage-gate.sh
#
# 【临时放宽】只允许显式开关 + 到期日，到期自动失效（不允许"静悄悄放宽"）：
#   COVERAGE_RELAX=1 COVERAGE_RELAX_UNTIL=2026-12-31 ./scripts/coverage-gate.sh
#   - 放宽时**仍然**跑总闸，只是跳过逐模块下限，并在输出里大字标注 + 打印到期日；
#   - **过了到期日**（含当天之后）直接判红，强制回收开关。
set -uo pipefail

total_min="${COVERAGE_MIN:-60}"
profile="${COVERAGE_PROFILE:-}"
relax="${COVERAGE_RELAX:-0}"
relax_until="${COVERAGE_RELAX_UNTIL:-}"

# 逐模块下限（技术栈 §9.3，照抄；改这里等于改判据，必须同步 decisions.md）
module_floor() {
  case "$1" in
    internal/crypto) echo 90 ;;
    # S10 补登（2026-11-10）：`internal/audit` 的 90% 下限**只能在 integration profile 判定**——
    # 它的行为是"哈希链落库 + 整链复核"（advisory lock、分区、扫表、锚点），unit profile 里
    # 只剩入参校验与指标语义，实测 56%，硬在 unit 判 90 会让**每个** unit job 永久红。
    # 与 flowstore / identity 同一处理方式（不是放宽：integration profile 里照样 90%）。
    internal/audit)
      case ",${COVERAGE_TAGS:-}," in
        *,integration,*) echo 90 ;;
        *) echo "" ;;   # 由"未判定"分支显式说明，不许静默跳过
      esac ;;
    internal/factor|internal/credential|internal/identifier|internal/flow|internal/authz) echo 85 ;;
    internal/risk|internal/gateway) echo 80 ;;
    # S10 补登（2026-11-10）：`internal/store` 的 75% 下限**只在 integration profile 判定**——
    # 仓储层的行为（事务、条件更新、聚合查询）只能在真库上验；unit profile 里新增仓储文件
    # （如 #71 的 `repo_platform_factors.go`）会把它拉到 69%，硬判会让每个 unit job 永久红。
    # 与 flowstore / identity / audit 同一处理：integration profile 里照样硬判 75%。
    internal/store)
      case ",${COVERAGE_TAGS:-}," in
        *,integration,*) echo 75 ;;
        *) echo "" ;;   # 由"未判定"分支显式说明，不许静默跳过
      esac ;;
    # D8 补登（2026-11-04）：flowstore 是 D5 的 I-F2/F5/F6/F8 唯一落点（锁/校验/amr 累加/两段式清理），
    # 错一处则上层全返工 → 高于 store（75）、与 flow 同级（85）。
    internal/flowstore)
      # D8 补登：flowstore = 85%（I-F2/F5/F6/F8 唯一落点）。但它的行为全在 //go:build integration 用例里，
      # **unit profile 不编译那些文件** → 只能用 integration profile 判（COVERAGE_TAGS 含 integration）。
      case ",${COVERAGE_TAGS:-}," in
        *,integration,*) echo 85 ;;
        *) echo "" ;;   # 由下方"未判定"分支显式说明，不许静默跳过
      esac ;;
    # D8 补登（2026-11-04，decisions.md 第 192 行 / AC-ENG-01）：「注册即登录」与 I1–I6 编排收口，
    # 错一处即用户被锁门外或产生孤儿账号 → 与 factor/credential/identifier 同级 85%。
    # 与 flowstore 同形：它的编排行为全在 //go:build integration 用例（注册事务、HTTP 面、挑战消费），
    # unit profile 实测 26.8%（85.7% 只有在 integration profile 里才拿得到）→ 硬在 unit profile 判 85
    # 会让**每一个** unit job 永久判红，等于把闸做成假的；判在 integration profile，unit profile 显式打 INFO。
    internal/identity)
      case ",${COVERAGE_TAGS:-}," in
        *,integration,*) echo 85 ;;
        *) echo "" ;;   # 由下方"未判定"分支显式说明，不许静默跳过
      esac ;;
    internal/protocol|internal/admin|internal/agent) echo 70 ;;
    *) echo "" ;;
  esac
}

to_hundredths() {  # "61.5" -> 6150；"60" -> 6000
  local v="$1" i d
  i="${v%%.*}"; d="${v#*.}"
  [ "$d" = "$v" ] && d=0
  d="${d}00"; d="${d:0:2}"
  echo $(( 10#${i:-0} * 100 + 10#${d:-0} ))
}

# ---- 临时放宽：要有到期日，且到期即失效 ----
if [ "$relax" = "1" ]; then
  if [ -z "$relax_until" ]; then
    echo "[FAIL] COVERAGE_RELAX=1 必须同时给 COVERAGE_RELAX_UNTIL=YYYY-MM-DD（不允许无到期日的放宽）"; exit 1
  fi
  today="$(date +%Y-%m-%d)"
  if [[ "$today" > "$relax_until" ]]; then
    echo "[FAIL] 覆盖率放宽已于 ${relax_until} 到期（今天 ${today}），请修覆盖率或重新申请，不得继续放宽"; exit 1
  fi
  echo "=================================================================="
  echo "[WARN] 覆盖率逐模块下限已临时放宽，到期日 ${relax_until}（今天 ${today}）"
  echo "=================================================================="
fi

if [ -z "$profile" ] || [ ! -f "$profile" ]; then
  profile="$(mktemp -t guanjin-cover.XXXXXX).out"
  echo "[coverage] no profile given → running tests (COVERAGE_PROFILE 可复用 make test 的产物)"
  CGO_ENABLED=0 go test ./... -count=1 -coverprofile="$profile" >/dev/null || { echo "[FAIL] go test failed while producing coverage"; exit 1; }
fi

# ---- 0) 可选：合并第二个 profile（S10 起 `internal/audit` 拆了"串行专用套件"） ----
# 串行套件（-tags 'integration audit_serial' -p 1）里的用例同样覆盖产品代码；
# 只看并行 profile 会因为"把用例搬去串行"而**误判为覆盖下降**。
# 传 `COVERAGE_EXTRA_PROFILE=<serial profile>` 即按两套标签**并集**判定（同名块计数相加）。
if [ -n "${COVERAGE_EXTRA_PROFILE:-}" ]; then
  if [ ! -f "$COVERAGE_EXTRA_PROFILE" ]; then
    echo "[FAIL] COVERAGE_EXTRA_PROFILE 指向的文件不存在：$COVERAGE_EXTRA_PROFILE"
    exit 1
  fi
  merged="$(mktemp -t guanjin-cover-merged.XXXXXX).out"
  if ! python3 - "$profile" "$COVERAGE_EXTRA_PROFILE" "$merged" <<'PYEOF'
import sys
main, extra, out = sys.argv[1], sys.argv[2], sys.argv[3]
counts = {}
order = []
mode = None
for path in (main, extra):
    with open(path) as f:
        for line in f:
            line = line.rstrip("\n")
            if not line:
                continue
            if line.startswith("mode:"):
                mode = line
                continue
            parts = line.rsplit(" ", 2)
            if len(parts) != 3:
                continue
            key, nstmt, cnt = parts[0], parts[1], parts[2]
            k = (key, nstmt)
            if k not in counts:
                counts[k] = 0
                order.append(k)
            counts[k] += int(cnt)
with open(out, "w") as f:
    f.write((mode or "mode: set") + "\n")
    for k in order:
        f.write("%s %s %d\n" % (k[0], k[1], counts[k]))
PYEOF
  then
    echo "[FAIL] 合并 profile 失败（$profile + $COVERAGE_EXTRA_PROFILE）"
    exit 1
  fi
  echo "[coverage] 已按两套标签并集判定：$profile + $COVERAGE_EXTRA_PROFILE"
  profile="$merged"
fi

# ---- 0.5) 剔除 `scripts/`：仓库工具（向量生成器等），不是产品代码 ----
# 正常流程下是**空操作**：`go test ./...` 对没有测试文件的包不产块（实测 profile 里
# `scripts/` 0 行）；只有单独为 `scripts/vecgen` 采 profile（89 块 / 225 语句 0%）时才会
# 拉低总闸。口径：含 `/scripts/` 的块不进分母，顺手打印剔除量；只认 `/scripts/`，
# 藏不起 internal/ sdk/ cmd/ 的语句。
if ! python3 - "$profile" <<'PYEOF_INNER'
import sys
path = sys.argv[1]
kept, dropped_stmts, dropped_blocks = [], 0, 0
with open(path) as f:
    for line in f:
        line_r = line.rstrip("\n")
        if not line_r or line_r.startswith("mode:"):
            kept.append(line_r)
            continue
        if "/scripts/" in line_r.rsplit(" ", 2)[0]:
            parts = line_r.rsplit(" ", 2)
            dropped_stmts += int(parts[1])
            dropped_blocks += 1
            continue
        kept.append(line_r)
with open(path, "w") as f:
    f.write("\n".join(kept) + "\n")
if dropped_blocks:
    print("[coverage] 已剔除 scripts/ 工具代码：%d 块 / %d 语句（不计入总闸）" % (dropped_blocks, dropped_stmts))
PYEOF_INNER
then
  echo "[FAIL] 剔除 scripts/ 失败"
  exit 1
fi

fail=0

# ---- 1) 逐模块下限 ----
# 直接解析 coverprofile（纯 bash：这个沙盒里没有 awk/bc，别再依赖它们）：
#   <file>:<start>,<end> <numStmt> <count>
if [ "$relax" != "1" ]; then
  declare -A stmts=()
  declare -A covered=()
  mod=""
  while read -r loc numstmt count; do
    case "$loc" in
      mode:*) continue ;;
    esac
    case "$loc" in
      */internal/*) rest="${loc#*/internal/}"; mod="internal/${rest%%/*}" ;;
      *) continue ;;
    esac
    stmts[$mod]=$(( ${stmts[$mod]:-0} + numstmt ))
    if [ "${count:-0}" -gt 0 ]; then
      covered[$mod]=$(( ${covered[$mod]:-0} + numstmt ))
    fi
  done < "$profile"

  for m in $(printf '%s\n' "${!stmts[@]}" | sort); do
    floor="$(module_floor "$m")"
    if [ -z "$floor" ]; then
      # D8 补登模块在 unit profile 下没有下限可判：**显式说明**，不让它变成静默跳过
      case "$m" in
        internal/flowstore|internal/identity|internal/audit|internal/store)
          echo "[INFO] ${m}：下限须由 integration profile 判定（unit profile 不编译 //go:build integration 用例）"
          echo "[INFO]       → 运行方式：COVERAGE_TAGS=integration COVERAGE_PROFILE=<integration 覆盖率> $0" ;;
      esac
      continue
    fi
    st=${stmts[$m]}; cv=${covered[$m]:-0}
    if [ "$st" -eq 0 ]; then
      case "$m" in
        internal/flowstore|internal/identity|internal/audit|internal/store)
          echo "[FAIL] ${m} 在 integration profile 里没有任何语句 → 下限无法判定（判据不成立即为红）"; fail=1 ;;
      esac
      continue
    fi
    pct=$(( cv * 100 / st ))   # 向下取整：宁可判严，不虚高
    if [ "$pct" -lt "$floor" ]; then
      echo "[FAIL] 模块下限：${m} = ${pct}% < ${floor}%（技术栈 §9.3）"
      fail=1
    else
      echo "[coverage] ${m} = ${pct}% (floor ${floor}%)"
    fi
  done
fi

# ---- 1b) integration scope 必判项：profile 里必须真有这些模块的语句 ----
# 为什么必须**逐个**显式查：上面 1) 的循环只遍历「profile 里出现过的模块」——某个模块的行
# 在 profile 里整段消失时，它连一次比较都不会发生，阈值写得再对也是静默绿。
# `flowstore` 与 `identity` 的下限只在 integration profile 判定，所以这两个模块在
# integration profile 里没有语句 = 判据不成立 = 红（不是跳过，也不是放行）。
case ",${COVERAGE_TAGS:-}," in
  *,integration,*)
    # relax 模式下 stmts 未声明（1) 整段被跳过）：此时按放宽语义不判，不做存在性检查。
    if declare -p stmts >/dev/null 2>&1; then
      for required in internal/flowstore internal/identity internal/audit; do
        if [ -z "${stmts[$required]:-}" ]; then
          echo "[FAIL] integration profile 里没有 ${required} 的语句 → 85% 下限无法判定（判据不成立即为红）"
          fail=1
        fi
      done
    fi ;;
esac

# ---- 2) 总闸 ----
total=$(go tool cover -func="$profile" | tail -1 | grep -oE '[0-9]+\.[0-9]+' || echo 0)
echo "[coverage] total=${total}% min=${total_min}% profile=${profile}"
t_h=$(to_hundredths "$total"); m_h=$(to_hundredths "$total_min")
if [ "$t_h" -lt "$m_h" ]; then
  echo "[FAIL] 总覆盖率 ${total}% < ${total_min}%"; fail=1
fi

[ "$fail" -eq 0 ] || exit 1
echo "[OK] coverage gate（逐模块下限 + 总闸）"
