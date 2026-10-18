package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/backend/history"
	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/core"
	"github.com/cschleiden/go-workflows/worker"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testEventuallyTimeout bounds positive waits. Coverage and -race
// instrumentation under CI's CPU cap make a short window flake.
const testEventuallyTimeout = time.Minute

func storageActivity(context.Context) (string, error) {
	return "recorded result", nil
}

func storageWorkflow(ctx workflow.Context) (string, error) {
	result, err := workflow.ExecuteActivity[string](
		ctx, workflow.DefaultActivityOptions, storageActivity,
	).Get(ctx)
	if err != nil {
		return "", ctxerrors.Wrap(err, "execute storage test activity")
	}

	return result, nil
}

func TestOpenWorkflowStoragePreservesSeparateSessionState(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cfg := Config{Directory: t.TempDir()}
	sessions, err := Open(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sessions.Close()) })
	record := &models.Session{
		ID: uuid.New(), CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(), RootAgent: "peen",
		ModelID: "provider/model",
	}
	query := repositories.Use(sessions.GormDB)
	require.NoError(t, query.Session.WithContext(ctx).Create(record))

	storage, err := OpenWorkflowStorage(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	instance, err := client.New(storage).CreateWorkflowInstance(
		ctx, client.WorkflowInstanceOptions{InstanceID: uuid.NewString()},
		storageWorkflow,
	)
	require.NoError(t, err)
	require.NoError(t, storage.Close())
	require.NoError(t, sessions.Close())

	reopened, err := OpenWorkflowStorage(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	state, err := reopened.GetWorkflowInstanceState(ctx, instance)
	require.NoError(t, err)
	assert.Equal(t, core.WorkflowInstanceStateActive, state)
	stats, err := reopened.GetStats(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, stats.ActiveWorkflowInstances)

	sessions, err = Open(ctx, cfg)
	require.NoError(t, err)
	query = repositories.Use(sessions.GormDB)
	saved, err := query.Session.WithContext(ctx).
		Where(query.Session.ID.Eq(record.ID)).First()
	require.NoError(t, err)
	assert.Equal(t, record.ID, saved.ID)
	info, err := os.Stat(filepath.Join(cfg.Directory, workflowSQLiteFileName))
	require.NoError(t, err)
	assert.Equal(t, sqliteFileMode, info.Mode().Perm())
}

func TestOpenWorkflowStorageReturnsRecordedResultAfterReopen(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cfg := Config{Directory: t.TempDir()}
	storage, err := OpenWorkflowStorage(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	executor := worker.New(storage, nil)
	require.NoError(t, executor.RegisterWorkflow(storageWorkflow))
	require.NoError(t, executor.RegisterActivity(storageActivity))
	require.NoError(t, executor.Start(ctx))
	stop := sync.OnceFunc(func() {
		cancel()
		require.NoError(t, executor.WaitForCompletion())
	})
	t.Cleanup(stop)
	api := client.New(storage)
	instance, err := api.CreateWorkflowInstance(
		ctx, client.WorkflowInstanceOptions{InstanceID: uuid.NewString()},
		storageWorkflow,
	)
	require.NoError(t, err)
	result, err := client.GetWorkflowResult[string](
		ctx, api, instance, testEventuallyTimeout,
	)
	require.NoError(t, err)
	assert.Equal(t, "recorded result", result)
	stop()
	require.NoError(t, storage.Close())

	reopened, err := OpenWorkflowStorage(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	result, err = client.GetWorkflowResult[string](
		t.Context(), client.New(reopened), instance, testEventuallyTimeout,
	)
	require.NoError(t, err)
	assert.Equal(t, "recorded result", result)
	stats, err := reopened.GetStats(t.Context())
	require.NoError(t, err)
	assert.Zero(t, stats.ActiveWorkflowInstances)
}

func TestOpenWorkflowStorageRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name string
		cfg  Config
	}{
		{name: "empty directory"},
		{name: "relative directory", cfg: Config{Directory: "relative"}},
		{name: "unsafe directory", cfg: Config{Directory: "/tmp/state?mode=memory"}},
		{name: "negative timeout", cfg: Config{InMemory: true, BusyTimeout: -time.Second}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			storage, err := OpenWorkflowStorage(t.Context(), tc.cfg)
			require.ErrorIs(t, err, commerr.ErrInvalidArgument)
			assert.Nil(t, storage)
		})
	}
}

