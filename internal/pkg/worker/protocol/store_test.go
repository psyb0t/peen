package protocol

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreAdmitsOpeningNoticesAtomicallyOverRPC(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	remote := NewStore(fixture.remote.conn, fixture.sessionID,
		fixture.controller.generationID)
	ids := make([]uuid.UUID, 0, 2)
	for range cap(ids) {
		notice, err := remote.CreateSessionNotice(t.Context(), fixture.sessionID,
			session.CreateSessionNoticeInput{
				Type: "app.ready", Source: "example", Summary: "ready",
				DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
			})
		require.NoError(t, err)
		ids = append(ids, notice.ID)
	}
	input := session.StartTurnInput{
		RequestID: uuid.New(), Workspace: rpcExecutionWorkspace,
		NoticeIDs: ids[:1],
		Messages: []session.MessageInput{{
			Role: models.MessageRoleUser, Content: "pending notice context",
		}},
	}
	_, err := remote.AcquireTurn(t.Context(), uuid.New(), input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	lease, err := remote.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.NoError(t, err)
	t.Cleanup(func() { remote.ReleaseTurn(lease) })
	page, err := remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, len(ids))
	assert.Equal(t, models.NoticeStateDelivered, page.Items[0].State)
	assert.Equal(t, models.NoticeStatePending, page.Items[1].State)
	assert.NotNil(t, page.Items[0].DeliveredAt)
	assert.Nil(t, page.Items[1].DeliveredAt)
}

func TestStorePendingNoticePagesOverRPC(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	remote := NewStore(fixture.remote.conn, fixture.sessionID,
		fixture.controller.generationID)
	create := func(count int) []uuid.UUID {
		t.Helper()
		ids := make([]uuid.UUID, 0, count)
		for range count {
			notice, err := remote.CreateSessionNotice(t.Context(), fixture.sessionID,
				session.CreateSessionNoticeInput{
					Type: "app.ready", Source: "example", Summary: "ready",
					DataJSON: `{}`, Delivery: models.NoticeDeliveryQueue,
				})
			require.NoError(t, err)
			ids = append(ids, notice.ID)
		}

		return ids
	}
	create(3)
	_, err := remote.DrainSessionNotices(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	pending := create(13)
	got := make([]uuid.UUID, 0, len(pending))
	for index, offset := range []int{0, 5, 10, 13, 100} {
		page, err := remote.ListSessionNotices(t.Context(), fixture.sessionID,
			session.ListSessionNoticesOptions{
				State: models.NoticeStatePending, Limit: 5, Offset: offset,
			})
		require.NoError(t, err)
		assert.Equal(t, index < 2, page.HasMore)
		want := 0
		if offset < len(pending) {
			want = min(5, len(pending)-offset)
		}
		require.Len(t, page.Items, want)
		for _, row := range page.Items {
			assert.Equal(t, models.NoticeStatePending, row.State)
			got = append(got, row.ID)
		}
	}
	assert.Equal(t, pending, got)
}

// fakeController stands in for the control plane on the other end of a worker's
// socket. It records what the worker actually put on the wire, which is the
// only place a params-encoding mistake becomes visible: the worker compiles
// and the controller answers regardless.
type fakeController struct {
	conn *Conn

	calls chan Frame
	reply func(Frame) Frame
}

func newStoreHarness(t *testing.T, reply func(Frame) Frame) (*Store, *fakeController) {
	t.Helper()

	workerSide, controllerSide := net.Pipe()
	controller := &fakeController{
		conn:  NewConn(controllerSide, nil),
		calls: make(chan Frame, 16),
		reply: reply,
	}

	workerConn := NewConn(workerSide, nil)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		// Serve returns when the pipe closes, which Cleanup triggers.
		_ = workerConn.Serve(ctx)
	}()

	go controller.serve()

	t.Cleanup(func() {
		cancel()

		_ = workerSide.Close()
		_ = controllerSide.Close()
	})

	sessionID := uuid.New()

	return NewStore(workerConn, sessionID, uuid.New()), controller
}

func (f *fakeController) serve() {
	for {
		frame, err := f.conn.Receive()
		if err != nil {
			return
		}

		f.calls <- frame

		answer := f.reply(frame)
		answer.ID = frame.ID

		if err := f.conn.Send(answer); err != nil {
			return
		}
	}
}

func (f *fakeController) awaitCall(t *testing.T) Frame {
	t.Helper()

	select {
	case frame := <-f.calls:
		return frame
	case <-time.After(5 * time.Second):
		t.Fatal("the worker sent no frame")

		return Frame{}
	}
}

