package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type checkpointResponseStore struct {
	session.ExecutionStorage
	change func(*models.ExecutionActivity) *models.ExecutionActivity
}

func TestRootAdmissionWaitRetainsUnstartedActivity(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		policy models.ActivityReplayPolicy
	}{
		{name: "retry", policy: models.ActivityReplayPolicyRetry},
		{name: "reconcile", policy: models.ActivityReplayPolicyReconcile},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
			busy, effects := true, 0
			descriptor := session.ScheduleActivityInput{
				StepID: "rootAdmissionProbe", Name: "rootAdmissionProbe", Kind: models.ActivityKindTool,
				ReplayPolicy: tc.policy,
			}
			invoke := func() (workflowActivityResult[json.RawMessage], error) {
				return runWorkflowCheckpoint(t.Context(), fixture.engine, fixture.input, descriptor,
					json.RawMessage(`{}`), func(context.Context) (json.RawMessage, error) {
						if busy {
							return nil, ctxerrors.Wrap(errRootAdmissionBusy, "restore root callback turn")
						}
						effects++

						return json.RawMessage(`{"admitted":true}`), nil
					}, func(json.RawMessage) json.RawMessage { return fixture.input.Context })
			}
			waiting, err := invoke()
			require.NoError(t, err)
			assert.Equal(t, session.ActivityDecisionPaused, waiting.Decision)
			assert.Zero(t, effects)
			stored, err := fixture.base.store.GetExecution(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID)
			require.NoError(t, err)
			assert.Equal(t, models.ExecutionStateRunning, stored.State)
			assert.Zero(t, stored.ControlRevision)
			if tc.policy == models.ActivityReplayPolicyReconcile {
				before, err := fixture.base.store.ListExecutionActivities(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, session.ListEventsOptions{})
				require.NoError(t, err)
				require.Len(t, before.Items, 1)
				assert.Contains(t, before.Items[0].ResolutionJSON, workflowRootAdmissionEvidence)
			}
			busy = false
			completed, err := invoke()
			require.NoError(t, err)
			assert.Equal(t, session.ActivityDecisionExecute, completed.Decision)
			repeated, err := invoke()
			require.NoError(t, err)
			assert.Equal(t, session.ActivityDecisionReuse, repeated.Decision)
			assert.Equal(t, completed.Output, repeated.Output)
			assert.Equal(t, 1, effects)
			activities, err := fixture.base.store.ListExecutionActivities(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, session.ListEventsOptions{})
			require.NoError(t, err)
			require.Len(t, activities.Items, 1)
			assert.EqualValues(t, 2, activities.Items[0].Attempt)
			if tc.policy == models.ActivityReplayPolicyReconcile {
				events, err := fixture.base.store.ListExecutionEvents(t.Context(), fixture.input.SessionID, fixture.input.ExecutionID, session.ListEventsOptions{})
				require.NoError(t, err)
				resolved := 0
				for _, event := range events.Items {
					if event.EventType != string(session.ExecutionEventTypeStepResolved) {
						continue
					}
					var resolution session.ResolveActivityInput
					require.NoError(t, json.Unmarshal([]byte(event.PayloadJSON), &resolution))
					assert.Equal(t, workflowRootAdmissionEvidence, resolution.Evidence)
					assert.Equal(t, session.ActivityResolutionRetry, resolution.Resolution)
					assert.EqualValues(t, 1, resolution.Attempt)
					resolved++
				}
				assert.Equal(t, 1, resolved)
			}
		})
	}
}

func TestWorkflowCapacityRefusalPreservesUnstartedEffect(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		cleanupErr error
	}{
		{name: "clean pause"},
		{name: "cleanup failure", cleanupErr: commerr.ErrFetchFailed},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertCapacityRefusalPreservesEffect(t, tc.cleanupErr)
		})
	}
}

