#!/usr/bin/env python3
"""依赖漏洞门禁引擎（AC-ENG-07）——**零网络**，数据来自入库的离线快照。

为什么是"离线快照 + 自研判定"而不是每次 CI 现跑 govulncheck/trivy：
  `ci.yml` 的 `vuln` job 必须零网络（#125：runner 解析不到外网源，apt/pip/curl 一律会红，
  `skipped` 又不是绿）。govulncheck 要 `vuln.go.dev`、trivy 要 `ghcr.io` 的库，
  两者在零网络 job 里**根本跑不起来**——而"跑不起来就跳过"等于把门禁关掉。
  所以口径改为：**数据离线入库（可评审的 diff）+ 判定在 PR 里可复算**，
  数据新鲜度用 `generated_at` 卡死（过期即红），生产者是 `scripts/vuln-snapshot-refresh.py`（需外网，不在 CI 跑）。

判定口径（写死，不许在脚本外"临时放宽"）：
  | 生态 | 阻断阈值 | 依据 |
  |---|---|---|
  | Go  | **命中即红**（含 `severity` 为 `-` 的官方 OSV 条目） | Go 官方 OSV 不带 CVSS；#98/`vuln-scan.md` 已定"可达即红"。离线数据没有可达性信息，只能按坐标命中判 → **更宽，只会多报不会漏报** |
  | npm / Maven | **HIGH/CRITICAL 红**；MODERATE/LOW 打 `[INFO]` 并计数 | 判据原文（`acceptance-criteria.md` §AC-ENG-07）"发现 HIGH/CRITICAL 判红" |

Maven（sdk/java，D23③ 第二段，2026-11-13 落地）：
  坐标**不解析 pom**（要外网、429），而是消费 `sdk` job 产出的 **CycloneDX 1.5 聚合 BOM**（离线 JSON）：
  `--java-bom sdk/java/target/bom.json`。组件 `group:artifact@version` 逐条判，生态名 `Maven`。
  **不传 `--java-bom` = Maven 生态未覆盖**，会显式打一行 `[UNCOVERED] sdk/java …`（不许静默跳过，
  D23③ 第一段的行为不变，只是触发条件从"永远"收窄为"没给 BOM"）。
  链路 =「Nova 产（sdk job 生成+上传 artifact）→ Gate 判（本引擎离线消费）」；artifact 缺失或
  与本次 commit 不符的校验在 `vuln` job 的步骤断言里（ci.yml）。

fail-closed 清单（任一条命中即退出 1）：
  1. 有未白名单的阻断级命中；
  2. 快照缺失 / 头部无 `generated_at` / 过期（> `--max-age-days`，默认 90）；
  3. 快照行不合法（列数不对、区间表达式不可解析、生态未知）→ 数据坏了等于没扫；
  4. 依赖坐标的版本号不可解析（`go.mod` / `package-lock.json` / `bom.json` 解析不完整也算）→ 未验证 = 红；
  5. 白名单行不合法（非 5 列 / 理由空 / 到期日非法）、条目已过期、条目本次不再命中（不许只增不减）；
  6. 传了 `--java-bom` 但文件缺失 / 不是合法 JSON / specVersion 非 1.x / 0 个可判组件 → 未验证 = 红。

用法：
  python3 scripts/vuln-gate.py                 # 判本仓（Go + npm；sdk/java 打 [UNCOVERED]）
  python3 scripts/vuln-gate.py --java-bom sdk/java/target/bom.json   # CI vuln job 用法（三生态）
  python3 scripts/vuln-gate.py --self-test     # 正控/负控（离线，临时目录注入，不动工作树）
"""

import argparse
import datetime
import json
import os
import re
import hashlib
import shutil
import sys
import tempfile

ROOT_DEFAULT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SNAPSHOT_REL = os.path.join("docs", "qa", "vuln-advisories.tsv")
ALLOWLIST_REL = os.path.join("docs", "qa", "vuln-allowlist.tsv")
LOCK_REL = os.path.join("web", "package-lock.json")
MAX_AGE_DAYS_DEFAULT = 90
MAX_ALLOW_DAYS = 180          # 白名单到期日上限（再远就是"永久豁免"）
BLOCK_NPM = {"HIGH", "CRITICAL"}

GO_REQUIRE = re.compile(r"^\s*([\w.\-~/]+)\s+(v[0-9][^\s]*)")
GO_REPLACE = re.compile(
    r"^\s*([\w.\-~/]+)(?:\s+(v[0-9][^\s]*))?\s*=>\s*([\w.\-~/]+)(?:\s+(v[0-9][^\s]*))?\s*$"
)
NPM_KEY = re.compile(r'^\s*"node_modules/(.+?)":\s*\{\s*$')
NPM_VER = re.compile(r'^\s*"version":\s*"([^"]+)"')
OPS = {"<": lambda c: c < 0, "<=": lambda c: c <= 0, ">": lambda c: c > 0,
       ">=": lambda c: c >= 0, "=": lambda c: c == 0}


# ---------------------------------------------------------------- 版本比较
def vparse(text):
    """解析版本号 → 可比较元组；不可解析返回 None（调用方 fail-closed）。

    Go 侧形态：v9.7.0 / v9.7.0-beta.1 / v0.0.0-20200520072559-a9ecdc9d1d3a / v2.0.0+incompatible
    npm 侧形态：3.5.13 / 6.4.6-beta.1
    Maven 侧形态（D23③ 第二段）：1.78.1 / 4.1.101.Final / 5.2.0.RELEASE / 5.0.0.RC2 / 5.0.0.M1 /
      5.3.39.SEC03 / 2.5.7.SR5。规则（对齐 Maven ComparableVersion 的**保守近似**）：
      - 尾缀 `.RELEASE` / `.Final` / `.GA`（大小写不敏感）= 发布版本身 → 剥掉后按数字比；
      - 尾缀 `.SEC<n>` / `.SP<n>` / `.SR<n>` = 发布版**之后**的安全/补丁修订（Spring 的 SR=Service
        Release）→ 剥掉后**末段（micro）+n**（5.3.39.SEC03 ⇒ 5.3.39.3；2.5.7.SR5 ⇒ 2.5.7.5；
        SR0<SR5<SR023 ⇒ .0 < .5 < .23，与 Maven 序一致）；
      - 其余点号尾缀（`.RC2` / `.M1` / `.Alpha1` …）与连字符尾缀（`-jre` / `-beta.1`）按**预发布**
        处理（< 同数字发布版）；预发布序号**数值感知**比较（RC10 > RC2，不按字典序）——残余边界：
        "未知尾缀 vs 纯发布版"的相对序是近似（OSV 同公告的区间与坐标形态同源，实际影响限于跨形态混用）；
      - 四段数字版本（Maven 微版本：2.13.4.2 / 2.12.6.1）按 (major,minor,patch,micro) 比；
        数字段 5 段以上（剥尾缀后）→ None（fail-closed）。
    """
    if text is None:
        return None
    s = text.strip()
    if s.startswith("v") or s.startswith("V"):
        s = s[1:]
    s = s.split("+", 1)[0]              # +incompatible / build metadata 不参与比较
    core, _, pre = s.partition("-")
    parts = core.split(".")
    if not 1 <= len(parts) <= 5:
        return None
    bump = 0
    last = parts[-1]
    if last and not last.isdigit():
        m = re.match(r"^(RELEASE|FINAL|GA)$", last, re.IGNORECASE)
        if m and len(parts) >= 2:
            parts = parts[:-1]          # 发布等价尾缀：剥掉按发布版比
        else:
            m2 = re.match(r"^(SEC|SP|SR)(\d+)$", last, re.IGNORECASE)
            if m2 and len(parts) >= 2:
                bump = int(m2.group(2))  # 发布后补丁：剥掉 + 末段（micro）+n（保守近似）
                parts = parts[:-1]
            else:
                pre = ("%s%s" % (last, ("-" + pre if pre else ""))).lower()  # RC/M/Alpha/jre… = 预发布
                parts = parts[:-1]
    if not 1 <= len(parts) <= 4:
        return None
    nums = []
    for p in parts:
        if not p.isdigit():
            return None
        nums.append(int(p))
    while len(nums) < 4:
        nums.append(0)
    if bump:
        nums[-1] += bump
    if pre and not re.match(r"^[0-9A-Za-z.\-]+$", pre):
        return None
    return (tuple(nums), pre)


