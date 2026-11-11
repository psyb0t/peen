package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryAdmissionQueuesBeforeWorkflowWorkerStarts(t *testing.T) {
	t.Parallel()
	fixture, prepared, dispatch := newRootSubmissionFixture(t)
	input, err := dispatch.prepareRootSubmission(t.Context(), prepared)
	require.NoError(t, err)
	fixture.stop()
	fixture.base.runtime.closeActiveUserMessageQueue(prepared)
	_, err = fixture.base.store.RecoverInterrupted(t.Context())
	require.NoError(t, err)
	runtime := fixture.base.runtime
	driver := newGatedDriver(
		elelemtest.Text("recovered result"),
		elelemtest.Text("first later result"),
		elelemtest.Text("second later result"))
	setRecoveryDriver(t, fixture.base, prepared, driver)
	runtime.resumeAfterCrash = true
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	remote, err := fixture.storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	worker, err := NewWorkflowWorker(ctx, runtime, remote, uuid.New(),
		runtime.sessionID, nil)
	require.NoError(t, err)
	requestID := uuid.New()
	result, handled, err := runtime.queueActiveUserMessage(ctx, TurnRequest{
		RequestID: requestID, Message: "work after recovery",
	})
	require.NoError(t, err)
	require.True(t, handled, "startup must register the resumed root queue")
	require.NotNil(t, result)
	require.True(t, result.Queued)
	page, err := fixture.base.store.ListExecutionMessages(ctx,
		input.SessionID, input.ExecutionID, session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateDeferred, page.Items[0].State)
	require.NoError(t, worker.Start(ctx))
	t.Cleanup(func() {
		cancel()
		require.NoError(t, worker.WaitForCompletion())
	})
	require.Eventually(t, func() bool {
		activities, readErr := fixture.base.store.ListExecutionActivities(ctx,
			input.SessionID, input.ExecutionID, session.ListEventsOptions{})
		if readErr != nil {
			return false
		}
		for _, activity := range activities.Items {
			if activity.Kind == models.ActivityKindModel &&
				activity.State == models.ActivityStateRunning {
				return true
			}
		}

		return false
	}, workflowTestResultTimeout, 10*time.Millisecond)
	secondID := uuid.New()
	result, err = runtime.Run(ctx, TurnRequest{
		RequestID: secondID, Message: "work while recovery is running",
	})
	require.NoError(t, err)
	require.True(t, result.Queued)
	close(driver.gate)
	_, err = client.GetWorkflowResult[workflowRoundResult](ctx,
		runtime.nativeWorkflowRunner().api,
		nativeWorkflowInstance(input.SessionID, input.ExecutionID),
		workflowTestResultTimeout)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, id := range []uuid.UUID{requestID, secondID} {
			row, readErr := fixture.base.store.GetExecutionMessageByRequest(ctx,
				input.SessionID, input.ExecutionID, id)
			if readErr != nil || row.ForwardedExecutionID == nil {
				return false
			}
			execution, readErr := fixture.base.store.GetExecution(ctx,
				input.SessionID, *row.ForwardedExecutionID)
			if readErr != nil ||
				execution.State != models.ExecutionStateCompleted {
				return false
			}
		}

		return true
	}, workflowTestResultTimeout, 10*time.Millisecond)
}

func setRecoveryDriver(
	t *testing.T,
	base runtimeFixture,
	prepared *preparedTurn,
	driver elelem.Driver,
) {
	t.Helper()
	model, err := base.runtime.models.ResolveModel(runtimeTestModelReference)
	require.NoError(t, err)
	model.Client = elelem.New(driver)
	registry, err := NewStaticRegistry(map[string]ModelClient{
		runtimeTestModelReference: model,
	})
	require.NoError(t, err)
	base.runtime.models = registry
	prepared.model = model
}

func TestRecoveryAdmissionRetainsPreCrashMessageDeliveryMode(t *testing.T) {
	t.Parallel()
	fixture, prepared := newRootModelWorkflowFixture(t,
		elelemtest.NewScriptedDriver())
	input := TurnRequest{
		RequestID: uuid.New(), Message: "accepted before crash",
	}
	require.NoError(t, prepared.userMessages.enqueue(t.Context(), input))
	fixture.base.store.ReleaseTurn(prepared.lease)
	restored, _, err := fixture.base.runtime.restoreRootWorkflowTurn(
		t.Context(), fixture.input, session.RestoreExecutionTurnInput{
			OwnerGenerationID: uuid.New(),
		}, nil)
	require.NoError(t, err)
	restored.userMessages.deferUntilCompletion = true
	require.NoError(t, restored.userMessages.enqueue(t.Context(), input))
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.InboxMessageStateQueued, page.Items[0].State)
}

