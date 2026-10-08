package protocol

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newControllerFixture(t *testing.T) (*Controller, uuid.UUID) {
	t.Helper()

	ctx := context.Background()

	handle, err := db.Open(ctx, db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = handle.Close() })

	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)

	opened, err := store.CreateOrResume(ctx, nil, session.OpenSessionOptions{})
	require.NoError(t, err)

	controller := NewController(store, nil, opened.Session.ID, uuid.New())

	return controller, opened.Session.ID
}

func mustEncode(t *testing.T, value any) json.RawMessage {
	t.Helper()

	encoded, err := json.Marshal(value)
	require.NoError(t, err)

	return encoded
}

type recordingPublisher struct {
	sessionIDs []uuid.UUID
	events     []session.EventInput
}

func (p *recordingPublisher) PublishSessionEvents(
	_ context.Context,
	sessionID uuid.UUID,
	events []session.EventInput,
) {
	p.sessionIDs = append(p.sessionIDs, sessionID)
	p.events = append(p.events, events...)
}

// A turn's opening events, such as the user's own message, are written by
// AcquireTurn rather than a checkpoint. They must still reach live clients,
// and only after the database holds them.
func TestControllerPublishesTurnStartEventsAfterTheWrite(t *testing.T) {
	ctx := context.Background()

	handle, err := db.Open(ctx, db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = handle.Close() })

	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)

	opened, err := store.CreateOrResume(ctx, nil, session.OpenSessionOptions{})
	require.NoError(t, err)

	publisher := &recordingPublisher{}
	controller := NewController(store, publisher, opened.Session.ID, uuid.New())

	requestID := uuid.New()
	opening := session.EventInput{
		RequestID:   requestID,
		EventType:   "user_message.created",
		PayloadJSON: `{"message":"make a script"}`,
	}

	result, err := controller.HandleCall(
		ctx,
		string(MethodAcquireTurn),
		mustEncode(t, InputRequest[session.StartTurnInput]{
			SessionID: opened.Session.ID,
			Input: session.StartTurnInput{
				RequestID: requestID,
				Workspace: opened.Session.Workspace,
				Events:    []session.EventInput{opening},
			},
		}),
	)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Len(t, publisher.events, 1)
	assert.Equal(t, []uuid.UUID{opened.Session.ID}, publisher.sessionIDs)
	assert.Equal(t, opening.EventType, publisher.events[0].EventType)
	assert.Equal(t, requestID, publisher.events[0].RequestID)

	stored, err := store.ListEvents(ctx, opened.Session.ID, session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, stored.Items, 1)
	assert.Equal(t, opening.EventType, stored.Items[0].EventType)
}

// The private socket is only a real boundary because every request is checked
// against the session the connection registered for. Without this check a
// worker holding one session's socket could read another session's transcript.
func TestControllerRefusesAnotherSessionsRequest(t *testing.T) {
	ctx := context.Background()
	controller, sessionID := newControllerFixture(t)

	result, err := controller.HandleCall(
		ctx,
		string(MethodGetSession),
		mustEncode(t, SessionRequest{SessionID: uuid.New()}),
	)
	require.Error(t, err)
	assert.Nil(t, result)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)

	// The same call for its own session is served, which proves the refusal
	// above came from the guard and not from a broken handler.
	own, err := controller.HandleCall(
		ctx,
		string(MethodGetSession),
		mustEncode(t, SessionRequest{SessionID: sessionID}),
	)
	require.NoError(t, err)
	assert.NotNil(t, own)
}

// The guard has to hold on every published method, not only the first one, so
// a new handler cannot quietly skip it.
func TestControllerRefusesAnotherSessionOnEveryPublishedRead(t *testing.T) {
	ctx := context.Background()
	controller, _ := newControllerFixture(t)
	foreign := uuid.New()
	child := uuid.New()

	testCases := []struct {
		name    string
		method  Method
		payload any
	}{
		{
			name:    "completed history",
			method:  MethodCompletedHistory,
			payload: SessionRequest{SessionID: foreign},
		},
		{
			name:   "messages",
			method: MethodListMessages,
			payload: OptionsRequest[session.ListMessagesOptions]{
				SessionID: foreign,
			},
		},
		{
			name:   "events",
			method: MethodListEvents,
			payload: OptionsRequest[session.ListEventsOptions]{
				SessionID: foreign,
			},
		},
		{
			name:   "turns",
			method: MethodListTurns,
			payload: OptionsRequest[session.ListTurnsOptions]{
				SessionID: foreign,
			},
		},
		{
			name:   "jobs",
			method: MethodListJobs,
			payload: OptionsRequest[session.ListJobsOptions]{
				SessionID: foreign,
			},
		},
		{
			name:   "agent runs",
			method: MethodListAgentRuns,
			payload: OptionsRequest[session.ListAgentRunsOptions]{
				SessionID: foreign,
			},
		},
		{
			name:   "job output",
			method: MethodListJobOutput,
			payload: ChildOptionsRequest[session.ListJobOutputOptions]{
				SessionID: foreign,
				ChildID:   child,
			},
		},
		{
			name:    "cancel",
			method:  MethodCancelSession,
			payload: SessionRequest{SessionID: foreign},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := controller.HandleCall(
				ctx,
				string(tc.method),
				mustEncode(t, tc.payload),
			)
			require.Error(t, err)
			assert.Nil(t, result)
			require.ErrorIs(
				t,
				err,
				commerr.ErrPermissionDenied,
				"%s must be refused for a foreign session",
				tc.method,
			)
		})
	}
}

// A method the controller does not publish is refused rather than ignored, so a
// worker built against a newer protocol fails loudly instead of silently
// receiving a nil answer it would treat as an empty result.
func TestControllerRefusesAnUnpublishedMethod(t *testing.T) {
	controller, sessionID := newControllerFixture(t)

	result, err := controller.HandleCall(
		context.Background(),
		"session.invent",
		mustEncode(t, SessionRequest{SessionID: sessionID}),
	)
	require.Error(t, err)
	assert.Nil(t, result)
	require.ErrorIs(t, err, commerr.ErrNotImplemented)
}

// A payload that does not decode is an error, never a zero-valued request that
// would then pass the session guard by naming the nil UUID.
func TestControllerRefusesAnUndecodablePayload(t *testing.T) {
	controller, _ := newControllerFixture(t)

	result, err := controller.HandleCall(
		context.Background(),
		string(MethodGetSession),
		json.RawMessage(`"not an object"`),
	)
	require.Error(t, err)
	assert.Nil(t, result)
}

// Commands travel controller to worker only. One arriving here is a protocol
// violation, and answering it would let a worker drive its own controller.
func TestControllerRefusesEveryCommandFromAWorker(t *testing.T) {
	controller, _ := newControllerFixture(t)

	for _, command := range []Command{
		CommandRunTurn,
		CommandCancel,
		CommandShutdown,
	} {
		result, err := controller.HandleCommand(
			context.Background(),
			command,
			nil,
		)
		require.Error(t, err, "command %q must be refused", command)
		assert.Nil(t, result)
		require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	}
}
