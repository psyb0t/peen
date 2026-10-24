package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/go-workflows/client"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/db/repositories"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func childQuestionPhaseFixture(t *testing.T, timeout string) (
	executionLoopFixture, workflowToolPhaseInput, *workflowDispatch,
) {
	t.Helper()
	arguments, err := json.Marshal(askInput{
		Question: "which path?", Timeout: timeout,
	})
	require.NoError(t, err)
	fixture, round, _ := childModelFixture(t,
		elelemtest.ToolCall("childQuestion", toolNameAsk,
			string(arguments)), elelemtest.Text(runtimeToolFinalText))
	round.Execution.MaxConcurrentTools = 1
	dispatch := newWorkflowDispatch(t.Context(), fixture.base.runtime,
		fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)
	model, err := fixture.engine.modelRound(t.Context(), round)
	require.NoError(t, err)
	round.Execution.Context = model.Output.Context
	input := workflowToolPhaseInput{
		Tool:  workflowToolInput{Round: round, Call: model.Output.Calls[0]},
		Phase: workflowToolPhasePre,
	}

	return fixture, input, dispatch
}

func TestChildWorkflowQuestionPreparesWaitWithoutPublishing(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childQuestionPhaseFixture(t, "1ns")
	prepared, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	require.NotNil(t, prepared.Output.Wait)
	assert.True(t, prepared.Output.Wait.Parent)
	assert.Equal(t, "which path?", prepared.Output.Wait.Question)
	assert.Equal(t, workflowPassiveWaitID(input.Tool), prepared.Output.Wait.ID)
	assert.False(t, prepared.Output.Wait.Deadline.IsZero())
	page, err := fixture.base.store.ListSessionNotices(t.Context(),
		input.Tool.Round.Execution.SessionID, session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

type childQuestionLostAcknowledgement struct {
	*session.Store
	lost bool
}

func (s *childQuestionLostAcknowledgement) CompleteExecutionActivity(
	ctx context.Context,
	sessionID uuid.UUID,
	lease session.ActivityLease,
	input session.CompleteActivityInput,
) (*models.ExecutionActivity, error) {
	activity, err := s.Store.CompleteExecutionActivity(ctx, sessionID,
		lease, input)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "commit child question fixture")
	}

	if input.ChildProjection != nil && input.ChildProjection.Question != nil &&
		!s.lost {
		s.lost = true

		return nil, ctxerrors.Wrap(commerr.ErrConflict,
			"lost question acknowledgement")
	}

	return activity, nil
}

func TestChildWorkflowQuestionCommitsDeliveryAndAnswerWithTheirPhases(
	t *testing.T,
) {
	t.Parallel()
	fixture, input, _ := childQuestionPhaseFixture(t, "1ns")
	prepared, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Phase, input.State = workflowToolPhaseQuestion, prepared.Output
	guard := &childQuestionLostAcknowledgement{Store: fixture.base.store}
	fixture.engine.options.Store = guard
	_, err = fixture.engine.toolPhase(t.Context(), input)
	require.ErrorIs(t, err, commerr.ErrConflict)
	question, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, question.Decision)
	page, err := fixture.base.store.ListSessionNotices(t.Context(),
		input.Tool.Round.Execution.SessionID, session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, prepared.Output.Wait.ID, question.Output.Wait.ID)
	assert.Equal(t, prepared.Output.Wait.Deadline, question.Output.Wait.Deadline)
	state := question.Output
	for _, phase := range []workflowToolPhase{
		workflowToolPhaseHandler, workflowToolPhaseOutcome,
		workflowToolPhasePost, workflowToolPhaseProject,
	} {
		input.Phase, input.State = phase, state
		result, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		state = result.Output
	}
	var answer askOutput
	require.NoError(t, json.Unmarshal([]byte(state.Outcome.Result.Content),
		&answer))
	assert.True(t, answer.TimedOut)
	assert.False(t, answer.Answered)
	execution, err := fixture.base.store.GetExecution(t.Context(),
		input.Tool.Round.Execution.SessionID, input.Tool.Round.Execution.ExecutionID)
	require.NoError(t, err)
	events, err := fixture.base.store.ListAgentRunEvents(t.Context(),
		execution.SessionID, *execution.AgentRunID,
		session.ListAgentRunEventsOptions{})
	require.NoError(t, err)
	counts := make(map[string]int)
	for _, event := range events.Items {
		counts[event.EventType]++
	}
	assert.Equal(t, 1, counts[EventTypeAgentRunQuestion])
	assert.Equal(t, 1, counts[EventTypeAgentRunAnswer])
}

