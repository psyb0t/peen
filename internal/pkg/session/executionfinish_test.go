package session

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func finishActivityTestInput() ScheduleActivityInput {
	return ScheduleActivityInput{
		StepID: ExecutionFinishStepID, Name: ExecutionFinishActivityName,
		Kind:         models.ActivityKindPreparation,
		ReplayPolicy: models.ActivityReplayPolicyRetry,
		Input:        json.RawMessage(`{"final":true}`),
	}
}

func finishActivityTestResult() CompleteActivityInput {
	return CompleteActivityInput{
		Output:  json.RawMessage(`{"text":"finished"}`),
		Context: json.RawMessage(`{"messages":["final context"]}`),
		Finish: &FinishExecutionInput{
			State:            models.ExecutionStateCompleted,
			WorkingDirectory: "/example/changed",
		},
	}
}

func TestExecutionFinishRefusesAcceptedInboxAndRollsBackResult(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	accepted, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, inboxTestInput())
	require.NoError(t, err)
	before, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, finishActivityTestResult())
	require.ErrorIs(t, err, commerr.ErrConflict)
	require.ErrorIs(t, err, ErrExecutionInboxPending)
	activity, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateRunning, activity.State)
	assert.Nil(t, activity.OutputJSON)
	after, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	retained, err := fixture.store.GetExecutionMessageByRequest(t.Context(),
		fixture.sessionID, fixture.root.ID, accepted.Message.RequestID)
	require.NoError(t, err)
	assert.Equal(t, models.InboxMessageStateQueued, retained.State)
}

func TestExecutionFinishAllowsDeliveredInbox(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	_, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	requestIDs := []uuid.UUID{input.RequestID}
	model := inboxTestClaim(t, fixture, requestIDs)
	_, err = fixture.store.DeliverExecutionMessages(t.Context(),
		fixture.sessionID, DeliverExecutionMessagesInput{
			TurnLease: lease, ModelLease: model.Lease, RequestIDs: requestIDs,
		})
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, model.Lease, CompleteActivityInput{
			Output: json.RawMessage(`{}`), Context: json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, finishActivityTestResult())
	require.NoError(t, err)
	execution, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateCompleted, execution.State)
	_, err = fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, inboxTestInput())
	require.ErrorIs(t, err, commerr.ErrInvalidState)
}

func TestExecutionFinishFailureKeepsPendingInbox(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	input := inboxTestInput()
	_, err := fixture.store.AcceptExecutionMessage(t.Context(),
		fixture.root.ID, lease, input)
	require.NoError(t, err)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	result := finishActivityTestResult()
	result.Finish.State = models.ExecutionStateFailed
	result.Finish.FailureDetail = "round budget exhausted"
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, result)
	require.NoError(t, err)
	retained, err := fixture.store.GetExecutionMessageByRequest(t.Context(),
		fixture.sessionID, fixture.root.ID, input.RequestID)
	require.NoError(t, err)
	assert.Equal(t, models.InboxMessageStateQueued, retained.State)
}

func TestExecutionFinishRacingAcceptanceCannotStrandMessage(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	lease := inboxTestLease(t, fixture)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	start := make(chan struct{})
	var group sync.WaitGroup
	var accepted *ExecutionInboxAcceptance
	var acceptErr, finishErr error

	group.Go(func() {
		<-start
		accepted, acceptErr = fixture.store.AcceptExecutionMessage(t.Context(),
			fixture.root.ID, lease, inboxTestInput())
	})
	group.Go(func() {
		<-start
		_, finishErr = fixture.store.CompleteExecutionActivity(t.Context(),
			fixture.sessionID, claim.Lease, finishActivityTestResult())
	})
	close(start)
	group.Wait()
	execution, err := fixture.store.GetExecution(t.Context(), fixture.sessionID,
		fixture.root.ID)
	require.NoError(t, err)
	if finishErr == nil {
		require.ErrorIs(t, acceptErr, commerr.ErrInvalidState)
		assert.Nil(t, accepted)
		assert.Equal(t, models.ExecutionStateCompleted, execution.State)

		return
	}

	require.ErrorIs(t, finishErr, commerr.ErrConflict)
	require.NoError(t, acceptErr)
	require.NotNil(t, accepted)
	assert.Equal(t, models.ExecutionStateRunning, execution.State)
	assert.Equal(t, models.InboxMessageStateQueued, accepted.Message.State)
}

func TestExecutionFinishReusesAfterReopen(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	activity, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateCompleted, activity.State)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	require.NoError(t, fixture.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{Directory: fixture.directory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store := newTestStore(t, handle)
	execution, err := store.GetExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateCompleted, execution.State)
	assert.Equal(t, string(input.Context), execution.ContextJSON)
	assert.Equal(t, input.Finish.WorkingDirectory, execution.WorkingDirectory)
	assert.NotNil(t, execution.CompletedAt)
	assert.EqualValues(t, 5, execution.EventCount)
	reused, err := store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	assert.Equal(t, ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, activity.ID, reused.Activity.ID)
	input.Finish.State = models.ExecutionStateFailed
	input.Finish.FailureDetail = "different outcome"
	_, err = store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestExecutionFinishCannotOverridePause(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	generationID := uuid.New()
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, generationID, finishActivityTestInput())
	require.NoError(t, err)
	_, err = fixture.store.PauseExecution(t.Context(),
		fixture.sessionID, fixture.root.ID, executionTestPauseReason)
	require.NoError(t, err)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, finishActivityTestResult())
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.NotErrorIs(t, err, ErrExecutionInboxPending)
	activity, err := fixture.store.GetExecutionActivity(t.Context(),
		fixture.sessionID, claim.Activity.ID)
	require.NoError(t, err)
	assert.Nil(t, activity.OutputJSON)
	require.NoError(t, fixture.store.InterruptExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease))
	_, err = fixture.store.ResumeExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	retry, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, generationID, finishActivityTestInput())
	require.NoError(t, err)
	assert.EqualValues(t, 2, retry.Lease.Attempt)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, finishActivityTestResult())
	require.ErrorIs(t, err, commerr.ErrConflict)
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, retry.Lease, finishActivityTestResult())
	require.NoError(t, err)
}