def _pre_key(pre):
    """预发布序号的**数值感知**键：'rc2' → ('rc', 2)，'rc10' → ('rc', 10)，'beta.1' → ('beta', 1)。

    纯字典序会把 RC10 排在 RC2 前面（漏报方向）；数字段必须按数值比。
    """
    out = []
    for tok in re.findall(r"\d+|\D+", pre):
        out.append(int(tok) if tok.isdigit() else tok)
    return tuple(out)


def vcmp(a, b):
    na, pa = a
    nb, pb = b
    if na != nb:
        return -1 if na < nb else 1
    if pa == pb:
        return 0
    if not pa:
        return 1                        # 正式版 > 预发布版
    if not pb:
        return -1
    try:
        ka, kb = _pre_key(pa), _pre_key(pb)
        # 同位对齐：数字段对数字段、字母段对字母段；先耗尽（更短）= 更小（'beta' < 'beta.1'）
        n = max(len(ka), len(kb))
        ka += ("",) * (n - len(ka))
        kb += ("",) * (n - len(kb))
        for x, y in zip(ka, kb):
            if x == y:
                continue
            if x == "":
                return -1
            if y == "":
                return 1
            x_num, y_num = isinstance(x, int), isinstance(y, int)
            if x_num and y_num:
                return -1 if x < y else 1
            sx, sy = str(x), str(y)
            if sx != sy:
                return -1 if sx < sy else 1
        return 0
    except TypeError:
        return -1 if pa < pb else 1


def parse_expr(expr):
    """`>=1.2.3,<2.0.0` → [(op, version), ...]；任一不可解析返回 None。"""
    out = []
    for chunk in expr.split(","):
        chunk = chunk.strip()
        if not chunk:
            return None
        m = re.match(r"^(<=|>=|<|>|=)\s*(\S+)$", chunk)
        if not m:
            return None
        ver = vparse(m.group(2))
        if ver is None:
            return None
        out.append((m.group(1), ver))
    return out or None


def vmatch(version, conds):
    for op, bound in conds:
        if not OPS[op](vcmp(version, bound)):
            return False
    return True


# ---------------------------------------------------------------- 坐标解析
def read_java_bom(java_bom):
    """解析 CycloneDX 1.x 聚合 BOM（sdk job 的 artifact），返回 (deps, errors)。

    deps: [(ecosystem, module, version_text, relpath, lineno)]
      - ecosystem 固定 "Maven"；module = `group:artifact`（无 group 时裸 artifact）；
      - relpath/lineno：BOM 是 JSON，没有"行号"——用组件在 `components[]` 里的**序号**充当
        稳定位点（同一次构建里顺序确定），点名形态 `…bom.json:N` 仍能直接定位到第 N 个组件；
      - **自有模块排除**：`group == metadata.component.group` 的是本仓模块
        （guanjin-core / guanjin-spring-boot2），不是第三方依赖，不进坐标集
        （生产者 scripts/vuln-snapshot-refresh.py 用同一规则，两指纹才一致）。
    fail-closed：文件缺失 / 非法 JSON / specVersion 非 1.x / 0 个可判组件 / 版本号不可解析 → 全红。
    """
    deps, errors = [], []
    if not os.path.exists(java_bom):
        return deps, ["--java-bom 指定的 BOM 不存在：%s（artifact 缺失 = 未验证 = 红，D23③②）" % java_bom]
    try:
        with open(java_bom, encoding="utf-8") as fh:
            data = json.load(fh)
    except (ValueError, UnicodeDecodeError) as exc:
        return deps, ["--java-bom 不是合法 JSON：%s（%s）→ 未验证 = 红" % (java_bom, exc)]
    if not isinstance(data, dict):
        return deps, ["--java-bom 顶层不是 JSON 对象：%s → 未验证 = 红" % java_bom]
    spec = data.get("specVersion")
    if not isinstance(spec, str) or not spec.startswith("1."):
        return deps, ["--java-bom specVersion=%r 非 1.x（只支持 CycloneDX 1.x，生成端锁 1.5）→ 未验证 = 红"
                      % (spec,)]
    meta = data.get("metadata") or {}
    own_group = ((meta.get("component") or {}).get("group") or "")
    comps = data.get("components")
    if not isinstance(comps, list) or not comps:
        return deps, ["--java-bom 里 components 为空/缺失 → 0 个坐标可判（扫描面为空 = 未验证 = 红）"]
    skipped_own = 0
    for idx, comp in enumerate(comps, 1):
        if not isinstance(comp, dict):
            errors.append("--java-bom components[%d] 不是对象 → BOM 坏了 = 红" % (idx - 1))
            continue
        group = (comp.get("group") or "").strip()
        name = (comp.get("name") or "").strip()
        ver = (comp.get("version") or "").strip()
        if group and group == own_group:
            skipped_own += 1
            continue
        if not name or not ver:
            errors.append("--java-bom components[%d] 缺 name/version（group=%r name=%r）→ 未验证 = 红"
                          % (idx - 1, group, name))
            continue
        if vparse(ver) is None:
            errors.append("--java-bom components[%d] 版本号不可解析：%s@%s" % (idx - 1, name, ver))
            continue
        deps.append(("Maven", "%s:%s" % (group, name) if group else name, ver, os.path.basename(java_bom), idx))
    if skipped_own == len(comps):
        errors.append("--java-bom %d 个组件全是本仓自有模块（group=%s）→ 0 个第三方坐标可判（未验证 = 红）"
                      % (len(comps), own_group or "<无>"))
    return deps, errors


