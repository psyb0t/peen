package agent

import (
	"testing"

	"github.com/google/uuid"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/elelem/elelemtest"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildWorkflowFinishCommitsResponseAndReusesDecision(t *testing.T) {
	t.Parallel()
	fixture, round, sink := childModelFixture(t,
		elelemtest.Text("completed child result"))
	model, err := fixture.engine.modelRound(t.Context(), round)
	require.NoError(t, err)
	round.Execution.Context = model.Output.Context
	fixture.engine.options.BeforeFinish = fixture.base.runtime.prepareChildWorkflowFinish
	input := workflowFinishInput{
		Execution: round.Execution,
		Result: workflowFinishResult{
			Result: model.Output,
			Finish: session.FinishExecutionInput{State: models.ExecutionStateCompleted},
		},
	}
	first, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.False(t, first.Output.Continue)
	reused, err := fixture.engine.finishExecution(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, session.ActivityDecisionReuse, reused.Decision)
	run, err := fixture.base.store.GetAgentRun(t.Context(), sink.sessionID,
		sink.run.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateCompleted, run.State)
	assert.Equal(t, model.Output.Text, run.ResponseText)
	page, err := fixture.base.store.ListExecutionMessages(t.Context(),
		round.Execution.SessionID, fixture.input.ExecutionID,
		session.ListExecutionInboxOptions{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Contains(t, page.Items[0].Message, sink.run.ID.String())
	assert.Contains(t, page.Items[0].Message, model.Output.Text)
	assert.Contains(t, run.ResponseMessagesJSON, model.Output.Text)
}

func TestChildWorkflowFinishContinuesForPendingInbox(t *testing.T) {
	t.Parallel()
	fixture, round, sink := childModelFixture(t, elelemtest.Text("child answer"))
	model, err := fixture.engine.modelRound(t.Context(), round)
	require.NoError(t, err)
	round.Execution.Context = model.Output.Context
	_, err = fixture.base.store.AcceptChildExecutionMessage(t.Context(),
		round.Execution.SessionID, round.Execution.ExecutionID,
		session.AcceptExecutionMessageInput{
			RequestID: uuid.New(), WorkingDirectory: fixture.base.workspace,
			Message: "continue after answer", Capacity: 1,
		})
	require.NoError(t, err)
	fixture.engine.options.BeforeFinish = fixture.base.runtime.prepareChildWorkflowFinish
	result, err := fixture.engine.finishExecution(t.Context(), workflowFinishInput{
		Execution: round.Execution,
		Result: workflowFinishResult{
			Result: model.Output, AllowContinuation: true,
			Finish: session.FinishExecutionInput{State: models.ExecutionStateCompleted},
		},
	})
	require.NoError(t, err)
	assert.True(t, result.Output.Continue)
	run, err := fixture.base.store.GetAgentRun(t.Context(), sink.sessionID,
		sink.run.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentRunStateRunning, run.State)
	assert.Nil(t, run.EndedAt)
}

func TestChildWorkflowFinishRefusesMissingRuntime(t *testing.T) {
	t.Parallel()
	var runtime Runtime
	_, err := runtime.prepareChildWorkflowFinish(t.Context(), workflowFinishInput{
		Execution: workflowExecutionInput{Context: []byte(`{}`)},
	})
	require.ErrorIs(t, err, commerr.ErrRequiredFieldNotSet)
}
