package session

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListSessionsWithUnfinishedExecutions(t *testing.T) {
	t.Parallel()

	fixture := newExecutionFixture(t)

	sessions, err := fixture.store.ListSessionsWithUnfinishedExecutions(
		t.Context())
	require.NoError(t, err)
	assert.Empty(t, sessions, "a queued execution has not started")

	_, err = fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(),
		ScheduleActivityInput{
			StepID: "model:0", Kind: models.ActivityKindModel,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Name:         "modelRound", Input: json.RawMessage(`{}`),
		})
	require.NoError(t, err)

	sessions, err = fixture.store.ListSessionsWithUnfinishedExecutions(
		t.Context())
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, fixture.sessionID, sessions[0].ID)

	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)

	sessions, err = fixture.store.ListSessionsWithUnfinishedExecutions(
		t.Context())
	require.NoError(t, err)
	assert.Empty(t, sessions)
}
