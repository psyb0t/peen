# Constants

Every literal that means something gets a name: a status, kind, header, path, event type, limit, timeout, file mode, or error code. Leave a literal raw only when it means nothing beyond its spot, such as a loop bound. `if status == 200` is not that case. Use `http.StatusOK`.

## Where a name lives

1. Used in one file: a `const` block at the top of that file, under the imports.
2. Used by several files in a package: the package's `constants.go`, with sentinels in `errors.go` and enum types in `types.go`. See `internal/pkg/db/constants.go` and `internal/pkg/http/server/constants.go`.
3. Used by several packages: the lowest package they already share, or a new package under `internal/pkg/` named for the concept. A service never imports another service to reach a constant.

Once a constant exists, every use goes through it. Typing the same value again elsewhere in the file is not allowed. Before adding one, check what the libraries already export: aichteeteapee has header names, content types, HTTP defaults, and error codes, and `ctxerrors/commerr` has generic sentinels.

## Typed enums in Go

A field that holds one of a finite set of values gets a type and prefixed constants, `<Type><Value>`:

```go
// BAD
if turn.State == "running" {

// GOOD
type TurnState string

const (
	TurnStateRunning   TurnState = "running"
	TurnStateCompleted TurnState = "completed"
)

if turn.State == models.TurnStateRunning {
```

- Use a defined type (`type TurnState string`) for a domain concept, or when it needs methods such as `IsValid` or `String`. `internal/pkg/db/models/types.go` is the example.
- Use an alias (`type Delivery = string`) for a thin label that is stored and serialized as a plain string with no behavior. `internal/pkg/events/types.go` is the example.
- Integer values that cross a wire or reach the database get explicit numbers, not `iota`.
- The `exhaustive` linter checks switches over these types. List every value instead of hiding new ones behind `default`.

## What stays inline

- Static SQL in a gorm/gen querier comment. See `database.md`.
- Values that come from users, providers, or the environment. Those are data, not constants.
- Contract values defined by `api/api.yml`. Use the generated identifiers in Go and the generated types in TypeScript instead of retyping them.

## TypeScript

The control surface keeps shared values in `web/src/lib/common/constants.ts` as `UPPER_SNAKE_CASE` exports, and finite sets as `as const` arrays. Import them through `$lib/common/constants`.
