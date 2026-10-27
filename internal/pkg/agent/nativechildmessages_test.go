package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type heldChildInboxRead struct {
	*session.Store
	executionID   uuid.UUID
	read, release chan struct{}
	once          sync.Once
}

func (s *heldChildInboxRead) ListExecutionMessages(
	ctx context.Context,
	sessionID, executionID uuid.UUID,
	options session.ListExecutionInboxOptions,
) (*session.ExecutionInboxPage, error) {
	page, err := s.Store.ListExecutionMessages(ctx, sessionID, executionID,
		options)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read child inbox before hydration")
	}

	if executionID == s.executionID {
		s.once.Do(func() {
			close(s.read)
			select {
			case <-s.release:
			case <-ctx.Done():
			}
		})
	}

	if err := ctx.Err(); err != nil {
		return nil, ctxerrors.Wrap(err, "release held child inbox read")
	}

	return page, nil
}

func runChildIngressTask(t *testing.T, operation func() error) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				result <- ctxerrors.Wrap(commerr.ErrInvalidState,
					"child ingress test operation panicked")
			}
		}()
		result <- operation()
	}()

	return result
}

func coldChildMessageFixture(t *testing.T) (
	executionLoopFixture, workflowRoundInput, uuid.UUID,
) {
	t.Helper()
	fixture, deps, launch, run, prepared := initialChildConversationFixture(t)
	runtime := fixture.base.runtime
	conversation, err := runtime.childWorkflowConversation(t.Context(), deps,
		launch, run, prepared)
	require.NoError(t, err)
	encoded, err := json.Marshal(conversation)
	require.NoError(t, err)
	execution, err := fixture.base.store.CreateExecution(t.Context(),
		deps.sessionID, session.CreateExecutionInput{
			ID:                uuid.NewSHA1(run.ID, []byte(childWorkflowIdentity)),
			RootTurnID:        deps.parentTurnID,
			ParentExecutionID: &fixture.input.ExecutionID, AgentRunID: &run.ID,
			WorkflowVersion:  agentWorkflowName,
			Task:             conversation.Child.Task,
			WorkingDirectory: deps.executor.Workspace(), Context: encoded,
		})
	require.NoError(t, err)
	dispatch := newWorkflowDispatch(t.Context(), runtime,
		fixture.engine.options.GenerationID, nil)
	backend, err := fixture.storage.WorkerBackend(t.Context(),
		fixture.engine.options.GenerationID)
	require.NoError(t, err)
	require.NoError(t, runtime.bindWorkflowRunner(backend, dispatch,
		fixture.engine, deps.sessionID))

	return fixture, workflowRoundInput{Execution: workflowExecutionInput{
		SessionID: deps.sessionID, ExecutionID: execution.ID, Context: encoded,
		MaxRounds: prepared.remainingRounds,
	}}, run.ID
}

func TestNativeChildMessageBeforeActorRestoration(t *testing.T) {
	t.Parallel()
	fixture, input, runID := coldChildMessageFixture(t)
	runtime := fixture.base.runtime
	requestID := uuid.New()
	request := MessageRequest{To: &runID, Message: "continue interrupted child"}
	for range 2 {
		result, err := runtime.RunMessage(t.Context(), request, requestID, nil)
		require.NoError(t, err)
		assert.True(t, result.Queued)
	}
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, requestID, page.Items[0].RequestID)
	assert.Equal(t, fixture.base.workspace, page.Items[0].WorkingDirectory)
	assert.Contains(t, page.Items[0].Message, request.Message)
	actor, err := runtime.nativeWorkflowRunner().dispatch.child.resolve(
		t.Context(), input)
	require.NoError(t, err)
	count, _ := actor.actor.run.inbox.arrival()
	assert.Equal(t, 1, count)
}

func TestNativeChildMessageCannotMissActorHydration(t *testing.T) {
	t.Parallel()
	fixture, input, runID := coldChildMessageFixture(t)
	runtime := fixture.base.runtime
	held := &heldChildInboxRead{
		Store: fixture.base.store, executionID: input.Execution.ExecutionID,
		read: make(chan struct{}), release: make(chan struct{}),
	}
	release := sync.OnceFunc(func() { close(held.release) })
	t.Cleanup(release)
	runtime.store = held
	callbacks := runtime.nativeWorkflowRunner().dispatch.child
	hydrated := runChildIngressTask(t, func() error {
		_, err := callbacks.resolve(t.Context(), input)

		return err
	})
	select {
	case <-held.read:
	case <-time.After(workflowTestResultTimeout):
		t.Fatal("child actor did not reach inbox hydration")
	}
	sent := runChildIngressTask(t, func() error {
		_, err := runtime.RunMessage(t.Context(), MessageRequest{
			To: &runID, Message: "message during actor hydration",
		}, uuid.New(), nil)

		return err
	})
	assert.Never(t, func() bool { return len(sent) != 0 },
		50*time.Millisecond, 5*time.Millisecond)
	release()
	require.NoError(t, <-hydrated)
	require.NoError(t, <-sent)
	actor, err := callbacks.resolve(t.Context(), input)
	require.NoError(t, err)
	count, _ := actor.actor.run.inbox.arrival()
	assert.Equal(t, 1, count)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}

