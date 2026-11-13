package db

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSessionReadsKeepTransactionsAndDoNotWaitForWriter(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	handle, err := Open(ctx, Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	query := repositories.Use(handle.GormDB)
	record := &models.Session{
		ID: uuid.New(), RootAgent: "original", ModelID: "provider/model",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, query.Session.WithContext(ctx).Create(record))
	tx := query.Begin()
	require.NoError(t, tx.Error)
	defer func() {
		// A successful Commit below makes this rollback a no-op.
		_ = tx.Rollback()
	}()
	repo := tx.Session
	_, err = repo.WithContext(ctx).Where(repo.ID.Eq(record.ID)).
		UpdateColumnSimple(repo.RootAgent.Value("changed"))
	require.NoError(t, err)
	uncommitted, err := repo.WithContext(ctx).Where(repo.ID.Eq(record.ID)).First()
	require.NoError(t, err)
	assert.Equal(t, "changed", uncommitted.RootAgent)
	readCtx, cancel := context.WithTimeout(ctx, workflowReadDeadline)
	defer cancel()
	repo = query.Session
	committed, err := repo.WithContext(readCtx).Where(repo.ID.Eq(record.ID)).First()
	require.NoError(t, err)
	assert.Equal(t, "original", committed.RootAgent)
	require.NoError(t, tx.Commit())
	committed, err = repo.WithContext(ctx).Where(repo.ID.Eq(record.ID)).First()
	require.NoError(t, err)
	assert.Equal(t, "changed", committed.RootAgent)

	// A handle reused after a read still starts its transaction on the writer.
	read := handle.GormDB.WithContext(ctx).First(&models.Session{}, "id = ?", record.ID)
	require.NoError(t, read.Error)
	reused := read.Session(&gorm.Session{NewDB: true}).Begin()
	require.NoError(t, reused.Error)
	defer func() {
		// Release the writer even when the mutation assertion fails.
		_ = reused.Rollback()
	}()
	require.NoError(t, reused.Model(record).
		UpdateColumn("root_agent", "reused").Error)
	require.NoError(t, reused.Rollback().Error)
}

func TestSlowSQLiteDiagnosticOmitsSQLValues(t *testing.T) {
	var output bytes.Buffer

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output,
		&slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	handle, err := Open(t.Context(), Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	output.Reset()
	database := handle.GormDB.Session(&gorm.Session{NewDB: true}).
		WithContext(t.Context())
	database.Statement.Table = (models.Session{}).TableName()
	database.Statement.SQL.WriteString("SELECT 'private-test-value'")
	database = database.InstanceSet(databaseMetricStartKey, databaseMetricStart{
		operation: databaseOperationQuery,
		startedAt: time.Now().Add(-time.Second),
	})
	finishDatabaseMetric(nil, handle.sqlDB, sqliteFileName)(database)
	assert.Contains(t, output.String(), `"database":"peen.db"`)
	assert.Contains(t, output.String(), `"table":"`+(models.Session{}).TableName()+`"`)
	assert.Contains(t, output.String(), `"operation":"query"`)
	assert.Contains(t, output.String(), `"pool_wait_count":`)
	assert.NotContains(t, output.String(), "private-test-value")
	assert.NotContains(t, output.String(), "SELECT")
}

func TestSQLiteReaderConnectionsAreQueryOnlyAndConfigured(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	handle, err := Open(ctx, Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	assertSQLiteReaderPragmas(t, handle.readDB)
	_, err = handle.readDB.ExecContext(ctx, "DELETE FROM sessions")
	require.Error(t, err)
	storage, err := OpenWorkflowStorage(ctx, Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	assertSQLiteReaderPragmas(t, storage.readDB)
	_, err = storage.readDB.ExecContext(ctx, "DELETE FROM instances")
	require.Error(t, err)
	require.NoError(t, storage.Close())
	require.Error(t, storage.readDB.PingContext(ctx))
	require.NoError(t, handle.Close())
	require.Error(t, handle.readDB.PingContext(ctx))
}

func assertSQLiteReaderPragmas(t *testing.T, pool *sql.DB) {
	t.Helper()
	connections := make([]*sql.Conn, 0, sqliteReadConnections)
	defer func() {
		for _, connection := range connections {
			require.NoError(t, connection.Close())
		}
	}()
	for range sqliteReadConnections {
		connection, err := pool.Conn(t.Context())
		require.NoError(t, err)
		connections = append(connections, connection)
		for _, tc := range []struct {
			pragma string
			want   int
		}{
			{"PRAGMA query_only", 1},
			{"PRAGMA synchronous", 2},
			{"PRAGMA busy_timeout", 5000},
			{"PRAGMA foreign_keys", 1},
		} {
			var value int
			require.NoError(t, connection.QueryRowContext(t.Context(), tc.pragma).Scan(&value))
			assert.Equal(t, tc.want, value, tc.pragma)
		}

		for _, pragma := range []string{
			"PRAGMA journal_mode", "PRAGMA cache_size", "PRAGMA mmap_size",
			"PRAGMA wal_autocheckpoint",
		} {
			var value string
			require.NoError(t, connection.QueryRowContext(t.Context(), pragma).Scan(&value))
			t.Logf("%s = %s", pragma, value)
		}
	}
}
