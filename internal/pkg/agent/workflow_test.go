package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cschleiden/go-workflows/backend/history"
	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/core"
	workflowworker "github.com/cschleiden/go-workflows/worker"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/psyb0t/peen/internal/pkg/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type executionLoopFixture struct {
	base    runtimeFixture
	engine  *workflowExecution
	worker  *workflowworker.Worker
	storage *db.WorkflowStorage
	api     *client.Client
	input   workflowExecutionInput
	stop    func()
	started *atomic.Bool
}

func newExecutionLoopFixture(
	t *testing.T,
	driver *elelemtest.ScriptedDriver,
	streaming bool,
) executionLoopFixture {
	t.Helper()
	base := newRuntimeFixture(t, driver)
	input := newWorkflowTestInput(t, base)

	return newExecutionLoopFixtureWithInput(t, base, input, streaming)
}

func newExecutionLoopFixtureWithInput(
	t *testing.T,
	base runtimeFixture,
	input workflowExecutionInput,
	streaming bool,
) executionLoopFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	storage, err := db.OpenWorkflowStorage(ctx,
		db.Config{Directory: base.stateDirectory})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	backend, err := storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	executor, err := tools.NewExecutor(tools.Options{Workspace: base.workspace})
	require.NoError(t, err)
	engine := &workflowExecution{options: workflowExecutionOptions{
		Store: base.store, GenerationID: uuid.New(), Lifecycle: ctx,
		MergeRound: workflowMergeRound,
		PrepareRound: func(_ context.Context, input workflowRoundInput) (
			json.RawMessage, error,
		) {
			return input.Execution.Context, nil
		},
		ModelRound: func(ctx context.Context, input workflowRoundInput) (
			workflowRoundResult, error,
		) {
			request, conversation, err := workflowModelRequest(base.runtime.models,
				input)
			if err != nil {
				return workflowRoundResult{}, err
			}

			return workflowRunModelRequest(ctx, request.WithStreaming(streaming),
				conversation)
		},
		ToolCall: workflowReadTool(executor),
	}}
	engine.options.ToolPhase = func(ctx context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		state := input.State
		if input.Phase != workflowToolPhaseHandler {
			return state, nil
		}
		outcome, err := engine.options.ToolCall(ctx, input.Tool)
		if err != nil {
			return state, ctxerrors.Wrap(err, "execute fixture tool handler")
		}
		state.Outcome = outcome

		return state, nil
	}
	options := workflowworker.DefaultOptions
	queue := workflow.Queue(input.SessionID.String())
	options.WorkflowQueues = []workflow.Queue{queue}
	options.ActivityQueues = []workflow.Queue{queue}
	worker := workflowworker.New(backend, &options)
	require.NoError(t, engine.register(worker))
	started := &atomic.Bool{}
	stop := sync.OnceFunc(func() {
		cancel()
		if started.Load() {
			require.NoError(t, worker.WaitForCompletion())
		}
	})
	t.Cleanup(stop)

	return executionLoopFixture{
		base: base, engine: engine, worker: worker, storage: storage,
		api: client.New(backend), input: input, stop: stop, started: started,
	}
}

func newWorkflowTestInput(
	t *testing.T,
	base runtimeFixture,
) workflowExecutionInput {
	t.Helper()
	sessionID := base.runtime.sessionID
	lease, err := base.store.AcquireTurn(t.Context(), sessionID,
		session.StartTurnInput{RequestID: uuid.New(), Workspace: base.workspace})
	require.NoError(t, err)
	encoded, err := json.Marshal(workflowConversation{
		ModelReference: runtimeTestModelReference,
		SystemPrompt:   "Inspect the example file.",
		Messages: []elelem.Message{{
			Role: elelem.RoleUser, Content: elelem.Text("read example file"),
		}},
		MaxContextTokens: runtimeTestMaxContextTokens,
		Tools: []workflowToolDefinition{{
			Name: toolNameReadFile, Description: "Read a file",
			ArgumentsSchema: json.RawMessage(`{"type":"object"}`),
		}},
	})
	require.NoError(t, err)
	execution, err := base.store.CreateExecution(t.Context(), sessionID,
		session.CreateExecutionInput{
			ID: uuid.New(), RootTurnID: lease.TurnID,
			WorkflowVersion: agentWorkflowName, Task: "read example file",
			WorkingDirectory: base.workspace, Context: encoded,
		})
	require.NoError(t, err)

	return workflowExecutionInput{
		SessionID: sessionID, ExecutionID: execution.ID,
		MaxRounds: 4, MaxConcurrentTools: 2, Context: encoded,
	}
}