func TestRecoveryAdmissionPauseRequiresInspectionAndExplicitRetry(
	t *testing.T,
) {
	t.Parallel()
	fixture, prepared, dispatch := newRootSubmissionFixture(t)
	effectPath := filepath.Join(fixture.base.workspace, "effect.txt")
	attemptsPath := effectPath + ".attempts"
	const command = `printf 'attempt\n' >> "$ATTEMPTS_PATH"; ` +
		`printf 'effect\n' > "$EFFECT_PATH"`
	arguments, err := json.Marshal(map[string]any{
		"command": command,
		"purpose": "write an idempotent effect",
		"environment": map[string]string{
			"EFFECT_PATH": effectPath, "ATTEMPTS_PATH": attemptsPath,
		},
	})
	require.NoError(t, err)
	initial := elelemtest.NewScriptedDriver(elelemtest.ToolCall(
		"crashedEffect", toolNameRunCommand, string(arguments)))
	driver := &inspectionControlDriver{ScriptedDriver: initial}
	setRecoveryDriver(t, fixture.base, prepared, driver)
	started := holdRecoveryEffectAcknowledgement(fixture.engine)
	submission, err := dispatch.submitRoot(t.Context(),
		newWorkflowClient(fixture.storage), prepared)
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("effect did not start")
	}
	fixture.stop()
	fixture.base.runtime.closeActiveUserMessageQueue(prepared)
	_, err = fixture.base.store.RecoverInterrupted(t.Context())
	require.NoError(t, err)
	_, err = fixture.base.store.PauseUnfinishedExecutions(t.Context(),
		fixture.base.runtime.sessionID)
	require.NoError(t, err)
	original, err := fixture.base.store.GetExecution(t.Context(),
		submission.Input.SessionID, submission.Input.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, models.ExecutionStatePaused, original.State)
	unknown := recoveryUncertainActivity(t, fixture.base, original)
	content, err := os.ReadFile(attemptsPath)
	require.NoError(t, err)
	assert.Equal(t, "attempt\n", string(content))
	continuePausedEffect(t, fixture, original, unknown, driver, effectPath)
	content, err = os.ReadFile(attemptsPath)
	require.NoError(t, err)
	assert.Equal(t, "attempt\nattempt\n", string(content))
}

// Hold after the command's effect, before its checkpoint acknowledges it.
func holdRecoveryEffectAcknowledgement(
	engine *workflowExecution,
) <-chan struct{} {
	started := make(chan struct{})
	original := engine.options.ToolPhase
	engine.options.ToolPhase = func(ctx context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		state, err := original(ctx, input)
		if err != nil {
			return state, ctxerrors.Wrap(err, "execute recovery fixture phase")
		}
		if input.Phase != workflowToolPhaseHandler {
			return state, nil
		}

		close(started)
		<-ctx.Done()

		return state, ctxerrors.Wrap(ctx.Err(), "lose effect acknowledgement")
	}

	return started
}

func recoveryUncertainActivity(
	t *testing.T,
	base runtimeFixture,
	execution *models.Execution,
) *models.ExecutionActivity {
	t.Helper()
	page, err := base.store.ListExecutionActivities(t.Context(),
		execution.SessionID, execution.ID, session.ListEventsOptions{})
	require.NoError(t, err)
	for _, activity := range page.Items {
		if activity.State == models.ActivityStateUncertain {
			return activity
		}
	}
	t.Fatal("crashed effect must remain uncertain in pause mode")

	return nil
}

func continuePausedEffect(
	t *testing.T,
	fixture executionLoopFixture,
	original *models.Execution,
	unknown *models.ExecutionActivity,
	driver *inspectionControlDriver,
	effectPath string,
) {
	t.Helper()
	resolve, err := json.Marshal(resolveExecutionActivityInput{
		ActivityID: unknown.ID, ControlRevision: original.ControlRevision,
		ResolveActivityInput: session.ResolveActivityInput{
			Attempt: unknown.Attempt, Resolution: session.ActivityResolutionRetry,
			Evidence: "read the effect file; repeating the write is safe",
		},
	})
	require.NoError(t, err)
	resume, err := json.Marshal(resumeExecutionInput{
		ExecutionID: original.ID, ControlRevision: original.ControlRevision,
	})
	require.NoError(t, err)
	read, err := json.Marshal(executionReadInput{
		ExecutionID: original.ID, Part: executionReadPartActivities,
	})
	require.NoError(t, err)
	file, err := json.Marshal(readFileInputForRecovery{Path: effectPath})
	require.NoError(t, err)
	driver.ScriptedDriver = elelemtest.NewScriptedDriver(
		elelemtest.ToolCall("inspectFile", toolNameReadFile, string(file)),
		elelemtest.ToolCall("inspectWork", toolNameReadExecution, string(read)),
		elelemtest.ToolCall("retryEffect", toolNameResolveExecutionActivity,
			string(resolve)),
		elelemtest.ToolCall("resumeEffect", toolNameResumeExecution,
			string(resume)),
		elelemtest.Text("inspection completed"),
		elelemtest.Text("effect retried"))
	ctx, cancel := context.WithCancel(t.Context())
	remote, err := fixture.storage.WorkerBackend(ctx, uuid.New())
	require.NoError(t, err)
	worker, err := NewWorkflowWorker(ctx, fixture.base.runtime, remote,
		uuid.New(), original.SessionID, nil)
	require.NoError(t, err)
	require.False(t, fixture.base.store.IsActive(original.SessionID))
	require.NoError(t, worker.Start(ctx))
	t.Cleanup(func() {
		cancel()
		require.NoError(t, worker.WaitForCompletion())
	})
	result, err := fixture.base.runtime.Run(ctx,
		TurnRequest{Message: "inspect and retry the paused effect"})
	require.NoError(t, err)
	require.False(t, result.Queued)
	require.Eventually(t, func() bool {
		stored, readErr := fixture.base.store.GetExecution(ctx,
			original.SessionID, original.ID)

		return readErr == nil && stored.State == models.ExecutionStateCompleted
	}, workflowTestResultTimeout, 10*time.Millisecond)
}

type readFileInputForRecovery struct {
	Path string `json:"path"`
}
