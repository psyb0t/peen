package protocol

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rpcExecutionContext   = `{"messages":[]}`
	rpcActivityOutput     = `{"text":"saved result"}`
	rpcPauseReason        = "leave this work paused"
	rpcExecutionWorkspace = "/workspace"
)

func executionChildRPCFixture(t *testing.T) (
	workflowRPCFixture, *Store, *models.Execution,
) {
	t.Helper()
	fixture, remote, root := newExecutionRPCFixture(t)
	run, err := remote.CreateAgentRun(t.Context(), fixture.sessionID,
		session.StartAgentRunInput{
			ID: uuid.New(), ParentTurnID: root.RootTurnID,
			RequestID: uuid.New(), Name: "exampleChild",
			Definition: models.AgentRunDefinitionAdHoc, Depth: 1,
			Workspace: rpcExecutionWorkspace, ModelReference: "example/model",
			ModelID: "example/model", Task: "summarize example history",
			Instructions: "inspect example files", AllowedToolsJSON: `[]`,
			SystemPrompt: "inspect example files",
		})
	require.NoError(t, err)
	child, err := remote.CreateExecution(t.Context(), fixture.sessionID,
		session.CreateExecutionInput{
			ID: uuid.New(), RootTurnID: root.RootTurnID,
			ParentExecutionID: &root.ID, AgentRunID: &run.ID,
			WorkflowVersion: "executionV1", Task: "summarize example history",
			WorkingDirectory: rpcExecutionWorkspace,
			Context:          json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)

	return fixture, remote, child
}

func TestExecutionStoreCommitsChildProjectionOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		child.ID, fixture.controller.generationID, session.ScheduleActivityInput{
			StepID: "tool:0:0:project", Name: "toolPhaseV1",
			Kind:         models.ActivityKindTool,
			ReplayPolicy: models.ActivityReplayPolicyReconcile,
			Input:        json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	input := session.CompleteActivityInput{
		Output:  json.RawMessage(rpcActivityOutput),
		Context: json.RawMessage(rpcExecutionContext),
		ChildProjection: &session.ExecutionChildProjection{
			Messages: []session.AgentRunMessageInput{{
				Role:    models.MessageRoleTool,
				Content: "saved child result", ToolCallID: "readCall",
			}},
			Events: []session.AgentRunEventInput{{
				ID:        uuid.New(),
				EventType: "agent.run.toolResult", PayloadJSON: `{}`,
			}},
		},
	}
	for range 2 {
		_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
			claim.Lease, input)
		require.NoError(t, err)
	}
	page, err := remote.ListAgentRunMessages(t.Context(), fixture.sessionID,
		*child.AgentRunID, session.ListAgentRunMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "saved child result", page.Items[0].Content)
	events, err := remote.ListAgentRunEvents(t.Context(), fixture.sessionID,
		*child.AgentRunID, session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	require.Len(t, events.Items, 1)
	assert.Equal(t, input.ChildProjection.Events[0].ID, events.Items[0].ID)
	_, err = remote.CompleteExecutionActivity(t.Context(), uuid.New(),
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	input.ChildProjection = nil
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestExecutionStoreCommitsChildQuestionOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		child.ID, fixture.controller.generationID, session.ScheduleActivityInput{
			StepID: "tool:0:0:question", Name: "toolPhaseV1",
			Kind:         models.ActivityKindTool,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	questionID := uuid.New()
	input := session.CompleteActivityInput{
		Output:  json.RawMessage(rpcActivityOutput),
		Context: json.RawMessage(rpcExecutionContext),
		ChildProjection: &session.ExecutionChildProjection{
			Events: []session.AgentRunEventInput{{
				ID: questionID, EventType: events.TypeAgentRunQuestion,
				PayloadJSON: `{"question":"which path?"}`,
			}},
			Question: &session.ExecutionChildQuestion{
				EventID: questionID, Question: "which path?",
				ParentMessage: "rendered child question",
			},
		},
	}
	for range 2 {
		_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
			claim.Lease, input)
		require.NoError(t, err)
	}
	page, err := remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, questionID, page.Items[0].ID)
	assert.Equal(t, events.TypeAgentQuestion, page.Items[0].Type)
	page, err = remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{ID: &questionID})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, questionID, page.Items[0].ID)
	missing := uuid.New()
	page, err = remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{ID: &missing})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	_, err = remote.ListSessionNotices(t.Context(), uuid.New(),
		session.ListSessionNoticesOptions{ID: &questionID})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = remote.CompleteExecutionActivity(t.Context(), uuid.New(),
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	input.ChildProjection.Question = nil
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	page, err = remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{
			ID: &questionID, State: models.NoticeStatePending,
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	page, err = remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{State: models.NoticeStateDelivered})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	_, err = remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{State: "unknown"})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	_, err = remote.DrainSessionNotices(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	page, err = remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{
			ID: &questionID, State: models.NoticeStatePending,
		})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	page, err = remote.ListSessionNotices(t.Context(), fixture.sessionID,
		session.ListSessionNoticesOptions{
			ID: &questionID, State: models.NoticeStateDelivered,
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, questionID, page.Items[0].ID)
}

func TestExecutionStoreCommitsChildCompactionOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	messages, err := remote.AppendAgentRunMessages(t.Context(),
		fixture.sessionID, *child.AgentRunID, []session.AgentRunMessageInput{
			{Role: models.MessageRoleUser, Content: "completed child history"},
		})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		child.ID, fixture.controller.generationID, session.ScheduleActivityInput{
			StepID: "compact:0", Name: "compactHistoryV1",
			Kind:         models.ActivityKindCompaction,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	input := session.CompleteActivityInput{
		Output:  json.RawMessage(rpcActivityOutput),
		Context: json.RawMessage(rpcExecutionContext),
		Compaction: &session.CompactionInput{
			FromMessageID: messages[0].ID, ToMessageID: messages[0].ID,
			FromSequence: 1, ToSequence: 1, SourceMessageCount: 1,
			Summary: "saved child summary", ModelID: "example/model",
			PromptHash: "example-prompt",
		},
	}
	for range 2 {
		_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
			claim.Lease, input)
		require.NoError(t, err)
	}

	history, err := remote.AgentRunHistory(t.Context(), fixture.sessionID,
		*child.AgentRunID)
	require.NoError(t, err)
	require.NotNil(t, history.Compaction)
	assert.Equal(t, claim.Activity.ID, history.Compaction.ID)
	assert.Empty(t, history.Messages)
	input.Compaction = nil
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func TestExecutionStoreCommitsCompactionOverRPC(t *testing.T) {
	t.Parallel()
	fixture := newWorkflowRPCFixture(t)
	remote := NewStore(fixture.remote.conn, fixture.sessionID,
		fixture.controller.generationID)
	lease, err := remote.AcquireTurn(t.Context(), fixture.sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: rpcExecutionWorkspace,
		})
	require.NoError(t, err)
	messageID := uuid.New()
	require.NoError(t, remote.FinalizeTurn(t.Context(), lease,
		session.FinalizeTurnInput{
			State: models.TurnStateCompleted,
			Messages: []session.MessageInput{{
				ID:   messageID,
				Role: models.MessageRoleUser, Content: "completed source",
			}},
		}))
	lease, err = remote.AcquireTurn(t.Context(), fixture.sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(),
			Workspace: rpcExecutionWorkspace,
		})
	require.NoError(t, err)
	execution, err := remote.CreateExecution(t.Context(), fixture.sessionID,
		session.CreateExecutionInput{
			ID: uuid.New(), RootTurnID: lease.TurnID,
			WorkflowVersion: "executionV1", Task: "compact example history",
			WorkingDirectory: rpcExecutionWorkspace,
			Context:          json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		execution.ID, fixture.controller.generationID,
		session.ScheduleActivityInput{
			StepID: "compact:0", Name: "compactHistoryV1",
			Kind:         models.ActivityKindCompaction,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	input := session.CompleteActivityInput{
		Output:  json.RawMessage(rpcActivityOutput),
		Context: json.RawMessage(rpcExecutionContext),
		Compaction: &session.CompactionInput{
			FromMessageID: messageID, ToMessageID: messageID,
			FromSequence: 1, ToSequence: 1, SourceMessageCount: 1,
			Summary: "saved summary", ModelID: "example/model",
			PromptHash: "example-prompt",
		},
	}
	for range 2 {
		_, err = remote.CompleteExecutionActivity(t.Context(),
			fixture.sessionID, claim.Lease, input)
		require.NoError(t, err)
	}
	history, err := remote.CompletedHistory(t.Context(), fixture.sessionID)
	require.NoError(t, err)
	require.NotNil(t, history.Compaction)
	assert.Equal(t, claim.Activity.ID, history.Compaction.ID)
	assert.Equal(t, input.Compaction.Summary, history.Compaction.Summary)
	assert.Empty(t, history.Messages)
	input.Compaction.Summary = "changed summary"
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

func newExecutionRPCFixture(t *testing.T) (
	workflowRPCFixture, *Store, *models.Execution,
) {
	t.Helper()
	fixture := newWorkflowRPCFixture(t)
	remote := NewStore(fixture.remote.conn, fixture.sessionID,
		fixture.controller.generationID)
	lease, err := remote.AcquireTurn(t.Context(), fixture.sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(), Workspace: rpcExecutionWorkspace,
		})
	require.NoError(t, err)
	execution, err := remote.CreateExecution(t.Context(), fixture.sessionID,
		session.CreateExecutionInput{
			ID: uuid.New(), RootTurnID: lease.TurnID,
			WorkflowVersion: "executionV1", Task: "example task",
			WorkingDirectory: rpcExecutionWorkspace,
			Context:          json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)

	return fixture, remote, execution
}

func TestExecutionStoreCheckpointsOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	ctx := t.Context()
	input := session.ScheduleActivityInput{
		StepID: "model1", Kind: models.ActivityKindModel,
		ReplayPolicy: models.ActivityReplayPolicyRetry, Name: "modelRoundV1",
		Input: json.RawMessage(rpcExecutionContext),
	}
	claim, err := remote.BeginExecutionActivity(ctx, fixture.sessionID,
		execution.ID, fixture.controller.generationID, input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionExecute, claim.Decision)
	_, err = remote.CompleteExecutionActivity(ctx, fixture.sessionID,
		claim.Lease, session.CompleteActivityInput{
			Output:  json.RawMessage(rpcActivityOutput),
			Context: json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	reused, err := remote.BeginExecutionActivity(ctx, fixture.sessionID,
		execution.ID, fixture.controller.generationID, input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	require.NotNil(t, reused.Activity.OutputJSON)
	assert.JSONEq(t, rpcActivityOutput, *reused.Activity.OutputJSON)
	page, err := remote.ListExecutionActivities(ctx, fixture.sessionID,
		execution.ID, session.ListEventsOptions{Limit: 5})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, claim.Activity.ID, page.Items[0].ID)
}

func TestExecutionStoreReadsDoNotResumePausedWork(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	ctx := t.Context()
	_, err := remote.PauseExecution(ctx, fixture.sessionID,
		execution.ID, rpcPauseReason)
	require.NoError(t, err)
	page, err := remote.SearchExecutions(ctx, fixture.sessionID,
		session.SearchExecutionsOptions{Text: rpcPauseReason, Limit: 5})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.ExecutionStatePaused, page.Items[0].State)
	events, err := remote.ListExecutionEvents(ctx, fixture.sessionID,
		execution.ID, session.ListEventsOptions{Limit: 5})
	require.NoError(t, err)
	require.Len(t, events.Items, 2)
	current, err := remote.GetExecution(ctx, fixture.sessionID, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, current.State)
	_, err = remote.ResumeExecution(ctx, fixture.sessionID, execution.ID)
	require.NoError(t, err)
	current, err = remote.GetExecution(ctx, fixture.sessionID, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateQueued, current.State)
}

func TestExecutionStoreRejectsForgedIdentities(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	_, err := remote.GetExecution(t.Context(), uuid.New(), execution.ID)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		execution.ID, uuid.New(), session.ScheduleActivityInput{})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		session.ActivityLease{GenerationID: uuid.New()},
		session.CompleteActivityInput{})
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestExecutionStoreResolvesInterruptedToolOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	ctx := t.Context()
	claim, err := remote.BeginExecutionActivity(ctx, fixture.sessionID,
		execution.ID, fixture.controller.generationID,
		session.ScheduleActivityInput{
			StepID: "tool1", Kind: models.ActivityKindTool,
			ReplayPolicy: models.ActivityReplayPolicyReconcile,
			Name:         "writeFile", Input: json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	require.NoError(t, remote.InterruptExecutionActivity(ctx,
		fixture.sessionID, claim.Lease))
	activity, err := remote.GetExecutionActivity(ctx, fixture.sessionID,
		claim.Activity.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateUncertain, activity.State)
	activity, err = remote.ResolveExecutionActivity(ctx, fixture.sessionID,
		claim.Activity.ID, session.ResolveActivityInput{
			Attempt:    claim.Lease.Attempt,
			Resolution: session.ActivityResolutionAccept,
			Evidence:   "inspected example file",
			Output:     json.RawMessage(rpcActivityOutput),
			Context:    json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateCompleted, activity.State)
}

func TestExecutionStoreRejectsReplacedReadyWorker(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	_, err := fixture.controller.store.CreateWorkerGeneration(
		t.Context(), fixture.sessionID, session.CreateWorkerGenerationInput{
			ID: uuid.New(), Kind: "control", AgentSpace: "control",
			State: "ready", Workspace: rpcExecutionWorkspace,
			SocketPath:     "/example/replacement.sock",
			CredentialHash: "EXAMPLE-HASH",
		},
	)
	require.NoError(t, err)
	_, err = remote.GetExecution(t.Context(), fixture.sessionID, execution.ID)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}

func TestExecutionStoreProjectsTranscriptWithActivityOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, session.RestoreExecutionTurnInput{
			OwnerGenerationID:  fixture.controller.generationID,
			WorkerGenerationID: fixture.controller.generationID,
		})
	require.NoError(t, err)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		execution.ID, fixture.controller.generationID,
		session.ScheduleActivityInput{
			StepID: "tool:0:0:project", Name: "toolPhaseV1",
			Kind:         models.ActivityKindTool,
			ReplayPolicy: models.ActivityReplayPolicyReconcile,
			Input:        json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	input := session.CompleteActivityInput{
		Output:  json.RawMessage(rpcActivityOutput),
		Context: json.RawMessage(rpcExecutionContext),
		RootProjection: &session.ExecutionRootProjection{
			Holder: session.ExecutionFinishHolder{LeaseID: lease.ID},
			Messages: []session.MessageInput{{
				Role:    models.MessageRoleTool,
				Content: "saved tool result", ToolCallID: "readCall",
			}},
			Events: []session.EventInput{{
				ID:        uuid.New(),
				EventType: "tool.result", PayloadJSON: `{}`,
			}},
		},
	}
	for range 2 {
		completed, err := remote.CompleteExecutionActivity(t.Context(),
			fixture.sessionID, claim.Lease, input)
		require.NoError(t, err)
		assert.Equal(t, models.ActivityStateCompleted, completed.State)
	}
	storedEvents, err := fixture.controller.store.ListEvents(t.Context(),
		fixture.sessionID, session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, storedEvents.Items, 1)
	assert.Equal(t, input.RootProjection.Events[0].EventType,
		storedEvents.Items[0].EventType)
	page, err := fixture.controller.store.ListMessages(t.Context(),
		fixture.sessionID, session.ListMessagesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "saved tool result", page.Items[0].Content)
	input.RootProjection = nil
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	storedEvents, err = fixture.controller.store.ListEvents(t.Context(),
		fixture.sessionID, session.ListEventsOptions{})
	require.NoError(t, err)
	assert.Len(t, storedEvents.Items, 1)
}

func TestExecutionStoreFinishesAtomicallyOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, session.RestoreExecutionTurnInput{
			OwnerGenerationID:  fixture.controller.generationID,
			WorkerGenerationID: fixture.controller.generationID,
		})
	require.NoError(t, err)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		execution.ID, fixture.controller.generationID,
		session.ScheduleActivityInput{
			StepID:       session.ExecutionFinishStepID,
			Name:         session.ExecutionFinishActivityName,
			Kind:         models.ActivityKindPreparation,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	input := session.CompleteActivityInput{
		Output:  json.RawMessage(rpcActivityOutput),
		Context: json.RawMessage(rpcExecutionContext),
		Finish: &session.FinishExecutionInput{
			State:      models.ExecutionStateCompleted,
			RootHolder: &session.ExecutionFinishHolder{LeaseID: lease.ID},
			Turn: &session.FinalizeTurnInput{
				State: models.TurnStateCompleted,
				Events: []session.EventInput{{
					ID:        uuid.New(),
					EventType: "turn.completed", PayloadJSON: `{}`,
				}},
			},
		},
	}
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.NoError(t, err)
	storedEvents, err := fixture.controller.store.ListEvents(t.Context(),
		fixture.sessionID, session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, storedEvents.Items, 1)
	assert.Equal(t, input.Finish.Turn.Events[0].EventType,
		storedEvents.Items[0].EventType)
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, input)
	require.NoError(t, err)
	storedEvents, err = fixture.controller.store.ListEvents(t.Context(),
		fixture.sessionID, session.ListEventsOptions{})
	require.NoError(t, err)
	assert.Len(t, storedEvents.Items, 1)
	current, err := remote.GetExecution(t.Context(), fixture.sessionID,
		execution.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateCompleted, current.State)
	assert.EqualValues(t, 6, current.EventCount)
	assertFinishedWorkerLeaseRetirement(t, remote, fixture.sessionID, lease)
}

func assertFinishedWorkerLeaseRetirement(
	t *testing.T,
	remote *Store,
	sessionID uuid.UUID,
	lease session.Lease,
) {
	t.Helper()
	assert.True(t, remote.IsActive(sessionID))
	remote.ReleaseTurn(lease)
	assert.False(t, remote.IsActive(sessionID))
	fresh, err := remote.AcquireTurn(t.Context(), sessionID,
		session.StartTurnInput{
			RequestID: uuid.New(), Workspace: rpcExecutionWorkspace,
		})
	require.NoError(t, err)
	t.Cleanup(func() { remote.ReleaseTurn(fresh) })
	remote.ReleaseTurn(lease)
	assert.True(t, remote.IsActive(sessionID))
	require.NoError(t, remote.AppendCheckpoint(t.Context(), fresh, nil, nil))
}

func TestExecutionFinishHolderRejectsOldRevisionThroughRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, execution := newExecutionRPCFixture(t)
	lease, err := remote.RestoreExecutionTurn(t.Context(), fixture.sessionID,
		execution.ID, session.RestoreExecutionTurnInput{
			OwnerGenerationID:  fixture.controller.generationID,
			WorkerGenerationID: fixture.controller.generationID,
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
	_, err = remote.PauseExecution(t.Context(), fixture.sessionID,
		execution.ID, "retain this execution")
	require.NoError(t, err)
	_, err = remote.ResumeExecution(t.Context(), fixture.sessionID, execution.ID)
	require.NoError(t, err)
	_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
		claim.Lease, session.CompleteActivityInput{
			Output: json.RawMessage(`{}`), Context: json.RawMessage(`{}`),
			Finish: &session.FinishExecutionInput{
				State:      models.ExecutionStateCompleted,
				RootHolder: &session.ExecutionFinishHolder{LeaseID: lease.ID},
			},
		})
	require.ErrorIs(t, err, commerr.ErrConflict)
	activity, err := remote.GetExecutionActivity(t.Context(), fixture.sessionID,
		claim.Activity.ID)
	require.NoError(t, err)
	assert.Nil(t, activity.OutputJSON)
}

func TestExecutionStoreCommitsChildParentReportOverRPC(t *testing.T) {
	t.Parallel()
	fixture, remote, child := executionChildRPCFixture(t)
	claim, err := remote.BeginExecutionActivity(t.Context(), fixture.sessionID,
		child.ID, fixture.controller.generationID, session.ScheduleActivityInput{
			StepID:       session.ExecutionFinishStepID,
			Name:         session.ExecutionFinishActivityName,
			Kind:         models.ActivityKindPreparation,
			ReplayPolicy: models.ActivityReplayPolicyRetry,
			Input:        json.RawMessage(rpcExecutionContext),
		})
	require.NoError(t, err)
	input := session.CompleteActivityInput{
		Output:  json.RawMessage(rpcActivityOutput),
		Context: json.RawMessage(rpcExecutionContext),
		Finish: &session.FinishExecutionInput{
			State: models.ExecutionStateCompleted,
			Child: &session.ExecutionFinishChild{
				State: models.AgentRunStateCompleted, ResponseText: "child RPC outcome",
			},
			ChildReport: "framed child RPC report",
		},
	}
	for range 2 {
		_, err = remote.CompleteExecutionActivity(t.Context(), fixture.sessionID,
			claim.Lease, input)
		require.NoError(t, err)
	}
	page, err := remote.ListExecutionMessages(t.Context(), fixture.sessionID,
		*child.ParentExecutionID, session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, input.Finish.ChildReport, page.Items[0].Message)
	require.NotNil(t, page.Items[0].SourceEventID)
	assert.Equal(t, page.Items[0].RequestID, *page.Items[0].SourceEventID)
	_, err = remote.CompleteExecutionActivity(t.Context(), uuid.New(),
		claim.Lease, input)
	require.ErrorIs(t, err, commerr.ErrPermissionDenied)
}
