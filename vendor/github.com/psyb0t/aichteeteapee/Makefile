DEV_IMAGE := aichteeteapee-dev
UID := $(shell id -u)
GID := $(shell id -g)
MIN_TEST_COVERAGE := 85

# Every Go and tooling command runs inside the dev image, so the toolchain
# (Go 1.26.6), the linter and the security scanners are identical in a local
# shell and in CI. The workspace is bind-mounted; caches go to /tmp so the
# non-root run needs no writable GOPATH. CGO stays on for the race detector.
DEV_RUN := docker run --rm --init \
	--user $(UID):$(GID) \
	-e HOME=/tmp \
	-e GOPATH=/tmp/go \
	-e GOCACHE=/tmp/go-build \
	-e CGO_ENABLED=1 \
	-e MIN_TEST_COVERAGE=$(MIN_TEST_COVERAGE) \
	-v $(CURDIR):/work \
	-w /work \
	$(DEV_IMAGE)

.PHONY: all dev-image dep generate lint lint-fix test test-coverage sec help

all: dep lint test ## Run dep, lint and test

dev-image: ## Build the sandboxed development image
	@docker build -f Dockerfile.dev -t $(DEV_IMAGE) .

dep: dev-image ## Get project dependencies (go mod tidy + vendor)
	@$(DEV_RUN) sh -ceu 'go mod tidy && go mod vendor'

generate: dev-image ## Run all code generation
	@$(DEV_RUN) go generate ./...

lint: dev-image ## Lint all Golang files
	@$(DEV_RUN) bash scripts/lint.sh

lint-fix: dev-image ## Lint all Golang files and fix
	@$(DEV_RUN) sh -ceu 'go fix ./...; go tool golangci-lint run --fix --timeout=30m0s ./...'

test: dev-image ## Run all tests
	@$(DEV_RUN) go test -race ./...

test-coverage: dev-image ## Run tests with coverage check. Fails if coverage is below the threshold.
	@$(DEV_RUN) bash scripts/test-coverage.sh

sec: dev-image ## Security scan (govulncheck + semgrep) merged into sec.sarif; gates on findings
	@$(DEV_RUN) bash scripts/sec.sh

help: ## Display this help message
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
