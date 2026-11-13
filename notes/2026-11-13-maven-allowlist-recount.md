# 33 条 Maven 白名单分族复算（2026-11-13，Owl #138 P0 收口）

> 触发：Owl 评审 PR #138 指出 `docs/qa/vuln-allowlist.tsv` 头注释的「24 同 major / 9 跨 major」不可复算
> （按注释写的口径机械复算是 31/2；Owl 另给 23/10；我自己按"修复版在同一发布线是否已发布"试算是 14/19——三个口径都不是真值）。
> 我复算后确认**旧账两个数都错**：真值是 **A 12 / B 11 / C 10**，且号错了 1 条（`spring-security-crypto`
> 被记成"同 major 小跳可立刻修"，实际同 major 内没有干净版本）。已改白名单 `reason` 列（33 行）+ 两份文档。

## 1. 结论

| 族 | 条数 | 判据 | 责任人 |
|---|---:|---|---|
| **A** 同 minor 线补丁已发布 | **12** | 目标版本与现装**同 `major.minor`** | Nova：一行版本覆盖 + 回归 |
| **B** 同 major 内需升 minor | **11** | 目标同 major、跨 minor | Nova：升 minor + 回归 |
| **C** 同 major 内无干净版本 | **10** | 现 major 内**没有任何已发布版本**能清掉该公告全部区间 | Nova+Spec：需跨 major / Boot 3 |
| 合计 | **33** | 全 Maven，到期 2026-12-13，CRITICAL 5 / HIGH 28 | |

**为什么旧账 24/9 错**：那一版按「公告的 fix 上界与组件是否同 major」粗判，**没核该版本是否真的发布过**。
典型反例：`spring-security-crypto@5.7.11` 的 5.7 线要求 `5.7.16`，而 Maven Central 上 5.7 线止于 `5.7.14`、
5.8 线止于 `5.8.16`（<5.8.18），6.0/6.1/6.2 的修复版也不存在，**真正干净的只有 `6.3.8`** → 属 C 族。
同类还有 `spring-webmvc`（要 5.3.40/5.3.49，均未发布）、`spring-core`（5.3.44 未发布）、
`spring-boot`（2.7.24/2.7.32 未发布，2.7 线止于 2.7.18）。

## 2. 方法（可复算，不改仓、不改门禁）

对**每条公告**取该模块在 `docs/qa/vuln-advisories.tsv` 里的**全部受影响区间**，
到 Maven Central `maven-metadata.xml` 找「**已发布** 且 **不在任一区间**」的**最小**版本作目标，
比较口径与引擎一致（复用 `scripts/vuln-gate.py` 的 `vparse/vcmp/vmatch`）。

```bash
# ① 取 16 个模块的已发布版本清单（每条命令一次，落盘复用）
for gav in $(cut -f3 docs/qa/vuln-allowlist.tsv | grep -v '^#' | grep ':' | sort -u); do
  g="${gav%%:*}"; a="${gav##*:}"; gp="$(echo "$g" | tr '.' '/')"
  curl -sS -o "meta/${gav/:/_}.xml" "https://repo1.maven.org/maven2/$gp/$a/maven-metadata.xml"
done

# ② 逐行算目标版本并分族（同一算法，落在 notes/ 外的临时脚本里跑，不入库）
python3 recount.py    # 见下：vparse/vcmp/vmatch 全部来自 scripts/vuln-gate.py
```

`recount.py` 的核心（20 行）：

```python
ranges[(id, module)] = [该公告该模块的全部 affected 区间]
for v in published(module):                       # Maven Central
    if vcmp(v, installed) < 0: continue           # 只升不降
    if any(vmatch(v, conds) for conds in ranges[(id, module)]): continue   # 必须清掉全部区间
    cand.append(v)
target = min(cand)                                # 最小可用目标
family = 'A' if target[:2] == installed[:2] else 'B' if target[0] == installed[0] else 'C'
```

**为什么不是"只看该条命中的那个区间"**：白名单按 `(公告 id, 模块)` 生效，门禁会拿**所有**区间去匹配。
只清一个区间（例：把 `spring-security-crypto` 升到 `5.8.0`）会立刻命中 5.8 线那条区间 → 仍然红。

