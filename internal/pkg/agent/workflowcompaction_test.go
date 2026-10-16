package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cschleiden/go-workflows/client"
	"github.com/cschleiden/go-workflows/workflow"
	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCompactingWorkflowFixture(
	t *testing.T,
	driver *elelemtest.ScriptedDriver,
) executionLoopFixture {
	t.Helper()
	base := newRuntimeFixture(t, driver)
	input := newWorkflowTestInput(t, base)

	return newExecutionLoopFixtureWithInput(t, base, input, false)
}

func startCompactingWorkflow(
	t *testing.T,
	fixture executionLoopFixture,
) *workflow.Instance {
	t.Helper()

	return startNamedWorkflow(t, fixture, agentWorkflowName)
}

func startNamedWorkflow(
	t *testing.T,
	fixture executionLoopFixture,
	name string,
) *workflow.Instance {
	t.Helper()
	require.NoError(t, fixture.worker.Start(fixture.engine.options.Lifecycle))
	fixture.started.Store(true)
	instance, err := fixture.api.CreateWorkflowInstance(t.Context(),
		client.WorkflowInstanceOptions{
			InstanceID: fmt.Sprintf("%s/%s", fixture.input.SessionID,
				fixture.input.ExecutionID),
			Queue: workflow.Queue(fixture.input.SessionID.String()),
		}, name, fixture.input)
	require.NoError(t, err)

	return instance
}

func TestWorkflowCompactionRetryKeepsActivityIdentity(t *testing.T) {
	t.Parallel()
	fixture := newCompactingWorkflowFixture(t, elelemtest.NewScriptedDriver())
	var calls atomic.Int32
	fixture.engine.options.CompactRound = func(
		_ context.Context, input workflowRoundInput,
	) (workflowCompactionResult, error) {
		if calls.Add(1) == 1 {
			return workflowCompactionResult{}, commerr.ErrFetchFailed
		}

		return workflowCompactionResult{Context: input.Execution.Context}, nil
	}
	input := workflowRoundInput{Execution: fixture.input}
	_, err := fixture.engine.compactRound(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrFetchFailed)
	result, err := fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionExecute, result.Decision)
	reused, err := fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.EqualValues(t, 2, calls.Load())
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.EqualValues(t, 2, page.Items[0].Attempt)
	assert.Equal(t, models.ActivityStateCompleted, page.Items[0].State)
}

func TestWorkflowCompactionModelRetryUsesSavedContext(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.Turn{Err: commerr.ErrFetchFailed}, elelemtest.Text("done"),
	)
	fixture := newCompactingWorkflowFixture(t, driver)
	var calls atomic.Int32
	fixture.engine.options.CompactRound = func(
		_ context.Context, input workflowRoundInput,
	) (workflowCompactionResult, error) {
		calls.Add(1)

		return workflowCompactionResult{Context: input.Execution.Context}, nil
	}
	instance := startCompactingWorkflow(t, fixture)
	_, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.EqualValues(t, 1, calls.Load())
	requests := driver.Requests()
	require.Len(t, requests, 2)
	assert.Equal(t, requests[0].Messages, requests[1].Messages)
	fixture.stop()
}

func TestWorkflowCompactionRejectsInvalidRound(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		round int
	}{
		{name: "negative", round: -1},
		{name: "exhausted", round: 4},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newCompactingWorkflowFixture(t,
				elelemtest.NewScriptedDriver())
			_, err := fixture.engine.compactRound(t.Context(),
				workflowRoundInput{Execution: fixture.input, Round: tc.round})
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
			page, err := fixture.base.store.ListExecutionActivities(t.Context(),
				fixture.input.SessionID, fixture.input.ExecutionID,
				session.ListEventsOptions{})
			require.NoError(t, err)
			assert.Empty(t, page.Items)
		})
	}
}

func TestWorkflowSchedulesSeparateCompactionBeforeModel(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("completed answer"))
	fixture := newCompactingWorkflowFixture(t, driver)
	instance := startCompactingWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "completed answer", result.Text)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 5)
	assert.Equal(t, models.ActivityKindPreparation, page.Items[0].Kind)
	assert.Equal(t, models.ActivityKindCompaction, page.Items[1].Kind)
	assert.Equal(t, models.ActivityKindModel, page.Items[2].Kind)
	assert.Equal(t, terminalHooksActivityName, page.Items[3].Name)
	assert.Equal(t, session.ExecutionFinishActivityName, page.Items[4].Name)
	fixture.stop()
}