func assertCapacityRefusalPreservesEffect(t *testing.T, cleanupErr error) {
	t.Helper()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), false)
	fixture.engine.options.AfterPause = func(context.Context, uuid.UUID,
		[]uuid.UUID,
	) error {
		return cleanupErr
	}
	available, effects := false, 0
	descriptor := session.ScheduleActivityInput{
		StepID: "capacityProbe:0", Name: "capacityProbe",
		Kind:         models.ActivityKindTool,
		ReplayPolicy: models.ActivityReplayPolicyReconcile,
	}
	run := func(context.Context) (json.RawMessage, error) {
		if !available {
			return nil, ctxerrors.Wrap(ErrTooManyAgentRuns,
				"actor admission refused before test effects")
		}
		effects++

		return json.RawMessage(`{"completed":true}`), nil
	}
	invoke := func() workflowActivityResult[json.RawMessage] {
		result, err := runWorkflowCheckpoint(t.Context(), fixture.engine,
			fixture.input, descriptor, json.RawMessage(`{}`), run,
			func(json.RawMessage) json.RawMessage { return fixture.input.Context })
		require.NoError(t, err)

		return result
	}
	assert.Equal(t, session.ActivityDecisionPaused, invoke().Decision)
	activities, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID, session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, activities.Items, 1)
	before := activities.Items[0]
	assert.Equal(t, models.ActivityStateScheduled, before.State)
	assert.Contains(t, before.ResolutionJSON, workflowCapacityEvidence)
	available = true
	assert.Equal(t, session.ActivityDecisionPaused, invoke().Decision)
	assert.Zero(t, effects)
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionExecute, invoke().Decision)
	assert.Equal(t, session.ActivityDecisionReuse, invoke().Decision)
	assert.Equal(t, 1, effects)
	after, err := fixture.base.store.GetExecutionActivity(t.Context(),
		fixture.input.SessionID, before.ID)
	require.NoError(t, err)
	assert.Equal(t, before.ID, after.ID)
	assert.Equal(t, before.InputJSON, after.InputJSON)
	assert.Equal(t, before.Attempt+1, after.Attempt)
}

func (s *checkpointResponseStore) CompleteExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
	input session.CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	activity, err := s.ExecutionStorage.CompleteExecutionActivity(ctx,
		sessionID, lease, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "complete receipt fault fixture")
	}

	return s.change(activity), nil
}

func TestWorkflowMissingCommitReceiptReturnsCheckedError(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*models.ExecutionActivity) *models.ExecutionActivity
	}{
		{name: "missing record", change: func(*models.ExecutionActivity) *models.ExecutionActivity {
			return nil
		}},
		{name: "missing output", change: func(row *models.ExecutionActivity) *models.ExecutionActivity {
			row.OutputJSON = nil

			return row
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := elelemtest.NewScriptedDriver(elelemtest.Text("saved"))
			fixture := newExecutionLoopFixture(t, driver, true)
			fixture.engine.options.Store = &checkpointResponseStore{
				ExecutionStorage: fixture.base.store, change: tc.change,
			}
			input := workflowRoundInput{Execution: fixture.input}
			_, err := fixture.engine.modelRound(t.Context(), input)
			require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
			fixture.engine.options.Store = fixture.base.store
			reused, err := fixture.engine.modelRound(t.Context(), input)
			require.NoError(t, err)
			assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
			assert.Equal(t, 1, driver.Calls())
		})
	}
}

func TestWorkflowMalformedCommitReceiptReusesStoredResult(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("saved"))
	fixture := newExecutionLoopFixture(t, driver, true)
	fixture.engine.options.Store = &checkpointResponseStore{
		ExecutionStorage: fixture.base.store,
		change: func(row *models.ExecutionActivity) *models.ExecutionActivity {
			malformed := "{"
			row.OutputJSON = &malformed

			return row
		},
	}
	input := workflowRoundInput{Execution: fixture.input}
	_, err := fixture.engine.modelRound(t.Context(), input)
	var syntax *json.SyntaxError
	require.ErrorAs(t, err, &syntax)
	fixture.engine.options.Store = fixture.base.store
	reused, err := fixture.engine.modelRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, 1, driver.Calls())
}

