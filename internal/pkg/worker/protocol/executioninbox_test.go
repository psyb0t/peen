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

func TestChildInboxAcceptanceOverAuthenticatedWorkerRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	input := session.AcceptExecutionMessageInput{
		RequestID: uuid.New(), WorkingDirectory: rpcExecutionWorkspace,
		Message: "continue child work", Capacity: 4,
	}
	first, err := remote.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	repeated, err := remote.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	assert.Equal(t, first.Message.ID, repeated.Message.ID)
	_, err = remote.AcceptChildExecutionMessage(t.Context(), uuid.New(),
		child.ID, input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestFinishedChildInboxReturnsDomainErrorOverWorkerRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		child.ID, fixture.controller.generationID,
		session.ScheduleActivityInput{
			StepID:       session.ExecutionFinishStepID,
			Name:         session.ExecutionFinishActivityName,
			Kind:         models.ActivityKindPreparation,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, session.CompleteActivityInput{
			Output: json.RawMessage(`{}`), Context: json.RawMessage(`{}`),
			Finish: &session.FinishExecutionInput{
				State: models.ExecutionStateCompleted,
			},
		})
	require.NoError(t, err)
	_, err = remote.AcceptChildExecutionMessage(t.Context(), fixture.sessionID,
		child.ID, session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: rpcExecutionWorkspace,
			Message: "message after completion", Capacity: 1,
		})
	require.ErrorIs(t, err, session.ErrAgentRunFinished)
	assert.ErrorIs(t, err, commerr.ErrConflict)
}

func TestChildInboxDeliveryOverAuthenticatedWorkerRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	input := session.AcceptExecutionMessageInput{
		RequestID: uuid.New(), WorkingDirectory: rpcExecutionWorkspace,
		Message: "continue child work", Capacity: 1,
	}
	_, err := remote.AcceptChildExecutionMessage(t.Context(),
		fixture.sessionID, child.ID, input)
	require.NoError(t, err)
	ids := []uuid.UUID{input.RequestID}
	encoded, err := json.Marshal(map[string]any{
		"execution": map[string]any{
			"context": map[string]any{"inboxIds": ids},
		},
	})
	require.NoError(t, err)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		child.ID, fixture.controller.generationID,
		session.ScheduleActivityInput{
			StepID: "model1", Name: "modelRoundV1", Kind: models.ActivityKindModel,
			ReplayPolicy: models.ActivityReplayPolicyRetry, Input: encoded,
		})
	require.NoError(t, err)
	delivered, err := remote.DeliverChildExecutionMessages(t.Context(),
		fixture.sessionID, claim.Lease, ids)
	require.NoError(t, err)
	require.Len(t, delivered.Messages, 1)
	assert.Equal(t, input.Message, delivered.Messages[0].Content)
	repeated, err := remote.DeliverChildExecutionMessages(t.Context(),
		fixture.sessionID, claim.Lease, ids)
	require.NoError(t, err)
	assert.Empty(t, repeated.Messages)
	_, err = remote.DeliverChildExecutionMessages(t.Context(), uuid.New(),
		claim.Lease, ids)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	claim.Lease.GenerationID = uuid.New()
	_, err = remote.DeliverChildExecutionMessages(t.Context(), fixture.sessionID,
		claim.Lease, ids)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestExecutionInboxPersistsAndDeliversThroughWorkerRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, session.RestoreExecutionTurnInput{
			OwnerGenerationID:  fixture.controller.generationID,
			WorkerGenerationID: fixture.controller.generationID,
		})
	require.NoError(t, err)
	input := session.AcceptExecutionMessageInput{
		RequestID: uuid.New(), SourceEventID: uuid.New(),
		WorkingDirectory: "/workspace", Message: "continue example work",
		Parts: []session.ExecutionInboxPart{{
			Content: "original skill content", SkillName: "example",
		}},
		Capacity: 2,
	}
	accepted, err := remote.AcceptExecutionMessage(t.Context(), execution.ID,
		lease, input)
	require.NoError(t, err)
	require.Len(t, accepted.Events, 2)
	assert.Equal(t, fixture.controller.generationID.String(),
		accepted.Events[0].WorkerGenerationID)
	repeated, err := remote.AcceptExecutionMessage(t.Context(), execution.ID,
		lease, input)
	require.NoError(t, err)
	assert.Equal(t, accepted.Message.ID, repeated.Message.ID)
	assert.Empty(t, repeated.Events)
	encoded, err := json.Marshal(map[string]any{
		"execution": map[string]any{"context": map[string]any{
			"inboxIds": []uuid.UUID{input.RequestID},
		}},
	})
	require.NoError(t, err)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		execution.ID, fixture.controller.generationID,
		session.ScheduleActivityInput{
			StepID: "model1", Name: "modelRoundV1", Kind: models.ActivityKindModel,
			ReplayPolicy: models.ActivityReplayPolicyRetry, Input: encoded,
		})
	require.NoError(t, err)
	delivery := session.DeliverExecutionMessagesInput{
		TurnLease: lease, ModelLease: claim.Lease,
		RequestIDs: []uuid.UUID{input.RequestID},
	}
	events, err := remote.DeliverExecutionMessages(t.Context(),
		fixture.sessionID, delivery)
	require.NoError(t, err)
	assert.Len(t, events.Events, 2)
	assert.Len(t, events.Messages, 2)
	assert.Equal(t, fixture.controller.generationID.String(),
		events.Events[0].WorkerGenerationID)
	events, err = remote.DeliverExecutionMessages(t.Context(),
		fixture.sessionID, delivery)
	require.NoError(t, err)
	assert.Empty(t, events.Events)
	assert.Empty(t, events.Messages)
	page, err := remote.ListExecutionMessages(t.Context(), fixture.sessionID,
		execution.ID, session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateDelivered, page.Items[0].State)
	assert.Contains(t, page.Items[0].PartsJSON, input.Parts[0].Content)
	assert.Equal(t, claim.Activity.ID, *page.Items[0].DeliveredActivityID)
	retained, err := remote.GetExecutionMessageByRequest(t.Context(),
		fixture.sessionID, execution.ID, input.RequestID)
	require.NoError(t, err)
	assert.Equal(t, page.Items[0], retained)
	_, err = remote.PauseExecution(t.Context(), fixture.sessionID,
		execution.ID, "operator pause")
	require.NoError(t, err)
	retained, err = remote.GetExecutionMessageByRequest(t.Context(),
		fixture.sessionID, execution.ID, input.RequestID)
	require.NoError(t, err)
	assert.Equal(t, page.Items[0], retained)
	_, err = remote.GetExecutionMessageByRequest(t.Context(),
		fixture.sessionID, execution.ID, uuid.New())
	require.ErrorIs(t, err, commerr.ErrNotFound)
}

