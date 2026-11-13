package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/backend"
	"github.com/psyb0t/go-workflows/backend/history"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/go-workflows/core"
	workflowworker "github.com/psyb0t/go-workflows/worker"
	"github.com/psyb0t/go-workflows/workflow"
	"github.com/psyb0t/peen/internal/pkg/contentblob"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rolloverTestHistoryCap    = 100
	rolloverTestThreshold     = 70
	rolloverTestRounds        = 16
	rolloverTestChildRounds   = 12
	rolloverTestContextTokens = 131072
)

func rolloverTestTurns(t *testing.T, rounds int) []elelemtest.Turn {
	t.Helper()
	turns := make([]elelemtest.Turn, 0, rounds+1)
	for round := range rounds {
		path := fmt.Sprintf("round%d.txt", round)
		turns = append(turns, elelemtest.ToolCall(path, toolNameReadFile,
			runtimeToolArguments(t, path)))
	}

	return append(turns, elelemtest.Text(runtimeToolFinalText))
}

func newRolloverRootFixture(t *testing.T, turns []elelemtest.Turn) (
	executionLoopFixture, *workflowDispatch,
) {
	t.Helper()
	base := newRuntimeFixtureWithOptions(t,
		elelemtest.NewScriptedDriver(turns...), func(options *RuntimeOptions) {
			options.MaxContextTokens = rolloverTestContextTokens
		})
	prepared, err := base.runtime.prepareTurn(t.Context(), TurnRequest{
		RequestID: uuid.New(), Workspace: base.workspace,
		Message: "inspect the round files",
	})
	require.NoError(t, err)
	prepared.attachPublisher(t.Context())
	require.NoError(t, base.runtime.registerActiveUserMessageQueue(prepared))
	t.Cleanup(func() { base.runtime.closeActiveUserMessageQueue(prepared) })
	generationID := uuid.New()
	dispatch := newWorkflowDispatch(t.Context(), base.runtime, generationID, nil)
	input, err := dispatch.prepareRootSubmission(t.Context(), prepared)
	require.NoError(t, err)
	fixture := newExecutionLoopFixtureWithInput(t, base, input, true)
	fixture.engine.options.GenerationID = generationID
	dispatch.bind(&fixture.engine.options)

	return fixture, dispatch
}

func newRolloverChildFixture(t *testing.T, turns []elelemtest.Turn) (
	executionLoopFixture, *workflowDispatch,
) {
	t.Helper()
	fixture, input, sink := childModelFixture(t, turns...)
	fixture.input = input.Execution
	fixture.input.MaxConcurrentTools = 2
	fixture.base.runtime.maxContextTokens = rolloverTestContextTokens
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(fixture.input.Context, &conversation))
	conversation.MaxContextTokens = rolloverTestContextTokens
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	fixture.input.Context = encoded
	require.NoError(t, sink.emit(t.Context(), EventTypeAgentRunStarted,
		agentRunStartedPayload{AgentRunID: sink.run.ID}))
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)

	return fixture, dispatch
}

func startRolloverTestWorker(t *testing.T, fixture executionLoopFixture,
	storage backend.Backend,
) (*client.Client, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	fixture.engine.options.Lifecycle = ctx
	options := workflowworker.DefaultOptions
	options.MaxParallelWorkflowTasks = 1
	options.WorkflowPollers = 1
	queue := workflow.Queue(fixture.input.SessionID.String())
	options.WorkflowQueues = []workflow.Queue{queue}
	options.ActivityQueues = []workflow.Queue{queue}
	worker := workflowworker.New(storage, &options)
	require.NoError(t, fixture.engine.register(worker))
	require.NoError(t, worker.Start(ctx))
	stop := sync.OnceFunc(func() {
		cancel()
		require.NoError(t, worker.WaitForCompletion())
	})
	t.Cleanup(stop)

	return newWorkflowClient(storage, fixture.base.store), stop
}

