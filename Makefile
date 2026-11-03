# 关津 GuanJin · 本地开发入口
#
# 约定：
#   - schema 唯一权威 = migrations/（golang-migrate 纯 SQL，手动执行）
#   - 应用启动任何情况都不得自动迁移（decisions.md P1）
#   - 本地依赖只有 PostgreSQL 17（三库）+ Redis 7
#
# 本文件在 S0（文档骨架）阶段只定义目标名，真正的二进制/脚本随 S1（工程骨架）落地。

SHELL := /bin/bash
MIGRATIONS_DIR ?= migrations
DATABASE_URL   ?= postgres://guanjin:guanjin@127.0.0.1:5432/guanjin_dev?sslmode=disable
MIGRATE        ?= migrate

.PHONY: help migrate-up migrate-down migrate-version test lint fmt check-gates

help: ## 显示可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

migrate-up: ## 执行全部迁移（手动、非应用启动触发）
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

migrate-down: ## 回滚一步（验证 down 可回滚）
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

migrate-version: ## 查看当前迁移版本
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" version

fmt: ## 格式化
	gofmt -l -w .

test: ## 单元测试
	go test ./...

lint: fmt check-gates ## 静态检查 + 门禁脚本
	go vet ./...

check-gates: ## 工程红线门禁（S1 落地脚本本体；脚本存在不算通过，需变异注入必红）
	@for s in scripts/check-no-automigrate.sh scripts/check-forbidden-error-codes.sh; do \
	  if [ -x "$$s" ]; then echo "[gate] $$s"; "$$s"; else echo "[gate] skip (未落地): $$s"; fi; \
	done
