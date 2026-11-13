#!/usr/bin/env python3
"""离线漏洞快照生成器（refresh）——**需要外网，绝不在 CI 里跑**。

为什么存在：`scripts/check-vuln.sh`（CI 门禁）必须零网络（见 ci.yml 顶部与 #125），
所以"漏洞数据"不能每次 CI 现拉，只能是一份**入库、可评审、带生成日期**的快照。
本脚本是那份快照的**唯一生产者**：有网的机器跑它 → 产出 `docs/qa/vuln-advisories.tsv` 的 diff → 走 PR 评审。

数据源：OSV（`https://api.osv.dev`）。生态覆盖三条：
  - `Go`    ：坐标来自根 `go.mod`（含 indirect）
  - `npm`   ：坐标来自 `web/package-lock.json`（平铺所有 `node_modules/*`，同名多版本逐条）
  - `Maven` ：坐标来自 **CycloneDX 1.5 聚合 BOM**（D23③ 第二段：`sdk` job 用 cyclonedx-maven-plugin
    生成 `sdk/java/target/bom.json`）。**不解析 pom**（要外网、实测 429）——refresh 与门禁消费的是
    同一份 BOM，坐标集天然同源；组件 `group:artifact@version` 逐条查。
    自有模块（`group == metadata.component.group`，即 guanjin-core / guanjin-spring-boot2）排除，
    规则与 `scripts/vuln-gate.py::read_java_bom` 逐字一致（两指纹才对得上）。

用法（在有网的开发机 / 宿主机上）：
    python3 scripts/vuln-snapshot-refresh.py            # 写 docs/qa/vuln-advisories.tsv（Go + npm）
    python3 scripts/vuln-snapshot-refresh.py --java-bom sdk/java/target/bom.json   # 三生态
    python3 scripts/vuln-snapshot-refresh.py --stdout    # 只打不写（评审时对比用）
    python3 scripts/vuln-snapshot-refresh.py --osv http://127.0.0.1:8080  # 内网镜像

产出格式（TSV，`#` 开头为注释头）：
    id <TAB> ecosystem <TAB> module <TAB> affected <TAB> severity <TAB> aliases <TAB> summary
  - `affected`：逗号分隔的比较式合取（AND），支持 `<` `<=` `>` `>=` `=` 与完整版本号；
    由 OSV 的 SEMVER/ECOSYSTEM 区间（introduced/fixed/last_affected）机械转换而来。
  - `severity`：OSV `database_specific.severity`（Go 侧多为空——Go 官方 OSV 不带 CVSS），
    空值写 `-`；**门禁不按 severity 筛**（命中即红），此列仅供评审阅读。
  - 同一 `id` 可能有多行（一个公告覆盖多个区间），门禁逐行判定。
"""

import argparse
import datetime
import json
import os
import re
import sys
import urllib.request

OSV_DEFAULT = "https://api.osv.dev"
ROOT_DEFAULT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT_REL = os.path.join("docs", "qa", "vuln-advisories.tsv")

GO_REQUIRE = re.compile(r"^\s*([\w.\-~/]+)\s+(v[0-9][^\s]*)")
GO_REPLACE = re.compile(r"^\s*([\w.\-~/]+)(?:\s+(v[0-9][^\s]*))?\s*=>\s*([\w.\-~/]+)(?:\s+(v[0-9][^\s]*))?\s*$")
NPM_NODE_MODULES = re.compile(r'^\s*"node_modules/(.+?)":\s*\{\s*$')


# ---------------- 坐标收集 ----------------
def collect_go(root):
    """读 go.mod 的 require/replace，返回 [(module, version)]（去重，跳过本地路径替换）。"""
    path = os.path.join(root, "go.mod")
    mods, replaces = [], {}
    block = None
    with open(path, encoding="utf-8") as fh:
        for raw in fh:
            line = raw.split("//")[0].strip()
            if not line:
                continue
            if line.startswith("require (") or line.startswith("replace ("):
                block = line.split()[0]
                continue
            if line == ")":
                block = None
                continue
            if block == "replace" or line.startswith("replace "):
                body = line[len("replace "):].strip() if line.startswith("replace ") else line
                m = GO_REPLACE.match(body)
                if m:
                    old, _oldver, new, newver = m.groups()
                    replaces[old] = (new, newver)  # newver=None → 本地路径替换
                continue
            if block == "require" or line.startswith("require "):
                body = line[len("require "):].strip() if line.startswith("require ") else line
                m = GO_REQUIRE.match(body)
                if m:
                    mods.append((m.group(1), m.group(2)))
    out = []
    for mod, ver in mods:
        if mod in replaces:
            new, newver = replaces[mod]
            if newver is None:
                continue  # 本地路径替换：无法按坐标扫描，门禁会打 [UNCOVERED]
            out.append((new, newver))
        else:
            out.append((mod, ver))
    return sorted(set(out))


