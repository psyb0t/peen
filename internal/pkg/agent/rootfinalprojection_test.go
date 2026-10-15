package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRootFinalProjectionUsesStableFailureClassification(t *testing.T) {
	t.Parallel()
	fixture, _, _ := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	input := callbackFinishInput(fixture)
	input.Result.Finish.State = models.ExecutionStateFailed
	input.Result.Finish.FailureDetail = "example provider diagnostic"
	result, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	turn, err := fixture.base.store.GetExecutionRootTurn(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, failureClassAgentRun, turn.FailureClassification)
	events := result.Output.Finish.Turn.Events
	require.NotEmpty(t, events)
	var payload turnFailedPayload
	require.NoError(t, json.Unmarshal(
		[]byte(events[len(events)-1].PayloadJSON), &payload))
	assert.Equal(t, failureClassAgentRun, payload.Reason)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, input.Result.Finish.FailureDetail, execution.FailureDetail)
}

type rootFinalReleaseStore struct {
	*session.Store
	released []session.Lease
}

func (s *rootFinalReleaseStore) ReleaseTurn(lease session.Lease) {
	s.released = append(s.released, lease)
	s.Store.ReleaseTurn(lease)
}

func TestRootFinalProjectionDeliversStoredEventAfterCommit(t *testing.T) {
	t.Parallel()
	fixture, callbacks, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	store := &rootFinalReleaseStore{Store: fixture.base.store}
	shared.prepared.turn.store = store
	var observed, delivered atomic.Int32
	shared.prepared.turn.observer = func(event Event) {
		if event.Type == EventTypeTurnCompleted {
			observed.Add(1)
		}
	}
	shared.prepared.turn.sink = func(event Event) error {
		if event.Type == EventTypeTurnCompleted {
			delivered.Add(1)
			assert.False(t, fixture.base.store.IsActive(fixture.input.SessionID))
			assert.Equal(t, shared.prepared.turn.requestID, event.RequestID)
			assert.Equal(t, shared.prepared.turn.triggeringEventID,
				event.TriggeringEventID)
		}

		return nil
	}
	input := callbackFinishInput(fixture)
	result, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.EqualValues(t, 1, observed.Load())
	assert.EqualValues(t, 1, delivered.Load())
	assert.Equal(t, []session.Lease{shared.lease}, store.released)
	assert.True(t, shared.prepared.turn.pendingTranscript().isEmpty())
	require.NoError(t, callbacks.afterFinish(t.Context(), input, result.Output))
	assert.EqualValues(t, 1, delivered.Load())
	assertRootFinalEvent(t, fixture, EventTypeTurnCompleted)
}

func TestRootFinalProjectionMarksFailedMessagesWithoutAnotherWrite(
	t *testing.T,
) {
	t.Parallel()
	fixture, _, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	shared.prepared.turn.appendMessage(session.MessageInput{
		Role: models.MessageRoleAssistant, Content: "unfinished answer",
	})
	input := callbackFinishInput(fixture)
	input.Result.Finish.State = models.ExecutionStateFailed
	input.Result.Finish.FailureDetail = "example failure"
	_, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	for _, message := range shared.prepared.turn.messageSnapshot() {
		assert.Equal(t, message.Role != models.MessageRoleUser,
			message.Incomplete)
	}
	assert.True(t, shared.prepared.turn.pendingTranscript().isEmpty())
	require.NoError(t, shared.prepared.turn.checkpoint(t.Context()))
	assertRootFinalEvent(t, fixture, EventTypeTurnFailed)
}

