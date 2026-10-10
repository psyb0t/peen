# Peen contributor guide

Peen is a durable coding-agent service written in Go. One controller owns SQLite, the REST and WebSocket API, and the embedded browser control surface, and it starts one worker process or container per session to run that session's turns. Every message, tool call, event, and provider exchange is stored, so a session survives restarts.

This file covers how to change Peen itself. The topic rules in `.agents/rules/` hold the details, and the user docs live in `docs/`. When this file and a rule disagree, the rule is more specific and wins.

## Repository layout

| Path | What it holds |
| --- | --- |
| `api/api.yml` | The OpenAPI contract. Source for the Go server, the Go client, and the web client types. `api/spec.go` embeds it for request validation. It is hand-written today and moves to a spec generated from Go in `cmd/apigen`; see `.agents/rules/http-api.md`. |
| `cmd/` | Entry point. `main.go` is Servicepack's, `init.go` and `commands.go` are Peen's, `repogen/` is the gorm/gen generator. |
| `internal/app/` | Servicepack's application lifecycle. Not edited here. |
| `internal/pkg/agent/` | Agent runtime: turns, provider calls, tools wiring, skills, child agents, compaction. |
| `internal/pkg/harness/` | Harness discovery and layering: `AGENTS.md`, rules, skills, agents, event handlers, hooks. `builtin/` is the embedded baseline shipped to every user. |
| `internal/pkg/hooks/` | Hook validation and execution. |
| `internal/pkg/tools/` | File, patch, and command tools, and background jobs. |
| `internal/pkg/session/` | Durable, transport-neutral session transcript. |
| `internal/pkg/db/` | SQLite open, embedded migrations, gorm models, generated repositories. |
| `internal/pkg/http/` | `api/` is generated server code. `server/` holds handlers, middleware, WebSocket, and the embedded web build in `server/web/dist`. |
| `internal/pkg/control/` | Workspace policy and the session registry. |
| `internal/pkg/worker/` | Controller to worker boundary: protocol, supervisor, native and Docker launchers, the worker command, worker logs. |
| `internal/pkg/events/`, `metrics/`, `auditlog/`, `config/` | Session events, Prometheus metrics, the JSON audit sink, and `PEEN_` configuration. |
| `internal/pkg/services/` | Servicepack services `control-core` and `control-api`. `services.gen.go` is generated. |
| `internal/pkg/service-manager/`, `pkg/runner/` | Servicepack-owned. |
| `pkg/peen/` | The stable public Go API for running Peen in-process. |
| `pkg/http/api/client/` | Generated public Go HTTP client. |
| `web/` | SvelteKit control surface source. |
| `tests/` | Black-box suites: `api`, `browser`, `executionforms`, `dockerworker`, `real`, and the shared `testinfra` harness. |
| `docker/entrypoint.sh`, `Dockerfile`, `Dockerfile.dev` | The production and worker image, its entrypoint, and the dev toolchain image. |
| `scripts/` | Make helpers. `scripts/make/servicepack/` is Servicepack-owned. |
| `docs/` | User and operator documentation. |

`internal/pkg/harness/builtin/AGENTS.md` is product text: Peen's embedded operating rules for every user. When Peen works on this repository it also loads that file as a nested `AGENTS.md`. Treat it as content you edit, not as instructions for that directory.

## Build, lint, and test

Every operation is a Make target. Targets run Go, Node, and the linters inside the `Dockerfile.dev` image, so the host needs only Docker, Make, Git, and a shell. Do not install or run Go, pnpm, or linters on the host. Run `make help` for the full list.

| Command | Use it for |
| --- | --- |
| `make format` | gofumpt on Go files, shfmt on scripts. |
| `make lint` | Embedded asset check, shfmt, shellcheck, `go fix -diff`, golangci-lint. Must be clean. |
| `make lint-fix` | Apply the fixes lint can make. |
| `make build` | Build `build/peen`. |
| `make test-unit` | Untagged Go tests without Docker access. The fast loop. |
| `make test` / `make test-integration` | Untagged Go tests with Docker access, for tests that start containers. |
| `make test-api` | `tests/api` through production HTTP and WebSocket wiring in the real image. |
| `make test-execution-forms` | Source and installed binary contracts. |
| `make test-browser` | The control surface in a real browser against a test controller. |
| `make test-docker-worker-source` | Builds this checkout's image and probes the worker entrypoint. |
| `make test-coverage` | The full suite with the `integration` tag and the 80 percent coverage floor. |
| `make sec` | govulncheck and semgrep, written to `sec.sarif`. |
| `make web-lint`, `make web-check`, `make web-test` | ESLint and Prettier, svelte-check and tsc, Vitest for `web/`. |
| `make test-real` | Opt-in live provider tests. Costs money. Never a routine check. |

Pick the tier that can see your change. A change inside one package needs `make test-unit` while you work. A change to HTTP, WebSocket, or wiring needs `make test-api`. A control surface change needs the web targets, `make web-build`, and `make test-browser`. A Dockerfile or entrypoint change needs `make test-docker-worker-source`. Before you call any change done, run `make lint` and `make test-coverage`, which is what `pre-commit.sh` and CI run. The Make targets run without the race detector.