def collect_maven(java_bom):
    """读 CycloneDX 1.x 聚合 BOM 的 components，返回 [(name, version)]，name = `group:artifact`。

    自有模块排除规则与 scripts/vuln-gate.py::read_java_bom 逐字一致：
    `group == metadata.component.group` 的是本仓模块（guanjin-core / guanjin-spring-boot2），
    不是第三方依赖。文件缺失 / 非法 JSON / specVersion 非 1.x / 组件缺 name-version →
    抛异常（refresh 端 fail-closed：BOM 坏了不许产出半份快照）。
    """
    with open(java_bom, encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, dict) or not str(data.get("specVersion", "")).startswith("1."):
        raise ValueError("%s：specVersion 非 1.x，只消费 CycloneDX 1.x" % java_bom)
    own_group = ((data.get("metadata") or {}).get("component") or {}).get("group") or ""
    comps = data.get("components") or []
    out = []
    for comp in comps:
        if not isinstance(comp, dict):
            raise ValueError("%s：components 里有非对象成员（BOM 坏了，先修生成端）" % java_bom)
        group = (comp.get("group") or "").strip()
        name = (comp.get("name") or "").strip()
        ver = (comp.get("version") or "").strip()
        if group and group == own_group:
            continue  # 本仓自有模块：门禁端同样排除
        if not name or not ver:
            raise ValueError("%s：组件缺 name/version（group=%r name=%r）" % (java_bom, group, name))
        out.append((("%s:%s" % (group, name)) if group else name, ver))
    return out


def collect_npm(root):
    """读 web/package-lock.json 的 packages 段，返回 [(包名, 版本)]。"""
    path = os.path.join(root, "web", "package-lock.json")
    if not os.path.exists(path):
        return []
    with open(path, encoding="utf-8") as fh:
        data = json.load(fh)
    pkgs = []
    for key, meta in (data.get("packages") or {}).items():
        if not key.startswith("node_modules/"):
            continue
        name = key.split("node_modules/")[-1]
        ver = meta.get("version")
        if name and ver:
            pkgs.append((name, ver))
    return sorted(set(pkgs))