func TestOpenWorkflowStorageRejectsSymlinksAndCorruptFiles(t *testing.T) {
	t.Parallel()
	t.Run("directory symlink", func(t *testing.T) {
		t.Parallel()
		link := filepath.Join(t.TempDir(), "link")
		require.NoError(t, os.Symlink(t.TempDir(), link))
		storage, err := OpenWorkflowStorage(t.Context(), Config{Directory: link})
		require.ErrorIs(t, err, commerr.ErrInvalidArgument)
		assert.Nil(t, storage)
	})
	t.Run("database symlink", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		require.NoError(t, os.Symlink(
			filepath.Join(t.TempDir(), "target"),
			filepath.Join(directory, workflowSQLiteFileName),
		))
		storage, err := OpenWorkflowStorage(t.Context(), Config{Directory: directory})
		require.ErrorIs(t, err, commerr.ErrInvalidArgument)
		assert.Nil(t, storage)
	})
	t.Run("corrupt database", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(directory, workflowSQLiteFileName),
			[]byte("not a sqlite database"), sqliteFileMode,
		))
		require.NotPanics(t, func() {
			storage, err := OpenWorkflowStorage(t.Context(), Config{Directory: directory})
			require.Error(t, err)
			assert.Nil(t, storage)
		})
	})
}

func TestOpenWorkflowStorageSupportsMemoryAndRepeatedClose(t *testing.T) {
	t.Parallel()
	storage, err := OpenWorkflowStorage(t.Context(), Config{InMemory: true})
	require.NoError(t, err)
	stats, err := storage.GetStats(t.Context())
	require.NoError(t, err)
	assert.Zero(t, stats.ActiveWorkflowInstances)
	require.NoError(t, storage.Close())
	require.NoError(t, storage.Close())
	_, err = storage.GetStats(t.Context())
	require.Error(t, err)
	var empty *WorkflowStorage
	require.NoError(t, empty.Close())
}

func TestOpenWorkflowStorageRejectsCancelledStartup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	storage, err := OpenWorkflowStorage(ctx, Config{InMemory: true})
	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, storage)
}

func TestOpenWorkflowStoragePreservesDirtyMigration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	directory := t.TempDir()
	path := filepath.Join(directory, workflowSQLiteFileName)
	connection, err := sql.Open(sqliteDriverName, path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	_, err = connection.ExecContext(ctx, `
CREATE TABLE schema_migrations (version BIGINT PRIMARY KEY, dirty BOOLEAN);
INSERT INTO schema_migrations VALUES (1, true);`)
	require.NoError(t, err)
	storage, err := OpenWorkflowStorage(ctx, Config{Directory: directory})
	require.Error(t, err)
	assert.Nil(t, storage)
	var dirty bool
	require.NoError(t, connection.QueryRowContext(ctx,
		"SELECT dirty FROM schema_migrations WHERE version = 1",
	).Scan(&dirty))
	assert.True(t, dirty)
}

