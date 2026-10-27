package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

type rootQuestionCommitGuard struct {
	*session.Store
	checks map[workflowToolPhase]int
	lost   workflowToolPhase
	pause  workflowToolPhase
}

func (s *rootQuestionCommitGuard) CompleteExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
	input session.CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	var state workflowToolPhaseState
	if err := json.Unmarshal(input.Output, &state); err != nil {
		return nil, ctxerrors.Wrap(err, "decode question commit guard output")
	}
	kind := EventTypeQuestionAnswered
	if state.LastPhase == workflowToolPhasePre {
		kind = EventTypeQuestionAsked
	}
	page, err := s.ListEvents(ctx, sessionID,
		session.ListEventsOptions{EventTypes: []string{kind}})
	if err != nil {
		return nil, ctxerrors.Wrap(err, "read guarded question history")
	}
	if len(page.Items) != 0 {
		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"question record preceded its phase commit")
	}
	s.checks[state.LastPhase]++
	activity, err := s.Store.CompleteExecutionActivity(ctx, sessionID,
		lease, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "commit guarded question phase")
	}
	if s.lost == state.LastPhase {
		s.lost = ""

		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"question completion acknowledgement lost")
	}
	if s.pause == state.LastPhase {
		s.pause = ""
		if _, err := s.PauseExecution(ctx, sessionID, activity.ExecutionID,
			"retain question checkpoint"); err != nil {
			return nil, ctxerrors.Wrap(err, "pause guarded question")
		}
	}

	return activity, nil
}

func retainRootQuestionPhases(
	t *testing.T,
	fixture executionLoopFixture,
	input workflowToolPhaseInput,
) workflowToolPhaseState {
	t.Helper()
	for _, phase := range []workflowToolPhase{
		workflowToolPhaseHandler, workflowToolPhaseOutcome,
		workflowToolPhasePost, workflowToolPhaseProject,
	} {
		input.Phase = phase
		result, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		input.State = result.Output
		advanceWorkflowToolPhase(&input.Tool, &input.State)
	}

	return input.State
}

func rootWorkflowQuestionFixture(t *testing.T, timeout string) (
	executionLoopFixture, *rootWorkflowCallbacks, *rootCallbackScope,
	workflowToolPhaseInput,
) {
	t.Helper()
	arguments, err := json.Marshal(askInput{
		Question: "Which workspace change should continue?", Timeout: timeout,
	})
	require.NoError(t, err)
	fixture, callbacks, shared := rootModelProjectionFixture(t,
		elelemtest.ToolCall("savedQuestion", toolNameAsk, string(arguments)))
	round := workflowRoundInput{Execution: fixture.input}
	model, err := fixture.engine.modelRound(t.Context(), round)
	require.NoError(t, err)
	round.Execution.Context = model.Output.Context

	return fixture, callbacks, shared, workflowToolPhaseInput{
		Tool:  workflowToolInput{Round: round, Call: model.Output.Calls[0]},
		Phase: workflowToolPhasePre,
	}
}

func TestRootWorkflowQuestionCommitsBeforeWait(t *testing.T) {
	t.Parallel()
	fixture, _, _, input := rootWorkflowQuestionFixture(t, "1ns")
	guard := &rootQuestionCommitGuard{
		Store: fixture.base.store, checks: make(map[workflowToolPhase]int),
	}
	fixture.engine.options.Store = guard
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	page, err := fixture.base.store.ListEvents(t.Context(),
		fixture.input.SessionID, session.ListEventsOptions{
			EventTypes: []string{EventTypeQuestionAsked},
		})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	require.NotNil(t, pre.Output.Wait)
	assert.NotEmpty(t, pre.Output.Wait.Question)
	input.State = pre.Output
	state := retainRootQuestionPhases(t, fixture, input)
	require.NotNil(t, state.Wait.Answer)
	assert.True(t, state.Wait.Answer.TimedOut)
	page, err = fixture.base.store.ListEvents(t.Context(),
		fixture.input.SessionID, session.ListEventsOptions{
			EventTypes: []string{EventTypeQuestionAsked, EventTypeQuestionAnswered},
		})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, EventTypeQuestionAsked, page.Items[0].EventType)
	assert.Equal(t, EventTypeQuestionAnswered, page.Items[1].EventType)
	assert.Equal(t, 1, guard.checks[workflowToolPhasePre])
	assert.Equal(t, 1, guard.checks[workflowToolPhaseProject])
}

