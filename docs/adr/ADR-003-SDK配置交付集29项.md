# ADR-003 · SDK 配置交付集 = 29 项（无 `mode`、无跳过验签的暗开关）

| 项 | 内容 |
|---|---|
| 状态 | Accepted |
| 日期 | 2026-11-02 |
| 主笔 | 关津 架构 · Ark（TWKJSV）；契约侧由 Nova（HBZ8HT）落 `sdk-contract.md`、Spec（W87AB0）核一致性 |
| 上位依据 | `decisions.md` **D6**(1)~(5)、**CH-5**（待追认）；索引 §2「SDK 协作形态 = 只保留网关形态」「令牌必须验签」；索引 §4.2-23（JWKS 路径）、§4.2-24（角色零回源）；SDK 契约 §3.1/§3.2、移交 §九 |

## 1. 背景

三处口径互不一致，且其中一处是**安全暗门**：

1. **数量**：`GuanJin-SDK契约.md` §3.1 表里实有 **29** 行，但编号排到 **32**（缺 #7/#16/#17），全项目口径却写"32 项"。
2. **形态**：`guanjin.mode: gateway|direct` 出现在 §4.1 示例、却不在 §3.1 清单里，与索引 §2「只保留网关形态」直接冲突。
3. **路径**：`jwks-uri` 默认 `${base-url}/api/v1/jwks` 指向**不存在的端点**（真值是 `/.well-known/jwks.json`），首次启动即 404。
4. **暗开关**：`guanjin.enabled` 默认 `true`，文档写「生产置 `false` 应被视为误配并告警」——**告警不是拦截**，等于留了一个"关掉验签还能跑"的开关。

## 2. 决定

1. **交付集 = 29 项，连续编号 1..29**；"32 项"作废。缺号 #7/#16/#17 是**编号空洞（文档笔误）**，**不得新增 key 补齐编号**（禁编造契约）。来历：#7/#16/#17 正是 direct 形态的 3 个键（`guanjin.mode`、`guanjin.direct.user-token-jwks-uri`、`guanjin.direct.user-token-issuer`），索引 §2 定"只保留网关形态"后 3 项失去存在理由、被删除但编号未重排。**缺口已在 `decisions.md` D6【待澄清登记】逐个键名登记**（29 条键 / 32 个编号位，编号不重排、不补号）。
2. **删除 `guanjin.mode`**：SDK 无模式开关，只有一种形态（流量必过数据面）。
3. **`jwks-uri` 默认 = `${base-url}/.well-known/jwks.json`**（`base-url` 不带尾斜杠），与 `/.well-known/openid-configuration` 同基址。
4. **`guanjin.enabled` / `guanjin.authz.enabled` 受 profile 门控**：仅 `spring.profiles.active` 含 `local|test` 时可置 `false`；**其它 profile 置 `false` → 启动直接失败（fail-fast，不是告警）**。生产不存在跳过验签/跳过判定的开关。
5. **三处默认值 = 纠错性默认值**：`authz.replay-store` 默认 **`redis`**（原 `memory` 在多副本下静默降级为单机内存 → `jti` 防重放失效）；`agent.pull.enabled` 默认 **`false`**（索引 §4.2-24 已定角色进内部令牌、零回源）；`gateway.jwks-uri` 见第 3 条。

**三分判据（判断某改动是否属"新增契约面"）**：这条改动是否让业务方能用**基线原本不允许**的方式配置 SDK？能 → 进 CH 清单待追认；不能（只是让基线已禁止的用法**启动失败**）→ 属加固，不进。

## 3. 备选与不选的理由

- **备选 A（新增 3 个 key 补齐 32 项）**：否。会凭空造出 3 个基线从未定义的配置项（编造契约），且 readers 会以为有 direct 形态。
- **备选 B（保留 `mode: direct` 作兜底）**：否。索引 §2 明定"只保留网关形态、不提供业务系统直连"，保留即破坏"流量必过数据面"这条唯一形态。
- **备选 C（`enabled=false` 在生产只告警不拦截）**：否。移交代 §九与 SDK 契约 §3.2 本就禁止"生产可跳过验签"，告警式处理等于禁令不可执行。
- **备选 D（`replay-store` 默认 `memory` 图轻量）**：否。多副本下 `memory` 会让 `jti` 防重放静默失效，属"降级放行"，fail-closed 明确禁止。

## 4. 后果

- **正面**：配置面可枚举、可反射校验；不存在"关掉验签还能上线"的路径；JWKS 首启即命中。
- **负面 / 约束**：需要 `local|test` 才允许关验签 → 本地调试必须显式激活 profile（这是刻意代价）；`agent.pull.enabled` 默认关 → 依赖旧回源行为的接入方需显式打开。
- **需要谁做什么**：**Nova** 落配置类/Properties/示例 yml/README + `guanjin-core` 的 fail-fast 校验 + `sdk-contract.md` §3.1/§8 同步；**Gate** `AC-SDK-01~03/07/10`、`AC-FC-05` 按 29 项与三处默认值断言；**Spec** 在下版对外口径删掉"32 项"。

## 5. 验证方式

- `AC-SDK-07`：反射扫描 `GuanjinProperties` ↔ 契约表**逐项一致、数量 = 29、三处默认值如上**。
- `AC-SDK-01`：设 `direct` → 启动失败。`AC-SDK-02`：非 `local|test` 置 `enabled=false` → **启动失败**；`local|test` 下仍真实验签（篡改令牌必须 401）。
- `AC-SDK-03`：默认配置首启即命中真实 JWKS（404 即失败）。`AC-SDK-10`：默认 `agent.pull.enabled=false` → 启动后 30s+ 零次 `/api/v1/agent/policies`。
- `AC-FC-05`：配置键扫描与 29 项逐项对齐，**多一个键即失败**。

## 6. 变更历史

- 2026-11-02 初版（`D6`）。`jwks-uri` 默认值曾挂在 **CH-5**，后按"纠错性默认值"统一移出（避免同类不同标），**CH-5 只保留「29 项交付集 + 删 `mode`」两项**——那两项仍**待追认**，未追认前按基线原文实现并同步回改台账。
