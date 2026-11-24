package control_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/agent"
	"github.com/psyb0t/peen/internal/pkg/control"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/harness"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/worker/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (w *routedWorker) cancelExactTurn(
	ctx context.Context,
	payload json.RawMessage,
) (any, error) {
	var request protocol.CancelTurn
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, ctxerrors.Wrap(err, "decode fixture cancellation")
	}
	store := w.connection.Store()
	store.RequestLocalTurnCancellation(w.sessionID, request.TurnID)
	ended, err := store.EndRunningTurn(ctx, w.sessionID, request.TurnID,
		session.CancelledTurnEnd())
	if err != nil {
		return nil, ctxerrors.Wrap(err, "end fixture turn")
	}

	return protocol.CancelResult{CancelRequested: ended}, nil
}

func acquireConversationTurn(
	ctx context.Context,
	w *routedWorker,
	request protocol.RunTurn,
) (session.Lease, error) {
	message, err := json.Marshal(map[string]string{"message": request.Message})
	if err != nil {
		return session.Lease{}, ctxerrors.Wrap(err, "encode fixture input")
	}
	lease, err := w.connection.Store().AcquireTurn(ctx, w.sessionID,
		session.StartTurnInput{
			RequestID: request.RequestID, Workspace: w.workspace,
			ExpectedStopRevision: request.ExpectedStopRevision,
			Events: []session.EventInput{{
				RequestID:   request.RequestID,
				EventType:   agent.EventTypeUserMessageCreated,
				PayloadJSON: string(message),
			}},
		})
	if err != nil {
		return session.Lease{}, ctxerrors.Wrap(err, "admit fixture turn")
	}

	return lease, nil
}

func finishConversationTurn(
	ctx context.Context,
	w *routedWorker,
	request protocol.RunTurn,
) (any, error) {
	lease, err := acquireConversationTurn(ctx, w, request)
	if err != nil {
		return nil, err
	}
	//nolint:contextcheck // Lease release carries no context.
	defer w.connection.Store().ReleaseTurn(lease)
	output := json.RawMessage(`{"answer":7}`)
	err = w.connection.Store().FinalizeTurn(ctx, lease,
		session.FinalizeTurnInput{
			State: models.TurnStateCompleted, Output: output,
			Events: []session.EventInput{
				{
					RequestID:   request.RequestID,
					EventType:   "content_block_delta",
					PayloadJSON: `{"index":0,"delta":{"type":"text_delta","text":"answer"}}`,
				},
				{
					RequestID:   request.RequestID,
					EventType:   agent.EventTypeTurnCompleted,
					PayloadJSON: `{"model":"provider/model","text":"answer"}`,
				},
			},
		})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "finish fixture conversation")
	}

	return protocol.TurnResult{Text: routedAnswer, Output: output}, nil
}

func TestConversationRunsInTheWorkerAndDrainsTheDurableTail(t *testing.T) {
	fixture := newRoutingFixture(t)
	fixture.launcher.onTurn = finishConversationTurn
	conversation, err := control.NewConversation(fixture.registry,
		fixture.router, fixture.feed)
	require.NoError(t, err)
	input := control.ConversationRequest{
		RequestID: uuid.New(),
		Workspace: fixture.launcher.workspace,
		Message:   agent.MessageRequest{Message: routedMessage},
	}
	first, err := conversation.Run(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, input.RequestID, first.RequestID)
	assert.Equal(t, routedAnswer, first.Text)
	assert.EqualValues(t, models.TurnStateCompleted, first.State)
	assert.JSONEq(t, `{"answer":7}`, string(first.Output))
	input.Workspace = ""
	input.SessionID = fixture.sessionID
	input.RequestID = uuid.Nil
	conversation, err = control.NewConversation(fixture.registry,
		fixture.router, control.NewFeedPump(t.Context(), fixture.store))
	require.NoError(t, err)
	var observed []session.FeedItem
	input.OnEvent = func(item session.FeedItem) error {
		observed = append(observed, item)

		return nil
	}
	second, err := conversation.Run(t.Context(), input)
	require.NoError(t, err)
	require.Len(t, observed, 3)
	assert.NotEqual(t, first.RequestID, second.RequestID)
	for i, item := range observed {
		assert.Equal(t, second.RequestID, item.RequestID)
		assert.Equal(t, fixture.sessionID, item.SessionID)
		if i > 0 {
			assert.Greater(t, item.Sequence, observed[i-1].Sequence)
		}
	}
	assert.Equal(t, "content_block_delta", observed[1].EventType)
	for _, request := range fixture.launcher.workers[0].turns {
		assert.True(t, request.RequireNewTurn)
	}
}

