package db

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	workflowsqlite "github.com/cschleiden/go-workflows/backend/sqlite"
	"github.com/google/uuid"
	commonsqlite "github.com/psyb0t/common-go/db/sqlite"
	sqlitegorm "github.com/psyb0t/common-go/db/sqlite/gorm"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/migrations"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOpenUsesPrivateFilesystemState(t *testing.T) {
	stateDirectory := filepath.Join(t.TempDir(), "state")
	handle, err := Open(context.Background(), Config{Directory: stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	directoryInfo, err := os.Stat(stateDirectory)
	require.NoError(t, err)
	assert.Equal(t, sqliteDirectoryMode, directoryInfo.Mode().Perm())

	databaseInfo, err := os.Stat(filepath.Join(stateDirectory, sqliteFileName))
	require.NoError(t, err)
	assert.Equal(t, sqliteFileMode, databaseInfo.Mode().Perm())
}

func TestOpenDoesNotLogSQLValues(t *testing.T) {
	var output bytes.Buffer

	previousLogger := slog.Default()

	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	handle, err := Open(context.Background(), Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	output.Reset()

	sensitiveSQLValue := "controlled-sensitive-sql-value"

	handle.GormDB.Logger.Trace(
		context.Background(),
		time.Now(),
		func() (string, int64) {
			return "INSERT INTO messages (content) VALUES ('" + sensitiveSQLValue + "')", 1
		},
		nil,
	)

	assert.NotContains(t, output.String(), sensitiveSQLValue)
}

func TestOpenRejectsSymlinkDirectory(t *testing.T) {
	stateRoot := t.TempDir()
	linkPath := filepath.Join(stateRoot, "state-link")
	require.NoError(t, os.Symlink(filepath.Join(stateRoot, "target"), linkPath))

	handle, err := Open(context.Background(), Config{Directory: linkPath})
	assert.Nil(t, handle)
	require.Error(t, err)
}

func TestOpenRejectsSymlinkDatabase(t *testing.T) {
	stateDirectory := t.TempDir()
	databasePath := filepath.Join(stateDirectory, sqliteFileName)
	require.NoError(t, os.Symlink(filepath.Join(stateDirectory, "target.db"), databasePath))

	handle, err := Open(context.Background(), Config{Directory: stateDirectory})
	assert.Nil(t, handle)
	require.Error(t, err)
}

func TestOpenRestrictsExistingFilesystemModes(t *testing.T) {
	stateDirectory := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Mkdir(stateDirectory, 0o750))
	databasePath := filepath.Join(stateDirectory, sqliteFileName)
	require.NoError(t, os.WriteFile(databasePath, nil, 0o600))
	require.NoError(t, os.Chmod(stateDirectory, 0o777)) //nolint:gosec // proves Open() tightens an over-permissive existing directory
	require.NoError(t, os.Chmod(databasePath, 0o666))   //nolint:gosec // proves Open() tightens an over-permissive existing file

	handle, err := Open(context.Background(), Config{Directory: stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	directoryInfo, err := os.Stat(stateDirectory)
	require.NoError(t, err)
	assert.Equal(t, sqliteDirectoryMode, directoryInfo.Mode().Perm())

	databaseInfo, err := os.Stat(databasePath)
	require.NoError(t, err)
	assert.Equal(t, sqliteFileMode, databaseInfo.Mode().Perm())
}

func TestOpenRebuildsDirectCompactionLinksAfterUpgrade(t *testing.T) {
	ctx := context.Background()
	stateDirectory := filepath.Join(t.TempDir(), "state")
	handle, err := Open(ctx, Config{Directory: stateDirectory})
	require.NoError(t, err)

	query := repositories.Use(handle.GormDB)
	now := time.Now().UTC()
	sessionID := uuid.New()
	turnID := uuid.New()
	firstMessageID := uuid.New()
	secondMessageID := uuid.New()
	thirdMessageID := uuid.New()
	firstCompactionID := uuid.New()
	secondCompactionID := uuid.New()

	require.NoError(t, query.Session.WithContext(ctx).Create(&models.Session{
		ID:           sessionID,
		CreatedAt:    now,
		UpdatedAt:    now,
		RootAgent:    "peen",
		ModelID:      "provider/model",
		MessageCount: 3,
	}))
	completedAt := now
	require.NoError(t, query.Turn.WithContext(ctx).Create(&models.Turn{
		ID:          turnID,
		SessionID:   sessionID,
		RequestID:   uuid.New(),
		Workspace:   "/workspace",
		State:       models.TurnStateCompleted,
		StartedAt:   now,
		CompletedAt: &completedAt,
	}))

	for sequence, messageID := range []uuid.UUID{
		firstMessageID,
		secondMessageID,
		thirdMessageID,
	} {
		require.NoError(t, query.Message.WithContext(ctx).Create(&models.Message{
			ID:            messageID,
			SessionID:     sessionID,
			TurnID:        turnID,
			Sequence:      int64(sequence + 1),
			Workspace:     "/workspace",
			Role:          models.MessageRoleUser,
			Content:       "message",
			ToolCallsJSON: "[]",
			CreatedAt:     now,
		}))
	}

	supersededCompactionID := firstCompactionID
	require.NoError(t, query.Compaction.WithContext(ctx).Create(&models.Compaction{
		ID:                 firstCompactionID,
		SessionID:          sessionID,
		FromMessageID:      firstMessageID,
		ToMessageID:        firstMessageID,
		FromSequence:       1,
		ToSequence:         1,
		Summary:            "first summary",
		SourceMessageCount: 1,
		ModelID:            "provider/model",
		PromptHash:         "prompt-one",
		CreatedAt:          now,
	}))
	require.NoError(t, query.Compaction.WithContext(ctx).Create(&models.Compaction{
		ID:                     secondCompactionID,
		SessionID:              sessionID,
		FromMessageID:          firstMessageID,
		ToMessageID:            secondMessageID,
		FromSequence:           1,
		ToSequence:             2,
		Summary:                "second summary",
		SourceMessageCount:     2,
		ModelID:                "provider/model",
		PromptHash:             "prompt-two",
		CreatedAt:              now,
		SupersedesCompactionID: &supersededCompactionID,
	}))

	for messageID, compactionID := range map[uuid.UUID]uuid.UUID{
		firstMessageID:  firstCompactionID,
		secondMessageID: secondCompactionID,
	} {
		_, updateErr := query.Message.WithContext(ctx).
			Where(query.Message.ID.Eq(messageID)).
			UpdateSimple(query.Message.CompactionID.Value(compactionID))
		require.NoError(t, updateErr)
	}

	require.NoError(t, commonsqlite.MigrateDown(
		handle.SQLDB(),
		sqliteMigrationsPath,
		1,
		&migrations.SQLiteFS,
	))
	require.NoError(t, handle.Close())

	reopened, reopenErr := Open(ctx, Config{Directory: stateDirectory})
	require.NoError(t, reopenErr)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })

	reopenedQuery := repositories.Use(reopened.GormDB)
	messages, listErr := reopenedQuery.Message.WithContext(ctx).
		Where(reopenedQuery.Message.SessionID.Eq(sessionID)).
		Order(reopenedQuery.Message.Sequence.Asc()).
		Find()
	require.NoError(t, listErr)
	require.Len(t, messages, 3)
	require.NotNil(t, messages[0].CompactionID)
	require.NotNil(t, messages[1].CompactionID)
	assert.Equal(t, firstCompactionID, *messages[0].CompactionID)
	assert.Equal(t, secondCompactionID, *messages[1].CompactionID)
	assert.Nil(t, messages[2].CompactionID)
}

func TestSQLiteCoexistsWithNativeWorkflowBackend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handle, err := Open(ctx, Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	storage := workflowsqlite.NewSqliteBackend(
		filepath.Join(t.TempDir(), "workflows.db"),
	)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })

	require.NoError(t, handle.SQLDB().PingContext(ctx))
}