`make run-dev` runs this checkout as a local controller on `http://localhost:8080`, with state under the gitignored `data/peen/`.

## Generated code

Never hand-edit generated output. Change the source, regenerate, and commit both together.

- `make generate` runs `go generate ./...`, which rebuilds the gorm/gen repositories, the oapi-codegen server and client, and `web/src/lib/api/generated.ts`.
- `make web-build` rebuilds `internal/pkg/http/server/web/dist`, the committed and embedded control surface.
- `make service-registration` rebuilds `internal/pkg/services/services.gen.go`.

See `.agents/rules/generated-code.md`.

## Servicepack-owned files

Peen is built on [Servicepack](https://github.com/psyb0t/servicepack), pinned in `servicepack.version`. A framework update overwrites these, so do not edit them by hand: `Makefile.servicepack`, `scripts/make/servicepack/`, `Dockerfile.servicepack`, `Dockerfile.servicepack.dev`, `servicepack.version`, `cmd/main.go`, `internal/app/`, `internal/pkg/service-manager/`, and `pkg/runner/`. Override a framework target in `Makefile` instead. Peen owns `Makefile`, `Dockerfile`, `Dockerfile.dev`, `.golangci.yml`, `cmd/init.go`, `cmd/commands.go`, `.github/workflows/pipeline.yml`, `.dockerignore`, `.gitleaks.toml`, `docs/`, `tests/`, and everything listed in `.servicepackupdateignore`. Framework updates arrive through `make servicepack-update` and are reviewed before merge.

## Keep docs in sync

A change that alters behavior, configuration, the API, events, the harness, or deployment updates its documentation in the same change:

- `docs/*.md` for the area, and `README.md` when the quick start or an overview claim changes;
- `.env.example` and `docs/configuration.md` for every `PEEN_` variable;
- `api/api.yml` and `docs/http-api.md` for the REST and WebSocket contract, and `docs/events.md` for event types;
- `pkg/peen/README.md` for the public Go API;
- `.agents/skills/peen/` when the published agent skill's setup or workflow changes;
- `CHANGELOG.md` under the release entry the change ships in.

## CHANGELOG, commits, and releases

Each release is `## vX.Y.Z (YYYY-MM-DD)`, then one paragraph that says what changed for a user and what they must do before upgrading, or "No configuration or API migration is needed.". Bullets follow, each one change, starting with a verb: "Fixes", "Adds", "Changes", "Removes", "Updates". Breaking changes come first and start with "Breaking:", with the migration. Describe what a user sees, not how the code changed.

Commit subjects use conventional commits in the imperative, lowercase: `fix: ...`, `feat: ...`, `docs: ...`, and `feat!:` for a breaking change. Text that speaks for the maintainer uses "I", never "we" or "the team". Releases are tagged by the maintainer, and the tag is the version. External pull requests are closed automatically, so report problems as issues. See `.agents/rules/versioning.md`.

## Prose

Write plain, exact, active sentences. No em dashes or en dashes. No filler words such as leverage, utilize, robust, seamless, or crucial. Sentence-case headings. In Markdown, keep each paragraph on one source line. This applies to docs, comments, the CHANGELOG, commit messages, and these rule files. See `.agents/rules/docs-and-prose.md`.

## Rule files

| File | Covers |
| --- | --- |
| `.agents/rules/go-style.md` | Control flow, naming, comments, line length, lint suppressions. |
| `.agents/rules/go-errors.md` | ctxerrors wrapping, sentinels, commerr, error mapping. |
| `.agents/rules/go-logging.md` | slog through ctxscope, levels, fields, what never gets logged. |
| `.agents/rules/constants.md` | Named constants, typed enums, where they live, in Go and TypeScript. |
| `.agents/rules/go-tests.md` | testify, table tests, file names, test tiers and tags, testinfra. |
| `.agents/rules/config.md` | `PEEN_` variables, gonfiguration, validation, the worker allowlist. |
| `.agents/rules/http-api.md` | The OpenAPI contract, generated strict server, aichteeteapee, WebSocket. |
| `.agents/rules/database.md` | SQLite, migrations, models, gorm/gen repositories and queriers. |
| `.agents/rules/web.md` | The SvelteKit control surface: layout, lint, checks, tests, build. |
| `.agents/rules/generated-code.md` | Every generator, its source, and how to regenerate. |
| `.agents/rules/dependencies.md` | Vendored Go modules, pnpm, the age gate, depguard, tools. |
| `.agents/rules/security.md` | Secrets, directory separation, workers, hooks, untrusted input. |
| `.agents/rules/docker.md` | The production and worker image, the dev image, pinning, the entrypoint. |
| `.agents/rules/make.md` | Makefile conventions and the dev container. |
| `.agents/rules/shell.md` | Bash script header, logging, and lint. |
| `.agents/rules/ci.md` | The GitHub workflows and what a change must not break. |
| `.agents/rules/versioning.md` | Semver policy, CHANGELOG entry shape, commits, manifests. |
| `.agents/rules/docs-and-prose.md` | Doc placement, sync, Markdown format, and the prose rules. |
