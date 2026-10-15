package protocol

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeferredRPCStoresFrozenInputAndPublishesOnce(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, claim := rpcCompletionClaim(t, fixture, remote, execution)
	_, err := remote.ReserveExecutionCompletion(t.Context(), fixture.sessionID,
		claim.Lease)
	require.NoError(t, err)
	publisher := &recordingPublisher{}
	fixture.controller.publisher = publisher
	input := session.AcceptExecutionMessageInput{
		RequestID: uuid.New(), WorkingDirectory: rpcExecutionWorkspace,
		Message: "next root task", Capacity: 2,
		Parts: []session.ExecutionInboxPart{{
			SkillName: "example",
			Content:   "frozen skill instructions",
		}},
	}
	first, err := remote.DeferExecutionMessage(t.Context(), execution.ID,
		lease, input)
	require.NoError(t, err)
	assert.Equal(t, models.InboxMessageStateDeferred, first.Message.State)
	repeated, err := remote.DeferExecutionMessage(t.Context(), execution.ID,
		lease, input)
	require.NoError(t, err)
	assert.Equal(t, first.Message, repeated.Message)
	assert.Empty(t, repeated.Events)
	assert.Len(t, publisher.events, 2)
	page, err := remote.ListExecutionMessages(t.Context(), fixture.sessionID,
		execution.ID, session.ListExecutionInboxOptions{
			State: models.InboxMessageStateDeferred,
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, first.Message, page.Items[0])
	input.Message = "changed replay"
	_, err = remote.DeferExecutionMessage(t.Context(), execution.ID, lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	input.RequestID = uuid.New()
	_, err = remote.DeferExecutionMessage(t.Context(), execution.ID, lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestDeferredRPCRejectsForeignOrStaleLease(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, _ := rpcCompletionClaim(t, fixture, remote, execution)
	testCases := []struct {
		name    string
		change  func(*session.Lease)
		wantErr error
	}{
		{name: "foreign session", change: func(lease *session.Lease) {
			lease.SessionID = uuid.New()
		}, wantErr: commerr.ErrPermissionDenied},
		{name: "stale lease", change: func(lease *session.Lease) {
			lease.ID = uuid.New()
		}, wantErr: commerr.ErrInvalidState},
		{name: "foreign turn", change: func(lease *session.Lease) {
			lease.TurnID = uuid.New()
		}, wantErr: commerr.ErrInvalidState},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			changed := lease
			tc.change(&changed)
			_, err := remote.DeferExecutionMessage(t.Context(), execution.ID,
				changed, session.AcceptExecutionMessageInput{
					RequestID: uuid.New(), WorkingDirectory: rpcExecutionWorkspace,
					Message: "next task", Capacity: 1,
				})
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestDeferredRPCRequiresReservedCompletion(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, _ := rpcCompletionClaim(t, fixture, remote, execution)
	_, err := remote.DeferExecutionMessage(t.Context(), execution.ID, lease,
		session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: rpcExecutionWorkspace,
			Message: "next task", Capacity: 1,
		})
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestDeferredRPCRejectsReplacedWorker(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, _ := rpcCompletionClaim(t, fixture, remote, execution)
	_, err := fixture.controller.store.CreateWorkerGeneration(t.Context(),
		fixture.sessionID, session.CreateWorkerGenerationInput{
			ID: uuid.New(), Kind: "control", AgentSpace: "control",
			State: "ready", Workspace: rpcExecutionWorkspace,
			SocketPath:     "/example/replacement.sock",
			CredentialHash: "EXAMPLE-HASH",
		})
	require.NoError(t, err)
	_, err = remote.DeferExecutionMessage(t.Context(), execution.ID, lease,
		session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: rpcExecutionWorkspace,
			Message: "next task", Capacity: 1,
		})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func finishDeferredRPCSource(
	t *testing.T,
	fixture workflowRPCFixture,
	remote *Store,
	executionID uuid.UUID,
	lease session.Lease,
	hook *session.ActivityClaim,
) {
	t.Helper()
	result := session.CompleteActivityInput{
		Output:  json.RawMessage(rpcActivityOutput),
		Context: json.RawMessage(rpcExecutionContext),
	}
	_, err := remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		hook.Lease, result)
	require.NoError(t, err)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		executionID, fixture.controller.generationID,
		session.ScheduleActivityInput{
			StepID:       session.ExecutionFinishStepID,
			Name:         session.ExecutionFinishActivityName,
			Kind:         models.ActivityKindPreparation,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	result.Finish = &session.FinishExecutionInput{
		State:      models.ExecutionStateCompleted,
		RootHolder: &session.ExecutionFinishHolder{LeaseID: lease.ID},
		Turn:       &session.FinalizeTurnInput{State: models.TurnStateCompleted},
	}
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, result)
	require.NoError(t, err)
	remote.ReleaseTurn(lease)
}

func TestDeferredRPCLinksNextRootAtomically(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, hook := rpcCompletionClaim(t, fixture, remote, execution)
	_, err := remote.ReserveExecutionCompletion(t.Context(), fixture.sessionID,
		hook.Lease)
	require.NoError(t, err)
	input := session.AcceptExecutionMessageInput{
		RequestID: uuid.New(), WorkingDirectory: rpcExecutionWorkspace,
		Message: "next root task", Capacity: 1,
	}
	accepted, err := remote.DeferExecutionMessage(t.Context(), execution.ID,
		lease, input)
	require.NoError(t, err)
	finishDeferredRPCSource(t, fixture, remote, execution.ID, lease, hook)
	ready, err := remote.ListDeferredExecutionMessages(t.Context(),
		fixture.sessionID, session.ListEventsOptions{Limit: 1})
	require.NoError(t, err)
	require.Len(t, ready.Items, 1)
	assert.Equal(t, accepted.Message.ID, ready.Items[0].ID)
	assert.False(t, ready.HasMore)
	_, err = remote.ListDeferredExecutionMessages(t.Context(), uuid.New(),
		session.ListEventsOptions{})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	admission, err := remote.AcquireDeferredExecutionTurn(t.Context(),
		fixture.sessionID, accepted.Message.ID,
		session.StartTurnInput{
			RequestID: input.RequestID,
			Workspace: input.WorkingDirectory,
		})
	require.NoError(t, err)
	next := admission.Lease
	t.Cleanup(func() { remote.ReleaseTurn(next) })
	create := session.CreateExecutionInput{
		ID: uuid.New(), RootTurnID: next.TurnID,
		DeferredInboxID: &accepted.Message.ID,
		WorkflowVersion: "exampleV1", Task: input.Message,
		WorkingDirectory: input.WorkingDirectory,
		Context:          json.RawMessage(rpcExecutionContext),
	}
	target, err := remote.CreateExecution(t.Context(), fixture.sessionID, create)
	require.NoError(t, err)
	repeated, err := remote.CreateExecution(t.Context(), fixture.sessionID,
		create)
	require.NoError(t, err)
	assert.Equal(t, target, repeated)
	page, err := remote.ListExecutionMessages(t.Context(), fixture.sessionID,
		execution.ID, session.ListExecutionInboxOptions{
			State: models.InboxMessageStateForwarded,
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotNil(t, page.Items[0].ForwardedExecutionID)
	assert.Equal(t, target.ID, *page.Items[0].ForwardedExecutionID)
	ready, err = remote.ListDeferredExecutionMessages(t.Context(),
		fixture.sessionID, session.ListEventsOptions{})
	require.NoError(t, err)
	assert.Empty(t, ready.Items)
}
