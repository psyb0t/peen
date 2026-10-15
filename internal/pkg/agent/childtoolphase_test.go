package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/client"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type childPhaseGuardStore struct {
	*session.Store
	runID               uuid.UUID
	checked             int
	loseAcknowledgement bool
	pauseAfterCommit    bool
}

func (s *childPhaseGuardStore) CompleteExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
	input session.CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	var state workflowToolPhaseState
	if err := json.Unmarshal(input.Output, &state); err != nil {
		return nil, ctxerrors.Wrap(err, "decode child guard output")
	}
	if state.LastPhase == workflowToolPhasePre {
		page, err := s.ListAgentRunEvents(ctx, sessionID, s.runID,
			session.ListAgentRunEventsOptions{})
		if err != nil {
			return nil, ctxerrors.Wrap(err, "read child pre-commit events")
		}
		for _, event := range page.Items {
			if event.EventType == EventTypeAgentRunToolUse {
				return nil, ctxerrors.Wrap(commerr.ErrConflict,
					"child tool start committed before phase")
			}
		}
		s.checked++
	}

	activity, err := s.Store.CompleteExecutionActivity(ctx, sessionID,
		lease, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "commit guarded child phase")
	}

	if state.LastPhase == workflowToolPhasePre && s.loseAcknowledgement {
		s.loseAcknowledgement = false

		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"lost child phase acknowledgement")
	}

	if state.LastPhase == workflowToolPhasePre && s.pauseAfterCommit {
		s.pauseAfterCommit = false
		if _, err := s.PauseExecution(ctx, sessionID, activity.ExecutionID,
			"retain committed child phase"); err != nil {
			return nil, ctxerrors.Wrap(err, "pause committed child phase")
		}
	}

	return activity, nil
}

func TestPhasedChildCallbacksCommitStartWithPhase(t *testing.T) {
	t.Parallel()
	fixture, input, original := childModelFixture(t, elelemtest.ToolCall(runtimeToolCallID,
		toolNameReadFile, runtimeToolArguments(t, runtimeToolFileName)))
	input.Execution.MaxConcurrentTools = 2
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)
	model, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	guard := &childPhaseGuardStore{
		Store: fixture.base.store,
		runID: original.run.ID,
	}
	fixture.engine.options.Store = guard
	input.Execution.Context = model.Output.Context
	result, err := fixture.engine.toolPhase(t.Context(), workflowToolPhaseInput{
		Tool:  workflowToolInput{Round: input, Call: model.Output.Calls[0]},
		Phase: workflowToolPhasePre,
	})
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionExecute, result.Decision)
	assert.Equal(t, 1, guard.checked)
}

func TestPhasedChildCallbacksRetainToolErrors(t *testing.T) {
	t.Parallel()
	fixture, input, original := childModelFixture(t, elelemtest.ToolCall(runtimeToolCallID,
		toolNameReadFile, runtimeToolArguments(t, "missing.txt")),
		elelemtest.Text(runtimeToolFinalText))
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 2
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)
	instance := startPhasedWorkflow(t, fixture)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	page, err := fixture.base.store.ListAgentRunMessages(t.Context(),
		input.Execution.SessionID, original.run.ID,
		session.ListAgentRunMessagesOptions{})
	require.NoError(t, err)
	count := 0
	for _, message := range page.Items {
		if message.ToolCallID == runtimeToolCallID {
			assert.True(t, message.IsError)
			count++
		}
	}
	assert.Equal(t, 1, count)
}

func TestPhasedChildCallbacksRunThroughOwnedDispatcher(t *testing.T) {
	t.Parallel()
	fixture, input, original := childModelFixture(t,
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)),
		elelemtest.Text(runtimeToolFinalText))
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 2
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace, runtimeToolFileName),
		runtimeToolFileBody)
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)
	shared, err := dispatch.child.resolve(t.Context(), input)
	require.NoError(t, err)
	inbox := shared.actor.run.inbox
	instance := startPhasedWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	assert.Same(t, inbox, shared.actor.run.inbox)
	stored, err := fixture.base.store.GetAgentRun(t.Context(),
		input.Execution.SessionID, original.run.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateCompleted, stored.State)
	assert.Contains(t, stored.ResponseMessagesJSON, "alpha")
	assert.Contains(t, stored.ResponseMessagesJSON, result.Text)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
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
		phases = append(phases, state.LastPhase)
	}
	assert.Equal(t, []workflowToolPhase{
		workflowToolPhasePre, workflowToolPhaseHandler,
		workflowToolPhaseOutcome, workflowToolPhasePost, workflowToolPhaseProject,
	}, phases)
}