## 3. 逐条结果（33 条，`reason` 列已按此改写）

### A 族（12 条，Nova 一行版本覆盖）

| # | 公告 | 模块 | 现装 | 目标 | 级别 |
|---:|---|---|---|---|---|
| 1 | `GHSA-vmq6-5m68-f53m` | `ch.qos.logback:logback-classic` | `1.2.12` | **`1.2.13`** | HIGH |
| 2 | `GHSA-gm62-rw4g-vrc4` | `ch.qos.logback:logback-core` | `1.2.12` | **`1.2.13`** | HIGH |
| 3 | `GHSA-vmq6-5m68-f53m` | `ch.qos.logback:logback-core` | `1.2.12` | **`1.2.13`** | HIGH |
| 4 | `GHSA-558v-64gr-wgg4` | `io.netty:netty-codec` | `4.1.101.Final` | **`4.1.136.Final`** | HIGH |
| 5 | `GHSA-mj4r-2hfc-f8p6` | `io.netty:netty-codec` | `4.1.101.Final` | **`4.1.133.Final`** | HIGH |
| 6 | `GHSA-3qp7-7mw8-wx86` | `io.netty:netty-handler` | `4.1.101.Final` | **`4.1.135.Final`** | HIGH |
| 7 | `GHSA-4g8c-wm8x-jfhw` | `io.netty:netty-handler` | `4.1.101.Final` | **`4.1.118.Final`** | HIGH |
| 8 | `GHSA-c4c3-7fpv-j4q5` | `io.netty:netty-handler` | `4.1.101.Final` | **`4.1.137.Final`** | CRITICAL |
| 9 | `GHSA-f3jh-qvm4-mg39` | `org.springframework.security:spring-security-core` | `5.7.11` | **`5.7.12`** | HIGH |
| 10 | `GHSA-2wrp-6fg6-hmc5` | `org.springframework:spring-web` | `5.3.31` | **`5.3.34`** | HIGH |
| 11 | `GHSA-ccgv-vj62-xf9h` | `org.springframework:spring-web` | `5.3.31` | **`5.3.32`** | HIGH |
| 12 | `GHSA-hgjh-9rj2-g67j` | `org.springframework:spring-web` | `5.3.31` | **`5.3.33`** | HIGH |

（netty 四条 collector 目标取最大者 `4.1.137.Final` 即可一次清掉；分条列是为了每行自证。）

### B 族（11 条，Nova 升 minor + 回归）

| # | 公告 | 模块 | 现装 | 目标 | 级别 |
|---:|---|---|---|---|---|
| 1 | `GHSA-7hhh-6rmp-j9qf` | `com.fasterxml.jackson.core:jackson-core` | `2.13.5` | **`2.18.11`** | HIGH |
| 2 | `GHSA-h46c-h94j-95f3` | `com.fasterxml.jackson.core:jackson-core` | `2.13.5` | **`2.15.0`** | HIGH |
| 3 | `GHSA-r7wm-3cxj-wff9` | `com.fasterxml.jackson.core:jackson-core` | `2.13.5` | **`2.18.8`** | HIGH |
| 4 | `GHSA-cxp5-3px4-pw24` | `com.fasterxml.jackson.core:jackson-databind` | `2.13.5` | **`2.18.11`** | HIGH |
| 5 | `GHSA-j3rv-43j4-c7qm` | `com.fasterxml.jackson.core:jackson-databind` | `2.13.5` | **`2.18.8`** | HIGH |
| 6 | `GHSA-wv8q-qhhj-9h54` | `com.fasterxml.jackson.core:jackson-databind` | `2.13.5` | **`2.18.11`** | HIGH |
| 7 | `GHSA-78wr-2p64-hpwj` | `commons-io:commons-io` | `2.11.0` | **`2.14.0`** | HIGH |
| 8 | `GHSA-574f-3g2m-x479` | `org.bouncycastle:bcprov-jdk18on` | `1.78.1` | **`1.80.2`** | CRITICAL |
| 9 | `GHSA-9pwp-9qqc-pr26` | `org.bouncycastle:bcprov-jdk18on` | `1.78.1` | **`1.85`** | CRITICAL |
| 10 | `GHSA-p93r-85wp-75v3` | `org.bouncycastle:bcprov-jdk18on` | `1.78.1` | **`1.80.2`** | HIGH |
| 11 | `GHSA-3mc7-4q67-w48m` | `org.yaml:snakeyaml` | `1.30` | **`1.31`** | HIGH |

