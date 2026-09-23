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
| Exercise production HTTP and WebSocket wiring | `make test-api` |
| Exercise the embedded control surface in a real browser | `make test-browser` |
| Exercise source and installed binaries | `make test-execution-forms` |
| Build the binary | `make build` |
| Build the production image | `make docker-build` |

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

Keep generated files generated. `api/api.gen.go`, repository `*.gen.go` files,
and service registration come from their generators. Change their source and
run the matching generation target instead of patching generated output.

## Change with the boundary in mind

The WebSocket owns live messages. REST owns durable reads and control. The
agent runtime owns turn behavior. The harness owns what enters model context.
Tools own filesystem and process semantics. Keep a change in the smallest area
that owns it, then test through the public boundary that can see it.

Peen uses [Servicepack](https://github.com/psyb0t/servicepack) for application
lifecycle plumbing. Read its repository when changing framework behavior.