//nolint:ireturn // Test workers use the production backend boundary.
func prepareRolloverTest(t *testing.T, fixture executionLoopFixture) (
	backend.Backend, *workflow.Instance,
) {
	t.Helper()
	for round := range rolloverTestRounds {
		path := fmt.Sprintf("round%d.txt", round)
		writeRuntimeFile(t, filepath.Join(fixture.base.workspace, path), path)
	}

	instance := nativeWorkflowInstance(fixture.input.SessionID,
		fixture.input.ExecutionID)
	repo := repositories.Use(fixture.base.handle.GormDB).Execution
	_, err := repo.WithContext(t.Context()).Where(
		repo.ID.Eq(fixture.input.ExecutionID),
	).UpdateColumnSimple(repo.WorkflowExecutionID.Value(instance.ExecutionID))
	require.NoError(t, err)
	fixture.base.handle.Workflows = fixture.storage
	storage, err := fixture.storage.WorkerBackend(t.Context(), uuid.New())
	require.NoError(t, err)
	backend.WithMaxHistorySize(rolloverTestHistoryCap)(storage.Options())
	storage.Options().Converter = contentblob.NewConverter(fixture.base.store)

	return storage, instance
}

func TestWorkflowRolloverRootAndChild(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprintf("child=%t", child), func(t *testing.T) {
			t.Parallel()
			rounds := rolloverTestRounds
			if child {
				rounds = rolloverTestChildRounds
			}
			turns := rolloverTestTurns(t, rounds)
			var fixture executionLoopFixture
			if child {
				fixture, _ = newRolloverChildFixture(t, turns)
			} else {
				fixture, _ = newRolloverRootFixture(t, turns)
			}

			fixture.input.HistoryThreshold = rolloverTestThreshold
			handoffs := observeRolloverContexts(t, &fixture)
			storage, original := prepareRolloverTest(t, fixture)
			api, _ := startRolloverTestWorker(t, fixture, storage)
			instance, err := api.CreateWorkflowInstance(t.Context(),
				client.WorkflowInstanceOptions{
					InstanceID: original.InstanceID,
					Queue:      workflow.Queue(fixture.input.SessionID.String()),
				}, agentWorkflowName, fixture.input)
			require.NoError(t, err)
			result, err := client.GetWorkflowResult[workflowRoundResult](
				t.Context(), api, instance, workflowTestResultTimeout)
			require.NoError(t, err)
			assert.Equal(t, runtimeToolFinalText, result.Text)
			assertRolloverTestHistory(t, fixture, storage, original, result, rounds)
			assert.GreaterOrEqual(t, handoffs(), 2)
		})
	}
}

func observeRolloverContexts(t *testing.T, fixture *executionLoopFixture) func() int {
	t.Helper()
	var mutex sync.Mutex
	snapshots := make(map[int]json.RawMessage)
	handoffs := 0
	merge := fixture.engine.options.MergeRound
	fixture.engine.options.MergeRound = func(encoded json.RawMessage,
		calls []workflowToolInput, results []workflowToolResult,
	) (json.RawMessage, error) {
		merged, err := merge(encoded, calls, results)
		if err == nil && len(calls) > 0 {
			mutex.Lock()
			snapshots[calls[0].Round.Round] = append(json.RawMessage(nil), merged...)
			mutex.Unlock()
		}

		return merged, err
	}
	prepare := fixture.engine.options.PrepareRound
	fixture.engine.options.PrepareRound = func(ctx context.Context,
		input workflowRoundInput,
	) (json.RawMessage, error) {
		if input.Execution.NextRound > 0 &&
			input.Round == input.Execution.NextRound {
			mutex.Lock()
			previous := snapshots[input.Round-1]
			handoffs++
			mutex.Unlock()
			assert.JSONEq(t, string(previous), string(input.Execution.Context))
		}

		return prepare(ctx, input)
	}

	return func() int {
		mutex.Lock()
		defer mutex.Unlock()

		return handoffs
	}
}

