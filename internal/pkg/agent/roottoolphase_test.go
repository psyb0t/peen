package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type phaseProjectionGuardStore struct {
	*session.Store
	checked             int
	loseAcknowledgement bool
	pauseAfterCommit    bool
}

func (s *phaseProjectionGuardStore) CompleteExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
	input session.CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	var state workflowToolPhaseState
	if err := json.Unmarshal(input.Output, &state); err != nil {
		return nil, ctxerrors.Wrap(err, "decode projection guard output")
	}
	if state.LastPhase == workflowToolPhasePre {
		page, err := s.ListEvents(ctx, sessionID, session.ListEventsOptions{
			EventTypes: []string{EventTypeToolUse},
		})
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read pre-commit tool events")
		}
		if len(page.Items) != 0 {
			return nil, ctxerrors.Wrap(commerr.ErrConflict,
				"tool events committed before phase receipt")
		}
		s.checked++
	}

	activity, err := s.Store.CompleteExecutionActivity(ctx, sessionID,
		lease, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "save guarded phase")
	}

	if state.LastPhase == workflowToolPhasePre && s.loseAcknowledgement {
		s.loseAcknowledgement = false

		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"lost phase completion acknowledgement")
	}

	if state.LastPhase == workflowToolPhasePre && s.pauseAfterCommit {
		s.pauseAfterCommit = false
		if _, err := s.PauseExecution(ctx, sessionID, activity.ExecutionID,
			"retain committed phase"); err != nil {
			return nil, ctxerrors.Wrap(err, "pause committed phase")
		}
	}

	return activity, nil
}

func TestPhasedRootCallbacksCommitToolEventsWithPhaseReceipt(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)))
	fixture, callbacks, _ := newRootCallbackFixture(t, driver)
	model, err := fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	guard := &phaseProjectionGuardStore{Store: fixture.base.store}
	fixture.engine.options.Store = guard
	fixture.engine.options.ToolPhase = callbacks.toolPhase
	input := workflowToolPhaseInput{
		Tool: workflowToolInput{
			Round: workflowRoundInput{Execution: fixture.input},
			Call:  model.Output.Calls[0],
		},
		Phase: workflowToolPhasePre,
	}
	input.Tool.Round.Execution.Context = model.Output.Context
	result, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionExecute, result.Decision)
	assert.Equal(t, 1, guard.checked)
}

func TestPhasedRootCallbacksReuseAfterLostProjectionAcknowledgement(
	t *testing.T,
) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)))
	fixture, callbacks, shared := newRootCallbackFixture(t, driver)
	model, err := fixture.engine.modelRound(t.Context(), workflowRoundInput{
		Execution: fixture.input,
	})
	require.NoError(t, err)
	guard := &phaseProjectionGuardStore{
		Store:               fixture.base.store,
		loseAcknowledgement: true,
	}
	fixture.engine.options.Store = guard
	fixture.engine.options.ToolPhase = callbacks.toolPhase
	input := workflowToolPhaseInput{
		Tool: workflowToolInput{
			Round: workflowRoundInput{Execution: fixture.input},
			Call:  model.Output.Calls[0],
		},
		Phase: workflowToolPhasePre,
	}
	input.Tool.Round.Execution.Context = model.Output.Context
	delivered := 0
	shared.prepared.turn.sink = func(event Event) error {
		if event.Type == EventTypeToolUse {
			delivered++
		}

		return nil
	}
	first, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReconcile, first.Decision)
	assert.Zero(t, delivered)
	for range 2 {
		reused, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	}
	assert.Equal(t, 1, delivered)
	assert.Equal(t, 1, guard.checked)
	assert.Equal(t, 1, driver.Calls())
	page, err := fixture.base.store.ListEvents(t.Context(), fixture.input.SessionID,
		session.ListEventsOptions{EventTypes: []string{EventTypeToolUse}})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	assert.True(t, shared.prepared.turn.pendingTranscript().isEmpty())
}

func TestPhasedRootCallbacksRunThroughOwnedDispatcher(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)),
		elelemtest.Text(runtimeToolFinalText))
	fixture, _ := newRootModelWorkflowFixture(t, driver)
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace, runtimeToolFileName),
		runtimeToolFileBody)
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)
	shared, err := dispatch.root.resolve(t.Context(), fixture.input)
	require.NoError(t, err)
	queue := shared.prepared.userMessages
	instance := startPhasedWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	assert.Equal(t, 2, driver.Calls())
	assert.Same(t, queue, shared.prepared.userMessages)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	phases := []workflowToolPhase{}
	for _, activity := range page.Items {
		if activity.Name != toolPhaseActivityName {
			continue
		}

		assert.Equal(t, models.ActivityStateCompleted, activity.State)
		var state workflowToolPhaseState
		require.NoError(t, json.Unmarshal([]byte(*activity.OutputJSON), &state))
		assert.Equal(t, int64(1), state.StreamAttempt)
		assert.False(t, state.StartedAt.IsZero())
		phases = append(phases, state.LastPhase)
	}
	assert.Equal(t, []workflowToolPhase{
		workflowToolPhasePre, workflowToolPhaseHandler,
		workflowToolPhaseOutcome, workflowToolPhasePost, workflowToolPhaseProject,
	}, phases)
	messages, err := fixture.base.store.ListMessages(t.Context(),
		fixture.input.SessionID, session.ListMessagesOptions{
			Order: session.PageOrderAscending,
		})
	require.NoError(t, err)
	results := 0
	for _, message := range messages.Items {
		if message.Role == models.MessageRoleTool {
			results++
			assert.Equal(t, runtimeToolCallID, message.ToolCallID)
			assert.Contains(t, message.Content, "alpha")
		}
	}
	assert.Equal(t, 1, results)
}

func TestPhasedRootCallbacksMergeParallelPrivateReadScopes(t *testing.T) {
	t.Parallel()
	paths := []string{"first.txt", "second.txt"}
	deltas := make([]elelem.Delta, len(paths))
	for index, path := range paths {
		deltas[index].ToolCall = &elelem.ToolCallDelta{
			Index: index, ID: path, Name: toolNameReadFile,
			Arguments: runtimeToolArguments(t, path),
		}
	}
	deltas[len(deltas)-1].FinishReason = elelem.FinishReasonToolCalls
	driver := elelemtest.NewScriptedDriver(elelemtest.Turn{Deltas: deltas},
		elelemtest.Text(runtimeToolFinalText))
	fixture, _ := newRootModelWorkflowFixture(t, driver)
	fixture.input.MaxConcurrentTools = len(paths)
	for _, path := range paths {
		writeRuntimeFile(t, filepath.Join(fixture.base.workspace, path), path)
	}

	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)
	instance := startPhasedWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(result.Context, &conversation))
	require.NotNil(t, conversation.Runtime)
	assert.Len(t, conversation.Runtime.Observations, len(paths))
	assert.Equal(t, runtimeToolFinalText, result.Text)
	assert.Equal(t, 2, driver.Calls())
}
