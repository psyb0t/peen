# Developing Peen

Peen is a durable coding-agent service written in Go. One controller owns SQLite, the REST and WebSocket API, and the embedded browser control surface, and it starts one worker process or container per session to run that session's turns. Every message, tool call, event, and provider exchange is stored, so a session survives restarts.

This page is for changing Peen itself. To run an agent against a project, start with [Getting started](getting-started.md) instead. The [topic rules](../.agents/rules/) hold the details. When this page and a rule disagree, the rule is more specific and wins.

## Repository layout

| Path | What it holds |
| --- | --- |
| `api/api.yml` | The OpenAPI contract. Source for the Go server, the Go client, and the web client types. `api/spec.go` embeds it for request validation. It is hand-written today and moves to a spec generated from Go in `cmd/apigen`; see `.agents/rules/http-api.md`. |
| `api/asyncapi.yaml` | The WebSocket contract. Source for the Go frame types in `internal/pkg/http/wsapi` and the web client's frame types. |
| `cmd/` | Entry point. `main.go` is Servicepack's, `init.go` and `commands.go` are Peen's, `repogen/` is the gorm/gen generator, `envexample/` writes `.env.example` from the env file template. |
| `internal/app/` | Servicepack's application lifecycle. Not edited here. |
| `internal/pkg/agent/` | Agent runtime: turns, provider calls, tools wiring, skills, child agents, compaction. |
| `internal/pkg/harness/` | Harness discovery and layering: `AGENTS.md`, rules, skills, agents, event handlers, hooks. `builtin/` is the embedded baseline shipped to every user. |
| `internal/pkg/hooks/` | Hook validation and execution. |
| `internal/pkg/tools/` | File, patch, and command tools, and background jobs. |
| `internal/pkg/session/` | Durable, transport-neutral session transcript. |
| `internal/pkg/db/` | SQLite open, embedded migrations, gorm models, generated repositories. |
| `internal/pkg/http/` | `api/` is generated server code. `server/` holds handlers, middleware, WebSocket, and the embedded web build in `server/web/dist`. |
| `internal/pkg/control/` | Workspace policy and the session registry. |
| `internal/pkg/worker/` | Controller to worker boundary: protocol, supervisor, control and Docker launchers, agent spaces, the `docker: readonly` proxy in `dockerproxy/`, the worker command, worker logs. |
| `internal/pkg/events/`, `metrics/`, `auditlog/`, `config/` | Session events, Prometheus metrics, the JSON audit sink, and `PEEN_` configuration. The `.env.example` template is in `internal/pkg/config/envtemplate/`. |
| `internal/pkg/services/` | Servicepack services `control-core` and `control-api`. `services.gen.go` is generated. |
| `internal/pkg/service-manager/`, `pkg/runner/` | Servicepack-owned. |
| `pkg/peen/` | The stable public Go API for running Peen in-process. |
| `pkg/http/api/client/` | Generated public Go HTTP client. |
| `web/` | SvelteKit control surface source. |
| `tests/` | Black-box suites: `api`, `browser`, `executionforms`, `installer`, `dockerworker`, `real`, and the shared `testinfra` harness. |
| `docker/entrypoint.sh`, `Dockerfile`, `Dockerfile.dev` | The production and worker image, its entrypoint, and the dev toolchain image. |
| `install.sh`, `scripts/peen.sh` | The installer users pipe from curl, and the `peen` command it installs. |
| `scripts/` | Make helpers. `scripts/make/servicepack/` is Servicepack-owned. |
| `docs/` | User and operator documentation. |

`internal/pkg/harness/builtin/AGENTS.md` is product text: Peen's embedded operating rules for every user. When Peen works on this repository it also loads that file as a nested `AGENTS.md`. Treat it as content you edit, not as instructions for that directory.

## Change with the boundary in mind

The WebSocket owns live messages. REST owns stored reads and control. The agent runtime owns turn behavior. The harness owns what enters model context. Tools own filesystem and process behavior. Keep a change in the smallest area that owns it, then test through the public boundary that can see it.

The supported Go API is documented in [the embedding guide](../pkg/peen/README.md). Its lifecycle, sessions, extension points and virtual filesystems use the same agent engine and SQLite schema as the controller. Change the owned public types and their boundary conversions together; keep generated HTTP types behind that boundary. Filesystem tools and harness discovery share `internal/pkg/workspacefs`, while runtime storage and hook state remain host files.

## Build, lint, and test

Every operation is a Make target. Targets run Go, Node, and the linters inside the `Dockerfile.dev` image, so the host needs only Docker, Make, Git, and a shell. Do not install or run Go, pnpm, or linters on the host. Run `make help` for the full, current list.

