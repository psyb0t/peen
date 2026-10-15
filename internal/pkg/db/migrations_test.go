package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	commonsqlite "github.com/psyb0t/common-go/db/sqlite"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/migrations"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqliteMigrationCount is every migration in the sequence. Migrating down by
// this many steps returns an opened database to an empty schema.
const sqliteMigrationCount = 25

// These tests do not call t.Parallel. Opening the store installs the generated
// repositories as a package default, which is process-global state.

// The worker architecture ships one migration sequence written in worker terms.
// An executor table, column, or index in the applied schema would mean a stale
// name reached a database an operator keeps.
func TestSQLiteSchemaUsesOnlyWorkerTerminology(t *testing.T) {
	handle := openMigratedStore(t)

	names := schemaObjectNames(t, handle.SQLDB())
	require.NotEmpty(t, names)

	for _, name := range names {
		assert.NotContains(
			t,
			strings.ToLower(name),
			"executor",
			"the applied schema still carries an executor name",
		)
	}

	assert.Contains(t, names, "worker_generations")
	assert.Contains(t, names, "worker_generations_session_created_at_id_index")
	assert.Contains(t, names, "worker_generations_session_state_index")

	testCases := []struct {
		table  string
		column string
	}{
		{table: "worker_generations", column: "credential_hash"},
		{table: "worker_generations", column: "socket_path"},
		{table: "turns", column: "worker_generation_id"},
		{table: "events", column: "worker_generation_id"},
		{table: "jobs", column: "worker_generation_id"},
		{table: "agent_runs", column: "worker_generation_id"},
		{table: "sessions", column: "agent_space"},
		{table: "worker_generations", column: "agent_space"},
		{table: "worker_generations", column: "agent_space_revision"},
		{table: "session_agent_space_decisions", column: "from_agent_space"},
		{table: "session_agent_space_decisions", column: "to_agent_space"},
		{table: "messages", column: "injected"},
		{table: "messages", column: "injected_kind"},
	}

	for _, tc := range testCases {
		columns := tableColumns(t, handle.SQLDB(), tc.table)
		assert.Contains(
			t,
			columns,
			tc.column,
			"table %q is missing column %q",
			tc.table,
			tc.column,
		)

		for _, column := range columns {
			assert.NotContains(t, strings.ToLower(column), "executor")
		}
	}
}

// Every migration's down is the inverse of its up. Running the sequence down
// and back up proves no step is one-way, which is what makes a rollback
// possible at all.
func TestSQLiteMigrationsAreReversible(t *testing.T) {
	handle := openMigratedStore(t)
	sqlDB := handle.SQLDB()

	require.NoError(t, commonsqlite.MigrateDown(
		sqlDB,
		sqliteMigrationsPath,
		sqliteMigrationCount,
		&migrations.SQLiteFS,
	))

	emptied := schemaObjectNames(t, sqlDB)
	assert.NotContains(t, emptied, "worker_generations")
	assert.NotContains(t, emptied, "sessions")

	require.NoError(t, commonsqlite.MigrateUp(
		sqlDB,
		sqliteMigrationsPath,
		&migrations.SQLiteFS,
	))

	restored := schemaObjectNames(t, sqlDB)
	assert.Contains(t, restored, "worker_generations")
	assert.Contains(t, restored, "sessions")
	assert.Contains(
		t,
		tableColumns(t, sqlDB, "turns"),
		"worker_generation_id",
	)
}

const (
	insertLegacyMessage = `INSERT INTO messages
		(id, session_id, turn_id, sequence, workspace, role, content, created_at)
		VALUES (?, 'session', 'turn', 1, '/work', 'user', 'no kind', ?)`

	insertKindedMessage = `INSERT INTO messages
		(id, session_id, turn_id, sequence, workspace, role, content,
		 created_at, injected_kind)
		VALUES (?, 'session', 'turn', ?, '/work', 'user', 'new row', ?, ?)`
)

