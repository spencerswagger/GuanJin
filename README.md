# 关津 GuanJin

> 名字取自《唐六典·刑部》：关为陆上关卡、津为渡口，于关津设吏查验行人凭证 —— **进出必经、只做查验**。
> 面向 C 端的**统一认证基础设施**（IAM 里"认证"那一半）：单租户、集团自建、greenfield。

## 这是什么

只做认证：账号 / 因子 / 编排 / 令牌 / 会话 / 审计 / 数据面。**不做**授权策略、组织人事、业务逻辑。
规模目标：100 个业务系统 / 千万级 C 端用户 / 峰值登录 200 QPS。合规目标：**等保三级**；**密评口径以预检清单为准，等级待追认**（本期不判定、不出证，也不进契约强制项）。

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
docs/                       # 设计基线（只读）+ 裁定台账 + M1 切片 + ADR（随 #2 合入）
```

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

启动（需要 `GUANJIN_MASTER_KEY` = base64、≥32 字节；缺失即非 0 退出，fail-closed）：

```bash
go run ./cmd/guanjind    # /livez /readyz
go run ./cmd/guanjingw   # 数据面；GUANJIN_UPSTREAM 指向业务系统时透传
```

## 六条必须知道的工程红线

1. **schema 唯一权威 = `migrations/`**（纯 SQL、可 up/down）；**禁 `AutoMigrate`**、**禁 GORM 软删除**——应用启动任何情况不得自动迁移（P1）。
2. **基线只读**：`docs/baseline/` 一个字不改；口径变更写进 `docs/decisions.md` 并标依据。
3. **Go module path = `github.com/spencerswagger/guanjin`**（P4），内部包前缀同此。
4. **旧仓 SpIAM 冻结归档、只读参考**（P5）：不改 / 不 push / 不开分支。
5. **分支必须短命**（P6）：一律从 `origin/main` 开 `feat/…` / `fix/…`，做完即 PR、合入即删；**禁 `feat/phase-N-*` 长寿命分支**；一个文件只允许一个 owner 分支落地。
6. **`docs/` 必须入库**（P7）：`.gitignore` 不得排除 `docs/`；**禁用 `git add -f` 绕过**（没有版本记录 = 回写无法校验）。

## 技术栈（基线冻结）

- 后端 **Go 1.25**：Gin + GORM + golang-migrate + pgx + `tjfoc/gmsm` + `golang-jwt/v5` + zap + `caarlos0/env`，`CGO_ENABLED=0`
- 前端 **Vue 3.5 + Vite + TypeScript + pinia + element-plus**，产物 `go:embed` 与后端同端口
- SDK 三语言 **Java 8 / Node / Go**（共用 `docs/sdk-contract.md` 一份语言无关契约：22 条能力 + 29 项配置）
- 全链路国密固定最高级：口令 **PBKDF2-SM3**、摘要 **SM3**、审计 **HMAC-SM3**、敏感字段 **SM4**、网关断言 **SM2**、对外令牌 **SM3withSM2**；主密钥只从环境变量注入、经 **HKDF-SM3** 派生子密钥、**不落库**
- 迁移**不随应用启动**；TLS / WAF / 负载均衡 / 灰度 / 熔断**不属本平台范围**（前置 nginx / Kong）

## 闸的两条纪律

- **判定只读**：检查目标只用 `gofmt -l`（列出即失败）；`make fmt-write` 只给人手动跑，**不得**出现在 `lint` / `ci` / CI 里——否则就是"一键把红改绿"。
- **race 独立**：`-race` 需要 cgo，与项目 `CGO_ENABLED=0` 口径冲突，故单列 `make test-race` 与独立 CI job，**不塞进主闸**。

## 协作

`git fetch origin` → 从 `origin/main` 开短特性分支 → 本地三闸全绿 → commit（写清"为什么"）→ push → 建 PR（base `main`）→ 评审（按 P0/P1/P2 修到 READY）→ **合入由 spencer 点**。禁止直推 `main`、禁止自行合入。
