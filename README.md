# 关津 GuanJin

> 名字取自《唐六典·刑部》：关为陆上关卡、津为渡口，于关津设吏查验行人凭证 —— **进出必经、只做查验**。
> 面向 C 端的**统一认证基础设施**（IAM 里"认证"那一半）：单租户、集团自建、greenfield。

## 这是什么

只做认证：账号 / 因子 / 编排 / 令牌 / 会话 / 审计 / 数据面。**不做**授权策略、组织人事、业务逻辑。
规模目标：100 个业务系统 / 千万级 C 端用户 / 峰值登录 200 QPS。合规目标：等保三级；密评整体后置。

## 文档地图（先读哪个）

| 位置 | 内容 | 能否改 |
|---|---|---|
| `docs/baseline/` | **设计基线（唯一权威）**：12 份设计文档 + 索引（`GuanJin-设计基线索引.md` 是入口，先看 §2「已生效的设计决定」） | ❌ **只读，一个字不许改** |
| `docs/decisions.md` | **文档矛盾裁定台账**：`D1`–`D11`（裁定）、`P1`–`P5`（已生效前提）、`CH-1`–`CH-6`（对外契约变更，**待追认**）、残余待裁 `R2`–`R5/R8/R9` | 仅架构（Ark） |
| `docs/m1-slices.md` | **M1 垂直切片拆解**：S0–S11 串行步骤、端点→包→表→判据映射、依赖链、DoD | 仅架构（Ark） |
| `docs/acceptance-criteria.md` | **验收判据**（85 条 / 10 组），状态：68 `PASS-able` / 10 `PENDING·CH-n` / 0 `BLOCKED` / 6 `NOT-APPLICABLE` / 0 `NOT-IMPLEMENTED` / 1「后置（DEFERRED）：本期不做判定」 | 仅测试（Gate） |
| `docs/sdk-contract.md` | **语言无关 SDK 契约**：22 条能力 + 29 项配置，Java 8 / Node / Go 三语言的等价定义 | 仅 SDK（Nova） |
| `docs/adr/` | 架构决策记录（`ADR-000` 规范 + 首批 4 篇） | 架构（Ark），涉及他人模块先确认 |

冲突时的权威顺序：**基线索引 §2 → 端点/数据契约 → `decisions.md` → `m1-slices.md`**。

## 五条必须知道的工程红线

1. **schema 唯一权威 = `migrations/`**（`golang-migrate`，纯 SQL、可 up/down）；**禁 `AutoMigrate`**——应用启动任何情况不得自动迁移。
2. **基线只读**：`docs/baseline/` 一个字不改；要改口径，写进 `decisions.md` 并标明依据。
3. **迁移不随应用启动**（P1）：项目未上线、不做自动迁移机制；本地手动 `migrate up`。
4. **Go module path = `github.com/spencerswagger/guanjin`**（P4），内部包前缀同此；`cmd/guanjind`（控制面 + 认证 + 管理面）+ `cmd/guanjingw`（数据面）。
5. **旧仓 SpIAM 冻结归档、只读参考**（P5）：不改 / 不 push / 不开分支；关津所有代码只进本仓（只借鉴设计，不继承代码）。

## 技术栈（基线冻结）

- 后端 **Go 1.25**：Gin + GORM + golang-migrate + pgx + `tjfoc/gmsm` + `golang-jwt/v5` + zap + `caarlos0/env`，`CGO_ENABLED=0`
- 前端 **Vue 3.5 + Vite + TypeScript + pinia + element-plus**，产物 `go:embed` 与后端同端口
- SDK 三语言 **Java 8 / Node / Go**（共用 `docs/sdk-contract.md` 一份语言无关契约，`GJ-CAP-01..22` + 29 项配置）
- 全链路国密固定最高级：口令 **PBKDF2-SM3**、摘要 **SM3**、审计 **HMAC-SM3**、敏感字段 **SM4**、网关断言 **SM2**、对外令牌 **SM3withSM2**；主密钥只从环境变量注入、经 **HKDF-SM3** 派生子密钥、**不落库**

## 本地跑

```bash
make migrate-up     # schema 唯一权威落到本地库（需先设 DATABASE_URL）
make test           # 单元测试
make lint           # gofmt + lint + 门禁脚本（禁 AutoMigrate / 禁软删除 / 禁废弃错误码）
```

本地依赖只有 **PostgreSQL 17（三库）+ Redis 7**；TLS / WAF / 负载均衡 / 灰度 / 熔断**不属本平台范围**，由前置 nginx / Kong 承担。

## 协作

`git fetch origin` → 从 `origin/main` 开分支（`feat/…` 或 `fix/…`）→ 本地门禁全绿 → commit（写清"为什么"）→ push → 建 PR（base `main`）→ 评审（Owl，按 P0/P1/P2 修到 READY）→ **合入由 spencer 点**。禁止直推 `main`、禁止自行合入。