func TestExecutionInboxRejectsForgedWorkerRPCIdentity(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	_, err := remote.GetExecutionMessageByRequest(t.Context(), uuid.New(),
		execution.ID, uuid.New())
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = remote.ListExecutionMessages(t.Context(), uuid.New(),
		execution.ID, session.ListExecutionInboxOptions{})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = remote.AcceptExecutionMessage(t.Context(), execution.ID,
		session.Lease{SessionID: uuid.New()},
		session.AcceptExecutionMessageInput{})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = remote.DeliverExecutionMessages(t.Context(), fixture.sessionID,
		session.DeliverExecutionMessagesInput{
			TurnLease:  session.Lease{SessionID: fixture.sessionID},
			ModelLease: session.ActivityLease{GenerationID: uuid.New()},
		})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestExecutionInboxPendingFinishSurvivesWorkerRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, session.RestoreExecutionTurnInput{
			OwnerGenerationID:  fixture.controller.generationID,
			WorkerGenerationID: fixture.controller.generationID,
		})
	require.NoError(t, err)
	_, err = remote.AcceptExecutionMessage(t.Context(), execution.ID, lease,
		session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: "/workspace",
			Message: "continue retained work", Capacity: 1,
		})
	require.NoError(t, err)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		execution.ID, fixture.controller.generationID,
		session.ScheduleActivityInput{
			StepID:       session.ExecutionFinishStepID,
			Name:         session.ExecutionFinishActivityName,
			Kind:         models.ActivityKindPreparation,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, session.CompleteActivityInput{
			Output: json.RawMessage(`{}`), Context: json.RawMessage(`{}`),
			Finish: &session.FinishExecutionInput{
				State: models.ExecutionStateCompleted,
			},
		})
	require.ErrorIs(t, err, session.ErrExecutionInboxPending)
	require.ErrorIs(t, err, commerr.ErrConflict)
	activity, err := remote.GetExecutionActivity(t.Context(), fixture.sessionID,
		claim.Activity.ID)
	require.NoError(t, err)
	assert.Nil(t, activity.OutputJSON)
	assert.Equal(t, models.ActivityStateRunning, activity.State)
}
