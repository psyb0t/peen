# Project Makefile
# Add your custom targets here - they will override servicepack defaults

# Peen owns its coverage policy. The Servicepack default is intentionally
# stricter for new projects, while Peen's full production suite currently
# measures in the low eighties.
MIN_TEST_COVERAGE := 80
TEST_TIMEOUT := 30m

# Include servicepack framework commands
include Makefile.servicepack

# Custom targets below this line
# Note: Override warnings are expected and can be ignored

.PHONY: test test-unit test-integration test-api test-execution-forms test-real

test: ## Run every Go test without the race detector
	@$(MAKE) dev-image
	@$(call run_dind_script,test.sh)

test-unit: dev-image ## Run unit tests without the race detector
	@$(DEV_RUN) go test -timeout=$(TEST_TIMEOUT) ./...

test-integration: dev-image ## Run integration tests without the race detector
	@$(DEV_RUN_DIND) go test -count=1 -timeout=$(TEST_TIMEOUT) ./...

test-api: dev-image ## Run containerized API tests through production wiring
	@$(DEV_RUN_DIND) go test -count=1 -tags=integration -timeout=$(TEST_TIMEOUT) ./tests/api

test-execution-forms: dev-image ## Run source and installed binary contract tests
	@$(DEV_RUN) go test -count=1 -tags=integration -timeout=$(TEST_TIMEOUT) ./tests/executionforms

test-real: ## Run opt-in real provider tests with the deployment .env
	@bash scripts/make/test_real.sh

# Example: override a framework command by uncommenting and editing this.
#
# Left COMMENTED on purpose. As a live target it shadowed the framework's real
# `build`, so `make build` printed a line and produced no binary — which broke
# the README's own Quick Start (`make own` → `make build` → ./build/<name> run)
# for everyone who followed it.
#
# build: ## Custom build command
# 	@echo "Running custom build..."

# Add your custom targets below this line