func TestWorkflowStorageWorkerBackendBorrowsConnection(t *testing.T) {
	t.Parallel()
	storage, err := OpenWorkflowStorage(t.Context(), Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	generationID := uuid.New()
	borrowed, err := storage.WorkerBackend(t.Context(), generationID)
	require.NoError(t, err)
	assert.Equal(t, generationID.String(), borrowed.Options().WorkerName)
	require.NoError(t, borrowed.Close())
	_, err = storage.GetStats(t.Context())
	require.NoError(t, err)

	_, err = storage.WorkerBackend(t.Context(), uuid.Nil)
	require.ErrorIs(t, err, commerr.ErrInvalidArgument)
	var empty *WorkflowStorage
	_, err = empty.WorkerBackend(t.Context(), generationID)
	require.ErrorIs(t, err, commerr.ErrInvalidArgument)
	require.NoError(t, storage.Close())
	_, err = storage.WorkerBackend(t.Context(), generationID)
	require.Error(t, err)
}

func TestWorkflowStorageWorkerGenerationFencesWorkflowTasks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	storage, err := OpenWorkflowStorage(ctx, Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	first, err := storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	second, err := storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	instance, err := client.New(storage).CreateWorkflowInstance(
		ctx, client.WorkflowInstanceOptions{InstanceID: uuid.NewString()},
		storageWorkflow,
	)
	require.NoError(t, err)
	queues := []workflow.Queue{workflow.QueueDefault}
	claimed, err := first.GetWorkflowTask(ctx, queues)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, instance, claimed.WorkflowInstance)
	_, err = storage.sqlDB.ExecContext(ctx, `
UPDATE instances SET locked_until = ?, sticky_until = ?
WHERE id = ? AND execution_id = ?`,
		time.Now().Add(-time.Minute), time.Now().Add(-time.Minute),
		instance.InstanceID, instance.ExecutionID,
	)
	require.NoError(t, err)
	reclaimed, err := second.GetWorkflowTask(ctx, queues)
	require.NoError(t, err)
	require.NotNil(t, reclaimed)
	assert.Equal(t, instance, reclaimed.WorkflowInstance)
	require.Error(t, first.ExtendWorkflowTask(ctx, claimed))
	require.Error(t, first.CompleteWorkflowTask(
		ctx, claimed, core.WorkflowInstanceStateActive,
		claimed.NewEvents, nil, nil, nil,
	))
	require.NoError(t, second.ExtendWorkflowTask(ctx, reclaimed))
	require.NoError(t, second.CompleteWorkflowTask(
		ctx, reclaimed, core.WorkflowInstanceStateActive,
		reclaimed.NewEvents, nil, nil, nil,
	))
}

func TestWorkflowStorageWorkerGenerationFencesActivityTasks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	storage, err := OpenWorkflowStorage(ctx, Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	first, err := storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	second, err := storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	instance, err := client.New(storage).CreateWorkflowInstance(
		ctx, client.WorkflowInstanceOptions{InstanceID: uuid.NewString()},
		storageWorkflow,
	)
	require.NoError(t, err)
	queues := []workflow.Queue{workflow.QueueDefault}
	workflowTask, err := first.GetWorkflowTask(ctx, queues)
	require.NoError(t, err)
	require.NotNil(t, workflowTask)
	scheduled := history.NewPendingEvent(
		time.Now().UTC(), history.EventType_ActivityScheduled,
		&history.ActivityScheduledAttributes{Name: "storageActivity"},
		history.ScheduleEventID(1),
	)
	executed := slices.Clone(workflowTask.NewEvents)
	executed = append(executed, scheduled)
	require.NoError(t, first.CompleteWorkflowTask(
		ctx, workflowTask, core.WorkflowInstanceStateActive,
		executed, []*history.Event{scheduled}, nil, nil,
	))
	claimed, err := first.GetActivityTask(ctx, queues)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assertStoredActivityLeaseBound(t, storage, claimed.ActivityID)
	liveClaim, err := second.GetActivityTask(ctx, queues)
	require.NoError(t, err)
	assert.Nil(t, liveClaim)
	_, err = storage.sqlDB.ExecContext(ctx,
		"UPDATE activities SET locked_until = ? WHERE id = ?",
		time.Now().Add(-time.Minute), claimed.ActivityID,
	)
	require.NoError(t, err)
	reclaimed, err := second.GetActivityTask(ctx, queues)
	require.NoError(t, err)
	require.NotNil(t, reclaimed)
	assert.Equal(t, claimed.ActivityID, reclaimed.ActivityID)
	assert.Equal(t, instance, reclaimed.WorkflowInstance)
	completed := history.NewPendingEvent(
		time.Now().UTC(), history.EventType_ActivityCompleted,
		&history.ActivityCompletedAttributes{}, history.ScheduleEventID(1),
	)
	require.Error(t, first.ExtendActivityTask(ctx, claimed))
	require.Error(t, first.CompleteActivityTask(ctx, claimed, completed))
	require.NoError(t, second.ExtendActivityTask(ctx, reclaimed))
	assertStoredActivityLeaseBound(t, storage, reclaimed.ActivityID)
	require.NoError(t, second.CompleteActivityTask(ctx, reclaimed, completed))
}

func assertStoredActivityLeaseBound(
	t *testing.T,
	storage *WorkflowStorage,
	activityID string,
) {
	t.Helper()
	var deadline time.Time
	err := storage.sqlDB.QueryRowContext(t.Context(),
		"SELECT locked_until FROM activities WHERE id = ?", activityID,
	).Scan(&deadline)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(time.Minute), deadline, time.Second)
}
