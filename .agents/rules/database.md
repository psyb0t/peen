# Database

Peen stores everything in one SQLite file, `peen.db` under `PEEN_STATE_DIR`, through gorm with the pure-Go `github.com/glebarez/sqlite` driver. Only the controller opens it. Workers reach durable state through the controller.

## Layout

| Path | Role |
| --- | --- |
| `internal/pkg/db/migrations/sqlite/*.sql` | The schema. Embedded by `migrations.go` and applied on open. |
| `internal/pkg/db/models/` | One plain struct per table, with `TableName()`. Enum types in `types.go`. |
| `cmd/repogen/` | The gorm/gen driver. `main.go` lists every model, `<table>_querier.go` holds custom SQL. |
| `internal/pkg/db/repositories/` | Generated typed repositories. Never edited. |
| `internal/pkg/db/` | Open, pragmas, integrity check, metrics, and the gorm error map. |

## Changing the schema

1. Add a migration pair under `migrations/sqlite/`: `<7-digit sequence>_<action>_<exact_table>_table[_details].up.sql` and the matching `.down.sql`, such as `0000015_alter_messages_table_add_injected`. The down file reverses the up file exactly.
2. Never edit a migration that has been committed. Fix it with a new one.
3. Update the model in `internal/pkg/db/models/`. Add a new model to `ApplyBasic` in `cmd/repogen/main.go`, in alphabetical order.
4. Run `make generate`.
5. Bump `sqliteMigrationCount` in `internal/pkg/db/migrations_test.go`. That test migrates every step down and back up.

The migrations own the schema: types, defaults, `CHECK` constraints, foreign keys, and indexes. Peen never calls `AutoMigrate`. Models carry no schema tags. The only gorm tags allowed on a model are `column:` when the field name does not map to the column, `primaryKey` when gorm cannot infer the key, and `-` for a non-column field.

Migrations run through `github.com/psyb0t/common-go/db/sqlite`. Do not call golang-migrate directly or write another runner.

## Queries

- Business code uses the generated repositories through `repositories.Query`, always with `.WithContext(ctx)`.
- Use the typed field accessors (`repo.SessionID.Eq(id)`), never a column name in a string. A renamed column then breaks the build instead of a query at runtime.
- Field accessors live on the static repository, chain methods on the value `WithContext` returns:

```go
repo := tx.Message
messages, err := repo.WithContext(ctx).
	Where(repo.SessionID.Eq(sessionID)).
	Order(repo.Sequence).
	Find()
if err != nil {
	return nil, ctxerrors.Wrap(err, "list session messages")
}
```

- Group writes that must land together with `query.Transaction(func(tx *repositories.Query) error { ... })`.
- Any join, or SQL the typed builder cannot express cleanly, goes in a querier interface in `cmd/repogen/<table>_querier.go` with the SQL in a `/* */` comment, wired with `ApplyInterface`. Use `@@table` for the table and `@name` for parameters, which gorm/gen binds. See `message_querier.go`.
- Never use `UnderlyingDB()`, `Raw`, or `Exec` in business code, and never build SQL from strings.
- Not-found and duplicate-key errors map to `commerr.ErrNotFound` and `commerr.ErrAlreadyExists` when wrapped. See `go-errors.md`.

## What goes in a row

Records hold what a user needs to replay and audit a session. They never hold a provider key, an API token, or a worker credential. Model call records keep the connection name and settings, not the credential. See `security.md`.