func TestExecutionWorkflowSavedPhasesSurviveReopen(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)),
		elelemtest.Text(runtimeToolFinalText),
	)
	fixture := newExecutionLoopFixture(t, driver, true)
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace,
		runtimeToolFileName), runtimeToolFileBody)
	instance := fixture.start(t)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	fixture.stop()
	require.NoError(t, fixture.storage.Close())
	require.NoError(t, fixture.base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	storage, err := db.OpenWorkflowStorage(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	fixture.engine.options.Store = store
	fixture.engine.options.GenerationID = uuid.New()
	fixture.engine.options.Lifecycle = t.Context()
	round := workflowRoundInput{Execution: fixture.input}
	reused, err := fixture.engine.modelRound(t.Context(), round)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	activities, err := store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	found := false
	for _, activity := range activities.Items {
		if activity.StepID != "tool:0:0:handler" {
			continue
		}
		found = true
		var input workflowToolPhaseInput
		require.NoError(t, json.Unmarshal([]byte(activity.InputJSON), &input))
		tool, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, session.ActivityDecisionReuse, tool.Decision)
		assert.Contains(t, tool.Output.Outcome.Result.Content, "alpha")
	}
	require.True(t, found)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		client.New(storage), instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	assert.Equal(t, 2, driver.Calls())
}

func TestWorkflowActivityClaimAnswersPausedAfterAPauseCancelsIt(
	t *testing.T,
) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text(runtimeToolFinalText))
	fixture := newExecutionLoopFixture(t, driver, true)
	require.NoError(t, fixture.engine.pause(t.Context(), fixture.input.SessionID,
		fixture.input.ExecutionID, "retain work"))
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := fixture.engine.modelRound(cancelled,
		workflowRoundInput{Execution: fixture.input})
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, result.Decision)
	assert.Equal(t, 0, driver.Calls())
}

func TestExecutionWorkflowContinuesAfterAToolPhaseFails(t *testing.T) {
	t.Parallel()
	arguments := runtimeToolArguments(t, runtimeToolFileName)
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Turn{Deltas: []elelem.Delta{
			{ToolCall: &elelem.ToolCallDelta{
				Index: 0, ID: "first", Name: toolNameReadFile,
				Arguments: arguments,
			}},
			{
				ToolCall: &elelem.ToolCallDelta{
					Index: 1, ID: "second", Name: toolNameReadFile,
					Arguments: arguments,
				},
				FinishReason: elelem.FinishReasonToolCalls,
			},
		}},
		elelemtest.Text(runtimeToolFinalText),
	)
	fixture := newExecutionLoopFixture(t, driver, true)
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace,
		runtimeToolFileName), runtimeToolFileBody)
	runPhase := fixture.engine.options.ToolPhase
	fixture.engine.options.ToolPhase = func(ctx context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		if input.Phase == workflowToolPhasePre && input.Tool.Index == 0 {
			return input.State, ctxerrors.Wrap(commerr.ErrConflict,
				"fixture cannot restore the tool phase")
		}

		return runPhase(ctx, input)
	}
	instance := fixture.start(t)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	requests := driver.Requests()
	require.Len(t, requests, 2)
	results := map[string]elelem.Message{}
	for _, message := range requests[1].Messages {
		if message.Role == elelem.RoleTool {
			results[message.ToolCallID] = message
		}
	}
	require.Len(t, results, 2)
	assert.Equal(t, failedToolResult, results["first"].Text())
	assert.Contains(t, results["second"].Text(), "alpha")
	assert.True(t, results["first"].ToolResultIsError)
	assert.False(t, results["second"].ToolResultIsError)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateCompleted, execution.State)
}