func TestRootWorkflowQuestionReusesLostHandlerAcknowledgement(t *testing.T) {
	t.Parallel()
	fixture, _, shared, input := rootWorkflowQuestionFixture(t, "1h")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	require.NoError(t, shared.prepared.userMessages.enqueue(t.Context(),
		TurnRequest{RequestID: uuid.New(), Message: "Continue the saved change."}))
	input.State, input.Phase = pre.Output, workflowToolPhaseHandler
	guard := &rootQuestionCommitGuard{
		Store: fixture.base.store, checks: make(map[workflowToolPhase]int),
		lost: workflowToolPhaseHandler,
	}
	fixture.engine.options.Store = guard
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	reused, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	require.NotNil(t, reused.Output.Wait.Answer)
	assert.True(t, reused.Output.Wait.Answer.Answered)
	assert.Equal(t, 1, guard.checks[workflowToolPhaseHandler])
	page, err := fixture.base.store.ListEvents(t.Context(),
		fixture.input.SessionID, session.ListEventsOptions{
			EventTypes: []string{EventTypeQuestionAsked},
		})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}

func TestRootWorkflowQuestionKeepsCappedDeadline(t *testing.T) {
	t.Parallel()
	fixture, _, _, input := rootWorkflowQuestionFixture(t, "1h")
	fixture.base.runtime.askTimeout = time.Nanosecond
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	fixture.base.runtime.askTimeout = time.Hour
	input.State = pre.Output
	state := retainRootQuestionPhases(t, fixture, input)
	assert.Equal(t, pre.Output.Wait.Deadline, state.Wait.Deadline)
	require.NotNil(t, state.Wait.Answer)
	assert.True(t, state.Wait.Answer.TimedOut)
}

func TestRootWorkflowQuestionPauseKeepsOneQuestion(t *testing.T) {
	t.Parallel()
	fixture, _, _, input := rootWorkflowQuestionFixture(t, "1ns")
	guard := &rootQuestionCommitGuard{
		Store: fixture.base.store, checks: make(map[workflowToolPhase]int),
		pause: workflowToolPhasePre,
	}
	fixture.engine.options.Store = guard
	paused, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionPaused, paused.Decision)
	_, err = fixture.base.store.ResumeExecution(t.Context(),
		fixture.input.SessionID, fixture.input.ExecutionID)
	require.NoError(t, err)
	reused, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	assert.Equal(t, paused.Output, reused.Output)
	input.State = reused.Output
	state := retainRootQuestionPhases(t, fixture, input)
	require.NotNil(t, state.Wait.Answer)
	assert.True(t, state.Wait.Answer.TimedOut)
	assert.Equal(t, 1, guard.checks[workflowToolPhasePre])
}

func TestRootWorkflowQuestionRejectsChangedCommittedQuestion(t *testing.T) {
	t.Parallel()
	fixture, callbacks, _, input := rootWorkflowQuestionFixture(t, "1ns")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	changed := pre.Output
	wait := *changed.Wait
	wait.Question = "A changed question."
	changed.Wait = &wait
	require.ErrorIs(t, callbacks.afterToolPhase(t.Context(), input, changed),
		commerr.ErrConflict)
}

func TestRootWorkflowQuestionResumesInterruptedHandlerAfterSQLiteReopen(
	t *testing.T,
) {
	t.Parallel()
	fixture, _, _, input := rootWorkflowQuestionFixture(t, "1h")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.State, input.Phase = pre.Output, workflowToolPhaseHandler
	fixture.engine.options.ToolPhase = func(context.Context,
		workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		return input.State, context.Canceled
	}
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.ErrorIs(t, err, context.Canceled)
	fixture.engine.options.GenerationID = uuid.New()
	store, replacement := reopenModelProjectionRuntime(t, fixture,
		input.Tool.Round)
	fixture.engine.options.Store = store
	callbacks := newRootWorkflowCallbacks(replacement,
		fixture.engine.options.GenerationID, nil)
	callbacks.bind(&fixture.engine.options)
	shared, err := callbacks.resolve(t.Context(), input.Tool.Round.Execution)
	require.NoError(t, err)
	require.NoError(t, shared.prepared.userMessages.enqueue(t.Context(),
		TurnRequest{RequestID: uuid.New(), Message: "Continue after recovery."}))
	state := retainRootQuestionPhases(t, fixture, input)
	assert.Equal(t, pre.Output.Wait.ID, state.Wait.ID)
	assert.Equal(t, pre.Output.Wait.Deadline, state.Wait.Deadline)
	require.NotNil(t, state.Wait.Answer)
	assert.True(t, state.Wait.Answer.Answered)
	page, err := store.ListEvents(t.Context(), fixture.input.SessionID,
		session.ListEventsOptions{EventTypes: []string{
			EventTypeQuestionAsked, EventTypeQuestionAnswered,
		}})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, EventTypeQuestionAsked, page.Items[0].EventType)
	assert.Equal(t, EventTypeQuestionAnswered, page.Items[1].EventType)
	var asked agentQuestionPayload
	require.NoError(t, json.Unmarshal([]byte(page.Items[0].PayloadJSON), &asked))
	assert.Equal(t, input.Tool.Call.ID, asked.CallID)
	var answered agentAnswerPayload
	require.NoError(t, json.Unmarshal([]byte(page.Items[1].PayloadJSON), &answered))
	assert.Equal(t, input.Tool.Call.ID, answered.CallID)
	assert.True(t, answered.Answered)
}