func assertRolloverTestHistory(t *testing.T, fixture executionLoopFixture,
	storage backend.Backend, original *workflow.Instance,
	result workflowRoundResult,
	rounds int,
) {
	t.Helper()
	current := original
	handoffs := 0
	nextRound := -1
	for {
		events, err := storage.GetWorkflowInstanceHistory(t.Context(),
			current, nil)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(events), rolloverTestHistoryCap)
		for _, event := range events {
			if event.Type != history.EventType_WorkflowExecutionStarted {
				continue
			}
			attributes, ok := event.Attributes.(*history.ExecutionStartedAttributes)
			require.True(t, ok)
			require.Len(t, attributes.Inputs, 1)
			var carried workflowExecutionInput
			require.NoError(t, storage.Options().Converter.From(
				attributes.Inputs[0], &carried))
			assert.Greater(t, carried.NextRound, nextRound)
			assert.Equal(t, fixture.input.MaxRounds, carried.MaxRounds)
			assert.Equal(t, fixture.input.HistoryThreshold, carried.HistoryThreshold)
			assert.Equal(t, fixture.input.ExecutionID, carried.ExecutionID)
			nextRound = carried.NextRound
		}
		state, err := storage.GetWorkflowInstanceState(t.Context(), current)
		require.NoError(t, err)
		if state != core.WorkflowInstanceStateContinuedAsNew {
			assert.Equal(t, core.WorkflowInstanceStateFinished, state)

			break
		}

		for _, event := range events {
			if event.Type == history.EventType_WorkflowExecutionContinuedAsNew {
				attributes, ok := event.Attributes.(*history.ExecutionContinuedAsNewAttributes)
				require.True(t, ok)
				current = &workflow.Instance{
					InstanceID:  current.InstanceID,
					ExecutionID: attributes.ContinuedExecutionID,
				}
				handoffs++
			}
		}
	}

	assert.GreaterOrEqual(t, handoffs, 2)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, current.ExecutionID, execution.WorkflowExecutionID)
	assert.Equal(t, models.ExecutionStateCompleted, execution.State)
	resolved, err := db.ResolveWorkflowExecution(t.Context(), storage, original)
	require.NoError(t, err)
	assert.Equal(t, current.ExecutionID, resolved.ExecutionID)
	repo := repositories.Use(fixture.base.handle.GormDB).Execution
	_, err = repo.WithContext(t.Context()).Where(repo.ID.Eq(execution.ID)).
		UpdateColumnSimple(repo.WorkflowExecutionID.Value(original.ExecutionID))
	require.NoError(t, err)
	repaired, err := fixture.base.store.GetExecution(t.Context(),
		execution.SessionID, execution.ID)
	require.NoError(t, err)
	assert.Equal(t, current.ExecutionID, repaired.WorkflowExecutionID)
	stored, err := repo.WithContext(t.Context()).Where(repo.ID.Eq(execution.ID)).
		First()
	require.NoError(t, err)
	assert.Equal(t, current.ExecutionID, stored.WorkflowExecutionID)
	var conversation workflowConversation
	require.NoError(t, json.Unmarshal(result.Context, &conversation))
	var calls []string
	for _, message := range conversation.Messages {
		if message.ToolCallID != "" {
			calls = append(calls, message.ToolCallID)
		}
	}
	expected := make([]string, 0, rounds)
	for round := range rounds {
		expected = append(expected, fmt.Sprintf("round%d.txt", round))
	}
	assert.Equal(t, expected, calls)
	steps := make(map[string]bool)
	options := session.ListEventsOptions{Limit: session.MaximumPageLimit}
	for {
		page, err := fixture.base.store.ListExecutionActivities(t.Context(),
			execution.SessionID, execution.ID, options)
		require.NoError(t, err)
		for _, activity := range page.Items {
			assert.False(t, steps[activity.StepID], activity.StepID)
			steps[activity.StepID] = true
			assert.Equal(t, models.ActivityStateCompleted, activity.State)
		}
		if !page.HasMore {
			break
		}
		options.Offset += len(page.Items)
	}
	for round := range rounds + 1 {
		assert.True(t, steps[fmt.Sprintf("model:%d", round)])
	}
	assertRolloverTranscript(t, fixture, execution, expected)
}

