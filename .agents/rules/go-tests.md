# Go tests

Tests use `testing` with `testify/require` and `testify/assert`, live in `foo_test.go` next to `foo.go`, and assert observable behavior: return values, HTTP and WebSocket responses, rows in SQLite, files in the workspace, published events. Never read Go source files or grep implementation text as a test. Tests that change process-wide state (`t.Setenv`, the default logger, `ctxerrors` error maps, the working directory) do not call `t.Parallel()`.

## Tiers and build tags

| Where | Tag | Target | What it proves |
| --- | --- | --- | --- |
| `internal/...`, `pkg/...`, `cmd/...` | none | `make test-unit`, `make test` | Package behavior. Tests that start containers need `make test`. |
| `tests/api` | `integration` | `make test-api` | The real image and production wiring over HTTP and WebSocket, with a mocked provider. |
| `tests/executionforms` | `integration` | `make test-execution-forms` | A binary built from source and an installed binary serve the same HTTP contract across a restart. |
| `tests/installer` | `installer` | `make test-installer` | The installer and the `peen` command end to end in Docker. |
| `tests/browser` | `browser` | `make test-browser` | The embedded control surface in a real browser. |
| `tests/dockerworker` | `dockerworker` | `make test-docker-worker-source` | The worker image entrypoint. |
| `tests/real` | `real` | `make test-real` | Live providers. Opt-in, costs money, needs the gitignored `.env`. |

`make test-coverage` runs every untagged and `integration` test and fails below 80 percent. Coverage leaves out `cmd/`, `tests/`, and `*.gen.go`. The targets set `-count=1` and a 30 minute timeout for anything that starts containers, and every Go test target runs with `-race`. Never disable race detection to make a failing test pass.

## Integration harness

- `tests/testinfra` builds the image from `Dockerfile`, starts the controller with testcontainers, and serves a mock provider. Each suite calls `testinfra.Setup` once in `TestMain` and `Teardown` on every exit path. Never boot infrastructure per test.
- Use the real SQLite store, the real migrations, and production constructors. Do not substitute an in-memory or simplified path.
- Docker being unavailable is an environment failure. Never skip a test because of it.
- Readiness waits on the controller's `/ready` endpoint, not on a log line.
- Disposable files for tests that bind host paths go under the gitignored `.testing/`.