（B 族里 jackson/bcprov 要同时升到族内最大目标 —— `2.18.11` / `1.85` —— 才能一次清完同模块的多条公告。）

### C 族（10 条，跨 major，Nova+Spec 排期）

| # | 公告 | 模块 | 现装 | 目标 | 级别 |
|---:|---|---|---|---|---|
| 1 | `GHSA-rc42-6c7j-7h5r` | `org.springframework.boot:spring-boot` | `2.7.18` | **`3.0.0`** | HIGH |
| 2 | `GHSA-wwpq-f5c3-7hvx` | `org.springframework.boot:spring-boot` | `2.7.18` | **`3.0.0`** | HIGH |
| 3 | `GHSA-mg83-c7gq-rv5c` | `org.springframework.security:spring-security-crypto` | `5.7.11` | **`6.3.8`** | HIGH |
| 4 | `GHSA-jmp9-x22r-554x` | `org.springframework:spring-core` | `5.3.31` | **`6.2.11`** | HIGH |
| 5 | `GHSA-775g-4xr8-78h8` | `org.springframework:spring-expression` | `5.3.31` | **`6.0.0`** | HIGH |
| 6 | `GHSA-4wrc-f8pq-fpqp` | `org.springframework:spring-web` | `5.3.31` | **`6.0.0`** | CRITICAL |
| 7 | `GHSA-3chg-m5w7-qfv5` | `org.springframework:spring-webmvc` | `5.3.31` | **`6.0.0`** | HIGH |
| 8 | `GHSA-g5vr-rgqm-vf78` | `org.springframework:spring-webmvc` | `5.3.31` | **`6.1.14`** | HIGH |
| 9 | `GHSA-pc63-qcmh-9cmg` | `org.springframework:spring-webmvc` | `5.3.31` | **`6.2.0`** | CRITICAL |
| 10 | `GHSA-mjmj-j48q-9wg2` | `org.yaml:snakeyaml` | `1.30` | **`2.0`** | HIGH |

## 4. 已知局限（别把这套数当"永远有效"）

1. **依赖 Maven Central 的当前快照**：目标版本是"**当下**已发布且干净的最小版"，上游继续发版后目标会变，**不是承诺**；
   台账到期（2026-12-13）或公告区间变化时必须重算。
2. **判的是坐标，不是可达性**：本闸没有符号级可达性分析，命中即算（更严，只会多报）。
3. **覆盖边界不变**：只含 compile/runtime 作用域组件（`includeTestScope` 未开），test 依赖不在扫描面；
   **Go 标准库仍不在扫描面**（`grep -c stdlib docs/qa/vuln-advisories.tsv` = 0）。
4. **A/B/C 只是修复成本分档**，不是风险分档：C 族的 CRITICAL（spring-web / spring-webmvc）比 A 族的 HIGH 更急 ——
   优先级按级别看，不要按族看。

## 5. 顺带纠正的旧账

- `notes/2026-11-13-d23-2-sbom-wiring-pr136.md` 第 37 行「同 major 线小跳 24 / 跨 major 9」→ 已加更正行指向本文。
- `docs/qa/vuln-scan.md` §4/§5、`docs/qa/vuln-allowlist.tsv` 头注释 → 已改为 A 12 / B 11 / C 10。
- `docs/qa/gate-criteria.md` §1.1 里 `AC-ENG-07` 的 PASS 口径仍是"两生态 + `[UNCOVERED]` 标注"（#136 前口径）
  → 已改为三生态 + artifact 锚定 + CI 出 `[UNCOVERED]` 即红。
