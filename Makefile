# 关津 GuanJin · 本地开发入口
#
# 约定（口径见 docs/decisions.md，本文件只是命令入口）：
#   - schema 唯一权威 = migrations/（golang-migrate 纯 SQL，**手动**执行）
#   - golang-migrate CLI：PATH 上没有时回退到 ./bin/migrate（`make migrate-tool` 装，版本 v4.19.1 与 CI 同一版本）
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
# golang-migrate CLI 的位置与版本：**版本单一来源**（Makefile 与 CI 用同一个 v4.19.1，不许漂）。
# 解析顺序：PATH 上的 `migrate` → 仓库内 `./bin/migrate`（`make migrate-tool` 装，bin/ 已在 .gitignore）。
# 两者都没有时由 `require-migrate` 给出可照抄的安装指引并以 2 退出，
# 而不是让 `$(MIGRATE) -path …` 抛一句 `migrate: not found`（那是环境问题，却被当成迁移失败）。
MIGRATE_VERSION ?= v4.19.1
MIGRATE        ?= $(shell command -v migrate 2>/dev/null || { [ -x bin/migrate ] && echo bin/migrate; } || true)
COVERAGE_MIN   ?= 60
# 证据目录（不入库，见 .gitignore 的 artifacts/）
TEST_LOG_DIR   ?= artifacts/local/gate

.PHONY: help require-db-url require-migrate migrate-tool migrate-up migrate-down migrate-version \
        test test-race vet fmt-check fmt-write gates coverage lint web-install web-check ci

help: ## 显示可用目标
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | \
	  while IFS= read -r line; do \
	    printf '  \033[36m%-18s\033[0m %s\n' "$${line%%:*}" "$${line#*## }"; \
	  done

# ---------- schema（唯一权威，手动执行）----------
require-db-url: ## 校验 DATABASE_URL 已显式注入（缺则给出可照抄的示例后失败）
	@if [ -z "$(DATABASE_URL)" ]; then \
	  echo "[FAIL] DATABASE_URL 未注入。示例："; \
	  echo "         make migrate-up DATABASE_URL=\"\$$GUANJIN_DEV_DSN\""; \
	  echo "       （本地三库 DSN 见环境变量 GUANJIN_DEV_DSN / GUANJIN_TEST_DSN / GUANJIN_STAGING_DSN；"; \
	  echo "        不要依赖 Makefile 里的默认口令——那只会换来 password authentication failed）"; \
	  exit 2; \
	fi

migrate-tool: ## 安装 golang-migrate CLI 到 ./bin（版本固定 = MIGRATE_VERSION，与 CI 同源）
	@mkdir -p bin
	@GOBIN=$(CURDIR)/bin go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)
	@# `migrate -version` 不带 ldflags 时只吐一个 "dev"，证明不了版本；模块版本记在二进制的 build info 里。
	@go version -m ./bin/migrate | grep -E '^[[:space:]]*mod[[:space:]]+github.com/golang-migrate/migrate/v4[[:space:]]' \
	  || { echo "[FAIL] ./bin/migrate 不是 golang-migrate 模块产物"; exit 1; }
	@echo "[OK] 已装 $(CURDIR)/bin/migrate（请求 $(MIGRATE_VERSION)，实际模块版本见上一条 mod 记录）"

require-migrate: ## 校验 migrate CLI 可用（PATH 或 ./bin/migrate），缺则给安装指引后失败
	@if [ -z "$(MIGRATE)" ]; then \
	  echo "[FAIL] 找不到 golang-migrate CLI（PATH 与 ./bin 都没有）；迁移是手动式（decisions.md P1），必须有它。"; \
	  echo "        一步装好（版本与 CI 一致）： make migrate-tool"; \
	  echo "        或手动： GOBIN=\"\$$PWD/bin\" go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION)"; \
	  exit 2; \
	fi
	@echo "[OK] migrate=$(MIGRATE)（请求版本 $(MIGRATE_VERSION)）"

migrate-up: require-db-url require-migrate ## 执行全部迁移（手动、非应用启动触发）
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

migrate-down: require-db-url require-migrate ## 回滚一步（验证 down 可回滚）
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

migrate-version: require-db-url require-migrate ## 查看当前迁移版本
	$(MIGRATE) -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" version

# ---------- Go 侧 ----------
fmt-check: ## 格式检查（**只读**：列出来即失败，绝不改文件）
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "[FAIL] gofmt needed（跑 make fmt-write 修，但不要让它进闸）:"; echo "$$out"; exit 1; fi; echo "[OK] gofmt"

fmt-write: ## 手动格式化（会改写文件；**不得**用于 lint / ci / CI）
	gofmt -l -w .
	@echo "[WARN] 已改写文件。此目标仅限人手跑，闸里只用 fmt-check。"

vet: ## go vet
	go vet ./...

test: ## 单元测试（主闸）：-count=1 不吃缓存、-v 可数 PASS/SKIP，并产出 summary.txt
	@mkdir -p $(TEST_LOG_DIR)
	@set -o pipefail; CGO_ENABLED=0 go test ./... -count=1 -v -coverprofile=$(TEST_LOG_DIR)/cover.out 2>&1 \
	  | tee $(TEST_LOG_DIR)/go-test.log
	@./scripts/test-summary.sh $(TEST_LOG_DIR)/go-test.log | tee $(TEST_LOG_DIR)/summary.txt

coverage: ## 覆盖率门禁（复用 test 的 coverprofile，不重复跑）
	@COVERAGE_PROFILE=$(TEST_LOG_DIR)/cover.out COVERAGE_MIN=$(COVERAGE_MIN) ./scripts/coverage-gate.sh

test-race: ## 竞态检测（独立目标：-race 需要 cgo，故必须 CGO_ENABLED=1，不进主闸；同样 -count=1 不吃缓存）
	CGO_ENABLED=1 go test ./... -count=1 -race

gates: ## 工程红线门禁（禁 AutoMigrate / 禁废弃错误码 / 数据面不越界；脚本存在不算通过，变异注入必须红）
	@./scripts/check-no-automigrate.sh
	@./scripts/check-forbidden-error-codes.sh
	@./scripts/check-dataplane-boundaries.sh --self-test

lint: fmt-check vet gates ## 静态检查（只读判定：gofmt -l + vet + 门禁）

# ---------- 前端 ----------
web-install: ## 安装前端依赖
	cd web && npm install --no-audit --no-fund

web-check: ## vue-tsc 类型检查（第三闸，只读）
	cd web && npx vue-tsc --noEmit

# ---------- 一条命令跑完三闸（＋门禁；不含 race）----------
ci: lint test coverage web-check ## 三闸全绿（go vet / go test / vue-tsc）＋门禁＋覆盖率；race 另跑 make test-race
	@echo "[OK] three gates green"
