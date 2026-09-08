# Project Makefile
# Add your custom targets here - they will override servicepack defaults

# Override framework variables (optional)
# MIN_TEST_COVERAGE := 95

# Include servicepack framework commands
include Makefile.servicepack

# Custom targets below this line
# Note: Override warnings are expected and can be ignored

.PHONY: test-api test-execution-forms test-real

test-api: dev-image ## Run containerized API tests through production wiring
	@$(DEV_RUN_DIND) go test -race -count=1 -tags=integration -timeout=600s ./tests/api

test-execution-forms: dev-image ## Run source and installed binary contract tests
	@$(DEV_RUN) go test -race -count=1 -tags=integration -timeout=600s ./tests/executionforms

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
