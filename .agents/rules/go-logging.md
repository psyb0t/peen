# Go logging

Peen logs through `log/slog`, configured once by `github.com/psyb0t/slogging/slogconf` from `LOG_LEVEL`, `LOG_FORMAT`, and `LOG_ADD_SOURCE`. `internal/pkg/auditlog` adds the daily JSON audit sink with `slogconf.AddSink`. A worker's records reach the controller through `internal/pkg/worker/workerlog`.

## Getting a logger

- A function that has a `context.Context` gets its logger with `logger := ctxscope.GetLogger(ctx)` from `github.com/psyb0t/ctxscope`.
- Never pass a `*slog.Logger` as a parameter, keep one in a struct or global, or call `slog.Info` and friends directly. They drop the request and session scope.
- Never print diagnostics with `fmt.Print*` or the `log` package.

## Scope

- Put identity that later code needs on the context with `ctxscope.Set(ctx, ctxscope.Attr("session_id", id))`. Every logger taken from that context then carries it.
- Use `logger.With` only for fields that matter to one call path.
- Goroutines take the caller's context so their records keep the scope.

```go
// BAD
func runHook(logger *slog.Logger, hook Hook) {
	logger.Info(fmt.Sprintf("running hook %s", hook.Name))
}

// GOOD
func runHook(ctx context.Context, hook Hook) {
	ctx = ctxscope.Set(ctx, ctxscope.Attr("hook", hook.Name))
	ctxscope.GetLogger(ctx).Info("hook started")
}
```

## Records

- Messages are short, lowercase, and fixed. Values go in fields with stable `lowercase_snake` keys, never interpolated into the message.
- Log an error as the `err` field.
- Debug is diagnostic detail. Info is lifecycle and progress: start, stop, ready, a completed unit of work. Warn is degraded but continuing, with a stable `reason` field when the code skipped, fell back, or retried. Error is an operation that failed or aborted.
- For a loop over items, log each skipped or failed item at Debug or Warn and finish with one Info summary of the counts.
- Short calls stay on one line. A long call puts each key and value pair on its own line with a trailing comma.

## Never log

Provider keys, `PEEN_API_TOKEN`, worker credentials, Authorization headers, cookies, request or response bodies, message text, tool output, or full environment dumps. The HTTP access log runs with query strings off. When a value may hold a secret, log that it was present, not what it was.

## Tests

Tests that capture logs swap the default handler, restore it with `t.Cleanup`, and do not run in parallel. Assert behavior first. Assert a log record only when the record is the contract, such as an audit entry.
