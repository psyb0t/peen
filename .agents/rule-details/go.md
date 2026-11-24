# Go

## Lint and format

`make format` runs gofumpt, gci, and goimports. `make lint` runs `.golangci.yml`, which enables most linters for test files too, and it must report 0 issues. Lines are at most 80 columns at tab width 4. A suppression names the linter and the reason at the smallest scope: `//nolint:gochecknoinits // Register mappings before database calls.`

## Errors

- Errors are created and wrapped with `github.com/psyb0t/ctxerrors`, with sentinels from `ctxerrors/commerr` or `aichteeteapee` before a package declares its own in `errors.go`.
- `internal/pkg/db/errormap.go` registers `gorm.ErrRecordNotFound` as `commerr.ErrNotFound` and `gorm.ErrDuplicatedKey` as `commerr.ErrAlreadyExists`, so a repository call needs no `errors.Is(err, gorm.ErrRecordNotFound)` branch when translating is all it does.
- `os.Exit`, `log.Fatal`, and `panic` stay in `cmd/`.
- HTTP handlers return errors to the generated strict server, which writes the aichteeteapee envelope. Peen-specific codes live in `internal/pkg/http/server/errorcodes.go`. See `http-api.md`.
- A sentinel used by more than one package lives in the lowest package both already import. Services under `internal/pkg/services/` never import each other.

## Logging

- Logging is `log/slog` configured by `github.com/psyb0t/slogging/slogconf` from `LOG_LEVEL`, `LOG_FORMAT`, and `LOG_ADD_SOURCE`, read through `ctxscope.GetLogger(ctx)`.
- `internal/pkg/auditlog` adds the daily JSON audit sink with `slogconf.AddSink`. A worker's records reach the controller through `internal/pkg/worker/workerlog`. `peen exec` moves every record to stderr with `slogconf.SetOutput`, because its stdout carries frames.
- Session identity goes on the context with `ctxscope.Set(ctx, ctxscope.Attr("session_id", id))`.
- Never log provider keys, `PEEN_API_TOKEN`, worker credentials, Authorization headers, cookies, or full environment dumps, at any level. The HTTP access log runs with query strings off.
- Request and response bodies, message text, and tool output go only into debug records. Debug is for tracing a problem, so it may carry them, and libraries such as commander log each command output line there. Info and higher never carry them.