func workflowReadTool(executor *tools.Executor) func(
	context.Context, workflowToolInput,
) (workflowToolResult, error) {
	tool := hostTool(toolNameReadFile, readFileDescription, readFileSchema,
		executor.ReadFile, nil)
	set := elelem.NewToolSet(tool)

	return func(ctx context.Context, input workflowToolInput) (
		workflowToolResult, error,
	) {
		return runWorkflowTool(ctx, set, input, workflowToolOptions{
			Timeout:         defaultToolTimeout,
			MaxResultTokens: defaultMaxToolResultTokens,
		})
	}
}

func (f executionLoopFixture) start(t *testing.T) *workflow.Instance {
	t.Helper()
	require.NoError(t, f.worker.Start(f.engine.options.Lifecycle))
	f.started.Store(true)
	instance, err := f.api.CreateWorkflowInstance(t.Context(),
		client.WorkflowInstanceOptions{
			InstanceID: fmt.Sprintf("%s/%s", f.input.SessionID, f.input.ExecutionID),
			Queue:      workflow.Queue(f.input.SessionID.String()),
		}, agentWorkflowName, f.input)
	require.NoError(t, err)

	return instance
}

func TestExecutionWorkflowUsesSeparateModelAndToolActivities(t *testing.T) {
	t.Parallel()
	for _, streaming := range []bool{true, false} {
		t.Run(fmt.Sprintf("streaming=%t", streaming), func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver(
				elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
					runtimeToolArguments(t, runtimeToolFileName)),
				elelemtest.Text(runtimeToolFinalText),
			)
			fixture := newExecutionLoopFixture(t, driver, streaming)
			writeRuntimeFile(t, filepath.Join(fixture.base.workspace,
				runtimeToolFileName), runtimeToolFileBody)
			instance := fixture.start(t)
			result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
				fixture.api, instance, workflowTestResultTimeout)
			require.NoError(t, err)
			assert.Equal(t, runtimeToolFinalText, result.Text)
			assert.Equal(t, []bool{streaming, streaming}, driver.Streamed())
			requests := driver.Requests()
			require.Len(t, requests, 2)
			assert.Equal(t, elelem.RoleTool,
				requests[1].Messages[len(requests[1].Messages)-1].Role)
			page, err := fixture.base.store.ListExecutionActivities(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID,
				session.ListEventsOptions{})
			require.NoError(t, err)
			names := make([]string, 0, len(page.Items))
			for _, activity := range page.Items {
				names = append(names, activity.Name)
			}
			assert.Equal(t, []string{
				prepareActivityName, compactionActivityName, modelActivityName,
				toolPhaseActivityName, toolPhaseActivityName, toolPhaseActivityName,
				toolPhaseActivityName, toolPhaseActivityName,
				prepareActivityName, compactionActivityName, modelActivityName,
				terminalHooksActivityName, session.ExecutionFinishActivityName,
			}, names)
			for _, activity := range page.Items {
				assert.Equal(t, models.ActivityStateCompleted, activity.State)
			}
			state, err := fixture.api.GetWorkflowInstanceState(t.Context(), instance)
			require.NoError(t, err)
			assert.Equal(t, core.WorkflowInstanceStateFinished, state)
			execution, err := fixture.base.store.GetExecution(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID)
			require.NoError(t, err)
			assert.Equal(t, models.ExecutionStateCompleted, execution.State)
			assert.Equal(t, string(result.Context), execution.ContextJSON)
			fixture.stop()
		})
	}
}