// The injected kind defaults to empty, and the column accepts only an empty
// kind or skill.
func TestSQLiteMessagesDefaultAndCheckTheInjectedKind(t *testing.T) {
	handle := openMigratedStore(t)
	sqlDB := handle.SQLDB()
	ctx := t.Context()

	now := time.Now().UTC()
	require.NoError(t, execWithoutForeignKeys(
		t,
		sqlDB,
		insertLegacyMessage,
		"legacy",
		now,
	))

	kind := "unset"
	require.NoError(t, sqlDB.QueryRowContext(
		ctx,
		"SELECT injected_kind FROM messages WHERE id = 'legacy'",
	).Scan(&kind))
	assert.Empty(t, kind)

	require.NoError(t, execWithoutForeignKeys(
		t, sqlDB, insertKindedMessage, "empty", 2, now, "",
	))
	require.NoError(t, execWithoutForeignKeys(
		t, sqlDB, insertKindedMessage, "skill", 3, now, "skill",
	))

	err := execWithoutForeignKeys(
		t, sqlDB, insertKindedMessage, "other", 4, now, "other",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CHECK constraint failed")
}

// execWithoutForeignKeys runs one statement on a connection that does not
// enforce foreign keys, so a row can name a session and turn that do not
// exist. The connection goes back to the pool before it returns.
func execWithoutForeignKeys(
	t *testing.T,
	sqlDB *sql.DB,
	query string,
	args ...any,
) error {
	t.Helper()

	conn, err := sqlDB.Conn(t.Context())
	require.NoError(t, err)

	defer func() { require.NoError(t, conn.Close()) }()

	_, err = conn.ExecContext(t.Context(), "PRAGMA foreign_keys = OFF")
	require.NoError(t, err)

	_, err = conn.ExecContext(t.Context(), query, args...)

	return err
}

// The migrations are the schema, so a change to them has to be deliberate.
// testdata/sqlite_schema.golden is the normalized schema the sequence must
// produce. The file was first checked equal to the schema the earlier
// sixteen-step sequence produced.
func TestSQLiteSchemaMatchesPinnedSchema(t *testing.T) {
	handle := openMigratedStore(t)

	golden, err := os.ReadFile("testdata/sqlite_schema.golden")
	require.NoError(t, err)

	assert.Equal(t, string(golden), dumpSchema(t, handle.SQLDB()))
}

// Each table has exactly one create migration numbered without gaps, and a
// down file that undoes it. sqliteTableOrder rejects any other file name.
func TestSQLiteMigrationFilesAreOneCreatePerTable(t *testing.T) {
	order, err := sqliteTableOrder()
	require.NoError(t, err)
	require.Len(t, order, sqliteMigrationCount)

	entries, err := migrations.SQLiteFS.ReadDir(sqliteMigrationsPath)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	want := make([]string, 0, sqliteMigrationCount*2)

	for i, table := range order {
		stem := fmt.Sprintf("%07d_create_%s_table", i+1, table)
		want = append(want, stem+".down.sql", stem+".up.sql")
	}

	assert.ElementsMatch(t, want, names)
}

// A database an earlier version created used a different migration sequence
// whose version numbers overlap the current one. Open refuses it and tells the
// operator to delete the file instead of running migrations over it.
func TestOpenRefusesDatabaseFromAnEarlierMigrationSequence(t *testing.T) {
	order, err := sqliteTableOrder()
	require.NoError(t, err)

	testCases := []struct {
		name    string
		version int
		tables  []string
	}{
		{name: "overlapping version with extra tables", version: 16, tables: order},
		{name: "early version with more tables", version: 1, tables: order[:7]},
		{name: "version past the sequence", version: 40, tables: order},
		{name: "version with no tables", version: 3, tables: nil},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			seedMigrationState(t, directory, tc.version, false, tc.tables)

			handle, err := Open(
				context.Background(),
				Config{Directory: directory},
			)
			require.ErrorIs(t, err, commerr.ErrInvalidState)
			assert.Nil(t, handle)
			assert.Contains(t, err.Error(), sqliteFileName)
			assert.Contains(t, err.Error(), "delete")
		})
	}
}

