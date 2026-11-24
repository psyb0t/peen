package cronstore

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) (*Store, *db.Handle) {
	t.Helper()
	handle, err := db.Open(t.Context(), db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := New(handle)
	require.NoError(t, err)

	return store, handle
}

func testJob(name string) *models.CronJob {
	return &models.CronJob{
		JobName: name, DefinitionHash: strings.Repeat("a", 64),
		NextScheduledAt: time.Unix(10, 0).UTC(),
	}
}

func testRun(name string, tick int64) *models.CronRun {
	started := time.Unix(tick, 0).UTC()

	return &models.CronRun{
		ID: uuid.New(), JobName: name,
		ScheduledAt: started, StartedAt: &started,
		State: models.CronRunStateRunning,
	}
}

func testSession(t *testing.T, handle *db.Handle) uuid.UUID {
	t.Helper()
	id := uuid.New()
	now := time.Unix(10, 0).UTC()
	row := &models.Session{
		ID: id, CreatedAt: now, UpdatedAt: now,
		RootAgent: "peen", ModelID: "example/model",
	}
	require.NoError(t, repositories.Use(handle.GormDB).Session.
		WithContext(t.Context()).Create(row))

	return id
}
