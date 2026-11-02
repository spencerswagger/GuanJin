# 关津 GuanJin · 本地开发入口
#
# 约定（口径见 docs/decisions.md，本文件只是命令入口）：
#   - schema 唯一权威 = migrations/（golang-migrate 纯 SQL，**手动**执行）
#   - 应用启动任何情况都不得自动迁移（P1）；无 AutoMigrate、无 GORM 软删除
#   - 本地依赖只有 PostgreSQL 17（三库）+ Redis 7；纯软国密，不接密码机
#   - 三闸必须绿：go vet / go test / vue-tsc

SHELL := /bin/bash
MIGRATIONS_DIR ?= migrations
DATABASE_URL   ?= $(GUANJIN_DB_DSN)
MIGRATE        ?= migrate
COVERAGE_MIN   ?= 60

.PHONY: help migrate-up migrate-down migrate-version test vet fmt fmt-check gates lint web-install web-check ci

help: ## 显示可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------- schema（唯一权威，手动执行）----------
migrate-up: ## 执行全部迁移
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

migrate-down: ## 回滚一步（验证 down 可回滚）
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

migrate-version: ## 查看当前迁移版本
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" version

# ---------- Go 侧 ----------
fmt: ## 格式化
	gofmt -l -w .

fmt-check: ## 格式检查（有未格式化文件即失败）
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "[FAIL] gofmt needed:"; echo "$$out"; exit 1; fi; echo "[OK] gofmt"

vet: ## go vet
	go vet ./...

test: ## 单元测试
	go test ./...

gates: ## 工程红线门禁（禁 AutoMigrate / 禁废弃错误码；脚本存在不算通过，变异注入必须红）
	@./scripts/check-no-automigrate.sh
	@./scripts/check-forbidden-error-codes.sh

lint: fmt-check vet gates ## 静态检查全家桶

# ---------- 前端 ----------
web-install: ## 安装前端依赖
	cd web && npm install --no-audit --no-fund

web-check: ## vue-tsc 类型检查（第三闸）
	cd web && npx vue-tsc --noEmit

# ---------- 一条命令跑完三闸 ----------
ci: lint test web-check ## 三闸全绿（go vet / go test / vue-tsc）
	@echo "[OK] three gates green"