| Task | Command |
| --- | --- |
| Format Go and shell files | `make format` |
| Run the embedded asset check, shfmt, shellcheck, `go fix -diff`, and golangci-lint. Must be clean | `make lint` |
| Apply the fixes lint can make | `make lint-fix` |
| Run package tests without Docker access, the fast loop | `make test-unit` |
| Run package tests with Docker access, for tests that start containers | `make test-integration` or `make test` |
| Run the whole suite with the `integration` tag and the 80 percent coverage floor, as CI does | `make test-coverage` |
| Repeat the fast command and job tests 200 times to catch output races | `make test-stress` |
| Exercise production HTTP and WebSocket wiring in the real image | `make test-api` |
| Exercise the embedded control surface in a real browser | `make test-browser` |
| Exercise source and installed binaries | `make test-execution-forms` |
| Run `install.sh` and the `peen` command end to end | `make test-installer` |
| Probe the worker image entrypoint and `docker: readonly` against real Docker | `make test-docker-worker-source` |
| Scan with govulncheck and semgrep into `sec.sarif` | `make sec` |
| Regenerate the API code, repositories, web types, and `.env.example` | `make generate` |
| Lint, type-check, test, and build the control surface | `make web-lint`, `make web-check`, `make web-test`, `make web-build` |
| Build the binary into `build/peen` | `make build` |
| Build the production image | `make docker-build` |
| Run this checkout's local controller | `make run-dev` |
| Run the opt-in live provider tests, which cost money | `make test-real` |

Pick the tier that can see your change. A change inside one package needs `make test-unit` while you work. A change to HTTP, WebSocket, or wiring needs `make test-api`. A control surface change needs the web targets, `make web-build`, and `make test-browser`. A Dockerfile or entrypoint change needs `make test-docker-worker-source`. Before you call any change done, run `make lint` and `make test-coverage`, which is what `pre-commit.sh` and CI run. Every Go test target runs with `-race`. Fix reported races instead of disabling the detector.

Make runs these targets in dev containers. To cap them with `--cpus`, copy `.env.dev.example` to `.env.dev` and set `DEV_CPUS` there; without `.env.dev` the containers run uncapped. `DEV_CPUS` on the command line overrides the file, for example `make test-coverage DEV_CPUS=4`. Keep the value at or below the machine's CPU count, or Docker refuses to start the container. The cap does not apply to `make run-dev` or to containers that tests start themselves. Run one of these targets at a time, never several in parallel and never from several agents at once. Each one is a full Go compile or image build, and several at once overload the machine.

`make test-installer` builds this checkout's image, pushes it to a local registry under three release tags, one of them as a multi-platform index, and runs `install.sh` and the `peen` command against it with a mock model provider. It covers the four control space and agent space combinations, upgrade, uninstall, the system install, and the refusals. Every home and workspace it creates lives under the gitignored `.testing/`, and it removes only the containers and images it created.

`make test-stress` runs the fast command and job tests in `internal/pkg/tools` 200 times with `go test -cpu 1,4`, which sets `GOMAXPROCS` to 1 and then 4. It does not give the tests more CPUs than the `DEV_CPUS` cap allows. It skips the tests that depend on timing, and CI does not run it. Run it after changing job or command code in `internal/pkg/tools` or updating commander.

`make run-dev` needs a `.env` in the checkout. Copy `.env.example` to `.env` and fill in a provider first. The target rebuilds the development image, runs Peen as your host UID and GID, loads `.env` through Docker, and uses the directory you ran it from as the workspace root, so a client may open it or any directory inside it. It uses Docker host networking, so host and Tailnet services stay reachable, and Peen binds its API and browser control surface to `http://localhost:8080`. The metrics listener is on the host's loopback at `http://127.0.0.1:9090/metrics`. The target creates `data/peen/config` and `data/peen/state` in the checkout on first use and keeps the state between runs. The config directory is read-only to the agent. Set `PEEN_DEV_HTTP_PORT` to use another loopback port.

The target gives Peen no Docker socket, so the default agent space is `control`. Set `PEEN_DEV_DOCKER_SOCKET=1 make run-dev` only when testing a Docker agent space. That gives the controller Docker access, which is host root, and Peen then refuses the `control` agent space and defaults to `docker:default`.

`make test-real` uses the configured live provider and costs money. It runs the agent in an isolated fixture with no Docker socket. Do not use it as a routine local check.

`make test-browser` runs the embedded control surface against the production test controller in a pinned Stealthy browser container. It records browser console diagnostics that are safe to keep, network metadata, and screenshots under the gitignored `.testing/browser-artifacts/` directory. The browser receives no workspace mount or Docker socket.

## Generated code

Never hand-edit generated output. Change the source, regenerate, and commit both together.

