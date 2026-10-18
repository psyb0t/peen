package protocol

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testEventuallyTimeout bounds positive waits. Coverage and -race
// instrumentation under CI's CPU cap make a short window flake.
const testEventuallyTimeout = time.Minute

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

	controller := NewController(store, opened.Session.ID, uuid.New())

	return controller, opened.Session.ID
}

func mustEncode(t *testing.T, value any) json.RawMessage {
	t.Helper()

	encoded, err := json.Marshal(value)
	require.NoError(t, err)

	return encoded
}

// A turn's opening events, such as the user's own message, are written by
// AcquireTurn rather than a checkpoint. The store's feed signal, not this
// call, is what reaches live clients; this proves only that the write lands.
func TestControllerWritesTurnStartEvents(t *testing.T) {
	ctx := context.Background()

	handle, err := db.Open(ctx, db.Config{Directory: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = handle.Close() })

	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)

	opened, err := store.CreateOrResume(ctx, nil, session.OpenSessionOptions{})
	require.NoError(t, err)

	controller := NewController(store, opened.Session.ID, uuid.New())

	requestID := uuid.New()
	opening := session.EventInput{
		RequestID:   requestID,
		EventType:   "userMessage.created",
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

	stored, err := store.ListEvents(ctx, opened.Session.ID, session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, stored.Items, 1)
	assert.Equal(t, opening.EventType, stored.Items[0].EventType)
	assert.Equal(t, requestID, stored.Items[0].RequestID)
}

func TestControllerDirectoryStateIsSessionScoped(t *testing.T) {
	t.Parallel()
	controller, sessionID := newControllerFixture(t)
	result, err := controller.HandleCall(context.Background(), string(MethodGetDirectoryState),
		mustEncode(t, SessionRequest{SessionID: sessionID}))
	require.NoError(t, err)
	assert.Equal(t, &session.DirectoryState{}, result)
	_, err = controller.HandleCall(context.Background(), string(MethodGetDirectoryState),
		mustEncode(t, SessionRequest{SessionID: uuid.New()}))
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestControllerAcquisitionCanRecoverBeforeWorkerAcknowledgement(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		owner string
	}{
		{name: "owner omitted"},
		{name: "forged owner", owner: uuid.New().String()},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			controller, sessionID := newControllerFixture(t)
			ctx := context.Background()
			_, err := controller.HandleCall(ctx, string(MethodAcquireTurn),
				mustEncode(t, InputRequest[session.StartTurnInput]{
					SessionID: sessionID,
					Input: session.StartTurnInput{
						RequestID: uuid.New(), WorkerGenerationID: tc.owner,
					},
				}))
			require.NoError(t, err)
			count, err := controller.store.RecoverWorkerTurns(ctx, sessionID, controller.generationID)
			require.NoError(t, err)
			assert.Equal(t, 1, count)
			assert.False(t, controller.store.IsActive(sessionID))
			turns, err := controller.store.ListTurns(ctx, sessionID, session.ListTurnsOptions{})
			require.NoError(t, err)
			require.Len(t, turns.Items, 1)
			assert.Equal(t, controller.generationID.String(), turns.Items[0].WorkerGenerationID)
			assert.Equal(t, models.TurnStateInterrupted, turns.Items[0].State)
		})
	}
}

func TestControllerChildRecoveryRequiresTheCurrentSessionWorker(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name            string
		state           string
		wrongSession    bool
		wrongGeneration bool
		wantErr         error
	}{
		{name: "ready", state: "ready"},
		{name: "starting", state: "starting"},
		{name: "stopped", state: "stopped", wantErr: commerr.ErrPermissionDenied},
		{name: "failed", state: "failed", wantErr: commerr.ErrPermissionDenied},
		{name: "wrong session", state: "ready", wrongSession: true, wantErr: commerr.ErrPermissionDenied},
		{name: "forged owner", state: "ready", wrongGeneration: true, wantErr: commerr.ErrPermissionDenied},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			controller, sessionID := newControllerFixture(t)
			ctx := context.Background()
			_, err := controller.store.CreateWorkerGeneration(ctx, sessionID, session.CreateWorkerGenerationInput{
				ID: controller.generationID, Kind: "control", AgentSpace: "control", State: tc.state,
				Workspace: "/workspace", SocketPath: "/example/worker.sock", CredentialHash: "EXAMPLE-HASH",
			})
			require.NoError(t, err)
			generationID := controller.generationID
			if tc.wrongGeneration {
				generationID = uuid.New()
			}
			if tc.wrongSession {
				sessionID = uuid.New()
			}
			_, err = controller.HandleCall(ctx, string(MethodPrepareAgentRunRecovery), mustEncode(t, InputRequest[uuid.UUID]{SessionID: sessionID, Input: generationID}))
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
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
