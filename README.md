# 关津 GuanJin · 统一身份认证平台

GuanJin（关津）是一个以「档位即准入 + 阶梯认证 + 因子无限扩充」为核心的统一身份认证平台。
本仓库为 **阶段 0（Phase 0）工程骨架**：只搭建目录、模块与依赖基线、两个二进制、配置加载、
PostgreSQL(GORM) + Redis 连接、golang-migrate 迁移框架、结构化日志与 trace、统一错误/响应与中间件、
健康与就绪探针、以及 CI 质量门禁。**不含任何认证业务逻辑，不含领域表**。

## 目录结构

```text
cmd/guanjind          控制面（认证 / 管理 / Agent / 静态资源）
cmd/guanjingw         数据面（反向代理 + 认证钩子 + 身份注入）
internal/             平台私有代码（protocol/flow/factor/.../store/config/httpx/observability）
migrations/           golang-migrate SQL（schema 唯一权威）
scripts/              check-no-automigrate.sh / gen-master-key.sh
web/                  Vue 3 前端（占位）
sdk/                  Java SDK（占位）
deploy/               nginx / compose / k8s / secrets（占位）
```

## 快速开始

```bash
# 生成主密钥（base64，>=32 字节）
export GUANJIN_MASTER_KEY="$(bash scripts/gen-master-key.sh)"

# 必填环境变量
export GUANJIN_PUBLIC_BASE_URL="http://localhost:8080"
export GUANJIN_DATABASE_URL="postgres://guanjin:guanjin@127.0.0.1:5432/guanjin?sslmode=disable"
export GUANJIN_REDIS_URL="redis://127.0.0.1:6379/0"

# 迁移（只在显式子命令触发，服务启动绝不自动迁移）
make migrate-up

# 运行
make run-guanjind
make run-guanjingw
```

## 常用命令

| 命令 | 说明 |
|---|---|
| `make build` | 构建 guanjind / guanjingw 两个二进制到 `bin/` |
| `make test` | `go test ./... -race` |
| `make lint` | `golangci-lint run ./...` |
| `make fmt-check` | gofmt 检查 |
| `make migrate-up` | `guanjind migrate up` |
| `make run-guanjind` / `make run-guanjingw` | 本地运行 |

## 探针

- `GET /healthz`：存活探针，恒返回 200。
- `GET /readyz`：就绪探针，返回 `{"checks":{"db":...,"redis":...,"keys":...}}`；
  任一依赖不可用即 **fail-closed** 返回 503，绝不降级放行。

## 工程硬约束

- schema 只由 golang-migrate 管理，**禁止 `AutoMigrate`**；**禁止软删除 `gorm.DeletedAt`**。
- 日志为 zap JSON，字段名固定；**绝不记录令牌 / 口令 / 一次性码 / 标识符明文 / 主密钥**。
- `trace_id` 贯穿：优先 W3C `traceparent`，缺失用 UUID v7，响应头 `X-Trace-Id`。
- 配置前缀 `GUANJIN_`，强类型绑定，必填缺失即 fail-fast。
- 启动配置（env）与运行时配置（`runtime_config` 表）二元分离。

设计基线见 `design-baseline/`，执行计划见 `docs/plans/`。