func resultWith(t *testing.T, value any) Frame {
	t.Helper()

	encoded, err := json.Marshal(value)
	require.NoError(t, err)

	return Frame{Kind: KindResult, Payload: encoded}
}

// Every durable read a worker performs has to arrive as a call frame naming the
// method, carrying the session and options the caller passed. The params are
// checked on the wire because Conn.Call marshals them itself: a caller that
// pre-marshals produces a double-encoded payload the controller cannot read.
func TestWorkerStoreSendsDurableReadsAsCallFrames(t *testing.T) {
	ctx := context.Background()
	sessionID := uuid.New()

	store, controller := newStoreHarness(t, func(Frame) Frame {
		return resultWith(t, session.MessagePage{Limit: 7, Offset: 3, HasMore: true})
	})

	page, err := store.ListMessages(ctx, sessionID, session.ListMessagesOptions{
		Limit:  7,
		Offset: 3,
	})
	require.NoError(t, err)
	require.NotNil(t, page)
	assert.Equal(t, 7, page.Limit)
	assert.True(t, page.HasMore)

	frame := controller.awaitCall(t)
	assert.Equal(t, KindCall, frame.Kind)
	assert.Equal(t, string(MethodListMessages), frame.Method)
	assert.NotEmpty(t, frame.ID)

	// The payload must decode in one step. A double-encoded payload would
	// decode into a JSON string instead of this object.
	//
	// The wire shape is spelled out here rather than reusing OptionsRequest,
	// so the test pins the field names both sides actually exchange instead of
	// agreeing with whatever the Go type happens to produce.
	// session.ListMessagesOptions carries no json tags, so the wire names are
	// Go's exported field names rather than the camelCase the rest of the
	// project uses. The tags below are deliberately capitalized to match what
	// actually crosses the socket.
	//nolint:tagliatelle // Mirrors the untagged option struct on the wire.
	decoded := struct {
		SessionID uuid.UUID `json:"sessionId"`
		Options   struct {
			Limit  int `json:"Limit"`
			Offset int `json:"Offset"`
		} `json:"options"`
	}{}
	require.NoError(t, json.Unmarshal(frame.Payload, &decoded))
	assert.Equal(t, sessionID, decoded.SessionID)
	assert.Equal(t, 7, decoded.Options.Limit)
	assert.Equal(t, 3, decoded.Options.Offset)
}

