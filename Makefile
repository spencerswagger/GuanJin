# 关津 GuanJin · 本地开发入口
#
# 约定（口径见 docs/decisions.md，本文件只是命令入口）：
#   - schema 唯一权威 = migrations/（golang-migrate 纯 SQL，**手动**执行）
#   - 应用启动任何情况都不得自动迁移（P1）；无 AutoMigrate、无 GORM 软删除
#   - 本地依赖只有 PostgreSQL 17（三库）+ Redis 7；纯软国密，不接密码机
#   - 三闸必须绿：go vet / go test / vue-tsc
#
# ⚠️ 闸的纪律：**判定目标只读、绝不自动改写源码**。
#    格式化检查用 `fmt-check`（`gofmt -l`，只列不改）；`fmt-write` 是给人手动跑的，
#    **不得**出现在 lint / ci / CI 里——否则「一键把红改绿」，闸就形同不存在。

SHELL := /bin/bash
MIGRATIONS_DIR ?= migrations
# 刻意不给默认 DSN：猜一个默认口令只会让新人第一次就撞「password authentication failed」。
# 必须显式注入（本地三库 DSN 见环境变量 GUANJIN_DEV_DSN / GUANJIN_TEST_DSN / GUANJIN_STAGING_DSN）。
DATABASE_URL   ?=
MIGRATE        ?= migrate
COVERAGE_MIN   ?= 60

.PHONY: help require-db-url migrate-up migrate-down migrate-version test test-race vet \
        fmt-check fmt-write gates lint web-install web-check ci

help: ## 显示可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------- schema（唯一权威，手动执行）----------
require-db-url: ## 校验 DATABASE_URL 已显式注入（缺则给出可照抄的示例后失败）
	@if [ -z "$(DATABASE_URL)" ]; then \
	  echo "[FAIL] DATABASE_URL 未注入。示例："; \
	  echo "         make migrate-up DATABASE_URL=\"\$$GUANJIN_DEV_DSN\""; \
	  echo "       （本地三库 DSN 见环境变量 GUANJIN_DEV_DSN / GUANJIN_TEST_DSN / GUANJIN_STAGING_DSN；"; \
	  echo "        不要依赖 Makefile 里的默认口令——那只会换来 password authentication failed）"; \
	  exit 2; \
	fi

migrate-up: require-db-url ## 执行全部迁移（手动、非应用启动触发）
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

migrate-down: require-db-url ## 回滚一步（验证 down 可回滚）
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

migrate-version: require-db-url ## 查看当前迁移版本
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" version

# ---------- Go 侧 ----------
fmt-check: ## 格式检查（**只读**：列出来即失败，绝不改文件）
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "[FAIL] gofmt needed（跑 make fmt-write 修，但不要让它进闸）:"; echo "$$out"; exit 1; fi; echo "[OK] gofmt"

fmt-write: ## 手动格式化（会改写文件；**不得**用于 lint / ci / CI）
	gofmt -l -w .
	@echo "[WARN] 已改写文件。此目标仅限人手跑，闸里只用 fmt-check。"

vet: ## go vet
	go vet ./...

test: ## 单元测试（主闸；按 CGO_ENABLED=0 口径，不带 -race）
	CGO_ENABLED=0 go test ./...

test-race: ## 竞态检测（独立目标：-race 需要 cgo，故必须 CGO_ENABLED=1，不进主闸）
	CGO_ENABLED=1 go test ./... -race

gates: ## 工程红线门禁（禁 AutoMigrate / 禁废弃错误码；脚本存在不算通过，变异注入必须红）
	@./scripts/check-no-automigrate.sh
	@./scripts/check-forbidden-error-codes.sh

lint: fmt-check vet gates ## 静态检查（只读判定：gofmt -l + vet + 门禁）

# ---------- 前端 ----------
web-install: ## 安装前端依赖
	cd web && npm install --no-audit --no-fund

web-check: ## vue-tsc 类型检查（第三闸，只读）
	cd web && npx vue-tsc --noEmit

# ---------- 一条命令跑完三闸（＋门禁；不含 race）----------
ci: lint test web-check ## 三闸全绿（go vet / go test / vue-tsc）＋门禁；race 另跑 make test-race
	@echo "[OK] three gates green"
