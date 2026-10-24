package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pauseAfterToolPhaseStore struct {
	session.ExecutionStorage
	engine *workflowExecution
	paused chan struct{}
	once   sync.Once
}

func (s *pauseAfterToolPhaseStore) CompleteExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
	input session.CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	activity, err := s.ExecutionStorage.CompleteExecutionActivity(ctx,
		sessionID, lease, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "commit phase pause fixture")
	}

	if activity.Name != toolPhaseActivityName {
		return activity, nil
	}

	var state workflowToolPhaseState
	if err := json.Unmarshal([]byte(*activity.OutputJSON), &state); err != nil {
		return nil, ctxerrors.Wrap(err, "decode committed phase pause fixture")
	}

	if state.LastPhase == workflowToolPhasePre {
		s.once.Do(func() {
			err = s.engine.pause(context.WithoutCancel(ctx), sessionID,
				activity.ExecutionID, "pause between phases")
			if err == nil {
				close(s.paused)
			}
		})
		if err != nil {
			return nil, ctxerrors.Wrap(err, "pause after committed pre-hook")
		}
	}

	return activity, nil
}

func startPhasedWorkflow(
	t *testing.T,
	fixture executionLoopFixture,
) *workflow.Instance {
	t.Helper()
	require.NoError(t, fixture.worker.Start(fixture.engine.options.Lifecycle))
	fixture.started.Store(true)
	instance, err := fixture.api.CreateWorkflowInstance(t.Context(),
		client.WorkflowInstanceOptions{
			InstanceID: fixture.input.ExecutionID.String(),
			Queue:      workflow.Queue(fixture.input.SessionID.String()),
		}, agentWorkflowName, fixture.input)
	require.NoError(t, err)

	return instance
}

func TestPhasedWorkflowRunsModelAndSeparateToolPhases(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)),
		elelemtest.Text(runtimeToolFinalText),
	)
	base := newRuntimeFixture(t, driver)
	input := newWorkflowTestInput(t, base)
	fixture := newExecutionLoopFixtureWithInput(t, base, input, true)
	writeRuntimeFile(t, filepath.Join(base.workspace, runtimeToolFileName),
		runtimeToolFileBody)
	phases := []workflowToolPhase{}
	fixture.engine.options.ToolPhase = func(ctx context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		phases = append(phases, input.Phase)
		if input.State.LastPhase != "" {
			assert.JSONEq(t, string(input.State.Outcome.Context),
				string(input.Tool.Round.Execution.Context))
		}
		state := input.State
		if input.Phase == workflowToolPhaseHandler {
			outcome, err := fixture.engine.options.ToolCall(ctx, input.Tool)
			if err != nil {
				return state, ctxerrors.Wrap(err, "read phase fixture file")
			}

			state.Outcome = outcome
		}

		var conversation workflowConversation
		if err := json.Unmarshal(input.Tool.Round.Execution.Context,
			&conversation); err != nil {
			return state, ctxerrors.Wrap(err, "decode phase fixture context")
		}
		if input.State.LastPhase != "" {
			assert.Equal(t, string(input.State.LastPhase), conversation.SystemPrompt)
		}

		conversation.SystemPrompt = string(input.Phase)
		encoded, err := json.Marshal(conversation)
		if err != nil {
			return state, ctxerrors.Wrap(err, "encode phase fixture context")
		}

		state.Outcome.Context = encoded

		return state, nil
	}
	instance := startPhasedWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	assert.Equal(t, []workflowToolPhase{
		workflowToolPhasePre, workflowToolPhaseHandler, workflowToolPhaseOutcome,
		workflowToolPhasePost, workflowToolPhaseProject,
	}, phases)
	assert.Equal(t, 2, driver.Calls())
	page, err := base.store.ListExecutionActivities(t.Context(),
		input.SessionID, input.ExecutionID, session.ListEventsOptions{})
	require.NoError(t, err)
	count := 0
	for _, activity := range page.Items {
		if activity.Name != toolPhaseActivityName {
			continue
		}

		count++
		assert.Equal(t, models.ActivityStateCompleted, activity.State)
	}
	assert.Equal(t, len(phases), count)
	var saved workflowConversation
	require.NoError(t, json.Unmarshal(result.Context, &saved))
	assert.Contains(t, saved.Messages[len(saved.Messages)-2].Text(), "alpha")
}

func TestPhasedWorkflowProjectsEarlyFailureWithoutMoreEffects(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.ToolCall(
		runtimeToolCallID, toolNameReadFile, runtimeToolArguments(t,
			runtimeToolFileName)))
	base := newRuntimeFixture(t, driver)
	input := newWorkflowTestInput(t, base)
	fixture := newExecutionLoopFixtureWithInput(t, base, input, true)
	phases := []workflowToolPhase{}
	fixture.engine.options.ToolPhase = func(_ context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		phases = append(phases, input.Phase)
		state := input.State
		if input.Phase == workflowToolPhasePre {
			state.Outcome = failedWorkflowToolResult(commerr.ErrPermissionDenied)
		}

		return state, nil
	}
	instance := startPhasedWorkflow(t, fixture)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.Error(t, err)
	assert.Equal(t, []workflowToolPhase{
		workflowToolPhasePre, workflowToolPhaseProject,
	}, phases)
	assert.Equal(t, 1, driver.Calls())
	execution, err := base.store.GetExecution(t.Context(), input.SessionID,
		input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateFailed, execution.State)
}