func TestRootWorkflowQuestionArguments(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		arguments string
		cap       time.Duration
		valid     bool
		timeout   time.Duration
	}{
		{name: "unbounded", arguments: `{"question":"Continue?"}`, valid: true},
		{
			name: "deployment cap", arguments: `{"question":"Continue?"}`,
			cap: time.Minute, valid: true, timeout: time.Minute,
		},
		{
			name: "request cap", arguments: `{"question":"Continue?","timeout":"1s"}`,
			cap: time.Minute, valid: true, timeout: time.Second,
		},
		{name: "blank question", arguments: `{"question":"  "}`},
		{name: "unknown", arguments: `{"question":"Continue?","extra":true}`},
		{name: "invalid timeout", arguments: `{"question":"Continue?","timeout":"0s"}`},
		{name: "malformed", arguments: `{"question":false}`},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runtime := &Runtime{askTimeout: tc.cap}
			_, timeout, valid := runtime.rootWorkflowQuestionArguments(
				json.RawMessage(tc.arguments))
			assert.Equal(t, tc.valid, valid)
			assert.Equal(t, tc.timeout, timeout)
		})
	}
}

func TestRootWorkflowQuestionValidatesSavedAnswer(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		question string
		answer   *askOutput
		phase    workflowToolPhase
		wantErr  error
	}{
		{name: "waiting", question: "Continue?", phase: workflowToolPhaseHandler},
		{
			name: "changed", question: "Changed?", phase: workflowToolPhaseHandler,
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "answered", question: "Continue?", phase: workflowToolPhaseOutcome,
			answer: &askOutput{Answered: true},
		},
		{
			name: "timed out", question: "Continue?", phase: workflowToolPhaseOutcome,
			answer: &askOutput{TimedOut: true},
		},
		{
			name: "ambiguous", question: "Continue?", phase: workflowToolPhaseOutcome,
			answer:  &askOutput{Answered: true, TimedOut: true},
			wantErr: commerr.ErrValidationFailed,
		},
		{
			name: "answered before handler", question: "Continue?",
			phase: workflowToolPhaseHandler, answer: &askOutput{Answered: true},
			wantErr: commerr.ErrValidationFailed,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			input := phaseExecutionTestInput(t)
			input.Tool.Call.Name = toolNameAsk
			input.Tool.Call.Arguments = json.RawMessage(`{"question":"Continue?"}`)
			input.Phase, input.State.LastPhase = tc.phase, workflowToolPhasePre
			if tc.phase == workflowToolPhaseOutcome {
				input.State.LastPhase = workflowToolPhaseHandler
			}
			input.State.Wait = &workflowPassiveWait{
				ID: workflowPassiveWaitID(input.Tool), Question: tc.question,
				Answer: tc.answer,
			}
			require.ErrorIs(t, input.validate(), tc.wantErr)
		})
	}
}

func TestRootWorkflowQuestionRetainsAnswerWithoutMutatingInput(t *testing.T) {
	t.Parallel()
	before := &workflowPassiveWait{ID: uuid.New(), Question: "Continue?"}
	state := workflowToolPhaseState{
		Wait: before, Outcome: workflowToolResult{
			Result: elelem.ToolResult{Content: `{"answered":true}`},
		},
	}
	require.NoError(t, retainRootWorkflowAnswer(&state))
	assert.Nil(t, before.Answer)
	require.NotNil(t, state.Wait.Answer)
	assert.True(t, state.Wait.Answer.Answered)
	state.Outcome.Result.Content = `{"answered":false}`
	require.ErrorIs(t, retainRootWorkflowAnswer(&state), commerr.ErrInvalidState)
	state.Outcome.Result.Content = `{`
	var syntax *json.SyntaxError
	require.ErrorAs(t, retainRootWorkflowAnswer(&state), &syntax)
}