func TestExecutionWorkflowToolPanicRequiresReconciliation(t *testing.T) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	finished := false
	tool := elelem.Tool{
		Name: toolNameReadFile,
		Handler: func(context.Context, elelem.ToolInput) (elelem.ToolResult, error) {
			panic("example extension panic")
		},
	}
	fixture.engine.options.ToolCall = func(
		ctx context.Context, input workflowToolInput,
	) (workflowToolResult, error) {
		return runWorkflowTool(ctx, elelem.NewToolSet(tool), input,
			workflowToolOptions{Finish: func(string) { finished = true }})
	}
	result, err := fixture.engine.toolCall(t.Context(), workflowToolInput{
		Round: workflowRoundInput{Execution: fixture.input},
		Call: elelem.ToolCall{
			ID: runtimeToolCallID, Name: toolNameReadFile,
			Arguments: json.RawMessage(`{}`),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReconcile, result.Decision)
	assert.True(t, finished)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	assert.Equal(t, models.ExecutionStateReconciling, execution.State)
}

func TestExecutionWorkflowInterruptedToolKeepsItsDecision(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		pause    bool
		decision session.ActivityDecision
		state    models.ExecutionState
	}{
		{
			name: "interrupted", decision: session.ActivityDecisionReconcile,
			state: models.ExecutionStateReconciling,
		},
		{
			name: "paused", pause: true, decision: session.ActivityDecisionPaused,
			state: models.ExecutionStatePaused,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newExecutionLoopFixture(t,
				elelemtest.NewScriptedDriver(), true)
			finished := 0
			hook := func(context.Context, *elelem.ToolEvent) error {
				t.Error("hook started after interrupted handler")

				return nil
			}
			tool := elelem.Tool{
				Name: toolNameReadFile, OnSuccess: hook, OnError: hook, PostRun: hook,
				Handler: func(ctx context.Context, _ elelem.ToolInput) (
					elelem.ToolResult, error,
				) {
					if tc.pause {
						require.NoError(t, fixture.engine.pause(ctx,
							fixture.input.SessionID, fixture.input.ExecutionID,
							"retain work"))
					}

					return elelem.ToolResult{}, context.Canceled
				},
			}
			fixture.engine.options.ToolCall = func(
				ctx context.Context, input workflowToolInput,
			) (workflowToolResult, error) {
				return runWorkflowTool(ctx, elelem.NewToolSet(tool), input,
					workflowToolOptions{Finish: func(string) { finished++ }})
			}
			result, err := fixture.engine.toolCall(t.Context(), workflowToolInput{
				Round: workflowRoundInput{Execution: fixture.input},
				Call: elelem.ToolCall{
					ID: runtimeToolCallID, Name: toolNameReadFile,
					Arguments: json.RawMessage(workflowEmptyArguments),
				},
			})
			require.NoError(t, err)
			assert.Equal(t, tc.decision, result.Decision)
			assert.Equal(t, 1, finished)
			execution, err := fixture.base.store.GetExecution(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID)
			require.NoError(t, err)
			assert.Equal(t, tc.state, execution.State)
		})
	}
}

func TestExecutionWorkflowUnencodableToolOutputRequiresReconciliation(
	t *testing.T,
) {
	t.Parallel()
	fixture := newExecutionLoopFixture(t, elelemtest.NewScriptedDriver(), true)
	fixture.engine.options.ToolCall = func(context.Context, workflowToolInput) (
		workflowToolResult, error,
	) {
		return workflowToolResult{Result: elelem.ToolResult{
			Content:  "effect completed",
			Metadata: map[string]any{"example": make(chan struct{})},
		}}, nil
	}
	result, err := fixture.engine.toolCall(t.Context(), workflowToolInput{
		Round: workflowRoundInput{Execution: fixture.input},
		Call: elelem.ToolCall{
			ID: runtimeToolCallID, Name: toolNameReadFile,
			Arguments: json.RawMessage(`{}`),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReconcile, result.Decision)
}
