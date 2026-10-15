# 关津 · 因子分类与能力模型

| 项目 | 内容 |
|---|---|
| 文档类型 | 设计规格（因子 SPI 契约） |
| 编写日期 | 2026-10-11 |
| 目的 | 定义"因子如何分类、如何扩展"，使**新增一种登录方式不需要改动核心代码** |
| 结论 | 用**能力接口组合 + 能力声明**，不用继承大类 |

---

## 0. 结论先行（三条设计决定）

| # | 决定 | 理由 |
|---|---|---|
| 1 | 因子的分类沿着**两个正交维度**：`承载类型（kind）` × `能力（capabilities）` | 单靠"类型"分类不足以表达行为差异 |
| 2 | 用**能力接口组合（interface composition）**，**不用继承大类** | 能力组合是交叉的，继承会迅速爆掉（见 §1） |
| 3 | 用**共享基类**解决"代码复用"，与能力接口**分开** | 短信与邮箱 90% 的实现相同，但那属于"实现复用"，不属于"分类" |

> 你提的"一个类实现多套接口"方向是对的，但落地形态是**多个能力小接口**，而不是"VerifiableProvider 这样的大类继承"——原因见下一节。

---

## 1. 为什么不能用继承大类

假设按你说的建几个大类：`VerifiableProvider`、`RecoverableProvider`……

问题在于**能力组合是交叉的**，几个例子就能把继承树打结：

| 因子 | 可认证 | 可验证标识符 | 可参与找回 | 可用于注册 | 冲突点 |
|---|---|---|---|---|---|
| 密码 | ✅ | ❌ | ❌ | ✅ | 能认证，但**不能用来找回自己** |
| 短信验证码 | ✅ | ✅ | ✅ | ✅ | 五毒俱全，能力最全 |
| 动态口令 TOTP | ✅ | ❌ | ✅ | ❌ | 可认证也可找回，但**不适合注册**（要先有绑定过程） |
| 恢复码 | ❌ | ❌ | ✅ | ❌ | **只能找回，不能日常登录** |

- 密码和短信都能"认证"，但找回能力完全相反；
- 短信和邮箱能力几乎完全一致，但**投递方式完全不同**；
- 恢复码能找回却**不能登录**——如果它继承 `Authenticatable`，就得在实现里抛异常，这正是抽象失败的信号。

如果硬做继承，最后一定会出现 `class SmsProvider extends VerifiableAndRecoverableAndRegistrableProvider` 这种怪物，或者到处是 `if type == "sms"` 的分支。

**正确做法：把"能力"拆成小接口，因子按需实现若干个；平台通过"能力断言"使用，而不是通过类型判断。**

---

## 2. 维度一：承载类型（kind）

| kind | 定义 | 本质 | 示例 |
|---|---|---|---|
| **holding**（持有型） | 用户持有一份长期密钥材料 | 你**有**什么 | 密码、TOTP 密钥、WebAuthn 凭证、恢复码 |
| **channel**（信道型） | 通过已绑定的通信通道投递一次性码 | 你**能收到**什么 | 短信验证码、邮箱验证码 |
| **proofing**（核验型） | 核验用户是否对应真实自然人 | 你**是谁**（实名） | 人脸实人核验 —— ⚠️ **不属本平台**，归"身份核验"独立模块 |

`kind` 决定三件事：

1. **是否需要前置绑定标识符**：channel 型必须先有一个已验证的手机号/邮箱，holding 型不需要；
2. **"换绑"的语义**：channel 型的换绑 = 换绑标识符（换手机号），而不是操作因子本身；
3. **是否消耗外部资源**：channel 型会花钱（短信费），因此限流要求最严。

---

## 3. 维度二：能力接口清单（10 个）

平台**从不按类型写分支**，只按能力断言。

