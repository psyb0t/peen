package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pendingFinishFixture(t *testing.T) (
	executionFixture, *ActivityClaim, CompleteActivityInput,
) {
	t.Helper()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	_, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, inboxTestInput())
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.PendingOutput = json.RawMessage(`{"continue":true}`)

	return fixture, claim, input
}

func TestFinishDecisionReusesContinuationAfterReopen(t *testing.T) {
	t.Parallel()
	fixture, claim, input := pendingFinishFixture(t)
	activity, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	assert.Equal(t, string(input.Finish.PendingOutput), *activity.OutputJSON)
	assert.Equal(t, models.ActivityStateCompleted, activity.State)
	execution, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateRunning, execution.State)
	assert.Nil(t, execution.CompletedAt)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	reused, err := store.BeginExecutionActivity(t.Context(), fixture.sessionID,
		fixture.root.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	assert.Equal(t, ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, activity, reused.Activity)
	_, err = store.PauseExecution(t.Context(), fixture.sessionID,
		fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	repeated, err := store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	assert.Equal(t, activity, repeated)
	execution, err = store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, execution.State)
}

func TestFinishDecisionRejectsChangedCompletionRequest(t *testing.T) {
	t.Parallel()
	fixture, claim, original := pendingFinishFixture(t)
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, original)
	require.NoError(t, err)
	testCases := []struct {
		name   string
		change func(*CompleteActivityInput)
	}{
		{name: "terminal output", change: func(input *CompleteActivityInput) {
			input.Output = json.RawMessage(`{"changed":true}`)
		}},
		{name: "terminal output whitespace", change: func(input *CompleteActivityInput) {
			input.Output = append(input.Output, ' ')
		}},
		{name: "continuation output", change: func(input *CompleteActivityInput) {
			input.Finish.PendingOutput = json.RawMessage(`{"changed":true}`)
		}},
		{name: "context", change: func(input *CompleteActivityInput) {
			input.Context = json.RawMessage(`{"changed":true}`)
		}},
		{name: "directory", change: func(input *CompleteActivityInput) {
			input.Finish.WorkingDirectory = "/another"
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := original
			finish := *original.Finish
			input.Finish = &finish
			tc.change(&input)
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, commerr.ErrConflict)
		})
	}
}

func TestFinishDecisionValidatesAlternativeOutput(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*FinishExecutionInput)
	}{
		{name: "malformed", change: func(input *FinishExecutionInput) {
			input.PendingOutput = json.RawMessage(`{`)
		}},
		{name: "oversized", change: func(input *FinishExecutionInput) {
			encoded, err := json.Marshal(strings.Repeat("x", 100))
			require.NoError(t, err)
			input.PendingOutput = encoded
		}},
		{name: "failure", change: func(input *FinishExecutionInput) {
			input.State = models.ExecutionStateFailed
			input.FailureDetail = "example failure"
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, claim, input := pendingFinishFixture(t)
			fixture.store.maxStoredMessageBytes = 99
			tc.change(input.Finish)
			_, err := fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestFinishDecisionHashRequiresOriginalFinish(t *testing.T) {
	t.Parallel()
	_, err := finishDecisionHash(CompleteActivityInput{})
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}

func TestFinishDecisionWithoutPendingInputKeepsTerminalResult(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.PendingOutput = json.RawMessage(`{"continue":true}`)
	activity, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	assert.Equal(t, string(input.Output), *activity.OutputJSON)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	input.Finish.PendingOutput = json.RawMessage(`{"different":true}`)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}
