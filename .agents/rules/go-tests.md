# Go tests

Every change is tested. A bug fix starts with a test that fails because of the bug, then passes with the fix. A new branch, error path, limit, or contract gets its own case.

## Shape

- Use the standard `testing` package with `testify/require` and `testify/assert`. No other test framework.
- `require` when a failure makes the rest of the test meaningless. `assert` for independent checks you want reported together.
- The tests for `foo.go` live in `foo_test.go` in the same package, so they can reach unexported code. Do not invent other file names. If a test file grows unwieldy, split the source file first.
- Table tests use `testCases` and `tc`, and are for cases with the same assertion shape. Cases with different assertions get separate named `t.Run` blocks.
- Call `t.Parallel()` in independent tests and subtests. Never in a test that changes process-wide state: `t.Setenv`, the default logger, `ctxerrors` error maps, the working directory.
- Helpers call `t.Helper()` first. Shared resources are released with `t.Cleanup`.
- Wait with channels, `require.Eventually`, or `assert.Eventually`, not `time.Sleep`.
- Match errors with `require.ErrorIs` and `require.ErrorAs`, not by message text.

```go
func TestParseDelivery(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name    string
		input   string
		want    Delivery
		wantErr error
	}{
		{name: "empty", wantErr: commerr.ErrValidationFailed},
		{name: "wake", input: "wake", want: DeliveryWake},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseDelivery(tc.input)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, tc.want, got)
		})
	}
}
```

## Test behavior, not source

Assert what a caller can observe: return values, HTTP and WebSocket responses, rows in SQLite, files in the workspace, published events. Never read Go source files, grep implementation text, or snapshot code as a test. A prose log line is not the behavior under test unless the record itself is the contract.

## Tiers and build tags

| Where | Tag | Target | What it proves |
| --- | --- | --- | --- |
| `internal/...`, `pkg/...`, `cmd/...` | none | `make test-unit`, `make test` | Package behavior. Tests that start containers need `make test`. |
| `tests/api` | `integration` | `make test-api` | The real image and production wiring over HTTP and WebSocket, with a mocked provider. |
| `tests/executionforms` | `integration` | `make test-execution-forms` | A binary built from source and an installed binary serve the same HTTP contract across a restart. |
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
