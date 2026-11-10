# `factors.disabled` 缺行/坏值 fail-closed + 未知类型保留（2026-11-11）

## 一、定稿口径（Ark 裁定、主持人改判后的最终版）

| 情形 | 行为 |
|---|---|
| **缺行** | 读 → `ErrFactorDisabledMissing`；写 → 同样（**不**顺手建行）；启动期 fail-fast |
| **坏值**（`null` / `{}` / 非字符串数组 / 含空串） | `ErrFactorDisabledStore`；启动期 fail-fast |
| **未知因子类型**（数组里有、本进程注册表没有） | **读保留原文、读-改-写不丢**；启动期 **WARN 点名**（带全量集合） |

为什么这么分（这是上一轮我判错、Ark 判对的地方）：
- 缺行/坏值当空集是**安全方向的静默降级**——"该禁的因子全可用"，系统照常跑、日志只少一行 →
  必须当场炸（启动 fail-fast + 运行期 500）。
- 未知类型如果 fail-fast，滚动升级时旧节点会 crash loop；而"读-改-写时把它丢掉"才是真事故：
  新版本加了类型、旧节点不认识，一丢，那个类型在其它节点上就**意外变为可用**。
  所以：**保留不丢 + 点名告警**。

## 二、改了什么

1. **读**（`PlatformFactorReader.Disabled`）：改 `Row().Scan`，`sql.ErrNoRows` → `ErrFactorDisabledMissing`；
   删掉 `raw == "" → nil, nil` 那条静默分支；`decodeDisabledFactors` 对 `null`/空串也一律当坏值
   （`json.Unmarshal` 对 `null` 不报错，必须显式挡——空集只能用 `[]` 表达，D22 原文）。
2. **写**（`SetEnabled`）：事务内仍是「advisory 锁 → `FOR UPDATE` 读」，但缺行直接 `ErrFactorDisabledMissing`；
   末尾的 `INSERT … ON CONFLICT DO UPDATE` 改成**纯 UPDATE + `RowsAffected != 1` 也当缺行**——
   行由迁移 `000020` seed（D22），写入路径不再建行（否则"谁先写谁决定初始值"，并发下还会丢更新）。
3. **启动**（`runtimeconfig.go`）：`startupSpecs` 增第 4 个键 `factors.disabled`（新 `kindJSONArray`），
   `ValidateStartup` 走新的 `requiredStringArray`：存在 + `value_type='json'` + 是 jsonb 数组 + 元素非空；
   `StartupConfig` 增 `FactorsDisabled` 字段（供装配期点名用）。
4. **未知类型**：新增纯函数 `store.UnknownFactorTypes(disabled, known)`；`cmd/guanjind/wiring.go` 在装配期
   读一次并 `logger.Warn("…未注册的因子类型（保留不删；可能来自更高版本的节点，或类型名写错）")`。

## 三、证据（A/B 与反向守线都实跑）

- **反向证据（这条用例改前会绿、改后必须红）**：`TestDBPlatformFactorMissingOrBadValueIsFailClosed` ——
  缺行时 `Disabled()` 必须返回 `ErrFactorDisabledMissing`（改前是 `nil, nil`）；`SetEnabled` 同样（且**不建行**，
  `count(*)=0`）；`null`/`{}`/`"password"`/`[1,2]`/`[null]` 五种坏值逐个断言 `ErrFactorDisabledStore`；
  未知类型 `["sms","from-newer-node"]` 经一次读-改-写后仍在（`got=[from-newer-node probe-a probe-c]`）。
- **启动校验**：`TestValidateStartupRejectsBadFactorsDisabled`（`[]` 通过、两项数组通过，
  `null`/`{}`/`""`/`[1]`/`[""]` 逐个 fail-fast，`value_type=int` 也拦）；`TestStartupKeysContainsFrozenNames`
  现在钉 4 个键（含 `factors.disabled`）；`TestValidateStartupReportsAllMissingKeys` 的错误里必须点到每个键名。
- 纯函数 `TestUnknownFactorTypes` / `TestDecodeDisabledFactorsRejectsSilentEmpty`。

## 四、顺手修的两处测试卫生（都会打掉"三连绿"，都是本轮实测踩到的）

1. **"造缺行"不能删真行**：第一版用例直接 `DELETE` 全局行，`-p 8` 下并行的 `cmd/guanjind` 当场红成
   `runtime_config[factors.disabled] row is missing`（fail-closed 生效后这就是必然）。改成**必然回滚的事务**里做
   （reader/repo 绑在 tx 上），缺行只在本事务可见。
2. **仓库用例改用探针因子类型**（`probe-a/b/c`）：原来用 `password`/`totp`/`sms`，用例期间把 `password` 停掉，
   并行跑的 `cmd/guanjind` E2E 会红成 `422 FACTOR_DISABLED`。仓库逻辑（同事务审计、幂等、排序）与"是不是真实因子"无关。
3. **报 Core 一处同类问题**：`cmd/guanjind/acflow05_db_test.go` 的 `newACFLOW05Env` cleanup 仍在
   "快照到缺行 → `DELETE`"，fail-closed 生效后它会把 seed 行删掉、让后续用例连环红；建议改成"补 `[]`"。

## 五、验证

- 相关包 `-p 8`：`internal/store` 三连绿、`cmd/guanjind` 三连绿。
- 全量集成：**759 PASS / 0 SKIP / 0 FAIL**（第一次跑撞到已知的**跨包分区 flake**
  `TestAuditPartitionJobCreatesMissingMonth`——`internal/audit` 的 `TestMain` 收尾 `DropPartitions` 与并行的
  `cmd/guanjind` 抢分区，与本 PR 无关，已由 Gate 定性进 flake 台账；单包跑 3/3 绿，复跑全量即全绿）。
- 单测全绿、`make gates` 绿、`-race` 绿、`gofmt` 干净。
- 覆盖率闸原值未动：`internal/audit` 91%（floor 90）、`internal/store` **80%**（floor 75）、总 81.3%（floor 60）。
