package cronstore

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/stretchr/testify/require"
)

func TestSchemaConstraints(t *testing.T) {
	t.Parallel()
	_, handle := testStore(t)
	query := repositories.Use(handle.GormDB)
	testCases := []struct {
		name   string
		change func(*models.CronRun)
	}{
		{"state", func(r *models.CronRun) { r.State = "unknown" }},
		{"error bound", func(r *models.CronRun) {
			r.Error = strings.Repeat("x", 257)
		}},
		{"job name bound", func(r *models.CronRun) {
			r.JobName = strings.Repeat("a", 65)
		}},
		{"session FK", func(r *models.CronRun) {
			id := uuid.New()
			r.SessionID = &id
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := testRun("nightlyReport", 10)
			tc.change(run)
			require.Error(t, query.CronRun.WithContext(t.Context()).Create(run))
		})
	}
	job := testJob("nightlyReport")
	id := uuid.New()
	job.SessionID = &id
	require.Error(t, query.CronJob.WithContext(t.Context()).Create(job))
	job.SessionID = nil
	job.DefinitionHash = "short"
	require.Error(t, query.CronJob.WithContext(t.Context()).Create(job))
}
