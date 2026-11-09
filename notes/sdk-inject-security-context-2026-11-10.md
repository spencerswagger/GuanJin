# `security.inject-security-context` 的启动失败口径（2026-11-10）

> **性质**：基线**释义 + 执行形态**记录，不是契约变更、不是判据变更。裁定人 = **Spec**（基线释义负责人）；入库转录 = Ark（2026-11-10）。路径固定为 `notes/sdk-inject-security-context-2026-11-10.md`，`docs/sdk-contract.md` §3.2 #26 引用本文件，改名/移动须同步。
>
> **一句话结论**：`guanjin.security.inject-security-context` 默认 **`true`**；`true` 而类路径**没有** `spring-security-core` → **启动失败**（不静默 NOOP）。唯一合法出路是**显式**置 `false`——该 `false` **不受 profile 门控、生产也允许**；此时 `GuanjinContextHolder` / `req.guanjin` / `context` 仍可用，验签与判定不受影响。

## 1. **基线对该情形无规定（必须保留的诚实标注）**

> 基线**既没写**"缺依赖必须启动失败"，**也没写**"可以静默 NOOP"。本裁定是**按 #26 已有语义把两种硬件状态做成显式二选一**，**不是**"补一条既有规定"，也不是新增接入前置条件。

因此实现方与评审都应把它读作：**「配置说要保护、环境却不具备」这类状态必须显式化**，同 `authz.enabled=true` 无 `AuthzModel` bean 的处理（见 `notes/sdk-authz-enabled-默认-2026-11-10.md`）。

## 2. 它执行的是哪两处原文

| # | 出处 | 原文要点 |
|---|---|---|
| 1 | `docs/sdk-contract.md:272`（§3.2 #26 安全约束列） | 「关掉只影响宿主取身份的方式（`GuanjinContextHolder`/`req.guanjin`/`context` 仍可用），**不跳过验签、不影响判定**」 → 说明 `false` 是**明文允许的合法形态**，不是降级 |
| 2 | `docs/sdk-contract.md:423`（§5.1 坐标行，原文：「`guanjin-spring-boot2`（`javax` 适配：Filter、`@ConfigurationProperties`、**`SecurityContext` 注入**、自动配置）」） | 把 `SecurityContext` 注入列为 boot2 的**职责** → 说明 `true` 时**必须真做**，做不到就不能装作做了 |

两者合起来：`true` 却做不到 = 配置说要做、实际没做 → **启动失败**；`false` = 明示不做注入，但身份仍可通过 holder 取到。

## 3. 门控边界（最容易写错的一点）

- `false` **不受 profile 门控、生产允许**：`AC-SDK-02` 的 `local|test` 门控只管 **#1 `guanjin.enabled`** 与 **#15 `authz.enabled`** 两个键。
- 若实现方顺手把 #26 的 `false` 也塞进「非 `local|test` 禁止」，等于**把合法出路堵死**——那是把加固做成新禁令。
- 反过来，`false` 也不允许被当成"静默降级"的托词：它必须是**显式配置**，且 `GuanjinContextHolder` 取身份可用（要有用例）。

## 4. 为什么是「澄清」而不是「改契约」

| 判据（`decisions.md:379` 三分法） | 本条 |
|---|---|
| 新增 key / 改 key 名 | 否（仍 29 项，编号 1..29 连续） |
| 改默认值 | 否（仍 `true`） |
| 改必填性 | 否（仍「否」） |
| 让业务方能用基线**原本不允许**的方式配置 | 否——`false` 本就是 #26 明文允许的形态；本条只是把「`true` 却缺依赖」定成可执行失败 |

→ **不进 CH、不动判据文件、不动 90 项账目**。

## 5. 硬边界

- **不加任何新开关**（如 `security.inject-security-context.fallback`）——出路只有「声明依赖」或「显式 `false`」两条。
- 不动判据文件、不动代码（本文件只记录口径）。
- 报错文本必须**点名两条修法**：①引入 `spring-security-core`；②显式置 `inject-security-context=false`（并说明其影响面仅"取身份方式"）。

## 6. 三语言等价

`sdk-contract.md:20/22`：三语言只允许打包物与宿主接入点两处差异，其余（含 fail-closed 形态）逐字节一致。Java 宿主的 `spring-security-core` 是 Java 侧特有依赖，但**"缺能力就启动失败、不许静默降级"这条形态三语言同**——Node/Go 若实现该能力，缺宿主运行时依赖时同样必须装配期失败。

## 7. 与 AC 账目的关系（只指路，不复写）

`GJ-CAP-17`/`GJ-CAP-20/21` 属**能力编号**，不占 AC 条目；能力矩阵与 AC 账目的记法（含"不得双计""22 能力 vs 90 判据不得换算"）**唯一出处 = `docs/decisions.md` D17**。本文不复写，避免同一口径出现两份表述。