func TestNativeChildMessageLiveArrivalAndRetryAfterDirectoryHop(t *testing.T) {
	t.Parallel()
	fixture, input, runID := coldChildMessageFixture(t)
	runtime := fixture.base.runtime
	actor, err := runtime.nativeWorkflowRunner().dispatch.child.resolve(
		t.Context(), input)
	require.NoError(t, err)
	inbox := actor.actor.run.inbox
	_, arrived := inbox.arrival()
	requestID := uuid.New()
	request := MessageRequest{To: &runID, Message: "continue live child"}
	_, err = runtime.RunMessage(t.Context(), request, requestID, nil)
	require.NoError(t, err)
	select {
	case <-arrived:
	default:
		t.Fatal("live child message did not signal its waiter")
	}
	directory := filepath.Join(fixture.base.workspace, "nested")
	require.NoError(t, inbox.workflow.refreshDirectory(directory))
	_, unchanged := inbox.arrival()
	_, err = runtime.RunMessage(t.Context(), request, requestID, nil)
	require.NoError(t, err)
	select {
	case <-unchanged:
		t.Fatal("repeated child message signalled another arrival")
	default:
	}
	_, err = runtime.RunMessage(t.Context(), request, uuid.New(), nil)
	require.NoError(t, err)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, fixture.base.workspace, page.Items[0].WorkingDirectory)
	assert.Equal(t, directory, page.Items[1].WorkingDirectory)
	count, _ := inbox.arrival()
	assert.Equal(t, 2, count)
	request.Message = "changed repeated input"
	_, err = runtime.RunMessage(t.Context(), request, requestID, nil)
	require.ErrorIs(t, err, commerr.ErrConflict)
}

// A message to a paused child resumes it, and the child reads why it was
// paused before the message. Repeating the message queues neither twice.
func TestNativeChildMessageResumesAPausedChild(t *testing.T) {
	t.Parallel()

	const pauseReason = "model call failed: <refused>"

	fixture, input, runID := coldChildMessageFixture(t)
	store := fixture.base.store
	sessionID := input.Execution.SessionID
	executionID := input.Execution.ExecutionID

	paused, err := store.PauseExecution(t.Context(), sessionID, executionID,
		pauseReason)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{executionID}, paused)

	requestID := uuid.New()
	request := MessageRequest{To: &runID, Message: "pick it back up"}

	result, err := fixture.base.runtime.RunMessage(t.Context(), request,
		requestID, nil)
	require.NoError(t, err)
	assert.True(t, result.Queued)

	execution, err := store.GetExecution(t.Context(), sessionID, executionID)
	require.NoError(t, err)
	assert.NotEqual(t, models.ExecutionStatePaused, execution.State)
	assert.Nil(t, execution.PausedAt)

	page, err := store.ListExecutionMessages(t.Context(), sessionID,
		executionID, session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)

	wantNotice, err := renderPausedNotice(pauseReason)
	require.NoError(t, err)
	assert.Equal(t, wantNotice, page.Items[0].Message)
	assert.NotContains(t, wantNotice, pauseReason,
		"the reason is escaped, so it cannot close the notice")

	wantMessage, err := renderAgentMessage(agentMessage{
		From: messageSenderUser, Text: request.Message,
	})
	require.NoError(t, err)
	assert.Equal(t, wantMessage, page.Items[1].Message)
	assert.Equal(t, requestID, page.Items[1].RequestID)

	_, err = fixture.base.runtime.RunMessage(t.Context(), request,
		requestID, nil)
	require.NoError(t, err)

	page, err = store.ListExecutionMessages(t.Context(), sessionID,
		executionID, session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
}