def read_deps(root, java_bom=None):
    """返回 (deps, uncovered, errors)。

    deps: [(ecosystem, module, version_text, relpath, lineno)]
    """
    deps, uncovered, errors = [], [], []

    gomod = os.path.join(root, "go.mod")
    if not os.path.exists(gomod):
        errors.append("缺 go.mod（坐标都读不到 = 未验证 = 红）")
    else:
        # 两遍扫描：先收 replace（require 可能写在 replace 之前，顺序不能影响结论）
        replaces, repl_lines = {}, {}
        block = None
        with open(gomod, encoding="utf-8") as fh:
            for lineno, raw in enumerate(fh, 1):
                line = raw.split("//")[0].strip()
                if not line:
                    continue
                if line.endswith("("):
                    block = line.split()[0]
                    continue
                if line == ")":
                    block = None
                    continue
                if block == "replace" or line.startswith("replace "):
                    body = line[len("replace "):].strip() if line.startswith("replace ") else line
                    m = GO_REPLACE.match(body)
                    if m:
                        old, _ov, new, newv = m.groups()
                        replaces[old] = (new, newv)
                        repl_lines[old] = lineno
        block = None
        with open(gomod, encoding="utf-8") as fh:
            for lineno, raw in enumerate(fh, 1):
                line = raw.split("//")[0].strip()
                if not line:
                    continue
                if line.endswith("("):
                    block = line.split()[0]
                    continue
                if line == ")":
                    block = None
                    continue
                if block == "replace" or line.startswith("replace "):
                    continue
                if block == "require" or line.startswith("require "):
                    body = line[len("require "):].strip() if line.startswith("require ") else line
                    m = GO_REQUIRE.match(body)
                    if not m:
                        errors.append("go.mod:%d 无法解析的 require 行：%s" % (lineno, line))
                        continue
                    mod, ver = m.group(1), m.group(2)
                    if mod in replaces:
                        new, newv = replaces[mod]
                        if newv is None:
                            uncovered.append(
                                "go.mod:%d %s => %s：本地路径替换，坐标无法比对（不计入覆盖）"
                                % (repl_lines[mod], mod, new)
                            )
                            continue
                        mod, ver = new, newv
                    if vparse(ver) is None:
                        errors.append("go.mod:%d 版本号不可解析：%s %s" % (lineno, mod, ver))
                        continue
                    deps.append(("Go", mod, ver, "go.mod", lineno))

    lock = os.path.join(root, LOCK_REL)
    if not os.path.exists(lock):
        uncovered.append("%s：不存在（前端依赖未纳入判断）" % LOCK_REL)
    else:
        with open(lock, encoding="utf-8") as fh:
            lines = fh.readlines()
        cur, found = None, 0
        for lineno, raw in enumerate(lines, 1):
            key = NPM_KEY.match(raw.rstrip("\n"))
            if key:
                cur = (key.group(1), lineno)
                continue
            if cur is None:
                continue
            ver = NPM_VER.match(raw.rstrip("\n"))
            if ver:
                name = cur[0].split("node_modules/")[-1]
                raw_ver = ver.group(1)
                if vparse(raw_ver) is None:
                    errors.append("%s:%d 版本号不可解析：%s %s" % (LOCK_REL, lineno, name, raw_ver))
                else:
                    deps.append(("npm", name, raw_ver, LOCK_REL, lineno))
                found += 1
                cur = None
        # 与 JSON 权威解析交叉核对：文本扫描漏包 = 覆盖不全 = 红
        with open(lock, encoding="utf-8") as fh:
            data = json.load(fh)
        version = data.get("lockfileVersion")
        if not isinstance(version, int) or version < 2:
            # lockfileVersion 1 的条目在 `dependencies` 而不是 `packages`：本闸读不到 → 不能算"扫过了"。
            errors.append("%s：lockfileVersion=%r < 2，条目不在 `packages` 段 → 读不到坐标（未验证 = 红）。"
                          "用 npm>=7 重新生成锁文件" % (LOCK_REL, version))
        expect = sum(1 for k in (data.get("packages") or {}) if k.startswith("node_modules/"))
        if found != expect:
            errors.append("%s：文本扫描到 %d 个包，JSON 里有 %d 个 → 解析不完整（未验证 = 红）"
                          % (LOCK_REL, found, expect))
        if expect == 0:
            errors.append("%s：`packages` 段里 0 个依赖 → 没有坐标可判（扫描面为空 = 未验证 = 红）"
                          % LOCK_REL)
    for eco in ("Go", "npm"):
        if not [d for d in deps if d[0] == eco]:
            errors.append("生态 %s 一个依赖坐标都没解析到（文件缺失/为空/结构变了）→ 未验证 = 红" % eco)
    if java_bom is not None:
        bdeps, berrors = read_java_bom(java_bom)
        deps.extend(bdeps)
        errors.extend(berrors)
    return deps, uncovered, errors


# ---------------------------------------------------------------- 数据加载
def coords_fingerprint(deps):
    """依赖**包名**集合的指纹（不含版本）。

    为什么不含版本：OSV 是按包名查、区间在本地算，单纯升版本不需要重刷快照；
    但**新增/换掉一个包名**就必须重刷，否则这个坐标从没被查过（= 假绿）。
    """
    names = sorted({"%s:%s" % (eco, name) for eco, name, _v, _rel, _ln in deps})
    return hashlib.sha256("\n".join(names).encode("utf-8")).hexdigest()


def load_snapshot(path):
    """返回 (rows, header, errors)；header: {generated_at, coords_sha256, coords_sha256_<eco>, dropped}。"""
    errors, rows = [], []
    header = {"generated_at": None, "coords_sha256": None, "dropped": None}
    if not os.path.exists(path):
        return [], header, ["快照不存在：%s（没有数据就没有门禁）" % path]
    with open(path, encoding="utf-8") as fh:
        for lineno, raw in enumerate(fh, 1):
            line = raw.rstrip("\n")
            if not line.strip():
                continue
            if line.lstrip().startswith("#"):
                for key, pat in (("generated_at", r"^#\s*generated_at:\s*(\d{4}-\d{2}-\d{2})\s*$"),
                                 ("coords_sha256", r"^#\s*coords_sha256:\s*([0-9a-f]{64})\s*$"),
                                 ("dropped", r"^#\s*dropped:\s*(\d+)\s*$")):
                    m = re.match(pat, line)
                    if m:
                        header[key] = m.group(1)
                m = re.match(r"^#\s*coords_sha256_(\w+):\s*([0-9a-f]{64})\s*$", line)
                if m:
                    header["coords_sha256_%s" % m.group(1).lower()] = m.group(2)
                continue
            f = line.split("\t")
            if len(f) < 5:
                errors.append("快照第 %d 行不是 ≥5 列（id/ecosystem/module/affected/severity）：%s"
                              % (lineno, line[:80]))
                continue
            vid, eco, module, expr, sev = f[0].strip(), f[1].strip(), f[2].strip(), f[3].strip(), f[4].strip()
            if eco not in ("Go", "npm", "Maven"):
                errors.append("快照第 %d 行生态未知：%r" % (lineno, eco))
                continue
            conds = parse_expr(expr)
            if conds is None:
                errors.append("快照第 %d 行区间不可解析：%r（id=%s）" % (lineno, expr, vid))
                continue
            rows.append({"id": vid, "eco": eco, "module": module, "expr": expr,
                         "conds": conds, "sev": sev, "line": lineno})
    return rows, header, errors