func TestConversationCancelsItsLateAdmissionAfterTheCallerLeaves(t *testing.T) {
	fixture := newRoutingFixture(t)
	seen := make(chan protocol.RunTurn, 1)
	admit := make(chan struct{})
	releaseAdmission := sync.OnceFunc(func() { close(admit) })
	t.Cleanup(releaseAdmission)
	stopped := make(chan struct{})
	fixture.launcher.onTurn = func(
		ctx context.Context, w *routedWorker, request protocol.RunTurn,
	) (any, error) {
		seen <- request
		<-admit
		lease, err := acquireConversationTurn(ctx, w, request)
		if err != nil {
			return nil, err
		}
		//nolint:contextcheck // Lease release carries no context.
		defer w.connection.Store().ReleaseTurn(lease)
		local, cancel := context.WithCancel(ctx)
		defer cancel()
		w.connection.Store().RegisterCancellation(lease, cancel)
		<-local.Done()
		close(stopped)

		return nil, commerr.ErrCancelled
	}
	conversation, err := control.NewConversation(fixture.registry,
		fixture.router, fixture.feed)
	require.NoError(t, err)
	caller, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	finished := make(chan error, 1)
	go func() {
		_, runErr := conversation.Run(caller, control.ConversationRequest{
			SessionID: fixture.sessionID,
			Message:   agent.MessageRequest{Message: routedMessage},
		})
		finished <- runErr
	}()
	var request protocol.RunTurn
	select {
	case request = <-seen:
	case runErr := <-finished:
		t.Fatalf("conversation ended before dispatch: %v", runErr)
	case <-time.After(feedPumpWait):
		t.Fatal("conversation never reached the worker")
	}
	cancel()
	select {
	case runErr := <-finished:
		require.ErrorIs(t, runErr, context.Canceled)
	case <-time.After(feedPumpWait):
		t.Fatal("conversation did not release the disconnected caller")
	}
	releaseAdmission()
	select {
	case <-stopped:
	case <-time.After(feedPumpWait):
		t.Fatal("late admission was not cancelled")
	}
	require.EventuallyWithT(t, func(check *assert.CollectT) {
		turn, readErr := fixture.store.GetTurnByRequest(t.Context(),
			fixture.sessionID, request.RequestID)
		if assert.NoError(check, readErr) {
			assert.True(check, turn.CancelRequested)
			assert.Equal(check, models.TurnStateCancelled, turn.State)
		}
		assert.False(check, fixture.store.IsActive(fixture.sessionID))
	}, feedPumpWait, time.Millisecond)
}

func TestConversationBusyFailureCannotCancelAnotherRequest(t *testing.T) {
	fixture := newRoutingFixture(t)
	fixture.launcher.onTurn = finishConversationTurn
	requestID := uuid.New()
	lease, err := fixture.store.AcquireTurn(t.Context(), fixture.sessionID,
		session.StartTurnInput{RequestID: requestID})
	require.NoError(t, err)
	t.Cleanup(func() { fixture.store.ReleaseTurn(lease) })
	conversation, err := control.NewConversation(fixture.registry,
		fixture.router, fixture.feed)
	require.NoError(t, err)
	_, err = conversation.Run(t.Context(), control.ConversationRequest{
		SessionID: fixture.sessionID,
		Message:   agent.MessageRequest{Message: routedMessage},
	})
	require.ErrorIs(t, err, session.ErrSessionBusy)
	turn, err := fixture.store.GetTurnByRequest(t.Context(), fixture.sessionID,
		requestID)
	require.NoError(t, err)
	assert.False(t, turn.CancelRequested)
	assert.Equal(t, models.TurnStateRunning, turn.State)
}

