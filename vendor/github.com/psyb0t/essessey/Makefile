MIN_TEST_COVERAGE ?= 90

# --- Dev / CI container ------------------------------------------------------
# All Go tooling runs inside the Dockerfile.dev image, so a clean host needs
# Docker rather than a curated Go toolchain. The workspace is mounted at run
# time (not baked in), so the image is reused without a rebuild per change.
APP_NAME := essessey
DEV_IMAGE ?= $(APP_NAME)-dev
UID := $(shell id -u)
GID := $(shell id -g)

DEV_RUN := docker run --rm --init \
	--user $(UID):$(GID) \
	-e HOME=/tmp \
	-e GOPATH=/tmp/go \
	-e GOCACHE=/tmp/go-cache \
	-e GOMODCACHE=/tmp/go-mod-cache \
	-e CGO_ENABLED=1 \
	-e MIN_TEST_COVERAGE=$(MIN_TEST_COVERAGE) \
	-v "$(CURDIR):/app" \
	-w /app \
	$(DEV_IMAGE)

.PHONY: all dev-image dep generate lint lint-fix test test-coverage sec clean help

all: dep lint test ## Run dep, lint and test

dev-image: ## Build the development / CI Docker image
	@docker build -f Dockerfile.dev -t $(DEV_IMAGE) .

dep: dev-image ## Get project dependencies
	@echo "Getting project dependencies..."
	@$(DEV_RUN) sh -ceu 'go mod tidy && go mod vendor'

generate: dev-image ## Run all code generation
	@echo "Running code generation..."
	@$(DEV_RUN) go generate ./...

lint: dev-image ## Lint all Golang files
	@echo "Linting all Go files..."
	@$(DEV_RUN) sh -ceu 'out=$$(go fix -diff ./... 2>&1); \
		if [ -n "$$out" ]; then \
			echo "$$out"; \
			echo "go fix found issues. Run make lint-fix to apply."; \
			exit 1; \
		fi; \
		go tool golangci-lint run --timeout=30m0s ./...'

lint-fix: dev-image ## Lint all Golang files and fix
	@echo "Linting all Go files..."
	@$(DEV_RUN) sh -ceu 'go fix ./...; go tool golangci-lint run --fix --timeout=30m0s ./...'

test: dev-image ## Run all tests
	@echo "Running all tests..."
	@$(DEV_RUN) go test -race ./...

test-coverage: dev-image ## Run tests with coverage check. Fails if coverage is below the threshold.
	@echo "Running tests with coverage check..."
	@$(DEV_RUN) bash scripts/test-coverage.sh

sec: dev-image ## Security scan (govulncheck + semgrep). Fails on any finding.
	@$(DEV_RUN) bash scripts/sec.sh

clean: ## Remove coverage artifacts
	@echo "Cleaning..."
	@rm -f coverage.txt coverage-percent.txt sec.sarif

help: ## Display this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
