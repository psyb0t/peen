# SQLite backend

SQLite embeds workflow persistence in one application host. File-backed workers can reopen persisted history after restart. In-memory databases do not survive process exit. This backend does not establish distributed SQLite operation across machines.

## Connections and migrations

`NewSqliteBackend(path)` owns its connection, enables WAL, configures a busy timeout, limits the pool to one open connection and applies embedded migrations by default. `NewInMemoryBackend` creates a unique named in-memory database and retains a connection to keep it alive.

`NewSqliteBackendWithDB` uses a caller-owned `*sql.DB`, does not close it and disables migrations by default. The caller must configure WAL, busy timeout and pool size. Enable migrations explicitly with `WithApplyMigrations(true)` when appropriate. Constructors can panic when initialization or automatic migration fails.

## Maintaining the backend

Embedded migrations live in `db/migrations`, with paired `.up.sql` and `.down.sql` files. Follow the existing sequence and preserve published migrations. Back up a consistent database before applying schema changes; copying a live main file alone can omit data still in its WAL.

The common-go SQLite GORM adapter wraps the production modernc driver. The common-go migration runner uses the same caller-owned pool and existing ledger. `db/models` mirrors the SQL schema, while `db/repositories` contains generated typed accessors. File-backed constructors use immediate transactions to serialize claims before selecting work. A supplied pool must provide equivalent writer serialization. See [generation](../../docs/generation.md#database-repositories).

Run `make test-sqlite` for backend contracts and `make test-recovery` for file-backed worker recovery. See [backend operations](../../docs/backends.md) and [testing](../../docs/testing.md).