func TestWorkerStoreRoutesEachReadToItsOwnMethod(t *testing.T) {
	ctx := context.Background()
	sessionID := uuid.New()
	childID := uuid.New()

	store, controller := newStoreHarness(t, func(Frame) Frame {
		return Frame{Kind: KindResult}
	})

	testCases := []struct {
		name   string
		invoke func() error
		want   Method
	}{
		{
			name: "events",
			invoke: func() error {
				_, err := store.ListEvents(ctx, sessionID, session.ListEventsOptions{})

				return err
			},
			want: MethodListEvents,
		},
		{
			name: "turns",
			invoke: func() error {
				_, err := store.ListTurns(ctx, sessionID, session.ListTurnsOptions{})

				return err
			},
			want: MethodListTurns,
		},
		{
			name: "model runs",
			invoke: func() error {
				_, err := store.ListModelRuns(ctx, sessionID, session.ListModelRunsOptions{})

				return err
			},
			want: MethodListModelRuns,
		},
		{
			name: "jobs",
			invoke: func() error {
				_, err := store.ListJobs(ctx, sessionID, session.ListJobsOptions{})

				return err
			},
			want: MethodListJobs,
		},
		{
			name: "model calls",
			invoke: func() error {
				_, err := store.ListModelCalls(
					ctx,
					sessionID,
					childID,
					session.ListModelCallsOptions{},
				)

				return err
			},
			want: MethodListModelCalls,
		},
		{
			name: "model call retries",
			invoke: func() error {
				return store.RecordModelCallRetries(
					ctx,
					sessionID,
					childID,
					childID,
					session.RecordModelCallRetriesInput{},
				)
			},
			want: MethodRecordModelCallRetries,
		},
		{
			name: "create job",
			invoke: func() error {
				_, err := store.CreateJob(ctx, sessionID, session.CreateJobInput{})

				return err
			},
			want: MethodCreateJob,
		},
		{
			name: "finalize job",
			invoke: func() error {
				_, err := store.FinalizeJob(
					ctx,
					sessionID,
					childID,
					session.FinalizeJobInput{},
				)

				return err
			},
			want: MethodFinalizeJob,
		},
		{
			name: "get job",
			invoke: func() error {
				_, err := store.GetJob(ctx, sessionID, childID)

				return err
			},
			want: MethodGetJob,
		},
		{
			name: "append job output",
			invoke: func() error {
				_, err := store.AppendJobOutput(
					ctx,
					sessionID,
					childID,
					session.AppendJobOutputInput{},
				)

				return err
			},
			want: MethodAppendJobOutput,
		},
		{
			name: "job output",
			invoke: func() error {
				_, err := store.ListJobOutput(
					ctx,
					sessionID,
					childID,
					session.ListJobOutputOptions{},
				)

				return err
			},
			want: MethodListJobOutput,
		},
		{
			name: "record job signal",
			invoke: func() error {
				_, err := store.RecordJobSignal(
					ctx,
					sessionID,
					childID,
					session.RecordJobSignalInput{},
				)

				return err
			},
			want: MethodRecordJobSignal,
		},
		{
			name: "job signal requests",
			invoke: func() error {
				_, err := store.ListJobSignalRequests(
					ctx,
					sessionID,
					childID,
					session.ListJobSignalRequestsOptions{},
				)

				return err
			},
			want: MethodListJobSignalRequests,
		},
		{
			name: "create agent run",
			invoke: func() error {
				_, err := store.CreateAgentRun(
					ctx,
					sessionID,
					session.StartAgentRunInput{},
				)

				return err
			},
			want: MethodCreateAgentRun,
		},
		{
			name: "finalize agent run",
			invoke: func() error {
				_, err := store.FinalizeAgentRun(
					ctx,
					sessionID,
					childID,
					session.FinalizeAgentRunInput{},
				)

				return err
			},
			want: MethodFinalizeAgentRun,
		},
		{
			name: "get agent run",
			invoke: func() error {
				_, err := store.GetAgentRun(ctx, sessionID, childID)

				return err
			},
			want: MethodGetAgentRun,
		},
		{
			name: "agent runs",
			invoke: func() error {
				_, err := store.ListAgentRuns(
					ctx,
					sessionID,
					session.ListAgentRunsOptions{},
				)

				return err
			},
			want: MethodListAgentRuns,
		},
		{
			name: "append agent run event",
			invoke: func() error {
				_, err := store.AppendAgentRunEvent(
					ctx,
					sessionID,
					childID,
					session.AgentRunEventInput{},
				)

				return err
			},
			want: MethodAppendAgentRunEvent,
		},
		{
			name: "agent run events",
			invoke: func() error {
				_, err := store.ListAgentRunEvents(
					ctx,
					sessionID,
					childID,
					session.ListAgentRunEventsOptions{},
				)

				return err
			},
			want: MethodListAgentRunEvents,
		},
		{
			name: "create compaction",
			invoke: func() error {
				_, err := store.CreateCompaction(
					ctx,
					sessionID,
					session.CompactionInput{},
				)

				return err
			},
			want: MethodCreateCompaction,
		},
		{
			name: "compactions",
			invoke: func() error {
				_, err := store.ListCompactions(
					ctx,
					sessionID,
					session.ListCompactionsOptions{},
				)

				return err
			},
			want: MethodListCompactions,
		},
		{
			name: "context snapshot",
			invoke: func() error {
				_, err := store.GetContextSnapshot(ctx, sessionID, "a-hash")

				return err
			},
			want: MethodGetContextSnapshot,
		},
		{
			name: "create session notice",
			invoke: func() error {
				_, err := store.CreateSessionNotice(
					ctx,
					sessionID,
					session.CreateSessionNoticeInput{},
				)

				return err
			},
			want: MethodCreateSessionNotice,
		},
		{
			name: "session notices",
			invoke: func() error {
				_, err := store.ListSessionNotices(
					ctx,
					sessionID,
					session.ListSessionNoticesOptions{},
				)

				return err
			},
			want: MethodListSessionNotices,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, tc.invoke())

			frame := controller.awaitCall(t)
			assert.Equal(t, string(tc.want), frame.Method)
			assert.Equal(t, KindCall, frame.Kind)
		})
	}
}

// A controller-side failure has to reach the worker's caller as an error, not
// as a zero value, or a worker would treat a refused write as a successful one.
func TestWorkerStoreSurfacesAControllerError(t *testing.T) {
	store, _ := newStoreHarness(t, func(Frame) Frame {
		return Frame{
			Kind:  KindResult,
			Error: &Error{Message: "session is gone"},
		}
	})

	page, err := store.ListMessages(
		context.Background(),
		uuid.New(),
		session.ListMessagesOptions{},
	)
	require.Error(t, err)
	assert.Nil(t, page)
	assert.Contains(t, err.Error(), "session is gone")
}

