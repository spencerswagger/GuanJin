# 关津 SDK（Go 侧 · S9 最小接入）

> 权威契约：**`docs/sdk-contract.md`**（22 条能力 `GJ-CAP-01..22` + 29 项配置）。
> 本目录只放**实现**，契约口径一律以那份文档为准，本文不复制内容、不写死版本。

## 这是什么

`net/http` 中间件形态的接入 SDK：**验签内部断言 `X-GuanJin-Assertion`（SM3withSM2）→ 校验 claim → `jti` 防重放 → 注入身份 → 替换请求视图（剥离 header）→ handler**。
剥离严格按契约 §4.5（= `decisions.md` **D10** 的**唯一六步时序**），**禁止"验签前剥离"**。

## 快速接入

```go
cfg, err := guanjin.LoadFromEnv() // 29 项配置；GUANJIN_* 严格白名单，未知键即失败
if err != nil { log.Fatal(err) }  // 配置错误必须当启动失败处理

sdk, err := guanjin.New(cfg)
if err != nil { log.Fatal(err) }

mux := http.NewServeMux()
mux.Handle("/orders", http.HandlerFunc(orders))
// 中间件**必须包在 router 外层**：否则未匹配路由与 router 自身日志会绕过视图替换
http.ListenAndServe(":8080", sdk.Middleware(mux))
```

handler 内取身份：

```go
p, ok := guanjin.PrincipalFrom(r.Context())
if !ok { /* 该路由未受保护（公开路径）——不是"已登录但没角色" */ }
```

## 能力状态（对应契约 §2.1，**不许静默缺**）

`guanjin.Capabilities()` 是唯一真值；本构建 = S9 最小集。

| 能力 | 名称 | 状态 | 说明 |
|---|---|:--:|---|
| `GJ-CAP-01` | `VerifyAssertion` | ✅ 已实现 | SM2 验签；`alg` 必须是 `SM3withSM2`（不做协商降级） |
| `GJ-CAP-02` | `SelectKeyByKid` | ✅ 已实现 | 按 JWT header 的 `kid` 选公钥 |
| `GJ-CAP-03` | `JwksCache` | ✅ 已实现 | 启动/首次使用拉取并缓存，TTL 见配置 #13 |
| `GJ-CAP-04` | `RefreshOnUnknownKid` | ✅ 已实现 | 未知 `kid` 强刷一次（配置 #14） |
| `GJ-CAP-05` | `ValidateExpIat` | ✅ 已实现 | `exp`/`iat` + 时钟偏移（配置 #10） |
| `GJ-CAP-06` | `ValidateIssAud` | ✅ 已实现 | `iss` = 配置 #8，`aud` = 配置 #9（默认跟随 `client.id`） |
| `GJ-CAP-07` | `ReplayGuard` | ✅ 已实现 | `jti` 窗口内唯一；`redis`（默认）/`memory`（仅 local\|test） |
| `GJ-CAP-08` | `StripAssertionHeader` | ✅ 已实现 | **替换请求视图**：handler / 下游 / 日志都取不到；原请求上的头也删 |
| `GJ-CAP-09` | `ReadIdentity` | ✅ 已实现 | `sub/uname/roles/acr/amr/cid` → `Principal` |
| `GJ-CAP-10` | `InjectHostContext` | ✅ 已实现 | Go 形态 `WithPrincipal(ctx, p)` / `PrincipalFrom(ctx)`（规则 R-3） |
| `GJ-CAP-11` | `AuthoritiesResolver` | ⛔ 未实现（可选） | 扩展点：S10 前的接入打磨补 |
| `GJ-CAP-12` | `AuthzEngine` | ⛔ 未实现 | 本地判定属 **M5**；调用返回 `ErrNotImplemented` |
| `GJ-CAP-13` | `AuthzModelBuilder` | ⛔ 未实现 | 同上；`withClearance(level)` 唯一写法在 M5 落地 |
| `GJ-CAP-14` | `RequirePermission` | ⛔ 未实现 | 同上（M5） |
| `GJ-CAP-15` | `CheckRow` / `CheckOwner` | ⛔ 未实现 | 同上（M5） |
| `GJ-CAP-16` | `CheckLabel` | ⛔ 未实现 | 同上（M5）；形参固定 `objLevel`（契约 §2.1.2） |
| `GJ-CAP-17` | `Scope` | ⛔ 未实现 | 同上（M5） |
| `GJ-CAP-18` | `DefaultRole` | ⛔ 未实现 | 同上（M5） |
| `GJ-CAP-19` | `PublicPathDeclare` | ✅ 已实现 | deny-by-default：内置白名单 + `public-paths` 显式声明 |
| `GJ-CAP-20` | `PolicyReport` | ⛔ 未实现 | Agent 面上报依赖判定模型，随 M5；S9 不回源 |
| `GJ-CAP-21` | `PolicyPull` | ⛔ 未实现（可选） | 默认 `agent.pull.enabled=false`（方案 A 零回源） |
| `GJ-CAP-22` | `StartupSelfCheck` | ✅ 已实现 | 启动打印放行清单 + 配置 fail-fast |