func TestPhasedWorkflowBoundsConcurrentToolHandlers(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Turn{Deltas: []elelem.Delta{
			{ToolCall: &elelem.ToolCallDelta{
				Index: 0, ID: runtimeToolCallID, Name: toolNameReadFile,
				Arguments: runtimeToolArguments(t, runtimeToolFileName),
			}},
			{ToolCall: &elelem.ToolCallDelta{
				Index: 1, ID: "second", Name: toolNameReadFile,
				Arguments: runtimeToolArguments(t, runtimeToolFileName),
			}},
			{ToolCall: &elelem.ToolCallDelta{
				Index: 2, ID: "third", Name: toolNameReadFile,
				Arguments: runtimeToolArguments(t, runtimeToolFileName),
			}, FinishReason: elelem.FinishReasonToolCalls},
		}},
		elelemtest.Text(runtimeToolFinalText))
	base := newRuntimeFixture(t, driver)
	input := newWorkflowTestInput(t, base)
	fixture := newExecutionLoopFixtureWithInput(t, base, input, true)
	writeRuntimeFile(t, filepath.Join(base.workspace, runtimeToolFileName),
		runtimeToolFileBody)
	started := make(chan struct{}, input.MaxConcurrentTools)
	release := make(chan struct{})
	closeRelease := sync.OnceFunc(func() { close(release) })
	t.Cleanup(closeRelease)
	var active, maximum, handlers atomic.Int64

	fixture.engine.options.ToolPhase = func(ctx context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		state := input.State
		if input.Phase != workflowToolPhaseHandler {
			return state, nil
		}

		handlers.Add(1)
		current := active.Add(1)
		defer active.Add(-1)
		for previous := maximum.Load(); current > previous; previous = maximum.Load() {
			if maximum.CompareAndSwap(previous, current) {
				break
			}
		}

		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return state, ctxerrors.Wrap(ctx.Err(), "wait for parallel phase")
		}

		outcome, err := fixture.engine.options.ToolCall(ctx, input.Tool)
		state.Outcome = outcome

		return state, err
	}
	instance := startPhasedWorkflow(t, fixture)
	for range input.MaxConcurrentTools {
		select {
		case <-started:
		case <-time.After(messageTestLongWait):
			t.Fatal("tool handlers did not run concurrently")
		}
	}
	closeRelease()
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	assert.Equal(t, 2, driver.Calls())
	assert.Equal(t, int64(input.MaxConcurrentTools), maximum.Load())
	assert.Equal(t, int64(3), handlers.Load())
}

func TestPhasedWorkflowResumesAfterCommittedPreHook(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)),
		elelemtest.Text(runtimeToolFinalText))
	base := newRuntimeFixture(t, driver)
	input := newWorkflowTestInput(t, base)
	fixture := newExecutionLoopFixtureWithInput(t, base, input, true)
	writeRuntimeFile(t, filepath.Join(base.workspace, runtimeToolFileName),
		runtimeToolFileBody)
	store := &pauseAfterToolPhaseStore{
		ExecutionStorage: base.store, engine: fixture.engine,
		paused: make(chan struct{}),
	}
	fixture.engine.options.Store = store
	var preHooks, handlers atomic.Int64
	fixture.engine.options.ToolPhase = func(ctx context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		state := input.State
		if input.Phase == workflowToolPhasePre {
			preHooks.Add(1)
		}

		if input.Phase == workflowToolPhaseHandler {
			handlers.Add(1)
			outcome, err := fixture.engine.options.ToolCall(ctx, input.Tool)
			if err != nil {
				return state, ctxerrors.Wrap(err, "read resumed phase fixture file")
			}

			state.Outcome = outcome
		}

		return state, nil
	}
	instance := startPhasedWorkflow(t, fixture)
	select {
	case <-store.paused:
	case <-time.After(messageTestLongWait):
		t.Fatal("workflow did not pause after its pre-hook checkpoint")
	}
	paused, err := base.store.GetExecution(t.Context(), input.SessionID,
		input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStatePaused, paused.State)
	assert.Equal(t, int64(1), preHooks.Load())
	assert.Zero(t, handlers.Load())
	assert.Equal(t, 1, driver.Calls())
	_, err = base.store.ResumeExecution(t.Context(), input.SessionID,
		input.ExecutionID)
	require.NoError(t, err)
	require.NoError(t, fixture.api.SignalWorkflow(t.Context(),
		instance.InstanceID, workflowResumeSignal, struct{}{}))
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	assert.Equal(t, int64(1), preHooks.Load())
	assert.Equal(t, int64(1), handlers.Load())
	assert.Equal(t, 2, driver.Calls())
}