def load_allowlist(path, today=None):
    """返回 (entries, errors)；entries: {(id, eco, module): (reason, expires_date)}。"""
    today = today or datetime.date.today()
    entries, errors = {}, []
    if not os.path.exists(path):
        return entries, errors
    with open(path, encoding="utf-8") as fh:
        for lineno, raw in enumerate(fh, 1):
            line = raw.rstrip("\n")
            if not line.strip() or line.lstrip().startswith("#"):
                continue
            f = line.split("\t")
            if len(f) != 5:
                errors.append("白名单第 %d 行不是 5 列（id/ecosystem/module/reason/expires）：%s"
                              % (lineno, line[:80]))
                continue
            vid, eco, module, reason, expires = [x.strip() for x in f]
            if not reason:
                errors.append("白名单第 %d 行理由为空（id=%s）：无理由豁免等于没有豁免" % (lineno, vid))
                continue
            try:
                exp = datetime.date.fromisoformat(expires)
            except ValueError:
                errors.append("白名单第 %d 行到期日不是 YYYY-MM-DD：%r" % (lineno, expires))
                continue
            if (exp - today).days > MAX_ALLOW_DAYS:
                errors.append("白名单第 %d 行到期日过远（%s，上限 %d 天）：长期豁免等于没有到期（D23① 的 30 天上限精神）"
                              % (lineno, expires, MAX_ALLOW_DAYS))
                continue
            entries[(vid, eco, module)] = (reason, exp)
    return entries, errors


# ---------------------------------------------------------------- 判定
def blocking(row):
    """该命中行是否阻断。

    Go：一律阻断（官方 OSV 不带 CVSS，坐标级命中即红）。
    npm / Maven：HIGH/CRITICAL 阻断；MODERATE/LOW 只提示；**缺少级别（`-`）或未知级别一律阻断**
    （缺级别 ≠ 低危：`MAL-*` 恶意包公告常常没有 severity，放行等于给它开绿灯）。
    """
    if row["eco"] == "Go":
        return True
    if row["id"].startswith("MAL-"):
        return True
    sev = row["sev"].upper()
    if sev in BLOCK_NPM:
        return True
    return sev not in ("MODERATE", "LOW", "MEDIUM")


def evaluate(root, snapshot, allowlist, max_age_days, today=None, out=sys.stdout, java_bom=None):
    """返回退出码；同时把证据行写到 out。java_bom：CycloneDX 聚合 BOM 路径（None = Maven 生态未覆盖）。"""
    today = today or datetime.date.today()
    emit = lambda s: print(s, file=out)
    fails = []

    deps, uncovered, errors = read_deps(root, java_bom=java_bom)
    rows, header, snap_errors = load_snapshot(snapshot)
    entries, allow_errors = load_allowlist(allowlist, today=today)
    fails.extend(errors)
    fails.extend(snap_errors)
    fails.extend(allow_errors)

    for u in uncovered:
        emit("[UNCOVERED] " + u)
    # D23③：没给 BOM = Maven 生态没判 = 必须显式说，不许静默（CI 的 vuln job 必须传 --java-bom）。
    if java_bom is None:
        emit("[UNCOVERED] sdk/java：未提供 --java-bom（CycloneDX bom.json）→ Maven 生态未纳入本次判定"
             "（D23③；CI vuln job 必须消费 sdk job 上传的 artifact）")

    # 数据面三条"没数据就算绿"的缝：一次判死（Owl II P0-1/P0-3）
    if not rows:
        fails.append("快照 0 行公告：没有数据 ≠ 没有漏洞（请重跑 scripts/vuln-snapshot-refresh.py）")
    dep_ecos = {d[0] for d in deps}
    have_ecos = {r["eco"] for r in rows}
    for eco in sorted(dep_ecos - have_ecos):
        fails.append("快照里没有任何 %s 侧公告，但依赖里有 %s 坐标 → 该生态等于没扫（未验证 = 红）"
                     % (eco, eco))
    if deps and header["coords_sha256"] is None:
        fails.append("快照缺少 `# coords_sha256:` 头 → 无法判断快照是否覆盖当前依赖坐标（未验证 = 红）")
    elif deps:
        if dep_ecos >= have_ecos:
            # 全生态运行（CI：带 BOM）→ 全集指纹，口径与 #127 逐字节一致。
            want = coords_fingerprint(deps)
            if want != header["coords_sha256"]:
                fails.append("快照与当前依赖坐标不一致（快照指纹 %s… ≠ 当前 %s…）：**新增 / 删掉 / 换掉**依赖后"
                             "都必须重跑 `python3 scripts/vuln-snapshot-refresh.py` 并提交 diff"
                             % (header["coords_sha256"][:12], want[:12]))
        else:
            # 子集运行（本地没传 --java-bom）：快照指纹是全集（含 Maven），本地算不出来。
            # 退到**分生态指纹**（快照头 `# coords_sha256_<eco>:`，与全集同算法）——逐生态对，
            # 口径没有变松：CI 全生态运行仍查全集指纹。
            for eco in sorted(dep_ecos):
                key = "coords_sha256_%s" % eco.lower()
                if header.get(key) is None:
                    fails.append("快照缺 `%s:` 分生态指纹头（本运行只判 %s 生态，无法核对坐标覆盖）→ 未验证 = 红"
                                 % (key, eco))
                    continue
                eco_deps = [d for d in deps if d[0] == eco]
                want = coords_fingerprint(eco_deps)
                if want != header[key]:
                    fails.append("快照 %s 生态坐标与当前不一致（快照 %s… ≠ 当前 %s…）：该生态有依赖新增/删掉/换掉，"
                                 "必须重跑 `python3 scripts/vuln-snapshot-refresh.py` 并提交 diff"
                                 % (eco, header[key][:12], want[:12]))
    if header["dropped"] is None:
        fails.append("快照缺少 `# dropped:` 头 → 无法判断生成时有没有丢区间（未验证 = 红）")
    elif header["dropped"] != "0":
        fails.append("快照生成时有 %s 条区间因不可比较版本被丢弃（生产端已判红，见 refresh 输出）"
                     % header["dropped"])

    # 快照新鲜度：库旧 = 没扫
    age = None
    generated = header["generated_at"]
    if generated is None:
        fails.append("快照缺少 `# generated_at: YYYY-MM-DD` 头（无法判断数据新旧 = 未验证 = 红）")
    else:
        gen_date = None
        try:
            gen_date = datetime.date.fromisoformat(generated)
        except ValueError:
            fails.append("快照 generated_at 不是 YYYY-MM-DD：%r" % generated)
        if gen_date is not None and gen_date > today:
            fails.append("快照 generated_at（%s）在未来日期 → 手误的年份会让门禁放过好几年" % generated)
        elif gen_date is not None:
            age = (today - gen_date).days
            if age > max_age_days:
                fails.append("快照已 %d 天未更新（上限 %d 天）：请在有网环境跑 "
                             "`python3 scripts/vuln-snapshot-refresh.py` 并提交 diff（库旧等于没扫）"
                             % (age, max_age_days))

    # 命中判定
    by_coord = {}
    for eco, module, ver, rel, lineno in deps:
        by_coord.setdefault((eco, module), []).append((ver, vparse(ver), rel, lineno))
    blocked, allowed, notes = [], [], []
    hit_keys = set()
    for row in rows:
        for ver, parsed, rel, lineno in by_coord.get((row["eco"], row["module"]), []):
            if not vmatch(parsed, row["conds"]):
                continue
            key = (row["id"], row["eco"], row["module"])
            hit_keys.add(key)
            loc = "%s:%d" % (rel, lineno)
            where = "%s %s（命中区间 %s，级别 %s）" % (loc, row["module"] + "@" + ver, row["expr"], row["sev"])
            if not blocking(row):
                notes.append("[INFO] %s %s —— 低于阻断阈值（npm/Maven 只阻断 HIGH/CRITICAL）" % (where, row["id"]))
                continue
            entry = entries.get(key)
            if entry is None:
                why = ("Go 侧命中即红（官方 OSV 不带 CVSS，级别记作 `%s`）" % row["sev"]
                       if row["eco"] == "Go" else "未登记的 %s 级漏洞" % row["sev"])
                blocked.append("[FAIL] %s %s —— %s，未白名单" % (where, row["id"], why))
                continue
            reason, exp = entry
            if today > exp:
                blocked.append("[FAIL] %s %s —— 白名单已过期（到期 %s），到期即重新判红：%s"
                               % (where, row["id"], exp, reason))
            else:
                allowed.append("[INFO] %s %s —— 白名单内（到期 %s）：%s" % (where, row["id"], exp, reason))

    for key, (reason, exp) in entries.items():
        # 「不再命中 → 红（白名单不许只增不减）」只对该生态**本次实际参与判定**时强制：
        # Maven 条目在本地跑（没传 --java-bom）时不算"不再命中"——生态没进判定 ≠ 豁免过期，
        # CI 的 vuln job 永远传 BOM，收紧在那里生效（D23③）。
        if key[1] not in dep_ecos:
            continue
        if key not in hit_keys:
            fails.append("白名单条目本次不再命中：%s / %s / %s —— 请删掉该行（白名单不许只增不减），"
                         "原理由：%s" % (key[0], key[1], key[2], reason))

    for line in notes + allowed:
        emit(line)
    for line in blocked:
        emit(line)
    fails.extend(blocked)

    go_n = len([d for d in deps if d[0] == "Go"])
    npm_n = len([d for d in deps if d[0] == "npm"])
    mvn_n = len([d for d in deps if d[0] == "Maven"])
    summary = ("扫描 %d 个依赖坐标（Go %d / npm %d / Maven %d）· 快照 %d 行 / %d 个公告 · 快照年龄 %s 天（上限 %d）"
               "· 阻断 %d / 白名单 %d / 仅提示 %d"
               % (len(deps), go_n, npm_n, mvn_n, len(rows), len({(r["id"], r["module"]) for r in rows}),
                  "?" if age is None else age, max_age_days, len(blocked), len(allowed), len(notes)))
    if fails:
        for f in fails:
            if f.startswith("[FAIL]"):
                continue
            emit("[FAIL] " + f)
        emit("[FAIL] 依赖漏洞门禁未通过：" + summary)
        return 1
    emit("[OK] 依赖漏洞门禁通过：" + summary)
    return 0


