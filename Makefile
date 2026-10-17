SHELL := /bin/bash
GO ?= go
GOLANGCI_LINT ?= golangci-lint
BIN_DIR := bin

.PHONY: help build test test-integration lint fmt-check check-no-automigrate migrate-up migrate-down migrate-version run-guanjind run-guanjingw tidy vet govulncheck

help: ## 显示可用目标
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

build: ## 构建 guanjind / guanjingw 两个二进制到 bin/
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/guanjind ./cmd/guanjind
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/guanjingw ./cmd/guanjingw

test: ## 单元测试（-race，不依赖 PG/Redis；集成测试自动跳过）
	@unset GUANJIN_TEST_DATABASE_URL GUANJIN_TEST_REDIS_URL; \
	$(GO) test ./... -race

test-integration: ## 集成测试（需本地 PG16+Redis7；串行 -p 1，避免共享库竞争）
	guanjin_test_database_url="$${GUANJIN_TEST_DATABASE_URL:-postgres://guanjin:guanjin@127.0.0.1:5432/guanjin?sslmode=disable}"; \
	guanjin_test_redis_url="$${GUANJIN_TEST_REDIS_URL:-redis://127.0.0.1:6379/0}"; \
	GUANJIN_TEST_DATABASE_URL="$$guanjin_test_database_url" \
	GUANJIN_TEST_REDIS_URL="$$guanjin_test_redis_url" \
	$(GO) test ./... -p 1 -count=1

lint: ## golangci-lint 静态检查
	$(GOLANGCI_LINT) run ./...

fmt-check: ## gofmt 格式检查
	@files="$$(gofmt -l .)"; if [ -n "$$files" ]; then echo "gofmt 失败，以下文件需格式化："; echo "$$files"; exit 1; fi
	@echo "gofmt ok"

check-no-automigrate: ## CI 门禁：禁止软删除 / AutoMigrate / 因子类型分支
	bash scripts/check-no-automigrate.sh

migrate-up: ## 应用迁移（guanjind migrate up）
	$(GO) run ./cmd/guanjind migrate up

migrate-down: ## 回滚一步迁移（仅开发/应急）
	$(GO) run ./cmd/guanjind migrate down 1

migrate-version: ## 查看当前迁移版本
	$(GO) run ./cmd/guanjind migrate version

run-guanjind: ## 本地运行控制面
	$(GO) run ./cmd/guanjind

run-guanjingw: ## 本地运行数据面
	$(GO) run ./cmd/guanjingw

tidy: ## go mod tidy
	$(GO) mod tidy

vet: ## go vet
	$(GO) vet ./...

govulncheck: ## 漏洞扫描
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...