// The check only rejects a foreign sequence. A database this sequence made
// opens again, and a migration that stopped midway stays a migration problem.
func TestOpenAcceptsDatabaseFromTheCurrentMigrationSequence(t *testing.T) {
	order, err := sqliteTableOrder()
	require.NoError(t, err)

	directory := t.TempDir()
	handle, err := Open(context.Background(), Config{Directory: directory})
	require.NoError(t, err)
	require.NoError(t, handle.Close())

	handle, err = Open(context.Background(), Config{Directory: directory})
	require.NoError(t, err)
	require.NoError(t, handle.Close())

	dirtyDirectory := t.TempDir()
	seedMigrationState(t, dirtyDirectory, 5, true, order[:4])

	sqlDB := openSeededDatabase(t, dirtyDirectory)
	require.NoError(t, verifyMigrationLineage(t.Context(), sqlDB))
}

// The tables can be the right ones while a column, index, or default inside
// one differs, which is what editing a create migration before 1.0 does. Open
// compares the whole schema against the one the recorded version should have,
// refuses the file, and names every file to delete.
func TestOpenRefusesDatabaseWithADifferentSchema(t *testing.T) {
	testCases := []struct {
		name      string
		statement string
	}{
		{
			name:      "an added column",
			statement: "ALTER TABLE sessions ADD COLUMN extra TEXT",
		},
		{
			name: "a renamed column",
			statement: "ALTER TABLE sessions " +
				"RENAME COLUMN agent_space TO agent_space_old",
		},
		{
			name:      "an added index",
			statement: "CREATE INDEX extra_index ON sessions (id)",
		},
		{
			name: "a dropped index",
			statement: "DROP INDEX " +
				"worker_generations_session_state_index",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()

			handle, err := Open(
				context.Background(),
				Config{Directory: directory},
			)
			require.NoError(t, err)
			require.NoError(t, handle.Close())

			sqlDB := openSeededDatabase(t, directory)
			_, err = sqlDB.ExecContext(t.Context(), tc.statement)
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			handle, err = Open(
				context.Background(),
				Config{Directory: directory},
			)
			require.ErrorIs(t, err, commerr.ErrInvalidState)
			assert.Nil(t, handle)
			assert.Contains(t, err.Error(), "different Peen version")
			assert.Contains(t, err.Error(), sqliteFileName)
			assert.Contains(t, err.Error(), sqliteFileName+"-wal")
			assert.Contains(t, err.Error(), sqliteFileName+"-shm")
		})
	}
}

// A database an earlier release of this same sequence left at a lower version
// has exactly the schema of that version, so Open accepts it and upgrades it.
func TestOpenAcceptsAnOlderDatabaseWithTheMatchingSchema(t *testing.T) {
	const olderVersion = 5

	directory := t.TempDir()

	handle, err := Open(context.Background(), Config{Directory: directory})
	require.NoError(t, err)
	require.NoError(t, handle.Close())

	sqlDB := openSeededDatabase(t, directory)
	require.NoError(t, commonsqlite.MigrateDown(
		sqlDB,
		sqliteMigrationsPath,
		sqliteMigrationCount-olderVersion,
		&migrations.SQLiteFS,
	))
	require.NoError(t, sqlDB.Close())

	handle, err = Open(context.Background(), Config{Directory: directory})
	require.NoError(t, err)

	assert.Contains(t, schemaObjectNames(t, handle.SQLDB()), "worker_generations")
	require.NoError(t, handle.Close())
}

