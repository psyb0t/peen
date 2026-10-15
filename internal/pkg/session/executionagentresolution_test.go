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

func agentInspectionFixture(t *testing.T) (executionFixture, *models.Execution, *ActivityClaim, *ActivityClaim) {
	t.Helper()
	fixture := newExecutionFixture(t)
	target := executionChildForTest(t, fixture, fixture.root)
	unknown, err := fixture.store.BeginExecutionActivity(t.Context(), fixture.sessionID, target.ID, uuid.New(), activityTestInput(models.ActivityKindTool))
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(), fixture.sessionID, target.ID, executionTestPauseReason)
	require.NoError(t, err)
	require.NoError(t, fixture.store.InterruptExecutionActivity(t.Context(), fixture.sessionID, unknown.Lease))
	source := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindTool)

	return fixture, target, unknown, source
}

func TestAgentInspectionPreservesPauseAndResolutionProvenance(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		resolution ActivityResolution
		state      models.ActivityState
		decision   ActivityDecision
	}{
		{name: "retry", resolution: ActivityResolutionRetry, state: models.ActivityStateScheduled, decision: ActivityDecisionExecute},
		{name: "accept", resolution: ActivityResolutionAccept, state: models.ActivityStateCompleted, decision: ActivityDecisionReuse},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, target, unknown, source := agentInspectionFixture(t)
			input := ResolveExecutionControlInput{
				SourceLease: source.Lease, ControlRevision: 1,
				ResolveActivityInput: ResolveActivityInput{Attempt: unknown.Lease.Attempt, Resolution: tc.resolution, Evidence: activityTestEvidence},
			}
			if tc.resolution == ActivityResolutionAccept {
				input.Output = json.RawMessage(activityTestOutputJSON)
				input.Context = json.RawMessage(activityTestContextJSON)
			}
			for range 2 {
				result, err := fixture.store.ResolveExecutionActivityFromActivity(t.Context(), fixture.sessionID, unknown.Activity.ID, input)
				require.NoError(t, err)
				assert.Equal(t, tc.state, result.State)
			}
			stored, err := fixture.store.GetExecution(t.Context(), fixture.sessionID, target.ID)
			require.NoError(t, err)
			assert.Equal(t, models.ExecutionStatePaused, stored.State)
			assert.EqualValues(t, 1, stored.ControlRevision)
			events, err := fixture.store.ListExecutionEvents(t.Context(), fixture.sessionID, target.ID, ListEventsOptions{})
			require.NoError(t, err)
			resolved := 0
			for _, event := range events.Items {
				if event.EventType != string(ExecutionEventTypeStepResolved) {
					continue
				}
				var origin executionControlOrigin
				require.NoError(t, json.Unmarshal([]byte(event.PayloadJSON), &origin))
				assert.Equal(t, fixture.root.ID, origin.SourceExecutionID)
				assert.Equal(t, source.Lease, origin.SourceLease)
				assert.EqualValues(t, 1, origin.ControlRevision)
				resolved++
			}
			assert.Equal(t, 1, resolved)
			paused, err := fixture.store.BeginExecutionActivity(t.Context(), fixture.sessionID, target.ID, uuid.New(), activityTestInput(models.ActivityKindTool))
			require.NoError(t, err)
			assert.Equal(t, ActivityDecisionPaused, paused.Decision)
			_, err = fixture.store.ResumeExecutionFromActivity(t.Context(), fixture.sessionID, target.ID, ResumeExecutionInput{SourceLease: source.Lease, ControlRevision: 1})
			require.NoError(t, err)
			next, err := fixture.store.BeginExecutionActivity(t.Context(), fixture.sessionID, target.ID, uuid.New(), activityTestInput(models.ActivityKindTool))
			require.NoError(t, err)
			assert.Equal(t, tc.decision, next.Decision)
			if tc.resolution == ActivityResolutionAccept {
				require.NotNil(t, next.Activity.OutputJSON)
				assert.Equal(t, activityTestOutputJSON, *next.Activity.OutputJSON)
				assert.Equal(t, unknown.Lease.Attempt, next.Activity.Attempt)
			} else {
				assert.Equal(t, unknown.Lease.Attempt+1, next.Activity.Attempt)
			}
		})
	}
}

