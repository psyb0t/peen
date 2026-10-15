package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
	"github.com/psyb0t/peen/internal/pkg/db/models"
	"github.com/psyb0t/peen/internal/pkg/events"
	"github.com/psyb0t/peen/internal/pkg/session"
)

func (actor *childWorkflowActor) checkpointWorkflowQuestion(
	ctx context.Context,
	runtime *Runtime,
	input workflowToolPhaseInput,
	state *workflowToolPhaseState,
) error {
	if input.Tool.Call.Name != toolNameAsk {
		return nil
	}

	switch input.Phase {
	case workflowToolPhasePre:
		prepareChildWorkflowQuestionWait(runtime, input, state)
	case workflowToolPhaseHandler:
		return retainRootWorkflowAnswer(state)
	case workflowToolPhaseProject:
		if state.Wait != nil && state.Wait.Answer != nil {
			return actor.sink.emit(ctx, EventTypeAgentRunAnswer,
				agentAnswerPayload{
					Answered: state.Wait.Answer.Answered,
					CallID:   input.Tool.Call.ID,
				})
		}
	case workflowToolPhaseQuestion, workflowToolPhaseOutcome,
		workflowToolPhasePost:
		return nil
	}

	return nil
}

func prepareChildWorkflowQuestionWait(
	runtime *Runtime,
	input workflowToolPhaseInput,
	state *workflowToolPhaseState,
) {
	if state.Finished || state.Outcome.Failure != "" {
		return
	}

	arguments, timeout, valid := runtime.rootWorkflowQuestionArguments(
		input.Tool.Call.Arguments)
	if !valid {
		return
	}

	state.Wait = &workflowPassiveWait{
		ID: workflowPassiveWaitID(input.Tool), Parent: true,
		Question: arguments.Question,
	}
	if timeout > 0 {
		state.Wait.Deadline = time.Now().UTC().Add(timeout)
	}
}

func (c *childWorkflowCallbacks) prepareChildQuestionDelivery(
	ctx context.Context,
	actor *childWorkflowActor,
	input workflowToolPhaseInput,
) (workflowToolPhaseState, error) {
	state := input.State
	wait := *state.Wait
	state.Wait = &wait
	state.LastPhase = input.Phase
	state.ChildProjection = actor.sink.phaseBuffer

	text, err := c.validateChildQuestionDelivery(ctx, actor, input)
	if err != nil {
		if isWorkflowToolInterrupted(ctx, err) ||
			(!errors.Is(err, commerr.ErrInvalidState) &&
				!errors.Is(err, commerr.ErrValidationFailed)) {
			return state, ctxerrors.Wrap(err, "prepare parent delivery")
		}

		wait.DeliveryFailure = toolErrorMessage(err)

		return state, nil
	}

	if err := actor.sink.emit(ctx, EventTypeAgentRunQuestion,
		agentQuestionPayload{
			Question: wait.Question, CallID: input.Tool.Call.ID,
		}); err != nil {
		return state, ctxerrors.Wrap(err, "buffer native child question")
	}

	buffer := actor.sink.phaseBuffer
	event := buffer.Transcript.Events[len(buffer.Transcript.Events)-1]
	buffer.Transcript.Question = &session.ExecutionChildQuestion{
		EventID: event.ID, Question: wait.Question, ParentMessage: text,
	}

	return state, nil
}

func (c *childWorkflowCallbacks) validateChildQuestionDelivery(
	ctx context.Context,
	actor *childWorkflowActor,
	input workflowToolPhaseInput,
) (string, error) {
	store, ok := c.runtime.store.(session.ExecutionStorage)
	if !ok {
		return "", ctxerrors.Wrap(commerr.ErrRequiredFieldNotSet,
			"child question execution storage")
	}

	child, err := store.GetExecution(ctx, input.Tool.Round.Execution.SessionID,
		input.Tool.Round.Execution.ExecutionID)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read questioning child")
	}

	if child.ParentExecutionID == nil {
		return "", ctxerrors.Wrap(commerr.ErrConflict,
			"questioning child has no parent execution")
	}

	parent, err := store.GetExecution(ctx, child.SessionID,
		*child.ParentExecutionID)
	if err != nil {
		return "", ctxerrors.Wrap(err, "read questioned parent")
	}

	if parent.Kind == models.ExecutionKindChild &&
		(parent.State == models.ExecutionStateFailed ||
			parent.State == models.ExecutionStateCompleted) {
		return "", ctxerrors.Wrap(commerr.ErrInvalidState,
			"parent agent has ended")
	}

	if parent.Kind == models.ExecutionKindRoot {
		if err := c.validateRootChildQuestion(actor.run,
			input.State.Wait.Question); err != nil {
			return "", ctxerrors.Wrap(err, "validate root question notice")
		}
	}

	return renderAgentMessage(agentMessage{
		From: messageSenderAgent, Name: actor.run.Name,
		Run: actor.run.ID.String(), Kind: messageKindQuestion,
		Text: input.State.Wait.Question,
	})
}

func (c *childWorkflowCallbacks) validateRootChildQuestion(
	run *AgentRun,
	question string,
) error {
	data, err := json.Marshal(events.AgentNoticeData{
		RunID: run.ID, AgentName: run.Name, Message: question,
	})
	if err != nil {
		return ctxerrors.Wrap(err, "encode parent question notice")
	}

	if err := c.runtime.eventLimits().Validate(
		fmt.Sprintf(events.AgentQuestionSummaryFormat, run.Name, run.ID),
		data); err != nil {
		return ctxerrors.Wrap(err, "bound parent question notice")
	}

	return nil
}

func parentWorkflowQuestionWait(ctx context.Context) (
	workflowPassiveWait, bool,
) {
	wait, ok := ctx.Value(workflowPassiveWaitContextKey{}).(workflowPassiveWait)

	return wait, ok && wait.Parent
}

func waitForWorkflowParentAnswer(
	ctx context.Context,
	run *AgentRun,
	wait workflowPassiveWait,
	question string,
	timeout time.Duration,
) (askOutput, error) {
	if wait.Question != question {
		return askOutput{}, ctxerrors.Wrap(commerr.ErrConflict,
			"child question differs from its prepared wait")
	}

	if wait.DeliveryFailure != "" {
		return askOutput{}, ctxerrors.Wrap(commerr.ErrInvalidState,
			wait.DeliveryFailure)
	}

	waited, err := waitForMessages(ctx, timeout, run.inbox.arrival, nil,
		noNotices)
	if err != nil {
		return askOutput{}, ctxerrors.Wrap(err,
			"wait for retained parent answer")
	}

	return askOutput{Answered: !waited.TimedOut, TimedOut: waited.TimedOut}, nil
}