func assertRolloverTranscript(t *testing.T, fixture executionLoopFixture,
	execution *models.Execution, expected []string,
) {
	t.Helper()
	var calls []string
	counts := make(map[string]int)
	if execution.Kind == models.ExecutionKindChild {
		require.NotNil(t, execution.AgentRunID)
		history, err := fixture.base.store.AgentRunHistory(t.Context(),
			execution.SessionID, *execution.AgentRunID)
		require.NoError(t, err)
		for _, message := range history.Messages {
			if message.ToolCallID != "" {
				calls = append(calls, message.ToolCallID)
			}
		}
		assert.Equal(t, runtimeToolFinalText,
			history.Messages[len(history.Messages)-1].Content)
		options := session.ListAgentRunEventsOptions{}
		for {
			page, err := fixture.base.store.ListAgentRunEvents(t.Context(),
				execution.SessionID, *execution.AgentRunID, options)
			require.NoError(t, err)
			for _, event := range page.Items {
				counts[event.EventType]++
			}
			if !page.HasMore {
				break
			}
			options.Cursor = page.NextCursor
		}
		assert.Equal(t, 1, counts[EventTypeAgentRunStarted])
		assert.Equal(t, 1, counts[EventTypeAgentRunCompleted])
		assert.Equal(t, expected, calls)
		assertRolloverAudit(t, fixture, execution, len(expected)+1)

		return
	}

	options := session.ListMessagesOptions{Order: session.PageOrderAscending}
	for {
		page, err := fixture.base.store.ListMessages(t.Context(),
			execution.SessionID, options)
		require.NoError(t, err)
		for _, message := range page.Items {
			assert.Equal(t, execution.RootTurnID, message.TurnID)
			if message.ToolCallID != "" {
				calls = append(calls, message.ToolCallID)
			}
		}
		if !page.HasMore {
			assert.Equal(t, runtimeToolFinalText,
				page.Items[len(page.Items)-1].Content)

			break
		}
		options.Offset += len(page.Items)
	}
	events := session.ListEventsOptions{}
	for {
		page, err := fixture.base.store.ListEvents(t.Context(),
			execution.SessionID, events)
		require.NoError(t, err)
		for _, event := range page.Items {
			counts[event.EventType]++
		}
		if !page.HasMore {
			break
		}
		events.Offset += len(page.Items)
	}
	assert.Equal(t, 1, counts[EventTypeTurnStarted])
	assert.Equal(t, 1, counts[EventTypeTurnCompleted])
	assert.Equal(t, expected, calls)
	assertRolloverAudit(t, fixture, execution, len(expected)+1)
}

func assertRolloverAudit(t *testing.T, fixture executionLoopFixture,
	execution *models.Execution, rounds int,
) {
	t.Helper()
	seen := make(map[int]bool)
	options := session.ListModelRunsOptions{}
	for {
		page, err := fixture.base.store.ListModelRuns(t.Context(),
			execution.SessionID, options)
		require.NoError(t, err)
		for _, run := range page.Items {
			var settings rootWorkflowModelAuditSettings
			require.NoError(t, json.Unmarshal([]byte(run.RequestSettingsJSON),
				&settings))
			assert.Equal(t, execution.ID, settings.ExecutionID)
			assert.False(t, seen[settings.WorkflowRound])
			seen[settings.WorkflowRound] = true
			calls, err := fixture.base.store.ListModelCalls(t.Context(),
				execution.SessionID, run.ID, session.ListModelCallsOptions{})
			require.NoError(t, err)
			assert.Len(t, calls.Items, 1)
		}
		if !page.HasMore {
			break
		}
		options.Offset += len(page.Items)
	}
	assert.Len(t, seen, rounds)
	for round := range rounds {
		assert.True(t, seen[round])
	}
}

func TestWorkflowRolloverShortTurnUnchanged(t *testing.T) {
	t.Parallel()
	fixture, _ := newRolloverRootFixture(t,
		[]elelemtest.Turn{elelemtest.Text(runtimeToolFinalText)})
	fixture.input.HistoryThreshold = rolloverTestThreshold
	storage, original := prepareRolloverTest(t, fixture)
	api, _ := startRolloverTestWorker(t, fixture, storage)
	_, err := api.CreateWorkflowInstance(t.Context(),
		client.WorkflowInstanceOptions{
			InstanceID: original.InstanceID,
			Queue:      workflow.Queue(fixture.input.SessionID.String()),
		},
		agentWorkflowName, fixture.input)
	require.NoError(t, err)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		api, original, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	current, err := db.ResolveWorkflowExecution(t.Context(), storage, original)
	require.NoError(t, err)
	assert.Equal(t, original.ExecutionID, current.ExecutionID)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assertRolloverTranscript(t, fixture, execution, nil)
}

