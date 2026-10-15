package session

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInterruptedToolRequiresReconciliation(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	first := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)
	unknown := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)
	assert.Equal(t, ActivityDecisionReconcile, unknown.Decision)
	assert.Equal(t, models.ActivityStateUncertain, unknown.Activity.State)
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, first.Lease, activityTestResult())
	require.ErrorIs(t, err, commerr.ErrConflict)
	resolution := ResolveActivityInput{
		Attempt: first.Lease.Attempt, Resolution: ActivityResolutionRetry,
		Evidence: activityTestEvidence,
	}
	_, err = fixture.store.ResolveExecutionActivity(t.Context(),
		fixture.sessionID, first.Activity.ID, resolution)
	require.NoError(t, err)
	_, err = fixture.store.ResolveExecutionActivity(t.Context(),
		fixture.sessionID, first.Activity.ID, resolution)
	require.NoError(t, err)
	retry := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)
	assert.Equal(t, ActivityDecisionExecute, retry.Decision)
	assert.EqualValues(t, 2, retry.Lease.Attempt)
	require.NoError(t, fixture.store.InterruptExecutionActivity(t.Context(),
		fixture.sessionID, retry.Lease))
	_, err = fixture.store.ResolveExecutionActivity(t.Context(),
		fixture.sessionID, retry.Activity.ID, resolution)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestAcceptingInspectedResultPreservesPause(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)
	_, err := fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	require.NoError(t, fixture.store.InterruptExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease))
	accepted, err := fixture.store.ResolveExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID, ResolveActivityInput{
			Attempt: claim.Lease.Attempt, Resolution: ActivityResolutionAccept,
			Evidence: activityTestEvidence,
			Output:   json.RawMessage(activityTestOutputJSON),
			Context:  json.RawMessage(activityTestContextJSON),
		})
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateCompleted, accepted.State)
	stored, err := fixture.store.GetExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, stored.State)
	_, err = fixture.store.ResumeExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	reused := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)
	assert.Equal(t, ActivityDecisionReuse, reused.Decision)
	require.NotNil(t, reused.Activity.OutputJSON)
	assert.Equal(t, activityTestOutputJSON, *reused.Activity.OutputJSON)
}

func TestResolutionRequiresEvidence(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)
	require.NoError(t, fixture.store.InterruptExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease))
	_, err := fixture.store.ResolveExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID, ResolveActivityInput{
			Attempt: claim.Lease.Attempt, Resolution: ActivityResolutionRetry,
		})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}