func TestExecutionFinishRecordsFailure(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim, err := fixture.store.BeginExecutionActivity(t.Context(),
		fixture.sessionID, fixture.root.ID, uuid.New(), finishActivityTestInput())
	require.NoError(t, err)
	input := finishActivityTestResult()
	input.Finish.State = models.ExecutionStateFailed
	input.Finish.FailureDetail = "example provider exhausted retries"
	_, err = fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, input)
	require.NoError(t, err)
	execution, err := fixture.store.GetExecution(t.Context(),
		fixture.sessionID, fixture.root.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateFailed, execution.State)
	assert.Equal(t, input.Finish.FailureDetail, execution.FailureDetail)
	events, err := fixture.store.ListExecutionEvents(t.Context(),
		fixture.sessionID, fixture.root.ID, ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, events.Items, 5)
	assert.Equal(t, string(ExecutionEventTypeFailed), events.Items[4].EventType)
}

func TestExecutionFinishRejectsOrdinaryActivity(t *testing.T) {
	t.Parallel()
	fixture := newExecutionFixture(t)
	claim := beginActivityForTest(t, fixture, uuid.New(), models.ActivityKindModel)
	_, err := fixture.store.CompleteExecutionActivity(t.Context(),
		fixture.sessionID, claim.Lease, finishActivityTestResult())
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
}

func TestExecutionFinishValidatesTerminalResult(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*CompleteActivityInput)
	}{
		{name: "running", change: func(input *CompleteActivityInput) {
			input.Finish.State = models.ExecutionStateRunning
		}},
		{name: "unknown", change: func(input *CompleteActivityInput) {
			input.Finish.State = models.ExecutionState("unknown")
		}},
		{name: "failure without reason", change: func(input *CompleteActivityInput) {
			input.Finish.State = models.ExecutionStateFailed
		}},
		{name: "success with failure", change: func(input *CompleteActivityInput) {
			input.Finish.FailureDetail = "example failure"
		}},
		{name: "missing terminal result", change: func(input *CompleteActivityInput) {
			input.Finish = nil
		}},
		{name: "large directory", change: func(input *CompleteActivityInput) {
			input.Finish.WorkingDirectory = strings.Repeat("a", 100)
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			fixture.store.maxStoredMessageBytes = 99
			claim, err := fixture.store.BeginExecutionActivity(t.Context(),
				fixture.sessionID, fixture.root.ID, uuid.New(),
				finishActivityTestInput())
			require.NoError(t, err)
			input := finishActivityTestResult()
			tc.change(&input)
			_, err = fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			execution, err := fixture.store.GetExecution(t.Context(),
				fixture.sessionID, fixture.root.ID)
			require.NoError(t, err)
			assert.Equal(t, models.ExecutionStateRunning, execution.State)
		})
	}
}

func TestExecutionFinishRefusesUnsettledActivities(t *testing.T) {
	t.Parallel()
	testCases := []models.ActivityState{
		models.ActivityStateScheduled, models.ActivityStateRunning,
		models.ActivityStatePaused, models.ActivityStateUncertain,
		models.ActivityStateFailed,
	}
	for _, tc := range testCases {
		t.Run(string(tc), func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionFixture(t)
			other := beginActivityForTest(t, fixture, uuid.New(),
				models.ActivityKindModel)
			repository := fixture.store.query.ExecutionActivity
			_, err := repository.WithContext(t.Context()).Where(
				repository.ID.Eq(other.Activity.ID),
			).UpdateSimple(repository.State.Value(string(tc)))
			require.NoError(t, err)
			claim, err := fixture.store.BeginExecutionActivity(t.Context(),
				fixture.sessionID, fixture.root.ID, uuid.New(),
				finishActivityTestInput())
			require.NoError(t, err)
			_, err = fixture.store.CompleteExecutionActivity(t.Context(),
				fixture.sessionID, claim.Lease, finishActivityTestResult())
			require.ErrorIs(t, err, commerr.ErrConflict)
			activity, err := fixture.store.GetExecutionActivity(t.Context(),
				fixture.sessionID, claim.Activity.ID)
			require.NoError(t, err)
			assert.Equal(t, models.ActivityStateRunning, activity.State)
			assert.Nil(t, activity.OutputJSON)
			execution, err := fixture.store.GetExecution(t.Context(),
				fixture.sessionID, fixture.root.ID)
			require.NoError(t, err)
			assert.Equal(t, models.ExecutionStateRunning, execution.State)
			assert.Nil(t, execution.CompletedAt)
			assert.EqualValues(t, 5, execution.EventCount)
			assert.Equal(t, executionTestContext, execution.ContextJSON)
		})
	}
}
