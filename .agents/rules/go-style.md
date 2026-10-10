# Go style

gofumpt, gci, and goimports format the code through `make format` and `make lint`. These rules cover what a formatter cannot decide. `.golangci.yml` enables most linters, test files included, and `make lint` must be clean.

## Control flow

- Handle errors, invalid input, missing data, and refusals first with guard clauses, then return. Keep the main path unindented.
- In loops, skip irrelevant items with `continue`.
- Drop `else` when the first branch returns, continues, or breaks. Keep it only for two equal alternatives that both fall through.
- When a condition stops reading plainly, name it: extract a predicate or a helper.

```go
// BAD
if session != nil {
	if session.State == models.TurnStateRunning {
		return busy()
	} else {
		return start(session)
	}
}
return errSessionMissing

// GOOD
if session == nil {
	return errSessionMissing
}
if session.State == models.TurnStateRunning {
	return busy()
}

return start(session)
```

## Lines and signatures

- Lines are at most 80 columns (`lll`, tab width 4). Break at natural units: one argument, chain step, boolean operand, or struct field per line, with a trailing comma.
- Keep up to two short parameters on one line. Longer lists go one per line. Adjacent parameters of one type may share it (`id, ownerID string`).
- Do not split one logical string with `+` to satisfy line length. Use a raw string when runtime newlines are correct, or one line with a justified `//nolint:lll` when they are not.
- Build structured text with a template or `fmt` verbs, not repeated `WriteString` or `+=`.

## Naming

- Keep the whole domain concept in a name. A wider scope needs a more self-contained name.
- Booleans read as assertions: `isReady`, `hasPendingCalls`.
- Use Go initialism casing: `sessionID`, `apiToken`, `HTTPServer`.
- Keep the conventional short names (`ctx`, `err`, `ok`, `i`, `w`, `r`, `t`, `tx`, `mu`, `wg`) where the scope is obvious. Avoid private abbreviations such as `cfgSvcMgr`.

## Comments

- Say what the code does or why it must be this way: a constraint, an invariant the types cannot express, a surprising behavior, an upstream workaround, a security boundary.
- Delete comments that restate the next line, section dividers, commented-out code, and notes about what you chose not to do.
- Doc comments on exported symbols describe behavior, errors, side effects, and invariants, not the symbol's name.
- Comments follow `docs-and-prose.md`: plain, exact words.

## Lint suppressions

Fix what the linter reports, including warnings your change caused in other packages. When a suppression is the right call, name the linter and give the specific reason, at the smallest scope:

```go
//nolint:gochecknoinits // Register mappings before database calls.
func init() {
```

A bare `//nolint` or one without a reason is not accepted.

## Concurrency and resources

- A goroutine gets the caller's context, never `context.Background()` in the middle of a request or turn.
- A goroutine recovers panics and logs or returns its error. Nothing runs fire-and-forget.
- Close resources in a `defer` that checks the error and logs it.