// An answer the worker cannot decode is an error rather than a zero page, so a
// protocol drift between the two sides fails loudly.
func TestWorkerStoreRejectsAnUndecodableAnswer(t *testing.T) {
	store, _ := newStoreHarness(t, func(Frame) Frame {
		return Frame{Kind: KindResult, Payload: json.RawMessage(`"not a page"`)}
	})

	page, err := store.ListMessages(
		context.Background(),
		uuid.New(),
		session.ListMessagesOptions{},
	)
	require.Error(t, err)
	assert.Nil(t, page)
}

// Finalizing a turn ends it on the worker too, the same as the controller's
// own store. A worker that still counted its finished turn as running would
// treat the session as busy forever, so no event could ever wake it.
func TestWorkerStoreFinalizingATurnEndsItLocally(t *testing.T) {
	ctx := context.Background()
	sessionID := uuid.New()
	turnID := uuid.New()

	store, controller := newStoreHarness(t, func(frame Frame) Frame {
		if frame.Method == string(MethodAcquireTurn) {
			return resultWith(
				t,
				session.Lease{SessionID: sessionID, TurnID: turnID},
			)
		}

		return resultWith(t, struct{}{})
	})

	lease, err := store.AcquireTurn(ctx, sessionID, session.StartTurnInput{})
	require.NoError(t, err)
	controller.awaitCall(t)
	require.True(t, store.IsActive(sessionID))

	require.NoError(t, store.FinalizeTurn(ctx, lease, session.FinalizeTurnInput{}))
	frame := controller.awaitCall(t)
	assert.Equal(t, string(MethodFinalizeTurn), frame.Method)

	assert.False(t, store.IsActive(sessionID), "the finished turn still counts")
}

// The lease and its cancellation live in the worker's own memory, because
// cancellation has to reach a running goroutine rather than a row. Acquiring
// records the session as active; cancelling marks it and reports that it
// reached something.
func TestWorkerStoreTracksItsLeaseAndLocalCancellation(t *testing.T) {
	ctx := context.Background()
	sessionID := uuid.New()
	turnID := uuid.New()

	store, controller := newStoreHarness(t, func(frame Frame) Frame {
		if frame.Method == string(MethodCancelSession) {
			return resultWith(t, BoolResult{Value: true})
		}

		return resultWith(t, session.Lease{SessionID: sessionID, TurnID: turnID})
	})

	assert.False(
		t,
		store.IsActive(sessionID),
		"no turn has been acquired yet",
	)

	lease, err := store.AcquireTurn(ctx, sessionID, session.StartTurnInput{})
	require.NoError(t, err)
	assert.Equal(t, turnID, lease.TurnID)
	controller.awaitCall(t)

	assert.True(t, store.IsActive(sessionID))

	cancelled, err := store.Cancel(ctx, sessionID)
	require.NoError(t, err)
	assert.True(t, cancelled)

	frame := controller.awaitCall(t)
	assert.Equal(t, string(MethodCancelSession), frame.Method)

	// The durable request already happened above, so a second local request
	// still reaches the live turn and reports that it did.
	assert.True(t, store.RequestLocalCancellation(sessionID))

	// A session this worker never leased has no goroutine to reach.
	assert.False(t, store.RequestLocalCancellation(uuid.New()))
}