func TestWorkflowRolloverRetainsRoundBudget(t *testing.T) {
	t.Parallel()
	const rounds = 3
	driver := elelemtest.NewScriptedDriver(rolloverTestTurns(t, rounds)...)
	fixture := newExecutionLoopFixture(t, driver, true)
	fixture.input.MaxRounds = rounds
	fixture.input.HistoryThreshold = 1
	storage, original := prepareRolloverTest(t, fixture)
	api, _ := startRolloverTestWorker(t, fixture, storage)
	_, err := api.CreateWorkflowInstance(t.Context(),
		client.WorkflowInstanceOptions{
			InstanceID: original.InstanceID,
			Queue:      workflow.Queue(fixture.input.SessionID.String()),
		},
		agentWorkflowName, fixture.input)
	require.NoError(t, err)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		api, original, workflowTestResultTimeout)
	require.Error(t, err)
	assert.Equal(t, rounds, driver.Calls())
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateFailed, execution.State)
	assert.NotEqual(t, original.ExecutionID, execution.WorkflowExecutionID)
}

func TestWorkflowRolloverBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		history   int64
		threshold int64
		nextRound int
		want      bool
	}{
		{name: "below", history: 69, threshold: 70, nextRound: 1},
		{name: "at", history: 70, threshold: 70, nextRound: 1, want: true},
		{name: "above", history: 71, threshold: 70, nextRound: 1, want: true},
		{name: "legacy input", history: 71, nextRound: 1},
		{name: "exhausted", history: 71, threshold: 70, nextRound: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := workflowExecutionInput{
				MaxRounds:        3,
				HistoryThreshold: tc.threshold,
			}
			assert.Equal(t, tc.want,
				workflowHistoryRolloverDue(input, tc.nextRound, tc.history))
		})
	}
}

func TestWorkflowRolloverCarriesSavedState(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	fixture.input.MaxRounds = 6
	fixture.input.HistoryThreshold = 1
	conversation := workflowConversation{
		ModelReference: runtimeTestModelReference,
		Messages: []elelem.Message{{
			Role:    elelem.RoleUser,
			Content: elelem.Text(strings.Repeat("saved context ", 128)),
		}},
		Runtime: &workflowRuntimeState{Skills: &workflowSkillState{
			CompactedThrough: 3,
			History: []workflowSkillHistory{{
				Sequence: 4,
				Names:    []string{"exampleSkill"},
			}},
			Delivered: []string{"exampleSkill"},
		}},
		Compaction: &workflowCompactionState{
			Version: workflowCompactionStateVersion,
			Plan:    reconstructionPlan{LeadCount: 1, HasSummary: true},
			Head: &compactionHead{
				ID: uuid.New(), FromMessageID: uuid.New(),
				FromSequence: 1, SourceMessageCount: 2, SummaryTokens: 3,
			},
			ModelReference: runtimeTestModelReference, Prompt: "Summarize context",
			PromptHash: hash("Summarize context"), MaxOutputTokens: 1024,
			Timeout: time.Minute,
		},
		Repeats: map[string]workflowRepeatCount{"exampleCall": {
			Hash: hash("arguments"), Count: 2,
			Results: map[string]int{hash("result"): 1},
		}},
		InboxIDs: []uuid.UUID{uuid.New()},
		Limits: &workflowExecutionLimits{
			MaxRounds:          fixture.input.MaxRounds,
			MaxConcurrentTools: fixture.input.MaxConcurrentTools,
			HistoryThreshold:   fixture.input.HistoryThreshold,
		},
	}
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	fixture.input.Context = encoded
	fixture.engine.options.ModelRound = func(_ context.Context,
		input workflowRoundInput,
	) (workflowRoundResult, error) {
		return workflowRoundResult{
			Context:  input.Execution.Context,
			Continue: input.Round < input.Execution.MaxRounds-1,
			Text:     runtimeToolFinalText,
		}, nil
	}
	storage, original := prepareRolloverTest(t, fixture)
	api, _ := startRolloverTestWorker(t, fixture, storage)
	_, err = api.CreateWorkflowInstance(t.Context(),
		client.WorkflowInstanceOptions{
			InstanceID: original.InstanceID,
			Queue:      workflow.Queue(fixture.input.SessionID.String()),
		},
		agentWorkflowName, fixture.input)
	require.NoError(t, err)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		api, original, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.JSONEq(t, string(encoded), string(result.Context))
	current, err := db.ResolveWorkflowExecution(t.Context(), storage, original)
	require.NoError(t, err)
	assert.NotEqual(t, original.ExecutionID, current.ExecutionID)
}