# ---------------------------------------------------------------- 自检（正控/负控）
SNAP_HEADER = """# generated_at: {gen}
{extra_header}# id\tecosystem\tmodule\taffected\tseverity\taliases\tsummary
"""


def _snapshot(dirpath, gen, extra="", fp="auto", gen_header=True, dropped="0", java_bom=None):
    """造一份快照夹具；默认把 `coords_sha256` 按**夹具当前依赖**算好（与生产者同源）。

    传 java_bom 时指纹也把 Maven 坐标算进去（与 CI 的三生态口径一致）。
    """
    header = ""
    if gen_header:
        header += "# generated_at: %s\n" % gen
    if fp == "auto":
        deps, _uncovered, _errors = read_deps(dirpath, java_bom=java_bom)
        header += "# coords_sha256: %s\n" % coords_fingerprint(deps)
    elif fp:
        header += "# coords_sha256: %s\n" % fp
    if dropped is not None:
        header += "# dropped: %s\n" % dropped
    path = os.path.join(dirpath, "snap.tsv")
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(SNAP_HEADER.format(gen=gen, extra_header=header) + extra)
    return path


def _allow(dirpath, lines=()):
    path = os.path.join(dirpath, "allow.tsv")
    with open(path, "w", encoding="utf-8") as fh:
        fh.write("# id\tecosystem\tmodule\treason\texpires\n")
        for ln in lines:
            fh.write(ln + "\n")
    return path


def _fixture(dirpath, gomod_requires, lock_packages=None, empty_lock=False, go_only=False):
    with open(os.path.join(dirpath, "go.mod"), "w", encoding="utf-8") as fh:
        fh.write("module probe\n\ngo 1.25.0\n\nrequire (\n")
        for mod, ver in gomod_requires:
            fh.write("\t%s %s // indirect\n" % (mod, ver))
        fh.write(")\n")
    os.makedirs(os.path.join(dirpath, "web"), exist_ok=True)
    # 默认塞一个不会被任何夹具公告命中的 npm 包：让"npm 生态必须有坐标"这条 fail-closed
    # 只在**故意**造空夹具时才触发（每个用例只测一件事）。
    pkgs = {}
    pairs = [("typescript", "5.6.3")] if lock_packages is None else lock_packages
    for name, ver in pairs:
        pkgs["node_modules/" + name] = {"version": ver}
    with open(os.path.join(dirpath, "web", "package-lock.json"), "w", encoding="utf-8") as fh:
        json.dump({"name": "probe", "lockfileVersion": 1 if empty_lock else 3, "packages": pkgs}, fh, indent=2)
    return dirpath


