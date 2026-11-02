# 关津 GuanJin

> 名字取自《唐六典·刑部》：关为陆上关卡、津为渡口，于关津设吏查验行人凭证 —— **进出必经、只做查验**。
> 面向 C 端的**统一认证基础设施**（IAM 里"认证"那一半）：单租户、集团自建、greenfield。

## 这是什么

只做认证：账号 / 因子 / 编排 / 令牌 / 会话 / 审计 / 数据面。**不做**授权策略、组织人事、业务逻辑。
规模目标：100 个业务系统 / 千万级 C 端用户 / 峰值登录 200 QPS。合规目标：**等保三级**；密评整体后置（**等级待澄清 / 待追认**；本期不做判定、不出证、不送检，按预检清单登记，不进契约强制项）。

## 文档地图（先读哪个）

| 位置 | 内容 | 能否改 |
|---|---|---|
| `docs/baseline/` | **设计基线（唯一权威）**：12 份设计文档 + 索引 + 2 份 HTML 清单（**共 14 个文件**；入口是 `GuanJin-设计基线索引.md`，先看 §2「已生效的设计决定」） | ❌ **只读，一个字不许改** |
| `docs/decisions.md` | **文档矛盾裁定台账**：`D1`–`D11`（裁定）、`P1`–`P7`（已生效前提）、`CH-1`–`CH-6`（对外契约变更，**待追认**）、残余待裁 `R2`–`R5/R8/R9` | 仅架构（Ark） |
| `docs/m1-slices.md` | **M1 垂直切片拆解**：S0–S11 串行步骤、端点→包→表→判据映射、依赖链、DoD | 仅架构（Ark） |
| `docs/acceptance-criteria.md` | **验收判据**（10 组）；**账目一律以该文件当前版本为准** —— 本文**不写版本号、不写任何计数**（硬写必然与源文件漂开） | 仅测试（Gate） |
| `docs/sdk-contract.md` | **语言无关 SDK 契约**：22 条能力 + 29 项配置，Java 8 / Node / Go 三语言的等价定义（**唯一 owner = Nova，落 PR #1**；其他人只引用不复制） | 仅 SDK（Nova） |
| `docs/adr/` | 架构决策记录（`ADR-000` 规范 + 首批 4 篇） | 架构（Ark），涉及他人模块先确认 |

冲突时的权威顺序：**基线索引 §2 → 端点/数据契约 → `decisions.md` → `m1-slices.md`**。

## 当前进度（S1 骨架）

| 块 | 状态 |
|---|---|
| 目录树 / 包边界（`cmd` + `internal` 16 包 + `web`） | ✅ 已落 |
| `migrations/` 14 张基线表 + `flows`（D5 第 15 张） | ✅ 16 个迁移，本地 PG 17 上 `up` 全绿、`down` 回滚到 0 |
| 三闸：`go vet` / `go test` / `vue-tsc` | ✅ 全绿（见 `make ci`） |
| 门禁脚本（禁 `AutoMigrate` / 禁废弃错误码 / 禁旧密级写法） | ✅ 脚本 + **变异注入实测必红** |
| 业务实现（协议面 / 编排 / 因子 / 数据面 / SDK） | ⏳ 按 `docs/m1-slices.md` S2–S11 逐个切片落地 |

## 目录结构（只列骨架，实现随切片填充）

```text
cmd/
  guanjind/                 # 控制面 + 认证服务 + 管理面 + Agent 面
  guanjingw/                # 数据面（薄：只做 7 件事，不做 TLS/WAF/LB）
internal/
  protocol/ flow/ factor/   # 协议面 / 编排状态机 / 因子 SPI
  identifier/ credential/   # 标识符三段式 / 凭据
  session/ authz/ admin/    # 会话与撤销 / 绑定与门槛方向 / 管理面三员
  gateway/ agent/ risk/     # 数据面 / Agent 面 HMAC / 风控计数
  audit/ crypto/            # 审计哈希链 / 国密封装与派生
  store/ store/model/       # 仓储（GORM 只做映射）/ 模型
  config/ httpx/            # 启动配置 fail-fast / 统一错误响应
migrations/                 # schema 唯一权威（golang-migrate，纯 SQL，手动执行）
web/                        # Vue 3.5 + Vite + TS（产物 go:embed 同端口）
scripts/                    # 门禁脚本
docs/                       # 设计基线（只读）+ 裁定台账 + M1 切片 + ADR
```

## 七条必须知道的工程红线

