---
title: "Developing Peen"
description: "Change Peen itself: repository layout, Make targets, test tiers, generated code, keeping the docs in sync, and releases."
nav_order: 20
---

# Developing Peen

Peen is a durable coding-agent service written in Go. One controller owns SQLite, the REST and WebSocket API, and the embedded browser control surface, and it starts one worker process or container per session to run that session's turns. Every message, tool call, event, and provider exchange is stored, so a session survives restarts.

This page is for changing Peen itself. To run an agent against a project, start with [Getting started](getting-started.md) instead. The [rules for every change](https://github.com/psyb0t/peen/blob/main/.agents/rules/core.md) and the [area rules](https://github.com/psyb0t/peen/tree/main/.agents/rule-details/) hold the details. When this page and a rule disagree, the rule is more specific and wins.

## Repository layout

| Path | What it holds |
| --- | --- |
| [`api/api.yml`](https://github.com/psyb0t/peen/blob/main/api/api.yml) | The OpenAPI contract. Source for the Go server, the Go client, and the web client types. [`api/spec.go`](https://github.com/psyb0t/peen/blob/main/api/spec.go) embeds it for request validation. It is hand-written today and moves to a spec generated from Go in `cmd/apigen`; see `.agents/rule-details/http-api.md`. |
| [`api/asyncapi.yaml`](https://github.com/psyb0t/peen/blob/main/api/asyncapi.yaml) | The WebSocket contract. Source for the Go frame types in [`internal/pkg/http/wsapi`](https://github.com/psyb0t/peen/tree/main/internal/pkg/http/wsapi) and the web client's frame types. |
| [`cmd/`](https://github.com/psyb0t/peen/tree/main/cmd) | Entry point. `main.go` is Servicepack's, `init.go` and `commands.go` are Peen's, `repogen/` is the gorm/gen generator, `envexample/` writes [`.env.example`](https://github.com/psyb0t/peen/blob/main/.env.example) from the env file template, `benchrelay/` is the model relay the benchmark task containers run, and `benchdriver/` runs the harness suite's scenario tasks inside them. |
| `bench/` | The benchmark runner: the Harbor driver image, the Peen agent adapter, the provider proxy, the job writer, the task sandbox, Peen's behavior eval tasks in `eval/tasks/`, and the harness suite in `harness/`, with its tasks, the grader library, and the audit, report, and compare tools. See [Benchmarks](benchmarks.md#running-them). |
| [`examples/rules/`](https://github.com/psyb0t/peen/tree/main/examples/rules) | The starter rule pack users copy into their harness. A test parses every file. See [AGENTS.md and rules](rules.md#a-starter-rule-pack). |
| [`examples/embedding/`](https://github.com/psyb0t/peen/tree/main/examples/embedding) | A runnable program that embeds [`pkg/peen`](https://github.com/psyb0t/peen/tree/main/pkg/peen) and runs one turn. `make lint` compiles it, and coverage leaves it out. |
| [`internal/app/`](https://github.com/psyb0t/peen/tree/main/internal/app) | Servicepack's application lifecycle. Not edited here. |
| [`internal/pkg/agent/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/agent) | Agent runtime: turns, provider calls, tools wiring, skills, child agents, compaction. |
| [`internal/pkg/harness/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/harness) | Harness discovery and layering: `AGENTS.md`, rules, skills, agents, event handlers, hooks. `builtin/` is the embedded baseline shipped to every user. |
| [`internal/pkg/hooks/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/hooks) | Hook validation and execution. |
| [`internal/pkg/tools/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/tools) | File, patch, and command tools, and background jobs. |
| [`internal/pkg/session/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/session) | Durable, transport-neutral session transcript. |
| [`internal/pkg/db/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/db) | SQLite open, embedded migrations, gorm models, generated repositories. |
| [`internal/pkg/http/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/http) | [`api/`](https://github.com/psyb0t/peen/tree/main/api) is generated server code. `server/` holds handlers, middleware, WebSocket, and the embedded web build in `server/web/dist`. |
| [`internal/pkg/control/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/control) | Workspace policy, the session registry, the conversation runner the OpenAI API and the MCP control server share, the workspace file API, and the controller side of cron and Telegram. |
| [`internal/pkg/worker/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/worker) | Controller to worker boundary: protocol, supervisor, control and Docker launchers, agent spaces, the `docker: readonly` proxy in `dockerproxy/`, the worker command, worker logs. |
| [`internal/pkg/cronconfig/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/cronconfig), `cronscheduler/`, `cronstore/` | Scheduled turns: the `cron.yaml` loader, the scheduler that runs jobs through session workers, and the durable schedule baselines and run claims. See [Scheduled turns](cron.md). |
| [`internal/pkg/telegram/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/telegram), `telegrambot/`, `telegramstore/` | The Telegram Bot API client, the bot that routes allowlisted chats to regular workers, and the chat and notification bindings. See [Telegram bot](telegram.md). |
| [`internal/pkg/mcpserver/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/mcpserver) | The tools of the `/mcp/v1` control server. See [MCP control server](mcp-server.md). |
| [`internal/pkg/mcptools/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/mcptools) | The MCP client that connects a session's worker to the MCP servers the operator configures. See [MCP tool servers](mcp.md). |
| [`internal/pkg/openaicompat/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/openaicompat) | The `/openai/v1` Chat Completions translation. See [OpenAI API](openai-api.md). |
| [`internal/pkg/events/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/events), `metrics/`, `metricsstore/`, `auditlog/`, `config/` | Session events, Prometheus metrics and worker reports, the `metrics.db` writer, the JSON audit sink, and `PEEN_` configuration. The `.env.example` template is in [`internal/pkg/config/envtemplate/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/config/envtemplate). |
| [`internal/pkg/services/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/services) | Servicepack services `control-core`, `control-api`, `control-cron`, and `control-telegram`. `services.gen.go` is generated. |
| [`internal/pkg/service-manager/`](https://github.com/psyb0t/peen/tree/main/internal/pkg/service-manager), [`pkg/runner/`](https://github.com/psyb0t/peen/tree/main/pkg/runner) | Servicepack-owned. |
| [`pkg/peen/`](https://github.com/psyb0t/peen/tree/main/pkg/peen) | The stable public Go API for running Peen in-process. |
| [`pkg/http/api/client/`](https://github.com/psyb0t/peen/tree/main/pkg/http/api/client) | Generated public Go HTTP client. |
| [`web/`](https://github.com/psyb0t/peen/tree/main/web) | SvelteKit control surface source. |
| [`tests/`](https://github.com/psyb0t/peen/tree/main/tests) | Black-box suites: `api`, `browser`, `executionforms`, `installer`, `dockerworker`, `real`, and the shared `testinfra` harness. |
| [`docker/entrypoint.sh`](https://github.com/psyb0t/peen/blob/main/docker/entrypoint.sh), [`Dockerfile`](https://github.com/psyb0t/peen/blob/main/Dockerfile), `Dockerfile.dev` | The production and worker image, its entrypoint, and the dev toolchain image. |
| [`install.sh`](https://github.com/psyb0t/peen/blob/main/install.sh), [`scripts/peen.sh`](https://github.com/psyb0t/peen/blob/main/scripts/peen.sh) | The installer users pipe from curl, and the `peen` command it installs. |
| [`scripts/`](https://github.com/psyb0t/peen/tree/main/scripts) | Make helpers. [`scripts/make/servicepack/`](https://github.com/psyb0t/peen/tree/main/scripts/make/servicepack) is Servicepack-owned. |
| `docs/` | User and operator documentation, the source of the documentation site. |
| [`website/`](https://github.com/psyb0t/peen/tree/main/website) | The Docusaurus project that builds `docs/` into the site. It holds no pages. See [the documentation site](#the-documentation-site). |

[`internal/pkg/harness/builtin/AGENTS.md`](https://github.com/psyb0t/peen/blob/main/internal/pkg/harness/builtin/AGENTS.md) is product text: Peen's embedded operating rules for every user. When Peen works on this repository it also loads that file as a nested `AGENTS.md`. Treat it as content you edit, not as instructions for that directory.

## Change with the boundary in mind

The WebSocket owns live messages. REST owns stored reads and control. The agent runtime owns turn behavior. The harness owns what enters model context. Tools own filesystem and process behavior. Keep a change in the smallest area that owns it, then test through the public boundary that can see it.

The supported Go API is documented in [the embedding guide](embedding.md). Its lifecycle, sessions, extension points and virtual filesystems use the same agent engine and SQLite schema as the controller. Change the owned public types and their boundary conversions together; keep generated HTTP types behind that boundary. Filesystem tools and harness discovery share [`internal/pkg/workspacefs`](https://github.com/psyb0t/peen/tree/main/internal/pkg/workspacefs), while runtime storage and hook state remain host files.

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
| Build the documentation site into `website/build` | `make docs-build` |
| Refresh the documentation site lockfile, or scan its packages for advisories | `make docs-lock`, `make docs-audit` |
| Build the binary into `build/peen` | `make build` |
| Build the production image | `make docker-build` |
| Run this checkout's local controller | `make run-dev` |
| Run the opt-in live provider tests, which cost money | `make test-real` |
| Run Aider Polyglot or LHTB against a live provider in a networkless sandbox, which costs money | `make bench-aider`, `make bench-lhtb`, after the setup in [Benchmarks](benchmarks.md#running-them) |
| Score a change to the system prompt, the built-in `AGENTS.md`, or a tool description, which costs money | `make bench-eval` before and after the change, see [the behavior eval](benchmarks.md#the-behavior-eval) |
| Test Peen's harness, recovery, and provider handling with a live model, which costs money | `make bench-harness`, and `make bench-compare` to compare two builds, see [the harness suite](benchmarks.md#the-harness-suite) |
| Test the benchmark proxy and the harness audit tools | `make bench-selftest` |

Pick the tier that can see your change. A change inside one package needs `make test-unit` while you work. A change to HTTP, WebSocket, or wiring needs `make test-api`. A control surface change needs the web targets, `make web-build`, and `make test-browser`. A Dockerfile or entrypoint change needs `make test-docker-worker-source`. A change is done when `make lint` is clean and the tier that can see it passes. `pre-commit.sh` and CI run `make lint` and `make test-coverage`, and every release needs `make test-coverage`. Every Go test target runs with `-race`. Fix reported races instead of disabling the detector.

Make runs these targets in dev containers. To cap them with `--cpus`, copy `.env.dev.example` to `.env.dev` and set `DEV_CPUS` there; without `.env.dev` the containers run uncapped. `DEV_CPUS` on the command line overrides the file, for example `make test-coverage DEV_CPUS=4`. Keep the value at or below the machine's CPU count, or Docker refuses to start the container. The cap does not apply to `make run-dev` or to containers that tests start themselves. Run one of these targets at a time, never several in parallel and never from several agents at once. Each one is a full Go compile or image build, and several at once overload the machine.

`make test-installer` builds this checkout's image, pushes it to a local registry under three release tags, one of them as a multi-platform index, and runs `install.sh` and the `peen` command against it with a mock model provider. It covers the four control space and agent space combinations, upgrade, uninstall, the system install, and the refusals. Every home and workspace it creates lives under the gitignored `.testing/`, and it removes only the containers and images it created.

`make test-stress` runs the fast command and job tests in [`internal/pkg/tools`](https://github.com/psyb0t/peen/tree/main/internal/pkg/tools) 200 times with `go test -cpu 1,4`, which sets `GOMAXPROCS` to 1 and then 4. It does not give the tests more CPUs than the `DEV_CPUS` cap allows. It skips the tests that depend on timing, and CI does not run it. Run it after changing job or command code in `internal/pkg/tools` or updating commander.

`make run-dev` needs a `.env` in the checkout. Copy `.env.example` to `.env` and fill in a provider first. The target rebuilds the development image, runs Peen as your host UID and GID, loads `.env` through Docker, and uses the directory you ran it from as the workspace root, so a client may open it or any directory inside it. It uses Docker host networking, so host and Tailnet services stay reachable, and Peen binds its API and browser control surface to `http://localhost:8080`. The metrics listener is on the host's loopback at `http://127.0.0.1:9090/metrics`. The target creates `data/peen/config` and `data/peen/state` in the checkout on first use and keeps the state between runs. The config directory is read-only to the agent. Set `PEEN_DEV_HTTP_PORT` to use another loopback port.

The target gives Peen no Docker socket, so the default agent space is `control`. Set `PEEN_DEV_DOCKER_SOCKET=1 make run-dev` only when testing a Docker agent space. That gives the controller Docker access, which is host root, and Peen then refuses the `control` agent space and defaults to `docker:default`.

`make test-real` uses the configured live provider and costs money. It runs the agent in an isolated fixture with no Docker socket. Do not use it as a routine local check.

`make test-browser` runs the embedded control surface against the production test controller in a pinned Stealthy browser container. It records browser console diagnostics that are safe to keep, network metadata, and screenshots under the gitignored `.testing/browser-artifacts/` directory. The browser receives no workspace mount or Docker socket.

## Generated code

Never hand-edit generated output. Change the source, regenerate, and commit both together.

- `make generate` runs `go generate ./...`, which rebuilds the gorm/gen repositories, the oapi-codegen server and client, the WebSocket frame types, [`web/src/lib/api/generated.ts`](https://github.com/psyb0t/peen/blob/main/web/src/lib/api/generated.ts), [`web/src/lib/ws/generated.ts`](https://github.com/psyb0t/peen/blob/main/web/src/lib/ws/generated.ts), and `.env.example`.
- `make web-build` rebuilds [`internal/pkg/http/server/web/dist`](https://github.com/psyb0t/peen/tree/main/internal/pkg/http/server/web/dist), the committed and embedded control surface.
- `make service-registration` rebuilds [`internal/pkg/services/services.gen.go`](https://github.com/psyb0t/peen/blob/main/internal/pkg/services/services.gen.go).

See `.agents/rule-details/generated-code.md`.

## Servicepack-owned files

Peen is built on [Servicepack](https://github.com/psyb0t/servicepack), pinned in `servicepack.version`. A framework update overwrites these, so do not edit them by hand: `Makefile.servicepack`, `scripts/make/servicepack/`, `Dockerfile.servicepack`, `Dockerfile.servicepack.dev`, `servicepack.version`, [`cmd/main.go`](https://github.com/psyb0t/peen/blob/main/cmd/main.go), `internal/app/`, `internal/pkg/service-manager/`, and `pkg/runner/`. Override a framework target in [`Makefile`](https://github.com/psyb0t/peen/blob/main/Makefile) instead. Peen owns `Makefile`, `Dockerfile`, `Dockerfile.dev`, `.golangci.yml`, [`cmd/init.go`](https://github.com/psyb0t/peen/blob/main/cmd/init.go), [`cmd/commands.go`](https://github.com/psyb0t/peen/blob/main/cmd/commands.go), `.github/workflows/pipeline.yml`, `.dockerignore`, [`.gitleaks.toml`](https://github.com/psyb0t/peen/blob/main/.gitleaks.toml), `docs/`, `tests/`, and everything listed in `.servicepackupdateignore`. Framework updates arrive through `make servicepack-update` and are reviewed before merge.

## Keep docs in sync

A change that alters behavior, configuration, the API, events, the harness, or deployment updates its documentation in the same change:

- `docs/*.md` for the area, and `README.md` when the quick start or an overview claim changes;
- `.env.example` and `docs/configuration.md` for every `PEEN_` variable;
- `api/api.yml` for a REST operation, including its tag, descriptions, and curl example, since the site renders it as the REST reference; `docs/http-api.md` only when a rule every operation shares changes;
- `api/asyncapi.yaml` and `docs/websocket-api.md` for the WebSocket contract, and `docs/events.md` for event types;
- `docs/tools.md` for a tool the agent can call;
- `docs/troubleshooting.md` for a new startup refusal or API error code;
- `docs/embedding.md` for the public Go API;
- `.agents/skills/peen/` when the published agent skill's setup or workflow changes;
- [`CHANGELOG.md`](https://github.com/psyb0t/peen/blob/main/CHANGELOG.md) under the release entry the change ships in.

## The documentation site

GitHub Pages publishes `docs/` at <https://psyb0t.github.io/peen/>, built with Docusaurus from the project in `website/`. Pages stay plain CommonMark, not MDX, so they read the same on GitHub. Every page needs `title`, `description`, and `nav_order` front matter, and a child page sets `parent` to its parent's exact title. `make docs-build` runs [`scripts/make/docs.sh`](https://github.com/psyb0t/peen/blob/main/scripts/make/docs.sh), which prepares the pages with [`scripts/make/docs_prepare.py`](https://github.com/psyb0t/peen/blob/main/scripts/make/docs_prepare.py), builds in a scratch copy, and writes the site to `website/build`. The prepare step refuses bad front matter, a description outside 50 to 170 characters, a repeated title or description, and Liquid syntax, and the build fails on a broken link or anchor. The REST reference pages under `/api/` are generated from `api/api.yml` by `docusaurus-plugin-openapi-docs`. Docs link them with full site URLs under `/peen/api/`, and [`scripts/make/docs_check_site_links.py`](https://github.com/psyb0t/peen/blob/main/scripts/make/docs_check_site_links.py) fails the build when such a link names a page or anchor the build does not have. [`.github/workflows/docs.yml`](https://github.com/psyb0t/peen/blob/main/.github/workflows/docs.yml) runs the same target and deploys the site from `main`.

Link another page with its relative `.md` path, so the link works on GitHub and on the site. Link a file outside `docs/` with its full GitHub URL, because the site does not contain it. [`.agents/rule-details/docs-and-prose.md`](https://github.com/psyb0t/peen/blob/main/.agents/rule-details/docs-and-prose.md) has the full page rules, and [`.agents/rule-details/dependencies.md`](https://github.com/psyb0t/peen/blob/main/.agents/rule-details/dependencies.md) covers the site's packages and `make docs-lock`.

## CHANGELOG, commits, and releases

Each release is `## vX.Y.Z (YYYY-MM-DD)`, then one paragraph that says what changed for a user and what they must do before upgrading, or "No configuration or API migration is needed.". Bullets follow, each one change, starting with a verb: "Fixes", "Adds", "Changes", "Removes", "Updates". Breaking changes come first and start with "Breaking:", with the migration. Describe what a user sees, not how the code changed.

Commit subjects use conventional commits in the imperative, lowercase: `fix: ...`, `feat: ...`, `docs: ...`, and `feat!:` for a breaking change. Text that speaks for the maintainer uses "I", never "we" or "the team". Releases are tagged by the maintainer, and the tag is the version. External pull requests are closed automatically, so report problems as issues. See `.agents/rule-details/versioning.md`.

## Prose

Write plain, exact, active sentences. No filler words such as leverage, utilize, robust, seamless, or crucial. Sentence-case headings. In Markdown, keep each paragraph on one source line. This applies to docs, comments, the CHANGELOG, commit messages, and the rule files.

## Rule files

[`.agents/rules/core.md`](https://github.com/psyb0t/peen/blob/main/.agents/rules/core.md) holds the rules for every change, and every agent session loads it. Area rules live in [`.agents/rule-details/`](https://github.com/psyb0t/peen/tree/main/.agents/rule-details/) and are read before touching their area. The table in [`AGENTS.md`](https://github.com/psyb0t/peen/blob/main/AGENTS.md) maps each area rule to the paths it covers, and [`.agents/rule-gate.json`](https://github.com/psyb0t/peen/blob/main/.agents/rule-gate.json) maps the same paths for tools that enforce the reads.
