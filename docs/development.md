# Development

The supported everyday workflow runs Go tooling inside the project's Docker
development image. That removes host-version drift and means a clean machine
needs Docker rather than a separately curated Go toolchain.

The Makefile has two execution shapes:

- ordinary build, lint, formatting, dependency, and vulnerability work runs
  in the development container with the repository mounted at `/work`;
- `make test-unit` uses the ordinary socketless container; full, mock
  integration, API, and coverage targets mount the Docker socket.

The exact scripts and override lookup live in the
[framework Make-script deep dive](../scripts/make/servicepack/README.md).

## Common targets

| Target | Runs where | Purpose |
| --- | --- | --- |
| `make dev-image` | Docker build | Build the development image used by normal tooling. |
| `make test` | Docker + socket | Race-enabled `go test ./...`. |
| `make test-unit` | Docker | Race-enabled `go test ./...` without Docker-socket access. |
| `make test-integration` | Docker + socket | Uncached, race-enabled core integration tests with mocked LLM replies. |
| `make test-api` | Docker + socket | Production image API tests with real HTTP, SQLite, migrations, SSE, and session handling. |
| `make test-execution-forms` | Docker | Source-build and local-install process tests with real loopback HTTP. |
| `make test-coverage` | Docker + socket | Race-enabled coverage check; default floor is `MIN_TEST_COVERAGE=90`. |
| `make lint` | Docker | `shfmt`, ShellCheck, `go fix` diff check, and golangci-lint. |
| `make lint-fix` | Docker | Apply supported formatting and lint fixes. Review its diff. |
| `make format` | Docker | Run gofumpt and shfmt. |
| `make sec` | Docker | Full security scan: `govulncheck` + semgrep, merged into `sec.sarif` for the Security tab. Fails on any finding. |
| `make generate` | Docker | Execute package-local `go:generate` directives. |
| `make build` | Docker | Build a static executable under `build/`. |
| `make docker-build` | Docker | Build the production image. |
| `make run-dev` | Docker | Build/run the development image with the race detector. |

Use `make help` as the current target list. Do not replace these calls with a
host `go test` or host linter in project automation: that gives two different
toolchains two chances to disagree.

## Tests, integration tests, and coverage

Peen has four test layers. `make test-unit` covers pure and in-process package
behaviour without a Docker socket. `make test-integration` is an uncached,
race-enabled mock integration suite. It exercises the agent, session, and
service paths against deterministic Elelem replies. `make test-api` builds the
production image and drives its real HTTP API, SQLite database, migrations,
SSE stream, session middleware, cancellation, and authentication. Its only
mock is the external OpenAI-compatible provider. `make test-execution-forms`
builds source and local `go install` artifacts, runs both as child processes,
and checks guarded tools, restart persistence, harness reload, authentication,
and the private metrics listener.

`make test` remains the standard package suite. It enables Go's race detector
and has Docker available for Testcontainers-backed tests.

`make test-coverage` also runs the race detector. It instruments every module
package (`-coverpkg=<module>/...`) and runs `-tags=integration`, so a test under
`tests/` credits coverage to the production package it drives, and **services are
covered**. A service-heavy project does not need to override the script to gate
its own code. A service that runs out-of-process in a real container is covered
too: the script exports `SERVICEPACK_COVDATA_DIR`, an integration test mounts it
into that container as `GOCOVERDIR`, and the native covdata is merged into the
total. Only what is not hand-written code under test is excluded from the floor:
`cmd/` mains, the `tests/` harness, generated `*.gen.go`, the service-manager
mocks, and the framework's own `example-*` / `hello-world` demo services.
Override the threshold deliberately:

```bash
make test-coverage MIN_TEST_COVERAGE=95
```

The target writes `coverage-percent.txt` for the badge workflow and removes
the temporary coverage profiles. Testcontainers needs access to the Docker
daemon; if Docker cannot create containers, fix Docker access rather than
working around the integration tests.

Real API tests live under `tests/api`. `tests/testinfra` builds the servicepack
image from the repo `Dockerfile`, starts it with production wiring, and exposes
the application endpoint to Go tests. Extend `Infra` with real dependencies
when Peen gains them, then drive those dependencies through public HTTP
contracts. `tests/` is protected by the update's mechanical floor, so a
framework update never overwrites it. `DEV_RUN_DIND` runs on the host network,
so testcontainers' host-published ports are reachable from the test process.

## Build outputs and runtime identity

`make build` uses the pinned Go build image, installs the static-build
requirements inside that temporary container, and writes `build/<module-tail>`
back with your host UID/GID. It injects three values with linker flags:

- `main.appName`: the final segment of the module path; it sets the binary
  name and root Cobra command name;
- `main.buildCommit`: the checked-out `HEAD` commit when available.
- `main.buildVersion`: the exact Git tag at `HEAD`, or `dev` for an untagged
  source build.

`cmd/main.go` puts those into the global log scope as `binary`, `commit`, and
`version`.
Build output is therefore traceable without hardcoding identity in service
code. A source tree without a resolvable Git `HEAD` builds, but has no commit
field to inject.

`make docker-build` is a separate production-image path. Do not assume a
Dockerfile build and `make build` have the same customization hooks; inspect
the relevant script before changing either.

## Dependencies and code generation

Use the Make targets so dependency metadata and `vendor/` stay synchronized:

```bash
make dep
make pkg-add PKG=example.com/module@v1.2.3
make pkg-update PKG=example.com/module
make pkg-upgrade
make pkg-remove PKG=example.com/module
```

The framework commits Go's `vendor/` tree. Do not edit vendored sources by
hand. `make service` already regenerates service registration; use
`make service-registration` after manual service changes. Treat
`internal/pkg/services/services.gen.go` as generated output.

## Override, do not fork framework plumbing

`Makefile` includes `Makefile.servicepack`. Define the same target in your
project Makefile to replace a framework target, or add your own targets next
to it. Framework script lookup prefers `scripts/make/<script>.sh` over
`scripts/make/servicepack/<script>.sh`.

```bash
cp scripts/make/servicepack/test.sh scripts/make/test.sh
# edit scripts/make/test.sh for project-specific behavior
```

Likewise, project `Dockerfile`, `Dockerfile.dev`, `cmd/init.go`, and
`cmd/commands.go` are yours. The framework-owned versions are updated by
`make servicepack-update`; see [framework updates](framework-updates.md).

## Before you hand off a change

For a normal code change, run the narrowest useful target first, then the
relevant full check, for example `make test`, `make lint`, and
`make test-coverage` when a framework behavior changes. The repository's
pre-commit hook calls `make lint && make test-coverage`, so it will repeat
those checks during the project's normal commit flow.

Read [getting started](getting-started.md) for ownership boundaries and
[architecture](architecture.md) for where a change should live.