// A tool call a Stop interrupted would leave the resumed child waiting on an
// inspection nobody makes. The message that resumes the child settles it as
// an error result, without running the call again.
func TestNativeChildMessageSettlesTheToolCallAPauseInterrupted(t *testing.T) {
	t.Parallel()

	const (
		pauseReason    = "user requested stop"
		interruptedCmd = "sleep 120"
	)

	fixture, input, runID := coldChildMessageFixture(t)
	store := fixture.base.store
	sessionID := input.Execution.SessionID
	executionID := input.Execution.ExecutionID

	arguments, err := json.Marshal(struct {
		Command string `json:"command"`
	}{interruptedCmd})
	require.NoError(t, err)

	phase := workflowToolPhaseInput{
		Tool: workflowToolInput{
			Round: workflowRoundInput{Execution: input.Execution},
			Call: elelem.ToolCall{
				ID: "call-interrupted", Name: toolNameRunCommand,
				Arguments: arguments,
			},
		},
		Phase: workflowToolPhaseHandler,
		State: workflowToolPhaseState{
			LastPhase: workflowToolPhasePre,
			ChildProjection: &childPhaseBuffer{
				ActivityID: uuid.New(),
			},
		},
	}
	encoded, err := json.Marshal(phase)
	require.NoError(t, err)

	claim, err := store.BeginExecutionActivity(t.Context(), sessionID,
		executionID, fixture.engine.options.GenerationID,
		session.ScheduleActivityInput{
			StepID: "tool:0:0:handler", Kind: models.ActivityKindTool,
			Name:         toolPhaseActivityName,
			ReplayPolicy: models.ActivityReplayPolicyReconcile,
			Input:        encoded,
		})
	require.NoError(t, err)
	require.Equal(t, session.ActivityDecisionExecute, claim.Decision)

	_, err = store.PauseExecution(t.Context(), sessionID, executionID,
		pauseReason)
	require.NoError(t, err)
	require.NoError(t, store.InterruptExecutionActivity(t.Context(),
		sessionID, claim.Lease))

	interrupted, err := store.GetExecutionActivity(t.Context(), sessionID,
		claim.Activity.ID)
	require.NoError(t, err)
	require.Equal(t, models.ActivityStateUncertain, interrupted.State)

	_, err = fixture.base.runtime.RunMessage(t.Context(), MessageRequest{
		To: &runID, Message: "carry on",
	}, uuid.New(), nil)
	require.NoError(t, err)

	settled, err := store.GetExecutionActivity(t.Context(), sessionID,
		claim.Activity.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ActivityStateCompleted, settled.State)
	assert.Equal(t, claim.Activity.Attempt, settled.Attempt,
		"the interrupted call is not run again")
	require.NotNil(t, settled.OutputJSON)

	var state workflowToolPhaseState
	require.NoError(t, json.Unmarshal([]byte(*settled.OutputJSON), &state))
	assert.True(t, state.Finished)
	assert.Equal(t, workflowToolPhaseHandler, state.LastPhase)
	assert.Nil(t, state.ChildProjection,
		"the earlier phase's projection is not replayed")
	assert.Nil(t, state.RootProjection)
	assert.Empty(t, state.Outcome.Failure)
	assert.Equal(t, elelem.NewToolErrorResult(
		fmt.Sprintf(interruptedToolResultFormat, pauseReason)),
		state.Outcome.Result)
}

// A message to a running child carries no pause notice.
func TestNativeChildMessageToARunningChildHasNoPauseNotice(t *testing.T) {
	t.Parallel()

	fixture, input, runID := coldChildMessageFixture(t)

	_, err := fixture.base.runtime.RunMessage(t.Context(), MessageRequest{
		To: &runID, Message: "keep going",
	}, uuid.New(), nil)
	require.NoError(t, err)

	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		input.Execution.SessionID, input.Execution.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)

	wantMessage, err := renderAgentMessage(agentMessage{
		From: messageSenderUser, Text: "keep going",
	})
	require.NoError(t, err)
	assert.Equal(t, wantMessage, page.Items[0].Message)
}

func TestNativeChildMessageRefusesUnavailableRecipients(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                       string
		state                      models.ExecutionState
		foreignSession, missingRun bool
		wantErr                    error
	}{
		{
			name: "completed", state: models.ExecutionStateCompleted,
			wantErr: session.ErrAgentRunFinished,
		},
		{
			name: "foreign session", foreignSession: true,
			wantErr: commerr.ErrPermissionDenied,
		},
		{
			name: "missing run", missingRun: true,
			wantErr: session.ErrAgentRunNotFound,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, input, runID := coldChildMessageFixture(t)
			if tc.state != "" {
				repository := repositories.Use(fixture.base.handle.GormDB).Execution
				_, err := repository.WithContext(t.Context()).Where(
					repository.ID.Eq(input.Execution.ExecutionID),
				).UpdateSimple(repository.State.Value(string(tc.state)))
				require.NoError(t, err)
			}
			request := MessageRequest{To: &runID, Message: "leave saved work"}
			if tc.foreignSession {
				id := uuid.New()
				request.SessionID = &id
			}
			if tc.missingRun {
				id := uuid.New()
				request.To = &id
			}
			result, err := fixture.base.runtime.RunMessage(t.Context(), request,
				uuid.New(), nil)
			require.ErrorIs(t, err, tc.wantErr)
			assert.Nil(t, result)
			page, err := fixture.base.store.ListExecutionMessages(t.Context(),
				input.Execution.SessionID, input.Execution.ExecutionID,
				session.ListExecutionInboxOptions{})
			require.NoError(t, err)
			assert.Empty(t, page.Items)
		})
	}
}
