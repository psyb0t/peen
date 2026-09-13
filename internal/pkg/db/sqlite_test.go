package db

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	commonsqlite "github.com/psyb0t/common-go/db/sqlite"
	"github.com/psyb0t/peen/internal/pkg/db/migrations"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