func TestSQLiteDialectorTranslatesNativeConstraints(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		query   string
		wantErr error
	}{
		{
			name:    "primary key",
			query:   "INSERT INTO parents VALUES (1, 'second')",
			wantErr: gorm.ErrDuplicatedKey,
		},
		{
			name:    "unique index",
			query:   "INSERT INTO parents VALUES (2, 'first')",
			wantErr: gorm.ErrDuplicatedKey,
		},
		{
			name:    "foreign key",
			query:   "INSERT INTO children VALUES (2)",
			wantErr: gorm.ErrForeignKeyViolated,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			connection, err := sql.Open(sqliteDriverName, ":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connection.Close()) })
			connection.SetMaxOpenConns(1)
			_, err = connection.ExecContext(t.Context(), `
PRAGMA foreign_keys = ON;
CREATE TABLE parents (id INTEGER PRIMARY KEY, name TEXT UNIQUE);
CREATE TABLE children (parent_id INTEGER REFERENCES parents(id));
INSERT INTO parents VALUES (1, 'first');`)
			require.NoError(t, err)
			_, err = connection.ExecContext(t.Context(), tc.query)
			require.Error(t, err)
			translated := sqlitegorm.Open(":memory:").Translate(
				ctxerrors.Wrap(err, "execute constraint probe"),
			)
			require.ErrorIs(t, translated, tc.wantErr)
		})
	}
}

func TestSQLiteDialectorPreservesOtherErrors(t *testing.T) {
	t.Parallel()
	dialector := sqlitegorm.Open(":memory:")
	assert.NoError(t, dialector.Translate(nil))
	assert.ErrorIs(t, dialector.Translate(commerr.ErrConflict),
		commerr.ErrConflict)
	connection, err := sql.Open(sqliteDriverName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	_, err = connection.ExecContext(t.Context(), "SELECT * FROM missing")
	require.Error(t, err)
	assert.Same(t, err, dialector.Translate(err))
}

func TestSQLiteDialectorPreservesRepositoryRecords(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	directory := t.TempDir()
	handle, err := Open(ctx, Config{Directory: directory})
	require.NoError(t, err)
	query := repositories.Use(handle.GormDB)
	record := &models.Session{
		ID:        uuid.New(),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		RootAgent: "peen",
		ModelID:   "provider/model",
		Workspace: "/workspace",
	}
	require.NoError(t, query.Session.WithContext(ctx).Create(record))
	err = query.Session.WithContext(ctx).Create(record)
	require.ErrorIs(t, ctxerrors.Wrap(err, "create duplicate session"),
		commerr.ErrAlreadyExists)
	require.NoError(t, handle.Close())
	handle, err = Open(ctx, Config{Directory: directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	query = repositories.Use(handle.GormDB)
	saved, err := query.Session.WithContext(ctx).
		Where(query.Session.ID.Eq(record.ID)).First()
	require.NoError(t, err)
	assert.Equal(t, record.ID, saved.ID)
	assert.Equal(t, record.ModelID, saved.ModelID)
	assert.Equal(t, record.Workspace, saved.Workspace)
}