func TestChildWorkflowQuestionResumesHandlerAfterSQLiteReopen(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childQuestionPhaseFixture(t, "1ns")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
	question, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	fixture.engine.options.GenerationID = uuid.New()
	store, replacement := reopenModelProjectionRuntime(t, fixture,
		input.Tool.Round)
	fixture.engine.options.Store = store
	dispatch := newWorkflowDispatch(t.Context(), replacement,
		fixture.engine.options.GenerationID, nil)
	dispatch.bind(&fixture.engine.options)
	input.State = question.Output
	for _, phase := range []workflowToolPhase{
		workflowToolPhaseHandler, workflowToolPhaseOutcome,
		workflowToolPhasePost, workflowToolPhaseProject,
	} {
		input.Phase = phase
		result, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		assert.Equal(t, question.Output.Wait.ID, result.Output.Wait.ID)
		assert.Equal(t, question.Output.Wait.Deadline, result.Output.Wait.Deadline)
		input.State = result.Output
		input.Tool.Round.Execution.Context = result.Output.Outcome.Context
	}
	assert.True(t, input.State.Wait.Answer.TimedOut)
	page, err := store.ListSessionNotices(t.Context(),
		input.Tool.Round.Execution.SessionID, session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}

func TestChildWorkflowQuestionUsesActualSDKPhaseSchedule(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childQuestionPhaseFixture(t, "1ns")
	execution, err := fixture.base.store.GetExecution(t.Context(),
		input.Tool.Round.Execution.SessionID, input.Tool.Round.Execution.ExecutionID)
	require.NoError(t, err)
	fixture.input = input.Tool.Round.Execution
	fixture.input.Context = json.RawMessage(execution.ContextJSON)
	instance := startPhasedWorkflow(t, fixture)
	result, err := client.GetWorkflowResult[workflowRoundResult](t.Context(),
		fixture.api, instance, workflowTestResultTimeout)
	require.NoError(t, err)
	assert.Equal(t, runtimeToolFinalText, result.Text)
	page, err := fixture.base.store.ListExecutionActivities(t.Context(),
		execution.SessionID, execution.ID, session.ListEventsOptions{})
	require.NoError(t, err)
	var phases []workflowToolPhase
	for _, activity := range page.Items {
		if activity.Name != toolPhaseActivityName {
			continue
		}
		var state workflowToolPhaseState
		require.NoError(t, json.Unmarshal([]byte(*activity.OutputJSON), &state))
		phases = append(phases, state.LastPhase)
	}
	assert.Equal(t, []workflowToolPhase{
		workflowToolPhasePre, workflowToolPhaseQuestion,
		workflowToolPhaseHandler, workflowToolPhaseOutcome,
		workflowToolPhasePost, workflowToolPhaseProject,
	}, phases)
}

func TestChildWorkflowQuestionRetriesInterruptedHandler(t *testing.T) {
	t.Parallel()
	fixture, input, dispatch := childQuestionPhaseFixture(t, "1h")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
	question, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	input.Phase, input.State = workflowToolPhaseHandler, question.Output
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	original := fixture.engine.options.ToolPhase
	fixture.engine.options.ToolPhase = func(ctx context.Context,
		input workflowToolPhaseInput,
	) (workflowToolPhaseState, error) {
		cancel()

		return original(ctx, input)
	}
	_, err = fixture.engine.toolPhase(ctx, input)
	require.ErrorIs(t, err, context.Canceled)
	fixture.engine.options.ToolPhase = original
	shared, err := dispatch.child.resolve(t.Context(), input.Tool.Round)
	require.NoError(t, err)
	require.NoError(t, shared.actor.run.inbox.deliver(t.Context(), "continue"))
	answer, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	require.NotNil(t, answer.Output.Wait.Answer)
	assert.True(t, answer.Output.Wait.Answer.Answered)
	assert.Nil(t, question.Output.Wait.Answer)
	assert.Equal(t, pre.Output.Wait.Deadline, answer.Output.Wait.Deadline)
}

func TestChildWorkflowQuestionRejectsForgedPreparedState(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		change func(*workflowToolPhaseInput)
	}{
		{name: "missing wait", change: func(in *workflowToolPhaseInput) {
			in.State.Wait = nil
		}},
		{name: "forged identity", change: func(in *workflowToolPhaseInput) {
			in.State.Wait.ID = uuid.New()
		}},
		{name: "root route", change: func(in *workflowToolPhaseInput) {
			in.State.Wait.Parent = false
		}},
		{name: "different text", change: func(in *workflowToolPhaseInput) {
			in.State.Wait.Question = "changed question"
		}},
		{name: "unsaved failure", change: func(in *workflowToolPhaseInput) {
			in.State.Wait.DeliveryFailure = "forged failure"
		}},
		{name: "skip delivery", change: func(in *workflowToolPhaseInput) {
			in.Phase = workflowToolPhaseHandler
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, input, _ := childQuestionPhaseFixture(t, "1ns")
			pre, err := fixture.engine.toolPhase(t.Context(), input)
			require.NoError(t, err)
			input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
			tc.change(&input)
			_, err = fixture.engine.toolPhase(t.Context(), input)
			require.ErrorIs(t, err, commerr.ErrValidationFailed)
		})
	}
}

