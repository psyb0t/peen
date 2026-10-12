# Developing Peen

This page is for changing Peen itself. To run an agent against a project, start with [Getting started](getting-started.md) instead. The [contributor guide](contributing.md) and [topic rules](../.agents/rules/) cover code style and when each test target applies.

## Tooling

The Make targets run Go, Node, and the linters in the project's Docker development image, so the host needs only Docker, Make, Git, and a shell. Run `make help` for the full, current list.

| Task | Command |
| --- | --- |
| Format Go and shell files | `make format` |
| Run static checks | `make lint` |
| Run package tests without Docker access | `make test-unit` |
| Run package tests with Docker access, for tests that start containers | `make test-integration` |
| Run the whole suite with the coverage floor, as CI does | `make test-coverage` |
| Repeat the fast command and job tests 200 times to catch output races | `make test-stress` |
| Exercise production HTTP and WebSocket wiring | `make test-api` |
| Exercise the embedded control surface in a real browser | `make test-browser` |
| Exercise source and installed binaries | `make test-execution-forms` |
| Run `install.sh` and the `peen` command end to end | `make test-installer` |
| Probe the worker image entrypoint and `docker: readonly` against real Docker | `make test-docker-worker-source` |
| Regenerate the API code, repositories, web types, and `.env.example` | `make generate` |
| Lint, type-check, test, and build the control surface | `make web-lint`, `make web-check`, `make web-test`, `make web-build` |
| Build the binary | `make build` |
| Build the production image | `make docker-build` |
| Run this checkout's local controller | `make run-dev` |

Make runs these targets in dev containers capped with `--cpus` to 6 CPUs. Set `DEV_CPUS` to change the cap, for example `make test-coverage DEV_CPUS=4`. The cap does not apply to `make run-dev` or to containers that tests start themselves. Run one of these targets at a time. Each one is a full Go compile or image build, and several at once overload the machine.

`make test-installer` builds this checkout's image, pushes it to a local registry under three release tags, one of them as a multi-platform index, and runs `install.sh` and the `peen` command against it with a mock model provider. It covers the four control space and agent space combinations, upgrade, uninstall, the system install, and the refusals. Every home and workspace it creates lives under the gitignored `.testing/`, and it removes only the containers and images it created.

`make test-stress` runs the fast command and job tests in `internal/pkg/tools` 200 times with `go test -cpu 1,4`, which sets `GOMAXPROCS` to 1 and then 4. It does not give the tests more CPUs than the `DEV_CPUS` cap allows. It skips the tests that depend on timing, and CI does not run it. Run it after changing job or command code in `internal/pkg/tools` or updating commander.

`make run-dev` needs a `.env` in the checkout. Copy `.env.example` to `.env` and fill in a provider first. The target rebuilds the development image, runs Peen as your host UID and GID, loads `.env` through Docker, and uses the directory you ran it from as the workspace root, so a client may open it or any directory inside it. It uses Docker host networking, so host and Tailnet services stay reachable, and Peen binds its API and browser control surface to `http://localhost:8080`. The metrics listener is on the host's loopback at `http://127.0.0.1:9090/metrics`. The target creates `data/peen/config` and `data/peen/state` in the checkout on first use and keeps the state between runs. The config directory is read-only to the agent. Set `PEEN_DEV_HTTP_PORT` to use another loopback port.

The target gives Peen no Docker socket, so the default agent space is `control`. Set `PEEN_DEV_DOCKER_SOCKET=1 make run-dev` only when testing a Docker agent space. That gives the controller Docker access, which is host root, and Peen then refuses the `control` agent space and defaults to `docker:default`.

`make test-real` is opt-in. It uses the configured live provider and costs money. It runs the agent in an isolated fixture with no Docker socket. Do not use it as a routine local check.

`make test-browser` runs the embedded control surface against the production test controller in a pinned Stealthy browser container. It records browser console diagnostics that are safe to keep, network metadata, and screenshots under the gitignored `.testing/browser-artifacts/` directory. The browser receives no workspace mount or Docker socket.

## Where changes go

| Area | Location |
| --- | --- |
| Agent turns, providers, skills, and child agents | `internal/pkg/agent/` |
| Harness discovery and layer resolution | `internal/pkg/harness/` |
| Hook execution | `internal/pkg/hooks/` |
| File and command tools | `internal/pkg/tools/` |
| Stored session data | `internal/pkg/session/` and `internal/pkg/db/` |
| REST and WebSocket transport | `internal/pkg/http/` |
| Workers, agent spaces, and the `docker: readonly` proxy | `internal/pkg/worker/` |
| Runtime configuration | `internal/pkg/config/`, with the `.env.example` template in `internal/pkg/config/envtemplate/` |
| The installer and the `peen` command | `install.sh` and `scripts/peen.sh` |
| Public API contract | `api/api.yml` |
| Black-box tests | `tests/` |

Never edit generated files by hand. `internal/pkg/http/api/api.gen.go`, the repository `*.gen.go` files, the web API types, `.env.example`, and the service registration come from their generators. Change their source and run `make generate`, or `make service-registration` for the services.

## Change with the boundary in mind

The WebSocket owns live messages. REST owns stored reads and control. The agent runtime owns turn behavior. The harness owns what enters model context. Tools own filesystem and process behavior. Keep a change in the smallest area that owns it, then test through the public boundary that can see it.

Peen uses [Servicepack](https://github.com/psyb0t/servicepack) for application lifecycle. Read its repository when changing framework behavior.

## Embedding changes

The supported Go surface is documented in [the embedding guide](../pkg/peen/README.md). Its lifecycle, sessions, extension points and virtual filesystems use the same agent engine and SQLite schema as the controller. Change the owned public types and their boundary conversions together; keep generated HTTP types behind that boundary. Filesystem tools and harness discovery share `internal/pkg/workspacefs`, while runtime storage and hook state remain host files.