func TestExecutionWorkflowParallelToolsRetainHookInjections(t *testing.T) {
	t.Parallel()
	arguments := runtimeToolArguments(t, runtimeToolFileName)
	driver := elelemtest.NewScriptedDriver(elelemtest.Turn{
		Deltas: []elelem.Delta{
			{ToolCall: &elelem.ToolCallDelta{
				Index: 0, ID: "first", Name: toolNameReadFile, Arguments: arguments,
			}},
			{ToolCall: &elelem.ToolCallDelta{
				Index: 1, ID: "second", Name: toolNameReadFile, Arguments: arguments,
			}, FinishReason: elelem.FinishReasonToolCalls},
		},
	}, elelemtest.Text(runtimeToolFinalText))
	fixture := newExecutionLoopFixture(t, driver, true)
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace,
		runtimeToolFileName), runtimeToolFileBody)
	executor, err := tools.NewExecutor(tools.Options{
		Workspace: fixture.base.workspace,
	})
	require.NoError(t, err)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	tool := hostTool(toolNameReadFile, readFileDescription, readFileSchema,
		executor.ReadFile, nil)
	tool.PreRun = func(ctx context.Context, _ *elelem.ToolEvent) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctxerrors.Wrap(ctx.Err(), "parallel tool gate")
		}
	}
	tool.PostRunMessageInjector = func(context.Context, *elelem.ToolEvent) (
		*elelem.MessageInjection, error,
	) {
		return &elelem.MessageInjection{
			Type: elelem.RoleUser, Content: "active tool hook instructions",
		}, nil
	}
	set := elelem.NewToolSet(tool)
	fixture.engine.options.ToolCall = func(
		ctx context.Context, input workflowToolInput,
	) (workflowToolResult, error) {
		return runWorkflowTool(ctx, set, input, workflowToolOptions{})
	}
	instance := fixture.start(t)
	for range 2 {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("both tool activities did not start")
		}
	}
	assert.Equal(t, 1, driver.Calls())
	close(release)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	messages := requests[1].Messages
	require.Len(t, messages, 7)
	assert.Equal(t, "first", messages[3].ToolCallID)
	assert.Equal(t, "second", messages[4].ToolCallID)
	assert.Equal(t, elelem.RoleTool, messages[3].Role)
	assert.Equal(t, elelem.RoleTool, messages[4].Role)
	assert.Equal(t, "active tool hook instructions", messages[5].Text())
	assert.Equal(t, "active tool hook instructions", messages[6].Text())
	fixture.stop()
}

type workflowFailedClaimStore struct {
	session.ExecutionStorage
	attempts   atomic.Int32
	failed     chan struct{}
	finalizing chan struct{}
	finalOnce  sync.Once
}

func (s *workflowFailedClaimStore) BeginExecutionActivity(
	ctx context.Context,
	sessionID, executionID, generationID uuid.UUID,
	input session.ScheduleActivityInput,
) (*session.ActivityClaim, error) {
	if input.StepID == "tool:0:0:preHook" {
		if int(s.attempts.Add(1)) == workflow.DefaultRetryOptions.MaxAttempts {
			close(s.failed)
		}

		return nil, ctxerrors.Wrap(commerr.ErrFetchFailed,
			"example first tool claim unavailable")
	}

	if input.Name == session.ExecutionFinishActivityName {
		s.finalOnce.Do(func() { close(s.finalizing) })
	}

	return s.ExecutionStorage.BeginExecutionActivity(ctx,
		sessionID, executionID, generationID, input)
}

func TestExecutionWorkflowJoinsOtherToolsAfterOneClaimFails(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Turn{
		Deltas: []elelem.Delta{
			{ToolCall: &elelem.ToolCallDelta{
				Index: 0, ID: "first", Name: toolNameReadFile,
				Arguments: `{}`,
			}},
			{ToolCall: &elelem.ToolCallDelta{
				Index: 1, ID: "second", Name: toolNameReadFile,
				Arguments: `{}`,
			}, FinishReason: elelem.FinishReasonToolCalls},
		},
	})
	fixture := newExecutionLoopFixture(t, driver, true)
	store := &workflowFailedClaimStore{
		ExecutionStorage: fixture.base.store,
		failed:           make(chan struct{}), finalizing: make(chan struct{}),
	}
	fixture.engine.options.Store = store
	entered, release := make(chan struct{}), make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	t.Cleanup(finish)
	fixture.engine.options.ToolCall = func(
		ctx context.Context, _ workflowToolInput,
	) (workflowToolResult, error) {
		close(entered)
		select {
		case <-release:
			return workflowToolResult{Result: elelem.ToolResult{
				Content: "second tool completed",
			}}, nil
		case <-ctx.Done():
			return workflowToolResult{}, ctxerrors.Wrap(ctx.Err(),
				"wait for parallel tool release")
		}
	}
	instance := fixture.start(t)
	for _, barrier := range []<-chan struct{}{entered, store.failed} {
		select {
		case <-barrier:
		case <-time.After(10 * time.Second):
			t.Fatal("parallel failure barriers did not complete")
		}
	}
	require.Never(t, func() bool {
		select {
		case <-store.finalizing:
			return true
		default:
			return false
		}
	}, 500*time.Millisecond, 10*time.Millisecond)
	finish()
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.Error(t, err)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	found := false
	for _, activity := range page.Items {
		if activity.StepID != "tool:0:1:handler" {
			continue
		}
		found = true
		assert.Equal(t, models.ActivityStateCompleted, activity.State)
		require.NotNil(t, activity.OutputJSON)
		assert.Contains(t, *activity.OutputJSON, "second tool completed")
	}
	require.True(t, found)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateFailed, execution.State)
	assert.Equal(t, 1, driver.Calls())
	fixture.stop()
}