def self_test(out=sys.stdout):
    """离线正控/负控：全部在临时目录里造夹具，不动工作树、不联网。"""
    emit = lambda s: print(s, file=out)
    today = datetime.date.today()
    gen = today.isoformat()
    old = (today - datetime.timedelta(days=999)).isoformat()
    results = []

    GO_HIGH = "GHSA-ppp9-7jff-5vj2\tGo\tgolang.org/x/text\t<0.3.7\tHIGH\t-\tx/text/language OOB read\n"
    GO_NOSEV = "GO-2026-5970\tGo\tgolang.org/x/text\t<0.39.0\t-\tCVE-2026-56852\tInfinite loop\n"
    NPM_HIGH = "GHSA-g2v6-rqmx-r4w6\tnpm\t@vue/server-renderer\t<3.5.42\tHIGH\t-\tSSR XSS\n"
    NPM_HIGH_MOD = "GHSA-g2v6-rqmx-r4w6\tnpm\t@vue/server-renderer\t<0.0.1\tHIGH\t-\tnever matches\n"
    NPM_MOD = "GHSA-moderate\tnpm\tminimatch\t<11.0.1\tMODERATE\t-\tbrace DoS\n"

    def check(name, want_rc, runner, want_text=(), forbid_text=()):
        tmp = tempfile.mkdtemp(prefix="vulngate-")
        try:
            rc, buf = runner(tmp)
            body = "\n".join(buf)
            ok = rc == want_rc and all(t in body for t in want_text) \
                and not any(t in body for t in forbid_text)
            results.append((ok, name, rc, want_rc, buf, want_text, forbid_text))
        finally:
            shutil.rmtree(tmp, ignore_errors=True)

    def run(root, snap, allow, max_age=MAX_AGE_DAYS_DEFAULT, java_bom=None):
        buf = []
        import io
        stream = io.StringIO()
        rc = evaluate(root, snap, allow, max_age, today=today, out=stream, java_bom=java_bom)
        buf = stream.getvalue().splitlines()
        return rc, buf

    # 每个夹具的依赖与快照必须**双生态齐全**（否则会被"某生态 0 公告"那条 fail-closed 判红，
    # 那是另一个用例要测的事）。SNAP_BASE = 一条 Go + 一条 npm 公告，且都不命中默认夹具。
    SNAP_BASE = GO_HIGH + NPM_HIGH

    # ① Go 侧 HIGH 命中 → 红，且点名 文件:行号 + 公告 + 坐标
    def c1(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.3.6")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), _allow(tmp))
    check("go/HIGH 命中判红并点名 go.mod:6", 1, c1,
          want_text=["[FAIL] go.mod:6 golang.org/x/text@v0.3.6", "GHSA-ppp9-7jff-5vj2"])

    # ② 同模块升到修复版 → 绿（证明是版本敏感，不是永远红）
    def c2(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), _allow(tmp))
    check("go/修复版判绿（版本敏感）", 0, c2, want_text=["[OK] 依赖漏洞门禁通过"])

    # ③ Go 侧 severity 为 `-`（官方 OSV 不带 CVSS）→ 仍然阻断（fail-closed）
    def c3(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.38.0")])
        return run(tmp, _snapshot(tmp, gen, GO_NOSEV + NPM_HIGH), _allow(tmp))
    check("go/无 CVSS 也阻断（fail-closed）", 1, c3,
          want_text=["[FAIL] go.mod:6 golang.org/x/text@v0.38.0", "GO-2026-5970"])

    # ④ npm HIGH 命中 → 红，行号落在 package-lock.json 的 version 行
    def c4(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")], [("@vue/server-renderer", "3.5.13")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), _allow(tmp))
    check("npm/HIGH 命中判红并点名 package-lock 行号", 1, c4,
          want_text=["[FAIL] web/package-lock.json:", "@vue/server-renderer@3.5.13", "GHSA-g2v6-rqmx-r4w6"])

    # ⑤ npm MODERATE → 只提示不阻断（判据原文阈值）
    def c5(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")], [("minimatch", "9.0.9")])
        return run(tmp, _snapshot(tmp, gen, GO_HIGH + NPM_MOD), _allow(tmp))
    check("npm/MODERATE 只打 INFO 不阻断", 0, c5,
          want_text=["[INFO] web/package-lock.json:", "仅提示 1"], forbid_text=["[FAIL]"])

    # ⑥ 白名单：未过期 → 绿；过期 → 红
    def c6(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.3.6")])
        allow = _allow(tmp, ["GHSA-ppp9-7jff-5vj2\tGo\tgolang.org/x/text\t等 go1.26\t%s"
                             % (today + datetime.timedelta(days=30)).isoformat()])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), allow)
    check("白名单未过期 → 绿", 0, c6, want_text=["白名单内", "[OK]"])

    def c7(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.3.6")])
        allow = _allow(tmp, ["GHSA-ppp9-7jff-5vj2\tGo\tgolang.org/x/text\t过期了\t%s"
                             % (today - datetime.timedelta(days=1)).isoformat()])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), allow)
    check("白名单已过期 → 红", 1, c7, want_text=["白名单已过期"])

    # ⑦ 白名单条目不再命中 → 红（不许只增不减）
    def c8(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        allow = _allow(tmp, ["GHSA-ppp9-7jff-5vj2\tGo\tgolang.org/x/text\t早已修复\t%s"
                             % (today + datetime.timedelta(days=30)).isoformat()])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), allow)
    check("白名单条目不再命中 → 红", 1, c8, want_text=["白名单条目本次不再命中"])

    # ⑧ 快照过期 → 红
    def c9(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        return run(tmp, _snapshot(tmp, old, SNAP_BASE), _allow(tmp))
    check("快照过期 → 红", 1, c9, want_text=["天未更新"])

    # ⑨ 快照坏行 / 区间不可解析 → 红（数据坏了等于没扫）
    def c10(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        bad = "GHSA-x\tGo\tgolang.org/x/text\t<nonsense\tHIGH\t-\tbad\n"
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE + bad), _allow(tmp))
    check("快照区间不可解析 → 红", 1, c10, want_text=["区间不可解析"])

    # ⑩ 缺快照 → 红（不是"没数据就算绿"）
    def c11(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        return run(tmp, os.path.join(tmp, "nope.tsv"), _allow(tmp))
    check("缺快照 → 红", 1, c11, want_text=["快照不存在"])

    # ⑪ go.mod 版本不可解析 → 红（未验证 = 红）。vparse 现支持四段数字（Maven 微版本），
    #    五段数字才不可解析——夹具随之升级为五段。
    def c12(tmp):
        _fixture(tmp, [("golang.org/x/text", "v9.7.0.1.2")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), _allow(tmp))
    check("依赖版本不可解析 → 红", 1, c12, want_text=["版本号不可解析"])

    # ⑫ 白名单格式坏行 → 红
    def c13(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), _allow(tmp, ["GHSA-x\tGo\tonly-three"]))
    check("白名单格式坏行 → 红", 1, c13, want_text=["白名单第 2 行不是 5 列"])

    # ⑬ 本地路径 replace → 打 [UNCOVERED]（不静默放过）
    def c14(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0"), ("golang.org/x/sync", "v0.22.0")])
        with open(os.path.join(tmp, "go.mod"), "a", encoding="utf-8") as fh:
            fh.write("\nreplace golang.org/x/text => ../local-x-text\n")
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), _allow(tmp))
    check("本地路径 replace → 打 [UNCOVERED]", 0, c14, want_text=["[UNCOVERED] go.mod:"])

    # ⑭ 快照 0 行公告 → 红（"没数据 ≠ 没有漏洞"）
    def c15(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        return run(tmp, _snapshot(tmp, gen, ""), _allow(tmp))
    check("快照 0 行公告 → 红", 1, c15, want_text=["快照 0 行公告"])

    # ⑮ 快照缺某个生态 → 红（该生态等于没扫）
    def c16(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")], [("@vue/server-renderer", "3.5.45")])
        return run(tmp, _snapshot(tmp, gen, GO_HIGH), _allow(tmp))
    check("快照缺 npm 生态 → 红", 1, c16, want_text=["没有任何 npm 侧公告"])

    # ⑯ npm 坐标 0（lockfileVersion 1 / packages 段为空）→ 红
    def c17(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")], [], empty_lock=True)
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), _allow(tmp))
    check("npm 坐标 0（老锁文件）→ 红", 1, c17,
          want_text=["lockfileVersion=1 < 2", "生态 npm 一个依赖坐标都没解析到"])

    # ⑰ Go 坐标 0（go.mod 无 require）→ 红
    def c18(tmp):
        _fixture(tmp, [], [("@vue/server-renderer", "3.5.45")])
        return run(tmp, _snapshot(tmp, gen, NPM_HIGH), _allow(tmp))
    check("Go 坐标 0 → 红", 1, c18, want_text=["生态 Go 一个依赖坐标都没解析到"])

    # ⑱ 坐标指纹不符 → 红（新增依赖却没重刷快照 = 假绿）
    def c19(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")], [("tinyglobby", "0.2.15")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE, fp="0" * 64), _allow(tmp))
    check("坐标指纹不符 → 红", 1, c19, want_text=["快照与当前依赖坐标不一致"])

    # ⑲ 坐标指纹缺失 → 红
    def c20(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE, fp=""), _allow(tmp))
    check("坐标指纹缺失 → 红", 1, c20, want_text=["缺少 `# coords_sha256:` 头"])

    # ⑳ npm 缺级别（severity=`-`，如 MAL-* 恶意包）→ 阻断（缺级别 ≠ 低危）
    def c21(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")], [("@vue/server-renderer", "3.5.13")])
        mal = "MAL-2023-462\tnpm\t@vue/server-renderer\t<9.9.9\t-\t-\tmalicious\n"
        return run(tmp, _snapshot(tmp, gen, GO_HIGH + mal), _allow(tmp))
    check("npm 缺级别（MAL-*）→ 阻断", 1, c21, want_text=["MAL-2023-462"])

    # ㉑ 白名单到期日过远（>180 天）→ 红（长期豁免等于没到期）
    def c22(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.3.6")])
        allow = _allow(tmp, ["GHSA-ppp9-7jff-5vj2\tGo\tgolang.org/x/text\t想永久豁免\t9999-12-31"])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), allow)
    check("白名单到期日过远 → 红", 1, c22, want_text=["到期日过远"])

    # ㉓ 缺 `# dropped:` 头 → 红（与 coords_sha256 对称，不许"不写就不判"）
    def c23b(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE, dropped=None), _allow(tmp))
    check("缺 dropped 头 → 红", 1, c23b, want_text=["缺少 `# dropped:` 头"])

    # ㉒ generated_at 在未来 → 红（手误年份不许放过好几年）
    def c23(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        future = (today + datetime.timedelta(days=400)).isoformat()
        return run(tmp, _snapshot(tmp, future, SNAP_BASE), _allow(tmp))
    check("generated_at 在未来 → 红", 1, c23, want_text=["在未来日期"])

    # ---------------- Maven / CycloneDX BOM（D23③ 第二段，2026-11-13） ----------------
    # BOM 夹具：CycloneDX 1.5 聚合 BOM 的最小形态；自有模块 group 必须与 metadata.component.group
    # 一致才会被排除。MVN_SAFE = 一条不命中夹具的 Maven 公告（保证 Maven 生态"有快照行"，
    # 不与"缺生态红"那条 fail-closed 互相污染——每个用例只测一件事）。
    def _bom(dirpath, components, spec="1.5"):
        path = os.path.join(dirpath, "bom.json")
        data = {
            "bomFormat": "CycloneDX",
            "specVersion": spec,
            "metadata": {"component": {"group": "com.guanjin", "name": "guanjin-sdk-parent",
                                       "type": "library", "version": "0.1.0-SNAPSHOT"}},
            "components": [{"type": "library", "group": g, "name": n, "version": v}
                           for g, n, v in components],
        }
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(data, fh)
        return path

    OWN = [("com.guanjin", "guanjin-core", "0.1.0-SNAPSHOT"),
           ("com.guanjin", "guanjin-spring-boot2", "0.1.0-SNAPSHOT")]
    MVN_SAFE = "GHSA-java-other\tMaven\torg.example:not-in-fixture\t<9.9.9\tHIGH\t-\tno hit\n"
    MVN_HIGH = "GHSA-java-high\tMaven\torg.bouncycastle:bcprov-jdk18on\t<1.80.0\tHIGH\t-\tpadding oracle\n"
    MVN_MOD = "GHSA-java-mod\tMaven\torg.bouncycastle:bcprov-jdk18on\t<1.80.0\tMODERATE\t-\tinfo only\n"
    MVN_NOSEV = "MAL-2026-0001\tMaven\torg.bouncycastle:bcprov-jdk18on\t<1.80.0\t-\t-\tmalicious jar\n"

    # ㉔ Maven HIGH 命中 → 红，并点名 bom.json 的组件序号（bcprov 是第 3 个组件 → bom.json:3）
    def c24(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        bom = _bom(tmp, OWN + [("org.bouncycastle", "bcprov-jdk18on", "1.78.1"),
                               ("com.fasterxml.jackson.core", "jackson-databind", "2.13.5")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE + MVN_HIGH + MVN_SAFE, java_bom=bom),
                   _allow(tmp), java_bom=bom)
    check("maven/HIGH 命中判红并点名 bom.json:3", 1, c24,
          want_text=["[FAIL] bom.json:3 org.bouncycastle:bcprov-jdk18on@1.78.1", "GHSA-java-high",
                     "Maven 2"])  # 汇总行必须把 Maven 坐标数报出来

    # ㉕ 同组件升到修复版 → 绿（版本敏感，不是永远红）
    def c25(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        bom = _bom(tmp, OWN + [("org.bouncycastle", "bcprov-jdk18on", "1.80.0")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE + MVN_HIGH + MVN_SAFE, java_bom=bom),
                   _allow(tmp), java_bom=bom)
    check("maven/修复版判绿（版本敏感）", 0, c25,
          want_text=["[OK] 依赖漏洞门禁通过", "Maven 1"], forbid_text=["[FAIL]", "[UNCOVERED]"])

    # ㉖ 不传 --java-bom → 显式 [UNCOVERED] sdk/java（D23③ 第一段：不许静默跳过），且其余生态照判
    def c26(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), _allow(tmp))
    check("maven/无 BOM → 显式 [UNCOVERED] sdk/java", 0, c26,
          want_text=["[UNCOVERED] sdk/java：未提供 --java-bom"])

    # ㉗ 传了 --java-bom 但文件缺失 → 红（artifact 缺失 = 未验证 = 红，D23③②）
    def c27(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        missing = os.path.join(tmp, "nope.json")
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE + MVN_SAFE, java_bom=missing),
                   _allow(tmp), java_bom=missing)
    check("maven/BOM 文件缺失 → 红", 1, c27, want_text=["--java-bom 指定的 BOM 不存在"])

    # ㉘ BOM 非法 JSON → 红（数据坏了等于没扫）
    def c28(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        path = os.path.join(tmp, "bom.json")
        with open(path, "w", encoding="utf-8") as fh:
            fh.write("{not-json")
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE, java_bom=path), _allow(tmp), java_bom=path)
    check("maven/BOM 非法 JSON → 红", 1, c28, want_text=["不是合法 JSON"])

    # ㉙ BOM 0 组件 → 红（扫描面为空 = 未验证 = 红）
    def c29(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        path = _bom(tmp, [])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE, java_bom=path), _allow(tmp), java_bom=path)
    check("maven/BOM 0 组件 → 红", 1, c29, want_text=["components 为空/缺失"])

    # ㉚ BOM 只剩自有模块（全部被排除）→ 红：0 个第三方坐标可判
    def c30(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        path = _bom(tmp, OWN)
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE, java_bom=path), _allow(tmp), java_bom=path)
    check("maven/全是自有模块 → 红（0 第三方坐标）", 1, c30, want_text=["全是本仓自有模块"])

    # ㉛ Maven MODERATE → 只提示不阻断（与 npm 同阈值）
    def c31(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        bom = _bom(tmp, OWN + [("org.bouncycastle", "bcprov-jdk18on", "1.78.1")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE + MVN_MOD + MVN_SAFE, java_bom=bom),
                   _allow(tmp), java_bom=bom)
    check("maven/MODERATE 只打 INFO 不阻断", 0, c31,
          want_text=["[INFO] bom.json:3 org.bouncycastle:bcprov-jdk18on@1.78.1", "仅提示 1"],
          forbid_text=["[FAIL]"])

    # ㉜ Maven 缺级别（MAL-* / severity=`-`）→ 阻断（缺级别 ≠ 低危）
    def c32(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        bom = _bom(tmp, OWN + [("org.bouncycastle", "bcprov-jdk18on", "1.78.1")])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE + MVN_NOSEV + MVN_SAFE, java_bom=bom),
                   _allow(tmp), java_bom=bom)
    check("maven/缺级别（MAL-*）→ 阻断", 1, c32, want_text=["MAL-2026-0001"])

    # ㉝ Maven 四段微版本（2.13.4.2 形态）：版本与区间边界都能解析且语义正确
    def c33(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        bom = _bom(tmp, OWN + [("com.fasterxml.jackson.core", "jackson-databind", "2.13.2")])
        row = ("GHSA-micro\tMaven\tcom.fasterxml.jackson.core:jackson-databind\t>=2.13.0,<2.13.4.2\tHIGH\t-\tmicro range\n")
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE + row, java_bom=bom), _allow(tmp), java_bom=bom)
    check("maven/四段微版本区间命中 → 红", 1, c33,
          want_text=["[FAIL] bom.json:3 com.fasterxml.jackson.core:jackson-databind@2.13.2", "GHSA-micro"])

    # ㉞ Maven 白名单条目在**无 BOM**（本地跑）时不判"不再命中"——生态没进判定 ≠ 豁免过期（D23③）
    def c34(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        allow = _allow(tmp, ["GHSA-java-high\tMaven\torg.bouncycastle:bcprov-jdk18on\t还没扫 Maven\t%s"
                             % (today + datetime.timedelta(days=30)).isoformat()])
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE), allow)
    check("maven/无 BOM 时 Maven 白名单不判不再命中", 0, c34,
          want_text=["[UNCOVERED] sdk/java", "[OK]"], forbid_text=["不再命中"])

    # ㉟ Maven 发布后补丁 .SR<n>（Spring Service Release）：2.5.7.SR5 ∈ [SR0, SR023) → 红（Owl 评审点名的漏报路径）
    def c35(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        bom = _bom(tmp, OWN + [("org.springframework", "spring-core", "2.5.7.SR5")])
        row = ("GHSA-wv88-pf73-x22p\tMaven\torg.springframework:spring-core\t>=2.5.7.SR0,<2.5.7.SR023\tHIGH\t-\tSR range\n")
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE + row, java_bom=bom), _allow(tmp), java_bom=bom)
    check("maven/SR 发布后补丁区间命中 → 红", 1, c35,
          want_text=["[FAIL] bom.json:3 org.springframework:spring-core@2.5.7.SR5", "GHSA-wv88-pf73-x22p"])

    # ㊱ 预发布序号数值感知（RC10 > RC2，不按字典序）：RC5 ∈ [RC2, RC10) → 红
    def c36(tmp):
        _fixture(tmp, [("golang.org/x/text", "v0.41.0")])
        bom = _bom(tmp, OWN + [("com.example", "probe", "5.0.0.RC5")])
        row = "GHSA-rc-order\tMaven\tcom.example:probe\t>=5.0.0.RC2,<5.0.0.RC10\tHIGH\t-\trc ordering\n"
        return run(tmp, _snapshot(tmp, gen, SNAP_BASE + row, java_bom=bom), _allow(tmp), java_bom=bom)
    check("maven/预发布序号数值感知（RC5 ∈ [RC2,RC10)）→ 红", 1, c36,
          want_text=["[FAIL] bom.json:3 com.example:probe@5.0.0.RC5", "GHSA-rc-order"])

    bad = [r for r in results if not r[0]]
    for ok, name, rc, want, buf, want_text, forbid in results:
        body = "\n".join(buf)
        if ok:
            emit("  [OK] self-test/%s（exit=%d）" % (name, rc))
            continue
        emit("  [FAIL] self-test/%s：期望 exit=%d 实际 %d" % (name, want, rc))
        for t in want_text:
            if t not in body:
                emit("         缺少期望输出：%r" % t)
        for t in forbid:
            if t in body:
                emit("         出现了不该有的输出：%r" % t)
        emit("         ---- 实际输出 ----")
        for line in buf:
            emit("         " + line)
    if bad:
        emit("[FAIL] 漏洞门禁自检未通过（%d/%d）" % (len(bad), len(results)))
        return 1
    emit("[OK] 漏洞门禁自检 %d 项全过（离线，不联网、不动工作树）" % len(results))
    return 0