> 未实现的 11 条**全部**在 `Capabilities()` 里带 `Reason`（单测强制：未实现却不写理由即失败）。

## 配置（29 项，严格白名单）

- 键名、默认值、安全约束**全部照抄契约 §3.2**；`LoadFromEnv()` 只认 `GUANJIN_*` 白名单，
  **未知键 / 已废止键（`guanjin.mode`、`direct.*`）即启动失败**（`AC-FC-05` / `AC-SDK-01`）。
- 三个易错点（代码里都做了硬校验）：
  1. `platform.base-url` **必填**、无内置默认、不带尾斜杠（`AC-SDK-04`）；
  2. `enabled` / `authz.enabled` = `false` **仅 `local|test`**，其它环境启动失败（`C-1` / `AC-SDK-02`）；
     `authz.replay-store=memory` 同规则（`C-4`）；
  3. `jwks-uri` 只允许 `/.well-known/jwks.json`（`C-2`；旧值 `/api/v1/jwks` 全库不存在）。
- **运行时环境标识**：`GUANJIN_ENV=local|test|…`（不是 29 项键之一，等价 Java 的 `spring.profiles.active`）；
  缺省按 `prod` 处理（宁可判严）。

## fail-closed 行为（契约 §4.4）

| 场景 | 行为 | HTTP / 码 |
|---|---|---|
| 缺头 / 验签失败 / `exp`·`iat` 越界 / `iss`·`aud` 不符 / `jti` 重放 | 拒绝，不注入身份 | `401 TOKEN_INVALID` |
| JWKS 拉取失败**有缓存** | 沿用缓存继续真实验签 | 正常（非降级） |
| JWKS 拉取失败**无缓存** | 拒绝 | `503 KEY_UNAVAILABLE` |
| `jti` 存储不可用 | 拒绝 | `503 REDIS_UNAVAILABLE` |

**`TOKEN_EXPIRED` 永不返回**（那是平台对用户令牌的响应，SDK 不验用户令牌）。

## 模块形态（Q10 未定）

当前放在主 module 的子包 `sdk/go/guanjin`（契约 §5.3.2 **案 B**），import 路径
`github.com/spencerswagger/guanjin/sdk/go/guanjin`。若后续定为**案 A（独立 module）**，
只需在 `sdk/go` 下加 `go.mod` 并把包路径换成 `…/guanjin/sdk/go/guanjin`，**代码与 API 名不变**。
> 本包**不 import `internal/*`**：SDK 不得依赖平台内部实现（契约 §5.3.2 的越界防护）。

## 测试与覆盖率

```bash
go test ./sdk/... -count=1 -cover    # 单测全部用 mock（httptest + 内存 jti），不依赖 S8 网关
```

端到端（真实 `guanjingw` 转发 + 断言头）等 **S8** 落地后另补一条，不在本切片范围内。