type workflowPausedClaimStore struct {
	session.ExecutionStorage
	attempts atomic.Int32
	entered  chan struct{}
	release  chan struct{}
}

func (s *workflowPausedClaimStore) BeginExecutionActivity(
	ctx context.Context,
	sessionID, executionID, generationID uuid.UUID,
	input session.ScheduleActivityInput,
) (*session.ActivityClaim, error) {
	if input.Name == toolPhaseActivityName && s.attempts.Add(1) <= 2 {
		s.entered <- struct{}{}
		<-s.release
	}

	return s.ExecutionStorage.BeginExecutionActivity(context.WithoutCancel(ctx),
		sessionID, executionID, generationID, input)
}

func TestExecutionWorkflowOneResumeSignalContinuesParallelPausedTools(
	t *testing.T,
) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Turn{
		Deltas: []elelem.Delta{
			{ToolCall: &elelem.ToolCallDelta{
				Index: 0, ID: "first", Name: toolNameReadFile, Arguments: `{}`,
			}},
			{ToolCall: &elelem.ToolCallDelta{
				Index: 1, ID: "second", Name: toolNameReadFile, Arguments: `{}`,
			}, FinishReason: elelem.FinishReasonToolCalls},
		},
	}, elelemtest.Text(runtimeToolFinalText))
	fixture := newExecutionLoopFixture(t, driver, true)
	store := &workflowPausedClaimStore{
		ExecutionStorage: fixture.base.store,
		entered:          make(chan struct{}, 2), release: make(chan struct{}),
	}
	finish := sync.OnceFunc(func() { close(store.release) })
	t.Cleanup(finish)
	fixture.engine.options.Store = store
	var calls atomic.Int32
	fixture.engine.options.ToolCall = func(
		_ context.Context, input workflowToolInput,
	) (workflowToolResult, error) {
		calls.Add(1)

		return workflowToolResult{Result: elelem.ToolResult{
			Content: input.Call.ID,
		}}, nil
	}
	instance := fixture.start(t)
	for range 2 {
		select {
		case <-store.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("parallel tool claims did not reach pause gate")
		}
	}
	require.NoError(t, fixture.engine.pause(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID, "leave both tools paused"))
	finish()
	require.Eventually(t, func() bool {
		events, err := fixture.storage.GetWorkflowInstanceHistory(
			t.Context(), instance, nil)
		if err != nil {
			return false
		}

		completed := 0
		for _, event := range events {
			if event.Type == history.EventType_ActivityCompleted {
				completed++
			}
		}

		return completed >= 4
	}, 10*time.Second, 10*time.Millisecond)
	assert.Zero(t, calls.Load())
	_, err := fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	require.NoError(t, fixture.api.SignalWorkflow(t.Context(), instance.InstanceID,
		workflowResumeSignal, struct{}{}))
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	assert.EqualValues(t, 2, calls.Load())
	assert.Equal(t, 2, driver.Calls())
	requests := driver.Requests()
	require.Len(t, requests, 2)
	messages := requests[1].Messages
	require.Len(t, messages, 5)
	assert.Equal(t, elelem.RoleTool, messages[3].Role)
	assert.Equal(t, "first", messages[3].ToolCallID)
	assert.Equal(t, "first", messages[3].Text())
	assert.Equal(t, elelem.RoleTool, messages[4].Role)
	assert.Equal(t, "second", messages[4].ToolCallID)
	assert.Equal(t, "second", messages[4].Text())
	fixture.stop()
}
