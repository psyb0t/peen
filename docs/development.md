# Developing Peen

This page is for changing Peen itself. To run an agent against a project, start
with [Getting started](getting-started.md) instead.

## Tooling

The Make targets run Go tooling in the project's Docker development image. Use
them instead of building a separate host toolchain. Start with `make help` when
you need the full, current list.

| Task | Command |
| --- | --- |
| Format Go and shell files | `make format` |
| Run static checks | `make lint` |
| Run package tests | `make test-unit` |
| Run mocked integration tests | `make test-integration` |
| Repeat the fast command and job tests 200 times to catch output races | `make test-stress` |
| Exercise production HTTP and WebSocket wiring | `make test-api` |
| Exercise the embedded control surface in a real browser | `make test-browser` |
| Exercise source and installed binaries | `make test-execution-forms` |
| Build the binary | `make build` |
| Build the production image | `make docker-build` |
| Run this checkout's local controller | `make run-dev` |

Make runs these targets in dev containers capped with `--cpus` to all but two CPU threads, and to at least one. Set `DEV_CPUS` to change the cap, for example `make test-coverage DEV_CPUS=4`. The cap does not apply to `make run-dev` or to containers that tests start themselves.

`make test-stress` runs the fast command and job tests in `internal/pkg/tools` 200 times with `go test -cpu 1,4`, which sets `GOMAXPROCS` to 1 and then 4. It does not give the tests more CPUs than the `DEV_CPUS` cap allows. It skips the tests that depend on timing, and CI does not run it. Run it after changing job or command code in `internal/pkg/tools` or updating commander.

`make run-dev` rebuilds the development image, runs Peen as the current host UID and GID, loads the gitignored `.env` through Docker, and uses the invoking directory as the workspace root, so a client may open it or any directory inside it. It uses Docker host networking so configured host and Tailnet services remain reachable, while Peen itself binds its API and browser control surface to `http://localhost:8080`. The default metrics listener is also host-loopback at `http://127.0.0.1:9090/metrics`. The target creates `data/peen/config` and `data/peen/state` in the Peen checkout on first use, and preserves state between runs. The config directory is read-only to the agent. Set `PEEN_DEV_HTTP_PORT` to use another loopback API port.

The target does not grant Docker socket access, so the default agent space is `control`. Set `PEEN_DEV_DOCKER_SOCKET=1 make run-dev` only when testing a Docker agent space. That grants the controller host-root-equivalent Docker authority, and Peen then refuses the `control` agent space and defaults to `docker:default`.

`make test-real` is opt-in. It uses the configured live provider and costs
money. It runs the agent in an isolated fixture with no Docker socket. Do not
use it as a routine local check.

`make test-browser` runs the embedded control surface against the production
test controller in a pinned Stealthy browser container. It records safe browser
console diagnostics, network metadata, and a screenshot under the gitignored
`.testing/browser-artifacts/` directory. The browser receives no workspace
mount or Docker socket.

## Where changes go

| Area | Location |
| --- | --- |
| Agent turns, providers, skills, and child agents | `internal/pkg/agent/` |
| Harness discovery and layer resolution | `internal/pkg/harness/` |
| Hook execution | `internal/pkg/hooks/` |
| File and command tools | `internal/pkg/tools/` |
| Durable session store | `internal/pkg/session/` and `internal/pkg/db/` |
| REST and WebSocket transport | `internal/pkg/http/` |
| Runtime configuration | `internal/pkg/config/` and `.env.example` |
| Public API contract | `api/api.yml` |
| Black-box API tests | `tests/api/` |

Keep generated files generated. `internal/pkg/http/api/api.gen.go`, repository `*.gen.go` files,
and service registration come from their generators. Change their source and
run the matching generation target instead of patching generated output.

## Change with the boundary in mind

The WebSocket owns live messages. REST owns durable reads and control. The
agent runtime owns turn behavior. The harness owns what enters model context.
Tools own filesystem and process semantics. Keep a change in the smallest area
that owns it, then test through the public boundary that can see it.

Peen uses [Servicepack](https://github.com/psyb0t/servicepack) for application
lifecycle plumbing. Read its repository when changing framework behavior.
