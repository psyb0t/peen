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

Before 1.0, each table has exactly one migration, `<7-digit sequence>_create_<exact_table>_table.up.sql`, and the matching `.down.sql` that drops the table and its indexes. Sequence numbers follow dependency order: a table comes after every table it references, and a table with no foreign keys may go anywhere before the tables that reference it. `sqliteTableOrder` in `internal/pkg/db/sqlite.go` reads that order from the file names, so a file that does not follow the pattern or skips a number fails the open.

1. Edit the table's create migration and its down file directly. Do not add an alter migration. To add a table, add a create pair numbered after every table it references, and bump `sqliteMigrationCount` in `internal/pkg/db/migrations_test.go`. Inserting before the last table renumbers the later files.
2. Update `internal/pkg/db/testdata/sqlite_schema.golden`, the pinned normalized schema. `TestSQLiteSchemaMatchesPinnedSchema` fails with the difference until the file matches, so the diff of that file is the review of the schema change.
3. Update the model in `internal/pkg/db/models/`. Add a new model to `ApplyBasic` in `cmd/repogen/main.go`, in alphabetical order.
4. Run `make generate`.

A database created before the edit does not match the new schema, and Peen does not migrate it. Stop Peen, delete `peen.db`, `peen.db-wal`, and `peen.db-shm` from your development state directory, and let Peen create it again. Open builds the schema the embedded migrations produce up to the recorded version in an in-memory database and compares it with the file's schema in the normalized form of `sqlite_schema.golden`. A different table set, column, default, constraint, or index refuses the open with an error that names the files to delete.

After 1.0 the append-only rule returns: never edit a migration that has been committed, and add a new numbered pair `<7-digit sequence>_<action>_<exact_table>_table[_details].up.sql` with its `.down.sql` that reverses it exactly, such as an `alter` migration that adds a column. The pinned schema and `sqliteMigrationCount` then follow each added step.

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