func TestAgentInspectionRefusesInvalidOrReplacedDecision(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		change  func(*ResolveExecutionControlInput)
		wantErr error
	}{
		{name: "changed revision", wantErr: commerr.ErrConflict, change: func(input *ResolveExecutionControlInput) { input.ControlRevision++ }},
		{name: "changed attempt", wantErr: commerr.ErrConflict, change: func(input *ResolveExecutionControlInput) { input.Attempt++ }},
		{name: "foreign generation", wantErr: commerr.ErrPermissionDenied, change: func(input *ResolveExecutionControlInput) { input.SourceLease.GenerationID = uuid.New() }},
		{name: "no evidence", wantErr: commerr.ErrValidationFailed, change: func(input *ResolveExecutionControlInput) { input.Evidence = "" }},
		{name: "unknown decision", wantErr: commerr.ErrValidationFailed, change: func(input *ResolveExecutionControlInput) { input.Resolution = "unknown" }},
		{name: "retry with output", wantErr: commerr.ErrValidationFailed, change: func(input *ResolveExecutionControlInput) { input.Output = json.RawMessage(`{}`) }},
		{name: "accept without output", wantErr: commerr.ErrValidationFailed, change: func(input *ResolveExecutionControlInput) { input.Resolution = ActivityResolutionAccept }},
		{name: "negative revision", wantErr: commerr.ErrValidationFailed, change: func(input *ResolveExecutionControlInput) { input.ControlRevision = -1 }},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, target, unknown, source := agentInspectionFixture(t)
			input := ResolveExecutionControlInput{
				SourceLease: source.Lease, ControlRevision: 1,
				ResolveActivityInput: ResolveActivityInput{Attempt: unknown.Lease.Attempt, Resolution: ActivityResolutionRetry, Evidence: activityTestEvidence},
			}
			tc.change(&input)
			_, err := fixture.store.ResolveExecutionActivityFromActivity(t.Context(), fixture.sessionID, unknown.Activity.ID, input)
			require.ErrorIs(t, err, tc.wantErr)
			stored, err := fixture.store.GetExecutionActivity(t.Context(), fixture.sessionID, unknown.Activity.ID)
			require.NoError(t, err)
			assert.Equal(t, models.ActivityStateUncertain, stored.State)
			assert.Equal(t, unknown.Lease.Attempt, stored.Attempt)
			_, err = fixture.store.ResolveExecutionActivityFromActivity(t.Context(), fixture.sessionID, uuid.New(), ResolveExecutionControlInput{SourceLease: source.Lease, ControlRevision: 1, ResolveActivityInput: ResolveActivityInput{Attempt: 1, Resolution: ActivityResolutionRetry, Evidence: activityTestEvidence}})
			require.ErrorIs(t, err, commerr.ErrNotFound)
			current, err := fixture.store.GetExecution(t.Context(), fixture.sessionID, target.ID)
			require.NoError(t, err)
			assert.Equal(t, models.ExecutionStatePaused, current.State)
		})
	}
}

func TestAgentInspectionAfterStopLeavesUnknownEffectUnresolved(t *testing.T) {
	t.Parallel()
	fixture, _, unknown, source := agentInspectionFixture(t)
	_, err := fixture.store.Cancel(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	_, err = fixture.store.ResolveExecutionActivityFromActivity(t.Context(), fixture.sessionID, unknown.Activity.ID, ResolveExecutionControlInput{
		SourceLease: source.Lease, ControlRevision: 1,
		ResolveActivityInput: ResolveActivityInput{Attempt: unknown.Lease.Attempt, Resolution: ActivityResolutionRetry, Evidence: activityTestEvidence},
	})
	require.ErrorIs(t, err, commerr.ErrConflict)
	stored, err := fixture.store.GetExecutionActivity(t.Context(), fixture.sessionID, unknown.Activity.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateUncertain, stored.State)
}