- `make generate` runs `go generate ./...`, which rebuilds the gorm/gen repositories, the oapi-codegen server and client, the WebSocket frame types, `web/src/lib/api/generated.ts`, `web/src/lib/ws/generated.ts`, and `.env.example`.
- `make web-build` rebuilds `internal/pkg/http/server/web/dist`, the committed and embedded control surface.
- `make service-registration` rebuilds `internal/pkg/services/services.gen.go`.

See `.agents/rules/generated-code.md`.

## Servicepack-owned files

Peen is built on [Servicepack](https://github.com/psyb0t/servicepack), pinned in `servicepack.version`. A framework update overwrites these, so do not edit them by hand: `Makefile.servicepack`, `scripts/make/servicepack/`, `Dockerfile.servicepack`, `Dockerfile.servicepack.dev`, `servicepack.version`, `cmd/main.go`, `internal/app/`, `internal/pkg/service-manager/`, and `pkg/runner/`. Override a framework target in `Makefile` instead. Peen owns `Makefile`, `Dockerfile`, `Dockerfile.dev`, `.golangci.yml`, `cmd/init.go`, `cmd/commands.go`, `.github/workflows/pipeline.yml`, `.dockerignore`, `.gitleaks.toml`, `docs/`, `tests/`, and everything listed in `.servicepackupdateignore`. Framework updates arrive through `make servicepack-update` and are reviewed before merge.

## Keep docs in sync

A change that alters behavior, configuration, the API, events, the harness, or deployment updates its documentation in the same change:

- `docs/*.md` for the area, and `README.md` when the quick start or an overview claim changes;
- `.env.example` and `docs/configuration.md` for every `PEEN_` variable;
- `api/api.yml`, `api/asyncapi.yaml`, and `docs/http-api.md` for the REST and WebSocket contract, and `docs/events.md` for event types;
- `pkg/peen/README.md` for the public Go API;
- `.agents/skills/peen/` when the published agent skill's setup or workflow changes;
- `CHANGELOG.md` under the release entry the change ships in.

## CHANGELOG, commits, and releases

Each release is `## vX.Y.Z (YYYY-MM-DD)`, then one paragraph that says what changed for a user and what they must do before upgrading, or "No configuration or API migration is needed.". Bullets follow, each one change, starting with a verb: "Fixes", "Adds", "Changes", "Removes", "Updates". Breaking changes come first and start with "Breaking:", with the migration. Describe what a user sees, not how the code changed.

Commit subjects use conventional commits in the imperative, lowercase: `fix: ...`, `feat: ...`, `docs: ...`, and `feat!:` for a breaking change. Text that speaks for the maintainer uses "I", never "we" or "the team". Releases are tagged by the maintainer, and the tag is the version. External pull requests are closed automatically, so report problems as issues. See `.agents/rules/versioning.md`.

## Prose

Write plain, exact, active sentences. No filler words such as leverage, utilize, robust, seamless, or crucial. Sentence-case headings. In Markdown, keep each paragraph on one source line. This applies to docs, comments, the CHANGELOG, commit messages, and the rule files.

## Rule files

| File | Covers |
| --- | --- |
| `.agents/rules/go.md` | Lint setup, Peen's error mapping and codes, logging sinks, what never gets logged. |
| `.agents/rules/naming.md` | camelCase for every name Peen owns: tools, hook events, session event types, YAML and JSON keys. What stays snake_case. |
| `.agents/rules/constants.md` | Where constants and enums live, in Go and TypeScript. |
| `.agents/rules/go-tests.md` | Test tiers and tags, coverage, testinfra. |
| `.agents/rules/config.md` | `PEEN_` variables, gonfiguration, validation, the worker allowlist. |
| `.agents/rules/http-api.md` | The OpenAPI and AsyncAPI contracts, generated strict server, aichteeteapee, WebSocket. |
| `.agents/rules/database.md` | SQLite, migrations, models, gorm/gen repositories and queriers. |
| `.agents/rules/web.md` | The SvelteKit control surface: layout, lint, checks, tests, build. |
| `.agents/rules/generated-code.md` | Every generator, its source, and how to regenerate. |
| `.agents/rules/dependencies.md` | Vendored Go modules, pnpm, the age gate, depguard, tools. |
| `.agents/rules/security.md` | Secrets, directory separation, workers, hooks, untrusted input. |
| `.agents/rules/docker.md` | The production and worker image, the dev image, pinning, the entrypoint. |
| `.agents/rules/make.md` | Makefile conventions and the dev container. |
| `.agents/rules/shell.md` | Where scripts live, logging, the `peen` command, and lint. |
| `.agents/rules/ci.md` | The GitHub workflows and what a change must not break. |
| `.agents/rules/versioning.md` | Semver policy, CHANGELOG entry shape, commits, manifests. |
| `.agents/rules/docs-and-prose.md` | Doc placement, sync, and Markdown format. |