func TestRetainedRootFinalEventRefusesChangedIdentity(t *testing.T) {
	t.Parallel()
	fixture, callbacks, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	input := callbackFinishInput(fixture)
	original, err := callbacks.beforeFinish(t.Context(), input)
	require.NoError(t, err)
	testCases := []struct {
		name   string
		change func(*session.FinalizeTurnInput)
	}{
		{name: "missing", change: func(turn *session.FinalizeTurnInput) {
			turn.Events = nil
		}},
		{name: "kind", change: func(turn *session.FinalizeTurnInput) {
			turn.Events[0].EventType = EventTypeTurnFailed
		}},
		{name: "request", change: func(turn *session.FinalizeTurnInput) {
			turn.Events[0].RequestID = uuid.New()
		}},
		{name: "json", change: func(turn *session.FinalizeTurnInput) {
			turn.Events[0].PayloadJSON = "invalid JSON"
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := original
			turn := *original.Finish.Turn
			turn.Events = append([]session.EventInput(nil), turn.Events...)
			result.Finish.Turn = &turn
			tc.change(&turn)
			_, err := retainedRootFinalEvent(shared.prepared, result)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestRootFinalProjectionRetainsUndeliveredEventDuringCancellation(
	t *testing.T,
) {
	t.Parallel()
	fixture, callbacks, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	fixture.engine.options.AfterFinish = nil
	input := callbackFinishInput(fixture)
	result, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	var delivered atomic.Int32
	shared.prepared.turn.sink = func(Event) error {
		delivered.Add(1)

		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, projectRootFinalEvent(ctx, shared, result.Output),
		context.Canceled)
	assert.Zero(t, delivered.Load())
	require.NoError(t, callbacks.afterFinish(t.Context(), input, result.Output))
	assert.EqualValues(t, 1, delivered.Load())
	assertRootFinalEvent(t, fixture, EventTypeTurnCompleted)
}

func TestRootFinalProjectionRetriesSinkWithoutRepeatingObserver(t *testing.T) {
	t.Parallel()
	fixture, _, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	var observed, delivered atomic.Int32
	shared.prepared.turn.observer = func(event Event) {
		if event.Type == EventTypeTurnCompleted {
			observed.Add(1)
		}
	}
	shared.prepared.turn.sink = func(event Event) error {
		if event.Type == EventTypeTurnCompleted && delivered.Add(1) == 1 {
			return ctxerrors.Wrap(commerr.ErrConflict,
				"example final sink interruption")
		}

		return nil
	}
	input := callbackFinishInput(fixture)
	_, err := fixture.engine.finishExecution(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	_, err = fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.EqualValues(t, 1, observed.Load())
	assert.EqualValues(t, 2, delivered.Load())
	assert.True(t, shared.prepared.turn.pendingTranscript().isEmpty())
	assertRootFinalEvent(t, fixture, EventTypeTurnCompleted)
}

func TestRootFinalProjectionDoesNotDeliverOrReleaseContinuation(t *testing.T) {
	t.Parallel()
	fixture, _, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	store := &rootFinalReleaseStore{Store: fixture.base.store}
	shared.prepared.turn.store = store
	var delivered atomic.Int32
	shared.prepared.turn.sink = func(event Event) error {
		if event.Type == EventTypeTurnCompleted ||
			event.Type == EventTypeTurnFailed {
			delivered.Add(1)
		}

		return nil
	}
	require.NoError(t, shared.prepared.userMessages.enqueue(t.Context(),
		TurnRequest{RequestID: uuid.New(), Message: "continue retained work"}))
	input := callbackFinishInput(fixture)
	input.Result.AllowContinuation = true
	result, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.True(t, result.Output.Continue)
	assert.Zero(t, delivered.Load())
	assert.Empty(t, store.released)
	assert.True(t, fixture.base.store.IsActive(fixture.input.SessionID))
	assertCallbackQueueClosed(t, shared, false)
}

func TestRootFinalProjectionRecoversObserverPanicWithoutRepeatingIt(
	t *testing.T,
) {
	t.Parallel()
	fixture, _, shared := newRootCallbackFixture(t,
		elelemtest.NewScriptedDriver())
	var observed, delivered atomic.Int32
	shared.prepared.turn.observer = func(event Event) {
		if event.Type == EventTypeTurnCompleted {
			observed.Add(1)
			panic("example observer interruption")
		}
	}
	shared.prepared.turn.sink = func(event Event) error {
		if event.Type == EventTypeTurnCompleted {
			delivered.Add(1)
		}

		return nil
	}
	input := callbackFinishInput(fixture)
	_, err := fixture.engine.finishExecution(t.Context(), input)
	require.Error(t, err)
	assert.Zero(t, delivered.Load())
	_, err = fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.EqualValues(t, 1, observed.Load())
	assert.EqualValues(t, 1, delivered.Load())
	assertRootFinalEvent(t, fixture, EventTypeTurnCompleted)
}

func TestRetainedRootFinalEventKeepsNonzeroOrigin(t *testing.T) {
	t.Parallel()
	requestID, sourceID := uuid.New(), uuid.New()
	prepared := &preparedTurn{turn: &runtimeTurn{
		requestID: requestID, triggeringEventID: sourceID,
	}}
	result := workflowFinishResult{Finish: session.FinishExecutionInput{
		State: models.ExecutionStateCompleted,
		Turn: &session.FinalizeTurnInput{Events: []session.EventInput{{
			RequestID: requestID, EventType: EventTypeTurnCompleted,
			PayloadJSON: `{}`,
		}}},
	}}
	event, err := retainedRootFinalEvent(prepared, result)
	require.NoError(t, err)
	assert.Equal(t, requestID, event.RequestID)
	assert.Equal(t, sourceID, event.TriggeringEventID)
}

func TestRootFinalProjectionSkipsLegacyAndAlreadyDeliveredResults(
	t *testing.T,
) {
	t.Parallel()
	testCases := []struct {
		name      string
		turn      *session.FinalizeTurnInput
		delivered bool
	}{
		{name: "legacy"},
		{
			name: "delivered", turn: &session.FinalizeTurnInput{},
			delivered: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			shared := &rootCallbackScope{finalDelivered: tc.delivered}
			result := workflowFinishResult{Finish: session.FinishExecutionInput{
				Turn: tc.turn,
			}}
			require.NoError(t, projectRootFinalEvent(t.Context(), shared, result))
			assert.Equal(t, tc.delivered, shared.finalDelivered)
			assert.False(t, shared.finalProjected)
		})
	}
}
