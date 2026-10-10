# Go errors

Peen creates and wraps errors with `github.com/psyb0t/ctxerrors`, which records the file, line, and function at each wrap.

## Rules

- Check every error and wrap it on the next line with `ctxerrors.Wrap(err, "what was being done")` or `ctxerrors.Wrapf`. The message names the action, in lowercase, without "failed to".
- For a new failure, wrap the sentinel that names it and add the detail: `ctxerrors.Wrapf(commerr.ErrInvalidArgument, "busy timeout must not be negative, got %s", timeout)`. Callers can then match it with `errors.Is`. Use `ctxerrors.New` only when no caller will ever match the error. Never use `fmt.Errorf` or `errors.New` to build or wrap a runtime error.
- Never discard an error with `_` unless a comment says why that is safe.
- Never call `log.Fatal`, `os.Exit`, or `panic` outside `cmd/`. Return the error.
- Match errors with `errors.Is` and `errors.As`, never by comparing strings.

```go
// BAD
data, _ := os.ReadFile(path)
if err != nil {
	return fmt.Errorf("failed to read: %w", err)
}

// GOOD
data, err := os.ReadFile(path)
if err != nil {
	return ctxerrors.Wrap(err, "read skill file")
}
```

## Sentinels

- A sentinel that callers match with `errors.Is` is declared with `errors.New` in the package's `errors.go`. That is the only place `errors.New` belongs.
- Check `github.com/psyb0t/ctxerrors/commerr` (generic sentinels such as `ErrNotFound`, `ErrAlreadyExists`, `ErrConflict`, `ErrValidationFailed`) and `aichteeteapee` (HTTP status sentinels) before declaring a new one. Do not redeclare what they already provide.
- A sentinel used by more than one package lives in the lowest package both already import. Services under `internal/pkg/services/` never import each other.

## Error mapping

`internal/pkg/db/errormap.go` registers `gorm.ErrRecordNotFound` as `commerr.ErrNotFound` and `gorm.ErrDuplicatedKey` as `commerr.ErrAlreadyExists` with `ctxerrors.MapError`. Every `ctxerrors.Wrap` applies the mapping, so a repository call needs no `errors.Is(err, gorm.ErrRecordNotFound)` branch when translating is all it does. Write the branch only when that case also does something else.

## Errors at the edges

- HTTP handlers return errors to the generated strict server, which writes the aichteeteapee error envelope. Peen-specific codes live in `internal/pkg/http/server/errorcodes.go`. See `http-api.md`.
- Errors reach logs as the `err` field. See `go-logging.md`.
- An error message never contains a credential, a token, or request body content.