# ---------------- OSV 访问 ----------------
def post_json(url, payload, timeout=30):
    req = urllib.request.Request(
        url, data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"}
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.load(resp)


def get_json(url, timeout=30):
    with urllib.request.urlopen(url, timeout=timeout) as resp:
        return json.load(resp)


def query_batch(base, pairs):
    """pairs = [(ecosystem, name)]；返回 {id: None} 形式的命中 id 集合。"""
    queries = [{"package": {"ecosystem": eco, "name": name}} for eco, name in pairs]
    hits = []
    for i in range(0, len(queries), 500):  # OSV querybatch 单次上限 1000，留余量
        chunk = queries[i:i + 500]
        data = post_json(base + "/v1/querybatch", {"queries": chunk})
        for res in data.get("results", []):
            hits.append([v["id"] for v in (res.get("vulns") or [])])
    return hits


# ---------------- 区间转换 ----------------
# 尾缀允许 `[-+X]` 之外再接受**点号尾缀**（Maven 形态：5.2.24.RELEASE / 4.1.135.Final / 5.3.39.SEC03），
# D23③ 第二段起 Maven 生态入快照。这里只做"形态放行"，语义归一化在门禁端 vparse：
# .RELEASE/.Final/.GA 剥掉、.SEC<n>/.SP<n> 剥掉后 patch+n、其余按预发布（两端口径逐字一致，见 vuln-gate.py）。
CMP_RE = re.compile(r"^(v?\d+(?:\.\d+)*)(?:[-+.][0-9A-Za-z.\-+]+)?$")


def _norm(v):
    """把 OSV 版本号规整成可比较形态；不可比较返回 None（调用方负责降级/丢弃）。"""
    if v is None:
        return None
    v = v.strip()
    if v in ("", "0"):
        return "0.0.0"
    if not CMP_RE.match(v):
        return None
    return v[1:] if v.startswith("v") else v


def range_to_expr(events):
    """OSV 的 introduced/fixed/last_affected 事件序列 → 比较式列表。

    只支持"单区间开闭"的常见形态（OSV 也允许一个区间里多次开闭）；多区间返回多个表达——
    本仓快照按"一行一个区间"落库，故这里返回 list[str]。
    不可比较的版本（如 `9.6.0b1`）会被丢弃 → 该区间不上库，但会由 refresh 打到 stderr 供人工复核。
    """
    exprs, cur = [], []
    for ev in events:
        if "introduced" in ev:
            intro = _norm(ev["introduced"])
            if intro is None:
                return [], ["introduced=%s" % ev["introduced"]]
            cur = [">=" + intro]
        elif "fixed" in ev:
            fixed = _norm(ev["fixed"])
            if fixed is None:
                return [], ["fixed=%s" % ev["fixed"]]
            cur.append("<" + fixed)
            exprs.append(",".join(cur))
            cur = []
        elif "last_affected" in ev:
            last = _norm(ev["last_affected"])
            if last is None:
                return [], ["last_affected=%s" % ev["last_affected"]]
            cur.append("<=" + last)
            exprs.append(",".join(cur))
            cur = []
    # 只有 introduced、没有 fixed/last_affected = **厂商还没发补丁**（最高危形态）：
    # 落成开区间 `>=X`，绝不允许"整条公告消失"。
    if cur:
        exprs.append(",".join(cur))
    return exprs, []


def build_rows(base, coords):
    """coords = {(ecosystem, name)}；返回 (rows, dropped)。"""
    pairs = sorted(coords)
    hits = query_batch(base, pairs)
    by_coord = {}
    for (eco, name), ids in zip(pairs, hits):
        for vid in ids:
            by_coord.setdefault(vid, []).append((eco, name))
    details = {}
    for vid in sorted(by_coord):
        details[vid] = get_json(base + "/v1/vulns/" + vid)
    rows, dropped = [], []
    for (eco, name) in pairs:
        for vid in sorted(details):
            rec = details[vid]
            if (eco, name) not in by_coord[vid]:
                continue
            matches = [aff for aff in rec.get("affected", [])
                       if (aff.get("package") or {}).get("ecosystem") == eco
                       and (aff.get("package") or {}).get("name") == name]
            ranges = [r for aff in matches for r in aff.get("ranges", [])
                      if r.get("type") in ("SEMVER", "ECOSYSTEM")]
            # SEMVER 优先（**按 (公告, 模块) 整体判**）：同一条公告常同时给 SEMVER 与 npm 风格
            # ECOSYSTEM 区间，后者会出现 `9.6.0b1` 这种不可比较版本（会被丢弃）；前者才是权威区间。
            if any(r.get("type") == "SEMVER" for r in ranges):
                ranges = [r for r in ranges if r.get("type") == "SEMVER"]
            for rng in ranges:
                exprs, why = range_to_expr(rng.get("events", []))
                if why:
                    dropped.append("%s / %s：区间含不可比较版本 %s（已丢弃，需人工复核）"
                                   % (vid, name, ";".join(why)))
                    continue
                for expr in exprs:
                    rows.append({
                        "id": vid,
                        "ecosystem": eco,
                        "module": name,
                        "affected": expr,
                        "severity": (rec.get("database_specific") or {}).get("severity") or "-",
                        "aliases": ",".join(rec.get("aliases") or []) or "-",
                        "summary": (rec.get("summary") or "").replace("\t", " ").replace("\n", " ")[:160],
                    })
    # 同 id+module+区间 只留一行（多个 aliases 记录会重复命中同一区间）
    seen, uniq = set(), []
    for r in sorted(rows, key=lambda r: (r["ecosystem"], r["module"], r["affected"], r["id"])):
        key = (r["ecosystem"], r["module"], r["affected"])
        if key in seen:
            continue
        seen.add(key)
        uniq.append(r)
    return uniq, dropped


def coords_fingerprint(coords):
    """依赖**包名**集合的指纹（与 scripts/vuln-gate.py 的算法必须逐字节一致）。"""
    import hashlib
    names = sorted({"%s:%s" % (eco, name) for eco, name in coords})
    return hashlib.sha256("\n".join(names).encode("utf-8")).hexdigest()


def render(rows, coords, base, dropped=0):
    import hashlib  # noqa: F401  （保持与 gate 同源的指纹实现）
    today = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d")
    lines = [
        "# 离线漏洞快照（AC-ENG-07 门禁的**唯一数据源**，由 scripts/vuln-snapshot-refresh.py 生成，勿手改）",
        "#",
        "# 生成方式（需要外网，**不在 CI 里跑**）：",
        "#   python3 scripts/vuln-snapshot-refresh.py            # 覆盖本文件",
        "# 评审方式：PR 里直接看本文件的 diff（新增/删除/区间变化都是一行一行的）。",
        "#",
        "# 列：id <TAB> ecosystem <TAB> module <TAB> affected <TAB> severity <TAB> aliases <TAB> summary",
        "#   affected = 逗号分隔的比较式合取（如 `>=9.7.0-beta.1,<9.7.3`；`<9.7.3` 表示修复在 9.7.3）",
        "#   severity = OSV database_specific.severity（Go 侧通常为 `-`：Go 官方 OSV 不带 CVSS）",
        "#",
        "# 门禁口径（scripts/check-vuln.sh）：**命中即红，不按 severity 筛**（比判据原文的 HIGH/CRITICAL 更严），",
        "# 例外只能进 docs/qa/vuln-allowlist.tsv 且逐条带理由 + 到期日。",
        "# 快照过期（generated_at 超过 MAX_AGE_DAYS）→ 门禁判红：库旧等于没扫。",
        "#",
        "# generated_at: " + today,
        "# coords_sha256: " + coords_fingerprint(coords),
        # 分生态指纹（与全集同算法）：本地不传 --java-bom 时只判 Go+npm，按生态核对覆盖（vuln-gate.py evaluate）。
        "# coords_sha256_go: " + coords_fingerprint(sorted(c for c in coords if c[0] == "Go")),
        "# coords_sha256_npm: " + coords_fingerprint(sorted(c for c in coords if c[0] == "npm")),
        "# coords_sha256_maven: " + coords_fingerprint(sorted(c for c in coords if c[0] == "Maven")),
        "# dropped: %d" % dropped,
        "# source: %s/v1/query（Go + npm + Maven 三条生态；坐标取自 go.mod / web/package-lock.json"
        " / CycloneDX bom.json --java-bom）" % base,
        "# coordinates: Go %d 个模块 + npm %d 个包 + Maven %d 个组件 = %d 个坐标（**坐标指纹以 coords_sha256 为准**）"
        % (len([c for c in coords if c[0] == "Go"]), len([c for c in coords if c[0] == "npm"]),
           len([c for c in coords if c[0] == "Maven"]), len(coords)),
        "# entries: %d 行（%d 个公告）" % (len(rows), len({(r["id"], r["module"]) for r in rows})),
        "#",
        "# id\tecosystem\tmodule\taffected\tseverity\taliases\tsummary",
    ]
    for r in rows:
        lines.append("\t".join([r["id"], r["ecosystem"], r["module"], r["affected"],
                                r["severity"], r["aliases"], r["summary"]]))
    return "\n".join(lines) + "\n"


def main(argv):
    ap = argparse.ArgumentParser(description="生成离线漏洞快照（需要外网）")
    ap.add_argument("--root", default=ROOT_DEFAULT)
    ap.add_argument("--osv", default=os.environ.get("OSV_BASE", OSV_DEFAULT))
    ap.add_argument("--java-bom", default=None,
                    help="CycloneDX 1.x 聚合 BOM（sdk/java/target/bom.json）；给了才扫 Maven 生态")
    ap.add_argument("--out", default=None)
    ap.add_argument("--stdout", action="store_true")
    args = ap.parse_args(argv)

    go_coords = [("Go", m) for m, _ in collect_go(args.root)]
    npm_coords = [("npm", n) for n, _ in collect_npm(args.root)]
    mvn_coords = []
    if args.java_bom:
        mvn_coords = [("Maven", n) for n, _ in collect_maven(args.java_bom)]
        if not mvn_coords:
            print("[FAIL] --java-bom 里 0 个第三方坐标（全是自有模块或 components 为空）→ 没坐标可判，拒绝产出",
                  file=sys.stderr)
            return 1
    coords = set(go_coords) | set(npm_coords) | set(mvn_coords)
    if not coords:
        print("[FAIL] 没收集到任何依赖坐标（go.mod / package-lock.json 都读不到？）", file=sys.stderr)
        return 1
    print("[INFO] 坐标：Go %d + npm %d + Maven %d = %d" % (len(go_coords), len(npm_coords),
                                                          len(mvn_coords), len(coords)), file=sys.stderr)
    rows, dropped = build_rows(args.osv, coords)
    for d in sorted(set(dropped)):
        print("[FAIL] " + d, file=sys.stderr)
    text = render(rows, coords, args.osv, dropped=len(set(dropped)))
    if args.stdout:
        sys.stdout.write(text)
        return 0
    out = args.out or os.path.join(args.root, OUT_REL)
    with open(out, "w", encoding="utf-8") as fh:
        fh.write(text)
    print("[OK] 写入 %s（%d 行 / %d 个公告）" % (os.path.relpath(out, args.root), len(rows),
                                                len({(r["id"], r["module"]) for r in rows})), file=sys.stderr)
    if dropped:
        # 丢弃 = 该区间没进快照 = 那段时间的依赖没被判过。宁可让刷新失败让人复核，也不静默发布半份数据。
        print("[FAIL] 有 %d 条区间被丢弃（见上），快照已写出但**不可用于门禁**：请人工复核后再提交"
              % len(set(dropped)), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