func TestChildWorkflowQuestionRetainsDeliveryFailureForHandler(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childQuestionPhaseFixture(t, "1h")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	child, err := fixture.base.store.GetExecution(t.Context(),
		input.Tool.Round.Execution.SessionID, input.Tool.Round.Execution.ExecutionID)
	require.NoError(t, err)
	fixture.base.runtime.eventBus = events.NewBus(events.Options{
		MaxDataBytes: 1,
	})
	input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
	question, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.NotEmpty(t, question.Output.Wait.DeliveryFailure)
	assert.Nil(t, question.Output.ChildProjection.Transcript.Question)
	input.State = question.Output
	for _, phase := range []workflowToolPhase{
		workflowToolPhaseHandler, workflowToolPhaseOutcome,
		workflowToolPhasePost, workflowToolPhaseProject,
	} {
		input.Phase = phase
		result, err := fixture.engine.toolPhase(t.Context(), input)
		require.NoError(t, err)
		input.State = result.Output
	}
	assert.True(t, input.State.Outcome.Result.IsError)
	assert.Equal(t, question.Output.Wait.DeliveryFailure,
		input.State.Wait.DeliveryFailure)
	assert.Nil(t, input.State.Wait.Answer)
	page, err := fixture.base.store.ListSessionNotices(t.Context(),
		child.SessionID, session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestChildWorkflowQuestionAcceptsFailedRootConversation(t *testing.T) {
	t.Parallel()
	fixture, input, _ := childQuestionPhaseFixture(t, "1ns")
	pre, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	child, err := fixture.base.store.GetExecution(t.Context(),
		input.Tool.Round.Execution.SessionID, input.Tool.Round.Execution.ExecutionID)
	require.NoError(t, err)
	repo := repositories.Use(fixture.base.handle.GormDB).Execution
	_, err = repo.WithContext(t.Context()).Where(
		repo.ID.Eq(*child.ParentExecutionID)).
		UpdateSimple(repo.State.Value(string(models.ExecutionStateFailed)))
	require.NoError(t, err)
	input.Phase, input.State = workflowToolPhaseQuestion, pre.Output
	question, err := fixture.engine.toolPhase(t.Context(), input)
	require.NoError(t, err)
	assert.Empty(t, question.Output.Wait.DeliveryFailure)
	require.NotNil(t, question.Output.ChildProjection.Transcript.Question)
	page, err := fixture.base.store.ListSessionNotices(t.Context(),
		child.SessionID, session.ListSessionNoticesOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, models.NoticeDeliveryWake, page.Items[0].Delivery)
}
