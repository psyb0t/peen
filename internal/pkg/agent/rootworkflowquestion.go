package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxerrors/commerr"
)

func (p *preparedTurn) checkpointRootWorkflowQuestion(
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
		return p.prepareRootWorkflowQuestion(ctx, runtime, input, state)
	case workflowToolPhaseHandler:
		return retainRootWorkflowAnswer(state)
	case workflowToolPhaseProject:
		if state.Wait == nil || state.Wait.Answer == nil {
			return nil
		}

		return p.turn.emit(ctx, EventTypeQuestionAnswered, agentAnswerPayload{
			Answered: state.Wait.Answer.Answered, CallID: input.Tool.Call.ID,
		})
	case workflowToolPhaseOutcome, workflowToolPhasePost:
		return nil
	case workflowToolPhaseQuestion:
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"root question cannot deliver to a parent")
	}

	return nil
}

func (p *preparedTurn) prepareRootWorkflowQuestion(
	ctx context.Context,
	runtime *Runtime,
	input workflowToolPhaseInput,
	state *workflowToolPhaseState,
) error {
	if state.Finished || state.Outcome.Failure != "" {
		return nil
	}

	arguments, timeout, valid := runtime.rootWorkflowQuestionArguments(
		input.Tool.Call.Arguments)
	if !valid {
		return nil
	}

	state.Wait = &workflowPassiveWait{
		ID: workflowPassiveWaitID(input.Tool), Question: arguments.Question,
	}
	if timeout > 0 {
		state.Wait.Deadline = time.Now().UTC().Add(timeout)
	}

	return p.turn.emit(ctx, EventTypeQuestionAsked, agentQuestionPayload{
		Question: arguments.Question, CallID: input.Tool.Call.ID,
	})
}

func (r *Runtime) rootWorkflowQuestionArguments(
	encoded json.RawMessage,
) (askInput, time.Duration, bool) {
	var arguments askInput
	if err := decodeToolArguments(encoded, &arguments); err != nil {
		return arguments, 0, false
	}

	if strings.TrimSpace(arguments.Question) == "" {
		return arguments, 0, false
	}

	return arguments, r.askTimeout, true
}

func retainRootWorkflowAnswer(state *workflowToolPhaseState) error {
	if state.Wait == nil || state.Wait.Question == "" ||
		state.Outcome.Result.IsError {
		return nil
	}

	var answer askOutput
	if err := json.Unmarshal([]byte(state.Outcome.Result.Content),
		&answer); err != nil {
		return ctxerrors.Wrap(err, "decode retained root answer")
	}

	if answer.Answered == answer.TimedOut {
		return ctxerrors.Wrap(commerr.ErrInvalidState,
			"retained root answer has no unique outcome")
	}

	// Activity input and output must not share mutable answer state.
	wait := *state.Wait
	wait.Answer = &answer
	state.Wait = &wait

	return nil
}

func validRootWorkflowQuestion(input workflowToolPhaseInput) bool {
	if input.Tool.Call.Name != toolNameAsk ||
		strings.TrimSpace(input.State.Wait.Question) == "" {
		return false
	}

	if input.Phase == workflowToolPhaseHandler &&
		input.State.Wait.Answer != nil {
		return false
	}

	var arguments askInput
	if err := decodeToolArguments(input.Tool.Call.Arguments,
		&arguments); err != nil {
		return false
	}

	if _, err := optionalWaitTimeout(arguments.Timeout); err != nil {
		return false
	}

	answer := input.State.Wait.Answer

	return arguments.Question == input.State.Wait.Question &&
		(answer == nil || answer.Answered != answer.TimedOut)
}

func hasRootWorkflowQuestion(ctx context.Context) bool {
	wait, ok := ctx.Value(workflowPassiveWaitContextKey{}).(workflowPassiveWait)

	return ok && wait.Question != ""
}

func (d *launchAgentDeps) waitRootWorkflowQuestion(
	ctx context.Context,
	question string,
	timeout time.Duration,
) (askOutput, error) {
	value := ctx.Value(workflowPassiveWaitContextKey{})

	wait, ok := value.(workflowPassiveWait)
	if !ok || wait.Question != question {
		return askOutput{}, ctxerrors.Wrap(commerr.ErrConflict,
			"root question differs from its prepared wait")
	}

	waited, err := waitForMessages(ctx, timeout, d.userMessages.arrival,
		nil, noNotices)
	if err != nil {
		return askOutput{}, ctxerrors.Wrap(err, "wait for retained user answer")
	}

	return askOutput{Answered: !waited.TimedOut, TimedOut: waited.TimedOut}, nil
}
