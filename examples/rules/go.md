---
paths:
  - "**/*.go"
---

# Go

Sources: [Effective Go](https://go.dev/doc/effective_go), [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments), and the `gofmt` and `go vet` tools.

Follow the project's own lint configuration, Make targets, and the style of the surrounding code first. Use this rule where the project says nothing.

- Format every file you change with `gofmt`, or `goimports` when the project uses it.
- Run `go vet ./...` and the project's tests before you report the work as done. Add `-race` when the project tests with it.
- Handle every error. Return it with context, such as `fmt.Errorf("read config: %w", err)`, or with the project's own wrapper. Discard an error with `_` only with a comment saying why.
- Write error strings in lowercase without trailing punctuation.
- Pass `context.Context` as the first parameter, named `ctx`. Do not store it in a struct.
- Accept interfaces and return concrete types. Define an interface in the package that uses it.
- Name things in MixedCaps and keep initialisms in one case: `userID`, `HTTPClient`.
- Start the doc comment of an exported name with that name, as a full sentence.
- Give every goroutine a way to stop, and close what you open, usually with `defer`.
- Do not panic for an ordinary error in library code.
- Prefer the standard library. Add a module only when the project already uses it or the task needs it, and update `go.mod` and `go.sum` with the Go tool.