func TestWorkflowCompactionCommitsSummaryAndFeedsModel(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("completed answer"))
	base := newRuntimeFixture(t, driver)
	lease, err := base.store.AcquireTurn(t.Context(), base.runtime.sessionID,
		session.StartTurnInput{RequestID: uuid.New(), Workspace: base.workspace})
	require.NoError(t, err)
	messageID := uuid.New()
	require.NoError(t, base.store.FinalizeTurn(t.Context(), lease,
		session.FinalizeTurnInput{
			State: models.TurnStateCompleted,
			Messages: []session.MessageInput{{
				ID:   messageID,
				Role: models.MessageRoleUser, Content: "completed history",
			}},
		}))
	input := newWorkflowTestInput(t, base)
	fixture := newExecutionLoopFixtureWithInput(t, base, input, false)
	var calls atomic.Int32
	fixture.engine.options.CompactRound = func(
		_ context.Context, input workflowRoundInput,
	) (workflowCompactionResult, error) {
		calls.Add(1)
		var conversation workflowConversation
		if err := json.Unmarshal(input.Execution.Context, &conversation); err != nil {
			return workflowCompactionResult{}, ctxerrors.Wrap(err,
				"decode compaction fixture context")
		}
		conversation.Messages = []elelem.Message{{
			Role:    elelem.RoleUser,
			Content: elelem.Text("retained summary"),
		}}
		encoded, err := json.Marshal(conversation)
		if err != nil {
			return workflowCompactionResult{}, ctxerrors.Wrap(err,
				"encode compaction fixture context")
		}

		return workflowCompactionResult{
			Context: encoded,
			Compaction: &session.CompactionInput{
				FromMessageID: messageID, ToMessageID: messageID,
				FromSequence: 1, ToSequence: 1, SourceMessageCount: 1,
				Summary: "retained summary", ModelID: "example/model",
				PromptHash: "example-prompt",
			},
		}, nil
	}
	instance := startCompactingWorkflow(t, fixture)
	_, err = client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.EqualValues(t, 1, calls.Load())
	requests := driver.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, "retained summary", requests[0].Messages[1].Text())
	head, err := base.store.LatestCompaction(t.Context(), input.SessionID)
	require.NoError(t, err)
	require.NotNil(t, head)
	assert.Equal(t, "retained summary", head.Summary)
	fixture.stop()
}

func TestWorkflowCompactionReusesCheckpointAfterReopen(t *testing.T) {
	t.Parallel()
	fixture := newCompactingWorkflowFixture(t, elelemtest.NewScriptedDriver())
	input := workflowRoundInput{Execution: fixture.input}
	saved, err := fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	require.NoError(t, fixture.base.handle.Close())
	handle, err := db.Open(t.Context(), db.Config{
		Directory: fixture.base.stateDirectory,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	store, err := session.NewStore(handle, session.Options{})
	require.NoError(t, err)
	fixture.engine.options.Store = store
	fixture.engine.options.GenerationID = uuid.New()
	fixture.engine.options.CompactRound = func(
		context.Context, workflowRoundInput,
	) (workflowCompactionResult, error) {
		t.Error("saved compaction callback ran again")

		return workflowCompactionResult{}, commerr.ErrConflict
	}
	_, err = store.PauseExecution(t.Context(), input.Execution.SessionID,
		input.Execution.ExecutionID, "leave completed compaction paused")
	require.NoError(t, err)
	reused, err := fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, reused.Decision)
	_, err = store.ResumeExecution(t.Context(), input.Execution.SessionID,
		input.Execution.ExecutionID)
	require.NoError(t, err)
	reused, err = fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.JSONEq(t, string(saved.Output.Context), string(reused.Output.Context))
}

func TestWorkflowCompactionRejectsInvalidContext(t *testing.T) {
	t.Parallel()
	fixture := newCompactingWorkflowFixture(t, elelemtest.NewScriptedDriver())
	fixture.engine.options.CompactRound = func(
		context.Context, workflowRoundInput,
	) (workflowCompactionResult, error) {
		return workflowCompactionResult{Context: json.RawMessage(`[]`)}, nil
	}
	_, err := fixture.engine.compactRound(t.Context(),
		workflowRoundInput{Execution: fixture.input})
	require.ErrorIs(t, err, commerr.ErrValidationFailed)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.ActivityStateFailed, page.Items[0].State)
}

