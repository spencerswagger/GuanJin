#!/usr/bin/env bash
# 关津 SDK（Java 8）机械门禁 —— 只读判定，绝不改写源码。
#
# 三条（依据 docs/sdk-contract.md §5.1.1 / §5.1.2 / AC-SDK-09）：
#   1) guanjin-core 依赖树不得出现 spring-* / jakarta.* / javax.servlet-api（零 Spring / 零 servlet）
#   2) 两个模块的源码不得命中 JDK 9+ API 黑名单（14 条）
#   3) 产物 class 文件 major version ≤ 52（Java 8），防止传递依赖把 class version 抬到 55+
#
# 用法：./scripts/check-sdk-java8.sh   （先 `mvn -f sdk/java/pom.xml test` 让产物就位）
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SDK="$ROOT/sdk/java"
fail=0
note() { printf '%s\n' "$*"; }
bad()  { printf '[FAIL] %s\n' "$*"; fail=1; }

if [ ! -f "$SDK/pom.xml" ]; then
  note "[SKIP] 没有 sdk/java/pom.xml —— 本仓库尚未包含 Java SDK"
  exit 0
fi

# ---------- 1) core 依赖树边界 ----------
TREE="$(mvn -B -f "$SDK/pom.xml" -pl guanjin-core dependency:tree 2>/dev/null \
  | grep -E '^\[INFO\] [|+\\ -]' || true)"
if [ -z "$TREE" ]; then
  bad "拿不到 guanjin-core 的依赖树（先跑一次 mvn 让依赖解析完成）"
else
  if printf '%s\n' "$TREE" | grep -Eq 'org\.springframework|jakarta\.|javax\.servlet'; then
    bad "guanjin-core 依赖树出现 spring/jakarta/servlet（AC-SDK-09 要求零 Spring / 零 servlet）"
    printf '%s\n' "$TREE" | grep -E 'org\.springframework|jakarta\.|javax\.servlet' | sed 's/^/       /'
  else
    note "[OK] guanjin-core 依赖树无 spring-* / jakarta.* / javax.servlet-api"
  fi
fi

# ---------- 2) JDK 9+ API 黑名单（14 条） ----------
# 只扫源码（含测试），不扫 docs/ 与基线（基线示例代码在 Java 8 下本就编译不过）。
BANNED=(
  'java\.net\.http\.'
  '\.isBlank\(\)'
  '\.strip\(\)'
  '\.repeat\('
  '(^|[^a-zA-Z_])var [a-zA-Z_]+ ='
  'List\.of\(|Map\.of\(|Set\.of\(|Collectors\.toUnmodifiable'
  'Files\.readString|Files\.writeString|Path\.of\('
  'List\.copyOf|Set\.copyOf|Map\.copyOf'
  '\.readAllBytes\(\)|\.readNBytes\(|\.transferTo\('
  'Map\.entry\(|Map\.ofEntries'
  'HexFormat'
  'Optional\.isEmpty\(\)|ifPresentOrElse'
  'Stream\.toList\(\)'
  'ProcessHandle|StackWalker|Thread\.onSpinWait'
)
banned_hits=0
for pat in "${BANNED[@]}"; do
  hits="$(grep -rnE "$pat" "$SDK" --include='*.java' 2>/dev/null \
    | grep -vE ':[0-9]+:[[:space:]]*(\*|//|/\*)' || true)"
  if [ -n "$hits" ]; then
    bad "命中 Java 9+ API：$pat"
    printf '%s\n' "$hits" | sed 's/^/       /'
    banned_hits=1
  fi
done
if [ "$banned_hits" -eq 0 ]; then
  note "[OK] 源码未命中 14 条 JDK 9+ API 黑名单"
fi

# ---------- 3) class 文件 major version ≤ 52 ----------
classes="$(find "$SDK" -path '*/target/classes/*' -name '*.class' 2>/dev/null | head -400 || true)"
if [ -z "$classes" ]; then
  note "[WARN] 没有编译产物，跳过 class 版本检查（先跑 mvn test/package）"
else
  worst=0
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    # class 文件第 7-8 字节 = major version（52 = Java 8）；不用 awk（沙盒里未必有）
    set -- $(od -An -tu1 -j6 -N2 "$f")
    major=$(( $1 * 256 + $2 ))
    if [ "$major" -gt "$worst" ]; then worst="$major"; fi
  done <<< "$classes"
  if [ "$worst" -gt 52 ]; then
    bad "class 文件 major version = $worst（>52 不是 Java 8；检查传递依赖，见契约 C-4）"
  else
    note "[OK] class 文件 major version 最大 = $worst（≤52 = Java 8）"
  fi
fi

if [ "$fail" -ne 0 ]; then
  note ""
  note "[FAIL] SDK Java 8 门禁未通过"
  exit 1
fi
note ""
note "[OK] SDK Java 8 门禁全部通过"