func TestStoreWakeAdmissionSnapshotOverRPC(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	remote := NewStore(fixture.remote.conn, fixture.sessionID,
		fixture.controller.generationID)
	notice, err := remote.CreateSessionNotice(t.Context(), fixture.sessionID,
		session.CreateSessionNoticeInput{
			Type: "app.ready", Source: "example",
			Summary: "ready", DataJSON: `{}`, Delivery: models.NoticeDeliveryWake,
		})
	require.NoError(t, err)
	input := session.StartTurnInput{
		RequestID: uuid.New(),
		Workspace: rpcExecutionWorkspace, WakeNoticeID: &notice.ID,
	}
	_, err = remote.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.False(t, remote.IsActive(fixture.sessionID))
	page, err := remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{State: models.NoticeStatePending})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	input.NoticeIDs = []uuid.UUID{notice.ID}
	lease, err := remote.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.NoError(t, err)
	remote.ReleaseTurn(lease)
	page, err = remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{State: models.NoticeStatePending})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestStoreRefusesPausedAgentWakeOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	source, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		child.RootExecutionID, session.RestoreExecutionTurnInput{
			OwnerGenerationID:  fixture.controller.generationID,
			WorkerGenerationID: fixture.controller.generationID,
		})
	require.NoError(t, err)
	remote.ReleaseTurn(source)
	_, err = remote.PauseExecution(t.Context(), fixture.sessionID, child.ID,
		rpcPauseReason)
	require.NoError(t, err)
	data, err := json.Marshal(events.AgentNoticeData{RunID: *child.AgentRunID})
	require.NoError(t, err)
	notice, err := remote.CreateSessionNotice(t.Context(), fixture.sessionID,
		session.CreateSessionNoticeInput{
			Type:   events.TypeAgentQuestion,
			Source: events.AgentSource, Summary: "child needs input",
			DataJSON: string(data), Delivery: models.NoticeDeliveryWake,
		})
	require.NoError(t, err)
	input := session.StartTurnInput{
		RequestID: uuid.New(),
		Workspace: rpcExecutionWorkspace, NoticeIDs: []uuid.UUID{notice.ID},
		WakeNoticeID: &notice.ID,
	}
	_, err = remote.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.False(t, remote.IsActive(fixture.sessionID))
	_, err = remote.ResumeExecution(t.Context(), fixture.sessionID, child.ID)
	require.NoError(t, err)
	admitted, err := remote.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.NoError(t, err)
	remote.ReleaseTurn(admitted)
}

func TestStoreJobOwnerAndWakeOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	input := session.CreateJobInput{
		ID: uuid.New(), TurnID: child.RootTurnID,
		AgentRunID: child.AgentRunID, PID: 1, Purpose: "inspect files",
		Command: "printf example", Directory: rpcExecutionWorkspace,
	}
	job, err := remote.CreateJob(t.Context(), fixture.sessionID, input)
	require.NoError(t, err)
	assert.Equal(t, child.AgentRunID, job.AgentRunID)
	stored, err := remote.GetJob(t.Context(), fixture.sessionID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, child.AgentRunID, stored.AgentRunID)
	source, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		child.RootExecutionID, session.RestoreExecutionTurnInput{
			OwnerGenerationID:  fixture.controller.generationID,
			WorkerGenerationID: fixture.controller.generationID,
		})
	require.NoError(t, err)
	remote.ReleaseTurn(source)
	_, err = remote.PauseExecution(t.Context(), fixture.sessionID, child.ID,
		rpcPauseReason)
	require.NoError(t, err)
	data, err := json.Marshal(events.JobNoticeData{JobID: job.ID})
	require.NoError(t, err)
	notice, err := remote.CreateSessionNotice(t.Context(), fixture.sessionID,
		session.CreateSessionNoticeInput{
			Type:   events.TypeJobExited,
			Source: events.JobSource, Summary: "job completed", DataJSON: string(data),
			Delivery: models.NoticeDeliveryWake,
		})
	require.NoError(t, err)
	opening := session.StartTurnInput{
		RequestID: uuid.New(),
		Workspace: rpcExecutionWorkspace, NoticeIDs: []uuid.UUID{notice.ID},
		WakeNoticeID: &notice.ID,
	}
	_, err = remote.AcquireTurn(t.Context(), fixture.sessionID, opening)
	require.ErrorIs(t, err, commerr.ErrInvalidState)
	assert.False(t, remote.IsActive(fixture.sessionID))
	_, err = remote.CreateJob(t.Context(), uuid.New(), input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = remote.ResumeExecution(t.Context(), fixture.sessionID, child.ID)
	require.NoError(t, err)
	admitted, err := remote.AcquireTurn(t.Context(), fixture.sessionID, opening)
	require.NoError(t, err)
	remote.ReleaseTurn(admitted)
}

func TestStoreStopRevisionRejectsLateOpeningOverRPC(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	remote := NewStore(fixture.remote.conn, fixture.sessionID,
		fixture.controller.generationID)
	old, err := remote.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	stopped, err := remote.Cancel(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.False(t, stopped)
	input := session.StartTurnInput{
		RequestID: uuid.New(),
		Workspace: rpcExecutionWorkspace, ExpectedStopRevision: &old.StopRevision,
	}
	_, err = remote.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	assert.False(t, remote.IsActive(fixture.sessionID))
	current, err := remote.Get(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	assert.Equal(t, old.StopRevision+1, current.StopRevision)
	input.ExpectedStopRevision = &current.StopRevision
	lease, err := remote.AcquireTurn(t.Context(), fixture.sessionID, input)
	require.NoError(t, err)
	remote.ReleaseTurn(lease)
}