func TestOpenPreservesRecordsAcrossWorkflowSchemaUpgrade(t *testing.T) {
	const previousMigrationCount = 19
	directory := t.TempDir()
	handle, err := Open(t.Context(), Config{Directory: directory})
	require.NoError(t, err)
	query := repositories.Use(handle.GormDB)
	now := time.Now().UTC()
	session := &models.Session{
		ID: uuid.New(), RootAgent: "exampleRoot", ModelID: "example/model",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, query.Session.WithContext(t.Context()).Create(session))
	turn := &models.Turn{
		ID: uuid.New(), SessionID: session.ID, RequestID: uuid.New(),
		Workspace: directory, State: models.TurnStateCompleted,
		StartedAt: now, CompletedAt: &now,
	}
	require.NoError(t, query.Turn.WithContext(t.Context()).Create(turn))
	require.NoError(t, commonsqlite.MigrateDown(handle.SQLDB(),
		sqliteMigrationsPath, sqliteMigrationCount-previousMigrationCount,
		&migrations.SQLiteFS))
	require.NoError(t, handle.Close())
	reopened, err := Open(t.Context(), Config{Directory: directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	query = repositories.Use(reopened.GormDB)
	storedSession, err := query.Session.WithContext(t.Context()).Where(
		query.Session.ID.Eq(session.ID),
	).First()
	require.NoError(t, err)
	assert.Equal(t, session.RootAgent, storedSession.RootAgent)
	storedTurn, err := query.Turn.WithContext(t.Context()).Where(
		query.Turn.ID.Eq(turn.ID),
	).First()
	require.NoError(t, err)
	assert.Equal(t, models.TurnStateCompleted, storedTurn.State)
	assert.Equal(t, turn.RequestID, storedTurn.RequestID)
	assert.Contains(t, schemaObjectNames(t, reopened.SQLDB()), "executions")
}

// seedMigrationState writes a database file holding the given tables, created
// from the migrations that create them, and the migration version the runner
// would have recorded.
func seedMigrationState(
	t *testing.T,
	directory string,
	version int,
	dirty bool,
	tables []string,
) {
	t.Helper()

	sqlDB := openSeededDatabase(t, directory)

	_, err := sqlDB.ExecContext(
		t.Context(),
		"CREATE TABLE schema_migrations (version uint64, dirty bool)",
	)
	require.NoError(t, err)

	_, err = sqlDB.ExecContext(
		t.Context(),
		"INSERT INTO schema_migrations (version, dirty) VALUES (?, ?)",
		version,
		dirty,
	)
	require.NoError(t, err)

	order, err := sqliteTableOrder()
	require.NoError(t, err)

	for _, table := range tables {
		for index, name := range order {
			if name != table {
				continue
			}

			createTable, readErr := createMigrationSQL(index, name)
			require.NoError(t, readErr)

			_, err = sqlDB.ExecContext(t.Context(), createTable)
			require.NoError(t, err)
		}
	}

	require.NoError(t, sqlDB.Close())
}

func openSeededDatabase(t *testing.T, directory string) *sql.DB {
	t.Helper()

	sqlDB, err := sql.Open("sqlite", filepath.Join(directory, sqliteFileName))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	return sqlDB
}

// dumpSchema renders the schema the way Open compares it.
func dumpSchema(t *testing.T, sqlDB *sql.DB) string {
	t.Helper()

	snapshot, err := schemaSnapshot(t.Context(), sqlDB)
	require.NoError(t, err)

	return snapshot
}

// openMigratedStore opens a store in its own directory, which runs every
// migration the way a deployment does.
func openMigratedStore(t *testing.T) *Handle {
	t.Helper()

	handle, err := Open(
		context.Background(),
		Config{Directory: filepath.Join(t.TempDir(), "state")},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	return handle
}

// schemaObjectNames lists the tables and indexes the migrations created,
// excluding the objects SQLite and the migration runner own.
func schemaObjectNames(t *testing.T, sqlDB *sql.DB) []string {
	t.Helper()

	rows, err := sqlDB.QueryContext(
		t.Context(),
		`SELECT name FROM sqlite_master
		 WHERE type IN ('table', 'index')
		   AND name NOT LIKE 'sqlite_%'
		   AND name <> 'schema_migrations'`,
	)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, rows.Close()) })

	names := []string{}

	for rows.Next() {
		name := ""
		require.NoError(t, rows.Scan(&name))

		names = append(names, name)
	}

	require.NoError(t, rows.Err())

	return names
}

func tableColumns(t *testing.T, sqlDB *sql.DB, table string) []string {
	t.Helper()

	rows, err := sqlDB.QueryContext(
		t.Context(),
		`SELECT name FROM pragma_table_info(?)`,
		table,
	)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, rows.Close()) })

	columns := []string{}

	for rows.Next() {
		name := ""
		require.NoError(t, rows.Scan(&name))

		columns = append(columns, name)
	}

	require.NoError(t, rows.Err())

	return columns
}
