# Project Makefile
# Add your custom targets here. They override servicepack defaults.

# Peen owns its coverage policy. The Servicepack default is intentionally
# stricter for new projects, while Peen's full production suite currently
# measures in the low eighties.
MIN_TEST_COVERAGE := 80
TEST_TIMEOUT := 30m

# Include servicepack framework commands
include Makefile.servicepack

# Custom targets below this line
# Note: Override warnings are expected and can be ignored

.PHONY: install test test-unit test-integration test-api test-execution-forms \
	test-docker-worker test-docker-worker-source test-real

# PREFIX is where `make install` puts the binary. Override it for a system-wide
# install: `sudo make install PREFIX=/usr/local/bin`.
PREFIX ?= $(HOME)/bin

# The binary is built in the dev image by `build`, so this target only places
# the artifact. It needs no Go toolchain on the host.
install: build ## Install the built binary to PREFIX (default ~/bin)
	@PREFIX="$(PREFIX)" bash scripts/make/install.sh

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

# test-docker-worker is the image-entrypoint contract test. It creates one real
# container, so it takes the Docker socket and is deliberately not part of
# `make test`. It needs a pinned image:
#
#   make test-docker-worker PEEN_DOCKER_WORKER_TEST_IMAGE=psyb0t/peen@sha256:...
#
# It proves that a container created with User 0:0 ends up running as the
# controller's host account, with no sudo, no Docker socket, no network, and no
# view of controller state or another session's socket. It runs no agent, no
# model, and no controller database: the probe replaces the worker command and
# exits.
#
# Its disposable root lives under the repository's gitignored .testing/, because
# this target binds the workspace at the identical host path and a bind source
# under /tmp would resolve to a different directory on the host.
test-docker-worker: dev-image ## Run the opt-in Docker image-entrypoint contract test
	@test -n "$(PEEN_DOCKER_WORKER_TEST_IMAGE)" || \
		{ echo "set PEEN_DOCKER_WORKER_TEST_IMAGE to a pinned psyb0t/peen@sha256 digest"; exit 1; }
	@$(DEV_RUN_DIND) env \
		PEEN_DOCKER_WORKER_TEST=true \
		PEEN_DOCKER_WORKER_TEST_IMAGE=$(PEEN_DOCKER_WORKER_TEST_IMAGE) \
		go test -count=1 -tags=dockerworker -timeout=$(TEST_TIMEOUT) \
		./tests/dockerworker

# test-docker-worker-source builds the Dockerfile in this checkout and runs the
# same probe. The temporary image tag is random, owned by this target, and
# removed afterwards. Unlike the pinned-image check above, this catches an
# entrypoint change before the image reaches a registry.
test-docker-worker-source: dev-image ## Build this checkout's worker image and probe its entrypoint
	@$(DEV_RUN_DIND) bash -euc '\
		source_image="peen-dockerworker-source-$$(cat /proc/sys/kernel/random/uuid)"; \
		cleanup() { if docker image inspect "$$source_image" >/dev/null 2>&1; then docker image rm "$$source_image" >/dev/null; fi; }; \
		trap cleanup EXIT; \
		DOCKER_BUILDKIT=1 docker build --quiet --tag "$$source_image" --file Dockerfile .; \
		env \
			PEEN_DOCKER_WORKER_TEST=true \
			PEEN_DOCKER_WORKER_TEST_LOCAL_IMAGE="$$source_image" \
			go test -count=1 -tags=dockerworker -timeout=$(TEST_TIMEOUT) \
			./tests/dockerworker \
	'

test-real: ## Run opt-in real provider tests with the deployment .env
	@bash scripts/make/test_real.sh

# Example: override a framework command by uncommenting and editing this.
#
# Left COMMENTED on purpose. As a live target it shadowed the framework's real
# `build`, so `make build` printed a line and produced no binary, which broke
# the README's own Quick Start (`make own` → `make build` → ./build/<name> run)
# for everyone who followed it.
#
# build: ## Custom build command
# 	@echo "Running custom build..."

# Add your custom targets below this line