func TestWorkflowCompactionPauseCancelsAndRequiresResume(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver()
	fixture := newCompactingWorkflowFixture(t, driver)
	var calls atomic.Int32
	fixture.engine.options.CompactRound = func(
		ctx context.Context, input workflowRoundInput,
	) (workflowCompactionResult, error) {
		if calls.Add(1) == 1 {
			if err := fixture.engine.pause(ctx, input.Execution.SessionID,
				input.Execution.ExecutionID, "stop current compaction"); err != nil {
				return workflowCompactionResult{}, ctxerrors.Wrap(err,
					"pause fixture compaction")
			}

			return workflowCompactionResult{}, ctx.Err()
		}

		return workflowCompactionResult{Context: input.Execution.Context}, nil
	}
	input := workflowRoundInput{Execution: fixture.input}
	paused, err := fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, paused.Decision)
	paused, err = fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, paused.Decision)
	assert.EqualValues(t, 1, calls.Load())
	assert.Zero(t, driver.Calls())
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	continued, err := fixture.engine.compactRound(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionExecute, continued.Decision)
	assert.EqualValues(t, 2, calls.Load())
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.EqualValues(t, 2, page.Items[0].Attempt)
	assert.Equal(t, models.ActivityStateCompleted, page.Items[0].State)
}

func TestCompactingWorkflowRunsPhaseOnEachToolRound(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(
		elelemtest.ToolCall(runtimeToolCallID, toolNameReadFile,
			runtimeToolArguments(t, runtimeToolFileName)),
		elelemtest.Text(runtimeToolFinalText),
	)
	fixture := newCompactingWorkflowFixture(t, driver)
	writeRuntimeFile(t, filepath.Join(fixture.base.workspace,
		runtimeToolFileName), runtimeToolFileBody)
	var calls atomic.Int32
	fixture.engine.options.CompactRound = func(
		_ context.Context, input workflowRoundInput,
	) (workflowCompactionResult, error) {
		calls.Add(1)

		return workflowCompactionResult{Context: input.Execution.Context}, nil
	}
	instance := startCompactingWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	assert.EqualValues(t, 2, calls.Load())
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListEventsOptions{})
	require.NoError(t, err)
	steps := make([]string, 0, len(page.Items))
	for _, activity := range page.Items {
		steps = append(steps, activity.StepID)
	}
	assert.Equal(t, []string{
		"prepare:0", "compact:0", "model:0",
		"tool:0:0:preHook", "tool:0:0:handler", "tool:0:0:outcomeHook",
		"tool:0:0:postHook", "tool:0:0:project",
		"prepare:1", "compact:1", "model:1", "terminalHooks:1", "finish:1",
	}, steps)
	fixture.stop()
}

func TestCompactingWorkflowFinalInboxContinues(t *testing.T) {
	t.Parallel()
	driver := elelemtest.NewScriptedDriver(elelemtest.Text("first answer"),
		elelemtest.Text("continued answer"))
	fixture, prepared := newRootModelWorkflowFixture(t, driver)
	var once sync.Once
	fixture.engine.options.Store = &finishAcceptanceStore{
		ExecutionStorage: fixture.base.store,
		beforeFinish: func(ctx context.Context) error {
			var err error
			once.Do(func() {
				err = prepared.userMessages.enqueue(ctx, TurnRequest{
					RequestID: uuid.New(), Message: "accepted after model checkpoint",
				})
			})

			return err
		},
	}
	instance := startCompactingWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, "continued answer", result.Text)
	assert.Equal(t, 2, driver.Calls())
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateDelivered, page.Items[0].State)
	fixture.stop()
}
