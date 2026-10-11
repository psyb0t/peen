# Makefile

Make is the only entry point for project operations. If an operation is supported, it has a target with a `##` description, and `make help` lists it.

## Files

- `Makefile` is Peen's. Add Peen targets here, and override a framework target here by defining one with the same name. Make prints override warnings, which are expected.
- `Makefile.servicepack` and `scripts/make/servicepack/` belong to Servicepack and are replaced by framework updates. Do not edit them. A framework target looks for `scripts/make/<name>.sh` before its own copy, so Peen overrides a framework script by adding one there, as `test.sh`, `lint.sh`, and `test_coverage.sh` do.

## Running tools

- Every Go, Node, and lint command runs in the dev image through `$(DEV_RUN)`: an ephemeral `docker run --rm` as your UID and GID, with `HOME=/tmp` and the checkout at `/work`. It has no Docker socket. Unit tests, lint, generation, and builds use it.
- `DEV_RUN` and `DEV_RUN_DIND` carry `--cpus=$(DEV_CPUS)`. Servicepack defines the cap (`DEV_CPUS_RESERVED`, `DEV_CPUS`, `DEV_CPU_LIMIT_ARGS`) and defaults it to all but two CPU threads. Peen's `Makefile` sets `DEV_CPUS ?= 6` before including `Makefile.servicepack`, so Peen's default is 6 and a command-line `DEV_CPUS` still wins. `run-dev` is Servicepack's and does not use them. `DEV_RUN_EXTRA_ARGS` and `DEV_RUN_DIND_EXTRA_ARGS` add `docker run` arguments. Peen does not redefine `DEV_RUN`.
- Run one lint, test, or build target at a time. Never start them in parallel, from one shell or from several agents: each one is a full dev container build or Go compile, and several at once overload the machine.
- `$(DEV_RUN_DIND)` adds the Docker socket, host networking, and the checkout at its real host path, for tests that start sibling containers. Use it only for the targets that need it.
- A target that needs the dev image depends on `dev-image`.
- Nothing installs or runs Go, pnpm, or linters on the host. The host needs Docker, Make, Git, and a shell.

## Writing targets

- Declare every target in `.PHONY`.
- Pass user input to the container through the environment, as `web-pkg-add` does with `WEB_PKG`, and validate it before use. Never splice an unchecked Make variable into shell source.
- Put logic longer than a few lines in a script under `scripts/make/` or `scripts/web/` and call it. See `shell.md`.
- Keep the target names CI calls: `lint`, `test-coverage`, `test-browser`, `sec`, `generate`. See `ci.md`.
- Opt-in targets that cost money or need extra authority, such as `test-real`, `test-docker-worker`, and `run-dev` with `PEEN_DEV_DOCKER_SOCKET=1`, stay out of the default suite.
- Coverage policy is Peen's: `MIN_TEST_COVERAGE := 80` and `TEST_TIMEOUT := 30m` at the top of `Makefile`.