| # | 能力接口 | 语义 | 需要声明的参数 |
|---|---|---|---|
| 1 | `Enrollable` | 能建立/解除绑定关系 | 是否需要前置标识符；能否自助绑定；绑定是否必须验证 |
| 2 | `IdentifierVerifying` | 能验证"某个标识符确实属于该用户" | 验证哪种标识符类型（phone / email） |
| 3 | `Authenticatable` | 能作为认证因子 | 可作第一因子 / 第二因子 / 两者 |
| 4 | `ACRContributing` | 能贡献保证级别 | 单独使用可达的级别、组合后可达的级别 |
| 5 | `Recoverable` | 能作为"重新证明身份"的凭证参与找回 | 信任等级 |
| 6 | `Resettable` | 自己的凭据**能被重置** | 重置所需的最小信任证明 |
| 7 | `Registrable` | 可用于注册（作为注册时的首个凭据） | 是否强制验证 |
| 8 | `Revocable` | 解绑/强制失效的联动语义 | 标识符解绑时的失效时机；解绑前置 ACR 与冷却期 |
| 9 | `RateLimited` | 涉及外部资源消耗或可被爆破 | 发送/校验频率、日配额、预算阈值 |
| 10 | `RiskReporting` | 能产生风险信号 | 信号类型（发送频率/地理跳变/设备变更…） |

**一条守护规则**：若代码中出现 `if provider.Type() == "sms"` 之类的分支，就说明抽象错了——应该改为能力断言。

---

## 4. 因子全清单与能力矩阵

### 4.1 V1 四类因子 + 两个配套

| 因子 | type | kind | 1 Enrollable | 2 IdentifierVerifying | 3 Authenticatable | 4 ACR | 5 Recoverable | 6 Resettable | 7 Registrable | 8 Revocable | 9 RateLimited | 10 RiskReporting |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| **密码** | `password` | holding | ✅ | — | ✅ 第一 | 一级 | ❌ | ✅ | ✅ | ✅ | ✅ 防爆破 | ✅ |
| **短信验证码** | `sms_otp` | channel | ✅（随 phone） | ✅ phone | ✅ 第一+第二 | 一级 | ✅ 信任 2 | — | ✅ 强制验证 | ✅ | ✅ **强** | ✅ |
| **邮箱验证码** | `email_otp` | channel | ✅（随 email） | ✅ email | ✅ 第一+第二 | 一级 | ✅ 信任 2 | — | ✅ 强制验证 | ✅ | ✅ 中 | ✅ |
| **动态口令** | `totp` | holding | ✅ | — | ✅ 第二（为主） | 二级（配密码） | ✅ 信任 2 | ✅ | ❌（需先绑定） | ✅ | ✅ 防爆破 | ✅ |
| **恢复码** | `recovery_code` | holding | ✅（随 TOTP 签发） | — | ❌ | — | ✅ 信任 3 | ✅（用完重发） | ❌ | ✅ | — | — |

### 4.2 预留因子（只验证架构弹性，不在 V1 范围）

| 因子 | type | kind | 说明 |
|---|---|---|---|
| WebAuthn / 硬件密钥 | `webauthn` | holding | 形态全新（需要浏览器 API），用来验证架构对"全新因子形态"的适应能力 |
| 人脸实人核验 | `face_proofing` | **proofing** | ⚠️ **不是认证因子**。它产出的是"标识符可信度提升 / 实名状态"，归身份核验独立模块；未来若要用"人脸比对登录"，它才作为一个 holding 型因子接入 |

### 4.3 几个值得注意的格子

| 观察 | 说明 |
|---|---|
| **密码 `Recoverable = ❌`** | 密码不能用来"找回自己"，这是最容易设计错的地方。找回密码必须依赖**其他**因子 |
| **恢复码 `Authenticatable = ❌`** | 只能找回，不能日常登录——所以它不能继承 `Authenticatable`，这正是"不能用继承大类"的直接证据 |
| **TOTP `Registrable = ❌`** | TOTP 可以先注册再绑定，但流程别扭（要先拿到二维码），所以不放进注册流程 |
| **channel 型 `Resettable = —`** | 短信/邮箱本身没有"凭据"可重置，重置的是**标识符**（换手机号） |

---

## 5. 共享实现：解决代码复用的地方不在分类，而在基类

分类解决"能不能用"，基类解决"怎么少写代码"。两件事分开。

### 5.1 信道型基类（短信与邮箱共用）

短信与邮箱的实现有 90% 相同，差别只有"投递到哪"：

