# Constants

## Where a name lives

- Package-wide values go in the package's `constants.go`, sentinels in `errors.go`, and enum types in `types.go`. See `internal/pkg/db/constants.go` and `internal/pkg/http/server/constants.go`.
- A value several packages use lives in the lowest package they already share. A service never imports another service to reach a constant.
- aichteeteapee already exports header names, content types, HTTP defaults, and error codes, and `ctxerrors/commerr` the generic sentinels.

## Enums

- A defined type (`type TurnState string`) with `<Type><Value>` constants is for a domain concept or one with methods. `internal/pkg/db/models/types.go` is the example.
- An alias (`type Delivery = string`) is for a thin label stored as a plain string with no behavior. `internal/pkg/events/types.go` is the example.
- Integer values that cross a wire or reach the database get explicit numbers, not `iota`.
- The `exhaustive` linter checks switches over these types. List every value.

## What stays inline

- Static SQL in a gorm/gen querier comment. See `database.md`.
- Contract values defined by `api/api.yml` and `api/asyncapi.yaml`. Use the generated identifiers in Go and the generated types in TypeScript.

## TypeScript

The control surface keeps shared values in `web/src/lib/common/constants.ts` as `UPPER_SNAKE_CASE` exports, and finite sets as `as const` arrays. Import them through `$lib/common/constants`.
