## 这块是什么

纯文档 PR（B 档）：两件事一次收口，`git diff origin/main...HEAD` 只有 `docs/decisions.md` + `docs/sdk-contract.md`，不动代码 / migrations / go.mod / package.json。

### 1. `decisions.md` D23 状态行 → 已生效（spencer 追认，2026-11-13）

- **② 已追认**并已随 #127 squash 合入 main（`d2cb689`，按评审锚 `aaceeb6` 收口）落地：`web/package.json` vue 3.5.45 / vite 6.4.6。
- **① 已解锁执行**（归 Ark，期限 2026-11-25）：`go.mod` 1.25.0 → 1.26.0、不写 `toolchain` 行——本 PR **不含** go.mod，另开 A 档 PR 单独走 Owl；「Gate 删 10 条 stdlib 白名单」已被 #127 事实完成（`.trivyignore.yaml` 已删、`vuln-allowlist.tsv` 现为 0 条），D28⑤ 收口走「升 Go 1.26」路径。
- **③ 第一段**（`[UNCOVERED]` 显式标注）已随 #127 落地；第二段（离线 SBOM）期限不变（2026-12-10）。
- 原禁改条款（「追认前不得据此改 `go.mod` / `package.json`」）解除。

### 2. 新增 D29：断言验签算法唯一口径（2026-11-13 spencer 拍板）

**#51 的 alg 二选一裁定 = 平台 keygen 写死 SM2，唯一口径、预留扩展但不留开关（无 alg 配置键）。**

- 性质 = **纠错，不进 CH、判据零改动**：基线 `技术栈:35` / `数据模型:62/812` 本就冻结「对外令牌 `SM3withSM2` / 网关断言 `SM2`」双轨，`000013` CHECK 把 `purpose='gateway'` 绑死 `SM2`，平台 keygen 与 signing 已按此实现；契约 M2 行原写 `SM3withSM2` 与同文件 `GJ-CAP-01` 能力行自相矛盾，且使 Java `JwksSet` 按 alg 过滤时把真网关键筛掉（#51 实测真机 100% 401）。
- **`sdk-contract.md` 三处订正**（§4.1 M2 行、§4.5 步②、§7 `assertion-verify.json` 向量行）：断言语境 `SM3withSM2` → `SM2`；M2 行同时补死「无协商降级、无 fallback、无 alg 配置键」。快照升 **v9**（732 行 / md5 `0d5dfc923d9db6b1e3cfe692352cb915`，`head -n 727 | md5sum` 可复现，已本地核对一致）。
- 对外令牌（`id_token`/`access_token`，alg=`SM3withSM2`）**不在本刀**：`acceptance-criteria.md` 一字未动（该文件里 `SM3withSM2` 三处全是对外令牌语境）。
- SDK 代码分工：**Java 侧 Nova 已交付 PR #132**（含 BC JCA 查找名与对外 alg 标识两层拆开）；**Go 侧随 #51 由 spencer 亲自改**。

## 本地证据

`go vet ./...` exit 0；`go test ./...` 全 ok；`web` 下 `vue-tsc --noEmit` exit 0（`make gates` 的 `test-audit-serial` 需真 PG，本地必然红，交 CI integration）。

## 风险

无。两份文件均为登记/订正；锚点索引 D29 行已加；无悬空引用（`notes/` 未新增文件）。