```
ChannelOtpProvider（基类，实现全部通用逻辑）
 ├─ 生成一次性码、存挑战（TTL / 尝试次数 / 一次性消费）
 ├─ 执行速率限制与日配额
 ├─ 校验码值（含防时序攻击的恒定时间比较）
 └─ 调用一个抽象方法  sender.Send(target, code)   ← 子类唯一要实现的东西

SmsOtpProvider   →  SmsSender（接短信服务商，含签名与模板）
EmailOtpProvider →  EmailSender（接 SMTP，含发件人与模板）
```

**新增一个信道型因子（例如"企业微信推送码"）只需实现一个 `Send` 方法。**

### 5.2 持有型基类

```
HoldingProvider（基类）
 ├─ 凭据的加密存储与比对
 ├─ 失败计数与锁定
 └─ 更新、重置、撤销

PasswordProvider → 派生自基类 + 密码策略校验（长度/复杂度/弱口令/历史）
TotpProvider      → 派生自基类 + TOTP 算法与时间窗
```

### 5.3 接口草图（Go）

```go
// 所有因子都要实现的骨架
type Provider interface {
    Type() FactorType
    Descriptor() Descriptor        // 能力声明：平台据此决定它能出现在哪些场景
}

// —— 能力接口：按需实现，平台按能力断言使用 ——

type Authenticatable interface {
    BeginChallenge(ctx Context, userID string, purpose Purpose) (Ticket, error)
    VerifyResponse(ctx Context, ticket Ticket, response string) error
}

type IdentifierVerifying interface {
    VerifiesIdentifierType() IdentifierType
    VerifyOwnership(ctx Context, userID, identifierID, code string) error
}

type Recoverable interface {
    RecoveryTrustLevel() int
    BeginRecovery(ctx Context, userID string, target RecoverTarget) (Ticket, error)
}

type Resettable interface {
    RequiredProofForReset() ProofRequirement
    Reset(ctx Context, userID, authenticatorID string, proof Proof) error
}

type RateLimited interface {
    Limits() RateLimits            // 发送频率 / 校验频率 / 日配额 / 预算
}
```

> 注意：`PasswordProvider` 只实现 `Authenticatable`、`Resettable`、`Registrable`，**不实现** `Recoverable` —— 这在类型层面就杜绝了"用密码找回密码"这种错误设计。

---

## 6. 新增一个因子的完整清单（这也是架构验收标准）

| 步骤 | 内容 | 是否改核心代码 |
|---|---|---|
| 1 | 选择承载类型（holding / channel），继承对应基类 | ❌ |
| 2 | 实现所需的能力接口（通常 2~5 个） | ❌ |
| 3 | 填写能力声明（Descriptor） | ❌ |
| 4 | 若是信道型，实现一个 `Send` 方法 | ❌ |
| 5 | 若 UI 形态已有（密码框 / 验证码框 / 二维码），**零改动**；若全新，加一个前端 UI 原子组件 | ⚠️ 仅前端 |
| 6 | 在因子管理界面启用并配置参数 | ❌ |

**验收判据：新增因子时，编排层 / 凭据层 / 标识符层代码零改动，以 diff 举证。**

能力声明长这样（示例：短信）：

```yaml
type: sms_otp
kind: channel
capabilities: [enrollable, identifier_verifying, authenticatable, acr_contributing,
               recoverable, registrable, revocable, rate_limited, risk_reporting]
authenticatable: { as_first: true, as_second: true }
acr: { sole: AAL1, combined: AAL2 }
identifier_verifying: { targets: [phone] }
recoverable: { trust_level: 2 }
registrable: { verify_required: true }
revocable: { on_identifier_unbind: immediate, require_acr: AAL2, cooldown_hours: 24 }
rate_limit: { send: "1/60s", verify: "5/10min", daily_send: 10, budget_alert: true }
risk_signals: [send_frequency, geo_jump, device_change]
```

---

## 7. 守护规则（写进代码评审）

| # | 规则 |
|---|---|
| 1 | 平台按**能力断言**使用因子，禁止 `if type == "sms"` 之类的类型分支 |
| 2 | 能力接口只表达**能力**，不得退化为类型标签 |
| 3 | 共享逻辑放基类，能力判定放接口，**两者不混** |
| 4 | 新增因子的 PR 若改动了编排层 / 凭据层 / 标识符层，必须说明为何能力声明无法表达 |
| 5 | 若某个因子被迫实现一个"用不到的能力"并抛异常，说明抽象错了，回到 §1 重新检查 |
