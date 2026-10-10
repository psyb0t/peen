package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonsqlite "github.com/psyb0t/common-go/db/sqlite"
	"github.com/psyb0t/peen/internal/pkg/db/migrations"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqliteMigrationCount is every migration in the sequence. Migrating down by
// this many steps returns an opened database to an empty schema.
const sqliteMigrationCount = 16

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
		{table: "sessions", column: "execution_profile"},
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
		VALUES (?, 'session', 'turn', 1, '/work', 'user', 'old row', ?)`

	insertKindedMessage = `INSERT INTO messages
		(id, session_id, turn_id, sequence, workspace, role, content,
		 created_at, injected_kind)
		VALUES (?, 'session', 'turn', ?, '/work', 'user', 'new row', ?, ?)`
)

// Migration 16 adds the injected kind. A message stored before it reads back
// with an empty kind, and the column accepts only an empty kind or skill.
func TestSQLiteMigration16DefaultsOldRowsAndChecksTheKind(t *testing.T) {
	handle := openMigratedStore(t)
	sqlDB := handle.SQLDB()
	ctx := t.Context()

	require.NoError(t, commonsqlite.MigrateDown(
		sqlDB,
		sqliteMigrationsPath,
		1,
		&migrations.SQLiteFS,
	))
	assert.NotContains(t, tableColumns(t, sqlDB, "messages"), "injected_kind")

	now := time.Now().UTC()
	require.NoError(t, execWithoutForeignKeys(
		t,
		sqlDB,
		insertLegacyMessage,
		"legacy",
		now,
	))

	require.NoError(t, commonsqlite.MigrateUp(
		sqlDB,
		sqliteMigrationsPath,
		&migrations.SQLiteFS,
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