# ---------------------------------------------------------------- CLI
def main(argv):
    ap = argparse.ArgumentParser(description="AC-ENG-07 依赖漏洞门禁（离线快照，零网络）")
    ap.add_argument("--root", default=ROOT_DEFAULT)
    ap.add_argument("--snapshot", default=None)
    ap.add_argument("--allowlist", default=None)
    ap.add_argument("--max-age-days", type=int, default=MAX_AGE_DAYS_DEFAULT)
    ap.add_argument("--java-bom", default=None,
                    help="CycloneDX 1.x 聚合 BOM（sdk job 的 artifact；不传 = Maven 生态未覆盖，显式打 [UNCOVERED]）")
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args(argv)

    if args.self_test:
        return self_test()
    root = os.path.abspath(args.root)
    snap = args.snapshot or os.path.join(root, SNAPSHOT_REL)
    allow = args.allowlist or os.path.join(root, ALLOWLIST_REL)
    print("== 依赖漏洞门禁（离线快照；零网络）==")
    print("  快照：%s" % os.path.relpath(snap, root))
    print("  白名单：%s" % os.path.relpath(allow, root))
    if args.java_bom:
        print("  Java BOM：%s（CycloneDX，Maven 生态）" % args.java_bom)
    return evaluate(root, snap, allow, args.max_age_days, java_bom=args.java_bom)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