1. **schema 唯一权威 = `migrations/`**（`golang-migrate`，纯 SQL、可 up/down）；**禁 `AutoMigrate`**、**禁 GORM 软删除**——应用启动任何情况不得自动迁移（P1）。
2. **基线只读**：`docs/baseline/` 一个字不改；要改口径，写进 `decisions.md` 并标明依据。
3. **迁移不随应用启动**（P1）：项目未上线、不做自动迁移机制；本地手动 `migrate up`。
4. **Go module path = `github.com/spencerswagger/guanjin`**（P4），内部包前缀同此；`cmd/guanjind`（控制面 + 认证 + 管理面）+ `cmd/guanjingw`（数据面）。
5. **旧仓 SpIAM 冻结归档、只读参考**（P5）：不改 / 不 push / 不开分支；关津所有代码只进本仓（只借鉴设计，不继承代码）。
6. **分支必须短命**（P6）：**一律从 `origin/main` 开 `feat/…` / `fix/…`**，做完即 PR、合入即删；**禁开 `feat/phase-N-*` 这类长寿命分支**（会分叉出互不为祖先的两条主干，合并必冲突并静默回退新版本）。**一个文件只允许一个 owner 分支落地**，别人的文件只引用不复制。
7. **`docs/` 必须入库**（P7）：`.gitignore` 不得排除 `docs/`；**禁止用 `git add -f` 单文件绕过**——没有版本记录就 `git diff` 不出回写，基线红线等于失效。

## 技术栈（基线冻结）

- 后端 **Go 1.25**：Gin + GORM + golang-migrate + pgx + `tjfoc/gmsm` + `golang-jwt/v5` + zap + `caarlos0/env`，`CGO_ENABLED=0`
- 前端 **Vue 3.5 + Vite + TypeScript + pinia + element-plus**，产物 `go:embed` 与后端同端口
- SDK 三语言 **Java 8 / Node / Go**（共用 `docs/sdk-contract.md` 一份语言无关契约：22 条能力 + 29 项配置）
- 全链路国密固定最高级：口令 **PBKDF2-SM3**、摘要 **SM3**、审计 **HMAC-SM3**、敏感字段 **SM4**、网关断言 **SM2**、对外令牌 **SM3withSM2**；主密钥只从环境变量注入、经 **HKDF-SM3** 派生子密钥、**不落库**
- 迁移**不随应用启动**；TLS / WAF / 负载均衡 / 灰度 / 熔断**不属本平台范围**（前置 nginx / Kong）

## 本地跑

```bash
# 1) 依赖：PostgreSQL 17（三库）+ Redis 7
#    DSN 只能来自环境变量（GUANJIN_DEV_DSN / GUANJIN_TEST_DSN / GUANJIN_STAGING_DSN），
#    Makefile **故意不内置默认口令** —— 猜一个默认值只会让新人第一次就跑出
#    「password authentication failed」。没注入时 make 会直接报错并给出可照抄的示例。
# 2) 迁移（手动，**不随应用启动**；缺 DATABASE_URL 会当场失败，不会静默连错库）
make migrate-up     DATABASE_URL="$GUANJIN_DEV_DSN"
make migrate-down   DATABASE_URL="$GUANJIN_DEV_DSN"
# 3) 三闸（**只读判定**：gofmt 只列不改，红就是红）
make ci             # = fmt-check + vet + gates + test(-count=1 -v) + coverage + vue-tsc
# 4) 竞态检测单跑（-race 需要 cgo，与主闸的 CGO_ENABLED=0 口径分开）
make test-race
# 5) 证据留档（不入库）：每跑一次 test 会产出可核对的测试计数与覆盖率
#    artifacts/local/gate/{go-test.log,summary.txt,cover.out}
#    summary.txt 形如：tests_run=19 tests_pass=19 tests_skip=0 tests_fail=0 packages_ok=5 cached=no
```

前端依赖首次需装一次（`node_modules` 不入库）：`make web-install`。
> 没装依赖直接跑 `make ci` 时，`vue-tsc` 会报 `Cannot find type definition file for 'vite/client'` —— 那是**环境依赖缺失**，不是代码问题，先 `make web-install` 再重跑。

启动（需要 `GUANJIN_MASTER_KEY` = base64、≥32 字节；缺失即非 0 退出，fail-closed）：

```bash
go run ./cmd/guanjind    # /livez /readyz
go run ./cmd/guanjingw   # 数据面；GUANJIN_UPSTREAM 指向业务系统时透传
```

## 闸的两条纪律

- **判定只读**：检查目标只用 `gofmt -l`（列出即失败）；`make fmt-write` 只给人手动跑，**不得**出现在 `lint` / `ci` / CI 里——否则就是"一键把红改绿"。
- **race 独立**：`-race` 需要 cgo，与项目 `CGO_ENABLED=0` 口径冲突，故单列 `make test-race` 与独立 CI job，**不塞进主闸**。

## 协作

`git fetch origin` → **从 `origin/main` 开短特性分支**（`feat/…` 或 `fix/…`，见 P6）→ 本地门禁全绿 → commit（写清"为什么"）→ push → 建 PR（base `main`）→ 评审（Owl，按 P0/P1/P2 修到 READY）→ **合入由 spencer 点**。禁止直推 `main`、禁止自行合入、禁止长寿命分支。