func TestConversationRefusesAmbiguousAndPrivilegedRouting(t *testing.T) {
	fixture := newRoutingFixture(t)
	conversation, err := control.NewConversation(fixture.registry,
		fixture.router, fixture.feed)
	require.NoError(t, err)
	for _, input := range []control.ConversationRequest{
		{},
		{SessionID: fixture.sessionID, Workspace: "/workspace"},
		{SessionID: fixture.sessionID, AgentSpace: "control"},
		{SessionID: fixture.sessionID, Message: agent.MessageRequest{System: true}},
		{SessionID: fixture.sessionID, Message: agent.MessageRequest{To: &fixture.sessionID}},
		{SessionID: fixture.sessionID, Message: agent.MessageRequest{Origin: &agent.TurnOrigin{}}},
	} {
		_, err := conversation.Run(t.Context(), input)
		require.ErrorIs(t, err, commerr.ErrValidationFailed)
	}
	_, err = conversation.Run(t.Context(), control.ConversationRequest{
		Workspace: canonicalTempDir(t),
		Message:   agent.MessageRequest{Message: routedMessage},
	})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	outside, err := fixture.store.OpenWorkspace(t.Context(), canonicalTempDir(t),
		session.OpenSessionOptions{
			RootAgent: testRootAgent, ModelID: testDefaultModel,
		})
	require.NoError(t, err)
	_, err = conversation.Run(t.Context(), control.ConversationRequest{
		SessionID: outside.Session.ID,
		Message:   agent.MessageRequest{Message: routedMessage},
	})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	system, err := fixture.store.OpenWorkspace(t.Context(), canonicalTempDir(t),
		session.OpenSessionOptions{
			RootAgent: harness.SystemAgentName, ModelID: testDefaultModel,
		})
	require.NoError(t, err)
	_, err = conversation.Run(t.Context(), control.ConversationRequest{
		SessionID: system.Session.ID,
		Message:   agent.MessageRequest{Message: routedMessage},
	})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	assert.Empty(t, fixture.launcher.workers)
}

func TestConversationReturnsPersistedOutputErrors(t *testing.T) {
	fixture := newRoutingFixture(t)
	fixture.launcher.onTurn = func(
		ctx context.Context, w *routedWorker, request protocol.RunTurn,
	) (any, error) {
		lease, err := acquireConversationTurn(ctx, w, request)
		if err != nil {
			return nil, err
		}
		//nolint:contextcheck // Lease release carries no context.
		defer w.connection.Store().ReleaseTurn(lease)
		if err := w.connection.Store().FinalizeTurn(ctx, lease,
			session.FinalizeTurnInput{
				State:       models.TurnStateCompleted,
				OutputError: "schema attempts exhausted",
			}); err != nil {
			return nil, err
		}

		return protocol.TurnResult{Text: routedAnswer}, nil
	}
	conversation, err := control.NewConversation(fixture.registry,
		fixture.router, fixture.feed)
	require.NoError(t, err)
	result, err := conversation.Run(t.Context(), control.ConversationRequest{
		SessionID: fixture.sessionID,
		Message:   agent.MessageRequest{Message: routedMessage},
	})
	require.NoError(t, err)
	assert.Empty(t, result.Output)
	assert.Equal(t, "schema attempts exhausted", result.OutputError)
	_, err = control.NewConversation(nil, nil, nil)
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}

func TestConversationObserverFailureCancelsOnlyItsTurn(t *testing.T) {
	fixture := newRoutingFixture(t)
	stopped := make(chan struct{})
	fixture.launcher.onTurn = func(
		ctx context.Context, w *routedWorker, request protocol.RunTurn,
	) (any, error) {
		lease, err := acquireConversationTurn(ctx, w, request)
		if err != nil {
			return nil, err
		}
		//nolint:contextcheck // Lease release carries no context.
		defer w.connection.Store().ReleaseTurn(lease)
		local, cancel := context.WithCancel(ctx)
		defer cancel()
		w.connection.Store().RegisterCancellation(lease, cancel)
		<-local.Done()
		close(stopped)

		return nil, commerr.ErrCancelled
	}
	conversation, err := control.NewConversation(fixture.registry,
		fixture.router, fixture.feed)
	require.NoError(t, err)
	_, err = conversation.Run(t.Context(), control.ConversationRequest{
		SessionID: fixture.sessionID,
		Message:   agent.MessageRequest{Message: routedMessage},
		OnEvent: func(session.FeedItem) error {
			return commerr.ErrExecFailed
		},
	})
	require.ErrorIs(t, err, commerr.ErrExecFailed)
	select {
	case <-stopped:
	case <-time.After(feedPumpWait):
		t.Fatal("observer failure did not cancel its turn")
	}
}
